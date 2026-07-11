# Architecture Overview

Aisphere Hub is a **Kernel-based Go service** that follows a clean-architecture / DDD-style layering. It runs as a Kubernetes deployment behind Envoy Gateway, with Casdoor for identity, SpiceDB (or IAM gRPC) for authorization, PostgreSQL for control-plane metadata, and MinIO/S3 for skill package content.

## Layered architecture

```
┌─────────────────────────────────────────────────────┐
│                    Envoy Gateway                      │
│  (OIDC auth, TLS termination, HTTP/gRPC routing)      │
├─────────────────────────────────────────────────────┤
│  ┌─────────────────────────────────────────────────┐ │
│  │              HTTP Server (18001)                  │ │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌────┐ │ │
│  │  │ AuthnSvc │ │ AuthzSvc │ │ AuditSvc │ │Skill│ │ │
│  │  └──────────┘ └──────────┘ └──────────┘ └────┘ │ │
│  │  ┌─────────────────────────────────────────────┐ │ │
│  │  │  gRPC Server (19001)                         │ │ │
│  │  │  Same service set, protobuf-wired            │ │ │
│  │  └─────────────────────────────────────────────┘ │ │
│  └─────────────────────────────────────────────────┘ │
│  ┌─────────────────────────────────────────────────┐ │
│  │  Service layer (internal/service/)               │ │
│  │  DTO conversion, no business rules               │ │
│  └─────────────────────────────────────────────────┘ │
│  ┌─────────────────────────────────────────────────┐ │
│  │  Biz layer (internal/biz/)                       │ │
│  │  Use cases, business rules, state machines,      │ │
│  │  authz checks, audit recording                   │ │
│  └─────────────────────────────────────────────────┘ │
│  ┌─────────────────────────────────────────────────┐ │
│  │  Data layer (internal/data/)                     │ │
│  │  Repos, Kernel provider adapters, DB, S3, DTM   │ │
│  └─────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────┤
│  PostgreSQL  │  MinIO/S3  │  Redis  │  SpiceDB/IAM   │
└─────────────────────────────────────────────────────┘
```

### Layer responsibilities

| Layer | Directory | Responsibility |
|-------|-----------|----------------|
| **Entry point** | `/cmd/aisphere-hub/` | Config loading, resource initialization, Wire DI, server startup |
| **Server** | `/internal/server/` | HTTP/gRPC server construction, middleware (authn, CORS, access), service registration |
| **Service** | `/internal/service/` | DTO conversion between proto messages and biz domain types. No business rules, no direct DB/S3 access |
| **Biz** | `/internal/biz/` | Use case orchestration, business validation, state machines, authz checks, audit events |
| **Data** | `/internal/data/` | Repository implementations, Kernel provider adapters (DB, Cache, ObjectStore, Authn, Authz, DTM) |
| **Observability** | `/internal/observability/` | Centralized metrics, structured logging, operation timing helpers |

## Kernel framework

Hub is built on the **Kernel** framework (`github.com/aisphereio/kernel v0.4.1`), which provides:

- **configx** — Configuration loading from files and environment variables
- **logx** — Structured logging with context injection
- **metricsx** — Prometheus metrics management
- **errorx** — HTTP-mapped error types with codes
- **dbx** — Database connection management (PostgreSQL via GORM)
- **cachex** — Cache abstraction (Redis)
- **objectstorex** — Object storage abstraction (MinIO/S3)
- **dtmx** — Distributed transaction manager (DTM Saga)
- **authn** — Authentication providers (Casdoor OIDC, JWKS, Gateway-trusted)
- **authz** — Authorization providers (SpiceDB, IAM gRPC)
- **accessx** — Access guard middleware
- **auditx** — Audit event recording
- **serverx** — Server construction and service module registration

## Code generation pipeline

Protobuf definitions in `/api/` are processed by `buf` with custom Kernel protoc plugins:

```
buf.gen.yaml plugins:
  - protoc-gen-go              → Go message types
  - protoc-gen-go-grpc        → gRPC server/client stubs
  - protoc-gen-go-http        → HTTP handler bindings
  - protoc-gen-grpc-gateway   → gRPC-Gateway reverse proxy
  - protoc-gen-go-errors      → Error code generation
  - protoc-gen-go-authz       → Authz rule generation
  - protoc-gen-go-gateway     → Gateway API resource generation
  - protoc-gen-go-kernel      → Kernel service registration
  - protoc-gen-openapiv2      → OpenAPI spec generation
```

Generated files follow the pattern `*_authz.pb.go`, `*_gateway.pb.go`, `*_kernel.pb.go`.

## Deployment topology

Hub is deployed as two container images:

| Image | Ports | Description |
|-------|-------|-------------|
| `aisphere-hub` | HTTP 18001, gRPC 19001, Metrics 19090 | Backend Go service |
| `aisphere-hub-frontend` | HTTP 3000 | Next.js standalone server |

The frontend and backend share a single HTTP domain. Envoy Gateway handles:

- TLS termination
- Casdoor OIDC login flow
- HTTP/gRPC routing
- Security policies (CORS, JWT validation)

See [Operations & deployment](operations/deployment.md) for full deployment details.

## Service modules

Hub registers three service modules (defined in `/internal/server/modules.go`):

1. **`authnv1`** — Authentication (login, logout, exchange, refresh, revoke, introspect)
2. **`auditv1`** — Audit record querying
3. **`skillv1`** — Skill CRUD, version lifecycle, draft workspace, sharing

The authz control-plane service (schema management, relationship CRUD) is intentionally **not** exposed as a Hub module — it is delegated to IAM gRPC.

## Key architectural decisions

1. **Root catalog model** — Skills live in a global root catalog (`/v1/skills`), not under organizations or projects. Organization is a governance attribute, not a placement path.
2. **S3-first storage** — Skill package content is stored in S3 (MinIO). PostgreSQL holds only control-plane metadata. Zip files are import/export artifacts only.
3. **DTM Saga for consistency** — Distributed transactions (DTM Saga) coordinate S3 promotion and PostgreSQL metadata writes for uploads and draft saves.
4. **Authz delegation** — Hub does not own the SpiceDB schema. IAM owns the `skill` definition. Hub only writes relationships (owner, viewer, editor) and reads permissions.
5. **No local token issuance** — All token operations are delegated to Casdoor/IAM. Hub does not issue local access tokens.
6. **Two authn modes** — `casdoor_jwt` (backend JWT verification) for development; `gateway_trusted` (trusted headers from Envoy) for production.

## Source references

- Entry point: `/cmd/aisphere-hub/main.go`
- Resource initialization: `/internal/data/data.go`
- Server construction: `/internal/server/http.go`, `/internal/server/grpc.go`
- Module registration: `/internal/server/module.go`
- Config DTOs: `/internal/conf/conf.go`
- Code generation: `/buf.gen.yaml`, `/buf.yaml`
- Makefile targets: `/Makefile`