# Access Control

Aisphere Hub implements a two-layer access control model: **authentication** (AuthN) verifies who the caller is, and **authorization** (AuthZ) determines what they can do. Hub delegates both layers to external providers — Casdoor for identity and SpiceDB (or IAM gRPC) for authorization — while maintaining its own business-level access policy for Skill resources.

## Authentication (AuthN)

Hub supports two authentication modes, configured via `security.authn.mode`:

| Mode | Description | Use case |
|------|-------------|----------|
| `casdoor_jwt` | Backend verifies JWT using Casdoor JWKS | Development/testing |
| `gateway_trusted` | Trusts Gateway-injected `X-Aisphere-*` headers | Production (requires mTLS/NetworkPolicy) |

### Authn middleware

All HTTP and gRPC requests pass through authn middleware (`/internal/server/security.go`):

1. **Public operations** — No authentication required (health checks, DTM callbacks, login/logout URLs)
2. **Authenticated operations** — Bearer token or Gateway trusted headers are validated. The middleware extracts the `authn.Principal` and injects it into the request context.
3. **Authorized operations** — After authentication, the `accessx.Guard` middleware checks the caller has the required permission.

### Token operations

Hub does **not** issue local access tokens. All token operations are delegated to Casdoor:

| Operation | Endpoint | Description |
|-----------|----------|-------------|
| Login URL | `GET /v1/authn/login` | Redirect to Casdoor OIDC login |
| Exchange | `POST /v1/authn/exchange` | Exchange authorization code for tokens |
| Refresh | `POST /v1/authn/refresh` | Refresh access token |
| Logout | `GET /v1/authn/logout` | Redirect to Casdoor logout |
| Revoke | `POST /v1/authn/revoke` | Revoke a token |
| Introspect | `POST /v1/authn/introspect` | Validate and inspect a token |
| Me | `GET /v1/authn/me` | Return current principal info |

### Internal call authentication

Hub supports internal service-to-service calls via a shared secret:

```yaml
security:
  internal_call:
    enabled: true
    header: X-Aisphere-Internal-Token
    token: "aisphere-internal-token-2026"
```

## Authorization (AuthZ)

### Provider model

Hub supports two authorization providers, configured via `security.authz.provider`:

| Provider | Backend | Description |
|----------|---------|-------------|
| `spicedb` | SpiceDB (direct) | Direct SpiceDB connection — **rejected** in production; must go through IAM |
| `iam_grpc` | IAM gRPC service | Production path — IAM wraps SpiceDB and adds directory/group projection |

The `spicedb` direct provider is rejected by tests (`TestNewAuthorizerRejectsDirectSpiceDBProvider`). Production must use `iam_grpc`.

### Authz service capabilities

The `biz.AuthzRepo` interface (implemented in `/internal/data/authz.go`) provides:

| Method | Description |
|--------|-------------|
| `Check` | Check if a subject has a permission on a resource |
| `BatchCheck` | Batch permission checks |
| `WriteRelationships` | Create authorization relationships |
| `DeleteRelationships` | Delete authorization relationships |
| `ReadRelationships` | Query relationships by filter |
| `LookupResources` | Find resources a subject can access |
| `LookupSubjects` | Find subjects who can access a resource |
| `ReadSchema` | Read the SpiceDB schema |
| `WriteSchema` | Write the SpiceDB schema |

### Schema ownership

IAM is the **owner** of the shared SpiceDB schema, including the `skill` resource definition. Hub must not overwrite an existing IAM-managed schema. The only schema fragment Hub maintains is a minimal `skill_version` definition for development SpiceDB instances:

```spicedb
definition skill_version {
  relation skill: skill
  permission view = skill->view
  permission edit = skill->edit
  permission delete = skill->delete
}
```

Hub bootstraps this schema only when SpiceDB has no schema at all (`/internal/data/authz_bootstrap.go`).

### Relationship bootstrap

On startup, Hub attempts to bootstrap authorization relationships from the durable `owner_id` field in PostgreSQL. This ensures that skills created before SpiceDB was enabled still have correct owner relationships. Failure is non-fatal — the owner field serves as a fallback for read access.

## Access policy

### Proto-level authz rules

Every RPC in the proto files declares its access policy using the `aisphere.access.v1.policy` option:

```protobuf
option (aisphere.access.v1.policy) = {
  exposure: AUTHORIZED
  authz: { action: "create" resource: "aihub:skill:*" audience: "aihub-service" mode: CHECK_ONLY }
  audit: { enabled: true event: "aihub.skill.create" risk: "medium" }
};
```

Three exposure levels:

| Level | Description |
|-------|-------------|
| `PUBLIC` | No authentication required (login, health, DTM) |
| `AUTHENTICATED` | Valid authentication required, no specific permission check |
| `AUTHORIZED` | Authentication + specific permission check via `accessx.Guard` |

### IAM permission mapping

The generated authz rules map Hub operations to IAM schema permissions. Verified by `TestSkillAuthzRulesUseIAMSchemaPermissions`:

| Operation | Permission | Resource |
|-----------|------------|----------|
| `UpdateSkill` | `edit` | `skill:{name}` |
| `PublishSkillVersion` | `publish` | `skill:{name}` |
| `UpdateSkillVisibility` | `manage` | `skill:{name}` |
| `CreateSkillShare` | `manage` | `skill:{name}` |
| `DeleteSkill` | `manage` | `skill:{name}` |

### Implicit policy fallback

When SpiceDB is unavailable, Hub falls back to an implicit policy for **read-only** operations:

- Owner always reads (matched by `owner_id` field)
- Public visibility → all authenticated users
- Internal visibility → same org
- Private → only owner

Write operations remain **fail-closed** — they never fall back.

### Audit

All `AUTHORIZED` operations record audit events. The audit system supports:

- Structured events with actor, resource, action, result
- Risk levels (low, medium, high)
- Configurable store (memory for dev, persistent for production)

## Responsibility boundary

```
Casdoor:     Identity source, org/group/user membership
IAM:         Directory adapter, principal search, SpiceDB relationship projection
Hub:         Skill metadata, versions, files, visibility, share intent
SpiceDB:     Authorization graph and permission checks
```

Hub must **not** become a second IAM. Platform login, token verification, directory lookup, generic permission APIs, organization management, group management, and member management belong to `aisphere-iam`.

## Source references

| File | Purpose |
|------|---------|
| `/internal/biz/authn.go` | AuthN use case (login, exchange, refresh, revoke, introspect) |
| `/internal/biz/authz.go` | AuthZ use case (check, relationships, lookup) |
| `/internal/data/authn.go` | AuthN repo adapter (Casdoor OIDC, Gateway-trusted) |
| `/internal/data/authz.go` | AuthZ repo adapter (SpiceDB/IAM gRPC) |
| `/internal/data/authz_bootstrap.go` | Startup schema and relationship bootstrap |
| `/internal/data/authz_provider_test.go` | Provider selection tests |
| `/internal/server/security.go` | AuthN middleware, access guard |
| `/internal/server/modules_test.go` | Module authz resolution tests |
| `/api/skill/v1/skill.proto` | Proto authz rules |
| `/api/skill/v1/skill_policy_test.go` | Authz rule verification tests |
| `/docs/ai/skill-access-policy.md` | Access policy design doc |
| `/docs/ai/root-skill-iam-share.md` | IAM sharing model design |
| `/docs/authn/` | AuthN design docs |