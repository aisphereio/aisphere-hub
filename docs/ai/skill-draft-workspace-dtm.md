# Skill draft workspace + S3/PG/DTM design

## Conclusion

Keep `draft`, but redefine it as a **server-side persistent workspace**, not a browser-only temporary copy.

This solves the online editor problem:

- A user can create a new skill, create folders, write `SKILL.md`, add source/config files, and refresh the page without losing work.
- Every file save is persisted immediately: file bytes go to S3, path/hash metadata goes to PostgreSQL.
- Commit/publish creates an immutable version snapshot from the draft workspace.

## State model

```text
Skill
  └── draft version workspace  mutable, autosaved
          ├── directories      PG metadata only
          └── files            S3 object + PG metadata
              ↓ commit
      immutable version package  package.zip + manifest.json in S3, control row in PG
              ↓ submit/publish/online
      runtime-visible version
```

Recommended UI behavior:

1. `CreateSkill` creates the skill row and an initial draft-status version row, defaulting to `0.0.1`. For later versions, the first draft file save can create the draft-status version row on demand if the skill exists.
2. The editor calls draft file APIs on each meaningful change.
3. Autosave debounce can be 300–1000ms for text files; binary files should save on explicit upload.
4. `CommitSkillDraft` zips the current draft tree and reuses the existing S3-first package save flow.
5. `submit/publish/online` can be separate buttons, or `CommitSkillDraft` can execute them with flags.

## Storage layout

Draft objects are content-addressed:

```text
skills/{skill}/drafts/{version}/objects/{sha256}
skills/_tmp/dtm/{gid}/draft/{sha256}
```

Committed versions keep the package layout from the S3-first design:

```text
skills/{skill}/versions/{version}/package.zip
skills/{skill}/versions/{version}/manifest.json
```

## PostgreSQL draft metadata

`aihub_skill_draft_files` stores the mutable workspace tree:

| column | purpose |
| --- | --- |
| `skill_name`, `version`, `path` | unique tree identity |
| `kind` | `file` or `directory` |
| `content_type`, `size_bytes`, `binary` | editor/download metadata |
| `sha256`, `object_key` | S3 object reference for files |
| `created_by`, `updated_by` | audit/debug ownership |
| `deleted_at` | soft delete support |

File bodies are not stored in PG.

## DTM flow for draft file save

When DTM is enabled, draft upsert uses Saga:

```text
PUT /v1/skills/{name}/draft/file
  -> validate skill/version is draft
  -> PutObject temp file to S3
  -> DTM Saga wait_result=true
      1. promote_draft_object
         copy temp object -> content-addressed final object
      2. upsert_draft_metadata
         upsert parent directories + file metadata in PG
  -> best-effort delete temp object
  -> return persisted file metadata/content
```

Compensation is conservative:

- If metadata fails after S3 promotion, compensation removes only the object that has no active PG references.
- If a content-addressed object was already referenced by another path/version, it is preserved to avoid data loss.
- The caller deletes the temp object when DTM submit fails before completion.

When DTM is disabled, the fallback path is:

```text
Put final S3 object -> PG transaction -> on PG failure delete the object if it was newly created
```

## HTTP draft endpoints

These are currently registered as manual kernel HTTP routes so the editor can use them before protobuf regeneration.

```text
GET    /v1/skills/{name}/draft/files?version=draft
GET    /v1/skills/{name}/draft/file?version=draft&path=SKILL.md
PUT    /v1/skills/{name}/draft/file
POST   /v1/skills/{name}/draft/dir
DELETE /v1/skills/{name}/draft/path?version=draft&path=src&recursive=true
POST   /v1/skills/{name}/draft/path:move
POST   /v1/skills/{name}/draft:commit
```

Example file save:

```json
{
  "version": "draft",
  "path": "SKILL.md",
  "type": "text/markdown; charset=utf-8",
  "content": "# Demo Skill\n\nDescribe the skill here.\n",
  "binary": false,
  "create_parents": true
}
```

Example directory create:

```json
{
  "version": "draft",
  "path": "prompts"
}
```

Example commit and publish:

```json
{
  "version": "draft",
  "commit_msg": "initial skill draft",
  "submit": true,
  "publish": true,
  "online": false
}
```

## Internal DTM branch endpoints

```text
POST /internal/dtm/skill/draft/object/promote
POST /internal/dtm/skill/draft/object/promote_compensate
POST /internal/dtm/skill/draft/metadata/upsert
POST /internal/dtm/skill/draft/metadata/upsert_compensate
```

All DTM branch handlers validate `dtm.branch_secret` through `dtmx.ValidateBranchRequest`. Keep `/internal/dtm/*` reachable only from DTM/private network.

## Why draft should stay

Do not remove draft. Without a persistent draft layer, the product has only two bad choices:

- save incomplete editor state only in the browser, which loses work on refresh/crash;
- create a new official version for every keystroke or folder change, which pollutes version history and makes rollback/review noisy.

The better model is:

- **draft workspace** = mutable, autosaved, cheap, user-editable;
- **version** = immutable, reviewable, publishable artifact;
- **online** = runtime selected version.
