package biz

import (
	"testing"

	"github.com/aisphereio/kernel/authn"
)

func TestNormalizeRootSkillCreateKeepsGovernanceOrg(t *testing.T) {
	principal := authn.Principal{SubjectID: "user-1", SubjectType: "user", OrgID: "aisphere"}
	out := NormalizeRootSkillCreate(&Skill{OwnerID: "spoofed", OrgID: "other", ProjectID: "project-1"}, principal)
	if out.OwnerID != "user-1" {
		t.Fatalf("OwnerID = %q, want user-1", out.OwnerID)
	}
	if out.OrgID != "aisphere" {
		t.Fatalf("OrgID = %q, want aisphere", out.OrgID)
	}
	if out.ProjectID != "" {
		t.Fatalf("ProjectID = %q, want empty root placement", out.ProjectID)
	}
}

func TestCanReadSkillByImplicitPolicy(t *testing.T) {
	owner := authn.Principal{SubjectID: "owner", SubjectType: "user", OrgID: "org-a"}
	member := authn.Principal{SubjectID: "member", SubjectType: "user", OrgID: "org-a"}
	outsider := authn.Principal{SubjectID: "outsider", SubjectType: "user", OrgID: "org-b"}

	cases := []struct {
		name      string
		principal authn.Principal
		skill     *Skill
		want      bool
	}{
		{"owner private", owner, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityPrivate}, true},
		{"member internal", member, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityInternal}, true},
		{"outsider internal", outsider, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityInternal}, false},
		{"authenticated public", outsider, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityPublic}, true},
		{"private without grant", member, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityPrivate}, false},
		{"anonymous public", authn.Anonymous(), &Skill{Visibility: SkillVisibilityPublic}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanReadSkillByImplicitPolicy(tc.principal, tc.skill); got != tc.want {
				t.Fatalf("CanReadSkillByImplicitPolicy() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCollapseSkillShareRelationships(t *testing.T) {
	rels := []AuthzRelationship{
		{Resource: AuthzObjectRef{Type: "skill", ID: "demo"}, Relation: "viewer", Subject: AuthzSubjectRef{Type: "user", ID: "reviewer"}},
		{Resource: AuthzObjectRef{Type: "skill", ID: "demo"}, Relation: "reviewer", Subject: AuthzSubjectRef{Type: "user", ID: "reviewer"}},
		{Resource: AuthzObjectRef{Type: "skill", ID: "demo"}, Relation: "editor", Subject: AuthzSubjectRef{Type: "group", ID: "dev", Relation: "member"}},
	}
	shares := CollapseSkillShareRelationships(rels)
	if len(shares) != 2 {
		t.Fatalf("len(shares) = %d, want 2", len(shares))
	}
	if shares[0].Relation != SkillShareRelationReviewer {
		t.Fatalf("reviewer relation collapsed to %q", shares[0].Relation)
	}
}

func TestNormalizeSkillShareRelation(t *testing.T) {
	for _, relation := range []string{SkillShareRelationViewer, SkillShareRelationEditor, SkillShareRelationReviewer} {
		if got, err := NormalizeSkillShareRelation(relation); err != nil || got != relation {
			t.Fatalf("NormalizeSkillShareRelation(%q) = %q, %v", relation, got, err)
		}
	}
	if _, err := NormalizeSkillShareRelation("owner"); err == nil {
		t.Fatal("owner must not be transferable through sharing")
	}
}
