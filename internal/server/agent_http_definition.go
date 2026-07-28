package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/kernel/errorx"
)

func normalizeAgentStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "disabled", "disable":
		return "disabled"
	default:
		return "active"
	}
}

func normalizeAgentScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "system":
		return "system"
	case "private":
		return "private"
	default:
		return "project"
	}
}

func normalizeApprovalMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case agentApprovalAlways:
		return agentApprovalAlways
	case agentApprovalDisabled:
		return agentApprovalDisabled
	case agentApprovalPerRun, "prompt", "ask":
		return agentApprovalPerRun
	default:
		return ""
	}
}

func normalizeAgentDefinition(raw json.RawMessage) (json.RawMessage, agentDefinitionProjection, error) {
	if len(raw) == 0 {
		return nil, agentDefinitionProjection{}, errAgentDefinitionInvalid
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_DEFINITION_INVALID", "definition must be valid JSON", errorx.WithCause(err))
	}
	entryPoint, _ := doc["entryPoint"].(string)
	if strings.TrimSpace(entryPoint) == "" {
		return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_DEFINITION_INVALID", "definition.entryPoint is required")
	}
	files, ok := doc["files"].(map[string]any)
	if !ok || len(files) == 0 {
		return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_DEFINITION_INVALID", "definition.files must contain at least one file")
	}
	if _, ok := files[entryPoint]; !ok {
		return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_DEFINITION_INVALID", "definition.entryPoint must reference a file in definition.files")
	}
	if tools, ok := doc["tools"].([]any); ok {
		seen := map[string]struct{}{}
		for _, value := range tools {
			binding, ok := value.(map[string]any)
			if !ok {
				return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_DEFINITION_INVALID", "definition.tools entries must be objects")
			}
			name, _ := binding["name"].(string)
			name = strings.TrimSpace(name)
			if !agentIDRE.MatchString(name) {
				return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_TOOL_INVALID", "definition.tools contains an invalid tool name")
			}
			if _, exists := seen[name]; exists {
				return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_TOOL_DUPLICATE", "definition.tools contains duplicate tool "+name)
			}
			seen[name] = struct{}{}
			if mode, exists := binding["approvalMode"]; exists {
				modeText, _ := mode.(string)
				normalized := normalizeApprovalMode(modeText)
				if normalized == "" {
					return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_APPROVAL_MODE_INVALID", "approvalMode must be always, per_run, or disabled")
				}
				binding["approvalMode"] = normalized
			}
		}
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		return nil, agentDefinitionProjection{}, errorx.Internal("AGENT_DEFINITION_ENCODE_FAILED", "failed to encode agent definition", errorx.WithCause(err))
	}
	var projection agentDefinitionProjection
	if err := json.Unmarshal(canonical, &projection); err != nil {
		return nil, agentDefinitionProjection{}, errorx.BadRequest("AGENT_DEFINITION_INVALID", err.Error(), errorx.WithCause(err))
	}
	return canonical, projection, nil
}

func definitionDigest(definition json.RawMessage) (sha, revision string) {
	sum := sha256.Sum256(definition)
	sha = hex.EncodeToString(sum[:])
	return sha, sha[:16]
}

func nextAgentVersion(latest string) string {
	latest = strings.TrimSpace(latest)
	if latest == "" {
		return "v1"
	}
	prefix := ""
	body := latest
	if strings.HasPrefix(body, "v") {
		prefix = "v"
		body = strings.TrimPrefix(body, "v")
	}
	parts := strings.Split(body, ".")
	if len(parts) == 3 {
		patch, err := strconv.Atoi(strings.SplitN(parts[2], "-", 2)[0])
		if err == nil {
			return fmt.Sprintf("%s%s.%s.%d", prefix, parts[0], parts[1], patch+1)
		}
	}
	n, err := strconv.Atoi(body)
	if err == nil {
		return fmt.Sprintf("%s%d", prefix, n+1)
	}
	return "v" + strconv.FormatInt(time.Now().UTC().Unix(), 10)
}

func labelsJSON(labels map[string]string) json.RawMessage {
	if labels == nil {
		return json.RawMessage(`{}`)
	}
	out, err := json.Marshal(labels)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}
