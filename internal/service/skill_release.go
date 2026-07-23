package service

import (
	"context"

	skillv1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

// SkillReleaseService exposes immutable, Git-tag-backed Skill releases through
// the generated HTTP and gRPC transports. Git remains the source of truth; the
// service only translates protobuf requests to the release use case.
type SkillReleaseService struct {
	skillv1.UnimplementedSkillReleaseServiceServer
	uc *biz.SkillUsecase
}

func NewSkillReleaseService(uc *biz.SkillUsecase) *SkillReleaseService {
	return &SkillReleaseService{uc: uc}
}

func (s *SkillReleaseService) RegisterHTTPServer(server *khttp.Server) {
	skillv1.RegisterSkillReleaseServiceHTTPServer(server, s)
}

func (s *SkillReleaseService) CreateSkillRelease(ctx context.Context, req *skillv1.CreateSkillReleaseRequest) (*skillv1.CreateSkillReleaseResponse, error) {
	release, err := s.uc.CreateRelease(ctx, principalFromContext(ctx), biz.CreateSkillRelease{
		SkillName:         req.GetName(),
		Version:           req.GetVersion(),
		SourceRef:         req.GetSourceRef(),
		ExpectedCommitSHA: req.GetExpectedCommitSha(),
		ReleaseNotes:      req.GetReleaseNotes(),
	})
	if err != nil {
		return nil, err
	}
	return &skillv1.CreateSkillReleaseResponse{Release: skillReleaseToProto(release)}, nil
}

func (s *SkillReleaseService) GetSkillRelease(ctx context.Context, req *skillv1.GetSkillReleaseRequest) (*skillv1.GetSkillReleaseResponse, error) {
	release, err := s.uc.GetRelease(ctx, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &skillv1.GetSkillReleaseResponse{Release: skillReleaseToProto(release)}, nil
}

func (s *SkillReleaseService) ResolveSkillRelease(ctx context.Context, req *skillv1.ResolveSkillReleaseRequest) (*skillv1.ResolveSkillReleaseResponse, error) {
	release, err := s.uc.GetRelease(ctx, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &skillv1.ResolveSkillReleaseResponse{Release: skillReleaseToProto(release)}, nil
}

func skillReleaseToProto(item *biz.SkillRelease) *skillv1.SkillRelease {
	if item == nil {
		return nil
	}
	return &skillv1.SkillRelease{
		Tag:            item.Tag,
		CommitSha:      item.CommitSHA,
		ManifestSha256: item.ManifestSHA256,
		CreateTime:     timestamp(item.CreateTime),
	}
}
