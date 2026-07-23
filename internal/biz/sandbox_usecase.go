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

	SandboxOperatingModeRunning   = "RUNNING"
	SandboxOperatingModeSuspended = "SUSPENDED"
)

// SandboxToolSchema describes one tool an agent may invoke inside a sandbox.
// InputSchema is a JSON Schema (string) so the biz layer stays free of encoding
// dependencies for the static registry.
type SandboxToolSchema struct {
	Name        string
	Description string
	InputSchema string // JSON Schema
}

// sandboxToolRegistry is the V1 fixed tool surface exposed by every sandbox
// (design §11). The exec implementation is stubbed in CallSandboxTool; this
// registry is the source of truth for ListSandboxTools.
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
	spec.ContainerCommand = parseContainerCommand(created.ContainerCommand)
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

	// Resolve template ref up front (validates the template exists and yields
	// its K8s name + pod template fields for the apply spec). A missing template
	// is a client error surfaced before any DB write.
	var (
		templateRef      string
		templateImage    string
		templateCommand  []string
	)
	if s.TemplateID != "" {
		tmpl, terr := uc.sandboxes.GetSandboxTemplate(ctx, s.TemplateID)
		if terr != nil {
			return nil, fmt.Errorf("%w: referenced template not found: %v", ErrClusterInvalidArgument, terr)
		}
		templateRef = tmpl.KubernetesName
		templateImage = tmpl.Image
		templateCommand = parseContainerCommand(tmpl.ContainerCommand)
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
		Name:             created.KubernetesName,
		Namespace:        ns.KubeName,
		TemplateRef:      templateRef,
		Image:            templateImage,
		ContainerCommand: templateCommand,
		OperatingMode:    created.OperatingMode,
		Labels:           created.Labels,
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

	// Step 8: best-effort runtime status backfill, then stamp READY.
	// SandboxRuntimeStatus carries PodName/PodIP/NodeName/Image (no UID/
	// ResourceVersion), so we backfill what is available; the periodic sync
	// reconciler fills in the rest. The K8s namespace name lives on the Hub
	// namespace row (ns.KubeName), not on the Sandbox row.
	fields := map[string]any{}
	if status, gerr := uc.provider.GetSandboxStatus(ctx, created.ClusterID, locator, ns.KubeName, created.KubernetesName); gerr == nil {
		fields["pod_name"] = status.PodName
		fields["pod_ip"] = status.PodIP
		fields["node_name"] = status.NodeName
		fields["image"] = status.Image
	} else {
		uc.log.WithContext(ctx).Debug("sandbox status backfill deferred; will sync later",
			logx.String("sandbox_id", created.ID), logx.Err(gerr))
	}
	fields["last_sync_at"] = time.Now().UTC()
	ready, err := uc.sandboxes.UpdateSandboxStatus(ctx, created.ID, SandboxLifecycleReady, "", fields)
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

// syncSandboxesCore is the authz-free internal sync used by the reconciler.
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
	// Project the template into the target namespace so the WarmPool's
	// sandboxTemplateRef can resolve it (the agent-sandbox operator requires
	// the template to be in the same namespace as the WarmPool). This is best-
	// effort: a failure is logged, not returned, so ApplyWarmPool still proceeds
	// (the operator may already have the template, or the apply may no-op on an
	// existing template).
	templateSpec := SandboxTemplateApplySpec{
		Name:             tmpl.KubernetesName,
		Namespace:        ns.KubeName,
		Image:            tmpl.Image,
		ContainerCommand: parseContainerCommand(tmpl.ContainerCommand),
		Labels:           tmpl.Labels,
	}
	if err := uc.provider.ApplySandboxTemplate(ctx, created.ClusterID, locator, templateSpec); err != nil {
		uc.log.WithContext(ctx).Warn("failed to project template to target namespace; warm pool may fail",
			logx.String("warm_pool_id", created.ID),
			logx.String("namespace", ns.KubeName),
			logx.String("template", tmpl.KubernetesName),
			logx.Err(err))
		// Don't fail — the operator might already have the template, or the
		// apply may succeed with an existing template. Let ApplyWarmPool proceed.
	}
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

	ready, err := uc.sandboxes.UpdateWarmPoolStatus(ctx, created.ID, WarmPoolStatusReady, map[string]any{
		"health_message": "",
	})
	if err != nil {
		return created, nil
	}
	return ready, nil
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
// best-effort remote CRD delete, then CAS soft-delete the row.
func (uc *SandboxUsecase) DeleteWarmPool(ctx context.Context, principal authn.Principal, id string, expectedRevision int64) (*WarmPool, error) {
	w, err := uc.sandboxes.GetWarmPool(ctx, id)
	if err != nil {
		return nil, err
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

// SyncWarmPools reconciles DB rows with the remote SandboxWarmPool CRDs in a
// namespace (design §11 sync), mirroring SyncSandboxes: authz `operate` on the
// namespace, list remote warm pools from the cluster, diff against local rows,
// then update observed runtime state (flipping status when readiness changes),
// import new ones, and remove (soft-delete) local ones no longer present
// remotely. Returns counts. WarmPools are addressed through the parent
// namespace permission and project no SpiceDB object, so removal is a DB
// soft-delete only (no revocation). Imported warm pools inherit the namespace
// owner; their template ref is resolved to a Hub template ID via a cluster-
// scoped template index, and imports whose ref cannot be resolved are skipped
// (best-effort) rather than creating an orphaned row.
func (uc *SandboxUsecase) SyncWarmPools(ctx context.Context, principal authn.Principal, namespaceID string) (imported, updated, removed int, err error) {
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
	return uc.syncWarmPoolsCore(ctx, namespaceID)
}

// syncWarmPoolsCore is the authz-free internal sync used by the reconciler.
func (uc *SandboxUsecase) syncWarmPoolsCore(ctx context.Context, namespaceID string) (imported, updated, removed int, err error) {
	ns, err := uc.namespaces.GetNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, 0, err
	}
	cluster, err := uc.clusters.GetCluster(ctx, ns.ClusterID)
	if err != nil {
		return 0, 0, 0, err
	}
	locator := CredentialLocator{ClusterID: cluster.ID, CredentialRef: cluster.CredentialRef, CredentialRevision: cluster.CredentialRevision}
	remote, err := uc.provider.ListWarmPools(ctx, ns.ClusterID, locator, ns.KubeName)
	if err != nil {
		return 0, 0, 0, err
	}
	local, err := uc.sandboxes.ListWarmPoolsByNamespace(ctx, namespaceID)
	if err != nil {
		return 0, 0, 0, err
	}

	localByName := make(map[string]*WarmPool, len(local))
	for _, w := range local {
		localByName[w.KubernetesName] = w
	}
	remoteByName := make(map[string]bool, len(remote))
	now := time.Now().UTC()

	// Build a template K8s-name → Hub ID index so imported warm pools can
	// resolve their sandboxTemplateRef to a Hub template row. A failure to load
	// templates is non-fatal: imports are simply skipped with a warning.
	templates, _ := uc.sandboxes.ListSandboxTemplatesByCluster(ctx, ns.ClusterID)
	templateByName := make(map[string]string, len(templates))
	for _, t := range templates {
		templateByName[t.KubernetesName] = t.ID
	}

	for _, r := range remote {
		remoteByName[r.Name] = true
		if existing, ok := localByName[r.Name]; ok {
			// Update observed runtime state.
			fields := map[string]any{
				"ready_replicas":   r.ReadyReplicas,
				"kubernetes_uid":   r.UID,
				"resource_version": r.ResourceVersion,
				"last_sync_at":     now,
			}
			if _, e := uc.sandboxes.UpdateWarmPoolSync(ctx, existing.ID, fields); e != nil {
				uc.log.WithContext(ctx).Warn("sync: warm pool update failed",
					logx.String("warm_pool_id", existing.ID), logx.String("kube_name", r.Name), logx.Err(e))
				continue
			}
			// Flip status when readiness changed: all desired replicas ready →
			// READY; some but not all → DEGRADED; none ready → leave unchanged.
			var newStatus string
			switch {
			case r.ReadyReplicas == r.Replicas:
				newStatus = WarmPoolStatusReady
			case r.ReadyReplicas > 0:
				newStatus = WarmPoolStatusDegraded
			default:
				newStatus = "" // not enough ready replicas; leave status unchanged
			}
			if newStatus != "" && newStatus != existing.Status {
				if _, se := uc.sandboxes.UpdateWarmPoolStatus(ctx, existing.ID, newStatus, nil); se != nil {
					uc.log.WithContext(ctx).Warn("sync: warm pool status flip failed",
						logx.String("warm_pool_id", existing.ID), logx.String("kube_name", r.Name), logx.Err(se))
				}
			}
			updated++
			continue
		}
		// Import: a remote warm pool with no matching Hub row. Resolve the
		// template ref to a Hub template ID; skip (best-effort) if unresolved so
		// we never insert a row with a dangling template_id.
		templateID, resolved := templateByName[r.TemplateRef]
		if !resolved {
			uc.log.WithContext(ctx).Warn("sync: warm pool import skipped; template ref unresolved",
				logx.String("kube_name", r.Name), logx.String("template_ref", r.TemplateRef))
			continue
		}
		impStatus := WarmPoolStatusCreating
		switch {
		case r.ReadyReplicas == r.Replicas:
			impStatus = WarmPoolStatusReady
		case r.ReadyReplicas > 0:
			impStatus = WarmPoolStatusDegraded
		}
		imp := &WarmPool{
			ID:              uuid.NewString(),
			NamespaceID:     namespaceID,
			ClusterID:       ns.ClusterID,
			OrgID:           cluster.OrgID,
			Name:            r.Name,
			KubernetesName:  r.Name,
			KubernetesUID:   r.UID,
			ResourceVersion: r.ResourceVersion,
			TemplateID:      templateID,
			Replicas:        r.Replicas,
			ReadyReplicas:   r.ReadyReplicas,
			Status:          impStatus,
			OwnerType:       ns.OwnerType,
			OwnerID:         ns.OwnerID,
			CreatedByType:   ns.OwnerType,
			CreatedBy:       ns.OwnerID,
			Revision:        1,
			LastSyncAt:      now,
		}
		if _, cerr := uc.sandboxes.CreateWarmPool(ctx, imp); cerr != nil {
			uc.log.WithContext(ctx).Warn("sync: warm pool import create failed",
				logx.String("kube_name", r.Name), logx.Err(cerr))
			continue
		}
		imported++
	}

	// Remove local warm pools no longer present remotely. Skip rows that are
	// mid-flight (CREATING) or already DELETED so we never clobber an in-progress
	// create or a finalized delete.
	for name, w := range localByName {
		if remoteByName[name] {
			continue
		}
		if w.Status == WarmPoolStatusCreating || w.Status == WarmPoolStatusDeleted {
			continue
		}
		if _, derr := uc.sandboxes.DeleteWarmPool(ctx, w.ID, w.Revision); derr != nil {
			uc.log.WithContext(ctx).Warn("sync: warm pool remove failed",
				logx.String("warm_pool_id", w.ID), logx.String("kube_name", name), logx.Err(derr))
			continue
		}
		removed++
	}
	return imported, updated, removed, nil
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

	ready, err := uc.sandboxes.UpdateSandboxClaimStatus(ctx, created.ID, SandboxClaimStatusReady, map[string]any{
		"health_message": "",
	})
	if err != nil {
		return created, nil
	}
	return ready, nil
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
// best-effort remote CRD delete, then CAS soft-delete the row.
func (uc *SandboxUsecase) DeleteSandboxClaim(ctx context.Context, principal authn.Principal, id string, expectedRevision int64) (*SandboxClaim, error) {
	c, err := uc.sandboxes.GetSandboxClaim(ctx, id)
	if err != nil {
		return nil, err
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

// SyncSandboxClaims reconciles DB rows with the remote SandboxClaim CRDs in a
// namespace (design §11 sync), mirroring SyncSandboxes/SyncWarmPools: authz
// `operate` on the namespace, list remote claims from the cluster, diff against
// local rows, then update observed runtime state (flipping status when
// readiness changes), import new ones, and remove (soft-delete) local ones no
// longer present remotely. Returns counts. SandboxClaims are addressed through
// the parent namespace permission and project no SpiceDB object, so removal is
// a DB soft-delete only (no revocation). Imported claims inherit the namespace
// owner; their warm pool ref is resolved to a Hub warm pool ID via a
// namespace-scoped warm pool index, and imports whose ref cannot be resolved
// are skipped (best-effort) rather than creating an orphaned row.
func (uc *SandboxUsecase) SyncSandboxClaims(ctx context.Context, principal authn.Principal, namespaceID string) (imported, updated, removed int, err error) {
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

// syncSandboxClaimsCore is the authz-free internal sync used by the reconciler.
func (uc *SandboxUsecase) syncSandboxClaimsCore(ctx context.Context, namespaceID string) (imported, updated, removed int, err error) {
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

	localByName := make(map[string]*SandboxClaim, len(local))
	for _, c := range local {
		localByName[c.KubernetesName] = c
	}
	remoteByName := make(map[string]bool, len(remote))
	now := time.Now().UTC()

	// Build a warm-pool K8s-name → Hub ID index so imported claims can resolve
	// their warmPoolRef to a Hub warm pool row. A failure to load pools is
	// non-fatal: imports are simply skipped with a warning.
	pools, _ := uc.sandboxes.ListWarmPoolsByNamespace(ctx, namespaceID)
	poolByName := make(map[string]string, len(pools))
	for _, p := range pools {
		poolByName[p.KubernetesName] = p.ID
	}

	for _, r := range remote {
		remoteByName[r.Name] = true
		if existing, ok := localByName[r.Name]; ok {
			// Update observed runtime state.
			fields := map[string]any{
				"sandbox_kube_name": r.SandboxName,
				"sandbox_pod_ip":    r.SandboxPodIP,
				"kubernetes_uid":    r.UID,
				"resource_version":  r.ResourceVersion,
				"last_sync_at":      now,
			}
			if _, e := uc.sandboxes.UpdateSandboxClaimSync(ctx, existing.ID, fields); e != nil {
				uc.log.WithContext(ctx).Warn("sync: sandbox claim update failed",
					logx.String("claim_id", existing.ID), logx.String("kube_name", r.Name), logx.Err(e))
				continue
			}
			// Flip status when readiness changed: Ready → READY, else PENDING.
			newStatus := SandboxClaimStatusPending
			if r.Ready {
				newStatus = SandboxClaimStatusReady
			}
			if newStatus != existing.Status {
				if _, se := uc.sandboxes.UpdateSandboxClaimStatus(ctx, existing.ID, newStatus, nil); se != nil {
					uc.log.WithContext(ctx).Warn("sync: sandbox claim status flip failed",
						logx.String("claim_id", existing.ID), logx.String("kube_name", r.Name), logx.Err(se))
				}
			}
			updated++
			continue
		}
		// Import: a remote claim with no matching Hub row. Resolve the warm pool
		// ref to a Hub warm pool ID; skip (best-effort) if unresolved so we never
		// insert a row with a dangling warm_pool_id.
		warmPoolID, resolved := poolByName[r.WarmPoolRef]
		if !resolved {
			uc.log.WithContext(ctx).Warn("sync: sandbox claim import skipped; warm pool ref unresolved",
				logx.String("kube_name", r.Name), logx.String("warm_pool_ref", r.WarmPoolRef))
			continue
		}
		impStatus := SandboxClaimStatusPending
		if r.Ready {
			impStatus = SandboxClaimStatusReady
		}
		imp := &SandboxClaim{
			ID:              uuid.NewString(),
			NamespaceID:     namespaceID,
			ClusterID:       ns.ClusterID,
			OrgID:           cluster.OrgID,
			Name:            r.Name,
			KubernetesName:  r.Name,
			KubernetesUID:   r.UID,
			ResourceVersion: r.ResourceVersion,
			WarmPoolID:      warmPoolID,
			SandboxKubeName: r.SandboxName,
			SandboxPodIP:    r.SandboxPodIP,
			Status:          impStatus,
			OwnerType:       ns.OwnerType,
			OwnerID:         ns.OwnerID,
			CreatedByType:   ns.OwnerType,
			CreatedBy:       ns.OwnerID,
			Revision:        1,
			LastSyncAt:      now,
		}
		if _, cerr := uc.sandboxes.CreateSandboxClaim(ctx, imp); cerr != nil {
			uc.log.WithContext(ctx).Warn("sync: sandbox claim import create failed",
				logx.String("kube_name", r.Name), logx.Err(cerr))
			continue
		}
		imported++
	}

	// Remove local claims no longer present remotely. Skip rows that are
	// mid-flight (PENDING create) or already DELETED so we never clobber an
	// in-progress create or a finalized delete.
	for name, c := range localByName {
		if remoteByName[name] {
			continue
		}
		if c.Status == SandboxClaimStatusPending || c.Status == SandboxClaimStatusDeleted {
			continue
		}
		if _, derr := uc.sandboxes.DeleteSandboxClaim(ctx, c.ID, c.Revision); derr != nil {
			uc.log.WithContext(ctx).Warn("sync: sandbox claim remove failed",
				logx.String("claim_id", c.ID), logx.String("kube_name", name), logx.Err(derr))
			continue
		}
		removed++
	}
	return imported, updated, removed, nil
}

// ReconcileNamespaceSync is the authz-free internal sync entry point used by
// the SandboxSyncReconciler. It runs the core sync logic for sandboxes,
// warm pools, and sandbox claims against the given namespace without a
// principal authz check (the reconciler is a trusted internal worker).
// Returns aggregated counts and the first error encountered (subsequent
// syncs still run).
func (uc *SandboxUsecase) ReconcileNamespaceSync(ctx context.Context, namespaceID string) (sbImp, sbUpd, sbRem, wpImp, wpUpd, wpRem, clImp, clUpd, clRem int, err error) {
	var firstErr error
	sbImp, sbUpd, sbRem, e1 := uc.syncSandboxesCore(ctx, namespaceID)
	if e1 != nil {
		firstErr = e1
		uc.log.WithContext(ctx).Warn("reconciler: sync sandboxes failed",
			logx.String("namespace_id", namespaceID), logx.Err(e1))
	}
	wpImp, wpUpd, wpRem, e2 := uc.syncWarmPoolsCore(ctx, namespaceID)
	if e2 != nil && firstErr == nil {
		firstErr = e2
	}
	if e2 != nil {
		uc.log.WithContext(ctx).Warn("reconciler: sync warm pools failed",
			logx.String("namespace_id", namespaceID), logx.Err(e2))
	}
	clImp, clUpd, clRem, e3 := uc.syncSandboxClaimsCore(ctx, namespaceID)
	if e3 != nil && firstErr == nil {
		firstErr = e3
	}
	if e3 != nil {
		uc.log.WithContext(ctx).Warn("reconciler: sync sandbox claims failed",
			logx.String("namespace_id", namespaceID), logx.Err(e3))
	}
	return sbImp, sbUpd, sbRem, wpImp, wpUpd, wpRem, clImp, clUpd, clRem, firstErr
}

// ===================== Tool operations =====================

// ListSandboxTools returns the V1 fixed tool surface for a sandbox. Authz `use`
// on k8s_sandbox:{id} gates the call; the registry itself is static.
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
	// Return a copy so callers cannot mutate the package-level registry.
	out := make([]SandboxToolSchema, len(sandboxToolRegistry))
	copy(out, sandboxToolRegistry)
	return out, nil
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

	// Validate the tool name against the registry.
	known := false
	for _, t := range sandboxToolRegistry {
		if t.Name == tool {
			known = true
			break
		}
	}
	if !known {
		return false, "", "", fmt.Errorf("%w: unknown tool %q", ErrClusterInvalidArgument, tool)
	}

	// V1 stub: acknowledge the call. Real exec (workspace.* via K8s exec,
	// browser.open via the sandbox browser sidecar) is implemented later.
	resp := map[string]any{
		"tool":     tool,
		"status":   "accepted",
		"trace_id": traceID,
		"message":  "tool execution not yet implemented; request accepted",
	}
	b, _ := json.Marshal(resp)
	uc.log.WithContext(ctx).Info("sandbox tool call (stub)",
		logx.String("sandbox_id", id),
		logx.String("tool", tool),
		logx.String("trace_id", traceID))
	return true, string(b), "", nil
}

// parseContainerCommand decodes the container_command field stored in the DB as
// a JSON-encoded string array (e.g. `["/bin/sh","-c","sleep infinity"]`) into a
// []string suitable for the K8s container spec. If the field is empty or not a
// valid JSON array, it returns nil (no command — the image entrypoint runs).
func parseContainerCommand(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var cmd []string
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		// Fallback: treat as a single shell command string split by whitespace.
		return strings.Fields(raw)
	}
	return cmd
}
