package gitengine

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aisphereio/kernel/accessx"
	"github.com/aisphereio/kernel/authz"
	khttp "github.com/aisphereio/kernel/transportx/http"
	softweb "github.com/aisphereio/soft-serve/pkg/web"
)

const HTTPPrefix = "/git"

type ProtocolAccess struct {
	Repository string
	Protocol   string
	Action     softweb.Action
}

// DescribeProtocolRequest removes Hub's routing prefix before delegating wire
// classification to Soft Serve. The cloned request is also the request passed
// to the terminal handler, so the embedded router sees its native repo path.
func DescribeProtocolRequest(r *http.Request) (khttp.ProtocolRequest, error) {
	if r == nil {
		return khttp.ProtocolRequest{}, fmt.Errorf("gitengine: HTTP request is required")
	}
	request := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = strings.TrimPrefix(urlCopy.Path, HTTPPrefix)
	if urlCopy.Path == "" {
		urlCopy.Path = "/"
	}
	request.URL = &urlCopy

	described, err := softweb.DescribeRequest(request)
	if err != nil {
		return khttp.ProtocolRequest{}, err
	}
	payload := ProtocolAccess{
		Repository: strings.TrimSuffix(strings.TrimSpace(described.Repository), ".git"),
		Protocol:   described.Protocol,
		Action:     described.Action,
	}
	operation, err := protocolOperation(payload)
	if err != nil {
		return khttp.ProtocolRequest{}, err
	}
	return khttp.ProtocolRequest{Operation: operation, Payload: payload, Request: request}, nil
}

func ResolveProtocolAccess(_ context.Context, operation string, request any) (accessx.Check, bool, error) {
	payload, ok := request.(ProtocolAccess)
	if !ok {
		return accessx.Check{}, false, fmt.Errorf("gitengine: unexpected protocol request %T", request)
	}
	permission := ""
	switch payload.Action {
	case softweb.ActionRead:
		permission = "view"
	case softweb.ActionWrite:
		// receive-pack is refined per ref by the server-side update hook.
		// The transport check only proves the caller can discover the Skill;
		// branch/tag updates then require edit, publish, or manage exactly once.
		permission = "edit"
		if payload.Protocol == softweb.ProtocolGit {
			permission = "view"
		}
	default:
		return accessx.Check{}, false, fmt.Errorf("gitengine: unsupported action %q", payload.Action)
	}
	if strings.TrimSpace(payload.Repository) == "" {
		return accessx.Check{}, false, fmt.Errorf("gitengine: repository is required")
	}
	return accessx.Check{
		Permission:  permission,
		Resource:    authz.ObjectRef{Type: "skill", ID: payload.Repository},
		AuditAction: operation,
	}, true, nil
}

func protocolOperation(request ProtocolAccess) (string, error) {
	switch {
	case request.Protocol == softweb.ProtocolGit && request.Action == softweb.ActionRead:
		return "git.fetch", nil
	case request.Protocol == softweb.ProtocolGit && request.Action == softweb.ActionWrite:
		return "git.push", nil
	case request.Protocol == softweb.ProtocolLFS && request.Action == softweb.ActionRead:
		return "git.lfs.read", nil
	case request.Protocol == softweb.ProtocolLFS && request.Action == softweb.ActionWrite:
		return "git.lfs.write", nil
	default:
		return "", fmt.Errorf("gitengine: unsupported protocol %q action %q", request.Protocol, request.Action)
	}
}
