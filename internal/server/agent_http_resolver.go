package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

func (h *agentHTTPHandler) validateToolBindings(ctx context.Context, principal authn.Principal, projection agentDefinitionProjection) ([]agentCompatibilityWarning, error) {
	if _, err := h.resolveAgentModelSnapshot(ctx, principal, projection.Model); err != nil {
		return nil, err
	}
	skills, err := h.resolveAgentSkillSnapshots(ctx, principal, projection.Skills, projection.SkillSets, false)
	if err != nil {
		return nil, err
	}
	resolvedTools := make([]resolvedAgentTool, 0, len(projection.Tools))
	for _, binding := range projection.Tools {
		if normalizeApprovalMode(binding.ApprovalMode) == agentApprovalDisabled {
			continue
		}
		resolved, err := h.resolveTool(ctx, principal, binding)
		if err != nil {
			return nil, err
		}
		resolvedTools = append(resolvedTools, resolved)
	}
	return h.skillToolCompatibilityWarnings(ctx, skills, resolvedTools), nil
}

// resolveAgentSkillSnapshots pins skills into the immutable run snapshot.
// Built-in skills come from the worker image; catalog skills are validated
// against the Hub catalog (the launcher must hold `skill:{name}#view` so a
// user cannot run an Agent whose definition binds a skill they cannot even
// see) and are pinned by name+version — the download contract
// (artifactRef/digest/URL) lands with the Runtime Skill Fetch work.
func (h *agentHTTPHandler) resolveAgentSkillSnapshots(ctx context.Context, principal authn.Principal, bindings []agentSkillBinding, sets []agentSkillSetBinding, allowArchived bool) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(bindings)+len(sets))
	byName := make(map[string]int, len(bindings)+len(sets))
	for _, binding := range bindings {
		entry, err := h.resolveSkillSnapshotEntry(ctx, principal, binding.Name, binding.Version, binding.Source, "", allowArchived)
		if err != nil {
			return nil, err
		}
		if err := appendResolvedSkillSnapshot(&out, byName, entry); err != nil {
			return nil, err
		}
	}
	for _, set := range sets {
		entries, err := h.resolveAgentSkillSet(ctx, principal, set, allowArchived)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := appendResolvedSkillSnapshot(&out, byName, entry); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func appendResolvedSkillSnapshot(out *[]map[string]any, byName map[string]int, entry map[string]any) error {
	name := strings.TrimSpace(fmt.Sprint(entry["name"]))
	version := strings.TrimSpace(fmt.Sprint(entry["version"]))
	if index, exists := byName[name]; exists {
		existing := (*out)[index]
		existingVersion := strings.TrimSpace(fmt.Sprint(existing["version"]))
		existingRevision := strings.TrimSpace(fmt.Sprint(existing["revision"]))
		revision := strings.TrimSpace(fmt.Sprint(entry["revision"]))
		existingSource := strings.TrimSpace(fmt.Sprint(existing["source"]))
		source := strings.TrimSpace(fmt.Sprint(entry["source"]))
		if existingVersion != version || existingRevision != revision || existingSource != source {
			return errorx.Conflict("AGENT_SKILL_VERSION_CONFLICT",
				fmt.Sprintf("skill %s resolves to multiple immutable releases (%s/%s vs %s/%s)", name, existingVersion, existingRevision, version, revision))
		}
		// Prefer a direct binding over an identical SkillSet-derived binding.
		if _, direct := entry["viaSkillSet"]; direct {
			return nil
		}
		(*out)[index] = entry
		return nil
	}
	byName[name] = len(*out)
	*out = append(*out, entry)
	return nil
}

// resolveSkillSnapshotEntry pins one Skill binding into the immutable run
// snapshot. Built-in skills come from the worker image; catalog skills are
// validated against the Hub catalog (the launcher must hold `skill:{name}#view`
// so a user cannot run an Agent whose definition binds a skill they cannot
// even see), S7/S22 checks (active + published release) apply, and the
// download contract (artifactRef/digest/URL) is attached to the entry.
// fromSet records the owning SkillSet name for provenance when the entry was
// expanded from an Agent SkillSet binding ("" = direct binding).
func (h *agentHTTPHandler) resolveSkillSnapshotEntry(ctx context.Context, principal authn.Principal, name, version, source, fromSet string, allowArchived bool) (map[string]any, error) {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" && (version == "builtin" || strings.HasPrefix(version, "builtin-")) {
		source = "builtin"
	}
	if source == "builtin" {
		return map[string]any{
			"name": name, "version": version, "revision": version,
			"source": "builtin", "object": "aisphere://builtin-skills/" + name,
		}, nil
	}
	// Catalog skill: the launcher must be able to see it first.
	if err := h.requirePermission(ctx, principal, "skill", name, "view"); err != nil {
		return nil, err
	}
	// S7/S22: the catalog binding must still hold at definition-save time
	// and again at resolve time. The skill must exist and be active
	// (disabled/archived skills reject new runs), and the pinned version
	// must be a published release — hand-crafted JSON cannot bypass the
	// UI and pin a version that never shipped.
	if h.skillRepo != nil {
		skill, err := h.skillRepo.GetSkill(ctx, name)
		if err != nil {
			return nil, err
		}
		if err := validateSkillLifecycleForBinding(name, skill.Status, allowArchived); err != nil {
			return nil, err
		}
	}
	if h.releaseResolver == nil {
		return nil, errorx.New(
			errorx.Code("AGENT_SKILL_RELEASE_UNAVAILABLE"),
			errorx.WithHTTPStatus(http.StatusServiceUnavailable),
			errorx.WithMessage("skill release resolver is unavailable"),
		)
	}
	release, err := h.releaseResolver.GetRelease(ctx, name, version)
	if err != nil {
		return nil, err
	}
	entry := catalogSkillSnapshotEntry(name, release)
	if fromSet != "" {
		entry["viaSkillSet"] = fromSet
	}
	// Load-phase download contract: when package signing is configured,
	// attach content digests + a short-lived signed URL so the Runtime
	// can fetch (and verify) the immutable package.
	if h.skillPackages != nil {
		pkg, err := h.skillPackages.BuildSkillPackage(ctx, name, release.Tag)
		if err != nil {
			return nil, err
		}
		downloadURL, err := h.skillPackages.BuildDownloadURL(name, release.Tag, string(principal.SubjectID))
		if err != nil {
			return nil, err
		}
		entry["sha256"] = pkg.SHA256
		entry["md5"] = pkg.MD5
		entry["size"] = pkg.Size
		entry["downloadUrl"] = downloadURL
	}
	return entry, nil
}

func catalogSkillSnapshotEntry(name string, release *biz.SkillRelease) map[string]any {
	return map[string]any{
		"name": name, "version": release.Tag, "revision": release.CommitSHA,
		"source": "catalog", "object": "aihub:skill:" + name,
		"commitSHA": release.CommitSHA, "treeSHA": release.TreeSHA,
		"manifestSHA256": release.ManifestSHA256,
	}
}

func validateSkillLifecycleForBinding(name, status string, allowArchived bool) error {
	status = strings.TrimSpace(status)
	if status == biz.SkillStatusActive || (status == biz.SkillStatusArchived && allowArchived) {
		return nil
	}
	if status == biz.SkillStatusArchived {
		return errorx.New(
			errorx.Code("SKILL_ARCHIVED"),
			errorx.WithHTTPStatus(http.StatusConflict),
			errorx.WithMessage("skill "+name+" is archived and cannot be added to a new Agent revision"),
		)
	}
	return errorx.New(
		errorx.Code("SKILL_DISABLED"),
		errorx.WithHTTPStatus(http.StatusConflict),
		errorx.WithMessage("skill "+name+" is not active"),
	)
}

// resolveAgentSkillSet expands an immutable SkillSet revision. A SkillSet is a
// collection, not a permission bundle: every historical member is resolved
// through the current catalog authorization and lifecycle checks before run.
func (h *agentHTTPHandler) resolveAgentSkillSet(ctx context.Context, principal authn.Principal, binding agentSkillSetBinding, allowArchived bool) ([]map[string]any, error) {
	name := strings.TrimSpace(binding.Name)
	members, err := h.loadAgentSkillSetRevision(ctx, principal, binding)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(members))
	for _, member := range members {
		entry, err := h.resolveSkillSnapshotEntry(ctx, principal, member.SkillName, member.Version, "catalog", name, allowArchived)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(fmt.Sprint(entry["revision"])) != member.CommitSHA ||
			strings.TrimSpace(fmt.Sprint(entry["treeSHA"])) != member.TreeSHA ||
			strings.TrimSpace(fmt.Sprint(entry["manifestSHA256"])) != member.ManifestSHA {
			return nil, errorx.Conflict("AGENT_SKILLSET_REVISION_STALE", fmt.Sprintf("immutable skillset member changed unexpectedly: %s@%s", member.SkillName, member.Version))
		}
		entry["viaSkillSetRevision"] = binding.Revision
		out = append(out, entry)
	}
	return out, nil
}

// resolveAgentModelSnapshot validates the Agent's ModelProfile binding and
// returns the immutable, fully-resolved V2 runtime snapshot. The Agent stores an
// internal Profile UUID, never a provider model name or plaintext credential.
func (h *agentHTTPHandler) resolveAgentModelSnapshot(ctx context.Context, principal authn.Principal, binding *agentModelBinding) (map[string]any, error) {
	if binding == nil || strings.TrimSpace(binding.ProfileID) == "" {
		return nil, nil
	}
	if err := h.requirePermission(ctx, principal, "zone", principal.OrgID, "use_models"); err != nil {
		return nil, errorx.Forbidden("AGENT_MODEL_USE_DENIED", "caller cannot use the selected model profile")
	}
	var profile modelProfileRowV2
	if err := h.db(ctx).
		Where("id = ? AND org_id = ? AND deleted_at IS NULL", strings.TrimSpace(binding.ProfileID), principal.OrgID).
		First(&profile).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorx.NotFound("AGENT_MODEL_PROFILE_NOT_FOUND", "bound model profile not found")
		}
		return nil, agentDBErr(err)
	}
	if profile.Status != "active" {
		return nil, errorx.Conflict("AGENT_MODEL_PROFILE_DISABLED", "bound model profile is disabled")
	}
	revision := binding.Revision
	if revision == 0 {
		revision = profile.LatestRevision
	}
	var row modelProfileRevisionRowV2
	if err := h.db(ctx).
		Where("profile_id = ? AND revision = ?", profile.ID, revision).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorx.NotFound("AGENT_MODEL_REVISION_NOT_FOUND", "bound model profile revision not found")
		}
		return nil, agentDBErr(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(row.SnapshotJSON, &snapshot); err != nil {
		return nil, errorx.Internal("AGENT_MODEL_SNAPSHOT_INVALID", "stored model snapshot is invalid", errorx.WithCause(err))
	}
	snapshot["profileId"] = profile.ID
	snapshot["profileCode"] = profile.Code
	snapshot["logicalName"] = "aisphere://model-profiles/" + profile.Code
	snapshot["revision"] = row.Revision
	snapshot["sha256"] = row.SHA256
	return snapshot, nil
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
		// SpiceDB object ids encode dots ('.' -> '/'); see biz.ToolAuthzObjectID.
		if err := h.requirePermission(ctx, principal, "tool", biz.ToolAuthzObjectID(binding.Name), "execute"); err != nil {
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
	skills, err := h.resolveAgentSkillSnapshots(ctx, principal, projection.Skills, projection.SkillSets, true)
	if err != nil {
		return nil, nil, err
	}
	modelSnapshot, err := h.resolveAgentModelSnapshot(ctx, principal, projection.Model)
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
		"model":                modelSnapshot,
		"tools":                approvals,
		"skills":               skills,
	}
	return plan, resolved, nil
}
