package server

import (
	auditv1 "github.com/aisphereio/aisphere-hub/api/audit/v1"
	authnv1 "github.com/aisphereio/aisphere-hub/api/authn/v1"
	kubernetesv1 "github.com/aisphereio/aisphere-hub/api/kubernetes/v1"
	modelv1 "github.com/aisphereio/aisphere-hub/api/model/v1"
	skillv1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
	toolv1 "github.com/aisphereio/aisphere-hub/api/tool/v1"
	"github.com/aisphereio/aisphere-hub/internal/service"
	"github.com/aisphereio/kernel/serverx"
)

// HubModules is the single generated-service catalog for request metadata,
// access resolution, Gateway manifests, and transport registration hooks.
//
// IAM owns the authorization control plane. Hub deliberately does not publish
// its legacy AuthzService module; Hub business services consume IAM's runtime
// authorization API through the IAM gRPC client.
//
// Hand-written HTTP resources (Agent, SkillSet, and Model Management V2) are
// deliberately not represented here because they do not have generated service
// modules yet; they keep their explicit secured registration paths.
func HubModules() []serverx.ServiceModule {
	return []serverx.ServiceModule{
		authnv1.AuthnServiceKernelModule(),
		auditv1.AuditServiceKernelModule(),
		skillv1.SkillServiceKernelModule(),
		skillv1.SkillReleaseServiceKernelModule(),
		skillv1.FileServiceKernelModule(),
		kubernetesv1.ClusterServiceKernelModule(),
		kubernetesv1.NamespaceServiceKernelModule(),
		kubernetesv1.SandboxServiceKernelModule(),
		toolv1.ToolServiceKernelModule(),
		modelv1.ModelProfileServiceKernelModule(),
	}
}

func HubCatalog() serverx.ServiceCatalog {
	return serverx.MustServiceCatalog(HubModules()...)
}

// HubHTTPBindings returns generated HTTP bindings that do not require custom
// transport behavior. Authn is registered separately because it adds browser
// 302 login/logout routes. ModelProfile is intentionally gRPC-only while Model
// Management V2 owns the public /v1/models, /v1/endpoints, and
// /v1/model-profiles HTTP contract.
func HubHTTPBindings(
	auditSvc *service.AuditService,
	skillSvc *service.SkillService,
	clusterSvc *service.ClusterService,
	namespaceSvc *service.NamespaceService,
	sandboxSvc *service.SandboxService,
	fileSvc *service.FileService,
	toolSvc *service.ToolService,
) []serverx.ServiceBinding {
	bindings := make([]serverx.ServiceBinding, 0, 8)
	if auditSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: auditv1.AuditServiceKernelModule(), Implementation: auditSvc})
	}
	if skillSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: skillv1.SkillServiceKernelModule(), Implementation: skillSvc})
		if releaseSvc := skillSvc.ReleaseService(); releaseSvc != nil {
			bindings = append(bindings, serverx.ServiceBinding{Module: skillv1.SkillReleaseServiceKernelModule(), Implementation: releaseSvc})
		}
	}
	if clusterSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: kubernetesv1.ClusterServiceKernelModule(), Implementation: clusterSvc})
	}
	if namespaceSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: kubernetesv1.NamespaceServiceKernelModule(), Implementation: namespaceSvc})
	}
	if sandboxSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: kubernetesv1.SandboxServiceKernelModule(), Implementation: sandboxSvc})
	}
	if fileSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: skillv1.FileServiceKernelModule(), Implementation: fileSvc})
	}
	if toolSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: toolv1.ToolServiceKernelModule(), Implementation: toolSvc})
	}
	return bindings
}

// HubGRPCBindings returns all generated gRPC service bindings owned by Hub.
// Authn needs no custom gRPC routes and the legacy ModelProfile transport remains
// available to internal clients during the Model Management V2 migration.
func HubGRPCBindings(
	authnSvc *service.AuthnService,
	auditSvc *service.AuditService,
	skillSvc *service.SkillService,
	clusterSvc *service.ClusterService,
	namespaceSvc *service.NamespaceService,
	sandboxSvc *service.SandboxService,
	fileSvc *service.FileService,
	toolSvc *service.ToolService,
	modelProfileSvc *service.ModelProfileService,
) []serverx.ServiceBinding {
	bindings := make([]serverx.ServiceBinding, 0, 10)
	if authnSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: authnv1.AuthnServiceKernelModule(), Implementation: authnSvc})
	}
	bindings = append(bindings, HubHTTPBindings(auditSvc, skillSvc, clusterSvc, namespaceSvc, sandboxSvc, fileSvc, toolSvc)...)
	if modelProfileSvc != nil {
		bindings = append(bindings, serverx.ServiceBinding{Module: modelv1.ModelProfileServiceKernelModule(), Implementation: modelProfileSvc})
	}
	return bindings
}
