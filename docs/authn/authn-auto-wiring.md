# Hub AuthN Auto Wiring Target

Hub should run in backend mode by default:

```yaml
security:
  authn:
    enabled: true
    mode: gateway_trusted
  internal_call:
    enabled: true
    header: X-Aisphere-Internal-Token
    token: "${GATEWAY_TO_HUB_INTERNAL_TOKEN}"
```

The target implementation is to construct `securityx.AuthnBoundaryRuntime` in
the data/bootstrap layer and install `runtime.ServerMiddleware()` on HTTP and
gRPC transports. Business handlers should only read:

```go
principal, ok := authn.PrincipalFromContext(ctx)
```

The current repository snapshot is missing its `internal/data` package, so this
archive documents the intended wiring and keeps the existing server authn hooks
compatible with Gateway trusted headers.
