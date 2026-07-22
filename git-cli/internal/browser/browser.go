// Package browser opens a URL in the user's default system browser. Native
// OAuth clients must use the system browser (RFC 8252 §4), never an embedded
// WebView. The cross-platform dispatch mirrors kernel/examples/authn-casdoor.
package browser

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// Open opens rawURL in the system browser. It returns immediately after
// launching the browser process (it does not wait for the browser to load).
func Open(rawURL string) error {
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	case "darwin":
		cmd = exec.Command("open", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}
