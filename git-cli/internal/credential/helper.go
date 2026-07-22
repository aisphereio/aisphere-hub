package credential

import (
	"fmt"
	"io"
	"strings"

	"github.com/aisphereio/aisphere-git-cli/internal/config"
	"github.com/aisphereio/aisphere-git-cli/internal/store"
)

// Helper implements the four git credential helper subcommands against the
// configured AISphere git endpoint and on-disk session store.
type Helper struct {
	cfg   config.Config
	store *store.Store
	// refresh refreshes a stale access token using the session's refresh
	// token. It returns the refreshed credentials or an error; an
	// invalid_grant-style error should be surfaced as store.ErrNoSession by
	// the caller via refreshToStoreErr.
	refresh func(store.Credentials) (store.Credentials, error)
}

// New returns a Helper. refresh is the token-refresh function (typically
// oauth.Flow.Refresh wrapped to update the stored Credentials).
func New(cfg config.Config, st *store.Store, refresh func(store.Credentials) (store.Credentials, error)) *Helper {
	return &Helper{cfg: cfg, store: st, refresh: refresh}
}

// Capability handles `git-credential-aisphere capability`. It advertises the
// authtype capability so modern Git requests Bearer auth.
func (h *Helper) Capability(out io.Writer) {
	w := NewWriter(out)
	w.WriteVersion(0)
	w.WriteCapability("authtype")
	w.End()
}

// Get handles `git-credential-aisphere get`. It validates the request is for
// the AISphere git endpoint, loads (and refreshes if needed) the access token,
// and emits it as a Bearer credential (or username/password for old Git).
func (h *Helper) Get(in io.Reader, out io.Writer) error {
	req, err := ParseRequest(in)
	if err != nil {
		return fmt.Errorf("parse request: %w", err)
	}
	if !h.isOurEndpoint(req) {
		// Not our endpoint: emit nothing so other helpers can handle it.
		NewWriter(out).End()
		return nil
	}
	creds, err := h.store.LoadAndRefresh(h.refresh)
	if err != nil {
		// Surface a clear instruction to stderr (not stdout) so Git shows it.
		return err
	}
	w := NewWriter(out)
	if req.SupportsAuthtype() {
		w.Write("capability[]", "authtype")
		w.Write("authtype", "Bearer")
		w.Write("credential", creds.AccessToken)
		w.Write("ephemeral", "1")
	} else {
		// Fallback for Git < 2.43: username + password (token as password).
		w.Write("username", fallbackUsername(creds))
		w.Write("password", creds.AccessToken)
	}
	w.End()
	return nil
}

// Store handles `git-credential-aisphere store`. We self-manage the session,
// so we intentionally do NOT persist what Git passes us (which could be a
// short-lived token it observed). No-op keeps the store authoritative.
func (h *Helper) Store(in io.Reader, out io.Writer) {
	// Drain stdin to keep Git happy, emit nothing.
	_, _ = ParseRequest(in)
	NewWriter(out).End()
}

// Erase handles `git-credential-aisphere erase`. Git calls this when the
// server rejects a token. We clear only the (now-invalid) access token but
// keep the refresh token; the next get will attempt a refresh. If the refresh
// is also dead, LoadAndRefresh will clear the whole session itself.
func (h *Helper) Erase(in io.Reader, out io.Writer) {
	_, _ = ParseRequest(in)
	// Deliberately do not wipe the file here: a 401 may be a transient
	// expiry, not a revoked session. The refresh path in LoadAndRefresh
	// decides whether to drop the session.
	NewWriter(out).End()
}

// isOurEndpoint guards the helper so it only ever returns AISphere git tokens
// for the exact configured host and /git path prefix. This prevents leaking
// the git JWT to unrelated hosts (e.g. other api.weagent.cc paths or other
// services) and is enforced in code, not only via .gitconfig.
func (h *Helper) isOurEndpoint(req Request) bool {
	expectedHost, expectedPathPrefix := splitEndpoint(h.cfg.GitEndpoint)
	if expectedHost == "" {
		return false
	}
	if !strings.EqualFold(req.Host, expectedHost) {
		return false
	}
	if req.Path == "" {
		// Git may omit path on info/refs; allow when host matches exactly.
		return true
	}
	return strings.HasPrefix(req.Path, expectedPathPrefix)
}

// splitEndpoint turns "https://api.weagent.cc:30723/git" into
// ("api.weagent.cc:30723", "git/").
func splitEndpoint(endpoint string) (host, pathPrefix string) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", ""
	}
	// strip scheme
	if idx := strings.Index(endpoint, "://"); idx >= 0 {
		endpoint = endpoint[idx+3:]
	}
	// split host / path
	slash := strings.IndexByte(endpoint, '/')
	if slash < 0 {
		return endpoint, ""
	}
	host = endpoint[:slash]
	path := strings.Trim(endpoint[slash:], "/")
	if path == "" {
		return host, ""
	}
	return host, path + "/"
}

// fallbackUsername returns a stable dummy username for the legacy
// username/password fallback. Some Git transports require a non-empty
// username; the Hub does not read it (auth is Bearer), so we use the resolved
// subject to aid audit logs.
func fallbackUsername(creds store.Credentials) string {
	if creds.SubjectName != "" {
		return creds.SubjectName
	}
	if creds.SubjectID != "" {
		return creds.SubjectID
	}
	return "aisphere"
}
