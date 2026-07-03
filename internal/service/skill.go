// Package service skill module — HTTP/gRPC handlers for SkillService.
//
// The service layer is intentionally thin: it converts proto DTOs to biz
// domain objects, calls the corresponding usecase method, and converts
// the result back. All business logic (validation, state machine, authz
// fallbacks) lives in biz.
//
// Authz: all RPCs require a Bearer token (kernel authn middleware
// enforces this before the handler runs). The principal is extracted
// from the request context via authn.PrincipalFromContext. For RPCs
// that need ownership (UploadSkillPackage), the principal is passed to
// the biz layer explicitly.
//
// Logging: the service layer does NOT log here — biz and data layers
// already log with request-scoped fields via logx.FromContext(ctx).
// Service-layer logs would just duplicate.

package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	v1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"

	"github.com/aisphereio/kernel/authn"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const defaultSkillPageSize = 20

// SkillService implements v1.SkillServiceHTTPServer and v1.SkillServiceServer.
//
// We embed v1.UnimplementedSkillServiceServer to satisfy the
// mustEmbedUnimplementedSkillServiceServer() constraint required by the
// generated gRPC interface. The HTTP interface does not require this
// embed.
type SkillService struct {
	v1.UnimplementedSkillServiceServer

	uc *biz.SkillUsecase
}

// NewSkillService creates a new SkillService.
func NewSkillService(uc *biz.SkillUsecase) *SkillService {
	return &SkillService{uc: uc}
}

// RegisterHTTPServer registers the proto-generated HTTP routes.
func (s *SkillService) RegisterHTTPServer(srv *khttp.Server) {
	v1.RegisterSkillServiceHTTPServer(srv, s)
}

// --- Skill CRUD ---

func (s *SkillService) CreateSkill(ctx context.Context, req *v1.CreateSkillRequest) (*v1.CreateSkillResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.CreateSkill(ctx, principal, &biz.Skill{
		Name:         req.GetName(),
		DisplayName:  req.GetDisplayName(),
		Description:  req.GetDescription(),
		Version:      req.GetVersion(),
		Status:       req.GetStatus(),
		Visibility:   req.GetVisibility(),
		OwnerID:      req.GetOwnerId(),
		OrgID:        req.GetOrgId(),
		ProjectID:    req.GetProjectId(),
		SourceType:   req.GetSourceType(),
		SourceURI:    req.GetSourceUri(),
		ManifestJSON: req.GetManifestJson(),
		Tags:         append([]string(nil), req.GetTags()...),
	})
	if err != nil {
		return nil, err
	}
	return &v1.CreateSkillResponse{Skill: skillDOToDTO(out)}, nil
}

func (s *SkillService) UpdateSkill(ctx context.Context, req *v1.UpdateSkillRequest) (*v1.UpdateSkillResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.UpdateSkill(ctx, principal, &biz.Skill{
		Name:         req.GetName(),
		DisplayName:  req.GetDisplayName(),
		Description:  req.GetDescription(),
		Version:      req.GetVersion(),
		SourceType:   req.GetSourceType(),
		SourceURI:    req.GetSourceUri(),
		ManifestJSON: req.GetManifestJson(),
		Tags:         append([]string(nil), req.GetTags()...),
	})
	if err != nil {
		return nil, err
	}
	return &v1.UpdateSkillResponse{Skill: skillDOToDTO(out)}, nil
}

func (s *SkillService) UpdateSkillVisibility(ctx context.Context, req *v1.UpdateSkillVisibilityRequest) (*v1.UpdateSkillVisibilityResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.UpdateSkillVisibility(ctx, principal, req.GetName(), req.GetVisibility())
	if err != nil {
		return nil, err
	}
	return &v1.UpdateSkillVisibilityResponse{Skill: skillDOToDTO(out)}, nil
}

func (s *SkillService) ListSkills(ctx context.Context, req *v1.ListSkillsRequest) (*v1.ListSkillsResponse, error) {
	principal := principalFromContext(ctx)
	limit := int(req.GetPageSize())
	if limit <= 0 {
		limit = defaultSkillPageSize
	}
	offset, err := parseSkillPageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	out, err := s.uc.ListSkills(ctx, principal, biz.SkillListOptions{
		Limit:      limit,
		Offset:     offset,
		Query:      req.GetQ(),
		Status:     req.GetStatus(),
		Visibility: req.GetVisibility(),
	})
	if err != nil {
		return nil, err
	}
	return skillListResultToDTO(out), nil
}

func (s *SkillService) GetSkill(ctx context.Context, req *v1.GetSkillRequest) (*v1.GetSkillResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.GetSkill(ctx, principal, req.GetName())
	if err != nil {
		return nil, err
	}
	return &v1.GetSkillResponse{Skill: skillDOToDTO(out)}, nil
}

func (s *SkillService) DeleteSkill(ctx context.Context, req *v1.DeleteSkillRequest) (*v1.DeleteSkillResponse, error) {
	principal := principalFromContext(ctx)
	if err := s.uc.DeleteSkill(ctx, principal, req.GetName()); err != nil {
		return nil, err
	}
	return &v1.DeleteSkillResponse{}, nil
}

// --- SkillVersion ---

func (s *SkillService) UploadSkillPackage(ctx context.Context, req *v1.UploadSkillPackageRequest) (*v1.UploadSkillPackageResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.UploadSkillPackage(ctx, principal, biz.SkillPackageUpload{
		PackageBytes:  req.GetPackageBytes(),
		Overwrite:     req.GetOverwrite(),
		TargetVersion: req.GetTargetVersion(),
		CommitMsg:     req.GetCommitMsg(),
	})
	if err != nil {
		return nil, err
	}
	return &v1.UploadSkillPackageResponse{Version: skillVersionDOToDTO(out)}, nil
}

func (s *SkillService) ListSkillVersions(ctx context.Context, req *v1.ListSkillVersionsRequest) (*v1.ListSkillVersionsResponse, error) {
	principal := principalFromContext(ctx)
	versions, err := s.uc.ListSkillVersions(ctx, principal, req.GetName())
	if err != nil {
		return nil, err
	}
	out := &v1.ListSkillVersionsResponse{
		Versions: make([]*v1.SkillVersion, 0, len(versions)),
	}
	for _, v := range versions {
		out.Versions = append(out.Versions, skillVersionDOToDTO(v))
	}
	return out, nil
}

func (s *SkillService) GetSkillVersion(ctx context.Context, req *v1.GetSkillVersionRequest) (*v1.GetSkillVersionResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.GetSkillVersion(ctx, principal, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &v1.GetSkillVersionResponse{Version: skillVersionDOToDTO(out)}, nil
}

func (s *SkillService) SubmitSkillVersion(ctx context.Context, req *v1.SubmitSkillVersionRequest) (*v1.SubmitSkillVersionResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.SubmitSkillVersion(ctx, principal, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &v1.SubmitSkillVersionResponse{Version: skillVersionDOToDTO(out)}, nil
}

func (s *SkillService) PublishSkillVersion(ctx context.Context, req *v1.PublishSkillVersionRequest) (*v1.PublishSkillVersionResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.PublishSkillVersion(ctx, principal, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &v1.PublishSkillVersionResponse{Version: skillVersionDOToDTO(out)}, nil
}

func (s *SkillService) OnlineSkillVersion(ctx context.Context, req *v1.OnlineSkillVersionRequest) (*v1.OnlineSkillVersionResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.OnlineSkillVersion(ctx, principal, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &v1.OnlineSkillVersionResponse{Version: skillVersionDOToDTO(out)}, nil
}

func (s *SkillService) OfflineSkillVersion(ctx context.Context, req *v1.OfflineSkillVersionRequest) (*v1.OfflineSkillVersionResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.OfflineSkillVersion(ctx, principal, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &v1.OfflineSkillVersionResponse{Version: skillVersionDOToDTO(out)}, nil
}

func (s *SkillService) DownloadSkillVersion(ctx context.Context, req *v1.DownloadSkillVersionRequest) (*v1.SkillPackageDownload, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.DownloadSkillPackage(ctx, principal, req.GetName(), req.GetVersion(), req.GetIfNoneMatch())
	if err != nil {
		return nil, err
	}
	return skillPackageDownloadDOToDTO(out), nil
}

// --- Skill draft workspace ---

func (s *SkillService) ListSkillDraftFiles(ctx context.Context, req *v1.ListSkillDraftFilesRequest) (*v1.ListSkillDraftFilesResponse, error) {
	principal := principalFromContext(ctx)
	files, err := s.uc.ListSkillDraftFiles(ctx, principal, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	out := &v1.ListSkillDraftFilesResponse{Files: make([]*v1.SkillFile, 0, len(files))}
	for _, f := range files {
		out.Files = append(out.Files, skillFileDOToDTO(f, false))
	}
	return out, nil
}

func (s *SkillService) GetSkillDraftFile(ctx context.Context, req *v1.GetSkillDraftFileRequest) (*v1.GetSkillDraftFileResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.GetSkillDraftFile(ctx, principal, req.GetName(), req.GetVersion(), req.GetPath())
	if err != nil {
		return nil, err
	}
	return &v1.GetSkillDraftFileResponse{File: skillFileDOToDTO(out, true)}, nil
}

func (s *SkillService) UpsertSkillDraftFile(ctx context.Context, req *v1.UpsertSkillDraftFileRequest) (*v1.UpsertSkillDraftFileResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.UpsertSkillDraftFile(ctx, principal, biz.SkillDraftFileInput{
		SkillName:     req.GetName(),
		Version:       req.GetVersion(),
		Path:          req.GetPath(),
		Type:          req.GetType(),
		Content:       req.GetContent(),
		Binary:        req.GetBinary(),
		CreateParents: req.GetCreateParents(),
	})
	if err != nil {
		return nil, err
	}
	return &v1.UpsertSkillDraftFileResponse{File: skillFileDOToDTO(out, true)}, nil
}

func (s *SkillService) UpsertSkillDraftDirectory(ctx context.Context, req *v1.UpsertSkillDraftDirectoryRequest) (*v1.UpsertSkillDraftDirectoryResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.UpsertSkillDraftFile(ctx, principal, biz.SkillDraftFileInput{
		SkillName:     req.GetName(),
		Version:       req.GetVersion(),
		Path:          req.GetPath(),
		Type:          "directory",
		CreateParents: true,
	})
	if err != nil {
		return nil, err
	}
	return &v1.UpsertSkillDraftDirectoryResponse{File: skillFileDOToDTO(out, false)}, nil
}

func (s *SkillService) DeleteSkillDraftPath(ctx context.Context, req *v1.DeleteSkillDraftPathRequest) (*v1.DeleteSkillDraftPathResponse, error) {
	principal := principalFromContext(ctx)
	if err := s.uc.DeleteSkillDraftPath(ctx, principal, biz.SkillDraftDeleteInput{SkillName: req.GetName(), Version: req.GetVersion(), Path: req.GetPath(), Recursive: req.GetRecursive()}); err != nil {
		return nil, err
	}
	return &v1.DeleteSkillDraftPathResponse{}, nil
}

func (s *SkillService) MoveSkillDraftPath(ctx context.Context, req *v1.MoveSkillDraftPathRequest) (*v1.MoveSkillDraftPathResponse, error) {
	principal := principalFromContext(ctx)
	if err := s.uc.MoveSkillDraftPath(ctx, principal, biz.SkillDraftMoveInput{SkillName: req.GetName(), Version: req.GetVersion(), OldPath: req.GetOldPath(), NewPath: req.GetNewPath(), Overwrite: req.GetOverwrite()}); err != nil {
		return nil, err
	}
	return &v1.MoveSkillDraftPathResponse{}, nil
}

func (s *SkillService) CommitSkillDraft(ctx context.Context, req *v1.CommitSkillDraftRequest) (*v1.CommitSkillDraftResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.CommitSkillDraft(ctx, principal, biz.SkillDraftCommitInput{SkillName: req.GetName(), Version: req.GetVersion(), CommitMsg: req.GetCommitMsg(), Overwrite: req.GetOverwrite(), Submit: req.GetSubmit(), Publish: req.GetPublish(), Online: req.GetOnline()})
	if err != nil {
		return nil, err
	}
	return &v1.CommitSkillDraftResponse{Version: skillVersionDOToDTO(out)}, nil
}

// --- SkillFile ---

func (s *SkillService) ListSkillVersionFiles(ctx context.Context, req *v1.ListSkillVersionFilesRequest) (*v1.ListSkillVersionFilesResponse, error) {
	principal := principalFromContext(ctx)
	files, err := s.uc.ListSkillVersionFiles(ctx, principal, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	out := &v1.ListSkillVersionFilesResponse{
		Files: make([]*v1.SkillFile, 0, len(files)),
	}
	for _, f := range files {
		out.Files = append(out.Files, skillFileDOToDTO(f, false))
	}
	return out, nil
}

func (s *SkillService) GetSkillVersionFile(ctx context.Context, req *v1.GetSkillVersionFileRequest) (*v1.GetSkillVersionFileResponse, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.GetSkillVersionFile(ctx, principal, req.GetName(), req.GetVersion(), req.GetPath())
	if err != nil {
		return nil, err
	}
	return &v1.GetSkillVersionFileResponse{File: skillFileDOToDTO(out, true)}, nil
}

// --- Skill share ---

func (s *SkillService) ListSkillShares(ctx context.Context, req *v1.ListSkillSharesRequest) (*v1.ListSkillSharesResponse, error) {
	principal := principalFromContext(ctx)
	shares, err := s.uc.ListSkillShares(ctx, principal, req.GetName())
	if err != nil {
		return nil, err
	}
	out := &v1.ListSkillSharesResponse{
		Shares: make([]*v1.SkillShare, 0, len(shares)),
	}
	for _, sh := range shares {
		out.Shares = append(out.Shares, skillShareDOToDTO(sh))
	}
	return out, nil
}

func (s *SkillService) CreateSkillShare(ctx context.Context, req *v1.CreateSkillShareRequest) (*v1.SkillShare, error) {
	principal := principalFromContext(ctx)
	out, err := s.uc.CreateSkillShare(ctx, principal, biz.SkillShareInput{
		Name:            req.GetName(),
		Relation:        req.GetRelation(),
		SubjectType:     req.GetSubjectType(),
		SubjectID:       req.GetSubjectId(),
		SubjectRelation: req.GetSubjectRelation(),
	})
	if err != nil {
		return nil, err
	}
	return skillShareDOToDTO(out), nil
}

func (s *SkillService) DeleteSkillShare(ctx context.Context, req *v1.DeleteSkillShareRequest) (*v1.DeleteSkillShareResponse, error) {
	principal := principalFromContext(ctx)
	if err := s.uc.DeleteSkillShare(ctx, principal, req.GetName(), req.GetSubjectType(), req.GetSubjectId()); err != nil {
		return nil, err
	}
	return &v1.DeleteSkillShareResponse{}, nil
}

func skillShareDOToDTO(sh *biz.SkillShare) *v1.SkillShare {
	if sh == nil {
		return nil
	}
	return &v1.SkillShare{
		ResourceType:    sh.ResourceType,
		ResourceId:      sh.ResourceID,
		Relation:        sh.Relation,
		SubjectType:     sh.SubjectType,
		SubjectId:       sh.SubjectID,
		SubjectRelation: sh.SubjectRelation,
	}
}

// --- DTO conversion helpers ---

func skillDOToDTO(item *biz.Skill) *v1.Skill {
	if item == nil {
		return nil
	}
	return &v1.Skill{
		Id:           item.ID,
		Name:         item.Name,
		DisplayName:  item.DisplayName,
		Description:  item.Description,
		Version:      item.Version,
		Status:       item.Status,
		Visibility:   item.Visibility,
		OwnerId:      item.OwnerID,
		OrgId:        item.OrgID,
		ProjectId:    item.ProjectID,
		SourceType:   item.SourceType,
		SourceUri:    item.SourceURI,
		ManifestJson: item.ManifestJSON,
		Tags:         append([]string(nil), item.Tags...),
		CreateTime:   timestampOrNil(item.CreateTime),
		UpdateTime:   timestampOrNil(item.UpdateTime),
	}
}

func skillListResultToDTO(out *biz.SkillListResult) *v1.ListSkillsResponse {
	resp := &v1.ListSkillsResponse{}
	if out == nil {
		return resp
	}
	resp.Skills = make([]*v1.Skill, 0, len(out.Items))
	for _, item := range out.Items {
		resp.Skills = append(resp.Skills, skillDOToDTO(item))
	}
	if out.HasMore {
		resp.NextPageToken = strconv.Itoa(out.NextOffset)
	}
	return resp
}

func skillVersionDOToDTO(item *biz.SkillVersion) *v1.SkillVersion {
	if item == nil {
		return nil
	}
	return &v1.SkillVersion{
		Id:                  item.ID,
		SkillName:           item.SkillName,
		Version:             item.Version,
		Status:              item.Status,
		Author:              item.Author,
		CommitMsg:           item.CommitMsg,
		PublishPipelineInfo: item.PublishPipelineInfo,
		DownloadCount:       item.DownloadCount,
		Md5:                 item.MD5,
		Sha256:              item.SHA256,
		Revision:            item.Revision,
		SizeBytes:           item.SizeBytes,
		ManifestJson:        item.ManifestJSON,
		CreateTime:          timestampOrNil(item.CreateTime),
		UpdateTime:          timestampOrNil(item.UpdateTime),
	}
}

func skillPackageDownloadDOToDTO(item *biz.SkillPackageDownload) *v1.SkillPackageDownload {
	if item == nil {
		return nil
	}
	return &v1.SkillPackageDownload{
		SkillName:    item.SkillName,
		Version:      item.Version,
		Etag:         item.ETag,
		Md5:          item.MD5,
		Sha256:       item.SHA256,
		NotModified:  item.NotModified,
		PackageBytes: append([]byte(nil), item.PackageBytes...),
	}
}

func skillFileDOToDTO(item *biz.SkillFile, includeContent bool) *v1.SkillFile {
	if item == nil {
		return nil
	}
	out := &v1.SkillFile{
		Id:         item.ID,
		SkillName:  item.SkillName,
		Version:    item.Version,
		Path:       item.Path,
		Name:       item.Name,
		Type:       item.Type,
		Size:       item.Size,
		Binary:     item.Binary,
		CreateTime: timestampOrNil(item.CreateTime),
		UpdateTime: timestampOrNil(item.UpdateTime),
	}
	if includeContent {
		out.Content = item.Content
	}
	return out
}

// --- helpers ---

// principalFromContext extracts the kernel authn.Principal from ctx.
//
// When the authn middleware (server/authn_middleware.go) is mounted, the
// principal is attached to ctx by the middleware before the handler runs.
// When authn is disabled (dev mode), the middleware is not mounted and
// this function returns Anonymous — the biz layer's authz checks then
// short-circuit (allow all) so dev mode stays usable.
func principalFromContext(ctx context.Context) authn.Principal {
	if p, ok := authn.PrincipalFromContext(ctx); ok {
		return p
	}
	return authn.Anonymous()
}

// timestampOrNil converts a time.Time to a *timestamppb.Timestamp, or
// nil when zero. Keeps the JSON output clean for rows that haven't been
// touched yet (created_at default 'now()' in PG so this rarely fires).
func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// parseSkillPageToken decodes a page token into an offset. Returns 0
// for empty token, ErrSkillInvalidArgument for malformed token.
func parseSkillPageToken(token string) (int, error) {
	if strings.TrimSpace(token) == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(token)
	if err != nil || offset < 0 {
		return 0, biz.ErrSkillInvalidArgument
	}
	return offset, nil
}
