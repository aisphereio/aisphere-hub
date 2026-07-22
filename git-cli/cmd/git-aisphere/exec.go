package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// runGit runs `git <args...>` and returns an error if it fails. It is a thin
// wrapper used by the diagnose command to probe git and the credential helper.
func runGit(args ...string) error {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}
