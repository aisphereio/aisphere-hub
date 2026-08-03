package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"gorm.io/gorm"
)

func (h *agentHTTPHandler) listEndpoint(ctx khttp.Context) error {
	query := strings.TrimSpace(ctx.Query().Get("q"))
	pageSize := positiveInt(ctx.Query().Get("pageSize"), 50)
	if pageSize > 200 {
		pageSize = 200
	}
	offset := positiveInt(ctx.Query().Get("offset"), 0)
	out, err := h.withAgentAuthn(ctx, "aisphere.hub.agent.v1.ListAgents", nil, func(c context.Context, _ any) (any, error) {
		principal, err := agentPrincipal(c)
		if err != nil {
			return nil, err
		}
		db := h.db(c).Where("deleted_at IS NULL")
		if query != "" {
			like := "%" + query + "%"
			db = db.Where("agent_id ILIKE ? OR display_name ILIKE ? OR description ILIKE ?", like, like, like)
		}
		var rows []agentRow
		if err := db.Order("updated_at DESC, agent_id ASC").Offset(offset).Limit(pageSize + 1).Find(&rows).Error; err != nil {
			return nil, agentDBErr(err)
		}
		hasMore := len(rows) > pageSize
		if hasMore {
			rows = rows[:pageSize]
		}
		items := make([]agentRow, 0, len(rows))
		for _, row := range rows {
			if row.Scope == "system" || row.OwnerID == principal.SubjectID {
				row.OwnerSubject = row.OwnerType + ":" + row.OwnerID
				items = append(items, row)
				continue
			}
			if err := h.requirePermission(c, principal, "agent", row.AgentID, "view"); err == nil {
				row.OwnerSubject = row.OwnerType + ":" + row.OwnerID
				items = append(items, row)
			}
		}
		next := ""
		if hasMore {
			next = strconv.Itoa(offset + pageSize)
		}
		return map[string]any{"items": items, "nextPageToken": next}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *agentHTTPHandler) createEndpoint(ctx khttp.Context) error {
	var req agentWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return agentDecodeErr(err)
	}
	req.ID = strings.TrimSpace(req.ID)
	if !agentIDRE.MatchString(req.ID) {
		return errAgentInvalidID
	}
	out, err := h.withAgentAuthn(ctx, "aisphere.hub.agent.v1.CreateAgent", &req, func(c context.Context, _ any) (any, error) {
		principal, err := agentPrincipal(c)
		if err != nil {
			return nil, err
		}
		if err := h.allowCreate(c, principal); err != nil {
			return nil, err
		}
		definition, projection, err := normalizeAgentDefinition(req.Definition)
		if err != nil {
			return nil, err
		}
		if err := h.validateToolBindings(c, principal, projection); err != nil {
			return nil, err
		}
		version := strings.TrimSpace(req.Version)
		if version == "" {
			version = "v1"
		}
		sha, revision := definitionDigest(definition)
		now := time.Now().UTC()
		row := agentRow{
			AgentID: req.ID, DisplayName: strings.TrimSpace(req.DisplayName),
			Description: strings.TrimSpace(req.Description), Status: normalizeAgentStatus(req.Status),
			Scope: normalizeAgentScope(req.Scope), OwnerType: principal.SubjectType,
			OwnerID: principal.SubjectID, OwnerName: principal.Name, OrgID: principal.OrgID,
			ProjectID:     strings.TrimSpace(req.ProjectID),
			LatestVersion: version, LabelsJSON: labelsJSON(req.Labels), ObjectRef: "agent:" + req.ID,
			CreatedAt: now, UpdatedAt: now,
		}
		versionRow := agentVersionRow{
			AgentID: req.ID, Version: version, Revision: revision, SHA256: sha,
			Author: principal.SubjectID, CommitMsg: firstNonEmpty(strings.TrimSpace(req.CommitMsg), "create agent"),
			DefinitionJSON: definition, CreatedAt: now,
		}
		if err := h.db(c).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			return tx.Create(&versionRow).Error
		}); err != nil {
			return nil, agentDBErr(err)
		}
		_, relErr := h.authz.WriteRelationships(c, biz.AuthzRelationship{
			Resource: biz.AuthzObjectRef{Type: "agent", ID: req.ID},
			Relation: "owner", Subject: agentSubject(principal),
		})
		if relErr != nil {
			_ = h.db(c).Model(&agentRow{}).Where("agent_id = ?", req.ID).Update("deleted_at", time.Now().UTC()).Error
			return nil, errAgentAuthzUnavailable
		}
		created, err := h.loadAgent(c, req.ID, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"agent": created, "object": created.ObjectRef, "latestVersion": created.LatestVersion}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, out)
}

func (h *agentHTTPHandler) getEndpoint(ctx khttp.Context) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	version := strings.TrimSpace(ctx.Query().Get("version"))
	out, err := h.withAgentAuthn(ctx, "aisphere.hub.agent.v1.GetAgent", nil, func(c context.Context, _ any) (any, error) {
		principal, err := agentPrincipal(c)
		if err != nil {
			return nil, err
		}
		if err := h.requirePermission(c, principal, "agent", id, "view"); err != nil {
			return nil, err
		}
		agent, err := h.loadAgent(c, id, version)
		if err != nil {
			return nil, err
		}
		return map[string]any{"agent": agent, "object": agent.ObjectRef, "latestVersion": agent.LatestVersion}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *agentHTTPHandler) updateEndpoint(ctx khttp.Context) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	var req agentWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return agentDecodeErr(err)
	}
	out, err := h.withAgentAuthn(ctx, "aisphere.hub.agent.v1.UpdateAgent", &req, func(c context.Context, _ any) (any, error) {
		principal, err := agentPrincipal(c)
		if err != nil {
			return nil, err
		}
		if err := h.requirePermission(c, principal, "agent", id, "edit"); err != nil {
			return nil, err
		}
		existing, err := h.loadAgent(c, id, "")
		if err != nil {
			return nil, err
		}
		definition, projection, err := normalizeAgentDefinition(req.Definition)
		if err != nil {
			return nil, err
		}
		if err := h.validateToolBindings(c, principal, projection); err != nil {
			return nil, err
		}
		version := strings.TrimSpace(req.Version)
		if version == "" {
			version = nextAgentVersion(existing.LatestVersion)
		}
		sha, revision := definitionDigest(definition)
		now := time.Now().UTC()
		versionRow := agentVersionRow{
			AgentID: id, Version: version, Revision: revision, SHA256: sha,
			Author: principal.SubjectID, CommitMsg: firstNonEmpty(strings.TrimSpace(req.CommitMsg), "update agent"),
			DefinitionJSON: definition, CreatedAt: now,
		}
		updates := map[string]any{
			"display_name": strings.TrimSpace(req.DisplayName), "description": strings.TrimSpace(req.Description),
			"status": normalizeAgentStatus(req.Status), "scope": normalizeAgentScope(req.Scope),
			"labels_json": labelsJSON(req.Labels), "latest_version": version, "updated_at": now,
		}
		if err := h.db(c).Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&agentRow{}).Where("agent_id = ? AND deleted_at IS NULL", id).Updates(updates)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
			return tx.Create(&versionRow).Error
		}); err != nil {
			return nil, agentDBErr(err)
		}
		updated, err := h.loadAgent(c, id, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"agent": updated, "object": updated.ObjectRef, "latestVersion": updated.LatestVersion}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *agentHTTPHandler) deleteEndpoint(ctx khttp.Context) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	out, err := h.withAgentAuthn(ctx, "aisphere.hub.agent.v1.DeleteAgent", nil, func(c context.Context, _ any) (any, error) {
		principal, err := agentPrincipal(c)
		if err != nil {
			return nil, err
		}
		if err := h.requirePermission(c, principal, "agent", id, "manage"); err != nil {
			return nil, err
		}
		agent, err := h.loadAgent(c, id, "")
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		res := h.db(c).Model(&agentRow{}).Where("agent_id = ? AND deleted_at IS NULL", id).Updates(map[string]any{"deleted_at": now, "updated_at": now})
		if res.Error != nil {
			return nil, agentDBErr(res.Error)
		}
		_, _ = h.authz.DeleteRelationships(c, biz.AuthzRelationshipFilter{ResourceType: "agent", ResourceID: id})
		return map[string]any{"agent": agent}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *agentHTTPHandler) planRunEndpoint(ctx khttp.Context) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	var req agentRunRequest
	if err := ctx.Bind(&req); err != nil {
		return agentDecodeErr(err)
	}
	out, err := h.withAgentAuthn(ctx, "aisphere.hub.agent.v1.PlanAgentRun", &req, func(c context.Context, _ any) (any, error) {
		principal, err := agentPrincipal(c)
		if err != nil {
			return nil, err
		}
		if err := h.requirePermission(c, principal, "agent", id, "execute"); err != nil {
			return nil, err
		}
		agent, err := h.loadAgent(c, id, req.Version)
		if err != nil {
			return nil, err
		}
		version, err := h.selectedVersion(agent, req.Version)
		if err != nil {
			return nil, err
		}
		plan, _, err := h.buildRunPlan(c, principal, agent, version, req)
		return plan, err
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *agentHTTPHandler) resolveEndpoint(ctx khttp.Context) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	var req agentRunRequest
	if err := ctx.Bind(&req); err != nil {
		return agentDecodeErr(err)
	}
	out, err := h.withAgentAuthn(ctx, "aisphere.hub.agent.v1.ResolveAgent", &req, func(c context.Context, _ any) (any, error) {
		principal, err := agentPrincipal(c)
		if err != nil {
			return nil, err
		}
		if err := h.requirePermission(c, principal, "agent", id, "execute"); err != nil {
			return nil, err
		}
		agent, err := h.loadAgent(c, id, req.Version)
		if err != nil {
			return nil, err
		}
		if agent.Status == "disabled" {
			return nil, errAgentDisabled
		}
		version, err := h.selectedVersion(agent, req.Version)
		if err != nil {
			return nil, err
		}
		plan, tools, err := h.buildRunPlan(c, principal, agent, version, req)
		if err != nil {
			return nil, err
		}
		approved := toSet(req.ApprovedTools)
		allowedTools := make([]resolvedAgentTool, 0, len(tools))
		for _, tool := range tools {
			switch tool.Binding.ApprovalMode {
			case agentApprovalAlways:
				allowedTools = append(allowedTools, tool)
			case agentApprovalPerRun:
				if !req.ApprovalConfirmed {
					return nil, errAgentRunApprovalRequired
				}
				if _, ok := approved[tool.Binding.Name]; ok {
					allowedTools = append(allowedTools, tool)
				} else if tool.Binding.Required {
					return nil, errorx.Conflict("AGENT_REQUIRED_TOOL_DENIED", "required tool was not approved: "+tool.Binding.Name)
				}
			}
		}
		resolvedDefinition, err := replaceResolvedTools(version.Definition, allowedTools)
		if err != nil {
			return nil, errorx.Internal("AGENT_RESOLVE_FAILED", "failed to build resolved definition", errorx.WithCause(err))
		}
		runtimeID := firstNonEmpty(strings.TrimSpace(req.RuntimeID), "runtime-unspecified")
		sessionID := firstNonEmpty(strings.TrimSpace(req.SessionID), "session-"+strconv.FormatInt(time.Now().UTC().UnixNano(), 36))
		generatedAt := time.Now().UTC()
		snapshotSeed := strings.Join([]string{id, version.Version, runtimeID, sessionID, strings.Join(req.ApprovedTools, ","), generatedAt.Format(time.RFC3339Nano)}, "|")
		snapshotHash := sha256.Sum256([]byte(snapshotSeed))
		toolSnapshots := make([]map[string]any, 0, len(allowedTools))
		for _, tool := range allowedTools {
			toolSnapshots = append(toolSnapshots, tool.Snapshot)
		}
		return map[string]any{
			"snapshotId": hex.EncodeToString(snapshotHash[:16]),
			"runtimeId":  runtimeID, "sessionId": sessionID,
			"agentId": id, "agentVersion": version.Version, "agentRevision": version.Revision,
			"generatedAt": generatedAt, "policy": "principal_passthrough_iam_enforced",
			"definition": json.RawMessage(resolvedDefinition), "tools": toolSnapshots,
			"skills":        plan["skills"],
			"authorization": plan,
		}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

var (
	errAgentUnauthenticated     = errorx.Unauthorized("UNAUTHENTICATED", "authentication required")
	errAgentForbidden           = errorx.Forbidden("AGENT_PERMISSION_DENIED", "permission denied")
	errAgentAuthzUnavailable    = errorx.New("AGENT_AUTHZ_UNAVAILABLE", errorx.WithHTTPStatus(http.StatusServiceUnavailable), errorx.WithMessage("IAM authorization service is unavailable"))
	errAgentZoneRequired        = errorx.BadRequest("AGENT_ZONE_REQUIRED", "authenticated principal has no zone")
	errAgentInvalidID           = errorx.BadRequest("AGENT_INVALID_ID", "agent id must match [A-Za-z0-9][A-Za-z0-9_.-]{0,127}")
	errAgentDefinitionInvalid   = errorx.BadRequest("AGENT_DEFINITION_INVALID", "agent definition is required")
	errAgentNotFound            = errorx.NotFound("AGENT_NOT_FOUND", "agent not found")
	errAgentVersionNotFound     = errorx.NotFound("AGENT_VERSION_NOT_FOUND", "agent version not found")
	errAgentAlreadyExists       = errorx.Conflict("AGENT_ALREADY_EXISTS", "agent already exists")
	errAgentDisabled            = errorx.Conflict("AGENT_DISABLED", "agent is disabled")
	errAgentRunApprovalRequired = errorx.Conflict("AGENT_RUN_APPROVAL_REQUIRED", "human approval is required before resolving this agent run")
)

func agentDecodeErr(err error) error {
	return errorx.BadRequest("AGENT_INVALID_ARGUMENT", err.Error(), errorx.WithCause(err))
}

func agentDBErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errAgentNotFound
	case strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(err.Error(), "23505"):
		return errAgentAlreadyExists
	default:
		return errorx.Internal("AGENT_INTERNAL", "agent operation failed", errorx.WithCause(err))
	}
}
