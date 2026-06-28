//go:build e2e

// Package main contains an end-to-end test for the aisphere-hub authn flow.
//
// This test requires a running hub AND a running Casdoor with a test user.
// It is gated behind the "e2e" build tag so it does NOT run by default in
// `go test ./...`. To run it:
//
//	# 1. Start hub (in one terminal):
//	go run ./cmd/aisphere-hub --conf ./configs/config.yaml
//
//	# 2. Start Casdoor (in another terminal) and create a test user
//	#    username: e2e@test.com
//	#    password: <set E2E_PASSWORD env var to the same value>
//
//	# 3. Run the e2e test:
//	E2E_PASSWORD='your-test-password' \
//	E2E_CASDOOR_USER='e2e@test.com' \
//	go test -tags=e2e -v ./examples/ -run TestAuthnE2E
//
// Configuration via environment variables (all required):
//
//	E2E_HUB_URL         default: http://127.0.0.1:8000
//	E2E_CASDOOR_USER    default: e2e@test.com
//	E2E_PASSWORD        (no default — must be set)
//	E2E_REDIRECT_URI    default: http://localhost:3000/callback
//	E2E_SCOPE           default: read
//	E2E_STATE           default: e2e-test
//
// The test does NOT use a real browser. Instead it:
//
//  1. Calls hub /v1/authn/login-url to get the Casdoor authorize URL.
//  2. Parses the URL and reuses its query parameters to POST credentials
//     directly to Casdoor's /api/login endpoint (the same endpoint the
//     Casdoor login form uses). This bypasses the browser/CSRF flow.
//  3. Extracts the `code` parameter from Casdoor's 302 redirect.
//  4. Calls hub /v1/authn/exchange with the code.
//  5. Calls hub /v1/authn/me with the access token.
//  6. Calls hub /v1/authn/introspect.
//  7. Calls hub /v1/authn/refresh.
//  8. Calls hub /v1/authn/revoke.
//  9. Verifies /v1/authn/me returns 401 after revoke.
//
// Note: step 2's /api/login approach depends on Casdoor's API surface
// staying stable. If Casdoor changes its login API, this test will need
// updating. For a more stable alternative, run the PowerShell script
// (authn_e2e.ps1) which uses a real browser.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// --- Test config -------------------------------------------------------------

type e2eConfig struct {
	HubURL      string
	CasdoorUser string
	Password    string
	RedirectURI string
	Scope       string
	State       string
}

func loadConfig(t *testing.T) e2eConfig {
	t.Helper()
	cfg := e2eConfig{
		HubURL:      getenv("E2E_HUB_URL", "http://127.0.0.1:8000"),
		CasdoorUser: getenv("E2E_CASDOOR_USER", "e2e@test.com"),
		Password:    os.Getenv("E2E_PASSWORD"),
		RedirectURI: getenv("E2E_REDIRECT_URI", "http://localhost:3000/callback"),
		Scope:       getenv("E2E_SCOPE", "read"),
		State:       getenv("E2E_STATE", "e2e-test"),
	}
	if cfg.Password == "" {
		t.Skip("E2E_PASSWORD not set; skipping end-to-end test. " +
			"Set it to your Casdoor test user's password to run this test.")
	}
	return cfg
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// --- HTTP helpers ------------------------------------------------------------

type apiClient struct {
	http *http.Client
	hub  string
}

func newAPIClient(hubURL string) *apiClient {
	jar, _ := cookiejar.New(nil)
	return &apiClient{
		http: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
			// We need to inspect 302 responses ourselves for the Casdoor
			// login step, so disable auto-redirect for that specific call.
			// For hub calls we want normal behavior, so we re-enable below.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		hub: hubURL,
	}
}

// do sends a request and returns status code + body. autoFollow controls
// whether 3xx responses are auto-followed.
func (c *apiClient) do(ctx context.Context, method, url string, body any, headers map[string]string, autoFollow bool) (int, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return 0, nil, fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := c.http
	if autoFollow {
		// Use a client that follows redirects for hub API calls.
		client = &http.Client{
			Timeout:   30 * time.Second,
			Jar:       c.http.Jar,
			Transport: c.http.Transport,
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, respBody, nil
}

// --- Hub API wrappers --------------------------------------------------------

type loginURLResponse struct {
	LoginURL string `json:"login_url"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type principal struct {
	SubjectType     string         `json:"subject_type"`
	SubjectID       string         `json:"subject_id"`
	Username        string         `json:"username"`
	Name            string         `json:"name"`
	Email           string         `json:"email"`
	IsAuthenticated bool           `json:"is_authenticated"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	Attributes      map[string]any `json:"attributes,omitempty"`
}

type meResponse struct {
	Principal principal `json:"principal"`
}

type introspectResponse struct {
	Active    bool      `json:"active"`
	Principal principal `json:"principal"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type revokeResponse struct {
	Revoked    bool   `json:"revoked"`
	IDPRevoked bool   `json:"idp_revoked"`
	Message    string `json:"message"`
}

// --- The test ----------------------------------------------------------------

func TestAuthnE2E(t *testing.T) {
	cfg := loadConfig(t)
	ctx := context.Background()
	api := newAPIClient(cfg.HubURL)

	// Step 1: Get login URL.
	t.Log("Step 1: GET /v1/authn/login-url")
	loginURL := buildLoginURL(cfg.HubURL, cfg.RedirectURI, cfg.Scope, cfg.State)
	status, body, err := api.do(ctx, "GET", loginURL, nil, nil, true)
	if err != nil {
		t.Fatalf("Step 1 failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("Step 1 expected 200, got %d: %s", status, body)
	}
	var loginResp loginURLResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		t.Fatalf("Step 1 parse response: %v", err)
	}
	if loginResp.LoginURL == "" {
		t.Fatalf("Step 1: empty login_url in response")
	}
	t.Logf("  login_url: %s", loginResp.LoginURL)

	// Step 2: Drive Casdoor login programmatically.
	t.Log("Step 2: Programmatic Casdoor login to obtain authorization code")
	code, err := driveCasdoorLogin(ctx, loginResp.LoginURL, cfg)
	if err != nil {
		t.Fatalf("Step 2 failed: %v", err)
	}
	t.Logf("  code: %s...", truncate(code, 20))

	// Step 3: Exchange code for tokens.
	t.Log("Step 3: POST /v1/authn/exchange")
	status, body, err = api.do(ctx, "POST", cfg.HubURL+"/v1/authn/exchange", map[string]string{
		"code":         code,
		"redirect_uri": cfg.RedirectURI,
		"state":        cfg.State,
	}, nil, true)
	if err != nil {
		t.Fatalf("Step 3 failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("Step 3 expected 200, got %d: %s", status, body)
	}
	var tokens tokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		t.Fatalf("Step 3 parse response: %v", err)
	}
	if tokens.AccessToken == "" {
		t.Fatalf("Step 3: empty access_token")
	}
	t.Logf("  access_token:  %s...", truncate(tokens.AccessToken, 40))
	t.Logf("  refresh_token: %s...", truncate(tokens.RefreshToken, 40))
	t.Logf("  expires_in:    %ds", tokens.ExpiresIn)

	// Step 4: Call /me with the access token.
	t.Log("Step 4: GET /v1/authn/me")
	status, body, err = api.do(ctx, "GET", cfg.HubURL+"/v1/authn/me", nil, map[string]string{
		"Authorization": "Bearer " + tokens.AccessToken,
	}, true)
	if err != nil {
		t.Fatalf("Step 4 failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("Step 4 expected 200, got %d: %s", status, body)
	}
	var me meResponse
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("Step 4 parse response: %v", err)
	}
	if !me.Principal.IsAuthenticated {
		t.Fatalf("Step 4: principal not authenticated: %+v", me.Principal)
	}
	t.Logf("  subject_id: %s", me.Principal.SubjectID)
	t.Logf("  username:   %s", me.Principal.Username)
	t.Logf("  email:      %s", me.Principal.Email)

	// Step 5: Introspect the token.
	t.Log("Step 5: POST /v1/authn/introspect")
	status, body, err = api.do(ctx, "POST", cfg.HubURL+"/v1/authn/introspect", map[string]string{
		"token":      tokens.AccessToken,
		"token_type": "access_token",
	}, nil, true)
	if err != nil {
		t.Fatalf("Step 5 failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("Step 5 expected 200, got %d: %s", status, body)
	}
	var intro introspectResponse
	if err := json.Unmarshal(body, &intro); err != nil {
		t.Fatalf("Step 5 parse response: %v", err)
	}
	if !intro.Active {
		t.Fatalf("Step 5: introspect returned active=false")
	}
	t.Logf("  active:     %v", intro.Active)
	t.Logf("  scope:      %s", intro.Scope)

	// Step 6: Refresh the token.
	t.Log("Step 6: POST /v1/authn/refresh")
	status, body, err = api.do(ctx, "POST", cfg.HubURL+"/v1/authn/refresh", map[string]string{
		"refresh_token": tokens.RefreshToken,
	}, nil, true)
	if err != nil {
		t.Fatalf("Step 6 failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("Step 6 expected 200, got %d: %s", status, body)
	}
	var refreshed tokenResponse
	if err := json.Unmarshal(body, &refreshed); err != nil {
		t.Fatalf("Step 6 parse response: %v", err)
	}
	if refreshed.AccessToken == "" {
		t.Fatalf("Step 6: empty access_token after refresh")
	}
	t.Logf("  new access_token: %s...", truncate(refreshed.AccessToken, 40))

	// Use the new access token for the revoke step below.
	tokens.AccessToken = refreshed.AccessToken

	// Step 7: Revoke the token.
	t.Log("Step 7: POST /v1/authn/revoke")
	status, body, err = api.do(ctx, "POST", cfg.HubURL+"/v1/authn/revoke", map[string]string{
		"token":      tokens.AccessToken,
		"token_type": "access_token",
	}, nil, true)
	if err != nil {
		t.Fatalf("Step 7 failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("Step 7 expected 200, got %d: %s", status, body)
	}
	var rev revokeResponse
	if err := json.Unmarshal(body, &rev); err != nil {
		t.Fatalf("Step 7 parse response: %v", err)
	}
	t.Logf("  revoked:     %v", rev.Revoked)
	t.Logf("  idp_revoked: %v", rev.IDPRevoked)
	t.Logf("  message:     %s", rev.Message)
	if !rev.Revoked {
		t.Fatalf("Step 7: revoke returned revoked=false")
	}

	// Step 8: Verify /me returns 401 after revoke.
	t.Log("Step 8: GET /v1/authn/me (should return 401 after revoke)")
	status, body, _ = api.do(ctx, "GET", cfg.HubURL+"/v1/authn/me", nil, map[string]string{
		"Authorization": "Bearer " + tokens.AccessToken,
	}, true)
	if status != 401 {
		t.Errorf("Step 8: expected 401 after revoke, got %d: %s", status, body)
	} else {
		t.Logf("  ✓ revoke enforced: /me returned 401")
	}
}

// --- Helpers -----------------------------------------------------------------

// buildLoginURL constructs the GET URL for /v1/authn/login-url with query
// parameters properly URL-encoded.
func buildLoginURL(hub, redirectURI, scope, state string) string {
	q := url.Values{}
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("state", state)
	return hub + "/v1/authn/login-url?" + q.Encode()
}

// driveCasdoorLogin programmatically logs in to Casdoor and extracts the
// authorization code from the 302 redirect's Location header.
//
// It uses a cookie jar to maintain session state across the OAuth flow.
// The flow:
//
//  1. Parse the login_url to extract Casdoor endpoint + oauth query params.
//  2. POST to Casdoor /api/login with username + password + application +
//     organization + provider (the same form fields the Casdoor login page
//     submits). Casdoor's /api/login returns JSON with status="ok" and a
//     `code` field on success — but only if the request came from a
//     browser with a valid CSRF flow. For programmatic use, we instead
//     follow the legacy approach: hit the /login/oauth/authorize endpoint
//     (which 302s to the login page if not authenticated) → POST
//     credentials → follow the 302 to the redirect_uri which contains the
//     code.
//
// This is fragile; see package comment about stability.
func driveCasdoorLogin(ctx context.Context, loginURL string, cfg e2eConfig) (string, error) {
	u, err := url.Parse(loginURL)
	if err != nil {
		return "", fmt.Errorf("parse login_url: %w", err)
	}
	casdoorBase := u.Scheme + "://" + u.Host

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
		// Capture the 302 to redirect_uri — we want its Location header.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Stop when we reach the configured redirect_uri so we can
			// extract the code without actually hitting the frontend
			// (which doesn't exist in this test).
			if strings.HasPrefix(req.URL.String(), cfg.RedirectURI) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	// 1. Hit the authorize endpoint to seed cookies + parse the login form.
	//    Casdoor 302s to /login/{org}/{app} when the session is missing.
	authReq, err := http.NewRequestWithContext(ctx, "GET", loginURL, nil)
	if err != nil {
		return "", err
	}
	authResp, err := client.Do(authReq)
	if err != nil {
		return "", fmt.Errorf("authorize step: %w", err)
	}
	_ = authResp.Body.Close()

	// 2. POST credentials to /api/login.
	//    Casdoor expects application/json with these fields.
	loginPayload := map[string]string{
		"application":  u.Query().Get("client_id"),
		"organization": guessOrganizationFromURL(u),
		"username":     cfg.CasdoorUser,
		"password":     cfg.Password,
		"provider":     "",
		"type":         "login",
	}
	payloadBytes, _ := json.Marshal(loginPayload)
	loginReq, err := http.NewRequestWithContext(ctx, "POST", casdoorBase+"/api/login", bytes.NewReader(payloadBytes))
	if err != nil {
		return "", err
	}
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp, err := client.Do(loginReq)
	if err != nil {
		return "", fmt.Errorf("login step: %w", err)
	}
	defer loginResp.Body.Close()
	loginBody, _ := io.ReadAll(io.LimitReader(loginResp.Body, 1<<20))

	var loginResult struct {
		Status string `json:"status"`
		Msg    string `json:"msg"`
		URL    string `json:"data"` // Casdoor returns the redirect URL here on success
	}
	if err := json.Unmarshal(loginBody, &loginResult); err != nil {
		return "", fmt.Errorf("parse login response: %w (body: %s)", err, loginBody)
	}
	if loginResult.Status != "ok" {
		return "", fmt.Errorf("casdoor login failed: status=%s msg=%s", loginResult.Status, loginResult.Msg)
	}

	// 3. The login response's `data` field contains the redirect URL that
	//    the browser would navigate to. It is the redirect_uri with code &
	//    state as query parameters.
	if loginResult.URL == "" {
		return "", fmt.Errorf("casdoor login returned empty redirect URL")
	}
	redirectURL, err := url.Parse(loginResult.URL)
	if err != nil {
		return "", fmt.Errorf("parse redirect URL: %w", err)
	}
	code := redirectURL.Query().Get("code")
	if code == "" {
		return "", fmt.Errorf("no code in redirect URL: %s", loginResult.URL)
	}
	return code, nil
}

// guessOrganizationFromURL extracts the organization from the Casdoor
// authorize URL's path. Casdoor's URL format is:
//
//	/login/oauth/authorize?client_id=<app>&redirect_uri=...&...
//
// The application's organization is not in the URL itself; we rely on
// the hub config to know it. For the e2e test we extract it from the
// client_id (which equals the application name in our setup) and assume
// the org matches hub config. If your org differs, set E2E_ORG env var.
func guessOrganizationFromURL(u *url.URL) string {
	if org := os.Getenv("E2E_ORG"); org != "" {
		return org
	}
	// Default for aisphere-hub dev setup.
	return "aisphere"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
