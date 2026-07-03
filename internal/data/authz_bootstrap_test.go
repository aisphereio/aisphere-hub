package data

import (
	"reflect"
	"testing"

	"github.com/aisphereio/kernel/authz"
)

func TestHasHubAuthzDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   bool
	}{
		{
			name: "kernel base schema only",
			schema: `definition user {}
definition service {}
definition resource {
  relation owner: user | service
}`,
			want: false,
		},
		{
			name:   "hub schema",
			schema: HubAuthzSchema,
			want:   true,
		},
		{
			name: "skill without skill version",
			schema: `definition user {}
definition service {}
definition skill {
  relation owner: user | service
}`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasHubAuthzDefinitions(tt.schema); got != tt.want {
				t.Fatalf("hasHubAuthzDefinitions() = %v, want %v", got, tt.want)
			}
		})
	}
}

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
