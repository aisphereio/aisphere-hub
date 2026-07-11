# Aisphere Hub — OpenWiki

**Aisphere Hub** is the business service for AIHub capabilities on the Aisphere platform. It manages the Skill catalog — including Skill metadata, versioned packages, draft workspaces, sharing workflows, and access control — as the first real business module migrated onto the Kernel-based platform stack.

## Repository identity

| Attribute | Value |
|-----------|-------|
| Module path | `github.com/aisphereio/aisphere-hub` |
| Language | Go 1.25.8 |
| Framework | [Kernel](https://github.com/aisphereio/kernel) v0.4.1 |
| API style | Protobuf → gRPC + HTTP (gRPC-Gateway) |
| AuthN | Casdoor OIDC / Gateway-trusted headers |
| AuthZ | SpiceDB (ReBAC) or IAM gRPC |
| Storage | PostgreSQL (control plane) + MinIO/S3 (data plane) |

## What Hub does

Hub is the **business service** for AIHub resources. It does **not** manage identity, organizations, groups, or generic permissions — those belong to `aisphere-iam`. Hub owns:

- **Skill** resources (`aihub:skill:{name}`) — the canonical AI skill catalog
- **SkillVersion** lifecycle — draft → submitted → published → online ↔ offline
- **Skill draft workspace** — persistent, autosaved online editor
- **Skill sharing** — visibility (private/internal/public) + subject-specific grants
- **AuthN** — OAuth/OIDC login, token exchange, refresh, revocation, introspection
- **AuthZ** — permission checks, relationship management, resource/subject lookup
- **Audit** — structured audit event recording and querying

## Repository layout

```
api/                  Protobuf APIs and generated HTTP/gRPC/Kernel bindings
cmd/aisphere-hub/      Application entrypoint with Wire DI
configs/              Local config with Kernel module defaults
migrations/postgres/  PostgreSQL schema migrations
internal/
  conf/               Config DTOs scanned by configx
  server/             Kernel HTTP and gRPC server construction
  service/            Transport-facing service layer (DTO conversion)
  biz/                Use cases, domain contracts, errorx errors
  data/               Repositories and Kernel resource initialization
  observability/      Centralized metrics, logging, tracing helpers
  skillzip/           Skill ZIP codec (parse/validate/encode)
deploy/               Kubernetes manifests, Envoy Gateway configs
docs/                 Design docs (AI architecture decisions, authn docs)
tests/                End-to-end authentication tests
```

## Key pages

| Page | What it covers |
|------|----------------|
| [Architecture overview](architecture/overview.md) | System architecture, layering, Kernel framework, deployment topology |
| [Skill domain](domain/skill.md) | Skill CRUD, version lifecycle, draft workspace, S3-first storage, directory-first model |
| [Access control](domain/access-control.md) | Authentication modes, authorization providers, access policy, sharing model |
| [API surface](api/overview.md) | Proto-defined API surface, generated code, authz rules, endpoint reference |
| [Operations & deployment](operations/deployment.md) | Configuration, Docker, K8s, CI/CD, DTM, observability |
| [External integrations](integrations/external-dependencies.md) | Casdoor, SpiceDB, IAM gRPC, MinIO, Redis, PostgreSQL, DTM, Kernel |

## Quick start

```bash
# Install code generation tools
make tools

# Generate API code from protos
make api

# Run proto contract checks
make proto-check

# Run all tests
go test ./... -count=1 -short

# Build the binary
go build ./cmd/aisphere-hub

# Run locally (requires PostgreSQL, Redis, MinIO, Casdoor, SpiceDB)
go run ./cmd/aisphere-hub -conf ./configs
```

Default transport ports:
- HTTP: `0.0.0.0:18001`
- gRPC: `0.0.0.0:19001`
- Metrics: `127.0.0.1:19090`

## Development rules

- **Proto-first**: new RPCs must start with a proto contract. Run `make api && make proto-check` after changes.
- **Kernel contract priority**: if the Kernel generator cannot express a requirement, fix the generator first.
- **Access control**: Hub is a business service, not a gateway proxy. All `AUTHORIZED` endpoints must use `accessx.Guard` and write audit records.
- **Layering**: `service` → DTO conversion only; `biz` → use cases, validation, state machines; `data` → persistence and provider adapters.
- **S3-first**: Skill package content lives in S3; PostgreSQL stores only control-plane metadata.
- **No second IAM**: Hub delegates identity, directory, and group membership to `aisphere-isphere`.

## Agent guidance

When maintaining this codebase:

1. **Start here** — this quickstart page gives the high-level map.
2. **Check AGENTS.md** — the repository has strict agent rules about module paths, Kernel contracts, access control, and layering.
3. **Read the relevant domain page** before modifying business logic.
4. **Run `go test ./... -count=1 -short`** before committing.
5. **Do not hand-edit generated OpenWiki pages** — update source code/docs and let the scheduled workflow regenerate.

## Source map

| Concern | Primary files |
|---------|---------------|
| Entrypoint | `/cmd/aisphere-hub/main.go`, `/cmd/aisphere-hub/wire.go` |
| Config | `/internal/conf/conf.go`, `/configs/config.local.yaml` |
| Server setup | `/internal/server/http.go`, `/internal/server/grpc.go`, `/internal/server/security.go` |
| Service layer | `/internal/service/skill.go`, `/internal/service/authn.go`, `/internal/service/authz.go`, `/internal/service/audit.go` |
| Business logic | `/internal/biz/skill.go`, `/internal/biz/skill_access_policy.go`, `/internal/biz/skill_root_catalog.go`, `/internal/biz/authn.go`, `/internal/biz/authz.go` |
| Data layer | `/internal/data/data.go`, `/internal/data/skill.go`, `/internal/data/skill_s3first.go`, `/internal/data/skill_draft.go`, `/internal/data/authn.go`, `/internal/data/authz.go`, `/internal/data/authz_bootstrap.go` |
| Proto API | `/api/skill/v1/skill.proto`, `/api/authn/v1/authn.proto`, `/api/authz/v1/authz.proto`, `/api/audit/v1/audit.proto` |
| Migrations | `/migrations/postgres/000001_create_aihub_skills.sql`, `000002_skill_s3_first.sql`, `000003_skill_draft_workspace.sql` |
| Design docs | `/docs/ai/skill-access-policy.md`, `/docs/ai/root-skill-iam-share.md`, `/docs/ai/skill-s3-first-dtm.md`, `/docs/ai/skill-draft-workspace-dtm.md`, `/docs/ai/skill-directory-first-storage.md` |
| Deployment | `/deploy/k8s/`, `/deploy/gateway/`, `/Dockerfile` |
| CI/CD | `/.github/workflows/ci.yml`, `/.github/workflows/docker-acr.yml` |