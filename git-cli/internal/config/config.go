// Package config holds the AISphere Git CLI runtime configuration: the
// Casdoor issuer/client id, the Hub git endpoint, the loopback callback port,
// and the on-disk credential store location.
//
// Defaults are tuned for the production deployment (issuer
// https://casdoor.weagent.cc:30723, git endpoint https://api.weagent.cc:30723/git).
// Every field can be overridden via environment variables (AISPHERE_GIT_*) so
// the same binary works against dev/staging without recompiling.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the resolved CLI configuration.
type Config struct {
	// Issuer is the Casdoor OAuth/OIDC issuer URL (also the JWT `iss`).
	Issuer string
	// ClientID is the aisphere-git-cli Casdoor application client id. This is
	// also the expected JWT `aud` and the audience configured in the Gateway
	// hub-git-security-policy.
	ClientID string
	// GitEndpoint is the Hub git origin prefix, e.g.
	// https://api.weagent.cc:30723/git. The credential helper only ever
	// returns tokens for requests whose host/path matches this endpoint.
	GitEndpoint string
	// CallbackPort is the loopback TCP port the login command listens on
	// for the OAuth redirect. First version uses a fixed port to simplify
	// Casdoor redirect-URI matching; a later version may switch to a random
	// loopback port per RFC 8252.
	CallbackPort int
	// StoreDir is the directory holding credentials.json (created 0700).
	// Defaults to ~/.aisphere.
	StoreDir string
}

// Defaults mirror the production Gateway/Casdoor deployment.
const (
	defaultIssuer      = "https://casdoor.weagent.cc:30723"
	defaultClientID    = "ec15766f6cb98b908433" // Casdoor aisphere-git-cli Public client (no secret)
	defaultGitEndpoint = "https://api.weagent.cc:30723/git"
	defaultCallbackPort = 52731
	defaultStoreSubdir  = ".aisphere"
)

// Default returns the production-tuned configuration.
func Default() Config {
	return Config{
		Issuer:       defaultIssuer,
		ClientID:     defaultClientID,
		GitEndpoint:  defaultGitEndpoint,
		CallbackPort: defaultCallbackPort,
		StoreDir:     storeDirDefault(),
	}
}

// FromEnv returns Default() with every field overridden by the matching
// AISPHERE_GIT_* environment variable when present.
func FromEnv() Config {
	c := Default()
	if v := strings.TrimSpace(os.Getenv("AISPHERE_GIT_ISSUER")); v != "" {
		c.Issuer = v
	}
	if v := strings.TrimSpace(os.Getenv("AISPHERE_GIT_CLIENT_ID")); v != "" {
		c.ClientID = v
	}
	if v := strings.TrimSpace(os.Getenv("AISPHERE_GIT_ENDPOINT")); v != "" {
		c.GitEndpoint = v
	}
	if v := strings.TrimSpace(os.Getenv("AISPHERE_GIT_CALLBACK_PORT")); v != "" {
		if port, err := strconv.Atoi(v); err == nil && port > 0 && port < 65536 {
			c.CallbackPort = port
		}
	}
	if v := strings.TrimSpace(os.Getenv("AISPHERE_GIT_STORE_DIR")); v != "" {
		c.StoreDir = v
	}
	return c
}

// CallbackHostPort is the loopback address the login server binds.
func (c Config) CallbackHostPort() string {
	return fmt.Sprintf("127.0.0.1:%d", c.CallbackPort)
}

// CallbackURI is the redirect_uri sent in the authorize request and validated
// at code exchange. It is always a loopback http URL (RFC 8252 §7.3).
func (c Config) CallbackURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d/callback", c.CallbackPort)
}

// CredentialsPath is the full path to the JSON credential file.
func (c Config) CredentialsPath() string {
	return filepath.Join(c.StoreDir, "credentials.json")
}

func storeDirDefault() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Fall back to a relative dir if HOME is unavailable; never fail to run.
		return defaultStoreSubdir
	}
	return filepath.Join(home, defaultStoreSubdir)
}
