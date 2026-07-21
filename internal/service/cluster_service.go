package service

import (
	"context"

	kubernetesv1 "github.com/aisphereio/aisphere-hub/api/kubernetes/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/kubernetesx"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// newClusterID allocates a UUID v4 cluster ID. The biz layer needs the ID
// before CreateCluster (AEAD AAD binds it), so the service layer generates it.
func newClusterID() string { return uuid.NewString() }

// ClusterService adapts the kubernetes.v1.ClusterService proto RPCs to
// biz.ClusterUsecase (design §5.7). It does proto↔biz translation and error
// passthrough; kernel transport middleware encodes errorx into HTTP/gRPC
// status. Authn principal comes from context (set by auth middleware).
type ClusterService struct {
	kubernetesv1.UnimplementedClusterServiceServer
	uc *biz.ClusterUsecase
}

// NewClusterService builds the service from the usecase.
func NewClusterService(uc *biz.ClusterUsecase) *ClusterService {
	return &ClusterService{uc: uc}
}

// RegisterHTTPServer registers the HTTP handlers on the kernel HTTP server.
func (s *ClusterService) RegisterHTTPServer(server *khttp.Server) {
	kubernetesv1.RegisterClusterServiceHTTPServer(server, s)
}

// CreateCluster (design §5.7.2). Translates the ClusterCredentialInput oneof
// into a kubernetesx.Credential, allocates a cluster ID (uuid), and calls the
// usecase. owner_type/owner_id in the request override the principal-derived
// owner only when non-empty (service-account callers).
func (s *ClusterService) CreateCluster(ctx context.Context, req *kubernetesv1.CreateClusterRequest) (*kubernetesv1.CreateClusterResponse, error) {
	cred, err := credentialInputToKubernetesx(req.GetCredential(), req.GetServerUrl())
	if err != nil {
		return nil, err
	}
	principal := principalFromContext(ctx)
	clusterID := newClusterID()
	c := &biz.Cluster{
		ID:           clusterID,
		OrgID:        req.GetOrgId(),
		Name:         req.GetName(),
		DisplayName:  req.GetDisplayName(),
		Description:  req.GetDescription(),
		ServerURL:    req.GetServerUrl(),
		Distribution: req.GetDistribution(),
		Labels:       req.GetLabels(),
	}
	// Allow service-account callers to set owner explicitly; user callers
	// inherit the principal (usecase stamps owner_type/owner_id from principal
	// when these are empty).
	if req.GetOwnerType() != "" && req.GetOwnerId() != "" {
		// Override principal so the usecase stamps the requested owner.
		principal = authn.Principal{SubjectType: req.GetOwnerType(), SubjectID: req.GetOwnerId(), OrgID: req.GetOrgId()}
	}
	created, err := s.uc.CreateCluster(ctx, principal, c, cred)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.CreateClusterResponse{Cluster: clusterToProto(created, nil)}, nil
}

// ListClusters (design §5.7.1 / §7.6.3).
func (s *ClusterService) ListClusters(ctx context.Context, req *kubernetesv1.ListClustersRequest) (*kubernetesv1.ListClustersResponse, error) {
	principal := principalFromContext(ctx)
	orgID := req.GetOrgId()
	if orgID == "" {
		orgID = principal.OrgID
	}
	clusters, nextCursor, err := s.uc.ListClusters(ctx, principal, orgID, req.GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &kubernetesv1.ListClustersResponse{Clusters: make([]*kubernetesv1.Cluster, 0, len(clusters))}
	for _, c := range clusters {
		out.Clusters = append(out.Clusters, clusterToProto(c, nil))
	}
	out.NextPageToken = nextCursor
	return out, nil
}

// GetCluster (design §7.6.2).
func (s *ClusterService) GetCluster(ctx context.Context, req *kubernetesv1.GetClusterRequest) (*kubernetesv1.GetClusterResponse, error) {
	c, perms, err := s.uc.GetCluster(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.GetClusterResponse{Cluster: clusterToProto(c, perms)}, nil
}

// UpdateCluster (design §5.7.4). Translates the FieldMask into a column→value
// map. Immutable fields are rejected by the usecase.
func (s *ClusterService) UpdateCluster(ctx context.Context, req *kubernetesv1.UpdateClusterRequest) (*kubernetesv1.UpdateClusterResponse, error) {
	updates, err := clusterFieldMaskToUpdates(req.GetUpdateMask(), req.GetCluster())
	if err != nil {
		return nil, err
	}
	updated, err := s.uc.UpdateCluster(ctx, principalFromContext(ctx), req.GetId(), req.GetExpectedRevision(), updates)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.UpdateClusterResponse{Cluster: clusterToProto(updated, nil)}, nil
}

// DeleteCluster (design §5.7.5).
func (s *ClusterService) DeleteCluster(ctx context.Context, req *kubernetesv1.DeleteClusterRequest) (*kubernetesv1.DeleteClusterResponse, error) {
	policy := biz.DeletePolicyDetachOnly
	if req.GetDeletePolicy() == kubernetesv1.DeletePolicy_DELETE_POLICY_CASCADE {
		policy = biz.DeletePolicyCascade
	}
	if err := s.uc.DeleteCluster(ctx, principalFromContext(ctx), req.GetId(), policy); err != nil {
		return nil, err
	}
	return &kubernetesv1.DeleteClusterResponse{}, nil
}

// ProbeCluster (design §5.7.6).
func (s *ClusterService) ProbeCluster(ctx context.Context, req *kubernetesv1.ProbeClusterRequest) (*kubernetesv1.ProbeClusterResponse, error) {
	updated, err := s.uc.ProbeCluster(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.ProbeClusterResponse{Cluster: clusterToProto(updated, nil)}, nil
}

// RotateCredential (design §5.7.3).
func (s *ClusterService) RotateCredential(ctx context.Context, req *kubernetesv1.RotateCredentialRequest) (*kubernetesv1.RotateCredentialResponse, error) {
	// Load the cluster to get server_url for the credential (the rotate
	// request carries only the credential oneof, not server_url).
	c, _, err := s.uc.GetCluster(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	cred, err := credentialInputToKubernetesx(req.GetCredential(), c.ServerURL)
	if err != nil {
		return nil, err
	}
	updated, err := s.uc.RotateCredential(ctx, principalFromContext(ctx), req.GetId(), req.GetExpectedRevision(), cred)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.RotateCredentialResponse{Cluster: clusterToProto(updated, nil)}, nil
}

// --- proto ↔ biz translation ---

// clusterToProto maps a biz.Cluster to the proto message. perms is optional
// (nil for responses that don't carry permissions).
func clusterToProto(c *biz.Cluster, perms *biz.ClusterPermissions) *kubernetesv1.Cluster {
	if c == nil {
		return nil
	}
	p := &kubernetesv1.Cluster{
		Id:                c.ID,
		Name:              c.Name,
		DisplayName:       c.DisplayName,
		Description:       c.Description,
		OrgId:             c.OrgID,
		ServerUrl:         c.ServerURL,
		Distribution:      c.Distribution,
		KubernetesVersion: c.KubernetesVersion,
		Status:            clusterStatusToProto(c.Status),
		HealthMessage:     c.HealthMessage,
		Labels:            c.Labels,
		Revision:          c.Revision,
		OwnerType:         c.OwnerType,
		OwnerId:           c.OwnerID,
		CreateTime:        timestamppb.New(c.CreatedAt),
		UpdateTime:        timestamppb.New(c.UpdatedAt),
	}
	if !c.LastProbeAt.IsZero() {
		p.LastProbeTime = timestamppb.New(c.LastProbeAt)
	}
	if perms != nil {
		p.Permissions = &kubernetesv1.ClusterPermissions{
			CanView:            perms.CanView,
			CanOperate:         perms.CanOperate,
			CanManage:          perms.CanManage,
			CanCreateNamespace: perms.CanCreateNamespace,
			CanDelete:          perms.CanDelete,
		}
	}
	return p
}

func clusterStatusToProto(s string) kubernetesv1.ClusterStatus {
	switch s {
	case biz.ClusterStatusCreating:
		return kubernetesv1.ClusterStatus_CLUSTER_STATUS_CREATING
	case biz.ClusterStatusReady:
		return kubernetesv1.ClusterStatus_CLUSTER_STATUS_READY
	case biz.ClusterStatusProbing:
		return kubernetesv1.ClusterStatus_CLUSTER_STATUS_PROBING
	case biz.ClusterStatusDegraded:
		return kubernetesv1.ClusterStatus_CLUSTER_STATUS_DEGRADED
	case biz.ClusterStatusDeleting:
		return kubernetesv1.ClusterStatus_CLUSTER_STATUS_DELETING
	case biz.ClusterStatusDeleted:
		return kubernetesv1.ClusterStatus_CLUSTER_STATUS_DELETED
	case biz.ClusterStatusFailed:
		return kubernetesv1.ClusterStatus_CLUSTER_STATUS_FAILED
	}
	return kubernetesv1.ClusterStatus_CLUSTER_STATUS_UNSPECIFIED
}

// credentialInputToKubernetesx translates the proto oneof into a
// kubernetesx.Credential. serverURL supplies Host for ServiceAccount/
// InCluster (the proto SA message carries only token + ca_cert; Host comes
// from the request's server_url).
func credentialInputToKubernetesx(in *kubernetesv1.ClusterCredentialInput, serverURL string) (kubernetesx.Credential, error) {
	if in == nil {
		return kubernetesx.Credential{}, biz.ErrClusterCredentialInvalid
	}
	switch src := in.GetSource().(type) {
	case *kubernetesv1.ClusterCredentialInput_Kubeconfig:
		return kubernetesx.Credential{Kind: kubernetesx.CredentialKindKubeconfig, Kubeconfig: src.Kubeconfig}, nil
	case *kubernetesv1.ClusterCredentialInput_InCluster:
		return kubernetesx.Credential{Kind: kubernetesx.CredentialKindInCluster, Host: serverURL}, nil
	case *kubernetesv1.ClusterCredentialInput_ServiceAccount:
		sa := src.ServiceAccount
		if sa == nil {
			return kubernetesx.Credential{}, biz.ErrClusterCredentialInvalid
		}
		return kubernetesx.Credential{
			Kind:   kubernetesx.CredentialKindServiceAccount,
			Host:   serverURL,
			Token:  sa.GetToken(),
			CACert: sa.GetCaCert(),
		}, nil
	default:
		return kubernetesx.Credential{}, biz.ErrClusterCredentialInvalid
	}
}

// clusterFieldMaskToUpdates converts a FieldMask + Cluster proto into a
// column→value map for the usecase. Only fields present in the mask are
// included. Column names match k8s_clusters DDL.
func clusterFieldMaskToUpdates(mask *fieldmaskpb.FieldMask, c *kubernetesv1.Cluster) (map[string]any, error) {
	if c == nil {
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
			updates["display_name"] = c.GetDisplayName()
		case "description":
			updates["description"] = c.GetDescription()
		case "distribution":
			updates["distribution"] = c.GetDistribution()
		case "labels":
			updates["labels"] = c.GetLabels()
		default:
			return nil, biz.ErrClusterInvalidArgument
		}
	}
	return updates, nil
}
