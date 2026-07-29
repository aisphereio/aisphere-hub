package server

import (
	"testing"

	"github.com/aisphereio/kernel/authn"
)

func TestNormalizeModelAPIFormat(t *testing.T) {
	tests := map[string]string{
		"":                        modelAPIFormatChatCompletions,
		"openai_chat_completions": modelAPIFormatChatCompletions,
		"responses":               modelAPIFormatResponses,
		"openai-responses":        modelAPIFormatResponses,
		"claude-code":             modelAPIFormatClaudeCode,
		"claudecode":              modelAPIFormatClaudeCode,
		"anthropic_messages":      modelAPIFormatClaudeCode,
		"gemini":                  modelAPIFormatGemini,
		"custom":                  modelAPIFormatCustom,
	}
	for input, want := range tests {
		got, err := normalizeModelAPIFormat(input)
		if err != nil {
			t.Fatalf("normalize %q: %v", input, err)
		}
		if got != want {
			t.Errorf("normalize %q = %q, want %q", input, got, want)
		}
	}
	if _, err := normalizeModelAPIFormat("unsupported"); err == nil {
		t.Fatal("unsupported api format should fail")
	}
}

func TestDefaultModelAPIPath(t *testing.T) {
	tests := map[string]string{
		modelAPIFormatChatCompletions: "/v1/chat/completions",
		modelAPIFormatResponses:       "/v1/responses",
		modelAPIFormatClaudeCode:      "/v1/messages",
		modelAPIFormatGemini:          "/v1beta/models",
		modelAPIFormatCustom:          "",
	}
	for format, want := range tests {
		if got := defaultModelAPIPath(format); got != want {
			t.Errorf("default path for %q = %q, want %q", format, got, want)
		}
	}
}

func TestBuildEndpointRowCanonicalizesClaudeCode(t *testing.T) {
	row, err := buildEndpointRow(endpointWriteRequest{
		ModelID:         "model-1",
		DisplayName:     "Claude Code Gateway",
		APIFormat:       "claudecode",
		BaseURL:         "https://models.example.test/",
		ProviderModelID: "claude-sonnet-4",
	}, authn.Principal{OrgID: "org-1"})
	if err != nil {
		t.Fatalf("build endpoint: %v", err)
	}
	if row.APIFormat != modelAPIFormatClaudeCode || row.Adapter != "anthropic" || row.APIPath != "/v1/messages" {
		t.Fatalf("unexpected claude endpoint: format=%q adapter=%q path=%q", row.APIFormat, row.Adapter, row.APIPath)
	}
}
