// Package store persists the AISphere Git CLI OAuth session to disk under
// ~/.aisphere/credentials.json (0700). It holds the access token, its expiry,
// the refresh token, and the resolved subject/issuer/client metadata.
//
// Concurrency: git may invoke the credential helper many times in parallel
// (e.g. LFS batch fetches). To avoid a refresh storm — every process
// simultaneously seeing an expiring token and each hitting the Casdoor
// token endpoint — LoadAndRefresh serializes refreshes with a file lock
// (lockfile next to the credentials file). Only the first caller performs
// the refresh; subsequent callers re-read the now-fresh file.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Credentials is the on-disk session representation. Fields are deliberately
// flat JSON so the file is easy to inspect (with a token redacted by status).
type Credentials struct {
	AccessToken      string    `json:"access_token"`
	AccessTokenExp   time.Time `json:"access_token_exp"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	Issuer           string    `json:"issuer"`
	ClientID         string    `json:"client_id"`
	SubjectID        string    `json:"subject_id"`        // Casdoor user UUID (id claim)
	SubjectName      string    `json:"subject_name"`      // username (sub claim), display only
	DisplayName      string    `json:"display_name,omitempty"`
	GitEndpoint      string    `json:"git_endpoint"`
	RefreshedAt      time.Time `json:"refreshed_at,omitempty"`
}

// ErrNoSession is returned when no credentials file exists or it is empty.
var ErrNoSession = errors.New("no aisphere git session; run `git aisphere login`")

// Store reads and writes the credentials file at dir/credentials.json.
type Store struct {
	dir      string
	lockPath string
	credPath string
}

// New returns a Store rooted at dir. The directory is created 0700 on first
// write; reads tolerate a missing directory.
func New(dir string) *Store {
	return &Store{
		dir:      dir,
		credPath: filepath.Join(dir, "credentials.json"),
		lockPath: filepath.Join(dir, "credentials.lock"),
	}
}

// Path returns the credentials file path (used by status/diagnose).
func (s *Store) Path() string { return s.credPath }

// Load reads the current credentials. Returns ErrNoSession when absent.
func (s *Store) Load() (Credentials, error) {
	b, err := os.ReadFile(s.credPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credentials{}, ErrNoSession
		}
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return Credentials{}, ErrNoSession
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, fmt.Errorf("parse credentials: %w", err)
	}
	return c, nil
}

// Save atomically writes credentials, creating the 0700 store dir if needed.
func (s *Store) Save(c Credentials) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, "credentials.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.credPath); err != nil {
		return fmt.Errorf("rename credentials: %w", err)
	}
	return nil
}

// Clear deletes the credentials file (logout).
func (s *Store) Clear() error {
	err := os.Remove(s.credPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// IsFresh reports whether the access token is still valid with the given skew.
// A token within skew of expiry is considered stale and should be refreshed.
func (c Credentials) IsFresh(skew time.Duration) bool {
	if c.AccessToken == "" {
		return false
	}
	if c.AccessTokenExp.IsZero() {
		return false
	}
	return time.Now().Add(skew).Before(c.AccessTokenExp)
}

// CanRefresh reports whether a refresh token is present.
func (c Credentials) CanRefresh() bool {
	return c.RefreshToken != ""
}

// RefreshSkew is how far before expiry a token is considered stale and
// refreshed. 2 minutes matches the kernel casdoor default token leeway.
const RefreshSkew = 2 * time.Minute

// LoadAndRefresh returns fresh credentials, refreshing first if the access
// token is stale. refreshFn must be idempotent-ish: it receives the current
// credentials and returns the refreshed ones (and a non-nil error to abort).
// The whole check-then-refresh is serialized across processes via the file
// lock so concurrent helpers do not each hit the token endpoint.
//
// If refreshFn returns ErrNoSession it is treated as a fatal logout: the
// stored session is cleared and ErrNoSession is returned to the caller so the
// credential helper can prompt `git aisphere login`.
func (s *Store) LoadAndRefresh(refreshFn func(Credentials) (Credentials, error)) (Credentials, error) {
	// Fast path: no lock needed if the token is already fresh. This avoids
	// touching the lock on the common clone/fetch where the token is fine.
	if c, err := s.Load(); err == nil && c.IsFresh(RefreshSkew) {
		return c, nil
	}
	var out Credentials
	err := s.withLock(func() error {
		// Re-read inside the lock: another process may have refreshed already.
		c, err := s.Load()
		if err != nil && !errors.Is(err, ErrNoSession) {
			return err
		}
		if errors.Is(err, ErrNoSession) {
			return ErrNoSession
		}
		if c.IsFresh(RefreshSkew) {
			out = c
			return nil
		}
		if !c.CanRefresh() {
			_ = s.Clear()
			return ErrNoSession
		}
		freshened, err := refreshFn(c)
		if err != nil {
			return err
		}
		if err := s.Save(freshened); err != nil {
			return err
		}
		out = freshened
		return nil
	})
	return out, err
}
