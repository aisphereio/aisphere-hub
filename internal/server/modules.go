package server

import (
	auditv1 "github.com/aisphereio/aisphere-hub/api/audit/v1"
	authnv1 "github.com/aisphereio/aisphere-hub/api/authn/v1"
	skillv1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
	"github.com/aisphereio/kernel/serverx"
)

func HubModules() []serverx.ServiceModule {
	return []serverx.ServiceModule{
		authnv1.AuthnServiceKernelModule(),
		auditv1.AuditServiceKernelModule(),
		skillv1.SkillServiceKernelModule(),
	}
}

func HubCatalog() serverx.ServiceCatalog {
	return serverx.MustServiceCatalog(HubModules()...)
}
