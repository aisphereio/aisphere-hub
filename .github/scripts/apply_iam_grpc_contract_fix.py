#!/usr/bin/env python3
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
IAM_VERSION = "v0.1.5-0.20260711142728-3322f35cfef0"


def read(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def write(path: str, content: str) -> None:
    (ROOT / path).write_text(content, encoding="utf-8")


def replace_once(path: str, old: str, new: str) -> None:
    content = read(path)
    if old not in content:
        raise RuntimeError(f"expected text not found in {path}: {old[:120]!r}")
    write(path, content.replace(old, new, 1))


def regex_once(path: str, pattern: str, replacement: str, flags: int = 0) -> None:
    content = read(path)
    updated, count = re.subn(pattern, replacement, content, count=1, flags=flags)
    if count != 1:
        raise RuntimeError(f"expected one regex match in {path}, got {count}: {pattern[:120]!r}")
    write(path, updated)


# Pin the IAM client commit that fixes compatibility with the currently released
# Kernel v0.4.1. go mod tidy below normalizes direct/indirect placement and sums.
go_mod = read("go.mod")
go_mod = re.sub(r"\n\tgithub\.com/aisphereio/aisphere-iam\s+[^\n]+(?: // indirect)?", "", go_mod)
go_mod = go_mod.replace(
    "require (\n\tgithub.com/aisphereio/kernel v0.4.1",
    f"require (\n\tgithub.com/aisphereio/aisphere-iam {IAM_VERSION}\n\tgithub.com/aisphereio/kernel v0.4.1",
    1,
)
write("go.mod", go_mod)

# Runtime authorization is deliberately smaller than schema administration.
write(
    "internal/data/authz_runtime.go",
    '''package data

import (
    iamauthz "github.com/aisphereio/aisphere-iam/client/authzgrpc"
    "github.com/aisphereio/kernel/authz"
)

// runtimeAuthzService is the data-plane authorization surface Hub is allowed
// to use through IAM. Schema publication remains an IAM control-plane concern.
type runtimeAuthzService interface {
    authz.Authorizer
    authz.BatchAuthorizer
    authz.ResourceLookup
    authz.SubjectLookup
    authz.RelationshipStore
}

var _ runtimeAuthzService = (*iamauthz.Client)(nil)
''',
)

# Resources and provider initialization must retain the runtime client directly,
# not type-assert it to authz.Service (which also requires SchemaManager).
replace_once("internal/data/data.go", '\t"github.com/aisphereio/kernel/authz/spicedb"\n', "")
replace_once("internal/data/data.go", "\tAuthzService authz.Service\n", "\tAuthzService runtimeAuthzService\n")
regex_once(
    "internal/data/data.go",
    r"\t\tr\.Authz = authorizer\n\t\t// If the authorizer also implements authz\.Service.*?\n\t\tif svc, ok := authorizer\.\(authz\.Service\); ok \{\n\t\t\tr\.AuthzService = svc\n\t\t\}\n",
    "\t\tr.Authz = authorizer\n\t\tr.AuthzService = authorizer\n",
    re.S,
)
regex_once(
    "internal/data/data.go",
    r"func newAuthorizer\(cfg conf\.AuthzConfig\) \(authz\.Authorizer, func\(\) error, error\) \{.*?\n\}\n\nfunc pingEnabled",
    '''func newAuthorizer(cfg conf.AuthzConfig) (runtimeAuthzService, func() error, error) {
    switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
    case "", "iam_grpc":
        client, err := iamauthz.New(cfg.IAMGRPC)
        if err != nil {
            return nil, nil, err
        }
        return client, client.Close, nil
    case "spicedb":
        return nil, nil, errorx.BadRequest(
            errorx.Code("AUTHZ_DIRECT_SPICEDB_FORBIDDEN"),
            "Hub must use security.authz.provider=iam_grpc; IAM owns SpiceDB access and schema",
        )
    default:
        return nil, nil, errorx.BadRequest(errorx.Code("AUTHZ_UNSUPPORTED_PROVIDER"), "unsupported authz provider: "+cfg.Provider)
    }
}

func pingEnabled''',
    re.S,
)

# AuthzRepo uses runtime capabilities. Schema operations are intentionally
# rejected rather than silently tunnelling control-plane access through Hub.
replace_once(
    "internal/data/authz.go",
    "func (r *authzRepo) service() (authz.Service, error) {",
    "func (r *authzRepo) service() (runtimeAuthzService, error) {",
)
regex_once(
    "internal/data/authz.go",
    r"func \(r \*authzRepo\) ReadSchema\(ctx context\.Context\).*?\n\}\n\nfunc \(r \*authzRepo\) WriteSchema\(ctx context\.Context, schema biz\.AuthzSchema\).*?\n\}\n",
    '''func (r *authzRepo) ReadSchema(context.Context) (biz.AuthzSchema, error) {
    return biz.AuthzSchema{}, errorx.BadRequest(
        errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY"),
        "authorization schema is owned and managed by IAM",
    )
}

func (r *authzRepo) WriteSchema(context.Context, biz.AuthzSchema) error {
    return errorx.BadRequest(
        errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY"),
        "authorization schema is owned and managed by IAM",
    )
}
''',
    re.S,
)

# Keep the exported bootstrap helper as a safe compatibility no-op, and remove
# the old Hub-owned schema fragment and tests.
regex_once(
    "internal/data/authz_bootstrap.go",
    r"// HubAuthzSchemaVersion.*?func hasHubAuthzDefinitions\(schema string\) bool \{.*?\n\}\n",
    '''// BootstrapAuthzSchema is retained for source compatibility. Hub never
// reads, validates, or publishes the shared SpiceDB schema; IAM owns that
// control-plane responsibility.
func BootstrapAuthzSchema(ctx context.Context, _ *Resources, log logx.Logger) error {
    if log != nil {
        log.WithContext(ctx).Info("authz schema bootstrap skipped: schema is owned by IAM")
    }
    return nil
}
''',
    re.S,
)
regex_once(
    "internal/data/authz_bootstrap_test.go",
    r"func TestHasHubAuthzDefinitions\(t \*testing\.T\) \{.*?\n\}\n\n",
    "",
    re.S,
)

# Startup only repairs durable owner relationships through IAM. It never tries
# to bootstrap or replace the authorization schema.
regex_once(
    "cmd/aisphere-hub/main.go",
    r"\n\t// Bootstrap SpiceDB schema on startup\..*?\n\t\}\n\n\tif err := data\.BootstrapAuthzRelationships",
    "\n\t// Repair durable owner relationships through IAM's runtime authorization API.\n\tif err := data.BootstrapAuthzRelationships",
    re.S,
)

# Production and Kubernetes configurations must use IAM, not SpiceDB directly.
regex_once(
    "deploy/config.yaml",
    r"  authz:\n    enabled: true\n    provider: spicedb\n    dev_allow_all: false\n    spicedb:\n(?:      .*\n)+?\n(?=gateway:)",
    '''  authz:
    enabled: true
    provider: iam_grpc
    dev_allow_all: false
    iam_grpc:
      endpoint: "aisphere-iam.aisphere.svc.cluster.local:19080"
      caller_service: aisphere-hub
      insecure: true
      timeout: 5000000000
      retry_max: 3
      metrics_enabled: true

''',
)
regex_once(
    "deploy/k8s/base/configmap.yaml",
    r"      authz:\n        enabled: true\n        provider: spicedb\n        dev_allow_all: false\n        spicedb:\n(?:          .*\n)+?\n(?=    audit:)",
    '''      authz:
        enabled: true
        provider: iam_grpc
        dev_allow_all: false
        iam_grpc:
          endpoint: "${IAM_GRPC_ENDPOINT}"
          caller_service: aisphere-hub
          insecure: true
          timeout: 5000000000
          retry_max: 3
          metrics_enabled: true

''',
)
replace_once(
    "deploy/k8s/base/configmap.yaml",
    "  CASDOOR_OWNER: aisphere\n  SPICEDB_ENDPOINT: spicedb.aisphere.svc.cluster.local:50051\n",
    "  CASDOOR_OWNER: aisphere\n  IAM_GRPC_ENDPOINT: aisphere-iam.aisphere.svc.cluster.local:19080\n",
)

# Remove the accidentally committed worktree gitlink from the merged feature PR.
subprocess.run(
    ["git", "rm", "-f", "--ignore-unmatch", ".worktrees/hub-gitlab-implementation"],
    cwd=ROOT,
    check=True,
)

# Add focused contract tests and architecture documentation.
write(
    "internal/data/authz_runtime_test.go",
    '''package data

import (
    "context"
    "testing"

    "github.com/aisphereio/aisphere-hub/internal/biz"
    "github.com/aisphereio/kernel/errorx"
)

func TestAuthzRepoRejectsSchemaAdministration(t *testing.T) {
    repo := NewAuthzRepo(&Resources{})
    if _, err := repo.ReadSchema(context.Background()); errorx.CodeOf(err) != errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY") {
        t.Fatalf("ReadSchema error = %v", err)
    }
    if err := repo.WriteSchema(context.Background(), biz.AuthzSchema{Text: "definition user {}"}); errorx.CodeOf(err) != errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY") {
        t.Fatalf("WriteSchema error = %v", err)
    }
}
''',
)
write(
    "docs/ai/iam-grpc-authz-boundary.md",
    '''# Hub authorization boundary

Hub is an IAM authorization **data-plane client**.

It may call IAM gRPC for:

- permission checks and batch checks;
- resource and subject lookup;
- Skill owner/share relationship writes and reads.

Hub must not:

- connect to SpiceDB directly in production;
- read, validate, publish, or replace the shared authorization schema;
- expose IAM's authorization control-plane service from the Hub API.

Use:

```yaml
security:
  authz:
    enabled: true
    provider: iam_grpc
    iam_grpc:
      endpoint: aisphere-iam.aisphere.svc.cluster.local:19080
      caller_service: aisphere-hub
```

IAM owns the SpiceDB schema and the authorization control plane. Kernel defines
provider-neutral runtime interfaces so Hub business code remains independent of
the IAM protobuf transport.
''',
)

print("IAM gRPC authorization contract repair applied")
