package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

const (
	modelEndpointProbeTimeout = 15 * time.Second
	modelProbeBodyLimit       = 64 << 10
	modelProbeMessageLimit    = 512
)

type modelEndpointProbeResult struct {
	Healthy         bool   `json:"healthy"`
	Reachable       bool   `json:"reachable"`
	ResponseValid   bool   `json:"responseValid"`
	HTTPStatus      int    `json:"httpStatus"`
	LatencyMS       int64  `json:"latencyMs"`
	HealthStatus    string `json:"healthStatus"`
	ProbeType       string `json:"probeType"`
	ModelListStatus string `json:"modelListStatus"`
	ErrorCode       string `json:"errorCode,omitempty"`
	Message         string `json:"message"`
	CheckedAt       string `json:"checkedAt"`
}

type modelProbeCredential struct {
	APIKey string
	Source string
}

type modelListProbeResult struct {
	Status     string
	Terminal   bool
	Reachable  bool
	HTTPStatus int
	ErrorCode  string
	Message    string
}

func registerModelEndpointProbeHTTP(srv *khttp.Server, resources *data.Resources) {
	if srv == nil || resources == nil || resources.DB == nil {
		return
	}
	h := &modelManagementHTTPHandler{resources: resources, authz: data.NewAuthzRepo(resources)}
	r := srv.Route("/")
	// Segment suffix is used instead of {id}:test because the current HTTP mux
	// greedily consumes custom-verb suffixes after path variables.
	r.Handle(http.MethodPost, "/v1/model-endpoints/{id}/test", h.testModelEndpoint)
}

func (h *modelManagementHTTPHandler) testModelEndpoint(ctx khttp.Context) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.TestEndpoint", nil, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "manage_models"); err != nil {
			return nil, err
		}
		var endpoint modelEndpointRowV2
		if err := h.db(c).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).First(&endpoint).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}

		result := probeModelEndpoint(c, endpoint, modelEndpointProbeTimeout)
		checkedAt, parseErr := time.Parse(time.RFC3339Nano, result.CheckedAt)
		if parseErr != nil {
			checkedAt = time.Now().UTC()
		}
		if err := h.db(c).Model(&modelEndpointRowV2{}).
			Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).
			Updates(map[string]any{
				"health_status":   result.HealthStatus,
				"last_checked_at": checkedAt,
			}).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		return result, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func probeModelEndpoint(ctx context.Context, endpoint modelEndpointRowV2, timeout time.Duration) modelEndpointProbeResult {
	checkedAt := time.Now().UTC()
	result := modelEndpointProbeResult{
		HealthStatus:    "unhealthy",
		ProbeType:       "minimal_inference",
		ModelListStatus: "skipped",
		CheckedAt:       checkedAt.Format(time.RFC3339Nano),
	}

	credential, err := resolveModelProbeCredential(endpoint.CredentialRef)
	if err != nil {
		result.ErrorCode = modelProbeCredentialErrorCode(endpoint.CredentialRef)
		result.Message = sanitizeModelProbeMessage(err.Error())
		return result
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := newModelProbeHTTPClient(timeout)

	if shouldProbeModelList(endpoint) {
		metadata := probeModelList(requestCtx, client, endpoint, credential)
		result.ModelListStatus = metadata.Status
		if metadata.Terminal {
			result.Reachable = metadata.Reachable
			result.HTTPStatus = metadata.HTTPStatus
			result.ErrorCode = metadata.ErrorCode
			result.Message = metadata.Message
			return result
		}
	}

	requestURL, payload, err := buildModelProbeRequest(endpoint)
	if err != nil {
		result.ErrorCode = "invalid_configuration"
		result.Message = sanitizeModelProbeMessage(err.Error())
		return result
	}
	applyReasoningDisabledProbePatch(payload, endpoint)
	body, err := json.Marshal(payload)
	if err != nil {
		result.ErrorCode = "request_encode_failed"
		result.Message = "failed to encode probe request"
		return result
	}

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		result.ErrorCode = "request_build_failed"
		result.Message = "failed to build probe request"
		return result
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	applyModelProbeCredential(req, endpoint, credential.APIKey)

	started := time.Now()
	resp, err := client.Do(req)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		result.ErrorCode = classifyModelProbeTransportError(err)
		result.Message = sanitizeModelProbeMessage(err.Error())
		return result
	}
	defer resp.Body.Close()

	result.Reachable = true
	result.HTTPStatus = resp.StatusCode
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, modelProbeBodyLimit))
	if readErr != nil {
		result.HealthStatus = "degraded"
		result.ErrorCode = "response_read_failed"
		result.Message = "model endpoint returned an unreadable response"
		return result
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.HealthStatus, result.ErrorCode = classifyModelProbeHTTPStatus(resp.StatusCode)
		result.Message = providerProbeErrorMessage(responseBody, resp.StatusCode)
		return result
	}

	if err := validateModelProbeResponse(endpoint.APIFormat, responseBody); err != nil {
		result.HealthStatus = "degraded"
		result.ErrorCode = "invalid_response"
		result.Message = sanitizeModelProbeMessage(err.Error())
		return result
	}

	result.Healthy = true
	result.ResponseValid = true
	result.HealthStatus = "healthy"
	result.Message = "minimal inference succeeded"
	return result
}

func newModelProbeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Never forward model credentials to a redirected host. The 3xx response
			// is returned to the caller and classified as a configuration error.
			return http.ErrUseLastResponse
		},
	}
}

func shouldProbeModelList(endpoint modelEndpointRowV2) bool {
	if strings.EqualFold(endpoint.Adapter, "gemini") || strings.EqualFold(endpoint.Adapter, "anthropic") {
		return false
	}
	switch endpoint.APIFormat {
	case modelAPIFormatGemini, modelAPIFormatClaudeCode:
		return false
	default:
		return true
	}
}

func probeModelList(ctx context.Context, client *http.Client, endpoint modelEndpointRowV2, credential modelProbeCredential) modelListProbeResult {
	requestURL, err := buildModelListProbeURL(endpoint)
	if err != nil {
		return modelListProbeResult{Status: "skipped"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return modelListProbeResult{Status: "skipped"}
	}
	req.Header.Set("Accept", "application/json")
	applyModelProbeCredential(req, endpoint, credential.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return modelListProbeResult{Status: "unavailable"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, modelProbeBodyLimit))

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		return modelListProbeResult{Status: "unsupported", Reachable: true, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return modelListProbeResult{
			Status: "failed", Terminal: true, Reachable: true, HTTPStatus: resp.StatusCode,
			ErrorCode: "authentication_failed", Message: providerProbeErrorMessage(body, resp.StatusCode),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modelListProbeResult{Status: "unavailable", Reachable: true, HTTPStatus: resp.StatusCode}
	}

	matched, parseErr := modelListContains(body, endpoint.ProviderModelID)
	if parseErr != nil {
		return modelListProbeResult{Status: "invalid", Reachable: true, HTTPStatus: resp.StatusCode}
	}
	if !matched {
		return modelListProbeResult{
			Status: "not_found", Terminal: true, Reachable: true, HTTPStatus: resp.StatusCode,
			ErrorCode: "model_not_found", Message: "configured provider model ID was not returned by /v1/models",
		}
	}
	return modelListProbeResult{Status: "matched", Reachable: true, HTTPStatus: resp.StatusCode}
}

func buildModelListProbeURL(endpoint modelEndpointRowV2) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(endpoint.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid base URL")
	}
	if strings.HasSuffix(parsed.Path, "/v1") {
		return baseURL + "/models", nil
	}
	return baseURL + "/v1/models", nil
}

func modelListContains(body []byte, modelID string) (bool, error) {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return false, err
	}
	for _, item := range response.Data {
		if item.ID == modelID {
			return true, nil
		}
	}
	return false, nil
}

func buildModelProbeRequest(endpoint modelEndpointRowV2) (string, map[string]any, error) {
	baseURL := strings.TrimSpace(endpoint.BaseURL)
	if baseURL == "" {
		return "", nil, errorx.BadRequest("MODEL_ENDPOINT_BASE_URL_EMPTY", "model endpoint base URL is empty")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", nil, errorx.BadRequest("MODEL_ENDPOINT_BASE_URL_INVALID", "model endpoint base URL must be an absolute HTTP(S) URL without userinfo or fragment")
	}

	apiPath := strings.TrimSpace(endpoint.APIPath)
	if apiPath == "" {
		apiPath = defaultModelAPIPath(endpoint.APIFormat)
	}
	requestURL := strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(apiPath, "/")
	modelID := strings.TrimSpace(endpoint.ProviderModelID)
	if modelID == "" {
		return "", nil, errorx.BadRequest("MODEL_ENDPOINT_PROVIDER_MODEL_ID_EMPTY", "provider model ID is empty")
	}

	switch endpoint.APIFormat {
	case modelAPIFormatResponses:
		return requestURL, map[string]any{
			"model":             modelID,
			"input":             "Reply with OK.",
			"max_output_tokens": 8,
			"stream":            false,
		}, nil
	case modelAPIFormatClaudeCode:
		return requestURL, map[string]any{
			"model":      modelID,
			"messages":   []map[string]any{{"role": "user", "content": "Reply with OK."}},
			"max_tokens": 8,
			"stream":     false,
		}, nil
	case modelAPIFormatGemini:
		if strings.HasSuffix(strings.TrimRight(requestURL, "/"), "/models") {
			requestURL = strings.TrimRight(requestURL, "/") + "/" + url.PathEscape(modelID) + ":generateContent"
		}
		return requestURL, map[string]any{
			"contents": []map[string]any{{"parts": []map[string]string{{"text": "Reply with OK."}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 8},
		}, nil
	default:
		return requestURL, map[string]any{
			"model":       modelID,
			"messages":    []map[string]any{{"role": "user", "content": "Reply with OK."}},
			"temperature": 0,
			"max_tokens":  8,
			"stream":      false,
		}, nil
	}
}

func applyReasoningDisabledProbePatch(payload map[string]any, endpoint modelEndpointRowV2) {
	var mapping reasoningMapping
	if len(endpoint.ReasoningMappingJSON) == 0 || json.Unmarshal(endpoint.ReasoningMappingJSON, &mapping) != nil {
		return
	}
	patch, err := buildProviderReasoningPatch(reasoningPolicy{Mode: "disabled", Effort: "none"}, mapping)
	if err == nil {
		mergeMap(payload, patch)
	}
}

func resolveModelProbeCredential(value string) (modelProbeCredential, error) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return modelProbeCredential{Source: "none"}, nil
	case strings.HasPrefix(value, "env://"):
		name := strings.TrimSpace(strings.TrimPrefix(value, "env://"))
		if name == "" {
			return modelProbeCredential{}, fmt.Errorf("env credential reference is missing a variable name")
		}
		key, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(key) == "" {
			return modelProbeCredential{}, fmt.Errorf("environment credential %q is not configured", name)
		}
		return modelProbeCredential{APIKey: key, Source: "env"}, nil
	case strings.HasPrefix(value, "secret://"):
		return modelProbeCredential{}, fmt.Errorf("secret credentials must be resolved by the Runtime CredentialProvider")
	case strings.HasPrefix(value, "plain://"):
		key := strings.TrimPrefix(value, "plain://")
		if strings.TrimSpace(key) == "" {
			return modelProbeCredential{}, fmt.Errorf("plain credential is empty")
		}
		return modelProbeCredential{APIKey: key, Source: "plain"}, nil
	default:
		// Backward compatibility for the current PostgreSQL MVP. New callers
		// should use an explicit reference scheme.
		return modelProbeCredential{APIKey: value, Source: "legacy_plain"}, nil
	}
}

func modelProbeCredentialErrorCode(value string) string {
	if strings.HasPrefix(strings.TrimSpace(value), "secret://") {
		return "credential_provider_unavailable"
	}
	return "credential_not_configured"
}

func applyModelProbeCredential(req *http.Request, endpoint modelEndpointRowV2, key string) {
	if key == "" {
		return
	}
	if endpoint.APIFormat == modelAPIFormatGemini {
		query := req.URL.Query()
		query.Set("key", key)
		req.URL.RawQuery = query.Encode()
		return
	}
	if endpoint.APIFormat == modelAPIFormatClaudeCode || strings.EqualFold(endpoint.Adapter, "anthropic") {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
}

func validateModelProbeResponse(apiFormat string, body []byte) error {
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("model endpoint returned non-JSON content")
	}
	switch apiFormat {
	case modelAPIFormatResponses:
		if text, ok := response["output_text"].(string); ok && text != "" {
			return nil
		}
		if output, ok := response["output"].([]any); ok && len(output) > 0 {
			return nil
		}
		return fmt.Errorf("Responses payload is missing output")
	case modelAPIFormatGemini:
		if candidates, ok := response["candidates"].([]any); ok && len(candidates) > 0 {
			return nil
		}
		return fmt.Errorf("Gemini payload is missing candidates")
	case modelAPIFormatClaudeCode:
		if content, ok := response["content"].([]any); ok && len(content) > 0 {
			return nil
		}
		return fmt.Errorf("Anthropic payload is missing content")
	default:
		choices, ok := response["choices"].([]any)
		if !ok || len(choices) == 0 {
			return fmt.Errorf("Chat Completions payload is missing choices")
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			return fmt.Errorf("Chat Completions choice has an invalid shape")
		}
		message, ok := choice["message"].(map[string]any)
		if !ok {
			return fmt.Errorf("Chat Completions choice is missing message")
		}
		if _, exists := message["content"]; exists {
			return nil
		}
		if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
			return nil
		}
		return fmt.Errorf("Chat Completions message is missing content")
	}
}

func classifyModelProbeHTTPStatus(status int) (healthStatus, errorCode string) {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "unhealthy", "authentication_failed"
	case http.StatusNotFound:
		return "unhealthy", "not_found"
	case http.StatusTooManyRequests:
		return "degraded", "rate_limited"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "degraded", "provider_timeout"
	default:
		if status >= 300 && status < 400 {
			return "degraded", "redirect_blocked"
		}
		if status >= 500 {
			return "degraded", "provider_error"
		}
		return "degraded", "invalid_request"
	}
}

func classifyModelProbeTransportError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns_error"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "connection_error"
	}
	return "request_failed"
}

func providerProbeErrorMessage(body []byte, status int) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		if nested, ok := payload["error"].(map[string]any); ok {
			if message, ok := nested["message"].(string); ok && message != "" {
				return sanitizeModelProbeMessage(message)
			}
		}
		if message, ok := payload["message"].(string); ok && message != "" {
			return sanitizeModelProbeMessage(message)
		}
	}
	if text := sanitizeModelProbeMessage(string(body)); text != "" {
		return text
	}
	return http.StatusText(status)
}

func sanitizeModelProbeMessage(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) > modelProbeMessageLimit {
		value = value[:modelProbeMessageLimit]
	}
	return value
}
