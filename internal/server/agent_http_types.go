package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/authn"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"gorm.io/gorm"
)

var agentIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

const (
	agentApprovalAlways   = "always"
	agentApprovalPerRun   = "per_run"
	agentApprovalDisabled = "disabled"
)

type agentHTTPHandler struct {
	resources *data.Resources
	authz     biz.AuthzRepo
}

type agentRow struct {
	ID            int64                         `gorm:"column:id" json:"-"`
	AgentID       string                        `gorm:"column:agent_id" json:"id"`
	DisplayName   string                        `gorm:"column:display_name" json:"displayName,omitempty"`
	Description   string                        `gorm:"column:description" json:"description,omitempty"`
	Status        string                        `gorm:"column:status" json:"status"`
	Scope         string                        `gorm:"column:scope" json:"scope"`
	OwnerType     string                        `gorm:"column:owner_type" json:"-"`
	OwnerID       string                        `gorm:"column:owner_id" json:"-"`
	OwnerName     string                        `gorm:"column:owner_name" json:"-"`
	OrgID         string                        `gorm:"column:org_id" json:"orgId,omitempty"`
	ProjectID     string                        `gorm:"column:project_id" json:"projectId,omitempty"`
	LatestVersion string                        `gorm:"column:latest_version" json:"latestVersion,omitempty"`
	LabelsJSON    json.RawMessage               `gorm:"column:labels_json" json:"labels,omitempty"`
	ObjectRef     string                        `gorm:"column:object_ref" json:"object,omitempty"`
	CreatedAt     time.Time                     `gorm:"column:created_at" json:"createTime"`
	UpdatedAt     time.Time                     `gorm:"column:updated_at" json:"updateTime"`
	DeletedAt     *time.Time                    `gorm:"column:deleted_at" json:"-"`
	Versions      map[string]agentVersionRecord `gorm:"-" json:"versions,omitempty"`
	OwnerSubject  string                        `gorm:"-" json:"ownerSubject,omitempty"`
}

func (agentRow) TableName() string { return "aihub_agents" }

type agentVersionRow struct {
	ID             int64           `gorm:"column:id"`
	AgentID        string          `gorm:"column:agent_id"`
	Version        string          `gorm:"column:version"`
	Revision       string          `gorm:"column:revision"`
	SHA256         string          `gorm:"column:sha256"`
	Author         string          `gorm:"column:author"`
	CommitMsg      string          `gorm:"column:commit_msg"`
	DefinitionJSON json.RawMessage `gorm:"column:definition_json"`
	CreatedAt      time.Time       `gorm:"column:created_at"`
}

func (agentVersionRow) TableName() string { return "aihub_agent_versions" }

type agentVersionRecord struct {
	Version    string          `json:"version"`
	Revision   string          `json:"revision,omitempty"`
	SHA256     string          `json:"sha256,omitempty"`
	Author     string          `json:"author,omitempty"`
	CommitMsg  string          `json:"commitMsg,omitempty"`
	CreateTime time.Time       `json:"createTime"`
	Definition json.RawMessage `json:"definition"`
}

type agentWriteRequest struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"displayName"`
	Description string            `json:"description"`
	Status      string            `json:"status"`
	Scope       string            `json:"scope"`
	Labels      map[string]string `json:"labels"`
	Version     string            `json:"version"`
	CommitMsg   string            `json:"commitMsg"`
	Definition  json.RawMessage   `json:"definition"`
	OrgID       string            `json:"orgId"`
	ProjectID   string            `json:"projectId"`
}

type agentToolBinding struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	Label        string `json:"label,omitempty"`
	Required     bool   `json:"required,omitempty"`
	ApprovalMode string `json:"approvalMode,omitempty"`
}

// agentSkillBinding is deliberately a reference, not an embedded payload.
// Hub pins the reference into the runtime snapshot; the execution plane
// decides whether it is already present in the worker image or must be
// materialized from the Skill catalog.
type agentSkillBinding struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
}

// agentModelBinding points to the AISphere-owned ModelProfile UUID. Revision is
// optional: zero means resolve the latest revision when the Agent run snapshot
// is created. The resolved revision is then frozen into that run snapshot.
type agentModelBinding struct {
	ProfileID string `json:"profileId"`
	Revision  int64  `json:"revision,omitempty"`
}

type agentDefinitionProjection struct {
	EntryPoint string              `json:"entryPoint"`
	Files      map[string]string   `json:"files"`
	Model      *agentModelBinding  `json:"model,omitempty"`
	Tools      []agentToolBinding  `json:"tools"`
	Skills     []agentSkillBinding `json:"skills,omitempty"`
}

type agentRunRequest struct {
	RuntimeID         string   `json:"runtimeId"`
	SessionID         string   `json:"sessionId"`
	Version           string   `json:"version"`
	ApprovalConfirmed bool     `json:"approvalConfirmed"`
	ApprovedTools     []string `json:"approvedTools"`
}

type agentIAMPermission struct {
	ResourceType string `json:"resourceType"`
	Permission   string `json:"permission"`
	Enforcement  string `json:"enforcement"`
}

type agentToolApproval struct {
	Tool         string               `json:"tool"`
	Version      string               `json:"version"`
	Required     bool                 `json:"required"`
	ApprovalMode string               `json:"approvalMode"`
	Approved     bool                 `json:"approved"`
	Capabilities []string             `json:"capabilities,omitempty"`
	Permissions  []agentIAMPermission `json:"permissions,omitempty"`
}

type resolvedAgentTool struct {
	Binding      agentToolBinding
	Version      string
	Revision     string
	Status       string
	Scope        string
	Definition   json.RawMessage
	Capabilities []string
	Snapshot     map[string]any
}

type agentToolCatalogRow struct {
	ToolID        string `gorm:"column:tool_id"`
	Status        string `gorm:"column:status"`
	Scope         string `gorm:"column:scope"`
	LatestVersion string `gorm:"column:latest_version"`
}

type agentToolVersionCatalogRow struct {
	Version        string          `gorm:"column:version"`
	Revision       string          `gorm:"column:revision"`
	SHA256         string          `gorm:"column:sha256"`
	DefinitionJSON json.RawMessage `gorm:"column:definition_json"`
}

func registerSecuredAgentHTTP(srv *khttp.Server, resources *data.Resources) {
	if srv == nil || resources == nil || resources.DB == nil {
		return
	}
	h := &agentHTTPHandler{resources: resources, authz: data.NewAuthzRepo(resources)}
	r := srv.Route("/")
	r.Handle(http.MethodGet, "/v1/agents", h.listEndpoint)
	r.Handle(http.MethodPost, "/v1/agents", h.createEndpoint)
	r.Handle(http.MethodPost, "/v1/agents/{id}:plan-run", h.planRunEndpoint)
	r.Handle(http.MethodPost, "/v1/agents/{id}:resolve", h.resolveEndpoint)
	r.Handle(http.MethodGet, "/v1/agents/{id}", h.getEndpoint)
	r.Handle(http.MethodPut, "/v1/agents/{id}", h.updateEndpoint)
	r.Handle(http.MethodDelete, "/v1/agents/{id}", h.deleteEndpoint)
}

func (h *agentHTTPHandler) db(ctx context.Context) *gorm.DB {
	return h.resources.DB.GORM(ctx)
}

func (h *agentHTTPHandler) withAgentAuthn(ctx khttp.Context, operation string, req any, fn func(context.Context, any) (any, error)) (any, error) {
	khttp.SetOperation(ctx, operation)
	chain := ctx.Middleware(func(c context.Context, r any) (any, error) { return fn(c, r) })
	return chain(ctx, req)
}

func agentPrincipal(ctx context.Context) (authn.Principal, error) {
	principal, ok := authn.PrincipalFromContext(ctx)
	if !ok || strings.TrimSpace(principal.SubjectID) == "" {
		return authn.Principal{}, errAgentUnauthenticated
	}
	if strings.TrimSpace(principal.SubjectType) == "" {
		principal.SubjectType = "user"
	}
	return principal, nil
}

func agentSubject(principal authn.Principal) biz.AuthzSubjectRef {
	t := strings.TrimSpace(principal.SubjectType)
	if t == "" {
		t = "user"
	}
	return biz.AuthzSubjectRef{Type: t, ID: principal.SubjectID}
}

func (h *agentHTTPHandler) requirePermission(ctx context.Context, principal authn.Principal, resourceType, resourceID, permission string) error {
	decision, err := h.authz.Check(ctx, biz.AuthzCheckRequest{
		Subject:    agentSubject(principal),
		Resource:   biz.AuthzObjectRef{Type: resourceType, ID: resourceID},
		Permission: permission,
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return errAgentAuthzUnavailable
	}
	if !decision.Allowed {
		return errAgentForbidden
	}
	return nil
}

// allowCreateInScope keeps Agent lifecycle authorization in the same IAM
// vocabulary as the resource model. Publishing a public or system Agent is a
// stronger operation than creating a private/project Agent because it changes
// who can discover or launch it.
func (h *agentHTTPHandler) allowCreateInScope(ctx context.Context, principal authn.Principal, projectID, scope string) error {
	if strings.TrimSpace(principal.OrgID) == "" {
		return errAgentZoneRequired
	}
	scope = normalizeAgentScope(scope)
	permission := "create_agent"
	if scope == "public" || scope == "system" {
		permission = "manage_agents"
	}
	if strings.TrimSpace(projectID) != "" && scope != "system" {
		return h.requirePermission(ctx, principal, "project", strings.TrimSpace(projectID), permission)
	}
	return h.requirePermission(ctx, principal, "zone", principal.OrgID, permission)
}
