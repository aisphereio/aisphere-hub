# Hub observability and correctness review

This change keeps the business semantics of the hub module intact and focuses on boundary-layer observability, metrics, and error normalization.

## Startup order

The hub entry point now initializes in this order:

1. Load config.
2. Build `logx.Logger` and install it as the default logger.
3. Build the shared `metricsx.Manager` when `metrics.enabled=true`.
4. Inject logger and metrics into the bootstrap context.
5. Initialize `dbx`, `cachex`, object store, authn, and authz resources.
6. Wire usecases and services.
7. Start HTTP/gRPC servers and optional Prometheus admin endpoint.

This ensures component initialization logs and metrics go to the same logger/manager as runtime traffic.

## Metrics

Hub-level metrics are centralized in `internal/observability`:

- `hub_operations_total`
- `hub_operation_duration_seconds`
- `hub_authn_middleware_total`
- `hub_authn_middleware_duration_seconds`
- `hub_component_configured`
- `hub_component_ready`
- `hub_component_init_total`
- `hub_component_init_duration_seconds`

Labels are intentionally low-cardinality: component, operation, status, error_code, transport, decision.

## Authn

Authn usecase and repo now emit structured debug/info/warn logs, use `errorx` for missing provider/configuration errors, and record operation metrics. Token/code values are not logged; only safe booleans such as `has_token`, `has_code`, `has_refresh_token` are logged.

HTTP authn middleware records accept/reject/public-path decisions and injects `authn.Principal` into context. gRPC now has the same authn interceptor path, so skill/authz handlers no longer depend only on HTTP middleware for principal propagation. Login/exchange/refresh/logout/revoke/introspect stay public at the transport layer because those RPCs validate the code/token carried in the request body; `Me` remains protected by Bearer metadata/header.

## Authz

Authz usecase and repo now use the same observability wrapper. Public methods record latency/count/status/error_code metrics and use `logx.WithContext(ctx)` instead of unsupported `WarnContext`/`InfoContext` calls on `logx.Logger`.

## Skill

Skill usecase now receives the shared metrics manager and has a single `begin/end` helper for context-aware logs and operation metrics. Main skill operations now use it for create, update, list, get, delete, upload, download, and share write paths. The data repo also has a single context logger helper, replacing repeated `logx.FromContext(ctx).WithContext(ctx)` chains.

The cleanup also fixed unsafe nil input handling in create/update skill paths.

## Fixed correctness issues

- Fixed stale import path `aisphere-hub/internal/biz` to `github.com/aisphereio/aisphere-hub/internal/biz`.
- Replaced unsupported `logx.Logger.WarnContext/InfoContext/DebugContext/ErrorContext` usages with `logger.WithContext(ctx).Warn/Info/Debug/Error`.
- Moved logger/metrics initialization before resource construction.
- Added HTTP/gRPC server log/metrics/access-log options.
- Added gRPC authn interceptor so gRPC skill/authz calls receive the authenticated principal in context.
- Normalized several authn resource/provider errors to `errorx` codes.
- Fixed nil `*Skill` panic risk in `CreateSkill` and `UpdateSkill`.
- Fixed the invalid `SkillListResult.Total` reference in list metrics; it now records `len(result.Items)`.

## Notes left for later

Audit persistence is intentionally not expanded in this pass. The current audit abstraction remains, but a production `auditx` store/migration/event-coverage hardening should be a separate phase.
