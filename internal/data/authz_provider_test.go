package data

import (
	"testing"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/conf"
	iamauthz "github.com/aisphereio/aisphere-iam/client/authzgrpc"
)

func TestNewAuthorizerUsesIAMGRPCProvider(t *testing.T) {
	runtime, closeFn, err := newAuthorizer(conf.AuthzConfig{
		Enabled:  true,
		Provider: "iam_grpc",
		IAMGRPC: iamauthz.Config{
			Endpoint: "127.0.0.1:65535",
			Insecure: true,
			Timeout:  100 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("newAuthorizer returned error: %v", err)
	}
	if runtime == nil || closeFn == nil {
		t.Fatalf("runtime configured=%t close configured=%t", runtime != nil, closeFn != nil)
	}
	_ = closeFn()
}

func TestNewAuthorizerRejectsDirectSpiceDBProvider(t *testing.T) {
	_, _, err := newAuthorizer(conf.AuthzConfig{Enabled: true, Provider: "spicedb"})
	if err == nil {
		t.Fatal("direct SpiceDB provider must be rejected outside IAM")
	}
}
