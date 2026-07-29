package server

import (
	"fmt"
	"strings"
)

const (
	modelAPIFormatChatCompletions = "chat_completions"
	modelAPIFormatResponses       = "responses"
	modelAPIFormatClaudeCode      = "claude_code"
	modelAPIFormatGemini          = "gemini"
	modelAPIFormatCustom          = "custom"
)

// normalizeModelAPIFormat keeps the control-plane protocol vocabulary small and
// stable while accepting aliases from the legacy flat ModelProfile contract.
// The runtime receives the canonical value in the immutable profile snapshot.
func normalizeModelAPIFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", modelAPIFormatChatCompletions, "openai_chat_completions", "chat-completion", "chat completion":
		return modelAPIFormatChatCompletions, nil
	case modelAPIFormatResponses, "openai_responses", "openai-responses":
		return modelAPIFormatResponses, nil
	case modelAPIFormatClaudeCode, "claude-code", "claudecode", "anthropic_messages", "anthropic-messages":
		return modelAPIFormatClaudeCode, nil
	case modelAPIFormatGemini:
		return modelAPIFormatGemini, nil
	case modelAPIFormatCustom:
		return modelAPIFormatCustom, nil
	default:
		return "", fmt.Errorf("unsupported api format %q; expected chat_completions, responses, claude_code, gemini or custom", value)
	}
}

func defaultModelAPIPath(format string) string {
	switch format {
	case modelAPIFormatChatCompletions:
		return "/v1/chat/completions"
	case modelAPIFormatResponses:
		return "/v1/responses"
	case modelAPIFormatClaudeCode:
		return "/v1/messages"
	case modelAPIFormatGemini:
		return "/v1beta/models"
	default:
		return ""
	}
}
