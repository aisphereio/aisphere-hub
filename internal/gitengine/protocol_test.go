package gitengine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

func TestDescribeProtocolRequestMapsOperationsAndPermissions(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		query      string
		body       string
		operation  string
		permission string
	}{
		{name: "fetch", method: http.MethodGet, path: "/git/search.git/info/refs", query: "service=git-upload-pack", operation: "git.fetch", permission: "view"},
		{name: "push", method: http.MethodPost, path: "/git/search.git/git-receive-pack", operation: "git.push", permission: "view"},
		{name: "lfs read", method: http.MethodPost, path: "/git/search.git/info/lfs/objects/batch", body: `{"operation":"download"}`, operation: "git.lfs.read", permission: "view"},
		{name: "lfs write", method: http.MethodPost, path: "/git/search.git/info/lfs/objects/batch", body: `{"operation":"upload"}`, operation: "git.lfs.write", permission: "edit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, "http://hub"+tt.path+"?"+tt.query, bytes.NewBufferString(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			described, err := DescribeProtocolRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			if described.Operation != tt.operation {
				t.Fatalf("operation = %q, want %q", described.Operation, tt.operation)
			}
			check, ok, err := ResolveProtocolAccess(context.Background(), described.Operation, described.Payload)
			if err != nil || !ok {
				t.Fatalf("resolve = (%+v, %v, %v)", check, ok, err)
			}
			if check.Permission != tt.permission || check.Resource.Type != "skill" || check.Resource.ID != "search" {
				t.Fatalf("check = %+v", check)
			}
			if tt.body != "" {
				body, _ := io.ReadAll(described.Request.Body)
				if string(body) != tt.body {
					t.Fatalf("restored body = %q, want %q", body, tt.body)
				}
			}
		})
	}
}
