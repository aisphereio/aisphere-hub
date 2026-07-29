package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

const modelEndpointProbeTimeout = 15 * time.Second

type modelEndpointProbeResult struct {
	Healthy      bool   `json:"healthy"`
	Reachable    bool   `json:"reachable"`
	HTTPStatus   int    `json:"httpStatus"`
	LatencyMS    int64  `json:"latencyMs"`
	HealthStatus string `json:"healthStatus"`
	Message      string `json:"message"`
	CheckedAt    string `json:"checkedAt"`
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
				"health_status":  result.HealthStatus,
				"last_checked_at": checkedAt,
				"updated_at":      checkedAt,
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
		HealthStatus: "unhealthy",
		CheckedAt:    checkedAt.Format(time.RFC3339Nano),
	}

	requestURL, payload, err := buildModelProbeRequest(endpoint)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	body, err := json.Marshal(payload)
	if err != nil {
		result.Message = "encode probe request: " + err.Error()
		return result
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		result.Message = "build probe request: " + err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	applyModelProbeCredential(req, endpoint)

	started := time.Now()
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Message = "request failed: " + err.Error()
		return result
	}
	defer resp.Body.Close()

	result.Reachable = true
	result.HTTPStatus = resp.StatusCode
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := strings.TrimSpace(string(responseBody))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	result.Message = message

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		result.Healthy = true
		result.HealthStatus = "healthy"
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		result.HealthStatus = "unhealthy"
	default:
		result.HealthStatus = "degraded"
	}
	return result
}

func buildModelProbeRequest(endpoint modelEndpointRowV2) (string, map[string]any, error) {
	baseURL := strings.TrimSpace(endpoint.BaseURL)
	if baseURL == "" {
		return "", nil, errorx.BadRequest("MODEL_ENDPOINT_BASE_URL_EMPTY", "model endpoint base URL is empty")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", nil, errorx.BadRequest("MODEL_ENDPOINT_BASE_URL_INVALID", "model endpoint base URL must be an absolute HTTP(S) URL")
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
			"max_output_tokens": 1,
			"stream":            false,
		}, nil
	case modelAPIFormatClaudeCode:
		return requestURL, map[string]any{
			"model":      modelID,
			"messages":   []map[string]any{{"role": "user", "content": "Reply with OK."}},
			"max_tokens": 1,
			"stream":     false,
		}, nil
	case modelAPIFormatGemini:
		if strings.HasSuffix(strings.TrimRight(requestURL, "/"), "/models") {
			requestURL = strings.TrimRight(requestURL, "/") + "/" + url.PathEscape(modelID) + ":generateContent"
		}
		if key := plaintextModelAPIKey(endpoint.CredentialRef); key != "" {
			u, parseErr := url.Parse(requestURL)
			if parseErr == nil {
				query := u.Query()
				query.Set("key", key)
				u.RawQuery = query.Encode()
				requestURL = u.String()
			}
		}
		return requestURL, map[string]any{
			"contents": []map[string]any{{"parts": []map[string]string{{"text": "Reply with OK."}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 1},
		}, nil
	default:
		return requestURL, map[string]any{
			"model":      modelID,
			"messages":   []map[string]any{{"role": "user", "content": "Reply with OK."}},
			"max_tokens": 1,
			"stream":     false,
		}, nil
	}
}

func applyModelProbeCredential(req *http.Request, endpoint modelEndpointRowV2) {
	key := plaintextModelAPIKey(endpoint.CredentialRef)
	if key == "" || endpoint.APIFormat == modelAPIFormatGemini {
		return
	}
	if endpoint.APIFormat == modelAPIFormatClaudeCode || strings.EqualFold(endpoint.Adapter, "anthropic") {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
}

func plaintextModelAPIKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "env://") || strings.HasPrefix(value, "secret://") {
		return ""
	}
	return strings.TrimPrefix(value, "plain://")
}

// Keep the biz import referenced in this file so future probe permission errors
// continue to use the same model-management authorization boundary.
var _ biz.AuthzRepo
var _ = fmt.Sprintf
