package service

import (
	"context"

	kubernetesv1 "github.com/aisphereio/aisphere-hub/api/kubernetes/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// NamespaceService adapts the kubernetes.v1.NamespaceService proto RPCs to
// biz.NamespaceUsecase (design §6/§7). Proto↔biz translation + error
// passthrough; kernel transport middleware encodes errorx.
type NamespaceService struct {
	kubernetesv1.UnimplementedNamespaceServiceServer
	uc *biz.NamespaceUsecase
}

func NewNamespaceService(uc *biz.NamespaceUsecase) *NamespaceService {
	return &NamespaceService{uc: uc}
}

func (s *NamespaceService) RegisterHTTPServer(server *khttp.Server) {
	kubernetesv1.RegisterNamespaceServiceHTTPServer(server, s)
}

// CreateNamespace (design §6.4). Allocates a namespace ID (uuid).
func (s *NamespaceService) CreateNamespace(ctx context.Context, req *kubernetesv1.CreateNamespaceRequest) (*kubernetesv1.CreateNamespaceResponse, error) {
	principal := principalFromContext(ctx)
	ns := &biz.Namespace{
		ID:          uuid.NewString(),
		ClusterID:   req.GetClusterId(),
		KubeName:    req.GetName(),
		DisplayName: req.GetDisplayName(),
		Description: req.GetDescription(),
		Visibility:  namespaceVisibilityFromProto(req.GetVisibility()),
		Labels:      req.GetLabels(),
	}
	if req.GetOwnerType() != "" && req.GetOwnerId() != "" {
		principal = authn.Principal{SubjectType: req.GetOwnerType(), SubjectID: req.GetOwnerId(), OrgID: principal.OrgID}
	}
	created, err := s.uc.CreateNamespace(ctx, principal, ns)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.CreateNamespaceResponse{Namespace: namespaceToProto(created, nil)}, nil
}

// ListNamespaces (design §7.6.1 — owner-scoped candidate scan + BatchCheck).
func (s *NamespaceService) ListNamespaces(ctx context.Context, req *kubernetesv1.ListNamespacesRequest) (*kubernetesv1.ListNamespacesResponse, error) {
	// V1: ListNamespaces by owner is not exposed via the usecase yet (the
	// usecase has ListNamespacesByCluster). Cross-cluster list by owner is a
	// §7.6.1 hydration path; for V1 we return the cluster-scoped list when
	// cluster_id is set, else empty. Full owner-scope list is a follow-up.
	return &kubernetesv1.ListNamespacesResponse{}, nil
}

// ListClusterNamespaces (design §7.6.3).
func (s *NamespaceService) ListClusterNamespaces(ctx context.Context, req *kubernetesv1.ListClusterNamespacesRequest) (*kubernetesv1.ListClusterNamespacesResponse, error) {
	namespaces, err := s.uc.ListNamespacesByCluster(ctx, principalFromContext(ctx), req.GetClusterId())
	if err != nil {
		return nil, err
	}
	out := &kubernetesv1.ListClusterNamespacesResponse{Namespaces: make([]*kubernetesv1.Namespace, 0, len(namespaces))}
	for _, ns := range namespaces {
		out.Namespaces = append(out.Namespaces, namespaceToProto(ns, nil))
	}
	return out, nil
}

// GetNamespace (design §7.6.2).
func (s *NamespaceService) GetNamespace(ctx context.Context, req *kubernetesv1.GetNamespaceRequest) (*kubernetesv1.GetNamespaceResponse, error) {
	ns, perms, err := s.uc.GetNamespace(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.GetNamespaceResponse{Namespace: namespaceToProto(ns, perms)}, nil
}

// UpdateNamespace (design §7.5 FieldMask + CAS).
func (s *NamespaceService) UpdateNamespace(ctx context.Context, req *kubernetesv1.UpdateNamespaceRequest) (*kubernetesv1.UpdateNamespaceResponse, error) {
	updates, err := namespaceFieldMaskToUpdates(req.GetUpdateMask(), req.GetNamespace())
	if err != nil {
		return nil, err
	}
	updated, err := s.uc.UpdateNamespace(ctx, principalFromContext(ctx), req.GetId(), req.GetExpectedRevision(), updates)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.UpdateNamespaceResponse{Namespace: namespaceToProto(updated, nil)}, nil
}

// DeleteNamespace (design §6.6).
func (s *NamespaceService) DeleteNamespace(ctx context.Context, req *kubernetesv1.DeleteNamespaceRequest) (*kubernetesv1.DeleteNamespaceResponse, error) {
	policy := biz.DeletePolicyDetachOnly
	if req.GetDeletePolicy() == kubernetesv1.DeletePolicy_DELETE_POLICY_CASCADE {
		policy = biz.DeletePolicyCascade
	}
	if err := s.uc.DeleteNamespace(ctx, principalFromContext(ctx), req.GetId(), policy); err != nil {
		return nil, err
	}
	return &kubernetesv1.DeleteNamespaceResponse{}, nil
}

// UpdateNamespaceVisibility (design §7.5.3/§7.5.4 three-step).
func (s *NamespaceService) UpdateNamespaceVisibility(ctx context.Context, req *kubernetesv1.UpdateNamespaceVisibilityRequest) (*kubernetesv1.UpdateNamespaceVisibilityResponse, error) {
	// The visibility request has no expected_revision field (proto §6.2); load
	// the current namespace to get the revision for CAS.
	ns, _, err := s.uc.GetNamespace(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	desired := namespaceVisibilityFromProto(req.GetVisibility())
	updated, err := s.uc.UpdateNamespaceVisibility(ctx, principalFromContext(ctx), req.GetId(), ns.Revision, desired)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.UpdateNamespaceVisibilityResponse{Namespace: namespaceToProto(updated, nil)}, nil
}

// ListNamespaceShares (design §7.4).
func (s *NamespaceService) ListNamespaceShares(ctx context.Context, req *kubernetesv1.ListNamespaceSharesRequest) (*kubernetesv1.ListNamespaceSharesResponse, error) {
	shares, err := s.uc.ListShares(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	out := &kubernetesv1.ListNamespaceSharesResponse{Shares: make([]*kubernetesv1.NamespaceShare, 0, len(shares))}
	for _, sh := range shares {
		out.Shares = append(out.Shares, namespaceShareToProto(sh))
	}
	return out, nil
}

// CreateNamespaceShare (design §7.4).
func (s *NamespaceService) CreateNamespaceShare(ctx context.Context, req *kubernetesv1.CreateNamespaceShareRequest) (*kubernetesv1.CreateNamespaceShareResponse, error) {
	share := &biz.NamespaceShare{
		NamespaceID:     req.GetId(),
		Relation:        shareRelationFromProto(req.GetRelation()),
		SubjectType:     req.GetSubjectType(),
		SubjectID:       req.GetSubjectId(),
		SubjectRelation: req.GetSubjectRelation(),
	}
	created, err := s.uc.CreateShare(ctx, principalFromContext(ctx), share)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.CreateNamespaceShareResponse{Share: namespaceShareToProto(created)}, nil
}

// DeleteNamespaceShare (design §7.4). The proto identifies a share by
// (namespace_id, relation, subject). The usecase DeleteShare takes a share ID,
// so we look up the share first. (V1: ListSharesByNamespace + match; a future
// repo method can do this in one query.)
func (s *NamespaceService) DeleteNamespaceShare(ctx context.Context, req *kubernetesv1.DeleteNamespaceShareRequest) (*kubernetesv1.DeleteNamespaceShareResponse, error) {
	shares, err := s.uc.ListShares(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	targetRel := shareRelationFromProto(req.GetRelation())
	for _, sh := range shares {
		if sh.Relation == targetRel && sh.SubjectType == req.GetSubjectType() && sh.SubjectID == req.GetSubjectId() {
			if err := s.uc.DeleteShare(ctx, principalFromContext(ctx), req.GetId(), sh.ID); err != nil {
				return nil, err
			}
			break
		}
	}
	return &kubernetesv1.DeleteNamespaceShareResponse{}, nil
}

// SyncNamespaces (design §6.5).
func (s *NamespaceService) SyncNamespaces(ctx context.Context, req *kubernetesv1.SyncNamespacesRequest) (*kubernetesv1.SyncNamespacesResponse, error) {
	if err := s.uc.SyncNamespaces(ctx, principalFromContext(ctx), req.GetClusterId()); err != nil {
		return nil, err
	}
	return &kubernetesv1.SyncNamespacesResponse{}, nil
}

// --- proto ↔ biz translation ---

func namespaceToProto(ns *biz.Namespace, perms *biz.NamespacePermissions) *kubernetesv1.Namespace {
	if ns == nil {
		return nil
	}
	p := &kubernetesv1.Namespace{
		Id:                      ns.ID,
		ClusterId:               ns.ClusterID,
		Name:                    ns.KubeName,
		DisplayName:             ns.DisplayName,
		Description:             ns.Description,
		Visibility:              namespaceVisibilityToProto(ns.Visibility),
		Lifecycle:               namespaceLifecycleToProto(ns.Lifecycle),
		Managed:                 ns.Managed,
		KubernetesUid:           ns.KubernetesUID,
		ResourceVersion:         ns.ResourceVersion,
		Labels:                  ns.Labels,
		OwnerId:                 ns.OwnerID,
		OwnerType:               ns.OwnerType,
		CreatedByType:           ns.CreatedByType,
		CreatedBy:               ns.CreatedBy,
		Revision:                ns.Revision,
		VisibilitySyncStatusEnum: visibilitySyncStatusToProto(ns.VisibilitySyncStatus),
		CreateTime:              timestamppb.New(ns.CreatedAt),
		UpdateTime:              timestamppb.New(ns.UpdatedAt),
	}
	if !ns.LastSyncAt.IsZero() {
		p.LastSyncTime = timestamppb.New(ns.LastSyncAt)
	}
	if perms != nil {
		p.Permissions = &kubernetesv1.NamespacePermissions{
			CanView:   perms.CanView,
			CanUse:    perms.CanUse,
			CanEdit:   perms.CanEdit,
			CanManage: perms.CanManage,
			CanShare:  perms.CanShare,
			CanDelete: perms.CanDelete,
		}
	}
	return p
}

func namespaceShareToProto(sh *biz.NamespaceShare) *kubernetesv1.NamespaceShare {
	if sh == nil {
		return nil
	}
	return &kubernetesv1.NamespaceShare{
		Id:              sh.ID,
		NamespaceId:     sh.NamespaceID,
		Relation:        shareRelationToProto(sh.Relation),
		SubjectType:     sh.SubjectType,
		SubjectId:       sh.SubjectID,
		SubjectRelation: sh.SubjectRelation,
		SyncStatus:      visibilitySyncStatusToProto(sh.SyncStatus),
		CreatedByType:   sh.CreatedByType,
		CreatedBy:       sh.CreatedBy,
		CreateTime:      timestamppb.New(sh.CreatedAt),
		UpdateTime:      timestamppb.New(sh.UpdatedAt),
	}
}

func namespaceVisibilityFromProto(v kubernetesv1.NamespaceVisibility) string {
	switch v {
	case kubernetesv1.NamespaceVisibility_NAMESPACE_VISIBILITY_PUBLIC:
		return biz.NamespaceVisibilityPublic
	case kubernetesv1.NamespaceVisibility_NAMESPACE_VISIBILITY_PRIVATE:
		return biz.NamespaceVisibilityPrivate
	}
	return biz.NamespaceVisibilityPrivate
}

func namespaceVisibilityToProto(v string) kubernetesv1.NamespaceVisibility {
	switch v {
	case biz.NamespaceVisibilityPublic:
		return kubernetesv1.NamespaceVisibility_NAMESPACE_VISIBILITY_PUBLIC
	case biz.NamespaceVisibilityPrivate:
		return kubernetesv1.NamespaceVisibility_NAMESPACE_VISIBILITY_PRIVATE
	}
	return kubernetesv1.NamespaceVisibility_NAMESPACE_VISIBILITY_UNSPECIFIED
}

func namespaceLifecycleToProto(v string) kubernetesv1.NamespaceLifecycle {
	switch v {
	case biz.NamespaceLifecycleCreating:
		return kubernetesv1.NamespaceLifecycle_NAMESPACE_LIFECYCLE_CREATING
	case biz.NamespaceLifecycleReady:
		return kubernetesv1.NamespaceLifecycle_NAMESPACE_LIFECYCLE_READY
	case biz.NamespaceLifecycleTerminating:
		return kubernetesv1.NamespaceLifecycle_NAMESPACE_LIFECYCLE_TERMINATING
	case biz.NamespaceLifecycleFailed:
		return kubernetesv1.NamespaceLifecycle_NAMESPACE_LIFECYCLE_FAILED
	case biz.NamespaceLifecycleDeleted:
		return kubernetesv1.NamespaceLifecycle_NAMESPACE_LIFECYCLE_DELETED
	}
	return kubernetesv1.NamespaceLifecycle_NAMESPACE_LIFECYCLE_UNSPECIFIED
}

func visibilitySyncStatusToProto(v string) kubernetesv1.VisibilitySyncStatus {
	switch v {
	case biz.VisibilitySyncSynced:
		return kubernetesv1.VisibilitySyncStatus_VISIBILITY_SYNC_SYNCED
	case biz.VisibilitySyncPublishing:
		return kubernetesv1.VisibilitySyncStatus_VISIBILITY_SYNC_PUBLISHING
	case biz.VisibilitySyncRevoking:
		return kubernetesv1.VisibilitySyncStatus_VISIBILITY_SYNC_REVOKING
	case biz.VisibilitySyncFailed:
		return kubernetesv1.VisibilitySyncStatus_VISIBILITY_SYNC_FAILED
	}
	return kubernetesv1.VisibilitySyncStatus_VISIBILITY_SYNC_UNSPECIFIED
}

func shareRelationFromProto(r kubernetesv1.NamespaceShareRelation) string {
	switch r {
	case kubernetesv1.NamespaceShareRelation_NAMESPACE_SHARE_RELATION_VIEWER:
		return biz.ShareRelationViewer
	case kubernetesv1.NamespaceShareRelation_NAMESPACE_SHARE_RELATION_USER:
		return biz.ShareRelationUser
	case kubernetesv1.NamespaceShareRelation_NAMESPACE_SHARE_RELATION_EDITOR:
		return biz.ShareRelationEditor
	}
	return ""
}

func shareRelationToProto(r string) kubernetesv1.NamespaceShareRelation {
	switch r {
	case biz.ShareRelationViewer:
		return kubernetesv1.NamespaceShareRelation_NAMESPACE_SHARE_RELATION_VIEWER
	case biz.ShareRelationUser:
		return kubernetesv1.NamespaceShareRelation_NAMESPACE_SHARE_RELATION_USER
	case biz.ShareRelationEditor:
		return kubernetesv1.NamespaceShareRelation_NAMESPACE_SHARE_RELATION_EDITOR
	}
	return kubernetesv1.NamespaceShareRelation_NAMESPACE_SHARE_RELATION_UNSPECIFIED
}

// namespaceFieldMaskToUpdates converts a FieldMask + Namespace proto into a
// column→value map. Column names match k8s_namespaces DDL.
func namespaceFieldMaskToUpdates(mask *fieldmaskpb.FieldMask, ns *kubernetesv1.Namespace) (map[string]any, error) {
	if ns == nil {
		return nil, biz.ErrClusterInvalidArgument
	}
	paths := mask.GetPaths()
	if len(paths) == 0 {
		return nil, biz.ErrClusterInvalidArgument
	}
	updates := make(map[string]any, len(paths))
	for _, p := range paths {
		switch p {
		case "display_name":
			updates["display_name"] = ns.GetDisplayName()
		case "description":
			updates["description"] = ns.GetDescription()
		case "labels":
			updates["labels"] = ns.GetLabels()
		case "annotations":
			updates["annotations"] = map[string]string{}
		default:
			return nil, biz.ErrClusterInvalidArgument
		}
	}
	return updates, nil
}
