package server

import (
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

// agentSpaceObjectID is deterministic so all Agents in one project inherit
// the project's IAM permissions through the same shared Agent space.
func agentSpaceObjectID(orgID, projectID string) string {
	return strings.TrimSpace(orgID) + "/" + strings.TrimSpace(projectID) + "/agents"
}

func agentAuthzRelationships(row agentRow) []biz.AuthzRelationship {
	agent := biz.AuthzObjectRef{Type: "agent", ID: row.AgentID}
	rels := []biz.AuthzRelationship{{
		Resource: agent,
		Relation: "owner",
		Subject:  biz.AuthzSubjectRef{Type: row.OwnerType, ID: row.OwnerID},
	}}
	if normalizeAgentScope(row.Scope) == "project" || normalizeAgentScope(row.Scope) == "public" {
		if strings.TrimSpace(row.ProjectID) != "" {
			spaceID := agentSpaceObjectID(row.OrgID, row.ProjectID)
			rels = append(rels,
				biz.AuthzRelationship{
					Resource: biz.AuthzObjectRef{Type: "agent_space", ID: spaceID},
					Relation: "parent",
					Subject:  biz.AuthzSubjectRef{Type: "project", ID: row.ProjectID},
				},
				biz.AuthzRelationship{
					Resource: agent,
					Relation: "parent",
					Subject:  biz.AuthzSubjectRef{Type: "agent_space", ID: spaceID},
				},
			)
		}
	}
	if normalizeAgentScope(row.Scope) == "public" {
		// Public means discoverable and launchable by authenticated users. Tool
		// and model dependencies still undergo their own IAM checks at resolve.
		rels = append(rels,
			biz.AuthzRelationship{Resource: agent, Relation: "public_viewer", Subject: biz.AuthzSubjectRef{Type: "user", ID: "*"}},
			biz.AuthzRelationship{Resource: agent, Relation: "public_executor", Subject: biz.AuthzSubjectRef{Type: "user", ID: "*"}},
		)
	}
	return rels
}
