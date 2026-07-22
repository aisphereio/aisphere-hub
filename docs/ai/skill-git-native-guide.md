# Skill Git-Native Usage Guide

This guide explains how a Skill owner and collaborators create, share, and
edit Skills through native Git, authenticated by their own Casdoor identity.
It is the user-facing companion to [skill-access-policy.md](skill-access-policy.md)
(which covers the authorization model) and the
[aisphere-git-cli README](https://github.com/aisphereio/aisphere-hub/blob/main/git-cli/README.md)
(which covers the CLI install).

## Mental model

A Skill **is** a Git repository. The repository is the single source of truth
for the Skill's identity and content metadata:

- The **repository name** is the canonical Skill identity (`skill:<name>`).
- **`SKILL.md`** at the repository root is the only seeded file. Its YAML
  front-matter (`name` / `description` / `version`) is the source of truth for
  display metadata; there is no separate `skill.yaml` manifest.
- Name and description are **not editable through the REST API** — edit
  `SKILL.md` and push. The API rejects direct name/description writes with
  `SKILL_METADATA_MANAGED_BY_GIT`.

```
Hub REST API (/v1/skills/*)   →  create / list / delete / visibility / share
Git (/git/<name>.git)         →  clone / fetch / push content (SKILL.md + files)
```

The two paths use **different tokens** (different Casdoor client audiences):

| Path | Token audience | How you get it |
| --- | --- | --- |
| `/v1/skills/*` (REST) | web client `bbdcfc272e2b990cb923` | browser OIDC session via Envoy Gateway |
| `/git/*` (Git) | git-cli client `ec15766f6cb98b908433` | `git aisphere login` (PKCE) |

A token from one path is rejected on the other — this is intentional isolation.
The Git CLI token can never call `/v1/*` REST routes, and a browser session
token can never authenticate Git.

## Prerequisites

1. **Git ≥ 2.46** — the `authtype` credential-helper capability (Bearer auth)
   ships in 2.46. Run `git --version` to confirm.
2. **aisphere-git-cli installed** — build the two binaries and put `bin/` on
   your `PATH`, then run `git aisphere install` once. See the
   [CLI README](https://github.com/aisphereio/aisphere-hub/blob/main/git-cli/README.md)
   for build/install steps.
3. **A Casdoor account** — you need a platform identity to log in.

## Quick start

```bash
# 1. Log in (opens browser, Casdoor PKCE). Token stored in ~/.aisphere/credentials.json
git aisphere login

# 2. Create a Skill via the Hub web UI or REST API (see below).
#    The create call provisions the git repo and seeds SKILL.md.

# 3. Clone with plain Git — no extra args, the credential helper injects the token
git clone https://api.weagent.cc:30723/git/<skill-name>.git

# 4. Edit, commit, push
cd <skill-name>
git config user.email "you@aisphere.io"
git config user.name "your-name"
echo "## Usage" >> SKILL.md
git add -A && git commit -m "docs: add usage section"
git push origin main
```

Every `git clone/fetch/push` transparently sends `Authorization: Bearer <JWT>`.
The token is refreshed automatically when it expires; you never type a password.

## Creating a Skill

A Skill is created through the Hub REST API (or web UI). The request carries
the repository name, an initial description (seeds `SKILL.md`), visibility, and
the organization id:

```bash
# Token: web client (browser session), NOT the git-cli token.
curl -X POST https://api.weagent.cc:30723/v1/skills \
  -H "Authorization: Bearer <web-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-skill",
    "display_name": "My Skill",
    "description": "What this skill does",
    "visibility": "private",
    "org_id": "aisphere"
  }'
```

Required fields (protobuf JSON, **snake_case**):

| Field | Required | Notes |
| --- | --- | --- |
| `name` | yes | repository name + canonical identity; lower-case, matches `[a-z0-9-]+` |
| `org_id` | yes | must equal the creator's organization (`owner` claim) |
| `visibility` | no | `private` (default) / `internal` / `public` |
| `display_name` / `description` | no | seeded into `SKILL.md` front-matter |

On success the backend provisions a bare Git repo, writes the initial
`SKILL.md` scaffold commit, and writes the owner + zone relationships to
SpiceDB. The creator becomes the `owner` and can immediately clone/push.

> **Why `org_id` is required and must match**: the Skill belongs to the
> creator's organization. Passing a different `org_id` is rejected with
> `SKILL_INVALID_ARGUMENT`. The `org_id` comes from the Casdoor `owner` claim,
> injected by the Gateway as `x-aisphere-external-owner`.

## Visibility

Visibility controls who can **read** (clone/fetch) a Skill. It is an
authorization capability backed by SpiceDB, not just a database flag —
changing it rewrites SpiceDB tuples and takes effect immediately.

| Visibility | Who can clone | How it's enforced |
| --- | --- | --- |
| `private` | owner + explicitly shared users | SpiceDB `skill:<name>` tuples only |
| `internal` | private policy + same-org members | SpiceDB zone relation |
| `public` | any authenticated platform user | SpiceDB `public` viewer wildcard tuple |

Change visibility through the REST API:

```bash
# Make a private Skill public
curl -X POST https://api.weagent.cc:30723/v1/skills/my-skill:visibility \
  -H "Authorization: Bearer <web-token>" \
  -H "Content-Type: application/json" \
  -d '{"visibility": "public"}'
```

The change is synchronous: the moment the call returns, other users can (or
can no longer) clone the repository. There is no cache to wait out.

## Sharing a private Skill

When a Skill must stay private but specific collaborators need access, use
shares. A share grants a SpiceDB relation to a subject (a user, by UUID).

```bash
# Grant editor role to a user
curl -X POST https://api.weagent.cc:30723/v1/skills/my-skill/shares \
  -H "Authorization: Bearer <web-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "relation": "editor",
    "subject_type": "user",
    "subject_id": "<target-user-uuid>"
  }'
```

Accepted relations:

| Relation | Can clone (view) | Can push feature branch (edit) | Can push main (publish) |
| --- | --- | --- | --- |
| `viewer` | yes | no | no |
| `reviewer` | yes | no | no |
| `editor` | yes | yes | no |
| `publisher` | yes | no | yes |
| `owner` | yes | yes | yes (owner, set at creation only) |

List and revoke shares:

```bash
# List current shares
curl https://api.weagent.cc:30723/v1/skills/my-skill/shares \
  -H "Authorization: Bearer <web-token>"

# Revoke (relation + subject_type + subject_id in the path)
curl -X DELETE \
  https://api.weagent.cc:30723/v1/skills/my-skill/shares/editor/user/<target-user-uuid> \
  -H "Authorization: Bearer <web-token>"
```

Shares take effect immediately — the collaborator can clone on their next
`git clone` without re-logging in.

## Git operations and permission matrix

The Git push path is enforced by a server-side `update` hook that calls
`IAM.CheckPermission` on every ref update. The required permission depends on
**which ref** you push and **how** you push it:

| Git operation | Required permission | Who has it |
| --- | --- | --- |
| `clone` / `fetch` / `ls-remote` (read) | `view` | owner, editor, reviewer, publisher, viewer, public |
| push new feature branch | `edit` | owner, editor |
| push feature branch (fast-forward) | `edit` | owner, editor |
| push `main` (fast-forward) | `publish` | owner, publisher |
| delete / force-push `main` | `manage` | owner |
| push/delete tags | `publish` (new) / `manage` (existing) | owner, publisher |

**Practical consequence**: an `editor` collaborator can clone, create feature
branches, and push to them — but they **cannot push directly to `main`**.
Direct `main` pushes require `publish` (owner or publisher). This is
intentional: `main` is the published line and only the owner/publisher
controls it. Editors collaborate on branches.

### SKILL.md validation on push

When you push to the default branch (`main`), the hook reads
`SKILL.md` at the new commit and validates its front-matter. A push that
removes `SKILL.md` or breaks the front-matter (`name`/`description`/`version`)
is **rejected** by the hook before the ref updates:

```
remote: skillhub: invalid default-branch metadata: <detail>
remote: error: hook declined to update refs/heads/main
```

Always keep a valid `SKILL.md` at the repo root on `main`.

## Working with multiple accounts

The CLI stores one session at a time in `~/.aisphere/credentials.json`. To
operate as a different user, log in again (the new session overwrites the
old):

```bash
git aisphere login        # log in as user B; overwrites the stored session
git aisphere status       # confirm: shows the new subject
git clone https://api.weagent.cc:30723/git/<skill>.git
```

To switch back, `git aisphere login` again as the original user. There is no
need to re-install the helper or change `.gitconfig` — the helper always reads
the current session file.

## Troubleshooting

### `403` on clone

You do not have `view` permission on the Skill. Either:
- the Skill is `private` and not shared with you → ask the owner to share, or
- the Skill was `public` and has been changed back to `private`.

Confirm the Skill's visibility and your shares via the REST API (web token).

### `permission publish denied on skill <name>` on push to main

You are pushing to `main` with an `editor` (or `viewer`) role. `main` requires
the `publish` permission. Either push to a feature branch (`edit` is enough),
or ask the owner to grant `publisher`/reassign ownership.

### `skillhub: invalid default-branch metadata`

Your push to `main` removed or malformed `SKILL.md`. Restore a valid
`SKILL.md` with front-matter `name`, `description`, `version` and push again.

### `Jwt is missing` / GCM password popup

The Windows Git Credential Manager intercepted the request before the
aisphere helper. Ensure `.gitconfig` has the empty helper line that clears
inherited helpers (see the
[CLI README troubleshooting section](https://github.com/aisphereio/aisphere-hub/blob/main/git-cli/README.md#排障)).

### `Audiences in Jwt are not allowed`

You used a Git CLI token (aud = git-cli) against a `/v1/*` REST route, or vice
versa. The two token audiences are deliberately isolated. Use the web session
token for REST API calls and `git aisphere login` for Git operations.
