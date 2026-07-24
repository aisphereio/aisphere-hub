# Skill Release Control Plane

Status: implemented end to end by the Hub release service.

## Product model

SkillHub presents two content states to normal users:

- **Draft**: the mutable content on the Skill's default branch.
- **Version**: an immutable strict-SemVer release backed by a Git tag and exact commit SHA.

Branches, commits and arbitrary Git tags remain available to Git clients and advanced
maintenance tooling, but they are not the primary SkillHub navigation model. The normal
workflow is deliberately reduced to:

```text
edit draft -> preview -> publish version -> browse/install exact version
```

A repository may contain operational tags such as `backup-0724`, `demo` or
`before-refactor`. These are native Git refs, not SkillHub versions. The release API
filters them out and exposes only strict SemVer tags such as `v1.4.2` and
`v1.5.0-beta.1`.

## Contract

A Git commit is an authored change, the default branch is the mutable draft, and a
release tag is the immutable runtime-consumable version.

Runtime consumers MUST resolve a Skill to a release tag and commit SHA. The default
branch is never a production version.

The protobuf service is the only transport contract source. Go HTTP/gRPC bindings,
Swagger, the contract bundle, and frontend SDKs must be generated from it and committed
together. Downstream consumers pin the generated contract by an immutable Hub commit
and verify the SHA-256 recorded in its contract lock.

## Release lifecycle

```text
optional contributor branch -> pull request -> default branch draft -> create release -> immutable vX.Y.Z tag
```

Release tags:

- use canonical SemVer form `vMAJOR.MINOR.PATCH`;
- may include SemVer prerelease identifiers such as `v2.0.0-rc.1`;
- point at an exact source commit;
- cannot be overwritten;
- are validated against the caller-supplied expected commit SHA;
- require the Skill `publish` permission;
- retain release notes, source ref and publisher identity in the annotated tag;
- expose publication time, Commit SHA, Tree SHA and the exact `SKILL.md` SHA-256.

The default SkillHub UI publishes from the default branch only. Source-ref parameters
remain in the low-level contract for Git-native compatibility and advanced tooling, but
normal users do not choose branches during publication.

This increment keeps Git as the source of truth without introducing a second release
metadata database. Durable release bundles, channels, yanking and signed artifacts are
follow-up increments.

## API

User-facing release operations:

- `POST /v1/skills/{name}/releases`
- `GET /v1/skills/{name}/releases`
- `GET /v1/skills/{name}/releases/{version}`
- `GET /v1/skills/{name}/releases/{version}:resolve`

Advanced Git operations retained behind the product model:

- `GET /v1/skills/{name}/refs`
- `GET /v1/skills/{name}/commits?ref=refs/heads/main`
- `GET /v1/skills/{name}/compare?baseRef=v1.3.0&targetRef=refs/heads/main`
- `POST /v1/skills/{name}:restore`

Create release request:

```json
{
  "version": "1.4.0",
  "sourceRef": "refs/heads/main",
  "expectedCommitSha": "<exact draft commit sha>",
  "releaseNotes": "optional notes"
}
```

The server normalizes the version to tag `v1.4.0`, verifies that `sourceRef` still
resolves to `expectedCommitSha`, validates `SKILL.md` at that commit, and creates an
annotated tag. Duplicate tags and stale source refs fail with conflict instead of moving
an existing release.

Exact resolution is encoded in the URL:

```text
GET /v1/skills/search/releases/v1.4.0:resolve
```

Only exact release versions are accepted by release resolution and SkillSet membership.
Floating selectors such as `latest`, `main`, and SemVer ranges remain intentionally
unsupported. The UI may label the highest stable release as "latest", but persistence
and runtime resolution always use its exact SemVer.

Restore never overwrites an old tag or force-pushes history. It creates a new commit
whose tree equals the selected source ref and advances the default branch only when
`expectedHeadSha` still matches. The user reviews the restored draft and publishes a new
SemVer release.
