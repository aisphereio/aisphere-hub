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
	// The catalog resolver surfaces the proto-declared authz action ("update")
	// and the resource template resolved against the request. ObjectRef splits
	// "aihub:skill:demo" into Type="aihub", ID="skill:demo".
	if check.Permission != "update" || check.Resource.Type != "aihub" || check.Resource.ID != "skill:demo" {
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
