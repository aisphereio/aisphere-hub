# Skill Release Control Plane

Status: implemented end to end by the Hub release service.

## Contract

A Git commit is an authored change, a branch is a mutable development line, and a release tag is the immutable runtime-consumable version.

Runtime consumers MUST resolve a Skill to a release tag and commit SHA. `main` is not a production version.

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

This increment keeps Git as the source of truth and exposes release creation and exact resolution through generated HTTP and gRPC transports. Durable release metadata, channels, bundle building, yanking, and Git ref outbox events are follow-up increments.

## API

- `POST /v1/skills/{name}/releases`
- `GET /v1/skills/{name}/releases`
- `GET /v1/skills/{name}/releases/{version}`
- `GET /v1/skills/{name}/releases/{version}:resolve`

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
GET /v1/skills/search/releasess/v1.4.0:resolve
```

Only exact release versions are accepted in this increment. Floating selectors such as `latest`, `main`, and SemVer ranges remain intentionally unsupported until channel and SkillSet revision resolution are implemented.
