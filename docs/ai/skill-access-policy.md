# Skill Access Policy

Skill remains a globally named resource in the Hub root catalog and its name is also
the native Git repository name. Creation requires an IAM Project authorization scope:
Hub checks `create_skill` on `project:{org_id}/{project_id}`, while `org_id` must match
the authenticated Principal. The resulting Skill is still authorized as the single
`skill:<name>` resource for metadata and Git operations.

## Visibility

| Value | Discovery and read policy | Authentication |
| --- | --- | --- |
| `private` | owner plus explicit IAM/SpiceDB grants | required |
| `internal` | private policy plus principals whose stable `org_id` equals the Skill governing org | required |
| `public` | every authenticated platform principal | required on current `/v1/skills/*` routes |

`public` is intentionally platform-public in this slice because all current Skill
routes are `AUTHENTICATED`. True anonymous distribution must use separate `PUBLIC`
routes and expose only online versions, never drafts, shares, or owner metadata.

## IAM roles

Hub uses the IAM-owned SpiceDB `skill` definition without maintaining a second
Schema:

| Relation | Product role | Effective capabilities |
| --- | --- | --- |
| `owner` | Owner | manage, edit, review, publish, view |
| `editor` | Editor | edit and view |
| `reviewer` + `viewer` | Reviewer / Publisher | view, review, and publish |
| `viewer` | Viewer / Consumer | view and download |

Only `skill.manage` may view authorization metadata, change visibility, or
create/delete shares. Editors cannot expand access. Share creation has replacement
semantics: a subject has one product role, while Reviewer/Publisher is stored as
`reviewer + viewer` to match the current IAM-owned Schema. Group grants target `group:{id}#member`; membership remains owned by
Casdoor and projected by IAM.

## Existing data migration

Older root Skills may have an empty `org_id` because the previous creation helper
cleared all placement fields. They continue to work as Private/Public resources,
but the backend rejects switching them to Internal until an administrator backfills
the governing organization from the stable owner identity/IAM directory.

## Consistency and failure behavior

- Durable `owner_id`, `org_id`, and visibility are read-only fallbacks.
- Explicit user/group grants are evaluated through IAM-managed SpiceDB data.
- IAM/Authz outages may fall back only to owner/internal/public reads.
- Write, publish, share, visibility, and delete operations remain fail-closed.
- Hub never publishes or overwrites the IAM-owned shared Schema.
