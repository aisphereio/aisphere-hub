# Root Skill + IAM Sharing Model

## Decision

Hub owns Skill resources only. It does **not** own organization, group, member, or department management.

Skills are created in the global Skill root catalog:

```text
/v1/skills
/v1/skills/{name}
```

Skill names remain globally unique and repository names stay identical to Skill names. Creation is nevertheless scoped to an IAM Project: the client must select `org_id` and `project_id`, Hub checks `create_skill` on `project:{org_id}/{project_id}`, and the selected organization must match the authenticated Principal. This scope controls who may create; it does not introduce a second repository or Skill identity.

## Ownership

When a user creates a Skill, Hub stamps the creator as the Skill owner from the authenticated principal.

Client supplied `owner_id` never decides ownership. The creation UI supplies `org_id` and `project_id` only as the authorization scope; Hub derives the owner from the authenticated Principal and rejects cross-organization scope.

```text
skill:{name}#owner@user:{principal.subject_id}
```

The durable `skills.owner_id` column remains the local fallback for ownership reads and bootstrap repair.

## Visibility

Hub supports only two first-class Skill visibility states:

- `private`: only owner/editor/viewer relationships can read.
- `public`: any authenticated user can read.

Public visibility is stored on the Skill row. It is not represented as a Casdoor group and does not require Hub to create a synthetic group.

## Sharing

Private Skill sharing is subject-specific and goes through IAM/Authz relationships.

Hub UI asks IAM for searchable principals:

```text
users
Casdoor/IAM groups
service principals later
```

Hub does **not** manage group lifecycle or group membership. For group shares, Hub writes a relation to `group:{id}#member` through the Skill share endpoint:

```text
skill:{name}#viewer@group:{group_id}#member
skill:{name}#editor@group:{group_id}#member
```

The current backend share endpoint is:

```text
GET    /v1/skills/{name}/shares
POST   /v1/skills/{name}/shares
DELETE /v1/skills/{name}/shares/{subject_type}/{subject_id}
```

`POST /shares` accepts only `viewer` and `editor`. `owner` is not transferable through sharing.

## Responsibility Boundary

```text
Casdoor: identity source, org/group/user membership
IAM: directory adapter, principal search, SpiceDB relationship projection
Hub: Skill metadata, versions, files, visibility, share intent
Git Server: Skill repo/file version protocol
SpiceDB: authorization graph and permission checks
```

## Frontend UX

The Skill list and editor should expose:

1. Create a globally named Skill after selecting an authorized IAM Project.
2. Private/public toggle.
3. Share dialog.
4. IAM principal picker for users and groups.
5. Viewer/editor grant selection.

The frontend should not expose Hub-level group management or organization tree management. It does expose a Project selector for the Principal's current Zone so the `create_skill` check targets a concrete IAM Project.
