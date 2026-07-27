package data

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

func newTestProber(t *testing.T, handler http.Handler) (*modelProfileProber, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := &modelProfileProber{
		client: srv.Client(),
		// env:// resolution uses this indirection so tests never touch the
		// real environment.
		getenv: func(name string) string {
			if name == "OPENAI_KEY" {
				return "sk-test-secret"
			}
			return ""
		},
	}
	// httptest.Server uses http://127.0.0.1:port with a sane TLS-less URL.
	_ = srv
	return p, srv
}

func TestModelProfileProber_SuccessReportsLatencyAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path: got %q, want /v1/chat/completions", r.URL.Path)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","choices":[]}`))
	}))
	t.Cleanup(srv.Close)
	p := &modelProfileProber{client: srv.Client(), getenv: func(string) string { return "" }}

	res, err := p.Probe(context.Background(), biz.ModelProfileProbeRequest{
		APIFormat:     "openai_chat_completions",
		Endpoint:      srv.URL,
		UpstreamModel: "gpt-4o",
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected ok, got %+v", res)
	}
	if res.HTTPStatus != 200 {
		t.Errorf("http_status: got %d, want 200", res.HTTPStatus)
	}
	if res.LatencyMs < 0 {
		t.Errorf("latency negative: %d", res.LatencyMs)
	}
}

func TestModelProfileProber_UnauthorizedMeansReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid api key", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := &modelProfileProber{client: srv.Client(), getenv: func(string) string { return "" }}

	res, _ := p.Probe(context.Background(), biz.ModelProfileProbeRequest{
		APIFormat:     "openai_chat_completions",
		Endpoint:      srv.URL,
		UpstreamModel: "gpt-4o",
		SecretRef:     "secret://model/openai-prod",
	})
	if res.OK {
		t.Errorf("expected ok=false on 401")
	}
	if res.HTTPStatus != 401 {
		t.Errorf("http_status: got %d, want 401", res.HTTPStatus)
	}
	if !strings.Contains(res.Error, "reachable") || !strings.Contains(res.Error, "HTTP 401") {
		t.Errorf("error should explain reachability + status, got %q", res.Error)
	}
}

func TestModelProfileProber_LogicalEndpointCannotBeProbed(t *testing.T) {
	p := &modelProfileProber{client: http.DefaultClient, getenv: func(string) string { return "" }}
	res, err := p.Probe(context.Background(), biz.ModelProfileProbeRequest{
		APIFormat:     "openai_chat_completions",
		Endpoint:      "vllm-ascend",
		UpstreamModel: "deepseek-v4",
	})
	if err != nil {
		t.Fatalf("probe returns result not error: %v", err)
	}
	if res.OK {
		t.Error("expected ok=false for a logical endpoint")
	}
	if !strings.Contains(res.Error, "logical name") {
		t.Errorf("error should explain logical endpoints, got %q", res.Error)
	}
}

func TestModelProfileProber_EnvCredentialInjectedAsBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	p := &modelProfileProber{client: srv.Client(), getenv: func(name string) string {
		if name == "OPENAI_KEY" {
			return "sk-test-secret"
		}
		return ""
	}}

	_, err := p.Probe(context.Background(), biz.ModelProfileProbeRequest{
		APIFormat:     "openai_chat_completions",
		Endpoint:      srv.URL,
		UpstreamModel: "gpt-4o",
		SecretRef:     "env://OPENAI_KEY",
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if gotAuth != "Bearer sk-test-secret" {
		t.Errorf("Authorization header: got %q", gotAuth)
	}
}

func TestModelProfileProber_ResponsesApiPathAndBody(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	p := &modelProfileProber{client: srv.Client(), getenv: func(string) string { return "" }}

	_, err := p.Probe(context.Background(), biz.ModelProfileProbeRequest{
		APIFormat:     "openai_responses",
		Endpoint:      srv.URL,
		UpstreamModel: "gpt-4o",
		Prompt:        "hello",
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Errorf("path: got %q, want /v1/responses", gotPath)
	}
	if !strings.Contains(gotBody, `"input":"hello"`) || !strings.Contains(gotBody, `"model":"gpt-4o"`) {
		t.Errorf("responses body mismatch: %s", gotBody)
	}
}

func TestModelProfileProber_GeminiApiPathAndBody(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	p := &modelProfileProber{client: srv.Client(), getenv: func(string) string { return "" }}

	_, err := p.Probe(context.Background(), biz.ModelProfileProbeRequest{
		APIFormat:     "gemini",
		Endpoint:      srv.URL,
		UpstreamModel: "gemini-2.0-flash",
		Prompt:        "hi",
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !strings.Contains(gotPath, "/v1beta/models/gemini-2.0-flash:generateContent") {
		t.Errorf("gemini path: got %q", gotPath)
	}
	if !strings.Contains(gotBody, `"text":"hi"`) {
		t.Errorf("gemini body mismatch: %s", gotBody)
	}
}

func TestResolveEnvCredential(t *testing.T) {
	cases := []struct {
		ref, want string
	}{
		{"env://OPENAI_KEY", "sk-test-secret"},
		{"env://MISSING", ""},
		{"secret://model/openai-prod", ""},
		{"", ""},
		{"env://", ""},
	}
	getenv := func(name string) string {
		if name == "OPENAI_KEY" {
			return "sk-test-secret"
		}
		return ""
	}
	for _, c := range cases {
		if got := resolveEnvCredential(c.ref, getenv); got != c.want {
			t.Errorf("resolveEnvCredential(%q): got %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestBuildProbeRequest_DefaultsToChatCompletions(t *testing.T) {
	target, body, err := buildProbeRequest(biz.ModelProfileProbeRequest{
		APIFormat:     "",
		Endpoint:      "https://api.openai.com",
		UpstreamModel: "gpt-4o",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if target != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("target: got %q", target)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `"messages"`) || !strings.Contains(bodyStr, `"max_tokens":1`) {
		t.Errorf("chat body mismatch: %s", bodyStr)
	}
}
