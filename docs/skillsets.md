# Lightweight SkillSet

`SkillSet` is a lightweight Hub resource that groups canonical Skills.

## Boundary

A SkillSet stores only:

- name, display name, description and visibility;
- owner and organization metadata;
- ordered references to canonical `Skill.name` values.

A SkillSet does **not**:

- pin or publish Skill versions;
- copy Skill packages;
- own runtime configuration;
- change the visibility or permissions of a referenced Skill;
- contain another SkillSet.

Each Skill keeps its own repository, versions, release lifecycle, authorization and runtime. API responses may expose the Skill's current version for display, but that value is dynamically joined from `aihub_skills` and is never persisted in `aihub_skillset_items`.

## Authorization model

A SkillSet is **PostgreSQL-only** by design. It is never modeled as a SpiceDB
resource: no tuple is written on create, no tuple is deleted on remove, and
no `Authz.Check` is performed on read/update/delete/member operations. The
SpiceDB schema (IAM-owned, not present in this repo) has no `skillset`
object type, and Hub must not introduce one.

| Operation | Authorization mechanism | SpiceDB |
|---|---|---|
| Create | `allowCreate` → `Authz.Check` `create_skill` on `zone:{org_id}` | Check only, no write |
| Read (list / get / reverse lookup) | SQL `visibility='public' OR owner_id=? OR (visibility='internal' AND org_id=? )` | none |
| Update / Delete / Bind / Unbind / Update member | SQL `owner_id = principal.SubjectID` (`requireOwnerPrincipal`) | none |

### Why SkillSet stays out of SpiceDB

1. **Reads are fully covered by SQL visibility.** Public = everyone passes
   the `visibility='public'` branch; a SpiceDB `Check` could only return the
   same answer, adding a network hop with zero functional gain.
2. **Owner mutations are atomic with the row they protect.** `owner_id=?`
   lives on the same row as the data; moving it to SpiceDB would create a
   synchronization fault surface (owner transfer, dangling tuples on
   delete) with no benefit.
3. **A SkillSet is a catalog grouping, not an access boundary.** Listing a
   public SkillSet grants no access to the referenced Skills — each Skill
   remains protected by its own `skill:{name}#owner`/`#zone` tuples written
   in `internal/biz/skill_usecase.go`. The actual resource is the Skill.
4. **Mirroring the Skill's SpiceDB pattern onto SkillSet would be
   cargo-cult.** Skill is a first-class protected resource (code/config
   payload); SkillSet is a lightweight index. Copying the tuple lifecycle
   would cost schema changes (IAM-owned), write/delete paths, and
   consistency risk for nothing.

### Why `create_skill` on zone is still required

The create-time Check is not about *visibility* (which governs reads) — it
gates the *privilege* of creating a SkillSet inside an organization.
Without it, any authenticated user could fill another org's catalog. This
check stays regardless of the default visibility.

### Known limitation

The SQL `owner_id` model cannot express "non-owner collaborator with edit
rights." If that capability is ever needed, prefer a local
`aihub_skillset_collaborators` table (atomic with the owner row, narrow
scope) over introducing a SpiceDB object type. There is no such requirement
today — the `ResourceSharePanel` / IAM ResourceGrants path governs sharing
and read access, not collaborative editing.

## Default visibility

A SkillSet is created as **`public`** unless the caller explicitly requests
`internal` or `private` in the `scope` field. The default is enforced at
three layers that all agree:

- `normalizeVisibility("")` returns `"public"` (`internal/server/skillset_http.go`);
- the `aihub_skillsets.visibility` column `DEFAULT 'public'`
  (`migrations/postgres/202607210002_skillset_default_visibility_public.sql`);
- the frontend create dialog initializes its scope selector to `public`
  (`aisphere-hub-front/.../skillset-create-dialog.tsx`).

Rationale: a SkillSet is a discoverable catalog; referenced Skills keep
their own authorization, so a public SkillSet leaks no protected content.
Users who want to restrict discovery can select `private` (owner-only) or
`internal` (same-org) at create time, or change visibility later via the
share panel.

## HTTP API

| Method | Path | Description |
|---|---|---|
| GET | `/v1/skillsets` | List visible sets |
| POST | `/v1/skillsets` | Create a set |
| GET | `/v1/skillsets/{name}` | Get set and ordered members |
| PUT | `/v1/skillsets/{name}` | Update metadata or replace members |
| DELETE | `/v1/skillsets/{name}` | Soft-delete a set |
| POST | `/v1/skillsets/{name}/members` | Add or update one Skill reference |
| PUT | `/v1/skillsets/{name}/members/{skill}` | Update member order |
| DELETE | `/v1/skillsets/{name}/members/{skill}` | Remove a Skill reference |
| GET | `/v1/skills/{skill}/skillsets` | Reverse lookup visible sets |

Example:

```json
{
  "name": "office",
  "displayName": "办公工具",
  "description": "PPT、Excel、Word、PDF 等办公类 Skill",
  "members": [
    { "skillName": "ppt", "order": 0 },
    { "skillName": "excel", "order": 1 },
    { "skillName": "word", "order": 2 },
    { "skillName": "pdf", "order": 3 }
  ]
}
```
