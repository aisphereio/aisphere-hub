# API Surface

Hub's API is defined in **Protobuf** and generates both gRPC and HTTP (gRPC-Gateway) bindings. All business RPCs declare access policy and audit rules at the proto level using Kernel extensions.

## Service modules

Hub exposes three service modules (registered in `/internal/server/modules.go`):

| Module | Proto package | Description |
|--------|--------------|-------------|
| `authnv1` | `aisphere.hub.authn.v1` | Authentication (login, logout, exchange, refresh, revoke, introspect) |
| `auditv1` | `aisphere.hub.audit.v1` | Audit record querying |
| `skillv1` | `skill.v1` | Skill CRUD, version lifecycle, draft workspace, sharing |

The authz control-plane service (schema management, relationship CRUD) is intentionally **not** exposed as a Hub module — it is delegated to IAM gRPC.

## Proto definitions

| File | Contents |
|------|----------|
| `/api/skill/v1/skill.proto` | SkillService — 30+ RPCs for skill management |
| `/api/authn/v1/authn.proto` | AuthnService — login, exchange, refresh, revoke, introspect |
| `/api/authz/v1/authz.proto` | AuthzService — permission checks, relationship management |
| `/api/audit/v1/audit.proto` | AuditService — query audit records |
| `/api/aisphere/access/v1/access.proto` | Access policy extension definitions |
| `/api/aisphere/options/v1/authz.proto` | Authz option definitions |

## Skill endpoints

### Skill CRUD

| Method | HTTP | gRPC | Authz | Risk |
|--------|------|------|-------|------|
| `CreateSkill` | `POST /v1/skills` | `CreateSkill` | `create` on `aihub:skill:*` | Medium |
| `GetSkill` | `GET /v1/skills/{name}` | `GetSkill` | `AUTHENTICATED` | Low |
| `UpdateSkill` | `PUT /v1/skills/{name}` | `UpdateSkill` | `update` on `aihub:skill:{name}` | Medium |
| `DeleteSkill` | `DELETE /v1/skills/{name}` | `DeleteSkill` | `delete` on `aihub:skill:{name}` | High |
| `ListSkills` | `GET /v1/skills` | `ListSkills` | `AUTHENTICATED` | Low |
| `UpdateSkillVisibility` | `POST /v1/skills/{name}:visibility` | `UpdateSkillVisibility` | `visibility:update` on `aihub:skill:{name}` | High |

### Version lifecycle

| Method | HTTP | Authz | Risk |
|--------|------|-------|------|
| `UploadSkillPackage` | `POST /v1/skills:upload` | `upload` on `aihub:skill:*` | Medium |
| `ListSkillVersions` | `GET /v1/skills/{name}/versions` | `AUTHENTICATED` | Low |
| `GetSkillVersion` | `GET /v1/skills/{name}/versions/{version}` | `AUTHENTICATED` | Low |
| `SubmitSkillVersion` | `POST /v1/skills/{name}/versions/{version}:submit` | `submit` on `aihub:skill:{name}:version:{version}` | Medium |
| `PublishSkillVersion` | `POST /v1/skills/{name}/versions/{version}:publish` | `publish` on `aihub:skill:{name}:version:{version}` | High |
| `OnlineSkillVersion` | `POST /v1/skills/{name}/versions/{version}:online` | `online` on `aihub:skill:{name}:version:{version}` | High |
| `OfflineSkillVersion` | `POST /v1/skills/{name}/versions/{version}:offline` | `offline` on `aihub:skill:{name}:version:{version}` | High |
| `DownloadSkillVersion` | `GET /v1/skills/{name}/versions/{version}/download` | `AUTHENTICATED` | Low |

### Version files

| Method | HTTP | Authz | Risk |
|--------|------|-------|------|
| `ListSkillVersionFiles` | `GET /v1/skills/{name}/versions/{version}/files` | `AUTHENTICATED` | Low |
| `GetSkillVersionFile` | `GET /v1/skills/{name}/versions/{version}/file` | `AUTHENTICATED` | Low |

### Draft workspace

| Method | HTTP | Authz | Risk |
|--------|------|-------|------|
| `ListSkillDraftFiles` | `GET /v1/skills/{name}/draft/files` | `AUTHENTICATED` | Low |
| `GetSkillDraftFile` | `GET /v1/skills/{name}/draft/file` | `AUTHENTICATED` | Low |
| `UpsertSkillDraftFile` | `PUT /v1/skills/{name}/draft/file` | `draft:file:write` on `aihub:skill:{name}` | Medium |
| `UpsertSkillDraftDirectory` | `POST /v1/skills/{name}/draft/dir` | `draft:dir:write` on `aihub:skill:{name}` | Medium |
| `DeleteSkillDraftPath` | `DELETE /v1/skills/{name}/draft/path` | `draft:path:delete` on `aihub:skill:{name}` | Medium |
| `MoveSkillDraftPath` | `POST /v1/skills/{name}/draft/path:move` | `draft:path:move` on `aihub:skill:{name}` | Medium |
| `CommitSkillDraft` | `POST /v1/skills/{name}/draft:commit` | `draft:commit` on `aihub:skill:{name}` | Medium |

### Sharing

| Method | HTTP | Authz | Risk |
|--------|------|-------|------|
| `ListSkillShares` | `GET /v1/skills/{name}/shares` | `share:list` on `aihub:skill:{name}` | Medium |
| `CreateSkillShare` | `POST /v1/skills/{name}/shares` | `share:create` on `aihub:skill:{name}` | High |
| `DeleteSkillShare` | `DELETE /v1/skills/{name}/shares/{type}/{id}` | `share:delete` on `aihub:skill:{name}` | High |

## AuthN endpoints

| Method | HTTP | Description |
|--------|------|-------------|
| `LoginURL` | `GET /v1/authn/login` | Redirect to Casdoor OIDC login |
| `Exchange` | `POST /v1/authn/exchange` | Exchange auth code for tokens |
| `Refresh` | `POST /v1/authn/refresh` | Refresh access token |
| `LogoutURL` | `GET /v1/authn/logout` | Redirect to Casdoor logout |
| `Revoke` | `POST /v1/authn/revoke` | Revoke a token |
| `Introspect` | `POST /v1/authn/introspect` | Validate and inspect a token |
| `Me` | `GET /v1/authn/me` | Return current principal info |

All authn endpoints are `PUBLIC` (no authentication required) because they are the entry points for authentication itself.

## AuthZ endpoints

| Method | HTTP | Description |
|--------|------|-------------|
| `CheckPermission` | `POST /v1/authz/check` | Check a single permission |
| `BatchCheckPermissions` | `POST /v1/authz/batch-check` | Batch permission checks |
| `WriteRelationships` | `POST /v1/authz/relationships` | Create relationships |
| `DeleteRelationships` | `DELETE /v1/authz/relationships` | Delete relationships |
| `ReadRelationships` | `GET /v1/authz/relationships` | Query relationships |
| `LookupResources` | `POST /v1/authz/lookup-resources` | Find accessible resources |
| `LookupSubjects` | `POST /v1/authz/lookup-subjects` | Find subjects with access |
| `ReadSchema` | `GET /v1/authz/schema` | Read SpiceDB schema |
| `WriteSchema` | `POST /v1/authz/schema` | Write SpiceDB schema |

## Audit endpoints

| Method | HTTP | Description |
|--------|------|-------------|
| `QueryAuditRecords` | `GET /v1/audit/records` | Query audit records with filters |

## Generated code

Each proto generates multiple Go files:

| Suffix | Purpose |
|--------|---------|
| `.pb.go` | Message types and marshaling |
| `_grpc.pb.go` | gRPC server/client stubs |
| `_http.pb.go` | HTTP handler bindings |
| `_gateway.pb.go` | Gateway API resource definitions |
| `_authz.pb.go` | Authz rule constants |
| `_kernel.pb.go` | Kernel service registration |
| `.pb.gw.go` | gRPC-Gateway reverse proxy |

## OpenAPI spec

An OpenAPI 3.0.3 spec is generated at `/openapi.yaml` (76KB) and `/docs/openapi/aisphere-hub.swagger.json` (90KB).

## Source references

| File | Purpose |
|------|---------|
| `/api/skill/v1/skill.proto` | Skill API definition |
| `/api/authn/v1/authn.proto` | AuthN API definition |
| `/api/authz/v1/authz.proto` | AuthZ API definition |
| `/api/audit/v1/audit.proto` | Audit API definition |
| `/api/skill/v1/skill_policy_test.go` | Authz rule verification tests |
| `/internal/server/modules_test.go` | Module registration tests |
| `/openapi.yaml` | Generated OpenAPI spec |
| `/docs/openapi/aisphere-hub.swagger.json` | Generated Swagger spec |