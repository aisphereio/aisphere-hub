package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	skillv1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type SkillService struct {
	skillv1.UnimplementedSkillServiceServer
	uc *biz.SkillUsecase
}

func NewSkillService(uc *biz.SkillUsecase) *SkillService { return &SkillService{uc: uc} }
func (s *SkillService) RegisterHTTPServer(server *khttp.Server) {
	skillv1.RegisterSkillServiceHTTPServer(server, s)
}

func (s *SkillService) CreateSkill(ctx context.Context, req *skillv1.CreateSkillRequest) (*skillv1.CreateSkillResponse, error) {
	out, err := s.uc.CreateSkill(ctx, principalFromContext(ctx), &biz.GitSkill{Name: req.GetName(), DisplayName: req.GetDisplayName(), Description: req.GetDescription(), Visibility: req.GetVisibility(), ProjectID: req.GetProjectId()})
	if err != nil {
		return nil, err
	}
	return &skillv1.CreateSkillResponse{Skill: skillToProto(out)}, nil
}

func (s *SkillService) ListSkills(ctx context.Context, req *skillv1.ListSkillsRequest) (*skillv1.ListSkillsResponse, error) {
	offset, err := decodePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	result, err := s.uc.ListSkills(ctx, biz.GitSkillListOptions{Limit: int(req.GetPageSize()), Offset: offset, Query: req.GetQuery(), Visibility: req.GetVisibility(), Status: biz.SkillStatusActive})
	if err != nil {
		return nil, err
	}
	out := &skillv1.ListSkillsResponse{Skills: make([]*skillv1.Skill, 0, len(result.Items))}
	for _, item := range result.Items {
		out.Skills = append(out.Skills, skillToProto(item))
	}
	if result.HasMore {
		out.NextPageToken = strconv.Itoa(result.NextOffset)
	}
	return out, nil
}

func (s *SkillService) GetSkill(ctx context.Context, req *skillv1.GetSkillRequest) (*skillv1.GetSkillResponse, error) {
	out, err := s.uc.GetSkill(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &skillv1.GetSkillResponse{Skill: skillToProto(out)}, nil
}

func (s *SkillService) UpdateSkill(ctx context.Context, req *skillv1.UpdateSkillRequest) (*skillv1.UpdateSkillResponse, error) {
	out, err := s.uc.UpdateSkill(ctx, &biz.GitSkill{Name: req.GetName(), DisplayName: req.GetDisplayName(), Description: req.GetDescription()})
	if err != nil {
		return nil, err
	}
	return &skillv1.UpdateSkillResponse{Skill: skillToProto(out)}, nil
}

func (s *SkillService) UpdateSkillVisibility(ctx context.Context, req *skillv1.UpdateSkillVisibilityRequest) (*skillv1.UpdateSkillVisibilityResponse, error) {
	out, err := s.uc.UpdateSkillVisibility(ctx, req.GetName(), req.GetVisibility())
	if err != nil {
		return nil, err
	}
	return &skillv1.UpdateSkillVisibilityResponse{Skill: skillToProto(out)}, nil
}

func (s *SkillService) DeleteSkill(ctx context.Context, req *skillv1.DeleteSkillRequest) (*skillv1.DeleteSkillResponse, error) {
	if err := s.uc.DeleteSkill(ctx, req.GetName()); err != nil {
		return nil, err
	}
	return &skillv1.DeleteSkillResponse{}, nil
}

func (s *SkillService) ListSkillShares(ctx context.Context, req *skillv1.ListSkillSharesRequest) (*skillv1.ListSkillSharesResponse, error) {
	items, err := s.uc.ListSkillShares(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	out := &skillv1.ListSkillSharesResponse{Shares: make([]*skillv1.SkillShare, 0, len(items))}
	for i := range items {
		out.Shares = append(out.Shares, shareToProto(&items[i]))
	}
	return out, nil
}

func (s *SkillService) CreateSkillShare(ctx context.Context, req *skillv1.CreateSkillShareRequest) (*skillv1.CreateSkillShareResponse, error) {
	out, err := s.uc.CreateSkillShare(ctx, biz.SkillShare{SkillName: req.GetName(), Relation: req.GetRelation(), SubjectType: req.GetSubjectType(), SubjectID: req.GetSubjectId(), SubjectRelation: req.GetSubjectRelation()})
	if err != nil {
		return nil, err
	}
	return &skillv1.CreateSkillShareResponse{Share: shareToProto(out)}, nil
}

func (s *SkillService) DeleteSkillShare(ctx context.Context, req *skillv1.DeleteSkillShareRequest) (*skillv1.DeleteSkillShareResponse, error) {
	if err := s.uc.DeleteSkillShare(ctx, biz.SkillShare{SkillName: req.GetName(), Relation: req.GetRelation(), SubjectType: req.GetSubjectType(), SubjectID: req.GetSubjectId()}); err != nil {
		return nil, err
	}
	return &skillv1.DeleteSkillShareResponse{}, nil
}

func (s *SkillService) CreatePullRequest(ctx context.Context, req *skillv1.CreatePullRequestRequest) (*skillv1.CreatePullRequestResponse, error) {
	out, err := s.uc.CreatePullRequest(ctx, principalFromContext(ctx), &biz.SkillPullRequest{SkillName: req.GetName(), SourceRef: req.GetSourceRef(), Title: req.GetTitle(), Description: req.GetDescription()})
	if err != nil {
		return nil, err
	}
	return &skillv1.CreatePullRequestResponse{PullRequest: pullRequestToProto(out)}, nil
}

func (s *SkillService) ListPullRequests(ctx context.Context, req *skillv1.ListPullRequestsRequest) (*skillv1.ListPullRequestsResponse, error) {
	offset, err := decodePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	result, err := s.uc.ListPullRequests(ctx, req.GetName(), biz.PullRequestListOptions{State: req.GetState(), Limit: int(req.GetPageSize()), Offset: offset})
	if err != nil {
		return nil, err
	}
	out := &skillv1.ListPullRequestsResponse{PullRequests: make([]*skillv1.PullRequest, 0, len(result.Items))}
	for _, item := range result.Items {
		out.PullRequests = append(out.PullRequests, pullRequestToProto(item))
	}
	if result.HasMore {
		out.NextPageToken = strconv.Itoa(result.NextOffset)
	}
	return out, nil
}

func (s *SkillService) GetPullRequest(ctx context.Context, req *skillv1.GetPullRequestRequest) (*skillv1.GetPullRequestResponse, error) {
	pr, reviews, err := s.uc.GetPullRequest(ctx, req.GetName(), req.GetId())
	if err != nil {
		return nil, err
	}
	out := &skillv1.GetPullRequestResponse{PullRequest: pullRequestToProto(pr), Reviews: make([]*skillv1.PullRequestReview, 0, len(reviews))}
	for _, review := range reviews {
		out.Reviews = append(out.Reviews, reviewToProto(review))
	}
	return out, nil
}

func (s *SkillService) ReviewPullRequest(ctx context.Context, req *skillv1.ReviewPullRequestRequest) (*skillv1.ReviewPullRequestResponse, error) {
	out, err := s.uc.ReviewPullRequest(ctx, principalFromContext(ctx), &biz.SkillPullRequestReview{PullRequestID: req.GetId(), Verdict: req.GetVerdict(), Comment: req.GetComment()})
	if err != nil {
		return nil, err
	}
	return &skillv1.ReviewPullRequestResponse{Review: reviewToProto(out)}, nil
}

func (s *SkillService) ClosePullRequest(ctx context.Context, req *skillv1.ClosePullRequestRequest) (*skillv1.ClosePullRequestResponse, error) {
	out, err := s.uc.ClosePullRequest(ctx, req.GetName(), req.GetId())
	if err != nil {
		return nil, err
	}
	return &skillv1.ClosePullRequestResponse{PullRequest: pullRequestToProto(out)}, nil
}

func (s *SkillService) MergePullRequest(ctx context.Context, req *skillv1.MergePullRequestRequest) (*skillv1.MergePullRequestResponse, error) {
	out, err := s.uc.MergePullRequest(ctx, principalFromContext(ctx), req.GetName(), req.GetId(), req.GetExpectedTargetSha())
	if err != nil {
		return nil, err
	}
	return &skillv1.MergePullRequestResponse{PullRequest: pullRequestToProto(out)}, nil
}

func (s *SkillService) ListSkillReleases(ctx context.Context, req *skillv1.ListSkillReleasesRequest) (*skillv1.ListSkillReleasesResponse, error) {
	items, err := s.uc.ListReleases(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	out := &skillv1.ListSkillReleasesResponse{Releases: make([]*skillv1.SkillRelease, 0, len(items))}
	for _, item := range items {
		out.Releases = append(out.Releases, &skillv1.SkillRelease{Tag: item.Tag, CommitSha: item.CommitSHA, ManifestSha256: item.ManifestSHA256, CreateTime: timestamp(item.CreateTime)})
	}
	return out, nil
}

func skillToProto(item *biz.GitSkill) *skillv1.Skill {
	if item == nil {
		return nil
	}
	return &skillv1.Skill{Name: item.Name, DisplayName: item.DisplayName, Description: item.Description, Visibility: item.Visibility, OwnerId: item.OwnerID, OrgId: item.OrgID, ProjectId: item.ProjectID, DefaultBranch: item.DefaultBranch, Status: item.Status, CreateTime: timestamp(item.CreateTime), UpdateTime: timestamp(item.UpdateTime)}
}
func shareToProto(item *biz.SkillShare) *skillv1.SkillShare {
	if item == nil {
		return nil
	}
	return &skillv1.SkillShare{SkillName: item.SkillName, Relation: item.Relation, SubjectType: item.SubjectType, SubjectId: item.SubjectID, SubjectRelation: item.SubjectRelation}
}
func pullRequestToProto(item *biz.SkillPullRequest) *skillv1.PullRequest {
	if item == nil {
		return nil
	}
	return &skillv1.PullRequest{Id: item.ID, SkillName: item.SkillName, SourceRef: item.SourceRef, TargetRef: item.TargetRef, SourceSha: item.SourceSHA, TargetSha: item.TargetSHA, Title: item.Title, Description: item.Description, State: item.State, AuthorId: item.AuthorID, MergedSha: item.MergedSHA, CreateTime: timestamp(item.CreateTime), UpdateTime: timestamp(item.UpdateTime), MergedTime: timestamp(item.MergedTime)}
}
func reviewToProto(item *biz.SkillPullRequestReview) *skillv1.PullRequestReview {
	if item == nil {
		return nil
	}
	return &skillv1.PullRequestReview{Id: item.ID, PullRequestId: item.PullRequestID, ReviewerId: item.ReviewerID, Verdict: item.Verdict, Comment: item.Comment, CreateTime: timestamp(item.CreateTime)}
}
func timestamp(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

func principalFromContext(ctx context.Context) authn.Principal {
	principal, _ := authn.PrincipalFromContext(ctx)
	return principal
}
func decodePageToken(token string) (int, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(token)
	if err != nil || offset < 0 {
		return 0, errorx.BadRequest(errorx.Code("INVALID_PAGE_TOKEN"), "invalid page token")
	}
	return offset, nil
}
