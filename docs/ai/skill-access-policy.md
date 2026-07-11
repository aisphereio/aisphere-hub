# Skill Access Policy

Skill remains a globally named resource in the Hub root catalog. It is not placed
under a Hub organization, group, or project. `org_id` is retained as the governing
IAM/Casdoor organization for access scope, directory selection, audit, and future
quota policy.

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
| `reviewer` | Reviewer / Publisher | review and publish |
| `viewer` | Viewer / Consumer | view and download |

Only `skill.manage` may change visibility or create/delete shares. Editors cannot
expand access. Group grants target `group:{id}#member`; membership remains owned by
Casdoor and projected by IAM.

## Consistency and failure behavior

- Durable `owner_id`, `org_id`, and visibility are read-only fallbacks.
- Explicit user/group grants are evaluated through IAM-managed SpiceDB data.
- IAM/Authz outages may fall back only to owner/internal/public reads.
- Write, publish, share, visibility, and delete operations remain fail-closed.
- Hub never publishes or overwrites the IAM-owned shared Schema.
