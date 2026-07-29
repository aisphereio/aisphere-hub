package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"gorm.io/gorm"
)

func (h *modelManagementHTTPHandler) listModels(ctx khttp.Context) error {
	return h.listResource(ctx, "aisphere.hub.model.v2.ListModels", func(c context.Context, orgID string, limit, offset int) (any, error) {
		query := h.db(c).Where("deleted_at IS NULL AND org_id = ?", orgID)
		if q := strings.TrimSpace(ctx.Query().Get("q")); q != "" {
			like := "%" + q + "%"
			query = query.Where("code ILIKE ? OR display_name ILIKE ? OR vendor ILIKE ? OR family ILIKE ?", like, like, like, like)
		}
		var rows []modelRowV2
		if err := query.Order("updated_at DESC").Offset(offset).Limit(limit + 1).Find(&rows).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		items, next := pageRows(rows, limit, offset)
		return map[string]any{"items": items, "nextPageToken": next}, nil
	})
}

func (h *modelManagementHTTPHandler) createModel(ctx khttp.Context) error {
	var req modelWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return errorx.BadRequest("MODEL_REQUEST_INVALID", "invalid model request", errorx.WithCause(err))
	}
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.CreateModel", &req, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "manage_models"); err != nil {
			return nil, err
		}
		row, err := buildModelRow(req, principal)
		if err != nil {
			return nil, err
		}
		if err := h.db(c).Create(&row).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		return map[string]any{"model": row}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, out)
}

func (h *modelManagementHTTPHandler) getModel(ctx khttp.Context) error {
	return h.getResource(ctx, "aisphere.hub.model.v2.GetModel", &modelRowV2{}, "model")
}

func (h *modelManagementHTTPHandler) updateModel(ctx khttp.Context) error {
	var req modelWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return errorx.BadRequest("MODEL_REQUEST_INVALID", "invalid model request", errorx.WithCause(err))
	}
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.UpdateModel", &req, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "manage_models"); err != nil {
			return nil, err
		}
		row, err := buildModelRow(req, principal)
		if err != nil {
			return nil, err
		}
		updates := map[string]any{
			"code": row.Code, "display_name": row.DisplayName, "description": row.Description,
			"status": row.Status, "vendor": row.Vendor, "family": row.Family, "model_type": row.ModelType,
			"capabilities_json": row.CapabilitiesJSON, "reasoning_json": row.ReasoningJSON,
			"provider_config_json": row.ProviderConfigJSON, "updated_at": time.Now().UTC(),
		}
		res := h.db(c).Model(&modelRowV2{}).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).Updates(updates)
		if res.Error != nil || res.RowsAffected == 0 {
			return nil, modelManagementDBErr(firstErr(res.Error, gorm.ErrRecordNotFound))
		}
		var updated modelRowV2
		if err := h.db(c).Where("id = ?", id).First(&updated).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		return map[string]any{"model": updated}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *modelManagementHTTPHandler) deleteModel(ctx khttp.Context) error {
	return h.deleteResource(ctx, "aisphere.hub.model.v2.DeleteModel", &modelRowV2{}, "model")
}

func (h *modelManagementHTTPHandler) listEndpoints(ctx khttp.Context) error {
	return h.listResource(ctx, "aisphere.hub.model.v2.ListEndpoints", func(c context.Context, orgID string, limit, offset int) (any, error) {
		query := h.db(c).Where("deleted_at IS NULL AND org_id = ?", orgID)
		if modelID := strings.TrimSpace(ctx.Query().Get("modelId")); modelID != "" {
			query = query.Where("model_id = ?", modelID)
		}
		var rows []modelEndpointRowV2
		if err := query.Order("updated_at DESC").Offset(offset).Limit(limit + 1).Find(&rows).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		items, next := pageRows(rows, limit, offset)
		return map[string]any{"items": items, "nextPageToken": next}, nil
	})
}

func (h *modelManagementHTTPHandler) createEndpoint(ctx khttp.Context) error {
	var req endpointWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return errorx.BadRequest("MODEL_ENDPOINT_REQUEST_INVALID", "invalid endpoint request", errorx.WithCause(err))
	}
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.CreateEndpoint", &req, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "manage_models"); err != nil {
			return nil, err
		}
		if err := h.ensureModel(c, req.ModelID, principal.OrgID); err != nil {
			return nil, err
		}
		row, err := buildEndpointRow(req, principal)
		if err != nil {
			return nil, err
		}
		if err := h.db(c).Create(&row).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		return map[string]any{"endpoint": row}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, out)
}

func (h *modelManagementHTTPHandler) getEndpoint(ctx khttp.Context) error {
	return h.getResource(ctx, "aisphere.hub.model.v2.GetEndpoint", &modelEndpointRowV2{}, "endpoint")
}

func (h *modelManagementHTTPHandler) updateEndpoint(ctx khttp.Context) error {
	var req endpointWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return errorx.BadRequest("MODEL_ENDPOINT_REQUEST_INVALID", "invalid endpoint request", errorx.WithCause(err))
	}
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.UpdateEndpoint", &req, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "manage_models"); err != nil {
			return nil, err
		}
		if err := h.ensureModel(c, req.ModelID, principal.OrgID); err != nil {
			return nil, err
		}
		row, err := buildEndpointRow(req, principal)
		if err != nil {
			return nil, err
		}
		updates := map[string]any{
			"model_id": row.ModelID, "display_name": row.DisplayName, "description": row.Description,
			"status": row.Status, "adapter": row.Adapter, "api_format": row.APIFormat, "base_url": row.BaseURL,
			"provider_model_id": row.ProviderModelID, "api_path": row.APIPath, "credential_ref": row.CredentialRef,
			"limits_json": row.LimitsJSON, "reasoning_mapping_json": row.ReasoningMappingJSON,
			"request_defaults_json": row.RequestDefaultsJSON, "provider_config_json": row.ProviderConfigJSON,
			"updated_at": time.Now().UTC(),
		}
		res := h.db(c).Model(&modelEndpointRowV2{}).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).Updates(updates)
		if res.Error != nil || res.RowsAffected == 0 {
			return nil, modelManagementDBErr(firstErr(res.Error, gorm.ErrRecordNotFound))
		}
		var updated modelEndpointRowV2
		if err := h.db(c).Where("id = ?", id).First(&updated).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		return map[string]any{"endpoint": updated}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *modelManagementHTTPHandler) deleteEndpoint(ctx khttp.Context) error {
	return h.deleteResource(ctx, "aisphere.hub.model.v2.DeleteEndpoint", &modelEndpointRowV2{}, "endpoint")
}

func (h *modelManagementHTTPHandler) listProfiles(ctx khttp.Context) error {
	return h.listResource(ctx, "aisphere.hub.model.v2.ListProfiles", func(c context.Context, orgID string, limit, offset int) (any, error) {
		query := h.db(c).Where("deleted_at IS NULL AND org_id = ?", orgID)
		if q := strings.TrimSpace(ctx.Query().Get("q")); q != "" {
			like := "%" + q + "%"
			query = query.Where("code ILIKE ? OR display_name ILIKE ?", like, like)
		}
		var rows []modelProfileRowV2
		if err := query.Order("updated_at DESC").Offset(offset).Limit(limit + 1).Find(&rows).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		items, next := pageRows(rows, limit, offset)
		return map[string]any{"items": items, "nextPageToken": next}, nil
	})
}

func (h *modelManagementHTTPHandler) createProfile(ctx khttp.Context) error {
	var req profileWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return errorx.BadRequest("MODEL_PROFILE_REQUEST_INVALID", "invalid profile request", errorx.WithCause(err))
	}
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.CreateProfile", &req, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "manage_models"); err != nil {
			return nil, err
		}
		row, revision, err := h.buildProfile(c, req, principal, "", 1)
		if err != nil {
			return nil, err
		}
		if err := h.db(c).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			return tx.Create(&revision).Error
		}); err != nil {
			return nil, modelManagementDBErr(err)
		}
		return map[string]any{"profile": row, "revision": revision.Revision}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, out)
}

func (h *modelManagementHTTPHandler) getProfile(ctx khttp.Context) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.GetProfile", nil, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "view_models"); err != nil {
			return nil, err
		}
		var row modelProfileRowV2
		if err := h.db(c).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).First(&row).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		var revisions []modelProfileRevisionRowV2
		if err := h.db(c).Where("profile_id = ?", id).Order("revision DESC").Find(&revisions).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		return map[string]any{"profile": row, "revisions": revisions}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *modelManagementHTTPHandler) updateProfile(ctx khttp.Context) error {
	var req profileWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return errorx.BadRequest("MODEL_PROFILE_REQUEST_INVALID", "invalid profile request", errorx.WithCause(err))
	}
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.UpdateProfile", &req, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "manage_models"); err != nil {
			return nil, err
		}
		var existing modelProfileRowV2
		if err := h.db(c).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).First(&existing).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		nextRevision := existing.LatestRevision + 1
		row, revision, err := h.buildProfile(c, req, principal, id, nextRevision)
		if err != nil {
			return nil, err
		}
		updates := map[string]any{
			"code": row.Code, "display_name": row.DisplayName, "description": row.Description,
			"status": row.Status, "endpoint_id": row.EndpointID, "limits_json": row.LimitsJSON,
			"reasoning_policy_json": row.ReasoningPolicyJSON, "default_parameters_json": row.DefaultParametersJSON,
			"allowed_tools_json": row.AllowedToolsJSON, "latest_revision": nextRevision, "updated_at": time.Now().UTC(),
		}
		if err := h.db(c).Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&modelProfileRowV2{}).Where("id = ? AND deleted_at IS NULL", id).Updates(updates)
			if res.Error != nil || res.RowsAffected == 0 {
				return firstErr(res.Error, gorm.ErrRecordNotFound)
			}
			return tx.Create(&revision).Error
		}); err != nil {
			return nil, modelManagementDBErr(err)
		}
		return map[string]any{"profile": row, "revision": nextRevision}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *modelManagementHTTPHandler) deleteProfile(ctx khttp.Context) error {
	return h.deleteResource(ctx, "aisphere.hub.model.v2.DeleteProfile", &modelProfileRowV2{}, "profile")
}

func (h *modelManagementHTTPHandler) resolveProfile(ctx khttp.Context) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	revisionNumber, _ := strconv.ParseInt(strings.TrimSpace(ctx.Query().Get("revision")), 10, 64)
	out, err := h.withAuthn(ctx, "aisphere.hub.model.v2.ResolveProfile", nil, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "view_models"); err != nil {
			return nil, err
		}
		var profile modelProfileRowV2
		if err := h.db(c).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).First(&profile).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		if profile.Status != "active" {
			return nil, errorx.Conflict("MODEL_PROFILE_DISABLED", "model profile is disabled")
		}
		if revisionNumber == 0 {
			revisionNumber = profile.LatestRevision
		}
		var revision modelProfileRevisionRowV2
		if err := h.db(c).Where("profile_id = ? AND revision = ?", id, revisionNumber).First(&revision).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		var snapshot map[string]any
		if err := json.Unmarshal(revision.SnapshotJSON, &snapshot); err != nil {
			return nil, errorx.Internal("MODEL_PROFILE_SNAPSHOT_INVALID", "stored model profile snapshot is invalid", errorx.WithCause(err))
		}
		snapshot["logicalName"] = "aisphere://model-profiles/" + profile.Code
		snapshot["profileId"] = profile.ID
		snapshot["revision"] = revision.Revision
		snapshot["sha256"] = revision.SHA256
		snapshot["generatedAt"] = time.Now().UTC()
		return snapshot, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *modelManagementHTTPHandler) listResource(ctx khttp.Context, operation string, fn func(context.Context, string, int, int) (any, error)) error {
	limit := positiveInt(ctx.Query().Get("pageSize"), 50)
	if limit > 200 {
		limit = 200
	}
	offset := positiveInt(ctx.Query().Get("offset"), 0)
	out, err := h.withAuthn(ctx, operation, nil, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "view_models"); err != nil {
			return nil, err
		}
		return fn(c, principal.OrgID, limit, offset)
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *modelManagementHTTPHandler) getResource(ctx khttp.Context, operation string, model any, key string) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	out, err := h.withAuthn(ctx, operation, nil, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "view_models"); err != nil {
			return nil, err
		}
		if err := h.db(c).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).First(model).Error; err != nil {
			return nil, modelManagementDBErr(err)
		}
		return map[string]any{key: model}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *modelManagementHTTPHandler) deleteResource(ctx khttp.Context, operation string, model any, key string) error {
	id := strings.TrimSpace(ctx.Vars().Get("id"))
	out, err := h.withAuthn(ctx, operation, nil, func(c context.Context, principal authn.Principal) (any, error) {
		if err := h.requireZone(c, principal, "manage_models"); err != nil {
			return nil, err
		}
		var count int64
		switch model.(type) {
		case *modelRowV2:
			if err := h.db(c).Model(&modelEndpointRowV2{}).Where("model_id = ? AND deleted_at IS NULL", id).Count(&count).Error; err != nil {
				return nil, modelManagementDBErr(err)
			}
		case *modelEndpointRowV2:
			if err := h.db(c).Model(&modelProfileRowV2{}).Where("endpoint_id = ? AND deleted_at IS NULL", id).Count(&count).Error; err != nil {
				return nil, modelManagementDBErr(err)
			}
		}
		if count > 0 {
			return nil, errorx.Conflict("MODEL_RESOURCE_IN_USE", "resource is referenced and cannot be deleted")
		}
		res := h.db(c).Model(model).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, principal.OrgID).Update("deleted_at", time.Now().UTC())
		if res.Error != nil || res.RowsAffected == 0 {
			return nil, modelManagementDBErr(firstErr(res.Error, gorm.ErrRecordNotFound))
		}
		return map[string]any{key + "Id": id}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func buildModelRow(req modelWriteRequest, principal authn.Principal) (modelRowV2, error) {
	code := strings.TrimSpace(req.Code)
	if !modelCodeRE.MatchString(code) {
		return modelRowV2{}, errorx.BadRequest("MODEL_CODE_INVALID", "model code must contain lowercase letters, numbers, and hyphens")
	}
	if strings.TrimSpace(req.DisplayName) == "" || strings.TrimSpace(req.Vendor) == "" || strings.TrimSpace(req.ModelType) == "" {
		return modelRowV2{}, errorx.BadRequest("MODEL_REQUIRED_FIELDS_MISSING", "displayName, vendor and modelType are required")
	}
	reasoning, err := normalizeReasoningCapability(req.Reasoning)
	if err != nil {
		return modelRowV2{}, errorx.BadRequest("MODEL_REASONING_INVALID", err.Error())
	}
	now := time.Now().UTC()
	return modelRowV2{
		ID: newModelUUID(), Code: code, DisplayName: strings.TrimSpace(req.DisplayName), Description: strings.TrimSpace(req.Description),
		Status: normalizeStatus(req.Status), Vendor: strings.TrimSpace(req.Vendor), Family: strings.TrimSpace(req.Family), ModelType: strings.TrimSpace(req.ModelType),
		CapabilitiesJSON: rawJSON(req.Capabilities, `{}`), ReasoningJSON: rawJSON(reasoning, `{}`), ProviderConfigJSON: rawJSON(req.ProviderConfig, `{}`),
		OwnerType: principal.SubjectType, OwnerID: principal.SubjectID, OwnerName: principal.Name, OrgID: principal.OrgID, ProjectID: strings.TrimSpace(req.ProjectID),
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func buildEndpointRow(req endpointWriteRequest, principal authn.Principal) (modelEndpointRowV2, error) {
	if strings.TrimSpace(req.ModelID) == "" || strings.TrimSpace(req.DisplayName) == "" || strings.TrimSpace(req.BaseURL) == "" || strings.TrimSpace(req.ProviderModelID) == "" {
		return modelEndpointRowV2{}, errorx.BadRequest("MODEL_ENDPOINT_REQUIRED_FIELDS_MISSING", "modelId, displayName, baseUrl and providerModelId are required")
	}
	apiFormat, err := normalizeModelAPIFormat(req.APIFormat)
	if err != nil {
		return modelEndpointRowV2{}, errorx.BadRequest("MODEL_ENDPOINT_API_FORMAT_INVALID", err.Error())
	}
	if req.ReasoningMapping.Strategy == "" {
		req.ReasoningMapping.Strategy = "none"
	}
	adapter := strings.TrimSpace(req.Adapter)
	if adapter == "" {
		switch apiFormat {
		case modelAPIFormatClaudeCode:
			adapter = "anthropic"
		case modelAPIFormatGemini:
			adapter = "gemini"
		case modelAPIFormatCustom:
			adapter = "custom"
		default:
			adapter = "openai_compatible"
		}
	}
	apiPath := strings.TrimSpace(req.APIPath)
	if apiPath == "" {
		apiPath = defaultModelAPIPath(apiFormat)
	}
	now := time.Now().UTC()
	return modelEndpointRowV2{
		ID: newModelUUID(), ModelID: strings.TrimSpace(req.ModelID), DisplayName: strings.TrimSpace(req.DisplayName), Description: strings.TrimSpace(req.Description),
		Status: normalizeStatus(req.Status), Adapter: adapter, APIFormat: apiFormat,
		BaseURL: strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"), ProviderModelID: strings.TrimSpace(req.ProviderModelID), APIPath: apiPath,
		CredentialRef: strings.TrimSpace(req.CredentialRef), LimitsJSON: rawJSON(req.Limits, `{}`), ReasoningMappingJSON: rawJSON(req.ReasoningMapping, `{}`),
		RequestDefaultsJSON: rawJSON(req.RequestDefaults, `{}`), ProviderConfigJSON: rawJSON(req.ProviderConfig, `{}`), HealthStatus: "unknown",
		OrgID: principal.OrgID, ProjectID: strings.TrimSpace(req.ProjectID), CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (h *modelManagementHTTPHandler) buildProfile(c context.Context, req profileWriteRequest, principal authn.Principal, id string, revisionNumber int64) (modelProfileRowV2, modelProfileRevisionRowV2, error) {
	code := strings.TrimSpace(req.Code)
	if !modelCodeRE.MatchString(code) {
		return modelProfileRowV2{}, modelProfileRevisionRowV2{}, errorx.BadRequest("MODEL_PROFILE_CODE_INVALID", "profile code must contain lowercase letters, numbers, and hyphens")
	}
	var endpoint modelEndpointRowV2
	if err := h.db(c).Where("id = ? AND org_id = ? AND deleted_at IS NULL", req.EndpointID, principal.OrgID).First(&endpoint).Error; err != nil {
		return modelProfileRowV2{}, modelProfileRevisionRowV2{}, modelManagementDBErr(err)
	}
	if endpoint.Status != "active" {
		return modelProfileRowV2{}, modelProfileRevisionRowV2{}, errorx.Conflict("MODEL_ENDPOINT_DISABLED", "selected endpoint is disabled")
	}
	var model modelRowV2
	if err := h.db(c).Where("id = ? AND org_id = ? AND deleted_at IS NULL", endpoint.ModelID, principal.OrgID).First(&model).Error; err != nil {
		return modelProfileRowV2{}, modelProfileRevisionRowV2{}, modelManagementDBErr(err)
	}
	if model.Status != "active" {
		return modelProfileRowV2{}, modelProfileRevisionRowV2{}, errorx.Conflict("MODEL_DISABLED", "selected model is disabled")
	}
	var capability reasoningCapability
	var mapping reasoningMapping
	_ = json.Unmarshal(model.ReasoningJSON, &capability)
	_ = json.Unmarshal(endpoint.ReasoningMappingJSON, &mapping)
	policy, err := normalizeReasoningPolicy(req.ReasoningPolicy, capability)
	if err != nil {
		return modelProfileRowV2{}, modelProfileRevisionRowV2{}, errorx.BadRequest("MODEL_PROFILE_REASONING_INVALID", err.Error())
	}
	providerPatch, err := buildProviderReasoningPatch(policy, mapping)
	if err != nil {
		return modelProfileRowV2{}, modelProfileRevisionRowV2{}, errorx.BadRequest("MODEL_REASONING_MAPPING_INVALID", err.Error())
	}
	if id == "" {
		id = newModelUUID()
	}
	now := time.Now().UTC()
	row := modelProfileRowV2{
		ID: id, Code: code, DisplayName: strings.TrimSpace(req.DisplayName), Description: strings.TrimSpace(req.Description), Status: normalizeStatus(req.Status),
		EndpointID: endpoint.ID, LimitsJSON: rawJSON(req.Limits, `{}`), ReasoningPolicyJSON: rawJSON(policy, `{}`),
		DefaultParametersJSON: rawJSON(req.DefaultParameters, `{}`), AllowedToolsJSON: rawJSON(req.AllowedTools, `[]`), LatestRevision: revisionNumber,
		OwnerType: principal.SubjectType, OwnerID: principal.SubjectID, OwnerName: principal.Name, OrgID: principal.OrgID, ProjectID: strings.TrimSpace(req.ProjectID),
		CreatedAt: now, UpdatedAt: now,
	}
	snapshot := map[string]any{
		"profile":   map[string]any{"id": row.ID, "code": row.Code, "limits": req.Limits, "allowedTools": req.AllowedTools, "defaultParameters": req.DefaultParameters},
		"model":     map[string]any{"id": model.ID, "code": model.Code, "displayName": model.DisplayName, "vendor": model.Vendor, "family": model.Family, "modelType": model.ModelType, "capabilities": json.RawMessage(model.CapabilitiesJSON), "reasoning": capability},
		"endpoint":  map[string]any{"id": endpoint.ID, "adapter": endpoint.Adapter, "apiFormat": endpoint.APIFormat, "baseUrl": endpoint.BaseURL, "providerModelId": endpoint.ProviderModelID, "apiPath": endpoint.APIPath, "credentialRef": endpoint.CredentialRef, "limits": json.RawMessage(endpoint.LimitsJSON), "requestDefaults": json.RawMessage(endpoint.RequestDefaultsJSON)},
		"reasoning": map[string]any{"policy": policy, "providerRequestPatch": providerPatch, "responseField": mapping.ResponseField, "preserveOnTool": mapping.PreserveOnTool},
	}
	snapshotJSON, sha, err := snapshotDigest(snapshot)
	if err != nil {
		return modelProfileRowV2{}, modelProfileRevisionRowV2{}, errorx.Internal("MODEL_PROFILE_SNAPSHOT_FAILED", "failed to build model profile snapshot", errorx.WithCause(err))
	}
	revision := modelProfileRevisionRowV2{ProfileID: id, Revision: revisionNumber, SnapshotJSON: snapshotJSON, SHA256: sha, Author: principal.SubjectID, CommitMsg: firstNonEmpty(strings.TrimSpace(req.CommitMsg), "update model profile"), CreatedAt: now}
	return row, revision, nil
}

func (h *modelManagementHTTPHandler) ensureModel(ctx context.Context, id, orgID string) error {
	var count int64
	if err := h.db(ctx).Model(&modelRowV2{}).Where("id = ? AND org_id = ? AND deleted_at IS NULL", id, orgID).Count(&count).Error; err != nil {
		return modelManagementDBErr(err)
	}
	if count == 0 {
		return errModelManagementNotFound
	}
	return nil
}

func modelManagementDBErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errModelManagementNotFound
	case strings.Contains(strings.ToLower(err.Error()), "duplicate"), strings.Contains(strings.ToLower(err.Error()), "unique"), strings.Contains(err.Error(), "23505"):
		return errModelManagementConflict
	default:
		return errorx.Internal("MODEL_MANAGEMENT_DB_FAILED", "model management database operation failed", errorx.WithCause(err))
	}
}

func pageRows[T any](rows []T, limit, offset int) ([]T, string) {
	if len(rows) <= limit {
		return rows, ""
	}
	return rows[:limit], strconv.Itoa(offset + limit)
}

func firstErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
