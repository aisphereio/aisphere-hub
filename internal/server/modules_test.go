package server

import (
	"context"
	"testing"

	skillv1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
)

func TestHubCatalogResolvesSkillIAMPermission(t *testing.T) {
	check, ok, err := HubCatalog().AccessResolver(context.Background(), "/skill.v1.SkillService/UpdateSkill", &skillv1.UpdateSkillRequest{Name: "demo"})
	if err != nil || !ok {
		t.Fatalf("resolve = (%+v, %v, %v)", check, ok, err)
	}
	if check.Permission != "edit" || check.Resource.Type != "skill" || check.Resource.ID != "demo" {
		t.Fatalf("check = %+v", check)
	}
}

func TestHubDoesNotExposeAuthzControlPlaneService(t *testing.T) {
	for _, module := range HubModules() {
		if module.Name == "authz-service" {
			t.Fatal("Hub must not expose an Authz control-plane service; callers use IAM gRPC")
		}
	}
}
