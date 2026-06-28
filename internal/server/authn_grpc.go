package server

import (
	"context"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/aisphere-hub/internal/observability"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func newAuthnUnaryInterceptor(resources *data.Resources) grpc.UnaryServerInterceptor {
	if resources == nil || resources.Authn == nil {
		return nil
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := authenticateGRPC(ctx, resources, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func newAuthnStreamInterceptor(resources *data.Resources) grpc.StreamServerInterceptor {
	if resources == nil || resources.Authn == nil {
		return nil
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authenticateGRPC(ss.Context(), resources, info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{ServerStream: ss, ctx: ctx})
	}
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context { return s.ctx }

func authenticateGRPC(ctx context.Context, resources *data.Resources, method string) (context.Context, error) {
	start := time.Now()
	logger := logx.FromContextOr(ctx, grpcAuthLogger(resources)).Named("authn.middleware").With(logx.String("method", method), logx.String("transport", "grpc"))
	ctx = logx.Inject(ctx, logger)
	ctx = metricsx.Inject(ctx, grpcAuthMetrics(resources))

	if isAuthnPublicGRPCMethod(method) {
		ctx = authn.ContextWithPrincipal(ctx, authn.Anonymous())
		observability.MiddlewareDecision(ctx, grpcAuthMetrics(resources), "grpc", "public", start, nil)
		logger.Debug("grpc authn skipped public method")
		return ctx, nil
	}

	token := bearerTokenFromContext(ctx)
	if token == "" {
		err := authn.ErrUnauthenticated("missing or malformed authorization metadata")
		observability.MiddlewareDecision(ctx, grpcAuthMetrics(resources), "grpc", "missing_token", start, err)
		logger.Warn("grpc authn rejected request", logx.String("reason", "missing_token"), logx.Err(err))
		return ctx, status.Error(codes.Unauthenticated, err.Error())
	}

	principal, err := resources.Authn.Authenticate(ctx, authn.Credential{Scheme: authn.CredentialBearer, Token: token})
	if err != nil {
		code := codes.Unauthenticated
		if isAuthnBackendError(err) {
			code = codes.Unavailable
		}
		observability.MiddlewareDecision(ctx, grpcAuthMetrics(resources), "grpc", "rejected", start, err)
		logger.Warn("grpc authn rejected request", logx.String("grpc_code", code.String()), logx.String("error_code", observability.ErrorCode(err)), logx.Err(err))
		return ctx, status.Error(code, err.Error())
	}

	principal = principal.Normalize()
	ctx = authn.ContextWithPrincipal(ctx, principal)
	observability.MiddlewareDecision(ctx, grpcAuthMetrics(resources), "grpc", "accepted", start, nil)
	logger.Debug("grpc authn accepted request", logx.String("subject_id", principal.SubjectID), logx.String("org_id", principal.OrgID))
	return ctx, nil
}

func bearerTokenFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, value := range md.Get("authorization") {
		if token := bearerTokenFromHeader(value); token != "" {
			return token
		}
	}
	return ""
}

func isAuthnPublicGRPCMethod(method string) bool {
	return strings.HasPrefix(method, "/aisphere.hub.authn.v1.AuthnService/LoginURL") ||
		strings.HasPrefix(method, "/aisphere.hub.authn.v1.AuthnService/Exchange") ||
		strings.HasPrefix(method, "/aisphere.hub.authn.v1.AuthnService/Refresh") ||
		strings.HasPrefix(method, "/aisphere.hub.authn.v1.AuthnService/LogoutURL") ||
		strings.HasPrefix(method, "/aisphere.hub.authn.v1.AuthnService/Revoke") ||
		strings.HasPrefix(method, "/aisphere.hub.authn.v1.AuthnService/Introspect") ||
		strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

func grpcAuthLogger(resources *data.Resources) logx.Logger {
	if resources != nil && resources.Logger != nil {
		return resources.Logger
	}
	return logx.DefaultLogger()
}

func grpcAuthMetrics(resources *data.Resources) metricsx.Manager {
	if resources != nil {
		return metricsx.Ensure(resources.Metrics)
	}
	return metricsx.Noop()
}
