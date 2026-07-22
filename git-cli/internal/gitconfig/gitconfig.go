// Package gitconfig writes the global git credential helper configuration
// used by `git aisphere install`. It configures the AISphere git endpoint to
// use the aisphere helper and useHttpPath, and clears inherited helpers so
// the Windows Git Credential Manager (or other helpers) cannot抢先 return a
// username/password for the same URL.
package gitconfig

import (
	"fmt"
	"os/exec"
	"strings"
)

// Install runs `git config --global` commands that:
//   - clear any inherited helper for the AISphere git endpoint
//   - set helper = aisphere
//   - enable useHttpPath so credentials are scoped per-path (only /git/*)
//
// The section key is the raw URL (no %q quoting): `git config` parses the
// trailing `.helper`/`.useHttpPath` as the key and the rest as the section,
// producing `[credential "https://api.weagent.cc:30723/git"]`, which
// `--get-urlmatch` correctly resolves for real /git URLs. (With %q the
// section name gains escaped quotes and never matches — verified.)
func Install(gitEndpoint string) error {
	section := fmt.Sprintf("credential.%s", gitEndpoint)
	cmds := [][]string{
		{"config", "--global", section + ".helper", ""},
		{"config", "--global", section + ".helper", "aisphere"},
		{"config", "--global", section + ".useHttpPath", "true"},
	}
	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// IsInstalled reports whether the aisphere helper is already configured for
// the endpoint (used by status/diagnose).
func IsInstalled(gitEndpoint string) (bool, error) {
	section := fmt.Sprintf("credential.%s.helper", gitEndpoint)
	cmd := exec.Command("git", "config", "--global", "--get-all", section)
	out, err := cmd.Output()
	if err != nil {
		// exit 1 means key not present -> not installed
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) == 0 {
			return false, nil
		}
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "aisphere" {
			return true, nil
		}
	}
	return false, nil
}
