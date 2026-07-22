// Package credential implements the git credential helper protocol
// (https://git-scm.com/docs/git-credential). Git drives the helper over
// stdin/stdout with newline-separated key=value pairs. The helper supports
// the `capability` negotiation (git >=2.43) so it can return
// `authtype=Bearer` + `credential=<token>`, which Git turns into an
// `Authorization: Bearer <token>` header — no http.extraHeader needed.
package credential

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Request is the parsed stdin input Git sends to get/store/erase. Only the
// fields we act on are strongly typed; the rest is retained in Extra for
// completeness/diagnostics.
type Request struct {
	Protocol  string   // https
	Host      string   // api.weagent.cc:30723
	Path      string   // git/ttt1.git
	Username  string
	Password  string
	WWWAuth   []string // wwwauth[] lines (Git passes the 401 challenge)
	Caps      []string // capability[] lines the caller supports
	Extra     map[string]string
}

// ParseRequest reads the git credential helper KV stream from r. The stream
// is terminated by a blank line.
func ParseRequest(r io.Reader) (Request, error) {
	req := Request{Extra: map[string]string{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "protocol":
			req.Protocol = value
		case "host":
			req.Host = value
		case "path":
			req.Path = value
		case "username":
			req.Username = value
		case "password":
			req.Password = value
		case "wwwauth[]":
			req.WWWAuth = append(req.WWWAuth, value)
		case "capability[]":
			req.Caps = append(req.Caps, value)
		default:
			req.Extra[key] = value
		}
	}
	return req, scanner.Err()
}

// Writer emits KV pairs back to Git. Output is terminated by a blank line.
type Writer struct {
	w io.Writer
}

// NewWriter wraps an io.Writer (typically os.Stdout).
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write emits a single key=value pair.
func (w *Writer) Write(key, value string) {
	fmt.Fprintf(w.w, "%s=%s\n", key, value)
}

// WriteCapability announces a supported capability (negotiation phase).
func (w *Writer) WriteCapability(cap string) {
	fmt.Fprintf(w.w, "capability %s\n", cap)
}

// WriteVersion announces the protocol version.
func (w *Writer) WriteVersion(version int) {
	fmt.Fprintf(w.w, "version %d\n", version)
}

// End terminates the output with a blank line.
func (w *Writer) End() { fmt.Fprintln(w.w) }

// SupportsAuthtype reports whether the caller advertised the authtype
// capability. Old Git versions do not and the helper falls back to emitting
// username/password instead.
func (r Request) SupportsAuthtype() bool {
	for _, c := range r.Caps {
		if c == "authtype" {
			return true
		}
	}
	return false
}

func splitKV(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, '=')
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
}
