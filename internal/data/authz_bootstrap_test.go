package data

import (
	"reflect"
	"testing"

	"github.com/aisphereio/kernel/authz"
)

func TestSkillOwnerRelationships(t *testing.T) {
	rels := skillOwnerRelationships([]skillOwnerRelationshipRow{
		{Name: " demo ", OwnerID: " aisphere/user "},
		{Name: "demo", OwnerID: "aisphere/user"},
		{Name: "", OwnerID: "missing-name"},
		{Name: "missing-owner", OwnerID: ""},
	})
	if len(rels) != 1 {
		t.Fatalf("len(rels) = %d, want 1", len(rels))
	}
	want := authz.Relationship{
		Resource: authz.ObjectRef{Type: "skill", ID: "demo"},
		Relation: "owner",
		Subject:  authz.SubjectRef{Type: authz.SubjectTypeUser, ID: "aisphere/user"},
	}
	if !reflect.DeepEqual(rels[0], want) {
		t.Fatalf("relationship = %#v, want %#v", rels[0], want)
	}
}
