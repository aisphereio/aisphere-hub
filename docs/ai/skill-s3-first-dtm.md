# Skill S3-first storage with DTM Saga

## Decision

Skill package content is now S3-first:

- S3/objectstore is the source of truth for package content and the file-tree manifest.
- PostgreSQL stores control-plane metadata only: skill/version rows, object keys, sha256/md5, size, status, visibility, owner, sharing and permission metadata.
- `aihub_skill_files` is kept for backward compatibility with old rows, but new uploads do not insert file content rows.

## Object layout

```text
skills/{skill}/versions/{version}/package.zip
skills/{skill}/versions/{version}/manifest.json
skills/_tmp/dtm/{gid}/package.zip
```

`manifest.json` contains the normalized file tree:

```json
{
  "schema_version": 1,
  "skill_name": "demo",
  "version": "1.0.0",
  "package_object_key": "skills/demo/versions/1.0.0/package.zip",
  "package_sha256": "...",
  "files": [
    {"path":"src/main.go","name":"main.go","type":"src","size":123,"binary":false,"sha256":"..."}
  ]
}
```

The version row keeps a compact control JSON in `manifest_json`:

```json
{
  "storage": "s3",
  "package_object_key": "skills/demo/versions/1.0.0/package.zip",
  "manifest_object_key": "skills/demo/versions/1.0.0/manifest.json",
  "package_sha256": "...",
  "manifest_sha256": "...",
  "file_count": 3,
  "total_file_size": 1024
}
```

## Upload flow

When DTM is enabled:

```text
HTTP UploadSkillPackage
  -> parse zip / validate metadata
  -> PutObject tmp package to S3
  -> DTM Saga submit, wait_result=true
      1. S3 promote action
         copy tmp package -> version package key
         put manifest.json -> version manifest key
      2. PG metadata action
         upsert skill row
         insert version row
         do NOT insert skill file content rows
  -> best-effort delete tmp object
  -> return version metadata
```

Compensation:

- If PG metadata action fails, DTM compensates the S3 promote action and deletes the promoted package/manifest objects.
- If DTM submit fails before completion, the staged tmp object is best-effort deleted by the upload caller.
- Branch handlers are idempotent so DTM retries are safe.

When DTM is disabled, hub still uses S3-first direct mode:

```text
Put package object -> Put manifest object -> PG metadata transaction
```

On PG failure it compensates by deleting the two objects. This direct path is a fallback; production should enable DTM.

## Read flow

- `DownloadSkillPackage`: PG version row -> package object key -> S3 GetObject.
- `ListSkillVersionFiles`: PG version row -> manifest object key -> S3 GetObject manifest.
- `GetSkillVersionFile`: manifest lookup -> package zip object -> extract the requested path.

For pre-S3-first rows without a manifest object key, `ListSkillVersionFiles` and `GetSkillVersionFile` fall back to `aihub_skill_files`.

## DTM branch endpoints

The HTTP server registers internal DTM endpoints when DTM is enabled:

```text
POST /internal/dtm/skill/package/promote
POST /internal/dtm/skill/package/promote_compensate
POST /internal/dtm/skill/metadata/upsert
POST /internal/dtm/skill/metadata/upsert_compensate

# draft workspace DTM branches
POST /internal/dtm/skill/draft/object/promote
POST /internal/dtm/skill/draft/object/promote_compensate
POST /internal/dtm/skill/draft/metadata/upsert
POST /internal/dtm/skill/draft/metadata/upsert_compensate
```

These paths are authn-public because DTM calls them as an internal coordinator. They should be exposed only on a trusted network or behind infrastructure ACLs. Branch callbacks also validate `dtm.branch_secret` through `dtmx.ValidateBranchRequest` when the secret is configured.

See `docs/ai/skill-draft-workspace-dtm.md` for the online editor draft workspace design.

## Config

```yaml
dtm:
  enabled: true
  server: "http://127.0.0.1:36789/api/dtmsvr"
  service_base_url: "http://127.0.0.1:18001"
  branch_prefix: "/internal/dtm"
  timeout_ns: 10000000000
  wait_result: true
  request_timeout: 10
  retry_interval: 5
  timeout_to_fail: 60
  metrics_enabled: true

skill:
  storage:
    max_versions: 5
```

`service_base_url` must be reachable by the DTM server.

## 2026-06 directory-first update

The storage model has been changed from `package.zip`-first to directory-first. New versions are stored under an immutable S3 prefix:

```text
skills/{skill_name}/versions/{version}/revisions/{tree_sha256}/files/{path}
skills/{skill_name}/versions/{version}/revisions/{tree_sha256}/manifest.json
```

The old `package.zip` object is no longer the primary runtime representation. Zip upload is parsed into directory objects; zip download is generated on demand from the manifest.
