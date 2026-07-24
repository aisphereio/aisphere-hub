package service

import (
	"context"
	"strings"

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

// ReleaseService reuses the SkillService use case so transport assembly does
// not create a second release domain graph.
func (s *SkillService) ReleaseService() *SkillReleaseService {
	if s == nil {
		return nil
	}
	return NewSkillReleaseService(s.uc)
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

func (s *SkillReleaseService) ListSkillRefs(ctx context.Context, req *skillv1.ListSkillRefsRequest) (*skillv1.ListSkillRefsResponse, error) {
	items, err := s.uc.ListRefs(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	out := &skillv1.ListSkillRefsResponse{Refs: make([]*skillv1.SkillGitRef, 0, len(items))}
	for _, item := range items {
		out.Refs = append(out.Refs, &skillv1.SkillGitRef{
			Name: item.Name, FullRef: item.FullRef, Type: item.Type,
			CommitSha: item.CommitSHA, IsDefault: item.IsDefault,
		})
	}
	return out, nil
}

func (s *SkillReleaseService) ListSkillCommits(ctx context.Context, req *skillv1.ListSkillCommitsRequest) (*skillv1.ListSkillCommitsResponse, error) {
	items, err := s.uc.ListCommits(ctx, req.GetName(), req.GetRef(), int(req.GetPageSize()), int(req.GetOffset()))
	if err != nil {
		return nil, err
	}
	out := &skillv1.ListSkillCommitsResponse{Commits: make([]*skillv1.SkillCommit, 0, len(items))}
	for i := range items {
		out.Commits = append(out.Commits, skillCommitToProto(&items[i]))
	}
	return out, nil
}

func (s *SkillReleaseService) CompareSkillRefs(ctx context.Context, req *skillv1.CompareSkillRefsRequest) (*skillv1.CompareSkillRefsResponse, error) {
	item, err := s.uc.CompareRefs(ctx, req.GetName(), req.GetBaseRef(), req.GetTargetRef())
	if err != nil {
		return nil, err
	}
	comparison := &skillv1.SkillComparison{
		BaseRef: item.BaseRef, TargetRef: item.TargetRef,
		BaseCommitSha: item.BaseCommitSHA, TargetCommitSha: item.TargetCommitSHA,
		MergeBaseSha: item.MergeBaseSHA, Patch: item.Patch, PatchTruncated: item.PatchTruncated,
		Files: make([]*skillv1.SkillDiffFile, 0, len(item.Files)),
	}
	for _, file := range item.Files {
		comparison.Files = append(comparison.Files, &skillv1.SkillDiffFile{
			Path: file.Path, PreviousPath: file.PreviousPath, Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary,
		})
	}
	return &skillv1.CompareSkillRefsResponse{Comparison: comparison}, nil
}

func (s *SkillReleaseService) RestoreSkillRef(ctx context.Context, req *skillv1.RestoreSkillRefRequest) (*skillv1.RestoreSkillRefResponse, error) {
	item, err := s.uc.RestoreRef(ctx, principalFromContext(ctx), biz.RestoreSkillRef{
		SkillName: req.GetName(), SourceRef: req.GetSourceRef(),
		TargetBranch: req.GetTargetBranch(), ExpectedHeadSHA: req.GetExpectedHeadSha(),
		CommitMessage: req.GetCommitMessage(),
	})
	if err != nil {
		return nil, err
	}
	targetBranch := req.GetTargetBranch()
	if targetBranch == "" {
		targetBranch = biz.SkillDefaultBranch
	}
	targetBranch = strings.TrimPrefix(strings.TrimSpace(targetBranch), "refs/heads/")
	return &skillv1.RestoreSkillRefResponse{
		Commit: skillCommitToProto(item), TargetRef: "refs/heads/" + targetBranch,
	}, nil
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
		TreeSha:        item.TreeSHA,
		ReleaseNotes:   item.ReleaseNotes,
		SourceRef:      item.SourceRef,
		PublisherId:    item.PublisherID,
		PublisherName:  item.PublisherName,
		PublisherEmail: item.PublisherEmail,
	}
}

func skillCommitToProto(item *biz.SkillCommit) *skillv1.SkillCommit {
	if item == nil {
		return nil
	}
	return &skillv1.SkillCommit{
		CommitSha: item.CommitSHA, TreeSha: item.TreeSHA, ParentShas: item.ParentSHAs,
		AuthorName: item.AuthorName, AuthorEmail: item.AuthorEmail,
		Subject: item.Subject, CreateTime: timestamp(item.CreateTime),
	}
}
