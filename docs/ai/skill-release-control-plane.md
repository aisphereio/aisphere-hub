# Skill Release Control Plane

Status: implemented in this branch.

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

This first control-plane increment keeps Git as the source of truth and exposes release creation and exact resolution through the Skill API. Durable release metadata, channels, bundle building, yanking, and Git ref outbox events are follow-up increments.

## API

- `POST /v1/skills/{name}/releases`
- `GET /v1/skills/{name}/releases`
- `GET /v1/skills/{name}/releases/{tag}`
- `POST /v1/skills/{name}/versions:resolve`

Create release request:

```json
{
  "version": "1.4.0",
  "sourceRef": "refs/heads/main",
  "expectedCommitSha": "<40-char commit sha>",
  "releaseNotes": "optional notes"
}
```

The server normalizes the version to tag `v1.4.0`, validates `SKILL.md` at the source commit, and creates an annotated tag. Duplicate or moved tags fail with conflict instead of overwriting the existing release.

Resolve request:

```json
{
  "selector": "v1.4.0"
}
```

Only exact release selectors are accepted in this increment. Floating selectors such as `latest`, `main`, and SemVer ranges remain intentionally unsupported until channel and SkillSet revision resolution are implemented.
