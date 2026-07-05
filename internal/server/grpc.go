package server

import (
	auditv1 "github.com/aisphereio/aisphere-hub/api/audit/v1"
	authnv1 "github.com/aisphereio/aisphere-hub/api/authn/v1"
	authzv1 "github.com/aisphereio/aisphere-hub/api/authz/v1"
	skillv1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/aisphere-hub/internal/service"

	"github.com/aisphereio/kernel/logx"
	kgrpc "github.com/aisphereio/kernel/transportx/grpc"
)

// NewGRPCServer builds a Kernel gRPC server and registers the enabled
// services.
//
// Each service may be nil when its corresponding feature is disabled in
// config; in that case the gRPC server starts empty (useful for dev/test
// where only health probes are needed).
//
// The 302 redirect routes (/v1/authn/login, /v1/authn/logout) are
// HTTP-only by design (gRPC clients are SPAs/SDKs that should consume
// the JSON RPCs directly), so they do not have gRPC equivalents.
func NewGRPCServer(c conf.ServerConfig, accessLog logx.AccessLogConfig, resources *data.Resources, securityCfg conf.SecurityConfig, authnSvc *service.AuthnService, authzSvc *service.AuthzService, auditSvc *service.AuditService, skillSvc *service.SkillService) *kgrpc.Server {
	var opts []kgrpc.ServerOption
	if c.GRPC.Addr != "" {
		opts = append(opts, kgrpc.Address(c.GRPC.Addr))
	}
	if c.GRPC.Timeout > 0 {
		opts = append(opts, kgrpc.Timeout(c.GRPC.Timeout))
	}
	if resources != nil {
		if resources.Logger != nil {
			opts = append(opts, kgrpc.Logger(resources.Logger.Named("grpc")))
		}
		opts = append(opts, kgrpc.Metrics(resources.Metrics))
	}
	opts = append(opts, kgrpc.AccessLog(accessLog))
	if m := hubServerMiddlewares(resources, securityCfg); len(m) > 0 {
		opts = append(opts, kgrpc.Middleware(m...))
		opts = append(opts, kgrpc.StreamMiddleware(m...))
	}
	srv := kgrpc.NewServer(opts...)
	if authnSvc != nil {
		authnv1.RegisterAuthnServiceServer(srv, authnSvc)
	}
	if authzSvc != nil {
		authzv1.RegisterAuthzServiceServer(srv, authzSvc)
	}
	if auditSvc != nil {
		auditv1.RegisterAuditServiceServer(srv, auditSvc)
	}
	if skillSvc != nil {
		skillv1.RegisterSkillServiceServer(srv, skillSvc)
	}
	return srv
}
