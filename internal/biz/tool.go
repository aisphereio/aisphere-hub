package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
)

// Tool catalog errors.
var (
	ErrToolNotFound        = errorx.NotFound(errorx.Code("TOOL_NOT_FOUND"), "tool not found")
	ErrToolInvalidArgument = errorx.BadRequest(errorx.Code("INVALID_ARGUMENT"), "invalid tool argument")
	ErrToolAlreadyExists   = errorx.Conflict(errorx.Code("TOOL_ALREADY_EXISTS"), "tool already exists")
	// ErrToolVersionConflict is returned when a write targets an already-existing
	// (tool_id, version) whose stored definition differs. Versions are
	// immutable: a behavior-changing edit must use a new version label.
	ErrToolVersionConflict = errorx.Conflict(errorx.Code("TOOL_VERSION_CONFLICT"), "tool version already exists and versions are immutable")
)

// toolIDPattern constrains tool identifiers (DNS-1123-ish, allowing dots for
// namespaced tool names like "skill.fetch", "workspace.read").
func validToolID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 {
		return false
	}
	// first/last char alnum; interior allows [A-Za-z0-9._-].
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			continue
		case r == '.' || r == '_' || r == '-':
			if i == 0 {
				return false
			}
			continue
		default:
			return false
		}
	}
	// last char must not be a separator.
	last := id[len(id)-1]
	return last != '.' && last != '_' && last != '-'
}

// --- Domain types ---

// Tool is the catalog record for an agent-invocable capability. The biz layer
// is the authority for the shape; data converts to/from GORM models.
type Tool struct {
	ID            string
	DisplayName   string
	Description   string
	Status        string // active | disabled | builtin
	Scope         string // system | project | private
	Labels        map[string]string
	Object        string // "tool:{id}" SpiceDB object ref
	OwnerSubject  string // "{type}:{id}"
	LatestVersion string
	Versions      map[string]ToolVersion // version label -> record
	OwnerType     string
	OwnerID       string
	OwnerName     string
	OrgID         string
	ProjectID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ToolVersion is an immutable snapshot of a Tool definition at a version.
type ToolVersion struct {
	Version    string
	Revision   string
	SHA256     string
	Author     string
	CommitMsg  string
	CreateTime time.Time
	Definition ToolDefinition
}

// ToolDefinition is the full spec a Runtime reads to register a tool.
type ToolDefinition struct {
	Runtime       ToolRuntimeDefinition
	Execution     *ToolExecutionDefinition
	InputSchema   json.RawMessage // JSON Schema
	OutputSchema  json.RawMessage // JSON Schema
	TimeoutMillis int32
	Retry         *ToolRetryPolicy
	Metadata      json.RawMessage // arbitrary JSON
}

type ToolRuntimeDefinition struct {
	Type          string
	Server        string
	Name          string
	URL           string
	Method        string
	Package       string
	EntryPoint    string
	Headers       map[string]string
	Config        json.RawMessage
	CredentialRef string
	Description   string
}

type ToolExecutionDefinition struct {
	Placement    string // sandbox | runtime | remote | hub
	Runner       string
	Image        string
	Command      string
	Args         []string
	WorkingDir   string
	Filesystem   string
	Network      string
	Mounts       []ToolMount
	Env          map[string]string
	SecretRefs   []string
	AllowHosts   []string
	DenyHosts    []string
	Resources    *ToolResources
	Capabilities []string
}

type ToolMount struct {
	Name      string
	Ref       string
	MountPath string
	Mode      string
}

type ToolResources struct {
	CPU           string
	Memory        string
	TimeoutMillis int32
	MaxOutputBytes int32
}

type ToolRetryPolicy struct {
	MaxAttempts       int32
	BackoffMillis     int32
	RetryOnErrorCodes []string
}

// ToolRepository is the data-layer interface for the Tool catalog.
type ToolRepository interface {
	List(ctx context.Context, opts ToolListOptions) ([]*Tool, string, error)
	Get(ctx context.Context, id string, version string) (*Tool, error)
	Create(ctx context.Context, t *Tool) (*Tool, error)
	Update(ctx context.Context, t *Tool) (*Tool, error)
	Delete(ctx context.Context, id string) error
	UpsertBuiltin(ctx context.Context, t *Tool) (*Tool, error) // idempotent seed
}

type ToolListOptions struct {
	Limit       int
	Offset      int
	Query       string
	Scope       string
	Status      string
	RuntimeType string
}

// ToolRelationships is the authz interface (a subset of SkillRelationships).
// Tool reuses the SpiceDB `tool` definition (owner/editor/executor/viewer).
type ToolRelationships interface {
	Check(ctx context.Context, req AuthzCheckRequest) (AuthzDecision, error)
	BatchCheck(ctx context.Context, req AuthzBatchCheckRequest) (AuthzBatchCheckResult, error)
	GrantOwner(ctx context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error
	GrantZone(ctx context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error
	RevokeAll(ctx context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error
	RevokeResource(ctx context.Context, resource AuthzObjectRef) error
}

// ToolUsecase implements the Tool catalog business logic.
type ToolUsecase struct {
	tools ToolRepository
	rels  ToolRelationships
}

func NewToolUsecase(tools ToolRepository, rels ToolRelationships) *ToolUsecase {
	return &ToolUsecase{tools: tools, rels: rels}
}

func toolResource(toolID string) AuthzObjectRef {
	return AuthzObjectRef{Type: "tool", ID: toolID}
}

func toolOwnerSubject(t *Tool) (AuthzSubjectRef, error) {
	tt := strings.TrimSpace(t.OwnerType)
	if tt == "" {
		tt = "user"
	}
	id := strings.TrimSpace(t.OwnerID)
	if id == "" {
		return AuthzSubjectRef{}, ErrToolInvalidArgument
	}
	return AuthzSubjectRef{Type: tt, ID: id}, nil
}

// CreateTool validates, persists, and writes SpiceDB owner + zone relationships.
func (uc *ToolUsecase) CreateTool(ctx context.Context, principal authn.Principal, t *Tool) (*Tool, error) {
	if err := validateToolForCreate(t); err != nil {
		return nil, err
	}
	// Tenant isolation: the tool always lands in the caller's org. A request
	// may restate the org explicitly (the gateway authz interpolates
	// tool_space:{org_id}); a mismatch is rejected rather than silently
	// re-homed. project_id is caller-chosen and persisted as-is.
	if reqOrg := strings.TrimSpace(t.OrgID); reqOrg != "" && reqOrg != principal.OrgID {
		return nil, fmt.Errorf("%w: org_id %q does not match the caller's org %q", ErrToolInvalidArgument, reqOrg, principal.OrgID)
	}
	t.OwnerType = principal.SubjectType
	t.OwnerID = principal.SubjectID
	t.OwnerName = principal.Name
	t.OrgID = principal.OrgID
	t.Object = "tool:" + t.ID
	if t.Status == "" {
		t.Status = "active"
	}
	if t.Scope == "" {
		t.Scope = "project"
	}
	out, err := uc.tools.Create(ctx, t)
	if err != nil {
		return nil, err
	}
	// Best-effort SpiceDB owner relationship; compensate by soft-deleting the
	// row on failure so the catalog does not leak an unowned tool.
	subject, err := canonicalSubject(principal)
	if err != nil {
		_ = uc.tools.Delete(ctx, t.ID)
		return nil, err
	}
	if err := uc.rels.GrantOwner(ctx, toolResource(t.ID), subject); err != nil {
		_ = uc.tools.Delete(ctx, t.ID)
		return nil, fmt.Errorf("tool create: grant owner: %w", err)
	}
	if principal.OrgID != "" {
		_ = uc.rels.GrantZone(ctx, toolResource(t.ID), AuthzSubjectRef{Type: "zone", ID: principal.OrgID})
	}
	return out, nil
}

func validateToolForCreate(t *Tool) error {
	if !validToolID(t.ID) {
		return fmt.Errorf("%w: id must match [A-Za-z0-9][A-Za-z0-9._-]{0,127} (trailing separators not allowed)", ErrToolInvalidArgument)
	}
	if t.Definition().Runtime.Type == "" {
		return fmt.Errorf("%w: definition.runtime.type is required", ErrToolInvalidArgument)
	}
	return nil
}

// Definition returns the latest version's definition, or a zero definition if
// the tool has no versions yet (e.g. during create before the version is set).
func (t *Tool) Definition() ToolDefinition {
	if t.LatestVersion == "" || t.Versions == nil {
		return ToolDefinition{}
	}
	if v, ok := t.Versions[t.LatestVersion]; ok {
		return v.Definition
	}
	return ToolDefinition{}
}

// GetTool reads a tool (optionally a specific version) and checks view permission.
func (uc *ToolUsecase) GetTool(ctx context.Context, principal authn.Principal, id, version string) (*Tool, error) {
	t, err := uc.tools.Get(ctx, id, version)
	if err != nil {
		return nil, err
	}
	// Builtin/system tools are readable by any authenticated principal.
	if t.Scope == "system" || t.Status == "builtin" {
		return t, nil
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   toolResource(t.ID),
		Permission: "view",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no view permission on tool")
	}
	return t, nil
}

// ListTools returns tools visible to the principal. System/builtin tools are
// always included; project/private tools are batch-checked for view permission.
func (uc *ToolUsecase) ListTools(ctx context.Context, principal authn.Principal, opts ToolListOptions) ([]*Tool, string, error) {
	tools, next, err := uc.tools.List(ctx, opts)
	if err != nil {
		return nil, "", err
	}
	if len(tools) == 0 {
		return tools, next, nil
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, "", err
	}
	// Partition into always-visible (system/builtin) and authz-checked.
	var checks []AuthzCheckRequest
	var checkIdx []int
	out := make([]*Tool, 0, len(tools))
	for i, t := range tools {
		if t.Scope == "system" || t.Status == "builtin" {
			out = append(out, t)
			continue
		}
		checks = append(checks, AuthzCheckRequest{
			Subject:    subject,
			Resource:   toolResource(t.ID),
			Permission: "view",
			OrgID:      principal.OrgID,
		})
		checkIdx = append(checkIdx, i)
	}
	if len(checks) == 0 {
		return tools, next, nil
	}
	res, err := uc.rels.BatchCheck(ctx, AuthzBatchCheckRequest{Checks: checks})
	if err != nil {
		return nil, "", err
	}
	for j, idx := range checkIdx {
		if j < len(res.Decisions) && res.Decisions[j].Allowed {
			out = append(out, tools[idx])
		}
	}
	return out, next, nil
}

// UpdateTool checks edit permission, persists metadata + a new version record.
func (uc *ToolUsecase) UpdateTool(ctx context.Context, principal authn.Principal, t *Tool) (*Tool, error) {
	if !validToolID(t.ID) {
		return nil, fmt.Errorf("%w: invalid id", ErrToolInvalidArgument)
	}
	existing, err := uc.tools.Get(ctx, t.ID, "")
	if err != nil {
		return nil, err
	}
	if existing.Scope != "system" && existing.Status != "builtin" {
		subject, err := canonicalSubject(principal)
		if err != nil {
			return nil, err
		}
		dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
			Subject:    subject,
			Resource:   toolResource(t.ID),
			Permission: "edit",
			OrgID:      principal.OrgID,
		})
		if err != nil {
			return nil, err
		}
		if !dec.Allowed {
			return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no edit permission on tool")
		}
	}
	t.OwnerType = existing.OwnerType
	t.OwnerID = existing.OwnerID
	t.OwnerName = existing.OwnerName
	t.OrgID = existing.OrgID
	t.Object = existing.Object
	return uc.tools.Update(ctx, t)
}

// DeleteTool checks manage permission, soft-deletes, and revokes SpiceDB rels.
func (uc *ToolUsecase) DeleteTool(ctx context.Context, principal authn.Principal, id string) (*Tool, error) {
	existing, err := uc.tools.Get(ctx, id, "")
	if err != nil {
		return nil, err
	}
	if existing.Scope != "system" && existing.Status != "builtin" {
		subject, serr := canonicalSubject(principal)
		if serr != nil {
			return nil, serr
		}
		dec, derr := uc.rels.Check(ctx, AuthzCheckRequest{
			Subject:    subject,
			Resource:   toolResource(id),
			Permission: "manage",
			OrgID:      principal.OrgID,
		})
		if derr != nil {
			return nil, derr
		}
		if !dec.Allowed {
			return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no manage permission on tool")
		}
	}
	if err := uc.tools.Delete(ctx, id); err != nil {
		return nil, err
	}
	// Best-effort revoke; a stale relationship does not resurrect a soft-deleted row.
	_ = uc.rels.RevokeResource(ctx, toolResource(id))
	return existing, nil
}

// ResolveTool is not yet implemented (depends on the Runtime execution plane).
func (uc *ToolUsecase) ResolveTool(ctx context.Context, principal authn.Principal, id string, runtimeID, sessionID, version, label string) error {
	return errorx.Unavailable(errorx.Code("TOOL_RESOLVE_NOT_AVAILABLE"), "tool resolve is not yet available; the runtime executor is not connected")
}

// ListToolFailures is not yet implemented (depends on the audit/failure store).
func (uc *ToolUsecase) ListToolFailures(ctx context.Context, principal authn.Principal, id string, limit int, offset int) error {
	return errorx.Unavailable(errorx.Code("TOOL_FAILURES_NOT_AVAILABLE"), "tool failure listing is not yet available")
}

// --- Builtin tool seeding ---

// builtinToolSeed describes one catalog entry seeded at Hub startup. The
// definitions mirror sandboxToolRegistry (sandbox_usecase.go) so the existing
// sandbox tool surface is represented as real Tool records the Runtime can pull.
type builtinToolSeed struct {
	id          string
	displayName string
	description string
	inputSchema string // JSON Schema
	placement   string // sandbox | runtime
	capability  string // SpiceDB capability, "" for non-privileged
}

// builtinToolSeeds is the V1 builtin tool surface. Privileged tools (placement
// "runtime") carry a capability that maps to a SpiceDB permission on the target
// skill resource; the Runtime runs the Tool-level authz gate using it.
var builtinToolSeeds = []builtinToolSeed{
	{ id: "workspace.read", displayName: "Workspace Read", description: "Read the contents of a file in the sandbox workspace.",
		inputSchema: `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file relative to the workspace root."}},"required":["path"],"additionalProperties":false}`, placement: "sandbox" },
	{ id: "workspace.write", displayName: "Workspace Write", description: "Write content to a file in the sandbox workspace.",
		inputSchema: `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`, placement: "sandbox" },
	{ id: "workspace.list", displayName: "Workspace List", description: "List entries under a path in the sandbox workspace.",
		inputSchema: `{"type":"object","properties":{"path":{"type":"string","default":"."}},"required":["path"],"additionalProperties":false}`, placement: "sandbox" },
	{ id: "workspace.delete", displayName: "Workspace Delete", description: "Delete a file or directory in the sandbox workspace.",
		inputSchema: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`, placement: "sandbox" },
	{ id: "workspace.search_text", displayName: "Workspace Search Text", description: "Search for a text pattern across the sandbox workspace.",
		inputSchema: `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string","default":"."}},"required":["pattern"],"additionalProperties":false}`, placement: "sandbox" },
	{ id: "browser.open", displayName: "Browser Open", description: "Open a URL in the sandbox browser environment.",
		inputSchema: `{"type":"object","properties":{"url":{"type":"string","format":"uri"}},"required":["url"],"additionalProperties":false}`, placement: "sandbox" },
	{ id: "skill.fetch", displayName: "Skill Fetch", description: "Fetch a published skill release into the sandbox workspace. Resolves <name>@<version> to a release tag and shallow-clones its snapshot.",
		inputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name, e.g. \"ttt1\"."},"version":{"type":"string","description":"SemVer, e.g. \"1.4.2\" or \"v1.4.2\"."},"dest":{"type":"string","default":"./skills/{name}","description":"Destination directory relative to the workspace root."}},"required":["name","version"],"additionalProperties":false}`, placement: "runtime", capability: "skill:view" },
	{ id: "skill.pull", displayName: "Skill Pull", description: "Pull the latest draft (main branch) of a skill repo into the sandbox workspace for editing.",
		inputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name."},"ref":{"type":"string","default":"refs/heads/main","description":"Git ref to pull."}},"required":["name"],"additionalProperties":false}`, placement: "runtime", capability: "skill:view" },
	{ id: "skill.push", displayName: "Skill Push", description: "Push local skill draft commits to the Hub git remote (updates the main branch draft).",
		inputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name."},"ref":{"type":"string","default":"refs/heads/main","description":"Git ref to push."}},"required":["name"],"additionalProperties":false}`, placement: "runtime", capability: "skill:edit" },
	{ id: "skill.tag", displayName: "Skill Tag", description: "Create an annotated release tag on a commit without pushing. Validates SKILL.md at the target commit. The tag message carries AISphere-Source-Ref and AISphere-Publisher-ID. Use skill.publish to tag and push in one step.",
		inputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name."},"version":{"type":"string","description":"SemVer to tag, e.g. \"1.4.2\"."},"commitSha":{"type":"string","description":"The commit SHA to tag. Defaults to the current HEAD of the pulled draft."},"notes":{"type":"string","description":"Release notes for the tag message."}},"required":["name","version"],"additionalProperties":false}`, placement: "runtime", capability: "skill:publish" },
	{ id: "skill.publish", displayName: "Skill Publish", description: "Publish a skill release: validate SKILL.md, create an annotated release tag, and push it to the Hub git remote. Irreversible (creates a public release). Equivalent to skill.tag + push.",
		inputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name."},"version":{"type":"string","description":"SemVer to publish, e.g. \"1.4.2\"."},"commitSha":{"type":"string","description":"The commit SHA to tag. Defaults to the current HEAD of the pulled draft."},"notes":{"type":"string","description":"Release notes for the tag message."}},"required":["name","version"],"additionalProperties":false}`, placement: "runtime", capability: "skill:publish" },
}

// deprecatedBuiltinToolIDs lists builtin tool IDs that were renamed or removed.
// SeedBuiltinTools soft-deletes any lingering rows so the catalog does not
// accumulate stale entries across versions.
var deprecatedBuiltinToolIDs = []string{
	"git.pull", // renamed to skill.pull
	"git.push", // renamed to skill.push
}

// SeedBuiltinTools idempotently upserts the builtin tool surface into the
// catalog. It is called at Hub startup. Builtin tools have scope=system,
// status=builtin, owner=system, and no SpiceDB relationships (they are
// universally readable; per-call authorization happens in the Runtime
// Tool-level gate using the capability field). Renamed/removed builtin IDs
// are soft-deleted so stale rows do not linger.
func SeedBuiltinTools(ctx context.Context, repo ToolRepository) error {
	for _, s := range builtinToolSeeds {
		t := builtinSeedToTool(s)
		if _, err := repo.UpsertBuiltin(ctx, t); err != nil {
			return fmt.Errorf("seed builtin tool %q: %w", s.id, err)
		}
	}
	for _, id := range deprecatedBuiltinToolIDs {
		// Best-effort soft-delete; a missing row is not an error.
		if err := repo.Delete(ctx, id); err != nil && !errors.Is(err, ErrToolNotFound) {
			return fmt.Errorf("deprecate builtin tool %q: %w", id, err)
		}
	}
	return nil
}

func builtinSeedToTool(s builtinToolSeed) *Tool {
	caps := []string{}
	if s.capability != "" {
		caps = []string{s.capability}
	}
	def := ToolDefinition{
		Runtime: ToolRuntimeDefinition{
			Type:        "builtin",
			Name:        s.id,
			Description: s.description,
		},
		Execution: &ToolExecutionDefinition{
			Placement:    s.placement,
			Runner:       "builtin",
			Filesystem:   "workspace",
			Network:      "none",
			Capabilities: caps,
		},
		InputSchema: json.RawMessage(s.inputSchema),
	}
	if s.placement == "runtime" {
		// Privileged tools need restricted egress to the Hub git endpoint.
		def.Execution.Network = "restricted"
	}
	ver := builtinSeedVersion(def)
	sum := sha256Hex(mustMarshalJSON(def))
	return &Tool{
		ID:            s.id,
		DisplayName:   s.displayName,
		Description:   s.description,
		Status:        "builtin",
		Scope:         "system",
		Object:        "tool:" + s.id,
		OwnerType:     "service",
		OwnerID:       "aisphere-hub",
		OwnerName:     "AISphere Hub",
		LatestVersion: ver,
		Versions: map[string]ToolVersion{
			ver: {
				Version:    ver,
				Revision:   sum[:12],
				SHA256:     sum,
				Author:     "aisphere-hub",
				CommitMsg:  "builtin tool seed",
				CreateTime: time.Now().UTC(),
				Definition: def,
			},
		},
	}
}

// builtinSeedVersion computes the content-addressed version label for a builtin
// seed definition. The label derives from the definition itself, so reseeding
// an unchanged definition is a natural no-op and any behavior-changing edit
// produces a NEW immutable version instead of overwriting the previous one in
// place (the old "builtin-v1" in-place upsert violated version immutability).
func builtinSeedVersion(def ToolDefinition) string {
	return "builtin-" + sha256Hex(mustMarshalJSON(def))[:12]
}

func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("builtin seed: marshal definition: %v", err))
	}
	return b
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// EnsureErrors exposes sentinel errors for the data layer to wrap.
var _ = errors.Is // keep import when no direct use remains
