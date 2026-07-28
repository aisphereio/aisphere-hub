package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

var canonicalEfforts = []string{"none", "minimal", "low", "medium", "high", "max"}

func normalizeReasoningCapability(in reasoningCapability) (reasoningCapability, error) {
	if !in.Supported {
		return reasoningCapability{
			Supported: false,
			Modes: []string{"disabled"},
			DefaultMode: "disabled",
			DefaultEffort: "none",
		}, nil
	}
	if len(in.Modes) == 0 {
		in.Modes = []string{"auto", "enabled", "disabled"}
	}
	if len(in.EffortLevels) == 0 {
		in.EffortLevels = append([]string(nil), canonicalEfforts...)
	}
	in.DefaultMode = normalizeReasoningMode(in.DefaultMode)
	if in.DefaultMode == "inherit" {
		in.DefaultMode = "auto"
	}
	in.DefaultEffort = normalizeEffort(in.DefaultEffort)
	if in.DefaultEffort == "inherit" {
		in.DefaultEffort = "medium"
	}
	if !containsString(in.Modes, in.DefaultMode) {
		return in, fmt.Errorf("default reasoning mode %q is not allowed by model", in.DefaultMode)
	}
	if !containsString(in.EffortLevels, in.DefaultEffort) {
		return in, fmt.Errorf("default reasoning effort %q is not allowed by model", in.DefaultEffort)
	}
	return in, nil
}

func normalizeReasoningPolicy(policy reasoningPolicy, capability reasoningCapability) (reasoningPolicy, error) {
	policy.Mode = normalizeReasoningMode(policy.Mode)
	policy.Effort = normalizeEffort(policy.Effort)
	if policy.Mode == "inherit" {
		policy.Mode = capability.DefaultMode
	}
	if policy.Effort == "inherit" {
		policy.Effort = capability.DefaultEffort
	}
	if !capability.Supported {
		if policy.Mode != "disabled" {
			return policy, fmt.Errorf("model does not support reasoning")
		}
		policy.Effort = "none"
		policy.BudgetTokens = 0
		return policy, nil
	}
	if !containsString(capability.Modes, policy.Mode) {
		return policy, fmt.Errorf("reasoning mode %q is not supported", policy.Mode)
	}
	if policy.Mode == "disabled" {
		policy.Effort = "none"
		policy.BudgetTokens = 0
		return policy, nil
	}
	if !containsString(capability.EffortLevels, policy.Effort) {
		return policy, fmt.Errorf("reasoning effort %q is not supported", policy.Effort)
	}
	if policy.BudgetTokens > 0 && !capability.SupportsBudgetTokens {
		return policy, fmt.Errorf("model does not support reasoning budget tokens")
	}
	return policy, nil
}

func normalizeReasoningMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enabled", "on", "true":
		return "enabled"
	case "disabled", "off", "false", "none":
		return "disabled"
	case "auto", "dynamic":
		return "auto"
	default:
		return "inherit"
	}
}

func normalizeEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	case "xhigh":
		return "max"
	case "":
		return "inherit"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func buildProviderReasoningPatch(policy reasoningPolicy, mapping reasoningMapping) (map[string]any, error) {
	patch := map[string]any{}
	if mapping.Strategy == "none" || mapping.Strategy == "" {
		mergeMap(patch, policy.ProviderOverrides)
		return patch, nil
	}
	if mapping.Strategy != "field_map" {
		return nil, fmt.Errorf("unsupported reasoning mapping strategy %q", mapping.Strategy)
	}
	if mapping.ModeField != "" {
		var value any
		switch policy.Mode {
		case "enabled":
			value = mapping.EnabledValue
		case "disabled":
			value = mapping.DisabledValue
		case "auto":
			value = mapping.AutoValue
		}
		if value != nil {
			setNestedValue(patch, mapping.ModeField, value)
		}
	}
	if policy.Mode != "disabled" && mapping.EffortField != "" && policy.Effort != "none" {
		effort := policy.Effort
		if mapped := mapping.EffortMap[effort]; mapped != "" {
			effort = mapped
		}
		setNestedValue(patch, mapping.EffortField, effort)
	}
	if policy.Mode != "disabled" && mapping.BudgetField != "" && policy.BudgetTokens > 0 {
		setNestedValue(patch, mapping.BudgetField, policy.BudgetTokens)
	}
	mergeMap(patch, policy.ProviderOverrides)
	return patch, nil
}

func setNestedValue(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := root
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i == len(parts)-1 {
			current[part] = value
			return
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
}

func mergeMap(dst, src map[string]any) {
	for key, value := range src {
		if nested, ok := value.(map[string]any); ok {
			current, _ := dst[key].(map[string]any)
			if current == nil {
				current = map[string]any{}
				dst[key] = current
			}
			mergeMap(current, nested)
			continue
		}
		dst[key] = value
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func snapshotDigest(snapshot any) (json.RawMessage, string, error) {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}
