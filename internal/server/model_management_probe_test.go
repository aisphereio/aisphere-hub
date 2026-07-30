package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeModelEndpointChatCompletions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want Bearer test-key", got)
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.2"}]}`))
		case "/v1/chat/completions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if got := body["model"]; got != "glm-5.2" {
				t.Fatalf("model = %v, want glm-5.2", got)
			}
			thinking, _ := body["thinking"].(map[string]any)
			if got := thinking["type"]; got != "disabled" {
				t.Fatalf("thinking.type = %v, want disabled", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := probeModelEndpoint(context.Background(), modelEndpointRowV2{
		BaseURL:              server.URL,
		APIPath:              "/v1/chat/completions",
		APIFormat:            modelAPIFormatChatCompletions,
		ProviderModelID:      "glm-5.2",
		CredentialRef:        "plain://test-key",
		ReasoningMappingJSON: json.RawMessage(`{"strategy":"field_map","modeField":"thinking.type","enabledValue":"enabled","disabledValue":"disabled"}`),
	}, 2*time.Second)

	if !result.Healthy || !result.Reachable || !result.ResponseValid {
		t.Fatalf("probe result = %+v, want healthy, reachable, valid", result)
	}
	if result.ModelListStatus != "matched" {
		t.Fatalf("ModelListStatus = %q, want matched", result.ModelListStatus)
	}
	if result.HTTPStatus != http.StatusOK || result.HealthStatus != "healthy" {
		t.Fatalf("probe result = %+v, want HTTP 200 healthy", result)
	}
}

func TestProbeModelEndpointRejectsInvalidSuccessPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	result := probeModelEndpoint(context.Background(), modelEndpointRowV2{
		BaseURL:         server.URL,
		APIPath:         "/v1/chat/completions",
		APIFormat:       modelAPIFormatChatCompletions,
		ProviderModelID: "qwen",
	}, 2*time.Second)

	if result.Healthy || result.ResponseValid {
		t.Fatalf("probe result = %+v, want invalid response", result)
	}
	if result.HealthStatus != "degraded" || result.ErrorCode != "invalid_response" {
		t.Fatalf("probe result = %+v, want degraded invalid_response", result)
	}
}

func TestProbeModelEndpointResolvesEnvCredential(t *testing.T) {
	t.Setenv("AISPHERE_TEST_MODEL_KEY", "env-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer env-key" {
			t.Fatalf("Authorization = %q, want Bearer env-key", got)
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer server.Close()

	result := probeModelEndpoint(context.Background(), modelEndpointRowV2{
		BaseURL:         server.URL,
		APIPath:         "/v1/chat/completions",
		APIFormat:       modelAPIFormatChatCompletions,
		ProviderModelID: "deepseek",
		CredentialRef:   "env://AISPHERE_TEST_MODEL_KEY",
	}, 2*time.Second)
	if !result.Healthy {
		t.Fatalf("probe result = %+v, want healthy", result)
	}
}

func TestProbeModelEndpointRequiresRuntimeForSecretRef(t *testing.T) {
	result := probeModelEndpoint(context.Background(), modelEndpointRowV2{
		BaseURL:         "https://models.internal",
		APIPath:         "/v1/chat/completions",
		APIFormat:       modelAPIFormatChatCompletions,
		ProviderModelID: "glm",
		CredentialRef:   "secret://models/glm",
	}, 2*time.Second)

	if result.Healthy || result.Reachable {
		t.Fatalf("probe result = %+v, want local credential failure", result)
	}
	if result.ErrorCode != "credential_provider_unavailable" {
		t.Fatalf("ErrorCode = %q, want credential_provider_unavailable", result.ErrorCode)
	}
}

func TestProbeModelEndpointStopsWhenModelIsMissing(t *testing.T) {
	var inferenceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"another-model"}]}`))
			return
		}
		inferenceCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer server.Close()

	result := probeModelEndpoint(context.Background(), modelEndpointRowV2{
		BaseURL:         server.URL,
		APIPath:         "/v1/chat/completions",
		APIFormat:       modelAPIFormatChatCompletions,
		ProviderModelID: "missing-model",
	}, 2*time.Second)

	if result.ErrorCode != "model_not_found" || result.ModelListStatus != "not_found" {
		t.Fatalf("probe result = %+v, want model_not_found", result)
	}
	if got := inferenceCalls.Load(); got != 0 {
		t.Fatalf("inference calls = %d, want 0", got)
	}
}

func TestProbeModelEndpointDoesNotFollowRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	result := probeModelEndpoint(context.Background(), modelEndpointRowV2{
		BaseURL:         origin.URL,
		APIPath:         "/v1/chat/completions",
		APIFormat:       modelAPIFormatChatCompletions,
		ProviderModelID: "glm",
		CredentialRef:   "plain://do-not-forward",
	}, 2*time.Second)

	if result.ErrorCode != "redirect_blocked" || result.HealthStatus != "degraded" {
		t.Fatalf("probe result = %+v, want redirect_blocked", result)
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("redirect target calls = %d, want 0", got)
	}
}
