# Operations & Deployment

## Configuration

Hub uses the Kernel `configx` framework to load configuration from files and environment variables. The config directory is specified via the `-conf` flag (default: `configs`).

### Config structure

```yaml
service:       # Name, version, environment
server:        # HTTP (18001) and gRPC (19001) settings, CORS
log:           # Structured logging (level, format, redact, access log)
data:          # Database (Postgres), Cache (Redis), ObjectStore (MinIO)
security:      # Authn (Casdoor/OIDC), Authz (SpiceDB/IAM), Access, InternalCall
audit:         # Audit event recording (memory store)
metrics:       # Prometheus metrics (port 19090)
dtm:           # Distributed transaction manager (Saga)
skill:         # Skill storage settings (max_versions)
```

### Key config files

| File | Purpose |
|------|---------|
| `/configs/config.yaml` | Default config (checked in) |
| `/configs/config.local.yaml` | Local development overrides |
| `/deploy/config.yaml` | Production config template |

## Docker

The Dockerfile uses a multi-stage build:

1. **Builder stage**: `golang:1.25.8-alpine`, downloads deps with `GOPROXY=direct`, builds with `CGO_ENABLED=0`
2. **Runtime stage**: `alpine:3.22`, runs as non-root `app` user

```dockerfile
# Build
docker build --build-arg VERSION=$VERSION -t aisphere-hub .

# Run
docker run -p 18001:18001 -p 19001:19001 aisphere-hub
```

Exposed ports: `18001` (HTTP), `19001` (gRPC), `19090` (metrics).

## Kubernetes deployment

Hub is deployed as two images:

| Image | Ports | Description |
|-------|-------|-------------|
| `aisphere-hub` | HTTP 18001, gRPC 19001, Metrics 19090 | Backend Go service |
| `aisphere-hub-frontend` | HTTP 3000 | Next.js standalone server |

### Deployment manifests

| File | Purpose |
|------|---------|
| `/deploy/k8s/base/deployment-backend.yaml` | Backend Deployment |
| `/deploy/k8s/base/deployment-frontend.yaml` | Frontend Deployment |
| `/deploy/k8s/base/services.yaml` | Service definitions |
| `/deploy/k8s/base/grpcroute.yaml` | gRPC route |
| `/deploy/k8s/base/httproutes.yaml` | HTTP routes |
| `/deploy/k8s/base/securitypolicy-grpc.yaml` | gRPC security policy |
| `/deploy/k8s/base/securitypolicy-http.yaml` | HTTP security policy |
| `/deploy/k8s/base/clienttrafficpolicy.yaml` | Client traffic policy |
| `/deploy/k8s/base/configmap.yaml` | ConfigMap |
| `/deploy/k8s/base/kustomization.yaml` | Kustomize overlay |

### Gateway configuration

| File | Purpose |
|------|---------|
| `/deploy/gateway/hub-http-route.yaml` | HTTP route for Envoy Gateway |
| `/deploy/gateway/hub-grpc-route.yaml` | gRPC route for Envoy Gateway |
| `/deploy/gateway/hub-front-http-route.yaml` | Frontend HTTP route |
| `/deploy/gateway/hub-security-policy.yaml` | Security policy (CORS, JWT) |
| `/deploy/gateway/casdoor-hub-client-secret.yaml` | Casdoor OIDC client secret |

### Deployment flow

1. Envoy Gateway handles TLS termination and Casdoor OIDC login
2. Authenticated requests are forwarded to Hub backend (HTTP 18001) or frontend (port 3000)
3. Hub backend validates Gateway-trusted headers and performs business authorization via SpiceDB/IAM

## CI/CD

### CI workflow (`.github/workflows/ci.yml`)

Triggered on PRs and pushes to `main` and `feat/*` branches:

1. Checkout + Go setup
2. `go mod download`
3. `go test ./... -count=1 -short`
4. `go build ./...`
5. Render Kubernetes manifests with `kubectl kustomize`
6. Build container image

### Docker ACR workflow (`.github/workflows/docker-acr.yml`)

Builds and pushes the backend image to Azure Container Registry.

### OpenWiki workflow (`.github/workflows/openwiki-update.yml`)

Scheduled workflow to refresh the repository wiki.

## DTM (Distributed Transaction Manager)

DTM is optional but recommended for production. It coordinates S3 promotion and PostgreSQL metadata writes using the Saga pattern.

### DTM config

```yaml
dtm:
  enabled: true
  server: "http://dtm-server:36789/api/dtmsvr"
  service_base_url: "http://aisphere-hub.aisphere:18001"
  branch_prefix: "/internal/dtm"
  wait_result: true
  timeout_ns: 10000000000
```

### DTM branch endpoints

These internal endpoints are registered when DTM is enabled:

```
POST /internal/dtm/skill/package/promote
POST /internal/dtm/skill/package/promote_compensate
POST /internal/dtm/skill/metadata/upsert
POST /internal/dtm/skill/metadata/upsert_compensate
POST /internal/dtm/skill/draft/object/promote
POST /internal/dtm/skill/draft/object/promote_compensate
POST /internal/dtm/skill/draft/metadata/upsert
POST /internal/dtm/skill/draft/metadata/upsert_compensate
```

These endpoints are `PUBLIC` (no authn) because DTM calls them as an internal coordinator. They should be exposed only on a trusted network or behind infrastructure ACLs. Branch callbacks validate `dtm.branch_secret` through `dtmx.ValidateBranchRequest`.

## Observability

### Metrics

Hub exposes Prometheus metrics on port `19090` (configurable):

| Metric | Type | Description |
|--------|------|-------------|
| `hub_operations_total` | Counter | Total business operations |
| `hub_operation_duration_seconds` | Histogram | Operation latency |
| `hub_authn_middleware_total` | Counter | AuthN middleware decisions |
| `hub_authn_middleware_duration` | Histogram | AuthN middleware latency |
| `hub_component_configured` | Gauge | Component configured/enabled |
| `hub_component_ready` | Gauge | Component ready |
| `hub_component_init_total` | Counter | Component init attempts |
| `hub_component_init_duration_seconds` | Histogram | Component init latency |

### Logging

Structured logging via `logx` with:

- Configurable level (debug/info/warn/error)
- JSON or console format
- Sensitive data redaction (password, secret, token, access_key, etc.)
- Access log with skip paths, slow threshold, request ID header
- Context injection for traceability

### Health probes

| Endpoint | Description |
|----------|-------------|
| `/healthz` | Liveness probe |
| `/readyz` | Readiness probe |

## Makefile targets

| Target | Description |
|--------|-------------|
| `make tools` | Install codegen tools into `.bin` |
| `make tools-local` | Install from local Kernel checkout |
| `make check-tools` | Verify required tools |
| `make api` | Generate API code from protos |
| `make proto-check` | Run buf lint and contract checks |
| `make config` | Generate config proto code |
| `make wire` | Generate dependency injection |
| `make generate` | Run `go generate` |
| `make build` | Build service binary |
| `make run` | Run service locally |
| `make test` | Run all tests |
| `make tidy` | Run `go mod tidy` |
| `make verify` | Full verification pipeline |
| `make clean` | Clean artifacts |

## Source references

| File | Purpose |
|------|---------|
| `/Dockerfile` | Container build |
| `/Makefile` | Build automation |
| `/deploy/k8s/README.md` | K8s deployment guide |
| `/deploy/k8s/base/` | K8s manifests |
| `/deploy/gateway/` | Envoy Gateway configs |
| `/deploy/config.yaml` | Production config |
| `/configs/config.local.yaml` | Local dev config |
| `/.github/workflows/ci.yml` | CI pipeline |
| `/.github/workflows/docker-acr.yml` | ACR build/push |
| `/internal/observability/observability.go` | Metrics and logging helpers |
| `/internal/conf/conf.go` | Config DTOs |