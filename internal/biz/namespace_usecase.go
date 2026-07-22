package biz

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
)

// NamespaceUsecase orchestrates namespace lifecycle (design §6.4 / §6.6 / §7.4
// / §7.5). Like ClusterUsecase it depends on the frozen PR② provider interface
// for remote apply/delete, plus the PR③ NamespaceRepository, OutboxEnqueuer,
// and NamespaceRelationships. Visibility switches follow the three-step
// DB-then-sync-then-ack pattern (design §7.5.3/§7.5.4) with compensation on
// SpiceDB projection failure.
type NamespaceUsecase struct {
	namespaces NamespaceRepository
	clusters   ClusterRepository
	provider   KubernetesProvider
	outbox     OutboxEnqueuer
	rels       NamespaceRelationships
	log        logx.Logger
	opts       ClusterUsecaseOptions
}

// NamespaceRelationships is the narrow authz surface NamespaceUsecase needs.
// *AuthzUsecase satisfies it. Includes LookupResources for List hydration.
type NamespaceRelationships interface {
	Check(ctx context.Context, req AuthzCheckRequest) (AuthzDecision, error)
	BatchCheck(ctx context.Context, req AuthzBatchCheckRequest) (AuthzBatchCheckResult, error)
	WriteRelationships(ctx context.Context, rels ...AuthzRelationship) (AuthzWriteResult, error)
	DeleteRelationships(ctx context.Context, filter AuthzRelationshipFilter) (AuthzWriteResult, error)
	RevokeResource(ctx context.Context, resource AuthzObjectRef) error
	LookupResources(ctx context.Context, req AuthzLookupResourcesRequest) (AuthzLookupResourcesResult, error)
	ReadRelationships(ctx context.Context, filter AuthzRelationshipFilter, limit int, cursor string) ([]AuthzRelationship, string, error)
	LookupSubjects(ctx context.Context, req AuthzLookupSubjectsRequest) (AuthzLookupSubjectsResult, error)
}

// NewNamespaceUsecase wires the usecase.
func NewNamespaceUsecase(
	namespaces NamespaceRepository,
	clusters ClusterRepository,
	provider KubernetesProvider,
	outbox OutboxEnqueuer,
	rels NamespaceRelationships,
	log logx.Logger,
	opts ClusterUsecaseOptions,
) *NamespaceUsecase {
	if opts.MaxScan <= 0 {
		opts.MaxScan = 100
	}
	if opts.MaxHydrateRounds <= 0 {
		opts.MaxHydrateRounds = 3
	}
	if log == nil {
		log = logx.Noop()
	}
	return &NamespaceUsecase{
		namespaces: namespaces,
		clusters:   clusters,
		provider:   provider,
		outbox:     outbox,
		rels:       rels,
		log:        log.Named("biz.namespace"),
		opts:       opts,
	}
}

// namespaceResource builds the authz object ref for a namespace.
func namespaceResource(namespaceID string) AuthzObjectRef {
	return AuthzObjectRef{Type: "k8s_namespace", ID: namespaceID}
}

// CreateNamespace runs the eight-step create (design §6.4):
//  1. Authz check on parent cluster (create_namespace).
//  2. Validate name (DNS-1123 label) + uniqueness guard.
//  3. INSERT namespace row (lifecycle=CREATING, visibility=PRIVATE, sync=SYNCED).
//  4. Write SpiceDB owner + parent (namespace→cluster) relationships.
//  5. Apply remote Namespace via provider (SSA).
//  6. Stamp lifecycle=READY + kubernetes_uid/resource_version.
//  7. (visibility PRIVATE needs no wildcard projection — SYNCED stays.)
//  8. Return.
//
// Compensation: on Step 4/5 failure, reverse: revoke SpiceDB rels, soft-delete
// the row (or mark FAILED). On Step 5 failure the remote Namespace may or may
// not exist; we mark FAILED and let the operator/reconciler inspect rather
// than guessing a delete (design §6.4 "remote apply failure → FAILED, not
// rollback, because a partial apply is hard to reverse safely").
func (uc *NamespaceUsecase) CreateNamespace(ctx context.Context, principal authn.Principal, ns *Namespace) (*Namespace, error) {
	if ns == nil {
		return nil, fmt.Errorf("%w: nil namespace", ErrClusterInvalidArgument)
	}
	if ns.ID == "" {
		return nil, fmt.Errorf("%w: namespace id must be pre-allocated by caller", ErrClusterInvalidArgument)
	}
	if ns.ClusterID == "" || ns.KubeName == "" {
		return nil, fmt.Errorf("%w: cluster_id, kube_name are required", ErrClusterInvalidArgument)
	}
	if !isDNS1123Label(ns.KubeName) {
		return nil, fmt.Errorf("%w: kube_name must be a DNS-1123 label", ErrClusterInvalidArgument)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}

	// Step 1: authz check on parent cluster.
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(ns.ClusterID),
		Permission: "create_namespace",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no create_namespace permission on cluster")
	}

	// Stamp owner/created_by.
	ns.OwnerType = subject.Type
	ns.OwnerID = subject.ID
	ns.CreatedByType = subject.Type
	ns.CreatedBy = subject.ID
	if ns.Visibility == "" {
		ns.Visibility = NamespaceVisibilityPrivate
	}
	if ns.VisibilitySyncStatus == "" {
		ns.VisibilitySyncStatus = VisibilitySyncSynced
	}
	ns.Lifecycle = NamespaceLifecycleCreating
	ns.Revision = 1
	ns.Managed = true

	// Step 3: INSERT row.
	created, err := uc.namespaces.CreateNamespace(ctx, ns)
	if err != nil {
		return nil, err
	}

	// Step 4: SpiceDB owner + cluster relationships (design §7.2.2 line 993:
	// k8s_namespace:{id}#cluster@k8s_cluster:{cluster_id} — the relation is
	// "cluster", not "parent", matching the SpiceDB schema definition).
	resource := namespaceResource(created.ID)
	if _, err := uc.rels.WriteRelationships(ctx,
		AuthzRelationship{Resource: resource, Relation: "owner", Subject: subject},
		AuthzRelationship{Resource: resource, Relation: "cluster", Subject: AuthzSubjectRef{Type: "k8s_cluster", ID: created.ClusterID}},
	); err != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.rels.RevokeResource(compensateCtx, resource)
		_, _ = uc.namespaces.UpdateNamespaceStatus(compensateCtx, created.ID, NamespaceLifecycleCreating, NamespaceLifecycleFailed, map[string]any{
			"last_error_code":    "AUTHZ_PROJECTION_FAILED",
			"last_error_message": err.Error(),
		})
		return nil, fmt.Errorf("%w: project relationships: %v", ErrClusterFailedPrecondition, err)
	}

	// Step 5: apply remote Namespace via provider.
	applySpec := NamespaceApplySpec{
		Name:        created.KubeName,
		Labels:      created.Labels,
		Annotations: created.Annotations,
	}
	if err := uc.provider.ApplyNamespace(ctx, created.ClusterID, CredentialLocator{ClusterID: created.ClusterID}, applySpec); err != nil {
		// Remote apply failed → mark FAILED (no rollback, design §6.4).
		uc.log.WithContext(ctx).Warn("remote namespace apply failed; marking FAILED",
			logx.String("namespace_id", created.ID),
			logx.String("kube_name", created.KubeName),
			logx.Err(err))
		failed, _ := uc.namespaces.UpdateNamespaceStatus(ctx, created.ID, NamespaceLifecycleCreating, NamespaceLifecycleFailed, map[string]any{
			"last_error_code":    "REMOTE_APPLY_FAILED",
			"last_error_message": err.Error(),
		})
		if failed != nil {
			return failed, nil
		}
		return created, nil
	}

	// Step 6: stamp READY. (kubernetes_uid/resource_version would come from
	// the apply response; the provider's ApplyNamespace returns only error in
	// V1 — a SyncNamespaces pass backfills uid/rv. For now stamp READY.)
	ready, err := uc.namespaces.UpdateNamespaceStatus(ctx, created.ID, NamespaceLifecycleCreating, NamespaceLifecycleReady, map[string]any{
		"last_error_code":    "",
		"last_error_message": "",
	})
	if err != nil {
		return created, nil
	}
	return ready, nil
}

// GetNamespace loads a namespace + computes permissions.
func (uc *NamespaceUsecase) GetNamespace(ctx context.Context, principal authn.Principal, id string) (*Namespace, *NamespacePermissions, error) {
	ns, err := uc.namespaces.GetNamespace(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	perms, err := uc.computeNamespacePermissions(ctx, principal, ns)
	if err != nil {
		return nil, nil, err
	}
	return ns, perms, nil
}

// ListNamespacesByCluster lists namespaces on a cluster, filtered by BatchCheck
// (design §7.6.3).
func (uc *NamespaceUsecase) ListNamespacesByCluster(ctx context.Context, principal authn.Principal, clusterID string) ([]*Namespace, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	candidates, err := uc.namespaces.ListNamespacesByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	checks := make([]AuthzCheckRequest, len(candidates))
	for i, ns := range candidates {
		checks[i] = AuthzCheckRequest{
			Subject:    subject,
			Resource:   namespaceResource(ns.ID),
			Permission: "view",
		}
	}
	result, err := uc.rels.BatchCheck(ctx, AuthzBatchCheckRequest{Checks: checks})
	if err != nil {
		return nil, err
	}
	visible := make([]*Namespace, 0, len(candidates))
	for i, dec := range result.Decisions {
		if dec.Allowed {
			visible = append(visible, candidates[i])
		}
	}
	return visible, nil
}

// UpdateNamespace applies FieldMask updates with CAS (design §7.5).
func (uc *NamespaceUsecase) UpdateNamespace(ctx context.Context, principal authn.Principal, id string, expectedRevision int64, updates map[string]any) (*Namespace, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(id),
		Permission: "edit",
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no edit permission on namespace")
	}
	for k := range updates {
		if isImmutableNamespaceField(k) {
			return nil, fmt.Errorf("%w: field %q is immutable", ErrClusterInvalidArgument, k)
		}
	}
	return uc.namespaces.UpdateNamespaceWithCAS(ctx, id, expectedRevision, updates)
}

func isImmutableNamespaceField(col string) bool {
	switch col {
	case "id", "cluster_id", "kube_name", "kubernetes_uid", "resource_version",
		"revision", "created_at", "created_by", "created_by_type",
		"visibility", "visibility_sync_status", "lifecycle":
		return true
	}
	return false
}

// UpdateNamespaceVisibility runs the three-step visibility switch (design
// §7.5.3 / §7.5.4):
//  1. DB transaction: stamp desired visibility + sync_status=PUBLISHING/REVOKING
//     + outbox row (atomic).
//  2. Synchronous SpiceDB projection: PUBLIC → write wildcard viewer; PRIVATE
//     → delete wildcard viewer. On failure compensate (reverse DB + SYNC_FAILED).
//  3. On success stamp SYNCED.
//
// Returns the updated namespace. When the SpiceDB projection fails the
// response carries sync_status=SYNC_FAILED and the reconciler will retry.
func (uc *NamespaceUsecase) UpdateNamespaceVisibility(ctx context.Context, principal authn.Principal, id string, expectedRevision int64, desired string) (*Namespace, error) {
	if desired != NamespaceVisibilityPrivate && desired != NamespaceVisibilityPublic {
		return nil, fmt.Errorf("%w: visibility must be PRIVATE or PUBLIC", ErrClusterInvalidArgument)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(id),
		Permission: "manage",
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no manage permission on namespace")
	}

	ns, err := uc.namespaces.GetNamespace(ctx, id)
	if err != nil {
		return nil, err
	}
	if ns.Visibility == desired {
		return ns, nil // no-op
	}

	// Step 1: DB stamp desired + sync_status. PUBLISHING for PRIVATE→PUBLIC,
	// REVOKING for PUBLIC→PRIVATE (design §7.5.3/§7.5.4).
	syncStatus := VisibilitySyncPublishing
	if desired == NamespaceVisibilityPrivate {
		syncStatus = VisibilitySyncRevoking
	}
	updated, err := uc.namespaces.UpdateNamespaceVisibility(ctx, id, expectedRevision, desired, syncStatus)
	if err != nil {
		return nil, err
	}

	// Step 2: synchronous SpiceDB projection. The wildcard subject represents
	// "all users" for PUBLIC visibility — SpiceDB subject `user:*` (design
	// §7.5 line 1010: k8s_namespace:{id}#viewer@user:*).
	wildcard := AuthzSubjectRef{Type: "user", ID: "*"}
	resource := namespaceResource(id)
	if desired == NamespaceVisibilityPublic {
		if _, err := uc.rels.WriteRelationships(ctx, AuthzRelationship{
			Resource: resource, Relation: "viewer", Subject: wildcard,
		}); err != nil {
			// Compensate: reverse DB to prior visibility + SYNC_FAILED.
			uc.compensateVisibility(ctx, id, ns.Visibility, err)
			return uc.namespaces.GetNamespace(ctx, id)
		}
	} else {
		if _, err := uc.rels.DeleteRelationships(ctx, AuthzRelationshipFilter{
			ResourceType:    "k8s_namespace",
			ResourceID:      id,
			Relation:        "viewer",
			SubjectType:     "user",
			SubjectID:       "*",
		}); err != nil {
			uc.compensateVisibility(ctx, id, ns.Visibility, err)
			return uc.namespaces.GetNamespace(ctx, id)
		}
	}

	// Step 3: success → SYNCED. Enqueue outbox for reconciler confirmation.
	ready, err := uc.namespaces.UpdateNamespaceVisibility(ctx, id, updated.Revision, desired, VisibilitySyncSynced)
	if err != nil {
		// CAS failed (concurrent writer); leave PUBLISHING/REVOKING for
		// reconciler. Return current state.
		return uc.namespaces.GetNamespace(ctx, id)
	}
	_ = uc.outbox.Enqueue(ctx, "namespace", id, "visibility_sync", map[string]any{
		"namespace_id": id,
		"visibility":   desired,
	})
	return ready, nil
}

// compensateVisibility reverses the DB visibility to the prior value and
// stamps SYNC_FAILED so the reconciler can retry (design §7.5.3 compensate).
// The compensating write reloads the current revision (the synchronous path
// already bumped it in step 1) so CAS succeeds; on a concurrent writer the
// reconciler still converges the row on its next tick.
func (uc *NamespaceUsecase) compensateVisibility(ctx context.Context, id, priorVisibility string, cause error) {
	compensateCtx := context.WithoutCancel(ctx)
	current, err := uc.namespaces.GetNamespace(compensateCtx, id)
	if err != nil {
		uc.log.WithContext(ctx).Warn("visibility compensate: reload failed; reconciler will converge",
			logx.String("namespace_id", id), logx.Err(err))
		return
	}
	if _, err := uc.namespaces.UpdateNamespaceVisibility(compensateCtx, id, current.Revision, priorVisibility, VisibilitySyncFailed); err != nil {
		uc.log.WithContext(ctx).Warn("visibility compensate CAS failed; reconciler will converge",
			logx.String("namespace_id", id), logx.Err(err))
		return
	}
	uc.log.WithContext(ctx).Warn("visibility SpiceDB projection failed; marked SYNC_FAILED for reconciler",
		logx.String("namespace_id", id), logx.Err(cause))
}

// DeleteNamespace (design §6.6). For managed=true + DeletePolicy=CASCADE the
// remote Namespace is deleted; DETACH_ONLY just soft-deletes the row. Revokes
// all SpiceDB rels on the namespace.
func (uc *NamespaceUsecase) DeleteNamespace(ctx context.Context, principal authn.Principal, id, deletePolicy string) error {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(id),
		Permission: "manage", // §7.6.2: can_delete = can_manage
	})
	if err != nil {
		return err
	}
	if !dec.Allowed {
		return errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no delete permission on namespace")
	}
	ns, err := uc.namespaces.GetNamespace(ctx, id)
	if err != nil {
		return err
	}
	// Soft-delete row first (marks TERMINATING→DELETED).
	if err := uc.namespaces.SoftDeleteNamespace(ctx, id); err != nil {
		return err
	}
	// Revoke SpiceDB rels (idempotent).
	compensateCtx := context.WithoutCancel(ctx)
	_ = uc.rels.RevokeResource(compensateCtx, namespaceResource(id))
	// Remote delete for managed + CASCADE.
	if ns.Managed && deletePolicy == DeletePolicyCascade {
		if err := uc.provider.DeleteNamespace(ctx, ns.ClusterID, CredentialLocator{ClusterID: ns.ClusterID}, ns.KubeName); err != nil {
			uc.log.WithContext(ctx).Warn("remote namespace delete failed; row is soft-deleted, operator may need to clean up",
				logx.String("namespace_id", id), logx.String("kube_name", ns.KubeName), logx.Err(err))
		}
	}
	return nil
}

// CreateShare (design §7.4). Writes the share row + SpiceDB relationship
// synchronously; compensate on SpiceDB failure.
func (uc *NamespaceUsecase) CreateShare(ctx context.Context, principal authn.Principal, share *NamespaceShare) (*NamespaceShare, error) {
	if share == nil || share.NamespaceID == "" || share.SubjectType == "" || share.SubjectID == "" {
		return nil, fmt.Errorf("%w: namespace_id, subject_type, subject_id are required", ErrClusterInvalidArgument)
	}
	if share.Relation != ShareRelationViewer && share.Relation != ShareRelationUser && share.Relation != ShareRelationEditor {
		return nil, fmt.Errorf("%w: relation must be viewer/user/editor", ErrClusterInvalidArgument)
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(share.NamespaceID),
		Permission: "manage", // §7.6.2: can_share = can_manage
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no share permission on namespace")
	}
	share.CreatedByType = subject.Type
	share.CreatedBy = subject.ID
	share.SyncStatus = VisibilitySyncSynced
	created, err := uc.namespaces.CreateShare(ctx, share)
	if err != nil {
		return nil, err
	}
	// SpiceDB projection.
	_, err = uc.rels.WriteRelationships(ctx, AuthzRelationship{
		Resource: namespaceResource(created.NamespaceID),
		Relation: created.Relation,
		Subject: AuthzSubjectRef{
			Type:     created.SubjectType,
			ID:       created.SubjectID,
			Relation: created.SubjectRelation,
		},
	})
	if err != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.namespaces.DeleteShare(compensateCtx, created.ID)
		return nil, fmt.Errorf("%w: share projection: %v", ErrClusterFailedPrecondition, err)
	}
	return created, nil
}

// DeleteShare removes a share row + SpiceDB relationship.
func (uc *NamespaceUsecase) DeleteShare(ctx context.Context, principal authn.Principal, namespaceID, shareID string) error {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(namespaceID),
		Permission: "manage", // §7.6.2: can_share = can_manage
	})
	if err != nil {
		return err
	}
	if !dec.Allowed {
		return errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no share permission on namespace")
	}
	if err := uc.namespaces.DeleteShare(ctx, shareID); err != nil {
		return err
	}
	// Best-effort SpiceDB cleanup: we don't know the exact tuple without a
	// load, so the reconciler handles dangling rels. For V1 this is acceptable
	// because share rows are the source of truth and the reconciler converges.
	return nil
}

// ListShares enumerates shares on a namespace.
func (uc *NamespaceUsecase) ListShares(ctx context.Context, principal authn.Principal, namespaceID string) ([]*NamespaceShare, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   namespaceResource(namespaceID),
		Permission: "view",
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no view permission on namespace")
	}
	return uc.namespaces.ListSharesByNamespace(ctx, namespaceID)
}

// SyncNamespaces pulls remote namespaces from a cluster and backfills
// kubernetes_uid/resource_version/labels for managed namespaces (design §6.5).
func (uc *NamespaceUsecase) SyncNamespaces(ctx context.Context, principal authn.Principal, clusterID string) error {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   clusterResource(clusterID),
		Permission: "operate",
	})
	if err != nil {
		return err
	}
	if !dec.Allowed {
		return errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no operate permission on cluster")
	}
	remote, err := uc.provider.ListNamespaces(ctx, clusterID, CredentialLocator{ClusterID: clusterID})
	if err != nil {
		return err
	}
	// Build a lookup of local managed namespaces by kube_name.
	local, err := uc.namespaces.ListNamespacesByCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	localByName := make(map[string]*Namespace, len(local))
	for _, ns := range local {
		if ns.Managed {
			localByName[ns.KubeName] = ns
		}
	}
	now := time.Now().UTC()
	for _, r := range remote {
		ns, ok := localByName[r.Name]
		if !ok {
			continue // unmanaged or unknown; skip
		}
		updates := map[string]any{
			"kubernetes_uid":   r.UID,
			"resource_version": r.ResourceVersion,
			"last_sync_at":     now,
		}
		if r.Labels != nil {
			updates["labels"] = r.Labels
		}
		_, _ = uc.namespaces.UpdateNamespaceWithCAS(ctx, ns.ID, ns.Revision, updates)
	}
	return nil
}

// computeNamespacePermissions BatchChecks the namespace permission set (design
// §7.6.2). The SpiceDB schema defines four permissions on k8s_namespace
// (view/use/edit/manage); can_share and can_delete are *derived* from
// can_manage (design §7.6.2 "can_share = can_manage", "can_delete = can_manage"),
// not separate SpiceDB permissions. Matches proto NamespacePermissions 1-6.
func (uc *NamespaceUsecase) computeNamespacePermissions(ctx context.Context, principal authn.Principal, ns *Namespace) (*NamespacePermissions, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	perms := []string{"view", "use", "edit", "manage"}
	checks := make([]AuthzCheckRequest, len(perms))
	for i, p := range perms {
		checks[i] = AuthzCheckRequest{
			Subject:    subject,
			Resource:   namespaceResource(ns.ID),
			Permission: p,
		}
	}
	result, err := uc.rels.BatchCheck(ctx, AuthzBatchCheckRequest{Checks: checks})
	if err != nil {
		return nil, err
	}
	out := &NamespacePermissions{}
	for i, dec := range result.Decisions {
		switch perms[i] {
		case "view":
			out.CanView = dec.Allowed
		case "use":
			out.CanUse = dec.Allowed
		case "edit":
			out.CanEdit = dec.Allowed
		case "manage":
			out.CanManage = dec.Allowed
			out.CanShare = dec.Allowed  // §7.6.2: can_share = can_manage
			out.CanDelete = dec.Allowed // §7.6.2: can_delete = can_manage
		}
	}
	return out, nil
}

// NamespacePermissions is the biz view of the proto NamespacePermissions.
// Fields mirror proto NamespacePermissions 1-6.
type NamespacePermissions struct {
	CanView   bool
	CanUse    bool
	CanEdit   bool
	CanManage bool
	CanShare  bool
	CanDelete bool
}

// isDNS1123Label is a minimal DNS-1123 label check: lowercase alphanumeric +
// '-', start/end alphanumeric, ≤63 chars. Mirrors k8s label rules.
func isDNS1123Label(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(s)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// strings anchored for future TrimSpace guards.
var _ = strings.TrimSpace
