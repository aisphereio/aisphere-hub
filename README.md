# Aisphere Hub

Aisphere Hub is the business service for AIHub capabilities, starting with Skill catalog, Skill versions, package storage, draft workspace, and sharing workflows.

This repository is being migrated to the current Aisphere platform stack:

```text
kernel v0.2.1
  -> generated HTTP/gRPC bindings
  -> generated access / gateway / kernel metadata
  -> aisphere-gateway route registry
  -> aisphere-iam authentication and authorization model
```

The Hub should not become a second IAM. Platform login, token verification, and generic permission APIs belong to `aisphere-iam`. Hub owns AIHub business resources such as `aihub:skill:{name}`.

## Current status

- Uses `github.com/aisphereio/kernel v0.2.1`.
- Uses Kernel code generation tools through `make tools`.
- `buf.gen.yaml` includes `protoc-gen-go-authz`, `protoc-gen-go-gateway`, and `protoc-gen-go-kernel`.
- Kernel access proto definitions are vendored under `api/aisphere/...` so Hub protos can declare `aisphere.access.v1.policy`.
- Existing Skill APIs and storage implementation are preserved during the migration.

See `docs/ai/kernel-gateway-iam-migration.md` for the staged migration plan.

## Layout

```text
api/                  Protobuf APIs and generated HTTP/gRPC/Kernel bindings
cmd/aisphere-hub/      Application entrypoint
configs/              Local config with Kernel module defaults
migrations/postgres/  PostgreSQL schema migrations
internal/conf/         Config DTOs scanned by configx
internal/server/       Kernel HTTP and gRPC server construction
internal/service/      Transport-facing services
internal/biz/          Use cases, domain contracts, errorx errors
internal/data/         Repositories and Kernel resource initialization
docs/ai/              Engineering notes for AI-assisted maintenance
```

## Run locally

```bash
go run ./cmd/aisphere-hub -conf ./configs/config.yaml
```

Default transport ports:

- HTTP: `0.0.0.0:18001`
- gRPC: `0.0.0.0:19001`

## Main Skill endpoints

- `POST /v1/skills`
- `GET /v1/skills`
- `GET /v1/skills/{name}`
- `PUT /v1/skills/{name}`
- `DELETE /v1/skills/{name}`
- `POST /v1/skills:upload`
- `GET /v1/skills/{name}/versions`
- `GET /v1/skills/{name}/versions/{version}`
- `POST /v1/skills/{name}/versions/{version}:submit`
- `POST /v1/skills/{name}/versions/{version}:publish`
- `POST /v1/skills/{name}/versions/{version}:online`
- `POST /v1/skills/{name}/versions/{version}:offline`
- `GET /v1/skills/{name}/versions/{version}/download`
- `GET /v1/skills/{name}/versions/{version}/files`
- `GET /v1/skills/{name}/versions/{version}/file`
- `GET /v1/skills/{name}/shares`
- `POST /v1/skills/{name}/shares`
- `DELETE /v1/skills/{name}/shares/{subject_type}/{subject_id}`

Skill package storage is S3-first and PG-control-plane only. Draft editor writes are persisted as S3 objects plus PG path metadata and can use DTM Saga compensation when `dtm.enabled=true`.

Related design notes:

- `docs/ai/skill-s3-first-dtm.md`
- `docs/ai/skill-draft-workspace-dtm.md`
- `docs/ai/skill-directory-first-storage.md`

## Generate

```bash
make tools
make api
make proto-check
```

Generated contract files should include:

```text
*_authz.pb.go
*_gateway.pb.go
*_kernel.pb.go
```

## Verify

```bash
go mod tidy
go test ./...
```

For full local verification:

```bash
make verify
```

## Development rules

- Do not commit `replace github.com/aisphereio/kernel => ../kernel` to main.
- Do not commit local configs, `.env`, `.exe`, `.bin/`, or `bin/` artifacts.
- Keep route and access policies in proto, not in Gateway hand-written switches.
- Gateway should consume generated Hub route manifests; business authorization remains in Hub/IAM.
