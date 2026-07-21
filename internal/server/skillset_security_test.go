package server

import (
	"context"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authz"
)

// principalForTest builds an authenticated Principal matching the shape
// gateway_trusted reconstruction would produce from gateway claim headers.
func principalForTest(subjectID, orgID string) authn.Principal {
	return authn.Principal{
		SubjectID:   subjectID,
		SubjectType: authz.SubjectTypeUser,
		OrgID:       orgID,
		AuthMethod:  authn.AuthMethodOIDC,
	}.Normalize()
}

func TestAllowCreateRequiresZoneMember(t *testing.T) {
	h := &skillSetHTTPHandler{resources: &data.Resources{Authz: authz.AllowAllForDevOnly()}}
	ctx := context.Background()
	p := principalForTest("user-1", "zone-1")
	if !h.allowCreate(ctx, p) {
		t.Fatal("AllowAll authorizer + zone-1 principal should permit create")
	}
}

func TestAllowCreateDeniesMissingZone(t *testing.T) {
	h := &skillSetHTTPHandler{resources: &data.Resources{Authz: authz.AllowAllForDevOnly()}}
	ctx := context.Background()
	// No OrgID → SKILLSET_ZONE_REQUIRED contract: allowCreate returns false.
	p := principalForTest("user-1", "")
	if h.allowCreate(ctx, p) {
		t.Fatal("principal without a zone must not be allowed to create")
	}
}

func TestAllowCreateDeniesWhenAuthzDenies(t *testing.T) {
	h := &skillSetHTTPHandler{resources: &data.Resources{Authz: authz.DenyAll()}}
	ctx := context.Background()
	p := principalForTest("user-1", "zone-1")
	if h.allowCreate(ctx, p) {
		t.Fatal("DenyAll authorizer must not permit create even with a zone")
	}
}

func TestAllowCreateDeniesWhenAuthzUnavailable(t *testing.T) {
	// Authz nil → SKILLSET_AUTHZ_UNAVAILABLE contract: allowCreate returns false.
	h := &skillSetHTTPHandler{resources: &data.Resources{Authz: nil}}
	ctx := context.Background()
	p := principalForTest("user-1", "zone-1")
	if h.allowCreate(ctx, p) {
		t.Fatal("nil authorizer must not permit create")
	}
}

func TestPrincipalFromCtxReadsContextPrincipal(t *testing.T) {
	ctx := authn.ContextWithPrincipal(context.Background(), principalForTest("user-1", "zone-1"))
	p, ok := principalFromCtx(ctx)
	if !ok {
		t.Fatal("authenticated principal not recovered from context")
	}
	if p.SubjectID != "user-1" || p.OrgID != "zone-1" {
		t.Fatalf("principal = {%s, %s}, want {user-1, zone-1}", p.SubjectID, p.OrgID)
	}
}

func TestPrincipalFromCtxRejectsAnonymous(t *testing.T) {
	// No principal in context → anonymous, ok=false.
	if _, ok := principalFromCtx(context.Background()); ok {
		t.Fatal("empty context must not authenticate")
	}
}
