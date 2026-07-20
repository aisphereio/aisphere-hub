package data

import (
	"reflect"
	"testing"

	"github.com/aisphereio/kernel/authz"
)

func TestSkillOwnerRelationships(t *testing.T) {
		rels := skillBootstrapRelationships([]skillOwnerRow{
			{Name: " demo ", OwnerID: " aisphere/user ", OrgID: "org-1"},
			{Name: "demo", OwnerID: "aisphere/user", OrgID: "org-1"},
			{Name: "", OwnerID: "missing-name", OrgID: "org-1"},
			{Name: "missing-owner", OwnerID: "", OrgID: "org-1"},
		})
		// Expect 2 relationships: owner + zone for the deduplicated "demo" row.
		if len(rels) != 2 {
			t.Fatalf("len(rels) = %d, want 2 (owner + zone)", len(rels))
		}
		wantOwner := authz.Relationship{
			Resource: authz.ObjectRef{Type: "skill", ID: "demo"},
			Relation: "owner",
			Subject:  authz.SubjectRef{Type: authz.SubjectTypeUser, ID: "aisphere/user"},
		}
		if !reflect.DeepEqual(rels[0], wantOwner) {
			t.Fatalf("relationship[0] = %#v, want %#v", rels[0], wantOwner)
		}
		wantZone := authz.Relationship{
			Resource: authz.ObjectRef{Type: "skill", ID: "demo"},
			Relation: "zone",
			Subject:  authz.SubjectRef{Type: "zone", ID: "org-1"},
		}
		if !reflect.DeepEqual(rels[1], wantZone) {
			t.Fatalf("relationship[1] = %#v, want %#v", rels[1], wantZone)
		}
	}
