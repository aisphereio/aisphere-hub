# Aisphere Hub

Aisphere Hub is the business service for AIHub capabilities, starting with Skill catalog, Skill versions, package storage, draft workspace, and sharing workflows.

This repository is being migrated to the current Aisphere platform stack:

```text
kernel v0.2.2
  -> generated HTTP/gRPC bindings
  -> generated access / gateway / kernel metadata
  -> aisphere-gateway route registry
  -> aisphere-iam authentication and authorization model
```

The Hub should not become a second IAM. Platform login, token verification, directory lookup, generic permission APIs, organization management, group management, and member management belong to `aisphere-iam`. Hub owns AIHub business resources such as `aihub:skill:{name}`.

## Current status

- Uses `github.com/aisphereio/kernel v0.2.2`.
- Uses Kernel code generation tools through `make tools`.
- `buf.gen.yaml` includes `protoc-gen-go-authz`, `protoc-gen-go-gateway`, and `protoc-gen-go-kernel`.
- Kernel access proto definitions are vendored under `api/aisphere/...` so Hub protos can declare `aisphere.access.v1.policy`.
- Existing Skill APIs and storage implementation are preserved during the migration.
- Skill sharing is modeled as Skill visibility plus IAM/Authz relationships, not as Hub-owned groups.

See `docs/ai/kernel-gateway-iam-migration.md` for the staged migration plan.

## Root Skill catalog model

Hub manages Skills in a global root catalog:

```text
/v1/skills
/v1/skills/{name}
```

There is intentionally no Hub product flow for creating a Skill under a Hub organization, group, or project. A Skill creator becomes the owner from the authenticated principal. Client supplied `owner_id`, `org_id`, and `project_id` are compatibility fields only and must not be used by new UI flows to decide ownership or placement.

Skill access is expressed as:

- `private`: owner/editor/viewer relationships can read.
- `public`: any authenticated user can read.
- subject sharing: users or IAM groups can be granted `viewer` or `editor` through `/v1/skills/{name}/shares`.

Hub never expands IAM groups locally. If a Skill is shared to an IAM group, the relationship targets `group:{id}#member`; Casdoor/IAM remains the source of group membership. See `docs/ai/root-skill-iam-share.md`.

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
go run ./cmd/aisphere-hub -conf ./configs
```

> `-conf` 参数指向配置**目录**（默认值即为 `configs`），`configx` 会读取目录下的 `config.local.yaml` 等文件。不要传 `config.yaml` 文件路径。

Default transport ports:

- HTTP: `0.0.0.0:18001`
- gRPC: `0.0.0.0:19001`

## Main Skill endpoints

- `POST /v1/skills`
- `GET /v1/skills`
- `GET /v1/skills/{name}`
- `PUT /v1/skills/{name}`
- `DELETE /v1/skills/{name}`
- `POST /v1/skills/{name}:visibility`
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
- `GET /v1/skills/{name}/draft/files`
- `GET /v1/skills/{name}/draft/file`
- `PUT /v1/skills/{name}/draft/file`
- `POST /v1/skills/{name}/draft/dir`
- `DELETE /v1/skills/{name}/draft/path`
- `POST /v1/skills/{name}/draft/path:move`
- `POST /v1/skills/{name}/draft:commit`
- `GET /v1/skills/{name}/shares`
- `POST /v1/skills/{name}/shares`
- `DELETE /v1/skills/{name}/shares/{subject_type}/{subject_id}`

Skill package storage is S3-first and PG-control-plane only. Draft editor writes are persisted as S3 objects plus PG path metadata and can use DTM Saga compensation when `dtm.enabled=true`.

Related design notes:

- `docs/ai/skill-s3-first-dtm.md`
- `docs/ai/skill-draft-workspace-dtm.md`
- `docs/ai/skill-directory-first-storage.md`
- `docs/ai/root-skill-iam-share.md`

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
- Hub must not become an IAM directory UI. Skill sharing should call IAM for principal lookup and Skill share endpoints for relationship changes.
