package gitengine

import (
	"os"
	"strings"
	"testing"
)

func TestRuntimeImageIncludesGitCLI(t *testing.T) {
	contents, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(contents), "FROM alpine:3.22")
	if len(parts) != 2 || !strings.Contains(parts[1], "apk add --no-cache ca-certificates tzdata wget git bash") {
		t.Fatal("runtime image must install git and bash for the embedded repository engine and its authorization hooks")
	}
	if !strings.Contains(string(contents), "ARG GO_VERSION=1.26.4") {
		t.Fatal("builder image must match the Go version required by go.mod")
	}
}
