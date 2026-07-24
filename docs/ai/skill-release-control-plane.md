# Skill Release Control Plane

Status: implemented end to end by the Hub release service.

## Contract

A Git commit is an authored change, a branch is a mutable development line, and a release tag is the immutable runtime-consumable version.

Runtime consumers MUST resolve a Skill to a release tag and commit SHA. `main` is not a production version.

The protobuf service is the only transport contract source. Go HTTP/gRPC bindings, Swagger, the contract bundle, and frontend SDKs must be generated from it and committed together. Downstream consumers pin the generated contract by an immutable Hub commit and verify the SHA-256 recorded in its contract lock.

## Release lifecycle

```text
workspace branch -> pull request -> main -> create release -> immutable vX.Y.Z tag
```

Release tags:

- use canonical SemVer form `vMAJOR.MINOR.PATCH`;
- point at an exact source commit;
- cannot be overwritten;
- are validated against the caller supplied expected commit SHA;
- require the Skill `publish` permission.
- retain release notes, source ref and publisher identity in the annotated tag;
- expose the tag publication time, Commit SHA, Tree SHA and exact `SKILL.md`
  SHA-256 without introducing a release metadata database.

This increment keeps Git as the source of truth and exposes release creation and exact resolution through generated HTTP and gRPC transports. Durable release metadata, channels, bundle building, yanking, and Git ref outbox events are follow-up increments.

## API

- `POST /v1/skills/{name}/releases`
- `GET /v1/skills/{name}/releases`
- `GET /v1/skills/{name}/releases/{version}`
- `GET /v1/skills/{name}/releases/{version}:resolve`
- `GET /v1/skills/{name}/refs`
- `GET /v1/skills/{name}/commits?ref=refs/heads/main`
- `GET /v1/skills/{name}/compare?baseRef=v1.3.0&targetRef=v1.4.0`
- `POST /v1/skills/{name}:restore`

Create release request:

```json
{
  "version": "1.4.0",
  "sourceRef": "refs/heads/main",
  "expectedCommitSha": "<exact commit sha>",
  "releaseNotes": "optional notes"
}
```

The server normalizes the version to tag `v1.4.0`, verifies that `sourceRef` still resolves to `expectedCommitSha`, validates `SKILL.md` at that commit, and creates an annotated tag. Duplicate tags and stale source refs fail with conflict instead of moving an existing release.

Exact resolution is encoded in the URL:

```text
GET /v1/skills/search/releases/v1.4.0:resolve
```

Only exact release versions are accepted by release resolution and SkillSet
membership. Floating selectors such as `latest`, `main`, and SemVer ranges
remain intentionally unsupported.

Restore never overwrites an old tag or force-pushes history. It creates a new
commit whose tree equals the selected source ref and advances the target branch
only when `expectedHeadSha` still matches. The user reviews and publishes a new
SemVer release from that commit.
