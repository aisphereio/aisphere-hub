# Hub AuthN Backend Verification

Hub can participate in the same full-flow authn test as IAM:

```text
Client -> Gateway verifies Casdoor JWT -> Hub verifies same Casdoor JWT -> business logic
```

The Gateway keeps forwarding `Authorization: Bearer <casdoor_access_token>` to backend gRPC calls. Hub's authn interceptor verifies it using Kernel's OIDC/JWKS verifier when `security.authn.mode=casdoor_jwt`.

Hub also accepts Gateway trusted headers as a fallback so the same middleware can run in `gateway_trusted` deployments later.

AuthZ is intentionally configured with `dev_allow_all: true` in the demo config so this test only validates authentication.
