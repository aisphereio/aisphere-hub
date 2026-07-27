package service

import (
	"context"
	"strings"

	modelv1 "github.com/aisphereio/aisphere-hub/api/model/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ModelProfileService implements the ModelProfile catalog HTTP/gRPC service.
type ModelProfileService struct {
	modelv1.UnimplementedModelProfileServiceServer
	uc *biz.ModelProfileUsecase
}

func NewModelProfileService(uc *biz.ModelProfileUsecase) *ModelProfileService {
	return &ModelProfileService{uc: uc}
}

func (s *ModelProfileService) RegisterHTTPServer(server *khttp.Server) {
	modelv1.RegisterModelProfileServiceHTTPServer(server, s)
}

func (s *ModelProfileService) ListModelProfiles(ctx context.Context, req *modelv1.ListModelProfilesRequest) (*modelv1.ListModelProfilesResponse, error) {
	offset, err := decodePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	profiles, next, err := s.uc.ListModelProfiles(ctx, principalFromContext(ctx), biz.ModelProfileListOptions{
		Limit:    int(req.GetPageSize()),
		Offset:   offset,
		Query:    req.GetQuery(),
		Status:   req.GetStatus(),
		Provider: req.GetProvider(),
	})
	if err != nil {
		return nil, err
	}
	out := &modelv1.ListModelProfilesResponse{ModelProfiles: make([]*modelv1.ModelProfile, 0, len(profiles))}
	for _, p := range profiles {
		out.ModelProfiles = append(out.ModelProfiles, modelProfileToProto(p))
	}
	out.NextPageToken = next
	return out, nil
}

func (s *ModelProfileService) GetModelProfile(ctx context.Context, req *modelv1.GetModelProfileRequest) (*modelv1.GetModelProfileResponse, error) {
	p, err := s.uc.GetModelProfile(ctx, principalFromContext(ctx), req.GetId(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &modelv1.GetModelProfileResponse{ModelProfile: modelProfileToProto(p)}, nil
}

func (s *ModelProfileService) CreateModelProfile(ctx context.Context, req *modelv1.CreateModelProfileRequest) (*modelv1.CreateModelProfileResponse, error) {
	p := createModelProfileRequestToBiz(req)
	out, err := s.uc.CreateModelProfile(ctx, principalFromContext(ctx), p)
	if err != nil {
		return nil, err
	}
	return &modelv1.CreateModelProfileResponse{ModelProfile: modelProfileToProto(out)}, nil
}

func (s *ModelProfileService) UpdateModelProfile(ctx context.Context, req *modelv1.UpdateModelProfileRequest) (*modelv1.UpdateModelProfileResponse, error) {
	p := updateModelProfileRequestToBiz(req)
	out, err := s.uc.UpdateModelProfile(ctx, principalFromContext(ctx), p)
	if err != nil {
		return nil, err
	}
	return &modelv1.UpdateModelProfileResponse{ModelProfile: modelProfileToProto(out)}, nil
}

func (s *ModelProfileService) DeleteModelProfile(ctx context.Context, req *modelv1.DeleteModelProfileRequest) (*modelv1.DeleteModelProfileResponse, error) {
	out, err := s.uc.DeleteModelProfile(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	return &modelv1.DeleteModelProfileResponse{ModelProfile: modelProfileToProto(out)}, nil
}

func (s *ModelProfileService) ResolveModelProfile(ctx context.Context, req *modelv1.ResolveModelProfileRequest) (*modelv1.ResolveModelProfileResponse, error) {
	snap, err := s.uc.ResolveModelProfile(ctx, principalFromContext(ctx), req.GetId(), req.GetVersion(), req.GetRuntimeId(), req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return snapshotToProto(snap), nil
}

func (s *ModelProfileService) TestModelProfile(ctx context.Context, req *modelv1.TestModelProfileRequest) (*modelv1.TestModelProfileResponse, error) {
	res, err := s.uc.TestModelProfile(ctx, principalFromContext(ctx), req.GetId(), req.GetPrompt())
	if err != nil {
		return nil, err
	}
	return &modelv1.TestModelProfileResponse{
		Ok:            res.OK,
		Error:         res.Error,
		LatencyMillis: res.LatencyMs,
		HttpStatus:    res.HTTPStatus,
	}, nil
}

// --- proto <-> biz conversion ---

func modelProfileToProto(p *biz.ModelProfile) *modelv1.ModelProfile {
	if p == nil {
		return nil
	}
	ownerSubject := strings.TrimSpace(p.OwnerType)
	if ownerSubject != "" && p.OwnerID != "" {
		ownerSubject = p.OwnerType + ":" + p.OwnerID
	}
	out := &modelv1.ModelProfile{
		Id: p.ID, Version: p.Version, Status: p.Status,
		DisplayName: p.DisplayName, Description: p.Description,
		Provider: p.Provider, ApiFormat: p.APIFormat, Endpoint: p.Endpoint,
		Model: p.Model, UpstreamModel: p.UpstreamModel, UpstreamPath: p.UpstreamPath,
		SecretRef: p.SecretRef, AllowedTools: p.AllowedTools,
		Limits:    limitsToProto(p.Limits),
		Reasoning: p.Reasoning, Labels: p.Labels, Metadata: p.Metadata,
		DefaultParameters: p.DefaultParameters,
		Object: p.Object, OwnerSubject: ownerSubject,
		CreateTime: timestamp(p.CreatedAt), UpdateTime: timestamp(p.UpdatedAt),
	}
	return out
}

func limitsToProto(l biz.ModelProfileLimits) *modelv1.ModelProfileLimits {
	return &modelv1.ModelProfileLimits{
		MaxInputTokens:  l.MaxInputTokens,
		MaxOutputTokens: l.MaxOutputTokens,
	}
}

func protoToLimits(l *modelv1.ModelProfileLimits) biz.ModelProfileLimits {
	if l == nil {
		return biz.ModelProfileLimits{}
	}
	return biz.ModelProfileLimits{MaxInputTokens: l.GetMaxInputTokens(), MaxOutputTokens: l.GetMaxOutputTokens()}
}

func createModelProfileRequestToBiz(req *modelv1.CreateModelProfileRequest) *biz.ModelProfile {
	return &biz.ModelProfile{
		ID:            req.GetId(),
		Version:       req.GetVersion(),
		Status:        req.GetStatus(),
		DisplayName:   req.GetDisplayName(),
		Description:   req.GetDescription(),
		Provider:      req.GetProvider(),
		APIFormat:     req.GetApiFormat(),
		Endpoint:      req.GetEndpoint(),
		Model:         req.GetModel(),
		UpstreamModel: req.GetUpstreamModel(),
		UpstreamPath:  req.GetUpstreamPath(),
		SecretRef:     req.GetSecretRef(),
		AllowedTools:  req.GetAllowedTools(),
		Limits:        protoToLimits(req.GetLimits()),
		Reasoning:     req.GetReasoning(),
		Labels:        req.GetLabels(),
		Metadata:      req.GetMetadata(),
		ProjectID:     req.GetProjectId(),
		DefaultParameters: req.GetDefaultParameters(),
	}
}

func updateModelProfileRequestToBiz(req *modelv1.UpdateModelProfileRequest) *biz.ModelProfile {
	return &biz.ModelProfile{
		ID:            req.GetId(),
		Version:       req.GetVersion(),
		Status:        req.GetStatus(),
		DisplayName:   req.GetDisplayName(),
		Description:   req.GetDescription(),
		Provider:      req.GetProvider(),
		APIFormat:     req.GetApiFormat(),
		Endpoint:      req.GetEndpoint(),
		Model:         req.GetModel(),
		UpstreamModel: req.GetUpstreamModel(),
		UpstreamPath:  req.GetUpstreamPath(),
		SecretRef:     req.GetSecretRef(),
		AllowedTools:  req.GetAllowedTools(),
		Limits:        protoToLimits(req.GetLimits()),
		Reasoning:     req.GetReasoning(),
		Labels:        req.GetLabels(),
		Metadata:      req.GetMetadata(),
		DefaultParameters: req.GetDefaultParameters(),
	}
}

func snapshotToProto(s *biz.ModelProfileSnapshot) *modelv1.ResolveModelProfileResponse {
	if s == nil {
		return nil
	}
	return &modelv1.ResolveModelProfileResponse{
		ProfileId:         s.ProfileID,
		Revision:          s.Revision,
		LogicalName:       s.LogicalName,
		Provider:          s.Provider,
		Protocol:          s.Protocol,
		BaseUrl:           s.BaseURL,
		UpstreamModel:     s.UpstreamModel,
		UpstreamPath:      s.UpstreamPath,
		CredentialRef:     s.CredentialRef,
		Capabilities:      capabilitiesToProto(s.Capabilities),
		Limits:            snapshotLimitsToProto(s.Limits),
		Sha256:            s.SHA256,
		DefaultParameters: s.DefaultParameters,
		GeneratedAt:       timestamppb.New(s.GeneratedAt),
	}
}

func capabilitiesToProto(c biz.ModelProfileCapabilities) *modelv1.ModelProfileCapabilities {
	return &modelv1.ModelProfileCapabilities{
		Tools: c.Tools, Streaming: c.Streaming, Reasoning: c.Reasoning, Multimodal: c.Multimodal,
	}
}

func snapshotLimitsToProto(l biz.ModelProfileSnapshotLimits) *modelv1.ModelProfileSnapshotLimits {
	return &modelv1.ModelProfileSnapshotLimits{
		ContextWindow:   l.ContextWindow,
		MaxOutputTokens: l.MaxOutputTokens,
	}
}

// keep authn import referenced (principalFromContext returns authn.Principal;
// the import is also used transitively but this guards future trimming).
var _ authn.Principal
