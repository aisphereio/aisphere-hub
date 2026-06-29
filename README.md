# Aisphere Kernel Service

This project was generated from the local Aisphere Kernel layout.

It is a Kernel-first service skeleton. Application code should use Kernel
modules instead of importing infrastructure SDKs directly.

## Included Defaults

- Features: `dbx,cachex,objectstorex,authn,authz,auditx,metricsx,logx,configx`
- DB: `dbx` with `postgres`
- Cache: `cachex` with `redis`
- Object storage: `objectstorex` with `minio`
- Authn: `casdoor`
- Authz: `spicedb`
- Audit: `auditx` memory recorder by default
- Logging: `logx` console output for local development
- Config: `configx` file source
- Transports: Kernel HTTP and gRPC servers
- API modules: authn/authz/audit/skill hub APIs with Kernel HTTP/gRPC bindings

External dependencies are configured in `configs/config.yaml`. For local development,
point Postgres/Redis/MinIO/Casdoor/SpiceDB/DTM at your own environment before running.

## Layout

```text
api/                 Protobuf APIs and generated HTTP/gRPC bindings
cmd/aisphere-hub/     Application entrypoint
configs/             Local config with Kernel module defaults
migrations/postgres/ PostgreSQL schema migrations
internal/conf/        Config DTOs scanned by configx
internal/server/      Kernel HTTP and gRPC server construction
internal/service/     Transport-facing services
internal/biz/         Use cases, domain contracts, errorx errors
internal/data/        Repositories and Kernel resource initialization
docs/ai/             Engineering notes for AI-assisted maintenance
```

## Run

```bash
go run ./cmd/aisphere-hub -conf ./configs/config.yaml
```

Default HTTP endpoints include:

- `GET /healthz`
- `GET /readyz`
- `POST /v1/skills`
- `GET /v1/skills/{name}`
- `POST /v1/skills:upload`
- `GET /v1/skills/{name}/versions/{version}/files`
- `GET /v1/skills/{name}/draft/files?version=0.0.1`
- `PUT /v1/skills/{name}/draft/file`
- `POST /v1/skills/{name}/draft/dir`
- `DELETE /v1/skills/{name}/draft/path`
- `POST /v1/skills/{name}/draft/path:move`
- `POST /v1/skills/{name}/draft:commit`

Skill package storage is S3-first and PG-control-plane only. Draft editor writes are persisted as S3 objects plus PG path metadata and can use DTM Saga compensation when `dtm.enabled=true`. See:

- `docs/ai/skill-s3-first-dtm.md`
- `docs/ai/skill-draft-workspace-dtm.md`

Default transport ports:

- HTTP: `0.0.0.0:18001`
- gRPC: `0.0.0.0:19001`

## Generate

```bash
buf generate --template buf.gen.yaml
```

## Verify

```bash
go test ./...
```

The generated `go.mod` contains a local `replace github.com/aisphereio/kernel => ../kernel`
for development against the latest sibling Kernel module. Remove or adjust it
when publishing the service outside the local repository layout.


## Skill storage

Skill versions use directory-first S3 storage. See `docs/ai/skill-directory-first-storage.md`.
