package service

import (
	"context"
	"encoding/base64"
	"strings"

	skillv1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

// FileService adapts the generated FileServiceServer / HTTPServer
// interfaces onto biz.FileUsecase. It is a thin transport adapter: it
// decodes the base64 content on the way in, runs the biz method, and
// re-encodes content + timestamps on the way out. Authz is enforced
// inside the usecase, not here.
type FileService struct {
	skillv1.UnimplementedFileServiceServer
	uc *biz.FileUsecase
}

func NewFileService(uc *biz.FileUsecase) *FileService { return &FileService{uc: uc} }

// RegisterHTTPServer wires the file-content routes onto the Kernel HTTP
// server. Mirrors SkillService.RegisterHTTPServer so http.go can treat
// both services the same way.
func (s *FileService) RegisterHTTPServer(server *khttp.Server) {
	skillv1.RegisterFileServiceHTTPServer(server, s)
}

func (s *FileService) ListFiles(ctx context.Context, req *skillv1.ListFilesRequest) (*skillv1.FileContents, error) {
	entries, err := s.uc.ListFiles(ctx, principalFromContext(ctx), req.GetSkillName(), req.GetPath(), req.GetRef())
	if err != nil {
		return nil, err
	}
	out := &skillv1.FileContents{Ref: req.GetRef(), Path: req.GetPath()}
	for _, entry := range entries {
		out.Entries = append(out.Entries, fileInfoToProto(entry))
	}
	if out.Entries == nil {
		out.Entries = []*skillv1.FileInfo{}
	}
	return out, nil
}

func (s *FileService) GetFile(ctx context.Context, req *skillv1.GetFileRequest) (*skillv1.GetFileResponse, error) {
	content, err := s.uc.GetFileContent(ctx, principalFromContext(ctx), req.GetSkillName(), req.GetPath(), req.GetRef())
	if err != nil {
		return nil, err
	}
	return fileContentToGetFileResponse(content), nil
}

func (s *FileService) CreateFile(ctx context.Context, req *skillv1.CreateFileRequest) (*skillv1.CreateFileResponse, error) {
	content, err := decodeContent(req.GetContent())
	if err != nil {
		return nil, errorx.BadRequest(errorx.Code("SKILL_FILE_CONTENT_INVALID"), "content is not valid base64")
	}
	out, err := s.uc.FileCreate(ctx, principalFromContext(ctx), req.GetSkillName(), req.GetPath(), string(content), req.GetMessage(), req.GetBranch())
	if err != nil {
		return nil, err
	}
	return fileContentToCreateFileResponse(out), nil
}

func (s *FileService) UpdateFile(ctx context.Context, req *skillv1.UpdateFileRequest) (*skillv1.UpdateFileResponse, error) {
	content, err := decodeContent(req.GetContent())
	if err != nil {
		return nil, errorx.BadRequest(errorx.Code("SKILL_FILE_CONTENT_INVALID"), "content is not valid base64")
	}
	out, err := s.uc.FileUpdate(ctx, principalFromContext(ctx), req.GetSkillName(), req.GetPath(), string(content), req.GetMessage(), req.GetSha(), req.GetBranch())
	if err != nil {
		return nil, err
	}
	return fileContentToUpdateFileResponse(out), nil
}

func (s *FileService) DeleteFile(ctx context.Context, req *skillv1.DeleteFileRequest) (*skillv1.DeleteFileResponse, error) {
	commitSHA, commitMessage, err := s.uc.FileDelete(ctx, principalFromContext(ctx), req.GetSkillName(), req.GetPath(), req.GetMessage(), req.GetSha(), req.GetBranch())
	if err != nil {
		return nil, err
	}
	return &skillv1.DeleteFileResponse{CommitSha: commitSHA, CommitMessage: commitMessage}, nil
}

// --- converters ------------------------------------------------------------

func fileInfoToProto(in *biz.FileInfo) *skillv1.FileInfo {
	if in == nil {
		return nil
	}
	return &skillv1.FileInfo{
		Name:         in.Name,
		Path:         in.Path,
		Type:         in.Type,
		Size:         in.Size,
		Mode:         in.Mode,
		Sha:          in.SHA,
		LastModified: timestamp(in.LastModified),
	}
}

func fileContentToGetFileResponse(in *biz.FileContent) *skillv1.GetFileResponse {
	if in == nil {
		return nil
	}
	return &skillv1.GetFileResponse{
		Name:          in.Name,
		Path:          in.Path,
		Sha:           in.SHA,
		Size:          in.Size,
		Content:       in.Content,
		Encoding:      in.Encoding,
		Ref:           in.Ref,
		CommitSha:     in.CommitSHA,
		CommitMessage: in.CommitMessage,
		LastModified:  timestamp(in.LastModified),
	}
}

func fileContentToCreateFileResponse(in *biz.FileContent) *skillv1.CreateFileResponse {
	if in == nil {
		return nil
	}
	return &skillv1.CreateFileResponse{
		Name:          in.Name,
		Path:          in.Path,
		Sha:           in.SHA,
		Size:          in.Size,
		Content:       in.Content,
		Encoding:      in.Encoding,
		Ref:           in.Ref,
		CommitSha:     in.CommitSHA,
		CommitMessage: in.CommitMessage,
		LastModified:  timestamp(in.LastModified),
	}
}

func fileContentToUpdateFileResponse(in *biz.FileContent) *skillv1.UpdateFileResponse {
	if in == nil {
		return nil
	}
	return &skillv1.UpdateFileResponse{
		Name:          in.Name,
		Path:          in.Path,
		Sha:           in.SHA,
		Size:          in.Size,
		Content:       in.Content,
		Encoding:      in.Encoding,
		Ref:           in.Ref,
		CommitSha:     in.CommitSHA,
		CommitMessage: in.CommitMessage,
		LastModified:  timestamp(in.LastModified),
	}
}

// decodeContent accepts standard base64. Empty content is allowed so
// the editor can create an empty file (the UI sends "" which decodes
// to an empty byte slice).
func decodeContent(content string) ([]byte, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return []byte{}, nil
	}
	return base64.StdEncoding.DecodeString(content)
}
