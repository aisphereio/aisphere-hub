package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"gorm.io/gorm"
)

var modelCodeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

var (
	errModelManagementUnauthenticated = errorx.Unauthorized("MODEL_MANAGEMENT_UNAUTHENTICATED", "authentication is required")
	errModelManagementForbidden       = errorx.Forbidden("MODEL_MANAGEMENT_FORBIDDEN", "permission denied")
	errModelManagementNotFound        = errorx.NotFound("MODEL_MANAGEMENT_NOT_FOUND", "model resource not found")
	errModelManagementConflict        = errorx.Conflict("MODEL_MANAGEMENT_CONFLICT", "model resource already exists")
)

type modelManagementHTTPHandler struct {
	resources *data.Resources
	authz     biz.AuthzRepo
}

type modelCapabilities struct {
	Chat             bool `json:"chat"`
	ToolCalling      bool `json:"toolCalling"`
	Streaming        bool `json:"streaming"`
	StructuredOutput bool `json:"structuredOutput"`
	VisionInput      bool `json:"visionInput"`
	AudioInput       bool `json:"audioInput"`
	AudioOutput      bool `json:"audioOutput"`
	Embedding        bool `json:"embedding"`
	Rerank           bool `json:"rerank"`
}

type reasoningCapability struct {
	Supported                bool     `json:"supported"`
	Modes                    []string `json:"modes"`
	EffortLevels             []string `json:"effortLevels"`
	DefaultMode              string   `json:"defaultMode"`
	DefaultEffort            string   `json:"defaultEffort"`
	SupportsBudgetTokens     bool     `json:"supportsBudgetTokens"`
	PreserveReasoningContent bool     `json:"preserveReasoningContent"`
	Notes                    string   `json:"notes,omitempty"`
}

type modelRowV2 struct {
	ID                 string          `gorm:"column:id" json:"id"`
	Code               string          `gorm:"column:code" json:"code"`
	DisplayName        string          `gorm:"column:display_name" json:"displayName"`
	Description        string          `gorm:"column:description" json:"description,omitempty"`
	Status             string          `gorm:"column:status" json:"status"`
	Vendor             string          `gorm:"column:vendor" json:"vendor"`
	Family             string          `gorm:"column:family" json:"family,omitempty"`
	ModelType          string          `gorm:"column:model_type" json:"modelType"`
	CapabilitiesJSON   json.RawMessage `gorm:"column:capabilities_json" json:"capabilities"`
	ReasoningJSON      json.RawMessage `gorm:"column:reasoning_json" json:"reasoning"`
	ProviderConfigJSON json.RawMessage `gorm:"column:provider_config_json" json:"providerConfig,omitempty"`
	OwnerType          string          `gorm:"column:owner_type" json:"-"`
	OwnerID            string          `gorm:"column:owner_id" json:"-"`
	OwnerName          string          `gorm:"column:owner_name" json:"-"`
	OrgID              string          `gorm:"column:org_id" json:"orgId"`
	ProjectID          string          `gorm:"column:project_id" json:"projectId,omitempty"`
	CreatedAt          time.Time       `gorm:"column:created_at" json:"createTime"`
	UpdatedAt          time.Time       `gorm:"column:updated_at" json:"updateTime"`
	DeletedAt          *time.Time      `gorm:"column:deleted_at" json:"-"`
}

func (modelRowV2) TableName() string { return "aihub_models_v2" }

type endpointLimits struct {
	ContextWindow   int `json:"contextWindow"`
	MaxOutputTokens int `json:"maxOutputTokens"`
}

type reasoningMapping struct {
	Strategy       string            `json:"strategy"`
	ModeField      string            `json:"modeField,omitempty"`
	EnabledValue   any               `json:"enabledValue,omitempty"`
	DisabledValue  any               `json:"disabledValue,omitempty"`
	AutoValue      any               `json:"autoValue,omitempty"`
	EffortField    string            `json:"effortField,omitempty"`
	EffortMap      map[string]string `json:"effortMap,omitempty"`
	BudgetField    string            `json:"budgetField,omitempty"`
	ResponseField  string            `json:"responseField,omitempty"`
	PreserveOnTool bool              `json:"preserveOnTool,omitempty"`
}

type modelEndpointRowV2 struct {
	ID                   string          `gorm:"column:id" json:"id"`
	ModelID              string          `gorm:"column:model_id" json:"modelId"`
	DisplayName          string          `gorm:"column:display_name" json:"displayName"`
	Description          string          `gorm:"column:description" json:"description,omitempty"`
	Status               string          `gorm:"column:status" json:"status"`
	Adapter              string          `gorm:"column:adapter" json:"adapter"`
	APIFormat            string          `gorm:"column:api_format" json:"apiFormat"`
	BaseURL              string          `gorm:"column:base_url" json:"baseUrl"`
	ProviderModelID      string          `gorm:"column:provider_model_id" json:"providerModelId"`
	APIPath              string          `gorm:"column:api_path" json:"apiPath,omitempty"`
	CredentialRef        string          `gorm:"column:credential_ref" json:"credentialRef,omitempty"`
	LimitsJSON           json.RawMessage `gorm:"column:limits_json" json:"limits"`
	ReasoningMappingJSON json.RawMessage `gorm:"column:reasoning_mapping_json" json:"reasoningMapping"`
	RequestDefaultsJSON  json.RawMessage `gorm:"column:request_defaults_json" json:"requestDefaults,omitempty"`
	ProviderConfigJSON   json.RawMessage `gorm:"column:provider_config_json" json:"providerConfig,omitempty"`
	HealthStatus         string          `gorm:"column:health_status" json:"healthStatus"`
	LastCheckedAt        *time.Time      `gorm:"column:last_checked_at" json:"lastCheckedAt,omitempty"`
	OrgID                string          `gorm:"column:org_id" json:"orgId"`
	ProjectID            string          `gorm:"column:project_id" json:"projectId,omitempty"`
	CreatedAt            time.Time       `gorm:"column:created_at" json:"createTime"`
	UpdatedAt            time.Time       `gorm:"column:updated_at" json:"updateTime"`
	DeletedAt            *time.Time      `gorm:"column:deleted_at" json:"-"`
}

func (modelEndpointRowV2) TableName() string { return "aihub_model_endpoints_v2" }

type reasoningPolicy struct {
	Mode              string         `json:"mode"`
	Effort            string         `json:"effort"`
	BudgetTokens      int            `json:"budgetTokens,omitempty"`
	ExposeReasoning   bool           `json:"exposeReasoning,omitempty"`
	ProviderOverrides map[string]any `json:"providerOverrides,omitempty"`
}

type modelProfileRowV2 struct {
	ID                    string          `gorm:"column:id" json:"id"`
	Code                  string          `gorm:"column:code" json:"code"`
	DisplayName           string          `gorm:"column:display_name" json:"displayName"`
	Description           string          `gorm:"column:description" json:"description,omitempty"`
	Status                string          `gorm:"column:status" json:"status"`
	EndpointID            string          `gorm:"column:endpoint_id" json:"endpointId"`
	LimitsJSON            json.RawMessage `gorm:"column:limits_json" json:"limits"`
	ReasoningPolicyJSON   json.RawMessage `gorm:"column:reasoning_policy_json" json:"reasoningPolicy"`
	DefaultParametersJSON json.RawMessage `gorm:"column:default_parameters_json" json:"defaultParameters,omitempty"`
	AllowedToolsJSON      json.RawMessage `gorm:"column:allowed_tools_json" json:"allowedTools,omitempty"`
	LatestRevision        int64           `gorm:"column:latest_revision" json:"latestRevision"`
	OwnerType             string          `gorm:"column:owner_type" json:"-"`
	OwnerID               string          `gorm:"column:owner_id" json:"-"`
	OwnerName             string          `gorm:"column:owner_name" json:"-"`
	OrgID                 string          `gorm:"column:org_id" json:"orgId"`
	ProjectID             string          `gorm:"column:project_id" json:"projectId,omitempty"`
	CreatedAt             time.Time       `gorm:"column:created_at" json:"createTime"`
	UpdatedAt             time.Time       `gorm:"column:updated_at" json:"updateTime"`
	DeletedAt             *time.Time      `gorm:"column:deleted_at" json:"-"`
}

func (modelProfileRowV2) TableName() string { return "aihub_model_profiles_v2" }

type modelProfileRevisionRowV2 struct {
	ID           int64           `gorm:"column:id" json:"-"`
	ProfileID    string          `gorm:"column:profile_id" json:"profileId"`
	Revision     int64           `gorm:"column:revision" json:"revision"`
	SnapshotJSON json.RawMessage `gorm:"column:snapshot_json" json:"snapshot"`
	SHA256       string          `gorm:"column:sha256" json:"sha256"`
	Author       string          `gorm:"column:author" json:"author"`
	CommitMsg    string          `gorm:"column:commit_msg" json:"commitMsg"`
	CreatedAt    time.Time       `gorm:"column:created_at" json:"createTime"`
}

func (modelProfileRevisionRowV2) TableName() string { return "aihub_model_profile_revisions_v2" }

type modelWriteRequest struct {
	Code           string              `json:"code"`
	DisplayName    string              `json:"displayName"`
	Description    string              `json:"description"`
	Status         string              `json:"status"`
	Vendor         string              `json:"vendor"`
	Family         string              `json:"family"`
	ModelType      string              `json:"modelType"`
	Capabilities   modelCapabilities   `json:"capabilities"`
	Reasoning      reasoningCapability `json:"reasoning"`
	ProviderConfig map[string]any      `json:"providerConfig"`
	ProjectID      string              `json:"projectId"`
}

type endpointWriteRequest struct {
	ModelID          string           `json:"modelId"`
	DisplayName      string           `json:"displayName"`
	Description      string           `json:"description"`
	Status           string           `json:"status"`
	Adapter          string           `json:"adapter"`
	APIFormat        string           `json:"apiFormat"`
	BaseURL          string           `json:"baseUrl"`
	ProviderModelID  string           `json:"providerModelId"`
	APIPath          string           `json:"apiPath"`
	CredentialRef    string           `json:"credentialRef"`
	Limits           endpointLimits   `json:"limits"`
	ReasoningMapping reasoningMapping `json:"reasoningMapping"`
	RequestDefaults  map[string]any   `json:"requestDefaults"`
	ProviderConfig   map[string]any   `json:"providerConfig"`
	ProjectID        string           `json:"projectId"`
}

type profileWriteRequest struct {
	Code              string          `json:"code"`
	DisplayName       string          `json:"displayName"`
	Description       string          `json:"description"`
	Status            string          `json:"status"`
	EndpointID        string          `json:"endpointId"`
	Limits            endpointLimits  `json:"limits"`
	ReasoningPolicy   reasoningPolicy `json:"reasoningPolicy"`
	DefaultParameters map[string]any  `json:"defaultParameters"`
	AllowedTools      []string        `json:"allowedTools"`
	CommitMsg         string          `json:"commitMsg"`
	ProjectID         string          `json:"projectId"`
}

func registerModelManagementHTTP(srv *khttp.Server, resources *data.Resources) {
	if srv == nil || resources == nil || resources.DB == nil {
		return
	}
	h := &modelManagementHTTPHandler{resources: resources, authz: data.NewAuthzRepo(resources)}
	r := srv.Route("/")
	r.Handle(http.MethodGet, "/v1/models", h.listModels)
	r.Handle(http.MethodPost, "/v1/models", h.createModel)
	r.Handle(http.MethodGet, "/v1/models/{id}", h.getModel)
	r.Handle(http.MethodPut, "/v1/models/{id}", h.updateModel)
	r.Handle(http.MethodDelete, "/v1/models/{id}", h.deleteModel)
	r.Handle(http.MethodGet, "/v1/model-endpoints", h.listEndpoints)
	r.Handle(http.MethodPost, "/v1/model-endpoints", h.createEndpoint)
	r.Handle(http.MethodGet, "/v1/model-endpoints/{id}", h.getEndpoint)
	r.Handle(http.MethodPut, "/v1/model-endpoints/{id}", h.updateEndpoint)
	r.Handle(http.MethodDelete, "/v1/model-endpoints/{id}", h.deleteEndpoint)
	r.Handle(http.MethodGet, "/v1/model-profiles", h.listProfiles)
	r.Handle(http.MethodPost, "/v1/model-profiles", h.createProfile)
	r.Handle(http.MethodGet, "/v1/model-profiles/{id}", h.getProfile)
	r.Handle(http.MethodPut, "/v1/model-profiles/{id}", h.updateProfile)
	r.Handle(http.MethodDelete, "/v1/model-profiles/{id}", h.deleteProfile)
	r.Handle(http.MethodPost, "/v1/model-profiles/{id}:resolve", h.resolveProfile)
}

func (h *modelManagementHTTPHandler) db(ctx context.Context) *gorm.DB {
	return h.resources.DB.GORM(ctx)
}

func (h *modelManagementHTTPHandler) withAuthn(ctx khttp.Context, operation string, req any, fn func(context.Context, authn.Principal) (any, error)) (any, error) {
	khttp.SetOperation(ctx, operation)
	chain := ctx.Middleware(func(c context.Context, _ any) (any, error) {
		principal, ok := authn.PrincipalFromContext(c)
		if !ok || strings.TrimSpace(principal.SubjectID) == "" {
			return nil, errModelManagementUnauthenticated
		}
		if principal.SubjectType == "" {
			principal.SubjectType = "user"
		}
		return fn(c, principal)
	})
	return chain(ctx, req)
}

func (h *modelManagementHTTPHandler) requireZone(ctx context.Context, principal authn.Principal, permission string) error {
	if strings.TrimSpace(principal.OrgID) == "" {
		return errModelManagementForbidden
	}
	permission = modelManagementPermission(permission)
	decision, err := h.authz.Check(ctx, biz.AuthzCheckRequest{
		Subject:    biz.AuthzSubjectRef{Type: principal.SubjectType, ID: principal.SubjectID},
		Resource:   biz.AuthzObjectRef{Type: "zone", ID: principal.OrgID},
		Permission: permission,
		OrgID:      principal.OrgID,
	})
	if err != nil || !decision.Allowed {
		return errModelManagementForbidden
	}
	return nil
}

// modelManagementPermission keeps call sites readable while decoupling model
// operations from the Skill permission domain. It also makes the migration from
// the first V2 draft explicit: no model request is authorized with a Skill
// capability after IAM PR #61 is deployed.
func modelManagementPermission(permission string) string {
	switch permission {
	case "manage_skills", "manage_models":
		return "manage_models"
	case "view_skills", "use_models":
		return "use_models"
	default:
		return permission
	}
}

func newModelUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))[:32]
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	x := hex.EncodeToString(b[:])
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
}

func normalizeStatus(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "disabled") {
		return "disabled"
	}
	return "active"
}

func rawJSON(value any, fallback string) json.RawMessage {
	out, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(fallback)
	}
	return out
}
