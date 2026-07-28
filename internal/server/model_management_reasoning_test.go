package server

import (
	"reflect"
	"testing"
)

func TestBuildProviderReasoningPatch(t *testing.T) {
	tests := []struct {
		name    string
		policy  reasoningPolicy
		mapping reasoningMapping
		want    map[string]any
	}{
		{
			name:   "qwen vllm hard switch",
			policy: reasoningPolicy{Mode: "disabled", Effort: "none"},
			mapping: reasoningMapping{
				Strategy: "field_map", ModeField: "chat_template_kwargs.enable_thinking",
				EnabledValue: true, DisabledValue: false,
			},
			want: map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}},
		},
		{
			name:   "deepseek thinking and compatible effort",
			policy: reasoningPolicy{Mode: "enabled", Effort: "low"},
			mapping: reasoningMapping{
				Strategy: "field_map", ModeField: "thinking.type", EnabledValue: "enabled", DisabledValue: "disabled",
				EffortField: "reasoning_effort", EffortMap: map[string]string{"low": "high", "medium": "high", "max": "max"},
			},
			want: map[string]any{"thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "high"},
		},
		{
			name:   "glm max effort",
			policy: reasoningPolicy{Mode: "enabled", Effort: "max", ExposeReasoning: true},
			mapping: reasoningMapping{
				Strategy: "field_map", ModeField: "thinking.type", EnabledValue: "enabled", DisabledValue: "disabled",
				EffortField: "reasoning_effort", ResponseField: "reasoning_content", PreserveOnTool: true,
			},
			want: map[string]any{"thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "max"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildProviderReasoningPatch(tt.policy, tt.mapping)
			if err != nil {
				t.Fatalf("build patch: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("patch mismatch\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func TestNormalizeReasoningPolicy(t *testing.T) {
	capability := reasoningCapability{
		Supported: true, Modes: []string{"auto", "enabled", "disabled"},
		EffortLevels: []string{"none", "high", "max"}, DefaultMode: "auto", DefaultEffort: "high",
	}
	got, err := normalizeReasoningPolicy(reasoningPolicy{Mode: "inherit", Effort: "inherit"}, capability)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got.Mode != "auto" || got.Effort != "high" {
		t.Fatalf("unexpected policy: %#v", got)
	}
}
