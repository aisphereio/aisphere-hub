// Command git-aisphere is the user-facing AISphere Git CLI. Git maps
// `git aisphere <subcommand>` to this binary.
//
// Subcommands:
//
//	login     — Casdoor Authorization Code + PKCE via the system browser,
//	            stores the session in ~/.aisphere/credentials.json
//	logout    — delete the stored session
//	status    — show the current login subject, token expiry, refresh state
//	install   — write the global .gitconfig credential helper block
//	diagnose  — sanity-check git, helper, Casdoor, gateway /git, token
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-git-cli/internal/browser"
	"github.com/aisphereio/aisphere-git-cli/internal/config"
	"github.com/aisphereio/aisphere-git-cli/internal/gitconfig"
	"github.com/aisphereio/aisphere-git-cli/internal/oauth"
	"github.com/aisphereio/aisphere-git-cli/internal/store"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "git-aisphere",
		Short:         "AISphere Git authentication helper",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(loginCmd(), logoutCmd(), statusCmd(), installCmd(), diagnoseCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loginCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in with Casdoor (PKCE) and store the git session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.FromEnv()
			flow := oauth.New(cfg)
			st := store.New(cfg.StoreDir)

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			res, err := flow.Login(ctx, browser.Open)
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			creds := store.Credentials{
				AccessToken:    res.AccessToken,
				AccessTokenExp: res.ExpiresAt,
				RefreshToken:   res.RefreshToken,
				Issuer:         cfg.Issuer,
				ClientID:       cfg.ClientID,
				SubjectID:      res.Claims.SubjectID(),
				SubjectName:    res.Claims.Name,
				DisplayName:    res.Claims.DisplayName,
				GitEndpoint:    cfg.GitEndpoint,
			}
			if err := st.Save(creds); err != nil {
				return fmt.Errorf("store session: %w", err)
			}
			fmt.Printf("Logged in as: %s\n", displaySubject(creds))
			fmt.Printf("Git endpoint: %s\n", cfg.GitEndpoint)
			fmt.Printf("Token expires in: %s\n", humanizeExpiry(res.ExpiresAt))
			fmt.Println("Refresh credential stored securely.")
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "max time to wait for the browser login to complete")
	return cmd
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Delete the stored git session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.FromEnv()
			if err := store.New(cfg.StoreDir).Clear(); err != nil {
				return fmt.Errorf("logout: %w", err)
			}
			fmt.Println("Logged out.")
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current login session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.FromEnv()
			creds, err := store.New(cfg.StoreDir).Load()
			if err != nil {
				return err
			}
			fmt.Printf("Subject:      %s\n", displaySubject(creds))
			fmt.Printf("Issuer:       %s\n", creds.Issuer)
			fmt.Printf("Git endpoint: %s\n", creds.GitEndpoint)
			fmt.Printf("Token expiry: %s (%s)\n", creds.AccessTokenExp.Format(time.RFC3339), humanizeExpiry(creds.AccessTokenExp))
			fmt.Printf("Refreshable:  %v\n", creds.CanRefresh())
			if ok, _ := gitconfig.IsInstalled(cfg.GitEndpoint); ok {
				fmt.Printf("Helper:       installed\n")
			} else {
				fmt.Printf("Helper:       NOT installed (run: git aisphere install)\n")
			}
			return nil
		},
	}
}

func installCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Write the global .gitconfig credential helper block",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.FromEnv()
			if err := gitconfig.Install(cfg.GitEndpoint); err != nil {
				return err
			}
			fmt.Printf("Installed aisphere credential helper for %s\n", cfg.GitEndpoint)
			fmt.Println("You can now run: git clone <endpoint>/<skill>.git")
			return nil
		},
	}
}

func diagnoseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose",
		Short: "Run sanity checks on git, the helper, Casdoor and the gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.FromEnv()
			ok := true
			check := func(name string, err error, detail string) {
				if err != nil {
					ok = false
					fmt.Printf("  [FAIL] %s: %v%s\n", name, err, prefixNonEmpty(": ", detail))
				} else {
					fmt.Printf("  [ OK ] %s%s\n", name, prefixNonEmpty(": ", detail))
				}
			}

			fmt.Println("aisphere-git-cli diagnose (version " + Version + ")")

			fmt.Println("\nGit:")
			check("git authtype capability", checkGitAuthtype(), "")

			fmt.Println("\nCredential helper:")
			check("helper in PATH", checkHelperInPath(), "")

			fmt.Println("\nCasdoor:")
			check("issuer reachable", checkURL(cfg.Issuer+"/.well-known/openid-configuration", 10*time.Second), cfg.Issuer)
			check("JWKS reachable", checkURL(cfg.Issuer+"/.well-known/jwks", 10*time.Second), cfg.Issuer+"/.well-known/jwks")

			fmt.Println("\nLocal session:")
			st := store.New(cfg.StoreDir)
			if creds, err := st.Load(); err != nil {
				check("session present", err, "")
			} else {
				check("session present", nil, displaySubject(creds))
				check("token fresh", errIf(!creds.IsFresh(store.RefreshSkew), "token expired or expiring soon"), humanizeExpiry(creds.AccessTokenExp))
				check("refresh token present", errIf(!creds.CanRefresh(), "no refresh token"), "")
				if claims, err := oauth.ParseTokenClaims(creds.AccessToken); err == nil {
					check("token iss", nil, claims.Issuer)
					check("token aud matches client", errIf(!claims.VerifyAudience(cfg.ClientID), "audience mismatch"), fmt.Sprintf("%v", claims.Audience))
				}
			}

			fmt.Println("\nGateway /git:")
			check("401 Bearer challenge", checkGit401Challenge(cfg.GitEndpoint), "")

			fmt.Println("\n.gitconfig:")
			if installed, _ := gitconfig.IsInstalled(cfg.GitEndpoint); installed {
				check("helper installed", nil, cfg.GitEndpoint)
			} else {
				check("helper installed", fmt.Errorf("not installed"), "run: git aisphere install")
			}

			if ok {
				fmt.Println("\nAll checks passed.")
				return nil
			}
			fmt.Println("\nSome checks failed; see above.")
			os.Exit(1)
			return nil
		},
	}
}

// ---- helpers used by diagnose ----

func displaySubject(c store.Credentials) string {
	if c.DisplayName != "" {
		return c.DisplayName + " (" + c.SubjectID + ")"
	}
	if c.SubjectName != "" {
		return c.SubjectName + " (" + c.SubjectID + ")"
	}
	return c.SubjectID
}

func humanizeExpiry(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Until(t)
	if d < 0 {
		return "expired " + humanizeDuration(-d) + " ago"
	}
	return "in " + humanizeDuration(d)
}

func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func prefixNonEmpty(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

func errIf(cond bool, msg string) error {
	if cond {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func checkGitAuthtype() error {
	// The `authtype`/`credential` capability (Git credential helper v2) ships
	// in Git >= 2.46 (the git-credential manual was first revised for it in
	// 2.46.0, 2024-07). Git 2.43-2.45 have no `capability` operation and
	// cannot send `Authorization: Bearer` via a helper. Since the Gateway
	// only accepts Bearer, this is a hard requirement — do not fall back.
	cmd := exec.Command("git", "version")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git not found: %w", err)
	}
	ver := strings.TrimSpace(string(out))
	major, minor, ok := parseGitMajorMinor(ver)
	if !ok {
		return fmt.Errorf("could not parse git version: %s", ver)
	}
	if major < 2 || (major == 2 && minor < 46) {
		return fmt.Errorf("git %d.%d does not support the authtype credential capability (need >= 2.46); upgrade git", major, minor)
	}
	// `git credential` only accepts fill|approve|reject — there is no
	// `git credential <helper> capability` subcommand, so we cannot probe the
	// helper through git itself here. The authtype advertisement is verified
	// by checkHelperInPath, which runs the helper binary directly.
	return nil
}

// parseGitMajorMinor extracts the major and minor integers from
// "git version 2.47.1.windows.1" -> (2, 47, true).
func parseGitMajorMinor(ver string) (int, int, bool) {
	// take the token after "git version"
	fields := strings.Fields(ver)
	if len(fields) < 3 || fields[1] != "version" {
		return 0, 0, false
	}
	v := fields[2]
	// strip any non-digits after the second dot
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := atoi(parts[0])
	minor, err2 := atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func atoi(s string) (int, error) {
	n := 0
	parsed := false
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		parsed = true
	}
	if !parsed {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	return n, nil
}

func checkHelperInPath() error {
	// `git credential <helper>` is not a valid invocation. Instead, verify the
	// helper binary is discoverable as git-credential-aisphere (the name Git
	// resolves `helper = aisphere` to) by running its capability subcommand.
	helper := exec.Command("git-credential-aisphere", "capability")
	out, err := helper.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git-credential-aisphere not in PATH: %w", err)
	}
	if !strings.Contains(string(out), "capability authtype") {
		return fmt.Errorf("helper did not advertise authtype: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func checkURL(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// checkGit401Challenge verifies the gateway /git endpoint rejects an
// unauthenticated request with 401 (not 302) and ideally a Bearer challenge.
func checkGit401Challenge(gitEndpoint string) error {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // do not follow redirects
		},
	}
	// hit info/refs which is the entry Git uses; missing token should 401.
	req, err := http.NewRequest(http.MethodGet, gitEndpoint+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		return fmt.Errorf("got redirect %d (OIDC still on /git?) instead of 401", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("expected 401, got %d", resp.StatusCode)
	}
	// WWW-Authenticate is desired but not guaranteed by Envoy; warn only.
	if resp.Header.Get("WWW-Authenticate") == "" {
		fmt.Println("  note: 401 had no WWW-Authenticate header (Envoy local reply may need patching)")
	}
	return nil
}
