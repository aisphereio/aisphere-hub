package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

// modelProfileProbeTimeout bounds a single test probe. Model endpoints on
// cold start can take seconds to answer even a 1-token request.
const modelProfileProbeTimeout = 15 * time.Second

// modelProfileProbeMaxErrBody caps how much of an error response body is
// echoed back to the caller (avoid leaking huge HTML error pages).
const modelProfileProbeMaxErrBody = 256

// modelProfileProber implements biz.ModelProfileProber over plain HTTP. It
// builds a minimal request from the profile's api_format and reports
// reachability. Hub never holds plain-text credentials: only env:// secret
// refs are resolved from the hub process environment; every other ref probes
// without auth, so a 401/403 still proves the endpoint answers.
type modelProfileProber struct {
	client *http.Client
	// getenv is indirection for tests; production passes os.Getenv.
	getenv func(string) string
}

// NewModelProfileProber builds the production HTTP prober.
func NewModelProfileProber() biz.ModelProfileProber {
	return &modelProfileProber{
		client: &http.Client{Timeout: modelProfileProbeTimeout},
		getenv: os.Getenv,
	}
}

func (p *modelProfileProber) Probe(ctx context.Context, req biz.ModelProfileProbeRequest) (*biz.ModelProfileTestResult, error) {
	target, body, err := buildProbeRequest(req)
	if err != nil {
		return &biz.ModelProfileTestResult{OK: false, Error: err.Error()}, nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return &biz.ModelProfileTestResult{OK: false, Error: fmt.Sprintf("build probe request: %v", err)}, nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if key := resolveEnvCredential(req.SecretRef, p.getenv); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}

	start := time.Now()
	resp, err := p.client.Do(httpReq)
	latency := int32(time.Since(start).Milliseconds())
	if err != nil {
		return &biz.ModelProfileTestResult{
			OK:        false,
			Error:     fmt.Sprintf("probe failed: %v", err),
			LatencyMs: latency,
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Drain a small slice so the connection can be reused; the body itself
		// (a 1-token completion) is not interesting.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return &biz.ModelProfileTestResult{OK: true, LatencyMs: latency, HTTPStatus: int32(resp.StatusCode)}, nil
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, modelProfileProbeMaxErrBody+1))
	snippet := strings.TrimSpace(string(raw))
	if len(snippet) > modelProfileProbeMaxErrBody {
		snippet = snippet[:modelProfileProbeMaxErrBody] + "…"
	}
	errMsg := fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		errMsg += " (endpoint reachable; credential missing or rejected — hub resolves only env:// refs, verify secret:// refs in the Runtime credential store)"
	}
	if snippet != "" {
		errMsg += ": " + snippet
	}
	return &biz.ModelProfileTestResult{
		OK:         false,
		Error:      errMsg,
		LatencyMs:  latency,
		HTTPStatus: int32(resp.StatusCode),
	}, nil
}

// buildProbeRequest assembles the URL and minimal JSON body for the profile's
// api_format. Logical (non-URL) endpoints such as "vllm-ascend" cannot be
// probed from Hub — only the Runtime knows how to dial them.
func buildProbeRequest(req biz.ModelProfileProbeRequest) (string, []byte, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(req.Endpoint), "/")
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return "", nil, fmt.Errorf("endpoint %q is a logical name, not an http(s) URL; hub can only probe URL endpoints (test logical endpoints from the Runtime)", req.Endpoint)
	}
	if _, err := url.Parse(endpoint); err != nil {
		return "", nil, fmt.Errorf("endpoint %q is not a valid URL: %v", req.Endpoint, err)
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "ping"
	}
	path := strings.TrimSpace(req.UpstreamPath)

	var payload map[string]any
	switch req.APIFormat {
	case "openai_responses":
		if path == "" {
			path = "/v1/responses"
		}
		payload = map[string]any{
			"model":             req.UpstreamModel,
			"input":             prompt,
			"max_output_tokens": 16,
		}
	case "gemini":
		if path == "" {
			path = fmt.Sprintf("/v1beta/models/%s:generateContent", url.PathEscape(req.UpstreamModel))
		}
		payload = map[string]any{
			"contents": []map[string]any{
				{"parts": []map[string]string{{"text": prompt}}},
			},
		}
	default: // openai_chat_completions and unknown formats
		if path == "" {
			path = "/v1/chat/completions"
		}
		payload = map[string]any{
			"model":       req.UpstreamModel,
			"messages":    []map[string]string{{"role": "user", "content": prompt}},
			"max_tokens":  1,
			"stream":      false,
			"temperature": 0,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("marshal probe body: %v", err)
	}
	return endpoint + path, body, nil
}

// resolveEnvCredential resolves env://NAME refs from the hub process
// environment. Every other scheme (secret://, vault://, empty) returns "" —
// the Runtime CredentialProvider owns those, and the probe runs unauthenticated.
func resolveEnvCredential(secretRef string, getenv func(string) string) string {
	ref := strings.TrimSpace(secretRef)
	if !strings.HasPrefix(ref, "env://") {
		return ""
	}
	name := strings.TrimSpace(strings.TrimPrefix(ref, "env://"))
	if name == "" {
		return ""
	}
	return strings.TrimSpace(getenv(name))
}
