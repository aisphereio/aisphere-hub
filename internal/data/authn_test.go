package data

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/conf"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authn/casdoor"
)

type captureLoginService struct {
	last authn.LoginURLRequest
}

func (s *captureLoginService) BuildLoginURL(_ context.Context, req authn.LoginURLRequest) (authn.LoginURL, error) {
	s.last = req
	return authn.LoginURL{
		URL:         "http://casdoor.example/login?scope=" + req.Scope,
		RedirectURI: req.RedirectURI,
		State:       req.State,
		Scope:       req.Scope,
	}, nil
}

func (s *captureLoginService) HandleCallback(context.Context, authn.CallbackRequest) (authn.CallbackResult, error) {
	return authn.CallbackResult{}, nil
}

func TestAuthnRepoLoginURLDefaultsToOIDCScope(t *testing.T) {
	login := &captureLoginService{}
	repo := NewAuthnRepo(&Resources{LoginService: login}, conf.AuthnConfig{})

	url, err := repo.LoginURL(context.Background(), biz.AuthnLoginURLRequest{
		RedirectURI: "http://localhost:3000/auth/callback",
	})
	if err != nil {
		t.Fatalf("LoginURL returned error: %v", err)
	}

	if login.last.Scope != "openid profile email" {
		t.Fatalf("scope = %q, want openid profile email", login.last.Scope)
	}
	if strings.Contains(url, "scope=read") {
		t.Fatalf("login URL should not contain Casdoor SDK default scope=read: %s", url)
	}
}

func TestAuthnRepoLoginURLPreservesExplicitScope(t *testing.T) {
	login := &captureLoginService{}
	repo := NewAuthnRepo(&Resources{LoginService: login}, conf.AuthnConfig{})

	_, err := repo.LoginURL(context.Background(), biz.AuthnLoginURLRequest{
		RedirectURI: "http://localhost:3000/auth/callback",
		Scope:       "openid profile",
	})
	if err != nil {
		t.Fatalf("LoginURL returned error: %v", err)
	}

	if login.last.Scope != "openid profile" {
		t.Fatalf("scope = %q, want explicit scope to be preserved", login.last.Scope)
	}
}

func TestAuthnRepoLogoutURLBuildsFromCasdoorConfig(t *testing.T) {
	client, err := casdoor.New(casdoorConfigForTest())
	if err != nil {
		t.Fatalf("casdoor.New: %v", err)
	}
	repo := NewAuthnRepo(&Resources{LogoutService: client}, conf.AuthnConfig{})

	rawURL, err := repo.LogoutURL(context.Background(), biz.AuthnLogoutURLRequest{
		PostLogoutRedirectURI: "http://localhost:3000/login",
		IDTokenHint:           "id-token",
		State:                 "app-test",
	})
	if err != nil {
		t.Fatalf("LogoutURL returned error: %v", err)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse logout URL: %v", err)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != "http://casdoor.example/api/logout" {
		t.Fatalf("logout endpoint = %s, want http://casdoor.example/api/logout", parsed.Scheme+"://"+parsed.Host+parsed.Path)
	}
	q := parsed.Query()
	if q.Get("client_id") != "client-id" {
		t.Fatalf("client_id = %q, want client-id", q.Get("client_id"))
	}
	if q.Get("post_logout_redirect_uri") != "http://localhost:3000/login" {
		t.Fatalf("post_logout_redirect_uri = %q", q.Get("post_logout_redirect_uri"))
	}
	if q.Get("id_token_hint") != "id-token" {
		t.Fatalf("id_token_hint = %q", q.Get("id_token_hint"))
	}
	if q.Get("state") != "app-test" {
		t.Fatalf("state = %q", q.Get("state"))
	}
}

func casdoorConfigForTest() casdoor.Config {
	return casdoor.Config{
		Endpoint:         "http://casdoor.example",
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		JWTCertificate:   "cert",
		OrganizationName: "aisphere",
		ApplicationName:  "aisphere-auth",
	}
}
