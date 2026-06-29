# Skill Directory-First Storage

Status: breaking-change proposal implemented in API module.

## Decision

Skill runtime storage is **directory-first**:

```text
skills/{skill_name}/drafts/{draft_version}/objects/{content_sha256}
skills/{skill_name}/versions/{version}/revisions/{tree_sha256}/files/{path}
skills/{skill_name}/versions/{version}/revisions/{tree_sha256}/manifest.json
```

`package.zip` is no longer the primary persisted representation of a Skill version. A zip file is only:

1. an **import format** uploaded by a user or CI system, parsed once into version files;
2. an **export format** generated on demand when clients call the download endpoint.

This avoids reading and extracting a zip just to show a file tree, read one file, or sync a Skill into a sandbox/file server.

## Why not store only zip?

Zip-only storage makes common operations expensive and awkward:

- UI file tree needs a zip read + unzip or a separately maintained manifest.
- Reading one file requires fetching the whole package.
- Syncing to a sandbox or file server naturally wants a directory layout.
- Updating one file forces rewriting the whole package.
- Failed metadata commits after zip promotion need compensation but cannot help with partial runtime sync.

Directory-first storage makes the S3 prefix itself the canonical runtime layout. The manifest records file metadata and object keys, so UI/runtime/sandbox consumers can list the tree and fetch only the files they need.

## Version immutability

Published versions are immutable snapshots. The object prefix includes the tree hash:

```text
skills/demo/versions/0.1.0/revisions/<tree_sha256>/files/SKILL.md
skills/demo/versions/0.1.0/revisions/<tree_sha256>/files/prompts/main.md
skills/demo/versions/0.1.0/revisions/<tree_sha256>/manifest.json
```

Even when the same semantic version is overwritten during development, the new commit writes a new revision prefix first. If PG metadata commit fails, DTM compensation deletes only the new revision prefix and does not corrupt the previously committed revision.

## PG metadata

PG stores control metadata only:

- skill name, owner, visibility, current version;
- skill version status and author;
- `sha256` = canonical tree hash;
- `size_bytes` = total file bytes;
- `manifest_json` = storage control document with `revision_prefix`, `files_prefix`, `manifest_object_key`, `tree_sha256`.

The file tree source of truth for committed versions is the S3 manifest. Draft workspaces still keep editable path metadata in PG because they are mutable.

## DTM flow for commit/upload

The version save saga is:

```text
1. Stage each file object under skills/_tmp/dtm/{gid}/version/...
2. DTM branch promote_version_directory:
   copy staged objects into the immutable version files prefix
   write manifest.json
3. DTM branch upsert_metadata:
   write skill + skill_version metadata to PG
4. Compensation:
   delete only the new revision prefix / manifest objects
```

Because the final prefix contains `tree_sha256`, compensation is safe for overwrite scenarios.

## Runtime sync

A sandbox/file-server sync should:

1. read skill version metadata from PG;
2. fetch `manifest_object_key` from S3;
3. for each manifest file entry, download `object_key` into local `{path}`;
4. skip entries where `kind=directory`, creating local directories as needed.

No zip extraction is needed for the runtime path.

## Export download

`DownloadSkillPackage` remains available for compatibility. It now builds a zip on demand from the version manifest and file objects. This keeps external UX simple without making zip the canonical storage model.
