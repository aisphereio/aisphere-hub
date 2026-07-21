package biz

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/kubernetesx"
	"github.com/aisphereio/kernel/logx"
)

// ClusterRelationships is the narrow authz surface ClusterUsecase needs
// (mirrors SkillRelationships). *AuthzUsecase satisfies it.
type ClusterRelationships interface {
	Check(ctx context.Context, req AuthzCheckRequest) (AuthzDecision, error)
	BatchCheck(ctx context.Context, req AuthzBatchCheckRequest) (AuthzBatchCheckResult, error)
	WriteRelationships(ctx context.Context, rels ...AuthzRelationship) (AuthzWriteResult, error)
	DeleteRelationships(ctx context.Context, filter AuthzRelationshipFilter) (AuthzWriteResult, error)
	RevokeResource(ctx context.Context, resource AuthzObjectRef) error
	LookupResources(ctx context.Context, req AuthzLookupResourcesRequest) (AuthzLookupResourcesResult, error)
	ReadRelationships(ctx context.Context, filter AuthzRelationshipFilter, limit int, cursor string) ([]AuthzRelationship, string, error)
}

// ClusterUsecase orchestrates cluster lifecycle (design §5.7). It depends on
// the frozen PR② interfaces (ClusterCredentialStore, EndpointPolicy,
// KubernetesProvider) plus the PR③ ClusterRepository, OutboxRepo, and
// ClusterRelationships. Compensation follows the skill_usecase.go:90-118
// pattern: on a step failure, reverse prior steps with context.WithoutCancel.
type ClusterUsecase struct {
	clusters   ClusterRepository
	creds      ClusterCredentialStore
	endpoint   EndpointPolicy
	provider   KubernetesProvider
	outbox     OutboxEnqueuer
	rels       ClusterRelationships
	log        logx.Logger
	opts       ClusterUsecaseOptions
}

// OutboxEnqueuer is the narrow outbox surface the usecase needs (Enqueue only).
// The full OutboxRepo (Claim/Ack/Nak) is used by the reconciler, not here.
// Defined separately so tests can inject a fake without implementing Claim.
type OutboxEnqueuer interface {
	Enqueue(ctx context.Context, aggregateType, aggregateID, eventType string, payload map[string]any) error
}

// NewClusterUsecase wires the usecase. opts zeroes default to sane values
// (MaxScan=100, MaxHydrateRounds=3, ProbeTimeout=30s).
func NewClusterUsecase(
	clusters ClusterRepository,
	creds ClusterCredentialStore,
	endpoint EndpointPolicy,
	provider KubernetesProvider,
	outbox OutboxEnqueuer,
	rels ClusterRelationships,
	log logx.Logger,
	opts ClusterUsecaseOptions,
) *ClusterUsecase {
	if opts.MaxScan <= 0 {
		opts.MaxScan = 100
	}
	if opts.MaxHydrateRounds <= 0 {
		opts.MaxHydrateRounds = 3
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = 30 * time.Second
	}
	if log == nil {
		log = logx.Noop()
	}
	return &ClusterUsecase{
		clusters: clusters,
		creds:    creds,
		endpoint: endpoint,
		provider: provider,
		outbox:   outbox,
		rels:     rels,
		log:      log.Named("biz.cluster"),
		opts:     opts,
	}
}

// canonicalSubject maps an authn.Principal to a SpiceDB subject ref per design
// §7.2.1. V1 Hub-side helper (kernel gap noted in plan "不做" list): prefixes
// the subject type and lowercases. Empty SubjectType → ErrUnsupportedPrincipal.
func canonicalSubject(p authn.Principal) (AuthzSubjectRef, error) {
	t := strings.TrimSpace(p.SubjectType)
	if t == "" {
		return AuthzSubjectRef{}, ErrClusterUnsupportedPrincipalType
	}
	id := strings.TrimSpace(p.SubjectID)
	if id == "" {
		return AuthzSubjectRef{}, ErrClusterUnauthenticated
	}
	return AuthzSubjectRef{Type: t, ID: id}, nil
}

// clusterResource builds the authz object ref for a cluster.
func clusterResource(clusterID string) AuthzObjectRef {
	return AuthzObjectRef{Type: "k8s_cluster", ID: clusterID}
}

// CreateCluster runs the five-step create (design §5.7.2):
//  1. Validate credential + endpoint (SSRF).
//  2. Store credential (AEAD Put) → locator.
//  3. INSERT cluster row (status=CREATING, revision=1).
//  4. Write SpiceDB relationships (zone + owner). Compensate credStore.Delete
//     + status=FAILED on failure.
//  5. Probe (pool.Probe) → fill cluster_uid/kubernetes_version/READY. Probe
//     failure marks DEGRADED (not rollback — the cluster row stays so the
//     operator can inspect; design §5.7.2 "Probe failure does not roll back").
//
// The cluster ID (c.ID) MUST be pre-allocated by the caller (service layer
// generates a uuid). It is needed before Step 2 because the AEAD AAD binds
// cluster_id, and the cluster row (Step 3) stores credential_ref from Step 2.
func (uc *ClusterUsecase) CreateCluster(ctx context.Context, principal authn.Principal, c *Cluster, cred kubernetesx.Credential) (*Cluster, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil cluster", ErrClusterInvalidArgument)
	}
	if c.ID == "" {
		return nil, fmt.Errorf("%w: cluster id must be pre-allocated by caller", ErrClusterInvalidArgument)
	}
	if strings.TrimSpace(c.Name) == "" || c.OrgID == "" || c.ServerURL == "" {
		return nil, fmt.Errorf("%w: name, org_id, server_url are required", ErrClusterInvalidArgument)
	}
	if err := cred.Validate(); err != nil {
		uc.log.WithContext(ctx).Warn("cluster create: cred.Validate failed",
			logx.String("kind", string(cred.Kind)),
			logx.Int("kubeconfig_len", len(cred.Kubeconfig)),
			logx.String("host", cred.Host),
			logx.Int("token_len", len(cred.Token)),
			logx.Int("ca_len", len(cred.CACert)),
			logx.Err(err))
		return nil, fmt.Errorf("%w: credential: %v", ErrClusterCredentialInvalid, err)
	}
	// Step 1: SSRF validate server_url.
	if _, err := uc.endpoint.Validate(ctx, c.ServerURL); err != nil {
		uc.log.WithContext(ctx).Warn("cluster create: endpoint.Validate failed",
			logx.String("server_url", c.ServerURL), logx.Err(err))
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}

	// Stamp owner/created_by from principal (design §7.2.1).
	c.OwnerType = subject.Type
	c.OwnerID = subject.ID
	c.CreatedByType = subject.Type
	c.CreatedBy = subject.ID

	// Step 2: store credential with the cluster's ID (revision=1 for create).
	locator, err := uc.creds.Put(ctx, c.ID, 1, cred)
	if err != nil {
		return nil, fmt.Errorf("store credential: %w", err)
	}
	c.CredentialRef = locator.CredentialRef
	c.CredentialRevision = locator.CredentialRevision
	c.Status = ClusterStatusCreating
	c.Revision = 1

	// Step 3: INSERT cluster row. Compensate credStore.Delete on failure.
	created, err := uc.clusters.CreateCluster(ctx, c)
	if err != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.creds.Delete(compensateCtx, locator.CredentialRef)
		return nil, err
	}

	// Step 4: write SpiceDB relationships (zone + owner). Compensate on failure.
	resource := clusterResource(created.ID)
	if _, err := uc.rels.WriteRelationships(ctx,
		AuthzRelationship{Resource: resource, Relation: "owner", Subject: subject},
		AuthzRelationship{Resource: resource, Relation: "zone", Subject: AuthzSubjectRef{Type: "zone", ID: created.OrgID}},
	); err != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.rels.RevokeResource(compensateCtx, resource)
		_ = uc.creds.Delete(compensateCtx, created.CredentialRef)
		_, _ = uc.clusters.UpdateClusterStatus(compensateCtx, created.ID, ClusterStatusCreating, ClusterStatusFailed, map[string]any{
			"health_message": fmt.Sprintf("authz projection failed: %v", err),
		})
		return nil, fmt.Errorf("%w: project relationships: %v", ErrClusterFailedPrecondition, err)
	}

	// Step 5: probe. Probe failure → DEGRADED (no rollback, design §5.7.2).
	probeCtx, cancel := context.WithTimeout(ctx, uc.opts.ProbeTimeout)
	defer cancel()
	probe, perr := uc.provider.Probe(probeCtx, created.ID, locator, cred)
	if perr != nil {
		uc.log.WithContext(ctx).Warn("cluster probe failed on create; marking DEGRADED",
			logx.String("cluster_id", created.ID),
			logx.Err(perr))
		degraded, _ := uc.clusters.UpdateClusterStatus(ctx, created.ID, ClusterStatusCreating, ClusterStatusDegraded, map[string]any{
			"health_message": fmt.Sprintf("probe failed: %v", perr),
		})
		if degraded != nil {
			return degraded, nil
		}
		return created, nil
	}
	// Probe success: stamp cluster_uid + version + READY.
	ready, err := uc.clusters.UpdateClusterStatus(ctx, created.ID, ClusterStatusCreating, ClusterStatusReady, map[string]any{
		"cluster_uid":        probe.ClusterUID,
		"kubernetes_version": probe.ServerVersion.GitVersion,
		"last_probe_at":      time.Now().UTC(),
		"health_message":     "",
	})
	if err != nil {
		// Status flip failed (row vanished?): return the CREATING row; the
		// reconciler/operator can re-probe. The cluster is usable.
		return created, nil
	}
	return ready, nil
}

// GetCluster loads a cluster and computes the caller's permissions via
// BatchCheck (design §7.6.2).
func (uc *ClusterUsecase) GetCluster(ctx context.Context, principal authn.Principal, id string) (*Cluster, *ClusterPermissions, error) {
	c, err := uc.clusters.GetCluster(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	perms, err := uc.computeClusterPermissions(ctx, principal, c)
	if err != nil {
		return nil, nil, err
	}
	return c, perms, nil
}

// ListClusters scans candidates by org + keyset, filters via BatchCheck
// (design §5.3.1 / §7.6.3). Returns visible clusters + next cursor.
func (uc *ClusterUsecase) ListClusters(ctx context.Context, principal authn.Principal, orgID, cursor string) ([]*Cluster, string, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, "", err
	}
	candidates, nextCursor, err := uc.clusters.ListClusterCandidates(ctx, orgID, cursor, uc.opts.MaxScan)
	if err != nil {
		return nil, "", err
	}
	if len(candidates) == 0 {
		return nil, nextCursor, nil
	}
	// BatchCheck view permission for every candidate.
	checks := make([]AuthzCheckRequest, len(candidates))
	for i, c := range candidates {
		checks[i] = AuthzCheckRequest{
			Subject:    subject,
			Resource:   clusterResource(c.ID),
			Permission: "view",
			OrgID:      orgID,
		}
	}
	result, err := uc.rels.BatchCheck(ctx, AuthzBatchCheckRequest{Checks: checks})
	if err != nil {
		return nil, "", err
	}
	visible := make([]*Cluster, 0, len(candidates))
	for i, dec := range result.Decisions {
		if dec.Allowed {
			visible = append(visible, candidates[i])
		}
	}
	return visible, nextCursor, nil
}

// UpdateCluster applies FieldMask updates with expected_revision CAS (design
// §5.7.4). immutableFields rejects id/org_id/credential_ref/revision/created_*.
func (uc *ClusterUsecase) UpdateCluster(ctx context.Context, principal authn.Principal, id string, expectedRevision int64, updates map[string]any) (*Cluster, error) {
	// Authorization: caller must have edit permission.
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(id),
		Permission: "operate",
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no edit permission on cluster")
	}
	// Reject immutable fields.
	for k := range updates {
		if isImmutableClusterField(k) {
			return nil, fmt.Errorf("%w: field %q is immutable", ErrClusterInvalidArgument, k)
		}
	}
	return uc.clusters.UpdateClusterWithCAS(ctx, id, expectedRevision, updates)
}

// isImmutableClusterField lists fields that UpdateCluster must never touch.
func isImmutableClusterField(col string) bool {
	switch col {
	case "id", "org_id", "credential_ref", "credential_revision", "revision",
		"created_at", "created_by", "created_by_type", "cluster_uid", "status":
		return true
	}
	return false
}

// DeleteCluster (design §5.7.5). With Namespaces → ErrFailedPrecondition
// unless DeletePolicy=DETACH_ONLY. CASCADE triggers async outbox namespace
// cleanup. Always soft-deletes the cluster row + revokes SpiceDB rels.
func (uc *ClusterUsecase) DeleteCluster(ctx context.Context, principal authn.Principal, id, deletePolicy string) error {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(id),
		Permission: "delete",
	})
	if err != nil {
		return err
	}
	if !dec.Allowed {
		return errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no delete permission on cluster")
	}

	nsCount, err := uc.clusters.CountNamespacesForCluster(ctx, id)
	if err != nil {
		return err
	}
	// Hard delete (no policy) is blocked when namespaces exist (design §5.7.5).
	// DETACH_ONLY soft-deletes the row and leaves namespaces intact; CASCADE
	// soft-deletes + enqueues async namespace cleanup. Both are allowed.
	if nsCount > 0 && deletePolicy != DeletePolicyDetachOnly && deletePolicy != DeletePolicyCascade {
		return fmt.Errorf("%w: cluster has %d namespaces; use DETACH_ONLY or CASCADE", ErrClusterFailedPrecondition, nsCount)
	}

	// Soft-delete the cluster row.
	if err := uc.clusters.SoftDeleteCluster(ctx, id); err != nil {
		return err
	}
	// Revoke all SpiceDB relationships on the cluster resource (idempotent).
	compensateCtx := context.WithoutCancel(ctx)
	_ = uc.rels.RevokeResource(compensateCtx, clusterResource(id))
	// Invalidate the client pool cache.
	uc.provider.InvalidateCluster(ctx, id)

	// CASCADE: enqueue async namespace cleanup (design §5.7.5). The reconciler
	// processes the outbox event and deletes remote namespaces.
	if deletePolicy == DeletePolicyCascade && nsCount > 0 {
		if err := uc.outbox.Enqueue(ctx, "cluster", id, "namespace_cleanup", map[string]any{
			"cluster_id":   id,
			"delete_policy": deletePolicy,
		}); err != nil {
			uc.log.WithContext(ctx).Warn("failed to enqueue namespace cleanup outbox event",
				logx.String("cluster_id", id), logx.Err(err))
		}
	}
	return nil
}

// ProbeCluster re-probes an existing cluster and stamps the result (design
// §5.7.6). cluster_uid mismatch → DEGRADED (identity drift).
func (uc *ClusterUsecase) ProbeCluster(ctx context.Context, principal authn.Principal, id string) (*Cluster, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(id),
		Permission: "operate",
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no probe permission on cluster")
	}
	c, err := uc.clusters.GetCluster(ctx, id)
	if err != nil {
		return nil, err
	}
	locator := CredentialLocator{ClusterID: c.ID, CredentialRef: c.CredentialRef, CredentialRevision: c.CredentialRevision}
	probeCtx, cancel := context.WithTimeout(ctx, uc.opts.ProbeTimeout)
	defer cancel()
	probe, perr := uc.provider.Probe(probeCtx, c.ID, locator, kubernetesx.Credential{})
	if perr != nil {
		_, _ = uc.clusters.UpdateClusterStatus(ctx, c.ID, "", ClusterStatusDegraded, map[string]any{
			"health_message": fmt.Sprintf("probe failed: %v", perr),
			"last_probe_at":  time.Now().UTC(),
		})
		return uc.clusters.GetCluster(ctx, id)
	}
	// cluster_uid drift check (design §5.7.6).
	extras := map[string]any{
		"kubernetes_version": probe.ServerVersion.GitVersion,
		"last_probe_at":      time.Now().UTC(),
		"health_message":     "",
	}
	nextStatus := ClusterStatusReady
	if c.ClusterUID != "" && probe.ClusterUID != "" && c.ClusterUID != probe.ClusterUID {
		nextStatus = ClusterStatusDegraded
		extras["health_message"] = fmt.Sprintf("cluster_uid drift: stored=%s probed=%s", c.ClusterUID, probe.ClusterUID)
		extras["cluster_uid"] = probe.ClusterUID
	}
	updated, err := uc.clusters.UpdateClusterStatus(ctx, c.ID, "", nextStatus, extras)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// RotateCredential runs the six-step rotate (design §5.7.3):
//  1. CAS-load cluster (expected_revision guard).
//  2. credStore.Put new credential at revision=current+1.
//  3. One-shot probe with the new credential + cluster_uid compare. Mismatch →
//     credStore.Delete new + return ErrClusterCredentialInvalid.
//  4. DB CAS stamp credential_ref/revision (expected_revision). CAS fail →
//     credStore.Delete new + ErrClusterRevisionConflict.
//  5. pool.InvalidateCluster (next probe rebuilds from new credential).
//  6. outbox.Enqueue delayed cleanup of old credential_ref (idempotent).
func (uc *ClusterUsecase) RotateCredential(ctx context.Context, principal authn.Principal, id string, expectedRevision int64, newCred kubernetesx.Credential) (*Cluster, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(id),
		Permission: "manage",
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no rotate permission on cluster")
	}
	if err := newCred.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrClusterCredentialInvalid, err)
	}
	if _, err := uc.endpoint.Validate(ctx, newCred.Host); err != nil {
		return nil, err
	}

	c, err := uc.clusters.GetCluster(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Revision != expectedRevision {
		return nil, ErrClusterRevisionConflict
	}
	oldRef := c.CredentialRef
	newRevision := c.CredentialRevision + 1

	// Step 2: Put new credential.
	newLocator, err := uc.creds.Put(ctx, c.ID, newRevision, newCred)
	if err != nil {
		return nil, fmt.Errorf("store new credential: %w", err)
	}

	// Step 3: probe with the new credential + cluster_uid compare.
	probeCtx, cancel := context.WithTimeout(ctx, uc.opts.ProbeTimeout)
	defer cancel()
	probe, perr := uc.provider.Probe(probeCtx, c.ID, newLocator, newCred)
	if perr != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.creds.Delete(compensateCtx, newLocator.CredentialRef)
		return nil, fmt.Errorf("%w: probe failed: %v", ErrClusterCredentialInvalid, perr)
	}
	if c.ClusterUID != "" && probe.ClusterUID != "" && c.ClusterUID != probe.ClusterUID {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.creds.Delete(compensateCtx, newLocator.CredentialRef)
		return nil, fmt.Errorf("%w: cluster_uid mismatch (stored=%s probed=%s)", ErrClusterCredentialInvalid, c.ClusterUID, probe.ClusterUID)
	}

	// Step 4: DB CAS stamp new credential_ref/revision.
	updated, err := uc.clusters.UpdateClusterCredential(ctx, c.ID, expectedRevision, newRevision, newLocator.CredentialRef)
	if err != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.creds.Delete(compensateCtx, newLocator.CredentialRef)
		return nil, err
	}

	// Step 5: invalidate pool cache.
	uc.provider.InvalidateCluster(ctx, c.ID)

	// Step 6: enqueue delayed cleanup of the old credential_ref.
	if err := uc.outbox.Enqueue(ctx, "cluster", c.ID, "credential_ref_cleanup", map[string]any{
		"cluster_id":         c.ID,
		"old_credential_ref": oldRef,
	}); err != nil {
		uc.log.WithContext(ctx).Warn("failed to enqueue old credential cleanup",
			logx.String("cluster_id", c.ID), logx.String("old_ref", oldRef), logx.Err(err))
	}
	return updated, nil
}

// computeClusterPermissions BatchChecks the cluster permission set (design
// §7.6.2): view, operate, manage, create_namespace, delete. Matches the proto
// ClusterPermissions fields 1-5.
func (uc *ClusterUsecase) computeClusterPermissions(ctx context.Context, principal authn.Principal, c *Cluster) (*ClusterPermissions, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	perms := []string{"view", "operate", "manage", "create_namespace", "delete"}
	checks := make([]AuthzCheckRequest, len(perms))
	for i, p := range perms {
		checks[i] = AuthzCheckRequest{
			Subject:    subject,
			Resource:   clusterResource(c.ID),
			Permission: p,
			OrgID:      c.OrgID,
		}
	}
	result, err := uc.rels.BatchCheck(ctx, AuthzBatchCheckRequest{Checks: checks})
	if err != nil {
		return nil, err
	}
	out := &ClusterPermissions{}
	for i, dec := range result.Decisions {
		switch perms[i] {
		case "view":
			out.CanView = dec.Allowed
		case "operate":
			out.CanOperate = dec.Allowed
		case "manage":
			out.CanManage = dec.Allowed
		case "create_namespace":
			out.CanCreateNamespace = dec.Allowed
		case "delete":
			out.CanDelete = dec.Allowed
		}
	}
	return out, nil
}

// ClusterPermissions is the biz view of the proto ClusterPermissions (design
// §7.6.2). Fields mirror the proto ClusterPermissions 1-5. The service layer
// maps this to the proto message.
type ClusterPermissions struct {
	CanView            bool
	CanOperate         bool
	CanManage          bool
	CanCreateNamespace bool
	CanDelete          bool
}
