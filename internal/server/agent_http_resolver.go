package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"gorm.io/gorm"
)

func (h *agentHTTPHandler) loadAgent(ctx context.Context, id, version string) (*agentRow, error) {
	var row agentRow
	if err := h.db(ctx).Where("agent_id = ? AND deleted_at IS NULL", id).First(&row).Error; err != nil {
		return nil, agentDBErr(err)
	}
	var versions []agentVersionRow
	query := h.db(ctx).Where("agent_id = ?", id).Order("created_at ASC")
	if strings.TrimSpace(version) != "" {
		query = query.Where("version = ?", strings.TrimSpace(version))
	}
	if err := query.Find(&versions).Error; err != nil {
		return nil, agentDBErr(err)
	}
	if strings.TrimSpace(version) != "" && len(versions) == 0 {
		return nil, errAgentVersionNotFound
	}
	row.Versions = make(map[string]agentVersionRecord, len(versions))
	for _, v := range versions {
		row.Versions[v.Version] = agentVersionRecord{
			Version: v.Version, Revision: v.Revision, SHA256: v.SHA256,
			Author: v.Author, CommitMsg: v.CommitMsg, CreateTime: v.CreatedAt,
			Definition: v.DefinitionJSON,
		}
	}
	row.OwnerSubject = row.OwnerType + ":" + row.OwnerID
	return &row, nil
}

func (h *agentHTTPHandler) selectedVersion(agent *agentRow, requested string) (agentVersionRecord, error) {
	version := strings.TrimSpace(requested)
	if version == "" {
		version = agent.LatestVersion
	}
	v, ok := agent.Versions[version]
	if !ok {
		return agentVersionRecord{}, errAgentVersionNotFound
	}
	return v, nil
}

func (h *agentHTTPHandler) validateToolBindings(ctx context.Context, principal authn.Principal, projection agentDefinitionProjection) error {
	for _, binding := range projection.Tools {
		if normalizeApprovalMode(binding.ApprovalMode) == agentApprovalDisabled {
			continue
		}
		if _, err := h.resolveTool(ctx, principal, binding); err != nil {
			return err
		}
	}
	return nil
}

func (h *agentHTTPHandler) resolveTool(ctx context.Context, principal authn.Principal, binding agentToolBinding) (resolvedAgentTool, error) {
	var tool agentToolCatalogRow
	if err := h.db(ctx).Table("aihub_tools").
		Select("tool_id, status, scope, latest_version").
		Where("tool_id = ? AND deleted_at IS NULL", binding.Name).
		First(&tool).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return resolvedAgentTool{}, errorx.NotFound("AGENT_TOOL_NOT_FOUND", "tool "+binding.Name+" not found")
		}
		return resolvedAgentTool{}, agentDBErr(err)
	}
	if tool.Status == "disabled" {
		return resolvedAgentTool{}, errorx.Conflict("AGENT_TOOL_DISABLED", "tool "+binding.Name+" is disabled")
	}
	if tool.Scope != "system" && tool.Status != "builtin" {
		if err := h.requirePermission(ctx, principal, "tool", binding.Name, "execute"); err != nil {
			return resolvedAgentTool{}, errorx.Forbidden("AGENT_TOOL_EXECUTE_DENIED", "caller cannot bind or execute tool "+binding.Name)
		}
	}
	version := strings.TrimSpace(binding.Version)
	if version == "" {
		version = tool.LatestVersion
	}
	var row agentToolVersionCatalogRow
	if err := h.db(ctx).Table("aihub_tool_versions").
		Select("version, revision, sha256, definition_json").
		Where("tool_id = ? AND version = ?", binding.Name, version).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return resolvedAgentTool{}, errorx.NotFound("AGENT_TOOL_VERSION_NOT_FOUND", "tool version not found: "+binding.Name+"@"+version)
		}
		return resolvedAgentTool{}, agentDBErr(err)
	}
	var definition map[string]any
	if err := json.Unmarshal(row.DefinitionJSON, &definition); err != nil {
		return resolvedAgentTool{}, errorx.Internal("AGENT_TOOL_DEFINITION_INVALID", "stored tool definition is invalid", errorx.WithCause(err))
	}
	capabilities := extractToolCapabilities(definition)
	mode := normalizeApprovalMode(binding.ApprovalMode)
	if mode == "" {
		if len(capabilities) > 0 {
			mode = agentApprovalPerRun
		} else {
			mode = agentApprovalAlways
		}
	}
	binding.ApprovalMode = mode
	snapshot := map[string]any{
		"name": binding.Name, "version": row.Version, "revision": row.Revision,
		"status": tool.Status, "approvalMode": mode,
	}
	for key, value := range definition {
		snapshot[key] = value
	}
	return resolvedAgentTool{
		Binding: binding, Version: row.Version, Revision: row.Revision,
		Status: tool.Status, Scope: tool.Scope, Definition: row.DefinitionJSON,
		Capabilities: capabilities, Snapshot: snapshot,
	}, nil
}

func extractToolCapabilities(definition map[string]any) []string {
	execution, _ := definition["execution"].(map[string]any)
	values, _ := execution["capabilities"].([]any)
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		capability := strings.TrimSpace(fmt.Sprint(value))
		if capability == "" {
			continue
		}
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	sort.Strings(out)
	return out
}

func parseAgentIAMCapability(capability string) (agentIAMPermission, bool) {
	capability = strings.TrimSpace(capability)
	separator := ":"
	if !strings.Contains(capability, separator) && strings.Count(capability, ".") == 1 {
		separator = "."
	}
	parts := strings.Split(capability, separator)
	if len(parts) != 2 {
		return agentIAMPermission{}, false
	}
	resourceType := strings.TrimSpace(parts[0])
	permission := strings.TrimSpace(parts[1])
	known := map[string]bool{
		"skill": true, "tool": true, "agent": true, "model_profile": true,
		"k8s_cluster": true, "k8s_namespace": true, "k8s_sandbox": true,
		"sandbox": true, "runtime_environment": true,
	}
	if !known[resourceType] || permission == "" {
		return agentIAMPermission{}, false
	}
	return agentIAMPermission{ResourceType: resourceType, Permission: permission, Enforcement: "iam_at_resource_service"}, true
}

func approvalForTool(tool resolvedAgentTool, approved map[string]struct{}) agentToolApproval {
	_, isApproved := approved[tool.Binding.Name]
	if tool.Binding.ApprovalMode == agentApprovalAlways {
		isApproved = true
	}
	permissions := make([]agentIAMPermission, 0, len(tool.Capabilities))
	for _, capability := range tool.Capabilities {
		if permission, ok := parseAgentIAMCapability(capability); ok {
			permissions = append(permissions, permission)
		}
	}
	return agentToolApproval{
		Tool: tool.Binding.Name, Version: tool.Version, Required: tool.Binding.Required,
		ApprovalMode: tool.Binding.ApprovalMode, Approved: isApproved,
		Capabilities: tool.Capabilities, Permissions: permissions,
	}
}

func toSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func replaceResolvedTools(definition json.RawMessage, tools []resolvedAgentTool) (json.RawMessage, error) {
	var doc map[string]any
	if err := json.Unmarshal(definition, &doc); err != nil {
		return nil, err
	}
	bindings := make([]agentToolBinding, 0, len(tools))
	for _, tool := range tools {
		binding := tool.Binding
		binding.Version = tool.Version
		bindings = append(bindings, binding)
	}
	doc["tools"] = bindings
	return json.Marshal(doc)
}

func (h *agentHTTPHandler) buildRunPlan(ctx context.Context, principal authn.Principal, agent *agentRow, version agentVersionRecord, request agentRunRequest) (map[string]any, []resolvedAgentTool, error) {
	_, projection, err := normalizeAgentDefinition(version.Definition)
	if err != nil {
		return nil, nil, err
	}
	approved := toSet(request.ApprovedTools)
	resolved := make([]resolvedAgentTool, 0, len(projection.Tools))
	approvals := make([]agentToolApproval, 0, len(projection.Tools))
	requiresApproval := false
	for _, binding := range projection.Tools {
		tool, err := h.resolveTool(ctx, principal, binding)
		if err != nil {
			return nil, nil, err
		}
		if tool.Binding.ApprovalMode == agentApprovalDisabled {
			continue
		}
		approval := approvalForTool(tool, approved)
		if tool.Binding.ApprovalMode == agentApprovalPerRun {
			requiresApproval = true
		}
		approvals = append(approvals, approval)
		resolved = append(resolved, tool)
	}
	plan := map[string]any{
		"agentId":              agent.AgentID,
		"agentVersion":         version.Version,
		"agentRevision":        version.Revision,
		"principalSubject":     principal.SubjectType + ":" + principal.SubjectID,
		"principalPropagation": "trusted_internal_context",
		"iamEnforcement":       "resource_service",
		"requiresApproval":     requiresApproval,
		"approvalConfirmed":    request.ApprovalConfirmed,
		"tools":                approvals,
	}
	return plan, resolved, nil
}
