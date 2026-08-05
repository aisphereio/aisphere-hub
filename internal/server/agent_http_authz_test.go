package server

import "testing"

func TestAgentAuthzRelationshipsForPrivateAgent(t *testing.T) {
	rels := agentAuthzRelationships(agentRow{
		AgentID: "private-agent", Scope: "private", OwnerType: "user", OwnerID: "alice",
		OrgID: "org-1", ProjectID: "project-1",
	})
	if len(rels) != 1 || rels[0].Relation != "owner" {
		t.Fatalf("private relationships = %#v, want owner only", rels)
	}
}

func TestAgentAuthzRelationshipsForProjectAgent(t *testing.T) {
	rels := agentAuthzRelationships(agentRow{
		AgentID: "project-agent", Scope: "project", OwnerType: "user", OwnerID: "alice",
		OrgID: "org-1", ProjectID: "project-1",
	})
	if len(rels) != 3 {
		t.Fatalf("project relationships = %#v, want owner plus project space projection", rels)
	}
	if rels[1].Resource.Type != "agent_space" || rels[1].Relation != "parent" || rels[1].Subject.ID != "project-1" {
		t.Fatalf("agent space parent = %#v", rels[1])
	}
	if rels[2].Resource.Type != "agent" || rels[2].Relation != "parent" || rels[2].Subject.Type != "agent_space" {
		t.Fatalf("agent parent = %#v", rels[2])
	}
}

func TestAgentAuthzRelationshipsForPublicAgent(t *testing.T) {
	rels := agentAuthzRelationships(agentRow{
		AgentID: "public-agent", Scope: "public", OwnerType: "user", OwnerID: "alice",
		OrgID: "org-1", ProjectID: "project-1",
	})
	if len(rels) != 5 {
		t.Fatalf("public relationships = %#v, want project projection plus public grants", rels)
	}
	if rels[3].Relation != "public_viewer" || rels[3].Subject.ID != "*" {
		t.Fatalf("public viewer = %#v", rels[3])
	}
	if rels[4].Relation != "public_executor" || rels[4].Subject.ID != "*" {
		t.Fatalf("public executor = %#v", rels[4])
	}
}
