# External Integrations

Aisphere Hub integrates with several external systems for identity, authorization, storage, caching, and distributed transactions. This page documents each integration, its purpose, and configuration.

## Kernel framework

**Module**: `github.com/aisphereio/kernel v0.4.1`

Kernel is the foundational framework that provides:

- **configx** — Configuration loading (file + environment)
- **logx** — Structured logging with context injection
- **metricsx** — Prometheus metrics management
- **errorx** — HTTP-mapped error types
- **dbx** — Database connection management (PostgreSQL via GORM)
- **cachex** — Cache abstraction (Redis)
- **objectstorex** — Object storage abstraction (MinIO/S3)
- **dtmx** — Distributed transaction manager (DTM Saga)
- **authn** — Authentication providers (Casdoor OIDC, JWKS, Gateway-trusted)
- **authz** — Authorization providers (SpiceDB, IAM gRPC)
- **accessx** — Access guard middleware
- **auditx** — Audit event recording
- **serverx** — Server construction and service module registration

**Code generation plugins** (from Kernel):

| Plugin | Purpose |
|--------|---------|
| `protoc-gen-go-http` | HTTP handler bindings |
| `protoc-gen-go-errors` | Error code generation |
| `protoc-gen-go-authz` | Authz rule generation |
| `protoc-gen-go-gateway` | Gateway API resource generation |
| `protoc-gen-go-kernel` | Kernel service registration |
| `buf-check-aisphere` | Proto contract checks |

## PostgreSQL

**Purpose**: Control-plane metadata for skills, versions, audit records, authn state.

**Config**:
```yaml
data:
  database:
    enabled: true
    config:
      driver: postgres
      dsn: "postgres://user:password@host:5432/aisphere_hub?sslmode=disable"
      max_open_conns: 20
      max_idle_conns: 10
    migration:
      enabled: true
      engine: goose
      dir: ./migrations/postgres
      table: aihub_schema_migrations
      mode: apply
```

**Migrations** (`/migrations/postgres/`):

| Migration | Description |
|-----------|-------------|
| `000001_create_aihub_skills.sql` | Initial schema: skills, skill_versions, skill_files, audit_logs |
| `000002_skill_s3_first.sql` | Add S3 storage columns (object_key, sha256, manifest) |
| `000003_skill_draft_workspace.sql` | Draft workspace: aihub_skill_draft_files |

**Key tables**: `aihub_skills`, `aihub_skill_versions`, `aihub_skill_files`, `aihub_skill_draft_files`, `audit_logs`

## Redis

**Purpose**: Caching for authn tokens, JWKS, and session data.

**Config**:
```yaml
data:
  cache:
    enabled: true
    config:
      driver: redis
      addrs: ["host:6379"]
      db: 0
      key_prefix: aisphere-hub
```

## MinIO / S3

**Purpose**: Object storage for skill package content, draft files, and manifests.

**Config**:
```yaml
data:
  object_store:
    enabled: true
    config:
      driver: minio
      endpoint: "host:9000"
      use_ssl: false
      bucket: aisphere-hub
      ensure_bucket: true
      presign_expiry_ns: 900000000000
```

**Object layout**:
```
skills/{skill_name}/drafts/{draft_version}/objects/{content_sha256}
skills/{skill_name}/versions/{version}/revisions/{tree_sha256}/files/{path}
skills/{skill_name}/versions/{version}/revisions/{tree_sha256}/manifest.json
skills/_tmp/dtm/{gid}/...
```

## Casdoor

**Purpose**: Identity provider — OIDC login, token issuance, user/org/group management.

**Config**:
```yaml
security:
  authn:
    provider: casdoor
    mode: gateway_trusted  # or casdoor_jwt
    oidc:
      issuer: http://casdoor.aisphere:8000
      discovery_url: http://casdoor.aisphere:8000/.well-known/openid-configuration
      jwks_url: http://casdoor.aisphere:8000/.well-known/jwks
      audience: [bbdcfc272e2b990cb923]
    casdoor:
      endpoint: http://casdoor.aisphere:8000
      organization_name: aisphere
      application_name: aisphere
      client_id: bbdcfc272e2b990cb923
```

**Integration points**:
- OIDC login/logout URLs
- Authorization code exchange
- Token refresh and revocation
- Token introspection
- JWKS-based JWT verification
- User/group directory for skill sharing

## SpiceDB

**Purpose**: Authorization graph (ReBAC) — permission checks, relationship management.

**Config**:
```yaml
security:
  authz:
    enabled: true
    provider: spicedb
    spicedb:
      endpoint: spicedb.aisphere:50051
      token: "keykeykey"
      transport: grpc
      insecure: true
      fully_consistent: true
```

**Note**: Direct SpiceDB provider is **rejected** in production. Production must use `iam_grpc` provider which wraps SpiceDB through the IAM service.

## IAM gRPC

**Purpose**: Production authorization provider — wraps SpiceDB with IAM's directory projection and group membership expansion.

**Config**:
```yaml
security:
  authz:
    enabled: true
    provider: iam_grpc
```

**Dependency**: `github.com/aisphereio/aisphere-iam v0.1.5`

**Integration points**:
- Permission checks (Check, BatchCheck)
- Relationship management (WriteRelationships, DeleteRelationships, ReadRelationships)
- Resource/subject lookup (LookupResources, LookupSubjects)
- Schema management (ReadSchema, WriteSchema)

## DTM (Distributed Transaction Manager)

**Purpose**: Saga-based distributed transactions for coordinating S3 promotion and PostgreSQL metadata writes.

**Config**:
```yaml
dtm:
  enabled: true
  driver: dtm
  protocol: http
  server: "http://dtm-server:36789/api/dtmsvr"
  service_base_url: "http://aisphere-hub.aisphere:18001"
  branch_prefix: "/internal/dtm"
  wait_result: true
  timeout_ns: 10000000000
```

**Dependency**: `github.com/dtm-labs/client v1.16.6`

**Used for**:
- Skill package upload (S3 promotion + PG metadata)
- Draft file save (S3 object promotion + PG metadata)

## Envoy Gateway

**Purpose**: API gateway — TLS termination, OIDC authentication, HTTP/gRPC routing, security policies.

**Integration points**:
- Casdoor OIDC login flow
- JWT validation
- CORS policy
- HTTP/gRPC route management
- Security policies (CORS, JWT, RBAC)

## etcd

**Purpose**: Service discovery and coordination.

**Dependency**: `go.etcd.io/etcd/client/v3 v3.6.12`

## Source references

| File | Purpose |
|------|---------|
| `/go.mod` | All Go dependencies |
| `/internal/data/data.go` | Resource initialization for all integrations |
| `/internal/conf/conf.go` | Config DTOs for all integrations |
| `/configs/config.local.yaml` | Local dev config with all integration endpoints |
| `/deploy/config.yaml` | Production config with all integration endpoints |
| `/deploy/k8s/base/configmap.yaml` | K8s ConfigMap |
| `/deploy/gateway/` | Envoy Gateway configs |