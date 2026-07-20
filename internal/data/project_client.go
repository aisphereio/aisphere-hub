package data

import (
	"context"
	"fmt"
	"strings"

	projectv1 "github.com/aisphereio/aisphere-iam/api/iam/project/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/grpcx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcinsecure "google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// projectValidator validates that a project exists, is readable, and is
// ACTIVE. It calls IAM's ProjectService.GetProject over gRPC with the
// caller's principal propagated via trusted metadata headers.
type projectValidator struct {
	client projectv1.ProjectServiceClient
	conn   *grpc.ClientConn
}

// NewProjectValidator creates a new projectValidator backed by IAM's
// ProjectService gRPC endpoint. The endpoint and caller identity are
// derived from the authz config so Hub uses the same IAM connection.
func NewProjectValidator(endpoint, callerService string, insecure bool) (*projectValidator, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("iam project service endpoint is required")
	}
	clientCfg := grpcx.DefaultClientConfig("iam-project")
	clientCfg.Timeout = 0 // let the caller's context control timeout
	fallbackPrincipal := authn.Principal{
		SubjectID:   callerService,
		SubjectType: authn.SubjectTypeService,
		Provider:    "internal",
	}
	clientCfg.ExtraUnary = append(clientCfg.ExtraUnary, principalUnaryClientInterceptor(fallbackPrincipal))
	opts := grpcx.DialOptions(clientCfg)
	if insecure {
		opts = append(opts, grpc.WithTransportCredentials(grpcinsecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial iam project service: %w", err)
	}
	return &projectValidator{
		client: projectv1.NewProjectServiceClient(conn),
		conn:   conn,
	}, nil
}

// principalUnaryClientInterceptor injects the caller's Principal into
// outgoing gRPC metadata as trusted headers, so IAM can authenticate
// the caller without re-verifying the JWT. Falls back to the service
// identity when no authenticated principal is found in context.
//
// This mirrors the same logic in aisphere-iam/client/authzgrpc to
// ensure consistent identity propagation across all Hub→IAM calls.
func principalUnaryClientInterceptor(fallback authn.Principal) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(outgoingPrincipalContext(ctx, fallback), method, req, reply, cc, opts...)
	}
}

func outgoingPrincipalContext(ctx context.Context, fallback authn.Principal) context.Context {
	principal, ok := authn.PrincipalFromContext(ctx)
	if !ok || !principal.IsAuthenticated() {
		principal = fallback
	}
	if !principal.IsAuthenticated() {
		return ctx
	}
	principal = principal.Normalize()
	headers := map[string]string{
		authn.TrustedHeaderVerified:    "true",
		authn.TrustedHeaderSubject:     principal.SubjectID,
		authn.TrustedHeaderSubjectType: principal.SubjectType,
		authn.TrustedHeaderProvider:    principal.Provider,
		authn.TrustedHeaderOrgID:       principal.OrgID,
		authn.TrustedHeaderProjectID:   principal.ProjectID,
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	for _, name := range authn.TrustedHeaderNames() {
		md.Delete(strings.ToLower(name))
	}
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			md.Set(strings.ToLower(key), value)
		}
	}
	return metadata.NewOutgoingContext(ctx, md)
}

// ValidateProject checks that the project exists, is readable by the caller
// (via the caller's principal propagated through gRPC metadata), and has
// ACTIVE status.
//
// Error mapping:
//   - Project not found or not accessible → 404 PROJECT_NOT_FOUND_OR_ACCESSIBLE
//   - Project exists but not ACTIVE → 409 PROJECT_NOT_ACTIVE
//   - IAM unavailable → 503 IAM_PROJECT_UNAVAILABLE
func (v *projectValidator) ValidateProject(ctx context.Context, orgID, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	orgID = strings.TrimSpace(orgID)
	if projectID == "" || orgID == "" {
		return errorx.BadRequest(errorx.Code("PROJECT_INVALID_ARGUMENT"),
			"project_id and org_id are required for project validation")
	}

project, err := v.client.GetProject(ctx, &projectv1.GetProjectRequest{
			OrgId:     orgID,
			ProjectId: projectID,
		})
	if err != nil {
		st, ok := status.FromError(err)
		if !ok {
			return errorx.Unavailable(errorx.Code("IAM_PROJECT_UNAVAILABLE"),
				"iam project service unavailable")
		}
		switch st.Code() {
		case codes.NotFound:
			return errorx.NotFound(errorx.Code("PROJECT_NOT_FOUND_OR_ACCESSIBLE"),
				"project not found or not accessible")
		case codes.PermissionDenied:
			return errorx.NotFound(errorx.Code("PROJECT_NOT_FOUND_OR_ACCESSIBLE"),
				"project not found or not accessible")
		case codes.Unavailable:
			return errorx.Unavailable(errorx.Code("IAM_PROJECT_UNAVAILABLE"),
				"iam project service unavailable")
		default:
			return errorx.Unavailable(errorx.Code("IAM_PROJECT_UNAVAILABLE"),
				fmt.Sprintf("iam project service error: %v", err))
		}
	}

	// Check project is ACTIVE (LifecycleStatus 1 = ACTIVE)
	if project.GetStatus() != projectv1.LifecycleStatus_ACTIVE {
		return errorx.Conflict(errorx.Code("PROJECT_NOT_ACTIVE"),
			"project is not active (archived, disabled, or deleted)")
	}

	return nil
}

// Close shuts down the gRPC connection.
func (v *projectValidator) Close() error {
	if v == nil || v.conn == nil {
		return nil
	}
	return v.conn.Close()
}

// Ensure projectValidator implements biz.ProjectValidator.
var _ biz.ProjectValidator = (*projectValidator)(nil)