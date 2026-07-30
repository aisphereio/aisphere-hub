package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	toolv1 "github.com/aisphereio/aisphere-hub/api/tool/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ToolService implements the Tool catalog HTTP/gRPC service.
type ToolService struct {
	toolv1.UnimplementedToolServiceServer
	uc *biz.ToolUsecase
}

func NewToolService(uc *biz.ToolUsecase) *ToolService { return &ToolService{uc: uc} }

func (s *ToolService) RegisterHTTPServer(server *khttp.Server) {
	toolv1.RegisterToolServiceHTTPServer(server, s)
}

func (s *ToolService) ListTools(ctx context.Context, req *toolv1.ListToolsRequest) (*toolv1.ListToolsResponse, error) {
	offset, err := decodePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	tools, next, err := s.uc.ListTools(ctx, principalFromContext(ctx), biz.ToolListOptions{
		Limit:       int(req.GetPageSize()),
		Offset:      offset,
		Query:       req.GetQuery(),
		Scope:       req.GetScope(),
		Status:      req.GetStatus(),
		RuntimeType: req.GetRuntimeType(),
	})
	if err != nil {
		return nil, err
	}
	out := &toolv1.ListToolsResponse{Tools: make([]*toolv1.Tool, 0, len(tools))}
	for _, t := range tools {
		out.Tools = append(out.Tools, toolToProto(t))
	}
	out.NextPageToken = next
	return out, nil
}

func (s *ToolService) GetTool(ctx context.Context, req *toolv1.GetToolRequest) (*toolv1.GetToolResponse, error) {
	t, err := s.uc.GetTool(ctx, principalFromContext(ctx), req.GetId(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return &toolv1.GetToolResponse{Tool: toolToProto(t)}, nil
}

func (s *ToolService) CreateTool(ctx context.Context, req *toolv1.CreateToolRequest) (*toolv1.CreateToolResponse, error) {
	t, err := protoToTool(req.GetId(), req.GetDefinition(), req.GetVersion(), req.GetCommitMsg(),
		req.GetDisplayName(), req.GetDescription(), req.GetStatus(), req.GetScope(), req.GetLabels(),
		req.GetOrgId(), req.GetProjectId())
	if err != nil {
		return nil, err
	}
	out, err := s.uc.CreateTool(ctx, principalFromContext(ctx), t)
	if err != nil {
		return nil, err
	}
	return &toolv1.CreateToolResponse{Tool: toolToProto(out)}, nil
}

func (s *ToolService) UpdateTool(ctx context.Context, req *toolv1.UpdateToolRequest) (*toolv1.UpdateToolResponse, error) {
	t, err := protoToTool(req.GetId(), req.GetDefinition(), req.GetVersion(), req.GetCommitMsg(),
		req.GetDisplayName(), req.GetDescription(), req.GetStatus(), req.GetScope(), req.GetLabels(), "", "")
	if err != nil {
		return nil, err
	}
	out, err := s.uc.UpdateTool(ctx, principalFromContext(ctx), t)
	if err != nil {
		return nil, err
	}
	return &toolv1.UpdateToolResponse{Tool: toolToProto(out)}, nil
}

func (s *ToolService) DeleteTool(ctx context.Context, req *toolv1.DeleteToolRequest) (*toolv1.DeleteToolResponse, error) {
	out, err := s.uc.DeleteTool(ctx, principalFromContext(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	return &toolv1.DeleteToolResponse{Tool: toolToProto(out)}, nil
}

func (s *ToolService) ResolveTool(ctx context.Context, req *toolv1.ResolveToolRequest) (*toolv1.ResolveToolResponse, error) {
	if err := s.uc.ResolveTool(ctx, principalFromContext(ctx), req.GetId(),
		req.GetRuntimeId(), req.GetSessionId(), req.GetVersion(), req.GetLabel()); err != nil {
		return nil, err
	}
	// Unreachable today (ResolveTool always returns Unavailable); the body
	// below is the shape callers will receive once the Runtime lands.
	return &toolv1.ResolveToolResponse{
		SnapshotId:  "",
		RuntimeId:   req.GetRuntimeId(),
		SessionId:   req.GetSessionId(),
		GeneratedAt: timestamppb.Now(),
	}, nil
}

func (s *ToolService) ListToolFailures(ctx context.Context, req *toolv1.ListToolFailuresRequest) (*toolv1.ListToolFailuresResponse, error) {
	offset, err := decodePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	if err := s.uc.ListToolFailures(ctx, principalFromContext(ctx), req.GetId(),
		int(req.GetPageSize()), offset); err != nil {
		return nil, err
	}
	return &toolv1.ListToolFailuresResponse{}, nil
}

// --- proto <-> biz conversion ---

func toolToProto(t *biz.Tool) *toolv1.Tool {
	if t == nil {
		return nil
	}
	ownerSubject := strings.TrimSpace(t.OwnerType)
	if ownerSubject != "" && t.OwnerID != "" {
		ownerSubject = t.OwnerType + ":" + t.OwnerID
	}
	out := &toolv1.Tool{
		Id: t.ID, DisplayName: t.DisplayName, Description: t.Description,
		Status: t.Status, Scope: t.Scope, Labels: t.Labels, Object: t.Object,
		OwnerSubject: ownerSubject, LatestVersion: t.LatestVersion,
		CreateTime: timestamp(t.CreatedAt), UpdateTime: timestamp(t.UpdatedAt),
		Versions: make(map[string]*toolv1.ToolVersionRecord, len(t.Versions)),
	}
	for ver, v := range t.Versions {
		out.Versions[ver] = toolVersionToProto(v)
	}
	return out
}

func toolVersionToProto(v biz.ToolVersion) *toolv1.ToolVersionRecord {
	return &toolv1.ToolVersionRecord{
		Version: v.Version, Revision: v.Revision, Sha256: v.SHA256,
		Author: v.Author, CommitMsg: v.CommitMsg, CreateTime: timestamp(v.CreateTime),
		Definition: toolDefinitionToProto(v.Definition),
	}
}

func toolDefinitionToProto(d biz.ToolDefinition) *toolv1.ToolDefinition {
	out := &toolv1.ToolDefinition{
		Runtime:       toolRuntimeToProto(d.Runtime),
		InputSchema:   rawJSONToString(d.InputSchema),
		OutputSchema:  rawJSONToString(d.OutputSchema),
		TimeoutMillis: d.TimeoutMillis,
		Metadata:      rawJSONToString(d.Metadata),
	}
	if d.Execution != nil {
		out.Execution = toolExecutionToProto(*d.Execution)
	}
	if d.Retry != nil {
		out.Retry = toolRetryToProto(*d.Retry)
	}
	return out
}

func toolRuntimeToProto(r biz.ToolRuntimeDefinition) *toolv1.ToolRuntimeDefinition {
	return &toolv1.ToolRuntimeDefinition{
		Type: r.Type, Server: r.Server, Name: r.Name, Url: r.URL, Method: r.Method,
		Package: r.Package, EntryPoint: r.EntryPoint, Headers: r.Headers,
		Config: rawJSONToString(r.Config), CredentialRef: r.CredentialRef,
		Description: r.Description,
	}
}

func toolExecutionToProto(e biz.ToolExecutionDefinition) *toolv1.ToolExecutionDefinition {
	out := &toolv1.ToolExecutionDefinition{
		Placement: e.Placement, Runner: e.Runner, Image: e.Image, Command: e.Command,
		Args: e.Args, WorkingDir: e.WorkingDir, Filesystem: e.Filesystem, Network: e.Network,
		Env: e.Env, SecretRefs: e.SecretRefs, AllowHosts: e.AllowHosts, DenyHosts: e.DenyHosts,
		Capabilities: e.Capabilities,
	}
	for _, m := range e.Mounts {
		out.Mounts = append(out.Mounts, &toolv1.ToolMount{
			Name: m.Name, Ref: m.Ref, MountPath: m.MountPath, Mode: m.Mode,
		})
	}
	if e.Resources != nil {
		out.Resources = &toolv1.ToolResources{
			Cpu: e.Resources.CPU, Memory: e.Resources.Memory,
			TimeoutMillis: e.Resources.TimeoutMillis, MaxOutputBytes: e.Resources.MaxOutputBytes,
		}
	}
	return out
}

func toolRetryToProto(r biz.ToolRetryPolicy) *toolv1.ToolRetryPolicy {
	return &toolv1.ToolRetryPolicy{
		MaxAttempts: r.MaxAttempts, BackoffMillis: r.BackoffMillis,
		RetryOnErrorCodes: r.RetryOnErrorCodes,
	}
}

// protoToTool builds a biz.Tool from a create/update request. The version
// defaults to "v1" when empty so the first definition is always addressable.
// orgID/projectID come from CreateToolRequest (UpdateToolRequest has none and
// passes ""); the biz layer reconciles org against the principal and persists
// the project as-is.
func protoToTool(id string, def *toolv1.ToolDefinition, version, commitMsg,
	displayName, description, status, scope string, labels map[string]string,
	orgID, projectID string) (*biz.Tool, error) {
	if def == nil {
		return nil, errorx.BadRequest(errorx.Code("INVALID_ARGUMENT"), "definition is required")
	}
	bd, err := protoToToolDefinition(def)
	if err != nil {
		return nil, err
	}
	ver := strings.TrimSpace(version)
	if ver == "" {
		ver = "v1"
	}
	return &biz.Tool{
		ID:          id,
		DisplayName: displayName,
		Description: description,
		Status:      status,
		Scope:       scope,
		Labels:      labels,
		OrgID:       strings.TrimSpace(orgID),
		ProjectID:   strings.TrimSpace(projectID),
		LatestVersion: ver,
		Versions: map[string]biz.ToolVersion{
			ver: {Version: ver, CommitMsg: commitMsg, Definition: bd},
		},
	}, nil
}

func protoToToolDefinition(def *toolv1.ToolDefinition) (biz.ToolDefinition, error) {
	d := biz.ToolDefinition{
		TimeoutMillis: def.GetTimeoutMillis(),
		InputSchema:   stringToRawJSON(def.GetInputSchema()),
		OutputSchema:  stringToRawJSON(def.GetOutputSchema()),
		Metadata:      stringToRawJSON(def.GetMetadata()),
	}
	if r := def.GetRuntime(); r != nil {
		d.Runtime = biz.ToolRuntimeDefinition{
			Type: r.GetType(), Server: r.GetServer(), Name: r.GetName(), URL: r.GetUrl(),
			Method: r.GetMethod(), Package: r.GetPackage(), EntryPoint: r.GetEntryPoint(),
			Headers: r.GetHeaders(), Config: stringToRawJSON(r.GetConfig()),
			CredentialRef: r.GetCredentialRef(), Description: r.GetDescription(),
		}
	}
	if e := def.GetExecution(); e != nil {
		exec := biz.ToolExecutionDefinition{
			Placement: e.GetPlacement(), Runner: e.GetRunner(), Image: e.GetImage(),
			Command: e.GetCommand(), Args: e.GetArgs(), WorkingDir: e.GetWorkingDir(),
			Filesystem: e.GetFilesystem(), Network: e.GetNetwork(), Env: e.GetEnv(),
			SecretRefs: e.GetSecretRefs(), AllowHosts: e.GetAllowHosts(), DenyHosts: e.GetDenyHosts(),
			Capabilities: e.GetCapabilities(),
		}
		for _, m := range e.GetMounts() {
			if m == nil {
				continue
			}
			exec.Mounts = append(exec.Mounts, biz.ToolMount{
				Name: m.GetName(), Ref: m.GetRef(), MountPath: m.GetMountPath(), Mode: m.GetMode(),
			})
		}
		if res := e.GetResources(); res != nil {
			exec.Resources = &biz.ToolResources{
				CPU: res.GetCpu(), Memory: res.GetMemory(),
				TimeoutMillis: res.GetTimeoutMillis(), MaxOutputBytes: res.GetMaxOutputBytes(),
			}
		}
		d.Execution = &exec
	}
	if r := def.GetRetry(); r != nil {
		d.Retry = &biz.ToolRetryPolicy{
			MaxAttempts: r.GetMaxAttempts(), BackoffMillis: r.GetBackoffMillis(),
			RetryOnErrorCodes: r.GetRetryOnErrorCodes(),
		}
	}
	return d, nil
}

// rawJSONToString converts a json.RawMessage to a string for proto fields that
// carry arbitrary JSON as a string. Empty/nil → "".
func rawJSONToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

// stringToRawJSON converts a JSON string back to json.RawMessage. Empty → nil
// (so the biz layer can distinguish "unset" from "{}").
func stringToRawJSON(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// keep strconv import even when unused after refactor
var _ = strconv.Atoi
