# Skill Domain

The Skill domain is the core business capability of Aisphere Hub. It manages the full lifecycle of AI skills — from creation through versioned package management, draft editing, sharing, and access control.

## Resource model

```
Skill           /v1/skills/{name}
SkillVersion    /v1/skills/{name}/versions/{version}
SkillFile       /v1/skills/{name}/versions/{version}/files/{path}
SkillShare      /v1/skills/{name}/shares/{subject_type}/{subject_id}
DraftFile       /v1/skills/{name}/draft/files/{path}
```

Skill names match `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$` and are globally unique. Version strings are arbitrary but should follow semver.

## Root catalog model

Skills live in a **global root catalog** — there is no product concept of creating a Skill under an organization, group, or project. Key rules:

- A Skill creator becomes the owner from the authenticated principal.
- Client-supplied `owner_id`, `org_id`, and `project_id` are compatibility fields only. New UI flows must not use them to decide ownership or placement.
- `org_id` is retained as the **governing organization** for access scope, directory selection, audit, and future quota policy.

## Skill CRUD

| Operation | Endpoint | Authz | Description |
|-----------|----------|-------|-------------|
| Create | `POST /v1/skills` | `create` on `aihub:skill:*` | Creates a skill; caller becomes owner |
| Get | `GET /v1/skills/{name}` | `AUTHENTICATED` | Returns skill metadata |
| Update | `PUT /v1/skills/{name}` | `update` on `aihub:skill:{name}` | Updates mutable fields (not owner/visibility/status) |
| Delete | `DELETE /v1/skills/{name}` | `delete` on `aihub:skill:{name}` | Soft-deletes, cascades to versions/files |
| List | `GET /v1/skills` | `AUTHENTICATED` | Lists skills visible to caller |
| Visibility | `POST /v1/skills/{name}:visibility` | `visibility:update` | Changes private/internal/public |

### Ownership

When a Skill is created, Hub stamps the creator as owner from the authenticated principal. After the row is persisted, it writes a SpiceDB relationship:

```
skill:{name}#owner@user:{principal.subject_id}
```

Failure to write the relationship is logged but does **not** roll back the skill row — the `owner_id` field serves as a fallback for read access. This is intentional: authz is a policy layer, not a data invariant.

## Version lifecycle

SkillVersions follow a state machine:

```
draft → submitted → published → online → offline
                                    ↑________|
```

Each transition is an idempotent CAS (compare-and-set on the status column) so concurrent transition calls on the same version cannot both succeed.

| Transition | Endpoint | Authz action | Risk |
|------------|----------|-------------|------|
| Submit | `POST ...:submit` | `submit` on `aihub:skill:{name}:version:{version}` | Medium |
| Publish | `POST ...:publish` | `publish` on `aihub:skill:{name}:version:{version}` | High |
| Online | `POST ...:online` | `online` on `aihub:skill:{name}:version:{version}` | High |
| Offline | `POST ...:offline` | `offline` on `aihub:skill:{name}:version:{version}` | High |

When a version is set online, any previously-online version of the same skill is automatically demoted to published.

## Draft workspace

The draft workspace is a **server-side persistent workspace** for the online skill editor. Every file save is persisted immediately (S3 object + PG metadata), so browser refreshes do not lose work.

### State model

```
Skill
  └── draft version workspace  mutable, autosaved
          ├── directories      PG metadata only
          └── files            S3 object + PG metadata
              ↓ commit
      immutable version package  directory-first S3 layout
              ↓ submit/publish/online
      runtime-visible version
```

### Draft endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /v1/skills/{name}/draft/files` | List draft workspace tree |
| `GET /v1/skills/{name}/draft/file?path=X` | Get one draft file |
| `PUT /v1/skills/{name}/draft/file` | Create or update a draft file |
| `POST /v1/skills/{name}/draft/dir` | Create a directory |
| `DELETE /v1/skills/{name}/draft/path` | Delete a file or directory |
| `POST /v1/skills/{name}/draft/path:move` | Rename or move a path |
| `POST /v1/skills/{name}/draft:commit` | Materialize draft into a SkillVersion |

The commit endpoint can optionally chain submit/publish/online transitions in a single call via boolean flags.

## Storage model

### S3-first (current)

Skill package content is stored in S3 (MinIO). PostgreSQL stores only control-plane metadata.

**Object layout (directory-first):**

```
skills/{skill_name}/drafts/{draft_version}/objects/{content_sha256}
skills/{skill_name}/versions/{version}/revisions/{tree_sha256}/files/{path}
skills/{skill_name}/versions/{version}/revisions/{tree_sha256}/manifest.json
```

- `package.zip` is no longer the primary persisted representation. It is only an import/export format.
- Published versions are immutable snapshots. The object prefix includes the tree hash.
- The manifest records file metadata and object keys for efficient listing and partial fetching.

### DTM Saga consistency

When DTM is enabled, uploads and draft saves use a Saga pattern:

1. Stage objects to a temp S3 prefix
2. DTM branch 1: promote temp objects to final location
3. DTM branch 2: write PG metadata
4. Compensation: delete promoted objects if PG metadata fails

When DTM is disabled, a direct fallback path is used with best-effort compensation.

### PG metadata

PostgreSQL stores only control metadata:

- Skill name, owner, visibility, current version
- Skill version status, author, hashes
- `manifest_json` — storage control document with revision prefix, tree hash, file count
- Draft file path metadata (mutable workspace)

The file tree source of truth for committed versions is the S3 manifest.

## Sharing model

### Visibility levels

| Level | Access | Authn required |
|-------|--------|----------------|
| `private` | Owner + explicit IAM/SpiceDB grants | Yes |
| `internal` | Private + same-org principals | Yes |
| `public` | All authenticated platform principals | Yes |

### Subject sharing

Private Skill sharing is subject-specific through IAM/Authz relationships:

| Relation | Product role | Capabilities |
|----------|-------------|--------------|
| `owner` | Owner | manage, edit, review, publish, view |
| `editor` | Editor | edit, view |
| `reviewer` + `viewer` | Reviewer/Publisher | view, review, publish |
| `viewer` | Viewer/Consumer | view, download |

- Only `skill.manage` may view authorization metadata, change visibility, or create/delete shares.
- Share creation has replacement semantics: a subject has one product role.
- Group grants target `group:{id}#member`; membership remains owned by Casdoor/IAM.
- Hub never expands IAM groups locally.

### Share endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /v1/skills/{name}/shares` | List shares (requires `share:list`) |
| `POST /v1/skills/{name}/shares` | Create share (accepts `viewer`/`editor`/`reviewer`) |
| `DELETE /v1/skills/{name}/shares/{type}/{id}` | Delete all relations for a subject |

## Implicit policy fallback

When SpiceDB is unavailable, Hub falls back to an implicit policy:

- Owner always can read (matched by `owner_id` field)
- Public visibility → all authenticated users
- Internal visibility → same org
- Private → only owner

Write operations (create, update, delete, share, visibility change) remain **fail-closed** — they never fall back.

## Source references

| File | Purpose |
|------|---------|
| `/api/skill/v1/skill.proto` | Proto API definition with authz rules |
| `/internal/biz/skill.go` | Core business logic, domain types, repo interface |
| `/internal/biz/skill_access_policy.go` | Access policy normalization, share logic |
| `/internal/biz/skill_root_catalog.go` | Root catalog operations |
| `/internal/biz/skill_draft.go` | Draft workspace business logic |
| `/internal/data/skill.go` | Skill repo implementation (PG) |
| `/internal/data/skill_s3first.go` | S3-first storage implementation |
| `/internal/data/skill_draft.go` | Draft workspace data layer |
| `/internal/service/skill.go` | Service layer (DTO conversion) |
| `/internal/skillzip/codec.go` | ZIP parse/validate/encode |
| `/migrations/postgres/000001_create_aihub_skills.sql` | Initial schema |
| `/migrations/postgres/000002_skill_s3_first.sql` | S3-first migration |
| `/migrations/postgres/000003_skill_draft_workspace.sql` | Draft workspace schema |
| `/docs/ai/skill-access-policy.md` | Access policy design doc |
| `/docs/ai/root-skill-iam-share.md` | IAM sharing model design |
| `/docs/ai/skill-s3-first-dtm.md` | S3-first + DTM design |
| `/docs/ai/skill-draft-workspace-dtm.md` | Draft workspace design |
| `/docs/ai/skill-directory-first-storage.md` | Directory-first storage design |