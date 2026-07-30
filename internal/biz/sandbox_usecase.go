package biz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"

	"github.com/google/uuid"
)

// SandboxUsecase orchestrates Agent Sandbox lifecycle (design §11). It mirrors
// NamespaceUsecase: each mutating op follows the DB-then-sync-then-ack pattern —
// INSERT the control-plane row (CREATING/PENDING), project SpiceDB rels where
// the resource owns its authz object, load the cluster to build a complete
// CredentialLocator, call the frozen SandboxProvider for the remote CRD apply,
// then stamp READY (or FAILED + compensation). Visibility/list authz is
// delegated to the parent cluster/namespace; only Sandbox gets its own
// k8s_sandbox SpiceDB object (design §11: per-sandbox use/manage). Templates,
// WarmPools, and SandboxClaims are addressed through the parent cluster/
// namespace permission and therefore do not project their own SpiceDB objects.
type SandboxUsecase struct {
	sandboxes  SandboxRepository
	namespaces NamespaceRepository
	clusters   ClusterRepository
	provider   SandboxProvider
	outbox     OutboxEnqueuer
	rels       NamespaceRelationships
	// tools is the Tool catalog usecase. When set, the sandbox tool surface
	// (ListSandboxTools / CallSandboxTool validation) is projected from the
	// catalog — the single source of truth — instead of the static
	// sandboxToolRegistry. Nil falls back to the static registry (tests, K8s
	// disabled wiring).
	tools      *ToolUsecase
	log        logx.Logger
	opts       ClusterUsecaseOptions
}

// NewSandboxUsecase wires the usecase. opts zeroes default to sane values
// (MaxScan=100, MaxHydrateRounds=3), mirroring NewNamespaceUsecase.
func NewSandboxUsecase(
	sandboxes SandboxRepository,
	namespaces NamespaceRepository,
	clusters ClusterRepository,
	provider SandboxProvider,
	outbox OutboxEnqueuer,
	rels NamespaceRelationships,
	tools *ToolUsecase,
	log logx.Logger,
	opts ClusterUsecaseOptions,
) *SandboxUsecase {
	if opts.MaxScan <= 0 {
		opts.MaxScan = 100
	}
	if opts.MaxHydrateRounds <= 0 {
		opts.MaxHydrateRounds = 3
	}
	if log == nil {
		log = logx.Noop()
	}
	return &SandboxUsecase{
		sandboxes:  sandboxes,
		namespaces: namespaces,
		clusters:   clusters,
		provider:   provider,
		outbox:     outbox,
		rels:       rels,
		tools:      tools,
		log:        log.Named("biz.sandbox"),
		opts:       opts,
	}
}

// sandboxResource builds the authz object ref for a sandbox. Only Sandbox gets
// its own SpiceDB object (k8s_sandbox); templates/warm pools/claims are
// authorized through their parent cluster/namespace.
func sandboxResource(sandboxID string) AuthzObjectRef {
	return AuthzObjectRef{Type: "k8s_sandbox", ID: sandboxID}
}

// --- Lifecycle / status constants (design §11) ---
//
// Uppercase strings mirror the DB CHECK constraints and proto enums (design
// decision 1, same convention as ClusterStatus/NamespaceLifecycle).

const (
	SandboxLifecycleCreating    = "CREATING"
	SandboxLifecycleReady       = "READY"
	SandboxLifecycleSuspended   = "SUSPENDED"
	SandboxLifecycleTerminating = "TERMINATING"
	SandboxLifecycleFailed      = "FAILED"
	SandboxLifecycleDeleted     = "DELETED"

	SandboxTemplateStatusCreating = "CREATING"
	SandboxTemplateStatusReady    = "READY"
	SandboxTemplateStatusFailed   = "FAILED"
	SandboxTemplateStatusDeleted  = "DELETED"

	WarmPoolStatusCreating = "CREATING"
	WarmPoolStatusReady    = "READY"
	WarmPoolStatusDegraded = "DEGRADED"
	WarmPoolStatusDeleted  = "DELETED"

	SandboxClaimStatusPending = "PENDING"
	SandboxClaimStatusReady   = "READY"
	SandboxClaimStatusFailed  = "FAILED"
	SandboxClaimStatusDeleted = "DELETED"

	SandboxNetworkModeOffline = "OFFLINE"
	SandboxNetworkModeOnline  = "ONLINE"

	SandboxOperatingModeRunning   = "Running"
	SandboxOperatingModeSuspended = "Suspended"
)

// SandboxToolSchema describes one tool an agent may invoke inside a sandbox.
// InputSchema is a JSON Schema (string) so the biz layer stays free of encoding
// dependencies for the static registry.
type SandboxToolSchema struct {
	Name        string
	Description string
	InputSchema string // JSON Schema

	// Privileged tools require a Tool-level authorization check beyond sandbox.use
	// (agent-identity-delegation-design §3.1). When Privileged is set, Permission
	// names the SpiceDB permission to check and ResourceFromInput extracts the
	// target resource ref from the tool's JSON input. Non-privileged (workspace.*)
	// tools leave these zero and are gated only by sandbox.use.
	Privileged        bool
	Permission        string                                              // e.g. "fetch", "publish", "view", "edit"
	ResourceFromInput func(inputJSON string) (AuthzObjectRef, error)     // e.g. {Type:"skill",ID:"ttt1"}
}

// sandboxToolRegistry is the V1 fixed tool surface exposed by every sandbox
// (design §11). The Tool catalog (aihub_tools) is now the source of truth and
// the sandbox surface is projected from it via sandboxToolSurface; this static
// registry is kept only as a fallback for wiring where the catalog usecase is
// unavailable (tests, K8s disabled). Keep its entries mirrored with
// builtinToolSeeds (tool.go) until the fallback is removed.
var sandboxToolRegistry = []SandboxToolSchema{
	{
		Name:        "workspace.read",
		Description: "Read the contents of a file in the sandbox workspace.",
		InputSchema: `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file relative to the workspace root."}},"required":["path"],"additionalProperties":false}`,
	},
	{
		Name:        "workspace.write",
		Description: "Write content to a file in the sandbox workspace.",
		InputSchema: `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`,
	},
	{
		Name:        "workspace.list",
		Description: "List entries under a path in the sandbox workspace.",
		InputSchema: `{"type":"object","properties":{"path":{"type":"string","default":"."}},"required":["path"],"additionalProperties":false}`,
	},
	{
		Name:        "workspace.delete",
		Description: "Delete a file or directory in the sandbox workspace.",
		InputSchema: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`,
	},
	{
		Name:        "workspace.search_text",
		Description: "Search for a text pattern across the sandbox workspace.",
		InputSchema: `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string","default":"."}},"required":["pattern"],"additionalProperties":false}`,
	},
	{
		Name:        "browser.open",
		Description: "Open a URL in the sandbox browser environment.",
		InputSchema: `{"type":"object","properties":{"url":{"type":"string","format":"uri"}},"required":["url"],"additionalProperties":false}`,
	},
	{
		Name:        "skill.fetch",
		Description: "Fetch a published skill release into the sandbox workspace. Resolves <name>@<version> to a release tag and shallow-clones its snapshot.",
		InputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name, e.g. \"ttt1\"."},"version":{"type":"string","description":"SemVer, e.g. \"1.4.2\" or \"v1.4.2\"."},"dest":{"type":"string","default":"./skills/{name}","description":"Destination directory relative to the workspace root."}},"required":["name","version"],"additionalProperties":false}`,
		// Privileged: fetch is a read operation, gated by view on skill
		// (agent-identity-delegation-design §5.1). Reuses the existing `view`
		// permission rather than a separate `fetch` to avoid role_binding/
		// custom_role churn.
		Privileged:        true,
		Permission:        "view",
		ResourceFromInput: skillResourceFromInput,
	},
	{
		Name:        "skill.publish",
		Description: "Publish a skill release from the sandbox workspace: validate SKILL.md, CAS-check the remote, tag and push an annotated release. Irreversible (creates a public release).",
		InputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name."},"version":{"type":"string","description":"SemVer to publish, e.g. \"1.4.2\"."},"notes":{"type":"string","description":"Release notes for the tag message."}},"required":["name","version"],"additionalProperties":false}`,
		// Privileged: publish on skill (agent-identity-delegation-design §5.2).
		Privileged:        true,
		Permission:        "publish",
		ResourceFromInput: skillResourceFromInput,
	},
	{
		Name:        "git.pull",
		Description: "Pull the latest draft of a skill repo into the sandbox workspace.",
		InputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name."},"ref":{"type":"string","default":"refs/heads/main","description":"Git ref to pull."}},"required":["name"],"additionalProperties":false}`,
		// Privileged: view on skill (read = pull).
		Privileged:        true,
		Permission:        "view",
		ResourceFromInput: skillResourceFromInput,
	},
	{
		Name:        "git.push",
		Description: "Push local skill draft commits to the Hub git remote.",
		InputSchema: `{"type":"object","properties":{"name":{"type":"string","description":"Skill name."},"ref":{"type":"string","default":"refs/heads/main","description":"Git ref to push."}},"required":["name"],"additionalProperties":false}`,
		// Privileged: edit on skill (write = push).
		Privileged:        true,
		Permission:        "edit",
		ResourceFromInput: skillResourceFromInput,
	},
}

// skillResourceFromInput extracts the skill name from a privileged tool's JSON
// input and returns the SpiceDB resource ref. It is shared by
// skill.fetch / skill.publish / git.pull / git.push.
func skillResourceFromInput(inputJSON string) (AuthzObjectRef, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &in); err != nil {
		return AuthzObjectRef{}, fmt.Errorf("parse tool input: %w", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return AuthzObjectRef{}, fmt.Errorf("tool input missing required field: name")
	}
	return AuthzObjectRef{Type: "skill", ID: name}, nil
}

// sandboxSchemaFromCatalogTool projects a Tool catalog record into the sandbox
// tool schema surface. It is the catalog-driven replacement for the static
// sandboxToolRegistry: the catalog (aihub_tools) is the single source of truth
// and the sandbox surface is a projection of it.
//
// Privileged metadata is derived from the definition's execution capabilities
// (e.g. "skill:view" / "skill.publish" -> permission view/publish on the skill
// resource extracted from the tool input). Tools without capabilities are
// gated only by sandbox.use. Unknown capability resource kinds fail closed:
// the call is rejected rather than silently downgraded to non-privileged.
func sandboxSchemaFromCatalogTool(t *Tool) (SandboxToolSchema, error) {
	def := t.Definition()
	out := SandboxToolSchema{
		Name:        t.ID,
		Description: t.Description,
		InputSchema: string(def.InputSchema),
	}
	if out.Description == "" {
		out.Description = def.Runtime.Description
	}
	if def.Execution == nil {
		return out, nil
	}
	for _, cap := range def.Execution.Capabilities {
		kind, perm, ok := strings.Cut(strings.TrimSpace(cap), ":")
		if !ok {
			kind, perm, ok = strings.Cut(cap, ".")
		}
		if !ok || kind == "" || perm == "" {
			return SandboxToolSchema{}, fmt.Errorf("tool %q: malformed capability %q", t.ID, cap)
		}
		if kind != "skill" {
			return SandboxToolSchema{}, fmt.Errorf("tool %q: unsupported capability resource kind %q", t.ID, kind)
		}
		out.Privileged = true
		out.Permission = perm
		out.ResourceFromInput = skillResourceFromInput
	}
	return out, nil
}

// sandboxToolSurface returns the effective sandbox tool list for the caller.
// When the Tool catalog usecase is wired, the surface is projected from the
// catalog (visibility-filtered per principal); otherwise it falls back to the
// static V1 registry so tests and K8s-disabled wiring keep working.
func (uc *SandboxUsecase) sandboxToolSurface(ctx context.Context, principal authn.Principal) ([]SandboxToolSchema, error) {
	if uc.tools == nil {
		out := make([]SandboxToolSchema, len(sandboxToolRegistry))
		copy(out, sandboxToolRegistry)
		return out, nil
	}
	tools, _, err := uc.tools.ListTools(ctx, principal, ToolListOptions{Limit: 200})
	if err != nil {
		return nil, err
	}
	out := make([]SandboxToolSchema, 0, len(tools))
	for _, t := range tools {
		schema, err := sandboxSchemaFromCatalogTool(t)
		if err != nil {
			// A malformed capability should not blank the whole surface; skip
			// the offending tool and log instead of failing the list call.
			uc.log.WithContext(ctx).Warn("skip tool with malformed capability in sandbox surface",
				logx.String("tool_id", t.ID), logx.Err(err))
			continue
		}
		out = append(out, schema)
	}
	return out, nil
}

// ===================== SandboxTemplate operations =====================

// CreateSandboxTemplate runs the create flow (design §11):
//  1. Validate name (DNS-1123) + image.
//  2. Authz `operate` on k8s_cluster:{cluster_id} (templates are cluster-scoped
//     infra; managed via the cluster operator permission, no per-template object).
//  3. Stamp owner/created_by from canonicalSubject.
//  4. INSERT row (status=CREATING, revision=1).
//  5. Load cluster → build locator → provider.ApplySandboxTemplate (SSA).
//  6. On success: UpdateSandboxTemplateStatus(READY).
//     On failure: UpdateSandboxTemplateStatus(FAILED, health_message).
//
// Compensation mirrors NamespaceUsecase.CreateNamespace step-5: a remote apply
// failure marks FAILED rather than rolling back (partial apply is hard to
// reverse safely); no SpiceDB rels exist for templates so there is nothing to
// revoke.
func (uc *SandboxUsecase) CreateSandboxTemplate(ctx context.Context, principal authn.Principal, t *SandboxTemplate) (*SandboxTemplate, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: nil sandbox template", ErrClusterInvalidArgument)
	}
	if t.ID == "" {
		return nil, fmt.Errorf("%w: sandbox template id must be pre-allocated by caller", ErrClusterInvalidArgument)
	}
	if t.ClusterID == "" {
		return nil, fmt.Errorf("%w: cluster_id is required", ErrClusterInvalidArgument)
	}
	if !isDNS1123Label(t.Name) {
		return nil, fmt.Errorf("%w: name must be a DNS-1123 label", ErrClusterInvalidArgument)
	}
	if strings.TrimSpace(t.Image) == "" {
		return nil, fmt.Errorf("%w: image is required", ErrClusterInvalidArgument)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}

	// Step 2: authz `operate` on parent cluster.
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(t.ClusterID),
		Permission: "operate",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no operate permission on cluster")
	}

	// Step 3: stamp owner/created_by. KubernetesName defaults to the validated
	// Name (already DNS-1123) when the caller did not set an explicit K8s name.
	t.OwnerType = subject.Type
	t.OwnerID = subject.ID
	t.CreatedByType = subject.Type
	t.CreatedBy = subject.ID
	if t.KubernetesName == "" {
		t.KubernetesName = t.Name
	}
	t.Status = SandboxTemplateStatusCreating
	t.Revision = 1

	// Step 4: INSERT row.
	created, err := uc.sandboxes.CreateSandboxTemplate(ctx, t)
	if err != nil {
		return nil, err
	}

	// Step 5: load cluster → locator → provider.ApplySandboxTemplate. The
	// cluster must be loaded to build a complete CredentialLocator (CredentialRef
	// + CredentialRevision are required by the AEAD credential store).
	cluster, err := uc.clusters.GetCluster(ctx, created.ClusterID)
	if err != nil {
		uc.log.WithContext(ctx).Warn("failed to load cluster for sandbox template apply; marking FAILED",
			logx.String("template_id", created.ID),
			logx.String("cluster_id", created.ClusterID),
			logx.Err(err))
		failed, _ := uc.sandboxes.UpdateSandboxTemplateStatus(ctx, created.ID, SandboxTemplateStatusFailed, err.Error())
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	spec := SandboxTemplateApplySpec{
		Name:      created.KubernetesName,
		Namespace: created.KubernetesNamespace,
		Image:     created.Image,
		Labels:    created.Labels,
	}
	if cmd := strings.TrimSpace(created.ContainerCommand); cmd != "" {
		spec.ContainerCommand = parseContainerCommand(cmd)
	}
	if err := uc.provider.ApplySandboxTemplate(ctx, created.ClusterID, locator, spec); err != nil {
		uc.log.WithContext(ctx).Warn("remote sandbox template apply failed; marking FAILED",
			logx.String("template_id", created.ID),
			logx.String("kube_name", created.KubernetesName),
			logx.Err(err))
		failed, _ := uc.sandboxes.UpdateSandboxTemplateStatus(ctx, created.ID, SandboxTemplateStatusFailed, err.Error())
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}

	// Step 6: stamp READY.
	ready, err := uc.sandboxes.UpdateSandboxTemplateStatus(ctx, created.ID, SandboxTemplateStatusReady, "")
	if err != nil {
		return created, nil
	}
	return ready, nil
}

// ListSandboxTemplates lists templates on a cluster. Templates are cluster-
// scoped, so a single `view` Check on the cluster gates the whole list (no
// per-template BatchCheck needed).
func (uc *SandboxUsecase) ListSandboxTemplates(ctx context.Context, principal authn.Principal, clusterID string) ([]*SandboxTemplate, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(clusterID),
		Permission: "view",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no view permission on cluster")
	}
	return uc.sandboxes.ListSandboxTemplatesByCluster(ctx, clusterID)
}

// GetSandboxTemplate loads a template + authorizes `view` on its parent cluster.
func (uc *SandboxUsecase) GetSandboxTemplate(ctx context.Context, principal authn.Principal, id string) (*SandboxTemplate, error) {
	t, err := uc.sandboxes.GetSandboxTemplate(ctx, id)
	if err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(t.ClusterID),
		Permission: "view",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no view permission on cluster")
	}
	return t, nil
}

// DeleteSandboxTemplate removes a template: authz `operate` on the cluster,
// best-effort remote CRD delete (log warning on failure so a stuck remote
// resource does not block DB cleanup), then CAS soft-delete the row.
func (uc *SandboxUsecase) DeleteSandboxTemplate(ctx context.Context, principal authn.Principal, id string, expectedRevision int64) (*SandboxTemplate, error) {
	t, err := uc.sandboxes.GetSandboxTemplate(ctx, id)
	if err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(t.ClusterID),
		Permission: "operate",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no operate permission on cluster")
	}

	// Best-effort remote delete. A failure is logged, not returned, so the DB
	// row is still cleaned up (operator may need to clean the stale CRD).
	cluster, clErr := uc.clusters.GetCluster(ctx, t.ClusterID)
	if clErr != nil {
		uc.log.WithContext(ctx).Warn("failed to load cluster for remote sandbox template delete; row will be soft-deleted",
			logx.String("template_id", id), logx.String("cluster_id", t.ClusterID), logx.Err(clErr))
	} else {
		locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
		if err := uc.provider.DeleteSandboxTemplate(ctx, t.ClusterID, locator, t.KubernetesNamespace, t.KubernetesName); err != nil {
			uc.log.WithContext(ctx).Warn("remote sandbox template delete failed; row will be soft-deleted, operator may need to clean up",
				logx.String("template_id", id), logx.String("kube_name", t.KubernetesName), logx.Err(err))
		}
	}

	return uc.sandboxes.DeleteSandboxTemplate(ctx, id, expectedRevision)
}

// ===================== Sandbox operations =====================

// CreateSandbox runs the create flow (design §11):
//  1. Validate name (DNS-1123) + namespace_id.
//  2. Load namespace → resolve cluster_id + kube_name.
//  3. Authz `use` on k8s_namespace:{namespace_id} (design §11: sandbox creation
//     is a namespace `use` privilege).
//  4. Stamp owner/created_by.
//  5. INSERT row (lifecycle=CREATING, revision=1).
//  6. Write SpiceDB: k8s_sandbox:{id}#owner@subject, k8s_sandbox:{id}#namespace
//     @k8s_namespace:{namespace_id}. Compensate (revoke + FAILED) on failure.
//  7. Load cluster → build locator → provider.ApplySandbox (spec.Namespace =
//     namespace.KubeName; TemplateRef from the referenced template's
//     KubernetesName when template_id is set).
//  8. On success: UpdateSandboxStatus(READY).
//     On failure: UpdateSandboxStatus(FAILED) + compensate SpiceDB.
func (uc *SandboxUsecase) CreateSandbox(ctx context.Context, principal authn.Principal, s *Sandbox) (*Sandbox, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil sandbox", ErrClusterInvalidArgument)
	}
	if s.ID == "" {
		return nil, fmt.Errorf("%w: sandbox id must be pre-allocated by caller", ErrClusterInvalidArgument)
	}
	if s.NamespaceID == "" {
		return nil, fmt.Errorf("%w: namespace_id is required", ErrClusterInvalidArgument)
	}
	if !isDNS1123Label(s.Name) {
		return nil, fmt.Errorf("%w: name must be a DNS-1123 label", ErrClusterInvalidArgument)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}

	// Step 2: load namespace → cluster_id + kube_name.
	ns, err := uc.namespaces.GetNamespace(ctx, s.NamespaceID)
	if err != nil {
		return nil, err
	}
	s.ClusterID = ns.ClusterID

	// Step 3: authz `use` on namespace.
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(s.NamespaceID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on namespace")
	}

	// Resolve the referenced Hub SandboxTemplate up front (validates it exists
	// and yields the image + command to inline into the Sandbox podTemplate).
	// The agent-sandbox Sandbox CRD inlines spec.podTemplate rather than
	// referencing a SandboxTemplate by name, so Hub carries the resolved image
	// + command through the apply spec. A missing template is a client error
	// surfaced before any DB write.
	var (
		templateImage   string
		templateCommand []string
	)
	if s.TemplateID != "" {
		tmpl, terr := uc.sandboxes.GetSandboxTemplate(ctx, s.TemplateID)
		if terr != nil {
			return nil, fmt.Errorf("%w: referenced template not found: %v", ErrClusterInvalidArgument, terr)
		}
		templateImage = tmpl.Image
		if cmd := strings.TrimSpace(tmpl.ContainerCommand); cmd != "" {
			templateCommand = parseContainerCommand(cmd)
		}
		// Merge template skills with inline skills (inline wins on name clash).
		s.Skills = mergeSandboxSkills(tmpl.Skills, s.Skills)
	}

	// Step 4: stamp owner/created_by + defaults.
	s.OwnerType = subject.Type
	s.OwnerID = subject.ID
	s.CreatedByType = subject.Type
	s.CreatedBy = subject.ID
	if s.KubernetesName == "" {
		s.KubernetesName = s.Name
	}
	if s.NetworkMode == "" {
		s.NetworkMode = SandboxNetworkModeOffline
	}
	if s.OperatingMode == "" {
		s.OperatingMode = SandboxOperatingModeRunning
	}
	s.Lifecycle = SandboxLifecycleCreating
	s.Revision = 1
	// Stamp a Hub-managed label selecting this sandbox's pod (propagated to the
	// pod by the operator) so SetSandboxNetworkMode's CiliumNetworkPolicy
	// endpointSelector can target exactly this sandbox.
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	s.Labels["aisphere.io/sandbox-id"] = s.ID

	// Step 5: INSERT row.
	created, err := uc.sandboxes.CreateSandbox(ctx, s)
	if err != nil {
		return nil, err
	}

	// Step 6: SpiceDB owner + namespace relationships.
	resource := sandboxResource(created.ID)
	if _, err := uc.rels.WriteRelationships(ctx,
		AuthzRelationship{Resource: resource, Relation: "owner", Subject: subject},
		AuthzRelationship{Resource: resource, Relation: "namespace", Subject: AuthzSubjectRef{Type: "k8s_namespace", ID: created.NamespaceID}},
	); err != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.rels.RevokeResource(compensateCtx, resource)
		_, _ = uc.sandboxes.UpdateSandboxStatus(compensateCtx, created.ID, SandboxLifecycleFailed, err.Error(), nil)
		return nil, fmt.Errorf("%w: project relationships: %v", ErrClusterFailedPrecondition, err)
	}

	// Step 7: load cluster → locator → provider.ApplySandbox.
	cluster, err := uc.clusters.GetCluster(ctx, created.ClusterID)
	if err != nil {
		uc.log.WithContext(ctx).Warn("failed to load cluster for sandbox apply; marking FAILED",
			logx.String("sandbox_id", created.ID),
			logx.String("cluster_id", created.ClusterID),
			logx.Err(err))
		failed, _ := uc.sandboxes.UpdateSandboxStatus(ctx, created.ID, SandboxLifecycleFailed, err.Error(), nil)
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.rels.RevokeResource(compensateCtx, resource)
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	applySpec := SandboxApplySpec{
		Name:            created.KubernetesName,
		Namespace:       ns.KubeName,
		Image:           templateImage,
		ContainerCommand: templateCommand,
		OperatingMode:   created.OperatingMode,
		Labels:          created.Labels,
		SkillAnnotations: sandboxSkillAnnotations(created.Skills),
	}
	if err := uc.provider.ApplySandbox(ctx, created.ClusterID, locator, applySpec); err != nil {
		// Remote apply failed → mark FAILED + compensate SpiceDB (design §11).
		uc.log.WithContext(ctx).Warn("remote sandbox apply failed; marking FAILED",
			logx.String("sandbox_id", created.ID),
			logx.String("kube_name", created.KubernetesName),
			logx.Err(err))
		failed, _ := uc.sandboxes.UpdateSandboxStatus(ctx, created.ID, SandboxLifecycleFailed, err.Error(), nil)
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.rels.RevokeResource(compensateCtx, resource)
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}

	// Step 8: stamp READY.
	ready, err := uc.sandboxes.UpdateSandboxStatus(ctx, created.ID, SandboxLifecycleReady, "", nil)
	if err != nil {
		return created, nil
	}
	return ready, nil
}

// ListSandboxes lists sandboxes in a namespace, gated by `use` on the namespace.
func (uc *SandboxUsecase) ListSandboxes(ctx context.Context, principal authn.Principal, namespaceID string) ([]*Sandbox, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(namespaceID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on namespace")
	}
	return uc.sandboxes.ListSandboxesByNamespace(ctx, namespaceID)
}

// GetSandbox loads a sandbox + authorizes `use` on k8s_sandbox:{id}.
func (uc *SandboxUsecase) GetSandbox(ctx context.Context, principal authn.Principal, id string) (*Sandbox, error) {
	s, err := uc.sandboxes.GetSandbox(ctx, id)
	if err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   sandboxResource(s.ID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on sandbox")
	}
	return s, nil
}

// DeleteSandbox removes a sandbox: authz `manage` on k8s_sandbox:{id}, best-
// effort remote CRD delete, CAS soft-delete the row, then revoke all SpiceDB
// rels on the sandbox object. The remote delete is best-effort so a stuck CRD
// does not block DB/spicedb cleanup (operator may need to clean the stale pod).
func (uc *SandboxUsecase) DeleteSandbox(ctx context.Context, principal authn.Principal, id string, expectedRevision int64) (*Sandbox, error) {
	s, err := uc.sandboxes.GetSandbox(ctx, id)
	if err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   sandboxResource(s.ID),
		Permission: "manage",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no manage permission on sandbox")
	}

	// Best-effort remote delete. The K8s namespace name lives on the Hub
	// namespace row, so load it (and the cluster for the locator). Failures are
	// logged, not returned, so DB cleanup still proceeds.
	cluster, clErr := uc.clusters.GetCluster(ctx, s.ClusterID)
	ns, nsErr := uc.namespaces.GetNamespace(ctx, s.NamespaceID)
	if clErr != nil || nsErr != nil {
		uc.log.WithContext(ctx).Warn("failed to load cluster/namespace for remote sandbox delete; row will be soft-deleted",
			logx.String("sandbox_id", id),
			logx.String("cluster_id", s.ClusterID),
			logx.String("namespace_id", s.NamespaceID),
			logx.Err(clErr),
			logx.Err(nsErr))
	} else {
		locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
		if err := uc.provider.DeleteSandbox(ctx, s.ClusterID, locator, ns.KubeName, s.KubernetesName); err != nil {
			uc.log.WithContext(ctx).Warn("remote sandbox delete failed; row will be soft-deleted, operator may need to clean up",
				logx.String("sandbox_id", id), logx.String("kube_name", s.KubernetesName), logx.Err(err))
		}
	}

	deleted, err := uc.sandboxes.DeleteSandbox(ctx, id, expectedRevision)
	if err != nil {
		return nil, err
	}
	// Revoke all SpiceDB rels on the sandbox object (idempotent).
	compensateCtx := context.WithoutCancel(ctx)
	_ = uc.rels.RevokeResource(compensateCtx, sandboxResource(id))
	return deleted, nil
}

// SuspendSandbox stops a READY sandbox's pod while retaining its PVC/state by
// patching the CRD spec.operatingMode to "Suspended" (SSA). The Hub row
// transitions lifecycle READY -> SUSPENDED. Authz: `manage` on k8s_sandbox.
func (uc *SandboxUsecase) SuspendSandbox(ctx context.Context, principal authn.Principal, id string, expectedRevision int64) (*Sandbox, error) {
	return uc.setSandboxOperatingMode(ctx, principal, id, expectedRevision, SandboxLifecycleReady, SandboxLifecycleSuspended, SandboxOperatingModeSuspended, "suspend")
}

// ResumeSandbox reverses SuspendSandbox: operatingMode -> "Running", lifecycle
// SUSPENDED -> READY. Authz: `manage` on k8s_sandbox.
func (uc *SandboxUsecase) ResumeSandbox(ctx context.Context, principal authn.Principal, id string, expectedRevision int64) (*Sandbox, error) {
	return uc.setSandboxOperatingMode(ctx, principal, id, expectedRevision, SandboxLifecycleSuspended, SandboxLifecycleReady, SandboxOperatingModeRunning, "resume")
}

// setSandboxOperatingMode is the shared core of Suspend/Resume. It validates
// the lifecycle transition, re-applies the Sandbox CRD with a new
// spec.operatingMode (SSA patch — podTemplate is rebuilt from the Hub row so
// the field owner does not drop it), and CAS-stamps the new lifecycle +
// operating_mode. On remote-apply failure the DB lifecycle is left unchanged
// (the sandbox is not broken, the patch is retriable).
func (uc *SandboxUsecase) setSandboxOperatingMode(ctx context.Context, principal authn.Principal, id string, expectedRevision int64, fromLifecycle, toLifecycle, toMode, verb string) (*Sandbox, error) {
	s, err := uc.sandboxes.GetSandbox(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Lifecycle != fromLifecycle {
		return nil, fmt.Errorf("%w: only %s sandboxes can be %sed (current: %s)", ErrClusterInvalidArgument, fromLifecycle, verb, s.Lifecycle)
	}
	// CAS pre-check: UpdateSandboxStatus does not guard expected_revision, so
	// verify here before mutating (mirrors DeleteSandbox's CAS expectation).
	if s.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: sandbox revision mismatch", ErrClusterRevisionConflict)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   sandboxResource(s.ID),
		Permission: "manage",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no manage permission on sandbox")
	}

	// Rebuild the SandboxApplySpec so the SSA patch does not drop fields Hub
	// owns. CreateSandbox only writes podTemplate (image+command) for
	// template-created sandboxes; claim-allocated sandboxes have TemplateID=""
	// and their podTemplate is owned by the controller, so Hub omits it (SSA
	// leaves fields it does not own untouched). For template sandboxes, reload
	// the image+command from the template exactly as CreateSandbox did.
	applySpec := SandboxApplySpec{
		Name:             s.KubernetesName,
		OperatingMode:    toMode,
		Labels:           s.Labels,
		SkillAnnotations: sandboxSkillAnnotations(s.Skills),
	}
	if s.TemplateID != "" {
		tmpl, terr := uc.sandboxes.GetSandboxTemplate(ctx, s.TemplateID)
		if terr != nil {
			return nil, fmt.Errorf("%w: referenced template not found: %v", ErrClusterInvalidArgument, terr)
		}
		applySpec.Image = tmpl.Image
		if cmd := strings.TrimSpace(tmpl.ContainerCommand); cmd != "" {
			applySpec.ContainerCommand = parseContainerCommand(cmd)
		}
	}

	ns, err := uc.namespaces.GetNamespace(ctx, s.NamespaceID)
	if err != nil {
		return nil, err
	}
	cluster, err := uc.clusters.GetCluster(ctx, s.ClusterID)
	if err != nil {
		return nil, err
	}
	applySpec.Namespace = ns.KubeName
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	if err := uc.provider.ApplySandbox(ctx, s.ClusterID, locator, applySpec); err != nil {
		// Remote patch failed — do not flip lifecycle; the sandbox is still in
		// its original state and the caller can retry.
		uc.log.WithContext(ctx).Warn("remote sandbox operating-mode patch failed; lifecycle unchanged",
			logx.String("sandbox_id", id),
			logx.String("kube_name", s.KubernetesName),
			logx.String("verb", verb),
			logx.String("to_mode", toMode),
			logx.Err(err))
		return nil, fmt.Errorf("%w: remote %s: %v", ErrClusterFailedPrecondition, verb, err)
	}

	updated, err := uc.sandboxes.UpdateSandboxStatus(ctx, id, toLifecycle, "", map[string]any{
		"operating_mode": toMode,
		"last_sync_at":   time.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// SetSandboxNetworkMode toggles a sandbox's network egress. OFFLINE applies a
// CiliumNetworkPolicy egressDeny (Cilium's deny rules override the operator's
// per-template allow policy, which standard NetworkPolicy cannot — Cilium
// unions allow rules); ONLINE removes it. The Hub row's network_mode is
// stamped to match. Authz: `manage` on k8s_sandbox.
func (uc *SandboxUsecase) SetSandboxNetworkMode(ctx context.Context, principal authn.Principal, id string, expectedRevision int64, mode string) (*Sandbox, error) {
	if mode != SandboxNetworkModeOffline && mode != SandboxNetworkModeOnline {
		return nil, fmt.Errorf("%w: network_mode must be OFFLINE or ONLINE", ErrClusterInvalidArgument)
	}
	s, err := uc.sandboxes.GetSandbox(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Lifecycle == SandboxLifecycleDeleted || s.Lifecycle == SandboxLifecycleTerminating {
		return nil, fmt.Errorf("%w: cannot change network mode on %s sandbox", ErrClusterInvalidArgument, s.Lifecycle)
	}
	if s.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: sandbox revision mismatch", ErrClusterRevisionConflict)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   sandboxResource(s.ID),
		Permission: "manage",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no manage permission on sandbox")
	}

	ns, err := uc.namespaces.GetNamespace(ctx, s.NamespaceID)
	if err != nil {
		return nil, err
	}
	cluster, err := uc.clusters.GetCluster(ctx, s.ClusterID)
	if err != nil {
		return nil, err
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	if err := uc.provider.ApplySandboxEgressPolicy(ctx, s.ClusterID, locator, ns.KubeName, s.KubernetesName, s.ID, mode); err != nil {
		uc.log.WithContext(ctx).Warn("remote sandbox egress policy apply failed; network_mode unchanged",
			logx.String("sandbox_id", id),
			logx.String("kube_name", s.KubernetesName),
			logx.String("mode", mode),
			logx.Err(err))
		return nil, fmt.Errorf("%w: set network mode: %v", ErrClusterFailedPrecondition, err)
	}

	updated, err := uc.sandboxes.UpdateSandboxStatus(ctx, id, s.Lifecycle, "", map[string]any{
		"network_mode":  mode,
		"last_sync_at":  time.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// SyncSandboxes reconciles DB rows with the remote Sandbox CRDs in a namespace
// (design §11 sync): authz `operate` on the namespace, list remote sandboxes
// from the cluster, diff against local rows, then upsert (import) new ones,
// update changed ones, and remove (soft-delete + revoke) local ones that no
// longer exist remotely. Returns counts. Imported sandboxes inherit the
// namespace owner and project k8s_sandbox SpiceDB rels best-effort so they are
// immediately visible to the namespace owner.
func (uc *SandboxUsecase) SyncSandboxes(ctx context.Context, principal authn.Principal, namespaceID string) (imported, updated, removed int, err error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return 0, 0, 0, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(namespaceID),
		Permission: "operate",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return 0, 0, 0, err
	}
	if !dec.Allowed {
		return 0, 0, 0, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no operate permission on namespace")
	}
	return uc.syncSandboxesCore(ctx, namespaceID)
}

// syncSandboxesCore is the principal-free core of SyncSandboxes: load the
// namespace/cluster, list remote Sandbox CRDs, diff against local rows, import
// new ones (inheriting the namespace owner), update changed ones, and remove
// local ones no longer present remotely. Extracted for the SandboxReconciler
// (background workers run with system-level trust, mirroring VisibilityReconciler).
func (uc *SandboxUsecase) syncSandboxesCore(ctx context.Context, namespaceID string) (imported, updated, removed int, err error) {
	ns, err := uc.namespaces.GetNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, 0, err
	}
	cluster, err := uc.clusters.GetCluster(ctx, ns.ClusterID)
	if err != nil {
		return 0, 0, 0, err
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	remote, err := uc.provider.ListSandboxes(ctx, ns.ClusterID, locator, ns.KubeName)
	if err != nil {
		return 0, 0, 0, err
	}
	local, err := uc.sandboxes.ListSandboxesByNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, 0, err
	}

	localByName := make(map[string]*Sandbox, len(local))
	for _, s := range local {
		localByName[s.KubernetesName] = s
	}
	remoteByName := make(map[string]bool, len(remote))
	now := time.Now().UTC()

	for _, r := range remote {
		remoteByName[r.Name] = true
		if existing, ok := localByName[r.Name]; ok {
			// Update observed runtime state.
			fields := map[string]any{
				"kubernetes_uid":   r.UID,
				"resource_version": r.ResourceVersion,
				"pod_name":         r.PodName,
				"pod_ip":           r.PodIP,
				"node_name":        r.NodeName,
				"image":            r.Image,
				"last_sync_at":     now,
			}
			if _, e := uc.sandboxes.UpdateSandboxSync(ctx, existing.ID, fields); e != nil {
				uc.log.WithContext(ctx).Warn("sync: update failed",
					logx.String("sandbox_id", existing.ID), logx.String("kube_name", r.Name), logx.Err(e))
				continue
			}
			updated++
			continue
		}
		// Import: a remote sandbox with no matching Hub row. Inherit the
		// namespace owner so the sandbox is visible to whoever owns the
		// namespace; project SpiceDB best-effort (reconciler converges on miss).
		imp := &Sandbox{
			ID:              uuid.NewString(),
			NamespaceID:     namespaceID,
			ClusterID:       ns.ClusterID,
			OrgID:           cluster.OrgID,
			Name:            r.Name,
			KubernetesName:  r.Name,
			KubernetesUID:   r.UID,
			ResourceVersion: r.ResourceVersion,
			PodName:         r.PodName,
			PodIP:           r.PodIP,
			NodeName:        r.NodeName,
			Image:           r.Image,
			Labels:          r.Labels,
			Lifecycle:       SandboxLifecycleReady,
			NetworkMode:     SandboxNetworkModeOffline,
			OperatingMode:   SandboxOperatingModeRunning,
			OwnerType:       ns.OwnerType,
			OwnerID:         ns.OwnerID,
			CreatedByType:   ns.OwnerType,
			CreatedBy:       ns.OwnerID,
			Revision:        1,
			LastSyncAt:      now,
		}
		created, cerr := uc.sandboxes.CreateSandbox(ctx, imp)
		if cerr != nil {
			uc.log.WithContext(ctx).Warn("sync: import create failed",
				logx.String("kube_name", r.Name), logx.Err(cerr))
			continue
		}
		imported++
		if _, werr := uc.rels.WriteRelationships(ctx,
			AuthzRelationship{Resource: sandboxResource(created.ID), Relation: "owner", Subject: AuthzSubjectRef{Type: ns.OwnerType, ID: ns.OwnerID}},
			AuthzRelationship{Resource: sandboxResource(created.ID), Relation: "namespace", Subject: AuthzSubjectRef{Type: "k8s_namespace", ID: namespaceID}},
		); werr != nil {
			uc.log.WithContext(ctx).Warn("sync: import SpiceDB projection failed; reconciler will converge",
				logx.String("sandbox_id", created.ID), logx.String("kube_name", r.Name), logx.Err(werr))
		}
	}

	// Remove local sandboxes no longer present remotely. Skip rows that are
	// mid-flight (CREATING) or already DELETED so we never clobber an in-progress
	// create or a finalized delete.
	for name, s := range localByName {
		if remoteByName[name] {
			continue
		}
		if s.Lifecycle == SandboxLifecycleCreating || s.Lifecycle == SandboxLifecycleDeleted {
			continue
		}
		if _, derr := uc.sandboxes.DeleteSandbox(ctx, s.ID, s.Revision); derr != nil {
			uc.log.WithContext(ctx).Warn("sync: remove failed",
				logx.String("sandbox_id", s.ID), logx.String("kube_name", name), logx.Err(derr))
			continue
		}
		removed++
		_ = uc.rels.RevokeResource(ctx, sandboxResource(s.ID))
	}
	return imported, updated, removed, nil
}

// ===================== WarmPool operations =====================
//
// WarmPools are namespace-scoped infra addressed through the namespace
// permission (no per-warm-pool SpiceDB object). Create uses `use` on the
// namespace (mirroring CreateSandbox); delete uses `operate` (operator-tier
// destructive action, mirroring template management).

// CreateWarmPool runs the create flow: validate, load namespace + template,
// authz `use` on namespace, stamp owner, INSERT (status=CREATING), load cluster
// → locator → provider.ApplyWarmPool, stamp READY (or DEGRADED on failure).
func (uc *SandboxUsecase) CreateWarmPool(ctx context.Context, principal authn.Principal, w *WarmPool) (*WarmPool, error) {
	if w == nil {
		return nil, fmt.Errorf("%w: nil warm pool", ErrClusterInvalidArgument)
	}
	if w.ID == "" {
		return nil, fmt.Errorf("%w: warm pool id must be pre-allocated by caller", ErrClusterInvalidArgument)
	}
	if w.NamespaceID == "" {
		return nil, fmt.Errorf("%w: namespace_id is required", ErrClusterInvalidArgument)
	}
	if w.TemplateID == "" {
		return nil, fmt.Errorf("%w: template_id is required", ErrClusterInvalidArgument)
	}
	if !isDNS1123Label(w.Name) {
		return nil, fmt.Errorf("%w: name must be a DNS-1123 label", ErrClusterInvalidArgument)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}

	ns, err := uc.namespaces.GetNamespace(ctx, w.NamespaceID)
	if err != nil {
		return nil, err
	}
	w.ClusterID = ns.ClusterID

	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(w.NamespaceID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on namespace")
	}

	// Resolve the template's K8s name for the WarmPoolApplySpec.TemplateRef.
	tmpl, terr := uc.sandboxes.GetSandboxTemplate(ctx, w.TemplateID)
	if terr != nil {
		return nil, fmt.Errorf("%w: referenced template not found: %v", ErrClusterInvalidArgument, terr)
	}

	w.OwnerType = subject.Type
	w.OwnerID = subject.ID
	w.CreatedByType = subject.Type
	w.CreatedBy = subject.ID
	if w.KubernetesName == "" {
		w.KubernetesName = w.Name
	}
	w.Status = WarmPoolStatusCreating
	w.Revision = 1

	created, err := uc.sandboxes.CreateWarmPool(ctx, w)
	if err != nil {
		return nil, err
	}

	cluster, err := uc.clusters.GetCluster(ctx, created.ClusterID)
	if err != nil {
		uc.log.WithContext(ctx).Warn("failed to load cluster for warm pool apply; marking DEGRADED",
			logx.String("warm_pool_id", created.ID),
			logx.String("cluster_id", created.ClusterID),
			logx.Err(err))
		failed, _ := uc.sandboxes.UpdateWarmPoolStatus(ctx, created.ID, WarmPoolStatusDegraded, map[string]any{
			"health_message": err.Error(),
		})
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	spec := WarmPoolApplySpec{
		Name:        created.KubernetesName,
		Namespace:   ns.KubeName,
		TemplateRef: tmpl.KubernetesName,
		Replicas:    created.Replicas,
	}
	if err := uc.provider.ApplyWarmPool(ctx, created.ClusterID, locator, spec); err != nil {
		uc.log.WithContext(ctx).Warn("remote warm pool apply failed; marking DEGRADED",
			logx.String("warm_pool_id", created.ID),
			logx.String("kube_name", created.KubernetesName),
			logx.Err(err))
		failed, _ := uc.sandboxes.UpdateWarmPoolStatus(ctx, created.ID, WarmPoolStatusDegraded, map[string]any{
			"health_message": err.Error(),
		})
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}

	// Apply only means the API server accepted the CRD; Ready must be driven by
	// status.readyReplicas == replicas via SyncWarmPools (or a future reconciler).
	// Keep status=CREATING (set above) and return the row as-is.
	return created, nil
}

// SyncWarmPools reconciles Hub-side WarmPool rows with the observed CRD status
// (status.readyReplicas). It mirrors SyncSandboxes' shape (List CRD -> diff by
// KubernetesName -> update/remove) but:
//   - does NOT import remote-only pools (WarmPools are explicit infra, created
//     via Hub; a drift here is logged, not auto-imported);
//   - drives status from readyReplicas: READY when ready==replicas, CREATING
//     while warming up. A row already DEGRADED from a failed Apply is only
//     promoted to READY once the pool is actually ready.
//
// Authz: `operate` on the namespace (operator-tier reconciliation, mirroring
// SyncSandboxes and WarmPool delete).
func (uc *SandboxUsecase) SyncWarmPools(ctx context.Context, principal authn.Principal, namespaceID string) (updated, removed int, err error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return 0, 0, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(namespaceID),
		Permission: "operate",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return 0, 0, err
	}
	if !dec.Allowed {
		return 0, 0, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no operate permission on namespace")
	}
	return uc.syncWarmPoolsCore(ctx, namespaceID)
}

// syncWarmPoolsCore is the principal-free core of SyncWarmPools: load the
// namespace/cluster, list remote WarmPool CRDs, and drive Hub-side status from
// observed readyReplicas. Extracted so the background SandboxReconciler can
// converge namespaces without re-running authz (background workers run with
// system-level trust, mirroring VisibilityReconciler).
func (uc *SandboxUsecase) syncWarmPoolsCore(ctx context.Context, namespaceID string) (updated, removed int, err error) {
	ns, err := uc.namespaces.GetNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, err
	}
	cluster, err := uc.clusters.GetCluster(ctx, ns.ClusterID)
	if err != nil {
		return 0, 0, err
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	remote, err := uc.provider.ListWarmPools(ctx, ns.ClusterID, locator, ns.KubeName)
	if err != nil {
		return 0, 0, err
	}
	local, err := uc.sandboxes.ListWarmPoolsByNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, err
	}

	localByName := make(map[string]*WarmPool, len(local))
	for _, w := range local {
		localByName[w.KubernetesName] = w
	}
	remoteByName := make(map[string]bool, len(remote))
	now := time.Now().UTC()

	for _, r := range remote {
		remoteByName[r.Name] = true
		existing, ok := localByName[r.Name]
		if !ok {
			// Remote-only pool: do not auto-import. WarmPools are explicit Hub
			// infra; surface the drift for an operator to investigate.
			uc.log.WithContext(ctx).Warn("sync warm pools: remote pool has no Hub row; skipping import",
				logx.String("kube_name", r.Name),
				logx.String("namespace", ns.KubeName),
				logx.String("template_ref", r.TemplateRef))
			continue
		}

		// Drive status from observed readyReplicas. Apply success only means
		// the API server accepted the CRD; Ready is readyReplicas == replicas.
		var status, health string
		switch {
		case r.Replicas > 0 && r.ReadyReplicas >= r.Replicas:
			status = WarmPoolStatusReady
			health = ""
		case r.ReadyReplicas > 0:
			status = WarmPoolStatusCreating
			health = fmt.Sprintf("warming up: %d/%d ready replicas", r.ReadyReplicas, r.Replicas)
		default:
			// 0 ready (or replicas==0). Keep CREATING; the controller has not
			// surfaced any ready pod yet. Do not overwrite a pre-existing
			// DEGRADED with CREATING unless we are promoting to READY above.
			if existing.Status == WarmPoolStatusDegraded {
				status = WarmPoolStatusDegraded
				health = existing.HealthMessage
			} else {
				status = WarmPoolStatusCreating
				health = "no ready replicas yet"
			}
		}

		fields := map[string]any{
			"kubernetes_uid":   r.UID,
			"resource_version": r.ResourceVersion,
			"replicas":         r.Replicas,
			"ready_replicas":   r.ReadyReplicas,
			"health_message":   health,
			"last_sync_at":     now,
		}
		if _, e := uc.sandboxes.UpdateWarmPoolStatus(ctx, existing.ID, status, fields); e != nil {
			uc.log.WithContext(ctx).Warn("sync warm pools: update failed",
				logx.String("warm_pool_id", existing.ID),
				logx.String("kube_name", r.Name),
				logx.Err(e))
			continue
		}
		updated++
	}

	// Remove local pools no longer present remotely. Skip rows that are
	// mid-flight (CREATING) or already DELETED so we never clobber an
	// in-progress create (the CRD may not have been listed yet) or a finalized
	// delete. WarmPools carry no per-resource SpiceDB object, so there is no
	// relationship to revoke.
	for name, w := range localByName {
		if remoteByName[name] {
			continue
		}
		if w.Status == WarmPoolStatusCreating || w.Status == WarmPoolStatusDeleted {
			continue
		}
		if _, derr := uc.sandboxes.DeleteWarmPool(ctx, w.ID, w.Revision); derr != nil {
			uc.log.WithContext(ctx).Warn("sync warm pools: remove failed",
				logx.String("warm_pool_id", w.ID),
				logx.String("kube_name", name),
				logx.Err(derr))
			continue
		}
		removed++
	}
	return updated, removed, nil
}

// ListWarmPools lists warm pools in a namespace, gated by `use` on the namespace.
func (uc *SandboxUsecase) ListWarmPools(ctx context.Context, principal authn.Principal, namespaceID string) ([]*WarmPool, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(namespaceID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on namespace")
	}
	return uc.sandboxes.ListWarmPoolsByNamespace(ctx, namespaceID)
}

// DeleteWarmPool removes a warm pool: authz `operate` on the namespace,
// best-effort remote CRD delete, then CAS soft-delete the row. The
// namespaceID from the request path is validated against the row's own
// namespace to prevent cross-namespace deletion via path tampering.
func (uc *SandboxUsecase) DeleteWarmPool(ctx context.Context, principal authn.Principal, namespaceID, id string, expectedRevision int64) (*WarmPool, error) {
	w, err := uc.sandboxes.GetWarmPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.NamespaceID != namespaceID {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: warm pool does not belong to the requested namespace")
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(w.NamespaceID),
		Permission: "operate",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no operate permission on namespace")
	}

	cluster, clErr := uc.clusters.GetCluster(ctx, w.ClusterID)
	ns, nsErr := uc.namespaces.GetNamespace(ctx, w.NamespaceID)
	if clErr != nil || nsErr != nil {
		uc.log.WithContext(ctx).Warn("failed to load cluster/namespace for remote warm pool delete; row will be soft-deleted",
			logx.String("warm_pool_id", id),
			logx.String("cluster_id", w.ClusterID),
			logx.String("namespace_id", w.NamespaceID),
			logx.Err(clErr),
			logx.Err(nsErr))
	} else {
		locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
		if err := uc.provider.DeleteWarmPool(ctx, w.ClusterID, locator, ns.KubeName, w.KubernetesName); err != nil {
			uc.log.WithContext(ctx).Warn("remote warm pool delete failed; row will be soft-deleted, operator may need to clean up",
				logx.String("warm_pool_id", id), logx.String("kube_name", w.KubernetesName), logx.Err(err))
		}
	}

	return uc.sandboxes.DeleteWarmPool(ctx, id, expectedRevision)
}

// ===================== SandboxClaim operations =====================
//
// SandboxClaims are namespace-scoped infra addressed through the namespace
// permission (no per-claim SpiceDB object), mirroring WarmPool.

// CreateSandboxClaim runs the create flow: validate, load namespace + warm pool,
// authz `use` on namespace, stamp owner, INSERT (status=PENDING), load cluster
// → locator → provider.ApplySandboxClaim, stamp READY (or FAILED on failure).
func (uc *SandboxUsecase) CreateSandboxClaim(ctx context.Context, principal authn.Principal, c *SandboxClaim) (*SandboxClaim, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil sandbox claim", ErrClusterInvalidArgument)
	}
	if c.ID == "" {
		return nil, fmt.Errorf("%w: sandbox claim id must be pre-allocated by caller", ErrClusterInvalidArgument)
	}
	if c.NamespaceID == "" {
		return nil, fmt.Errorf("%w: namespace_id is required", ErrClusterInvalidArgument)
	}
	if c.WarmPoolID == "" {
		return nil, fmt.Errorf("%w: warm_pool_id is required", ErrClusterInvalidArgument)
	}
	if !isDNS1123Label(c.Name) {
		return nil, fmt.Errorf("%w: name must be a DNS-1123 label", ErrClusterInvalidArgument)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}

	ns, err := uc.namespaces.GetNamespace(ctx, c.NamespaceID)
	if err != nil {
		return nil, err
	}
	c.ClusterID = ns.ClusterID

	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(c.NamespaceID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on namespace")
	}

	// Resolve the warm pool's K8s name for SandboxClaimApplySpec.WarmPoolRef.
	pool, perr := uc.sandboxes.GetWarmPool(ctx, c.WarmPoolID)
	if perr != nil {
		return nil, fmt.Errorf("%w: referenced warm pool not found: %v", ErrClusterInvalidArgument, perr)
	}

	c.OwnerType = subject.Type
	c.OwnerID = subject.ID
	c.CreatedByType = subject.Type
	c.CreatedBy = subject.ID
	if c.KubernetesName == "" {
		c.KubernetesName = c.Name
	}
	c.Status = SandboxClaimStatusPending
	c.Revision = 1

	created, err := uc.sandboxes.CreateSandboxClaim(ctx, c)
	if err != nil {
		return nil, err
	}

	cluster, err := uc.clusters.GetCluster(ctx, created.ClusterID)
	if err != nil {
		uc.log.WithContext(ctx).Warn("failed to load cluster for sandbox claim apply; marking FAILED",
			logx.String("claim_id", created.ID),
			logx.String("cluster_id", created.ClusterID),
			logx.Err(err))
		failed, _ := uc.sandboxes.UpdateSandboxClaimStatus(ctx, created.ID, SandboxClaimStatusFailed, map[string]any{
			"health_message": err.Error(),
		})
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	spec := SandboxClaimApplySpec{
		Name:        created.KubernetesName,
		Namespace:   ns.KubeName,
		WarmPoolRef: pool.KubernetesName,
	}
	if err := uc.provider.ApplySandboxClaim(ctx, created.ClusterID, locator, spec); err != nil {
		uc.log.WithContext(ctx).Warn("remote sandbox claim apply failed; marking FAILED",
			logx.String("claim_id", created.ID),
			logx.String("kube_name", created.KubernetesName),
			logx.Err(err))
		failed, _ := uc.sandboxes.UpdateSandboxClaimStatus(ctx, created.ID, SandboxClaimStatusFailed, map[string]any{
			"health_message": err.Error(),
		})
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}

	// Apply only means the API server accepted the CRD; Ready is driven by the
	// controller allocating a sandbox (status.sandbox.name + Ready condition)
	// via SyncSandboxClaims (or a future reconciler). Keep status=PENDING (set
	// above) and return the row as-is.
	return created, nil
}

// SyncSandboxClaims reconciles Hub-side SandboxClaim rows with the observed
// CRD status (status.conditions[Ready], status.sandbox.name). When a claim
// becomes Ready and the controller has filled status.sandbox.name, the sandbox
// name/podIP are mirrored onto the claim and a Hub Sandbox row is created
// (template_id=NULL, warm_pool_id+claim_id set) so the delivered sandbox is
// operable from Hub.
//
// Mirrors SyncSandboxes/SyncWarmPools (List CRD -> diff by KubernetesName ->
// update/remove) but adds the sandbox-linkage step and does NOT import
// remote-only claims. Authz: `operate` on the namespace.
func (uc *SandboxUsecase) SyncSandboxClaims(ctx context.Context, principal authn.Principal, namespaceID string) (updated, removed, sandboxesLinked int, err error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return 0, 0, 0, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(namespaceID),
		Permission: "operate",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return 0, 0, 0, err
	}
	if !dec.Allowed {
		return 0, 0, 0, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no operate permission on namespace")
	}
	return uc.syncSandboxClaimsCore(ctx, namespaceID)
}

// syncSandboxClaimsCore is the principal-free core of SyncSandboxClaims: load
// the namespace/cluster, list remote SandboxClaim CRDs, drive claim status from
// the Ready condition + allocated sandbox name, and ensure a Hub Sandbox row
// exists for each delivered sandbox (back-linked to the claim). Extracted for
// the SandboxReconciler (background workers run with system-level trust,
// mirroring VisibilityReconciler).
func (uc *SandboxUsecase) syncSandboxClaimsCore(ctx context.Context, namespaceID string) (updated, removed, sandboxesLinked int, err error) {
	ns, err := uc.namespaces.GetNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, 0, err
	}
	cluster, err := uc.clusters.GetCluster(ctx, ns.ClusterID)
	if err != nil {
		return 0, 0, 0, err
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	remote, err := uc.provider.ListSandboxClaims(ctx, ns.ClusterID, locator, ns.KubeName)
	if err != nil {
		return 0, 0, 0, err
	}
	local, err := uc.sandboxes.ListSandboxClaimsByNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, 0, err
	}
	// Existing Hub sandboxes in this namespace, keyed by KubernetesName, so we
	// can tell whether the controller-allocated sandbox already has a Hub row.
	existingSandboxes, err := uc.sandboxes.ListSandboxesByNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, 0, err
	}
	sandboxByKubeName := make(map[string]*Sandbox, len(existingSandboxes))
	for _, s := range existingSandboxes {
		sandboxByKubeName[s.KubernetesName] = s
	}

	localByName := make(map[string]*SandboxClaim, len(local))
	for _, c := range local {
		localByName[c.KubernetesName] = c
	}
	remoteByName := make(map[string]bool, len(remote))
	now := time.Now().UTC()

	for _, r := range remote {
		remoteByName[r.Name] = true
		existing, ok := localByName[r.Name]
		if !ok {
			uc.log.WithContext(ctx).Warn("sync sandbox claims: remote claim has no Hub row; skipping import",
				logx.String("kube_name", r.Name),
				logx.String("namespace", ns.KubeName),
				logx.String("warm_pool_ref", r.WarmPoolRef))
			continue
		}

		// Drive status from the Ready condition + allocated sandbox name.
		var status, health string
		switch {
		case r.Ready && r.SandboxName != "":
			status = SandboxClaimStatusReady
			health = ""
		case r.Ready && r.SandboxName == "":
			// Ready condition true but no sandbox name yet — still allocating.
			status = SandboxClaimStatusPending
			health = "ready condition true but sandbox not yet allocated"
		default:
			// Not ready. Keep a pre-existing FAILED's health_message; otherwise
			// PENDING (do not clobber a controller-reported failure).
			if existing.Status == SandboxClaimStatusFailed {
				status = SandboxClaimStatusFailed
				health = existing.HealthMessage
			} else {
				status = SandboxClaimStatusPending
				health = "waiting for warm pool to allocate a sandbox"
			}
		}

		fields := map[string]any{
			"kubernetes_uid":   r.UID,
			"resource_version": r.ResourceVersion,
			"sandbox_kube_name": r.SandboxName,
			"sandbox_pod_ip":   r.SandboxPodIP,
			"health_message":   health,
			"last_sync_at":     now,
		}

		// Sandbox linkage: when the controller has allocated a sandbox, ensure a
		// Hub Sandbox row exists and is back-linked to this claim.
		if r.SandboxName != "" {
			if hubSandbox, ok := sandboxByKubeName[r.SandboxName]; ok {
				// A Hub Sandbox row already exists for this kube_name (e.g. a
				// prior sync, or SyncSandboxes imported it). Link it to this
				// claim if not already, and refresh observed pod_ip.
				if hubSandbox.ClaimID == "" {
					hubSandbox.ClaimID = existing.ID
					hubSandbox.WarmPoolID = existing.WarmPoolID
					if r.SandboxPodIP != "" {
						hubSandbox.PodIP = r.SandboxPodIP
					}
					hubSandbox.ResourceVersion = r.ResourceVersion
					hubSandbox.LastSyncAt = now
					if _, e := uc.sandboxes.UpdateSandboxSync(ctx, hubSandbox.ID, map[string]any{
						"claim_id":        existing.ID,
						"warm_pool_id":    existing.WarmPoolID,
						"pod_ip":          hubSandbox.PodIP,
						"resource_version": r.ResourceVersion,
						"last_sync_at":    now,
					}); e != nil {
						uc.log.WithContext(ctx).Warn("sync sandbox claims: link existing sandbox failed",
							logx.String("claim_id", existing.ID),
							logx.String("sandbox_kube_name", r.SandboxName),
							logx.Err(e))
					} else {
						sandboxesLinked++
					}
				}
				fields["sandbox_id"] = hubSandbox.ID
			} else {
				// No Hub Sandbox row yet — create one representing the delivered
				// sandbox. template_id is NULL (derived from a warm pool, not a
				// direct template create); warm_pool_id + claim_id link it back.
				created, cerr := uc.sandboxes.CreateSandbox(ctx, &Sandbox{
					ID:              uuid.NewString(),
					NamespaceID:     namespaceID,
					ClusterID:       ns.ClusterID,
					OrgID:           cluster.OrgID,
					Name:            r.SandboxName,
					KubernetesName:  r.SandboxName,
					KubernetesUID:   r.UID,
					ResourceVersion: r.ResourceVersion,
					WarmPoolID:      existing.WarmPoolID,
					ClaimID:         existing.ID,
					PodIP:           r.SandboxPodIP,
					Lifecycle:       SandboxLifecycleReady,
					OperatingMode:   SandboxOperatingModeRunning,
					NetworkMode:     SandboxNetworkModeOffline,
					OwnerType:       existing.OwnerType,
					OwnerID:         existing.OwnerID,
					CreatedByType:   existing.OwnerType,
					CreatedBy:       existing.OwnerID,
					Revision:        1,
					LastSyncAt:      now,
				})
				if cerr != nil {
					uc.log.WithContext(ctx).Warn("sync sandbox claims: create linked sandbox failed",
						logx.String("claim_id", existing.ID),
						logx.String("sandbox_kube_name", r.SandboxName),
						logx.Err(cerr))
				} else {
					sandboxByKubeName[r.SandboxName] = created
					fields["sandbox_id"] = created.ID
					sandboxesLinked++
					// Project SpiceDB relationships best-effort (reconciler
					// converges on miss) so the claim owner can operate the
					// delivered sandbox.
					if _, werr := uc.rels.WriteRelationships(ctx,
						AuthzRelationship{Resource: sandboxResource(created.ID), Relation: "owner", Subject: AuthzSubjectRef{Type: existing.OwnerType, ID: existing.OwnerID}},
						AuthzRelationship{Resource: sandboxResource(created.ID), Relation: "namespace", Subject: AuthzSubjectRef{Type: "k8s_namespace", ID: namespaceID}},
					); werr != nil {
						uc.log.WithContext(ctx).Warn("sync sandbox claims: SpiceDB projection failed; reconciler will converge",
							logx.String("sandbox_id", created.ID),
							logx.String("claim_id", existing.ID),
							logx.Err(werr))
					}
				}
			}
		}

		if _, e := uc.sandboxes.UpdateSandboxClaimStatus(ctx, existing.ID, status, fields); e != nil {
			uc.log.WithContext(ctx).Warn("sync sandbox claims: update failed",
				logx.String("claim_id", existing.ID),
				logx.String("kube_name", r.Name),
				logx.Err(e))
			continue
		}
		updated++
	}

	// Remove local claims no longer present remotely. Skip PENDING/DELETED so
	// an in-flight claim (CRD not yet listed) is never clobbered. Deleting a
	// claim does NOT cascade to the delivered sandbox (plan: default retain).
	for name, c := range localByName {
		if remoteByName[name] {
			continue
		}
		if c.Status == SandboxClaimStatusPending || c.Status == SandboxClaimStatusDeleted {
			continue
		}
		if _, derr := uc.sandboxes.DeleteSandboxClaim(ctx, c.ID, c.Revision); derr != nil {
			uc.log.WithContext(ctx).Warn("sync sandbox claims: remove failed",
				logx.String("claim_id", c.ID),
				logx.String("kube_name", name),
				logx.Err(derr))
			continue
		}
		removed++
	}
	return updated, removed, sandboxesLinked, nil
}

// ListSandboxClaims lists claims in a namespace, gated by `use` on the namespace.
func (uc *SandboxUsecase) ListSandboxClaims(ctx context.Context, principal authn.Principal, namespaceID string) ([]*SandboxClaim, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(namespaceID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on namespace")
	}
	return uc.sandboxes.ListSandboxClaimsByNamespace(ctx, namespaceID)
}

// DeleteSandboxClaim removes a claim: authz `operate` on the namespace,
// best-effort remote CRD delete, then CAS soft-delete the row. The
// namespaceID from the request path is validated against the row's own
// namespace to prevent cross-namespace deletion via path tampering.
func (uc *SandboxUsecase) DeleteSandboxClaim(ctx context.Context, principal authn.Principal, namespaceID, id string, expectedRevision int64) (*SandboxClaim, error) {
	c, err := uc.sandboxes.GetSandboxClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.NamespaceID != namespaceID {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: sandbox claim does not belong to the requested namespace")
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(c.NamespaceID),
		Permission: "operate",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no operate permission on namespace")
	}

	cluster, clErr := uc.clusters.GetCluster(ctx, c.ClusterID)
	ns, nsErr := uc.namespaces.GetNamespace(ctx, c.NamespaceID)
	if clErr != nil || nsErr != nil {
		uc.log.WithContext(ctx).Warn("failed to load cluster/namespace for remote sandbox claim delete; row will be soft-deleted",
			logx.String("claim_id", id),
			logx.String("cluster_id", c.ClusterID),
			logx.String("namespace_id", c.NamespaceID),
			logx.Err(clErr),
			logx.Err(nsErr))
	} else {
		locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
		if err := uc.provider.DeleteSandboxClaim(ctx, c.ClusterID, locator, ns.KubeName, c.KubernetesName); err != nil {
			uc.log.WithContext(ctx).Warn("remote sandbox claim delete failed; row will be soft-deleted, operator may need to clean up",
				logx.String("claim_id", id), logx.String("kube_name", c.KubernetesName), logx.Err(err))
		}
	}

	return uc.sandboxes.DeleteSandboxClaim(ctx, id, expectedRevision)
}

// ===================== Tool operations =====================

// ListSandboxTools returns the tool surface for a sandbox, projected from the
// Tool catalog (the single source of truth) so the Tools page, the Agent
// editor, and the Sandbox tool list all show the same tools. Authz `use` on
// k8s_sandbox:{id} gates the call; catalog visibility filtering applies per
// principal. Falls back to the static V1 registry when the catalog usecase is
// not wired (tests / K8s disabled).
func (uc *SandboxUsecase) ListSandboxTools(ctx context.Context, principal authn.Principal, id string) ([]SandboxToolSchema, error) {
	s, err := uc.sandboxes.GetSandbox(ctx, id)
	if err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   sandboxResource(s.ID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on sandbox")
	}
	return uc.sandboxToolSurface(ctx, principal)
}

// CallSandboxTool invokes a tool inside a sandbox. Authz `use` on
// k8s_sandbox:{id} gates the call. The V1 implementation is a stub: it validates
// the tool name against the registry and returns an accepted response; actual
// workspace/browser exec via the K8s exec API can be layered in later without
// changing this signature.
func (uc *SandboxUsecase) CallSandboxTool(ctx context.Context, principal authn.Principal, id, tool, inputJSON, traceID string) (ok bool, outputJSON, errMsg string, err error) {
	s, err := uc.sandboxes.GetSandbox(ctx, id)
	if err != nil {
		return false, "", "", err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return false, "", "", err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   sandboxResource(s.ID),
		Permission: "use",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return false, "", "", err
	}
	if !dec.Allowed {
		return false, "", "", errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no use permission on sandbox")
	}

	// Validate the tool name against the sandbox tool surface (catalog
	// projection, or the static registry when the catalog is not wired).
	surface, err := uc.sandboxToolSurface(ctx, principal)
	if err != nil {
		return false, "", "", err
	}
	var toolSchema *SandboxToolSchema
	known := false
	for i := range surface {
		if surface[i].Name == tool {
			known = true
			toolSchema = &surface[i]
			break
		}
	}
	if !known {
		return false, "", "", fmt.Errorf("%w: unknown tool %q", ErrClusterInvalidArgument, tool)
	}

	// Privileged tools (skill.fetch/publish, git.pull/push) require a Tool-level
	// authorization check beyond sandbox.use: the acting subject must hold the
	// tool's Permission on the target resource extracted from the input
	// (agent-identity-delegation-design §3.1). This gate is enforced now so it
	// is in place when the Runtime executor lands; non-privileged workspace.*
	// tools are gated only by the sandbox.use check above.
	if toolSchema.Privileged {
		resourceRef, rerr := toolSchema.ResourceFromInput(inputJSON)
		if rerr != nil {
			return false, "", "", fmt.Errorf("%w: %v", ErrClusterInvalidArgument, rerr)
		}
		pdec, perr := uc.rels.Check(ctx, AuthzCheckRequest{
			Subject:    subject,
			Resource:   resourceRef,
			Permission: toolSchema.Permission,
			OrgID:      principal.OrgID,
		})
		if perr != nil {
			return false, "", "", perr
		}
		if !pdec.Allowed {
			return false, "", "", errorx.Forbidden(errorx.Code("PERMISSION_DENIED"),
				fmt.Sprintf("forbidden: no %s permission on %s:%s", toolSchema.Permission, resourceRef.Type, resourceRef.ID))
		}
	}

	// Tool execution is not yet wired to a runtime executor (design §11,
	// sandbox-development-plan.md PR2). Return an explicit Unavailable error
	// instead of a false "accepted" success so callers (and the UI) cannot
	// mistake the stub for a real result. ListSandboxTools still works for
	// surfacing the tool protocol/schema. This becomes a real exec call once
	// the Runtime Tool Gateway lands (PR 13/14).
	return false, "", "", errorx.Unavailable(
		errorx.Code("SANDBOX_TOOL_EXECUTION_NOT_AVAILABLE"),
		"sandbox tool execution is not yet available; the runtime executor is not connected",
	)
}

// parseContainerCommand decodes a SandboxTemplate.ContainerCommand value into
// the argv slice consumed by the agent-sandbox CRD podTemplate. The field is
// stored as a JSON-encoded string array (see sandbox.proto CreateSandboxTemplate
// comment), e.g. `["/bin/sh","-c","sleep infinity"]`. strings.Fields would
// corrupt argv entries containing spaces (e.g. "sleep infinity"); JSON decode
// is the correct parse. A non-JSON value falls back to Fields so a plain
// shell string still works.
func parseContainerCommand(raw string) []string {
	cmd := strings.TrimSpace(raw)
	if cmd == "" {
		return nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(cmd), &argv); err == nil {
		return argv
	}
	return strings.Fields(cmd)
}

// mergeSandboxSkills combines template-declared skills with inline
// CreateSandboxRequest skills. Inline entries override template entries with
// the same name (so a caller can pin a different version). The template list
// is taken first; dedup is by Name (case-sensitive). A nil/empty result is
// returned when neither side declares anything.
func mergeSandboxSkills(template, inline []SandboxSkillRef) []SandboxSkillRef {
	if len(template) == 0 && len(inline) == 0 {
		return nil
	}
	seen := make(map[string]int, len(template)+len(inline))
	out := make([]SandboxSkillRef, 0, len(template)+len(inline))
	for _, s := range template {
		if s.Name == "" {
			continue
		}
		if _, ok := seen[s.Name]; !ok {
			seen[s.Name] = len(out)
			out = append(out, s)
		}
	}
	for _, s := range inline {
		if s.Name == "" {
			continue
		}
		if idx, ok := seen[s.Name]; ok {
			out[idx] = s // inline override
			continue
		}
		seen[s.Name] = len(out)
		out = append(out, s)
	}
	return out
}

// sandboxSkillAnnotations renders the resolved skill declarations as the CRD
// metadata.annotations payload the future Runtime/sidecar reads at pod boot.
// Returns nil when there are no skills (so no annotation is written). The
// annotation key is aisphere.io/skills; the value is a JSON array of
// {"name","version"} objects.
func sandboxSkillAnnotations(skills []SandboxSkillRef) map[string]string {
	if len(skills) == 0 {
		return nil
	}
	type ref struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	out := make([]ref, 0, len(skills))
	for _, s := range skills {
		out = append(out, ref{Name: s.Name, Version: s.Version})
	}
	b, err := json.Marshal(out)
	if err != nil {
		// SandboxSkillRef only holds strings; json.Marshal cannot fail here in
		// practice. Fall back to an empty annotation rather than failing the
		// whole create over a serialization error.
		return nil
	}
	return map[string]string{"aisphere.io/skills": string(b)}
}
