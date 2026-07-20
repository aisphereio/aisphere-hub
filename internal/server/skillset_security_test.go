package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authz"
)

func TestSkillSetSecurityUsesContextPrincipal(t *testing.T) {
	h := &skillSetHTTPHandler{resources: &data.Resources{Authz: authz.AllowAllForDevOnly()}}
	req := httptest.NewRequest(http.MethodGet, "/v1/skillsets", nil)
	req.Header.Set("X-Aisphere-Principal", "spoofed-user")
	req.Header.Set("X-Aisphere-Org", "spoofed-zone")
	req = req.WithContext(authn.ContextWithPrincipal(req.Context(), authn.Principal{
		SubjectID:   "user-1",
		SubjectType: authz.SubjectTypeUser,
		OrgID:       "zone-1",
		AuthMethod:  authn.AuthMethodJWT,
	}))

	called := false
	handler := h.withSkillSetSecurity(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal, org := requestIdentity(r)
		if principal != "user-1" {
			t.Fatalf("principal = %q, want user-1", principal)
		}
		if org != "zone-1" {
			t.Fatalf("org = %q, want zone-1", org)
		}
	})
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("wrapped handler was not called")
	}
}

func TestSkillSetSecurityRejectsAnonymousCreate(t *testing.T) {
	h := &skillSetHTTPHandler{resources: &data.Resources{Authz: authz.AllowAllForDevOnly()}}
	req := httptest.NewRequest(http.MethodPost, "/v1/skillsets", nil)
	req.Header.Set("X-Aisphere-Principal", "spoofed-user")
	req.Header.Set("X-Aisphere-Org", "spoofed-zone")
	recorder := httptest.NewRecorder()

	h.withSkillSetSecurity(func(http.ResponseWriter, *http.Request) {
		t.Fatal("anonymous create reached handler")
	}).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestSkillSetSecurityAllowsZoneMemberCapability(t *testing.T) {
	h := &skillSetHTTPHandler{resources: &data.Resources{Authz: authz.AllowAllForDevOnly()}}
	req := httptest.NewRequest(http.MethodPost, "/v1/skillsets", nil)
	req = req.WithContext(authn.ContextWithPrincipal(req.Context(), authn.Principal{
		SubjectID:   "user-1",
		SubjectType: authz.SubjectTypeUser,
		OrgID:       "zone-1",
		AuthMethod:  authn.AuthMethodJWT,
	}))

	called := false
	h.withSkillSetSecurity(func(http.ResponseWriter, *http.Request) {
		called = true
	}).ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("authorized Zone member did not reach handler")
	}
}

func TestSkillSetSecurityDeniesMissingZoneCapability(t *testing.T) {
	h := &skillSetHTTPHandler{resources: &data.Resources{Authz: authz.DenyAll()}}
	req := httptest.NewRequest(http.MethodPost, "/v1/skillsets", nil)
	req = req.WithContext(authn.ContextWithPrincipal(req.Context(), authn.Principal{
		SubjectID:   "user-1",
		SubjectType: authz.SubjectTypeUser,
		OrgID:       "zone-1",
		AuthMethod:  authn.AuthMethodJWT,
	}))
	recorder := httptest.NewRecorder()

	h.withSkillSetSecurity(func(http.ResponseWriter, *http.Request) {
		t.Fatal("denied principal reached handler")
	}).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}
