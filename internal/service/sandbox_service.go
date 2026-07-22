package service

import (
	"context"
	"encoding/json"

	kubernetesv1 "github.com/aisphereio/aisphere-hub/api/kubernetes/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SandboxService adapts the kubernetes.v1.SandboxService proto RPCs to
// biz.SandboxUsecase (design §11). Proto↔biz translation + error passthrough.
type SandboxService struct {
	kubernetesv1.UnimplementedSandboxServiceServer
	uc *biz.SandboxUsecase
}

func NewSandboxService(uc *biz.SandboxUsecase) *SandboxService {
	return &SandboxService{uc: uc}
}

func (s *SandboxService) RegisterHTTPServer(server *khttp.Server) {
	kubernetesv1.RegisterSandboxServiceHTTPServer(server, s)
}

// ---- SandboxTemplate CRUD ----

func (s *SandboxService) CreateSandboxTemplate(ctx context.Context, req *kubernetesv1.CreateSandboxTemplateRequest) (*kubernetesv1.CreateSandboxTemplateResponse, error) {
	principal := principalFromContext(ctx)
	t := &biz.SandboxTemplate{
		ID:              uuid.NewString(),
		ClusterID:       req.GetClusterId(),
		OrgID:           principal.OrgID,
		Name:            req.GetName(),
		DisplayName:     req.GetDisplayName(),
		Description:     req.GetDescription(),
		Image:           req.GetImage(),
		ContainerCommand: req.GetContainerCommand(),
		Labels:          req.GetLabels(),
		KubernetesName:  req.GetName(),
	}
	if req.GetOwnerType() != "" && req.GetOwnerId() != "" {
		principal = authn.Principal{SubjectType: req.GetOwnerType(), SubjectID: req.GetOwnerId(), OrgID: principal.OrgID}
	}
	created, err := s.uc.CreateSandboxTemplate(ctx, principal, t)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.CreateSandboxTemplateResponse{Template: sandboxTemplateToProto(created)}, nil
}

func (s *SandboxService) ListSandboxTemplates(ctx context.Context, req *kubernetesv1.ListSandboxTemplatesRequest) (*kubernetesv1.ListSandboxTemplatesResponse, error) {
	templates, err := s.uc.ListSandboxTemplates(ctx, principalFromContext(ctx), req.GetClusterId())
	if err != nil {
		return nil, err
	}
	out := &kubernetesv1.ListSandboxTemplatesResponse{Templates: make([]*kubernetesv1.SandboxTemplate, 0, len(templates))}
	for _, t := range templates {
		out.Templates = append(out.Templates, sandboxTemplateToProto(t))
	}
	return out, nil
}

func (s *SandboxService) GetSandboxTemplate(ctx context.Context, req *kubernetesv1.GetSandboxTemplateRequest) (*kubernetesv1.GetSandboxTemplateResponse, error) {
	t, err := s.uc.GetSandboxTemplate(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.GetSandboxTemplateResponse{Template: sandboxTemplateToProto(t)}, nil
}

func (s *SandboxService) DeleteSandboxTemplate(ctx context.Context, req *kubernetesv1.DeleteSandboxTemplateRequest) (*kubernetesv1.DeleteSandboxTemplateResponse, error) {
	deleted, err := s.uc.DeleteSandboxTemplate(ctx, principalFromContext(ctx), req.GetId(), req.GetExpectedRevision())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.DeleteSandboxTemplateResponse{Template: sandboxTemplateToProto(deleted)}, nil
}

// ---- Sandbox CRUD + sync ----

func (s *SandboxService) CreateSandbox(ctx context.Context, req *kubernetesv1.CreateSandboxRequest) (*kubernetesv1.CreateSandboxResponse, error) {
	principal := principalFromContext(ctx)
	sb := &biz.Sandbox{
		ID:            uuid.NewString(),
		NamespaceID:   req.GetNamespaceId(),
		Name:          req.GetName(),
		KubernetesName: req.GetName(),
		TemplateID:    req.GetTemplateId(),
		WarmPoolID:    req.GetWarmPoolId(),
		OperatingMode: sandboxOperatingModeFromProto(req.GetOperatingMode()),
		NetworkMode:   biz.SandboxNetworkModeOffline,
		Labels:        req.GetLabels(),
	}
	if req.GetOwnerType() != "" && req.GetOwnerId() != "" {
		principal = authn.Principal{SubjectType: req.GetOwnerType(), SubjectID: req.GetOwnerId(), OrgID: principal.OrgID}
	}
	created, err := s.uc.CreateSandbox(ctx, principal, sb)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.CreateSandboxResponse{Sandbox: sandboxToProto(created)}, nil
}

func (s *SandboxService) ListSandboxes(ctx context.Context, req *kubernetesv1.ListSandboxesRequest) (*kubernetesv1.ListSandboxesResponse, error) {
	sandboxes, err := s.uc.ListSandboxes(ctx, principalFromContext(ctx), req.GetNamespaceId())
	if err != nil {
		return nil, err
	}
	out := &kubernetesv1.ListSandboxesResponse{Sandboxes: make([]*kubernetesv1.Sandbox, 0, len(sandboxes))}
	for _, sb := range sandboxes {
		out.Sandboxes = append(out.Sandboxes, sandboxToProto(sb))
	}
	return out, nil
}

func (s *SandboxService) GetSandbox(ctx context.Context, req *kubernetesv1.GetSandboxRequest) (*kubernetesv1.GetSandboxResponse, error) {
	sb, err := s.uc.GetSandbox(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.GetSandboxResponse{Sandbox: sandboxToProto(sb)}, nil
}

func (s *SandboxService) DeleteSandbox(ctx context.Context, req *kubernetesv1.DeleteSandboxRequest) (*kubernetesv1.DeleteSandboxResponse, error) {
	deleted, err := s.uc.DeleteSandbox(ctx, principalFromContext(ctx), req.GetId(), req.GetExpectedRevision())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.DeleteSandboxResponse{Sandbox: sandboxToProto(deleted)}, nil
}

func (s *SandboxService) SyncSandboxes(ctx context.Context, req *kubernetesv1.SyncSandboxesRequest) (*kubernetesv1.SyncSandboxesResponse, error) {
	imported, updated, removed, err := s.uc.SyncSandboxes(ctx, principalFromContext(ctx), req.GetNamespaceId())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.SyncSandboxesResponse{Imported: int32(imported), Updated: int32(updated), Removed: int32(removed)}, nil
}

// ---- WarmPool CRUD ----

func (s *SandboxService) CreateWarmPool(ctx context.Context, req *kubernetesv1.CreateWarmPoolRequest) (*kubernetesv1.CreateWarmPoolResponse, error) {
	principal := principalFromContext(ctx)
	w := &biz.WarmPool{
		ID:            uuid.NewString(),
		NamespaceID:   req.GetNamespaceId(),
		Name:          req.GetName(),
		KubernetesName: req.GetName(),
		TemplateID:    req.GetTemplateId(),
		Replicas:      req.GetReplicas(),
	}
	if req.GetOwnerType() != "" && req.GetOwnerId() != "" {
		principal = authn.Principal{SubjectType: req.GetOwnerType(), SubjectID: req.GetOwnerId(), OrgID: principal.OrgID}
	}
	created, err := s.uc.CreateWarmPool(ctx, principal, w)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.CreateWarmPoolResponse{WarmPool: warmPoolToProto(created)}, nil
}

func (s *SandboxService) ListWarmPools(ctx context.Context, req *kubernetesv1.ListWarmPoolsRequest) (*kubernetesv1.ListWarmPoolsResponse, error) {
	pools, err := s.uc.ListWarmPools(ctx, principalFromContext(ctx), req.GetNamespaceId())
	if err != nil {
		return nil, err
	}
	out := &kubernetesv1.ListWarmPoolsResponse{WarmPools: make([]*kubernetesv1.WarmPool, 0, len(pools))}
	for _, w := range pools {
		out.WarmPools = append(out.WarmPools, warmPoolToProto(w))
	}
	return out, nil
}

func (s *SandboxService) DeleteWarmPool(ctx context.Context, req *kubernetesv1.DeleteWarmPoolRequest) (*kubernetesv1.DeleteWarmPoolResponse, error) {
	deleted, err := s.uc.DeleteWarmPool(ctx, principalFromContext(ctx), req.GetId(), req.GetExpectedRevision())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.DeleteWarmPoolResponse{WarmPool: warmPoolToProto(deleted)}, nil
}

// ---- SandboxClaim CRUD ----

func (s *SandboxService) CreateSandboxClaim(ctx context.Context, req *kubernetesv1.CreateSandboxClaimRequest) (*kubernetesv1.CreateSandboxClaimResponse, error) {
	principal := principalFromContext(ctx)
	c := &biz.SandboxClaim{
		ID:            uuid.NewString(),
		NamespaceID:   req.GetNamespaceId(),
		Name:          req.GetName(),
		KubernetesName: req.GetName(),
		WarmPoolID:    req.GetWarmPoolId(),
	}
	if req.GetOwnerType() != "" && req.GetOwnerId() != "" {
		principal = authn.Principal{SubjectType: req.GetOwnerType(), SubjectID: req.GetOwnerId(), OrgID: principal.OrgID}
	}
	created, err := s.uc.CreateSandboxClaim(ctx, principal, c)
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.CreateSandboxClaimResponse{Claim: sandboxClaimToProto(created)}, nil
}

func (s *SandboxService) ListSandboxClaims(ctx context.Context, req *kubernetesv1.ListSandboxClaimsRequest) (*kubernetesv1.ListSandboxClaimsResponse, error) {
	claims, err := s.uc.ListSandboxClaims(ctx, principalFromContext(ctx), req.GetNamespaceId())
	if err != nil {
		return nil, err
	}
	out := &kubernetesv1.ListSandboxClaimsResponse{Claims: make([]*kubernetesv1.SandboxClaim, 0, len(claims))}
	for _, c := range claims {
		out.Claims = append(out.Claims, sandboxClaimToProto(c))
	}
	return out, nil
}

func (s *SandboxService) DeleteSandboxClaim(ctx context.Context, req *kubernetesv1.DeleteSandboxClaimRequest) (*kubernetesv1.DeleteSandboxClaimResponse, error) {
	deleted, err := s.uc.DeleteSandboxClaim(ctx, principalFromContext(ctx), req.GetId(), req.GetExpectedRevision())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.DeleteSandboxClaimResponse{Claim: sandboxClaimToProto(deleted)}, nil
}

// ---- Sandbox tool invocation ----

func (s *SandboxService) ListSandboxTools(ctx context.Context, req *kubernetesv1.ListSandboxToolsRequest) (*kubernetesv1.ListSandboxToolsResponse, error) {
	tools, err := s.uc.ListSandboxTools(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	out := &kubernetesv1.ListSandboxToolsResponse{Tools: make([]*kubernetesv1.SandboxToolSchema, 0, len(tools))}
	for _, t := range tools {
		out.Tools = append(out.Tools, &kubernetesv1.SandboxToolSchema{
			Name:            t.Name,
			Description:     t.Description,
			InputSchemaJson: t.InputSchema,
		})
	}
	return out, nil
}

func (s *SandboxService) CallSandboxTool(ctx context.Context, req *kubernetesv1.CallSandboxToolRequest) (*kubernetesv1.CallSandboxToolResponse, error) {
	ok, outputJSON, errMsg, err := s.uc.CallSandboxTool(ctx, principalFromContext(ctx), req.GetId(), req.GetTool(), req.GetInputJson(), req.GetTraceId())
	if err != nil {
		return nil, err
	}
	return &kubernetesv1.CallSandboxToolResponse{
		Ok:        ok,
		OutputJson: outputJSON,
		Error:     errMsg,
		TraceId:   req.GetTraceId(),
	}, nil
}

// ---- proto ↔ biz translation ----

func sandboxToProto(sb *biz.Sandbox) *kubernetesv1.Sandbox {
	if sb == nil {
		return nil
	}
	p := &kubernetesv1.Sandbox{
		Id:             sb.ID,
		NamespaceId:    sb.NamespaceID,
		ClusterId:      sb.ClusterID,
		Name:           sb.Name,
		KubernetesName: sb.KubernetesName,
		TemplateId:     sb.TemplateID,
		WarmPoolId:     sb.WarmPoolID,
		ClaimId:        sb.ClaimID,
		Lifecycle:      sandboxLifecycleToProto(sb.Lifecycle),
		PodName:        sb.PodName,
		PodIp:          sb.PodIP,
		NodeName:       sb.NodeName,
		Image:          sb.Image,
		WorkspacePvc:   sb.WorkspacePVC,
		NetworkMode:    sandboxNetworkModeToProto(sb.NetworkMode),
		OperatingMode:  sandboxOperatingModeToProto(sb.OperatingMode),
		OwnerId:        sb.OwnerID,
		OwnerType:      sb.OwnerType,
		Revision:       sb.Revision,
		CreateTime:     timestamppb.New(sb.CreatedAt),
		UpdateTime:     timestamppb.New(sb.UpdatedAt),
		CreatedByType:  sb.CreatedByType,
		CreatedBy:      sb.CreatedBy,
		HealthMessage:  sb.HealthMessage,
	}
	if !sb.LastSyncAt.IsZero() {
		p.LastSyncTime = timestamppb.New(sb.LastSyncAt)
	}
	return p
}

func sandboxTemplateToProto(t *biz.SandboxTemplate) *kubernetesv1.SandboxTemplate {
	if t == nil {
		return nil
	}
	p := &kubernetesv1.SandboxTemplate{
		Id:               t.ID,
		ClusterId:        t.ClusterID,
		OrgId:            t.OrgID,
		Name:             t.Name,
		DisplayName:      t.DisplayName,
		Description:      t.Description,
		KubernetesName:   t.KubernetesName,
		Image:            t.Image,
		ContainerCommand: t.ContainerCommand,
		Labels:           t.Labels,
		Status:           sandboxTemplateStatusToProto(t.Status),
		OwnerId:          t.OwnerID,
		OwnerType:        t.OwnerType,
		Revision:         t.Revision,
		CreateTime:       timestamppb.New(t.CreatedAt),
		UpdateTime:       timestamppb.New(t.UpdatedAt),
		CreatedByType:    t.CreatedByType,
		CreatedBy:        t.CreatedBy,
		HealthMessage:    t.HealthMessage,
	}
	return p
}

func warmPoolToProto(w *biz.WarmPool) *kubernetesv1.WarmPool {
	if w == nil {
		return nil
	}
	p := &kubernetesv1.WarmPool{
		Id:             w.ID,
		NamespaceId:    w.NamespaceID,
		ClusterId:      w.ClusterID,
		Name:           w.Name,
		KubernetesName: w.KubernetesName,
		TemplateId:     w.TemplateID,
		Replicas:       w.Replicas,
		ReadyReplicas:  w.ReadyReplicas,
		Status:         warmPoolStatusToProto(w.Status),
		OwnerId:        w.OwnerID,
		OwnerType:      w.OwnerType,
		Revision:       w.Revision,
		CreateTime:     timestamppb.New(w.CreatedAt),
		UpdateTime:     timestamppb.New(w.UpdatedAt),
		CreatedByType:  w.CreatedByType,
		CreatedBy:      w.CreatedBy,
	}
	return p
}

func sandboxClaimToProto(c *biz.SandboxClaim) *kubernetesv1.SandboxClaim {
	if c == nil {
		return nil
	}
	p := &kubernetesv1.SandboxClaim{
		Id:              c.ID,
		NamespaceId:     c.NamespaceID,
		ClusterId:       c.ClusterID,
		Name:            c.Name,
		KubernetesName:  c.KubernetesName,
		WarmPoolId:      c.WarmPoolID,
		SandboxId:       c.SandboxID,
		Status:          sandboxClaimStatusToProto(c.Status),
		SandboxKubeName: c.SandboxKubeName,
		SandboxPodIp:    c.SandboxPodIP,
		OwnerId:         c.OwnerID,
		OwnerType:       c.OwnerType,
		Revision:        c.Revision,
		CreateTime:      timestamppb.New(c.CreatedAt),
		UpdateTime:      timestamppb.New(c.UpdatedAt),
		CreatedByType:   c.CreatedByType,
		CreatedBy:       c.CreatedBy,
	}
	return p
}

// ---- enum conversions ----

func sandboxLifecycleToProto(v string) kubernetesv1.SandboxLifecycle {
	switch v {
	case biz.SandboxLifecycleCreating:
		return kubernetesv1.SandboxLifecycle_SANDBOX_LIFECYCLE_CREATING
	case biz.SandboxLifecycleReady:
		return kubernetesv1.SandboxLifecycle_SANDBOX_LIFECYCLE_READY
	case biz.SandboxLifecycleSuspended:
		return kubernetesv1.SandboxLifecycle_SANDBOX_LIFECYCLE_SUSPENDED
	case biz.SandboxLifecycleTerminating:
		return kubernetesv1.SandboxLifecycle_SANDBOX_LIFECYCLE_TERMINATING
	case biz.SandboxLifecycleFailed:
		return kubernetesv1.SandboxLifecycle_SANDBOX_LIFECYCLE_FAILED
	case biz.SandboxLifecycleDeleted:
		return kubernetesv1.SandboxLifecycle_SANDBOX_LIFECYCLE_DELETED
	}
	return kubernetesv1.SandboxLifecycle_SANDBOX_LIFECYCLE_UNSPECIFIED
}

func sandboxNetworkModeToProto(v string) kubernetesv1.SandboxNetworkMode {
	switch v {
	case biz.SandboxNetworkModeOffline:
		return kubernetesv1.SandboxNetworkMode_SANDBOX_NETWORK_MODE_OFFLINE
	case biz.SandboxNetworkModeOnline:
		return kubernetesv1.SandboxNetworkMode_SANDBOX_NETWORK_MODE_ONLINE
	}
	return kubernetesv1.SandboxNetworkMode_SANDBOX_NETWORK_MODE_UNSPECIFIED
}

func sandboxOperatingModeToProto(v string) kubernetesv1.SandboxOperatingMode {
	switch v {
	case biz.SandboxOperatingModeRunning:
		return kubernetesv1.SandboxOperatingMode_SANDBOX_OPERATING_MODE_RUNNING
	case biz.SandboxOperatingModeSuspended:
		return kubernetesv1.SandboxOperatingMode_SANDBOX_OPERATING_MODE_SUSPENDED
	}
	return kubernetesv1.SandboxOperatingMode_SANDBOX_OPERATING_MODE_UNSPECIFIED
}

func sandboxOperatingModeFromProto(v kubernetesv1.SandboxOperatingMode) string {
	switch v {
	case kubernetesv1.SandboxOperatingMode_SANDBOX_OPERATING_MODE_RUNNING:
		return biz.SandboxOperatingModeRunning
	case kubernetesv1.SandboxOperatingMode_SANDBOX_OPERATING_MODE_SUSPENDED:
		return biz.SandboxOperatingModeSuspended
	}
	return biz.SandboxOperatingModeRunning
}

func sandboxTemplateStatusToProto(v string) kubernetesv1.SandboxTemplateStatus {
	switch v {
	case biz.SandboxTemplateStatusCreating:
		return kubernetesv1.SandboxTemplateStatus_SANDBOX_TEMPLATE_STATUS_CREATING
	case biz.SandboxTemplateStatusReady:
		return kubernetesv1.SandboxTemplateStatus_SANDBOX_TEMPLATE_STATUS_READY
	case biz.SandboxTemplateStatusFailed:
		return kubernetesv1.SandboxTemplateStatus_SANDBOX_TEMPLATE_STATUS_FAILED
	case biz.SandboxTemplateStatusDeleted:
		return kubernetesv1.SandboxTemplateStatus_SANDBOX_TEMPLATE_STATUS_DELETED
	}
	return kubernetesv1.SandboxTemplateStatus_SANDBOX_TEMPLATE_STATUS_UNSPECIFIED
}

func warmPoolStatusToProto(v string) kubernetesv1.WarmPoolStatus {
	switch v {
	case biz.WarmPoolStatusCreating:
		return kubernetesv1.WarmPoolStatus_WARM_POOL_STATUS_CREATING
	case biz.WarmPoolStatusReady:
		return kubernetesv1.WarmPoolStatus_WARM_POOL_STATUS_READY
	case biz.WarmPoolStatusDegraded:
		return kubernetesv1.WarmPoolStatus_WARM_POOL_STATUS_DEGRADED
	case biz.WarmPoolStatusDeleted:
		return kubernetesv1.WarmPoolStatus_WARM_POOL_STATUS_DELETED
	}
	return kubernetesv1.WarmPoolStatus_WARM_POOL_STATUS_UNSPECIFIED
}

func sandboxClaimStatusToProto(v string) kubernetesv1.SandboxClaimStatus {
	switch v {
	case biz.SandboxClaimStatusPending:
		return kubernetesv1.SandboxClaimStatus_SANDBOX_CLAIM_STATUS_PENDING
	case biz.SandboxClaimStatusReady:
		return kubernetesv1.SandboxClaimStatus_SANDBOX_CLAIM_STATUS_READY
	case biz.SandboxClaimStatusFailed:
		return kubernetesv1.SandboxClaimStatus_SANDBOX_CLAIM_STATUS_FAILED
	case biz.SandboxClaimStatusDeleted:
		return kubernetesv1.SandboxClaimStatus_SANDBOX_CLAIM_STATUS_DELETED
	}
	return kubernetesv1.SandboxClaimStatus_SANDBOX_CLAIM_STATUS_UNSPECIFIED
}

// ensure json import is used (for potential future tool input marshaling).
var _ = json.Marshal
