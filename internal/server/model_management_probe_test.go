package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeModelEndpointChatCompletions(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want Bearer test-key", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := body["model"]; got != "glm-5.2" {
			t.Fatalf("model = %v, want glm-5.2", got)
		}
		if got := body["stream"]; got != false {
			t.Fatalf("stream = %v, want false", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer server.Close()

	result := probeModelEndpoint(context.Background(), modelEndpointRowV2{
		BaseURL:         server.URL,
		APIPath:         "/v1/chat/completions",
		APIFormat:       modelAPIFormatChatCompletions,
		ProviderModelID: "glm-5.2",
		CredentialRef:   "test-key",
	}, 2*time.Second)

	if !result.Healthy || !result.Reachable {
		t.Fatalf("probe result = %+v, want healthy and reachable", result)
	}
	if result.HTTPStatus != http.StatusOK {
		t.Fatalf("HTTPStatus = %d, want 200", result.HTTPStatus)
	}
	if result.HealthStatus != "healthy" {
		t.Fatalf("HealthStatus = %q, want healthy", result.HealthStatus)
	}
}

func TestProbeModelEndpointUnauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid api key", http.StatusUnauthorized)
	}))
	defer server.Close()

	result := probeModelEndpoint(context.Background(), modelEndpointRowV2{
		BaseURL:         server.URL,
		APIPath:         "/v1/responses",
		APIFormat:       modelAPIFormatResponses,
		ProviderModelID: "gpt-5",
		CredentialRef:   "wrong-key",
	}, 2*time.Second)

	if result.Healthy || !result.Reachable {
		t.Fatalf("probe result = %+v, want unhealthy but reachable", result)
	}
	if result.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("HTTPStatus = %d, want 401", result.HTTPStatus)
	}
	if result.HealthStatus != "unhealthy" {
		t.Fatalf("HealthStatus = %q, want unhealthy", result.HealthStatus)
	}
}
