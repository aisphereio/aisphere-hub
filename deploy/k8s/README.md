# Hub 测试环境部署

这版部署先把一个点确定下来：Hub 前端和后端是两个镜像，但对浏览器只暴露一个 HTTP 域名。Envoy Gateway 负责 Casdoor OIDC 登录，登录后的 access token 继续转发给 Hub 和 IAM；Hub、IAM 再用同一套 Casdoor JWKS 验证 token。

默认部署链路是：

```text
Browser
  -> https://hub.example.com
  -> Envoy Gateway OIDC / Casdoor Application
  -> /                 -> aisphere-hub-frontend:3000
  -> /v1/iam/*         -> aisphere-iam:18080
  -> /v1/*             -> aisphere-hub:18001

CLI / Agent
  -> https://grpc.hub.example.com
  -> Envoy Gateway JWT
  -> GRPCRoute
  -> aisphere-hub:19001
```

Hub 当前的业务 authz 仍然是：

```text
Hub -> SpiceDB
```

SpiceDB schema 和 IAM 目录关系由 IAM 管理，Hub 只消费 `skill` schema 并写 Skill 自己的 owner/editor/viewer 关系。这里没有把 Envoy 的 gRPC ExternalAuth 直接指向 IAM，原因见文档后面的“为什么没有直接启用 IAM gRPC ExtAuth”。

## 1. 版本和前置条件

这份清单按以下 API 编写：

- Kubernetes Gateway API Standard Channel，`GRPCRoute v1`。
- Envoy Gateway `v1.8.x`，需要 `SecurityPolicy` 和 `ClientTrafficPolicy` CRD。
- 已存在 `aisphere/aisphere-gateway` Gateway，并且有 HTTPS listener。
- Gateway TLS 证书覆盖两个域名：
  - `hub.example.com`
  - `grpc.hub.example.com`
- `aisphere` namespace 中已经部署：PostgreSQL、Redis、MinIO、SpiceDB、Casdoor、`aisphere-iam`。
- IAM 已发布包含 `skill` resource 的 SpiceDB schema。

先确认 CRD 和 Gateway：

```bash
kubectl api-resources | grep -E 'httproutes|grpcroutes|securitypolicies|clienttrafficpolicies'
kubectl get gateway -n aisphere aisphere-gateway
kubectl get gateway -n aisphere aisphere-gateway -o yaml
```

如果 Gateway 不在 `aisphere` namespace，当前清单不能直接跨 namespace 引用，需要把 Route/SecurityPolicy 放到 Gateway 所在 namespace，或者补 `ReferenceGrant`。

## 2. GitHub Actions 和镜像

前后端两个仓库都使用以下 Actions secrets：

| Secret | 说明 |
| --- | --- |
| `ALIYUN_REGISTRY` | 例如 `registry.cn-beijing.aliyuncs.com` |
| `ALIYUN_NAMESPACE` | 例如 `ainfracn` |
| `ALIYUN_USERNAME` | ACR 用户名 |
| `ALIYUN_PASSWORD` | ACR 密码或访问凭据 |

目标分支 `feat/root-skill-iam-share` push 后会发布：

```text
aisphere-hub:feat-root-skill-iam-share
aisphere-hub:sha-<short-sha>

aisphere-hub-frontend:feat-root-skill-iam-share
aisphere-hub-frontend:sha-<short-sha>
```

测试环境建议固定 `sha-*`，这样前后端版本可以明确对应；分支 tag 适合快速联调。

如果 ACR 是私有仓库，先创建拉取 Secret：

```bash
kubectl create namespace aisphere --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret docker-registry aliyun-registry \
  -n aisphere \
  --docker-server=registry.cn-beijing.aliyuncs.com \
  --docker-username='<username>' \
  --docker-password='<password>' \
  --dry-run=client -o yaml | kubectl apply -f -
```

## 3. Casdoor Application

Casdoor 单独创建一个 Hub Application，例如：

```text
name: aisphere-hub
organization: aisphere
client ID: <CASDOOR_CLIENT_ID>
client secret: <CASDOOR_CLIENT_SECRET>
redirect URI: https://hub.example.com/oauth2/callback
scopes: openid profile email
```

注意这几个值必须完全一致：

1. `SecurityPolicy.spec.oidc.provider.issuer`。
2. Hub/IAM 配置中的 `security.authn.oidc.issuer`。
3. Casdoor token 的 `iss` claim。
4. `SecurityPolicy`、Hub、IAM 使用的 audience/client ID。

不要把 Casdoor 内部 Service 地址当成 issuer。比如 Casdoor 在集群内是 `http://casdoor.aisphere:8000`，但 token 的 `iss` 是 `https://casdoor.example.com`，那么 issuer 必须使用后者；内部地址只放到 `CASDOOR_ENDPOINT`。

创建运行时 Secret：

```bash
kubectl create secret generic aisphere-hub-secrets \
  -n aisphere \
  --from-literal=HUB_POSTGRES_DSN='postgres://postgres:<password>@postgres.aisphere.svc.cluster.local:5432/aisphere_hub?sslmode=disable' \
  --from-literal=HUB_REDIS_PASSWORD='<redis-password>' \
  --from-literal=HUB_S3_ACCESS_KEY='<minio-access-key>' \
  --from-literal=HUB_S3_SECRET_KEY='<minio-secret-key>' \
  --from-literal=CASDOOR_CLIENT_ID='<casdoor-client-id>' \
  --from-literal=CASDOOR_CLIENT_SECRET='<casdoor-client-secret>' \
  --from-literal=SPICEDB_TOKEN='<spicedb-token>' \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic casdoor-hub-oidc \
  -n aisphere \
  --from-literal=client-secret='<casdoor-client-secret>' \
  --dry-run=client -o yaml | kubectl apply -f -
```

示例结构见 `examples/secrets.example.yaml`，不要把填好真实值的 Secret 提交到 Git。

## 4. 修改测试环境参数

部署前至少替换这些占位值：

```bash
grep -RInE 'example\.com|CHANGE_ME' deploy/k8s/base
```

需要修改：

- `configmap.yaml`
  - `CASDOOR_ENDPOINT`
  - `CASDOOR_ISSUER`
  - Redis、MinIO、SpiceDB Service 地址
- `httproutes.yaml`
  - Hub HTTP hostname
- `grpcroute.yaml`
  - Hub gRPC hostname
- `securitypolicy-http.yaml`
  - issuer、client ID、redirect URL、JWKS URL
- `securitypolicy-grpc.yaml`
  - issuer、audience/client ID、JWKS URL
- `deployment-backend.yaml` / `deployment-frontend.yaml`
  - ACR 地址和镜像 tag

`securitypolicy-http.yaml` 同时支持两类客户端：

- Browser 没有 Bearer token，走 OIDC cookie/session。
- CLI/Agent 自带 `Authorization: Bearer ...`，JWT 验证成功后跳过 OIDC redirect。

Envoy 会通过 `forwardAccessToken: true` 把浏览器 OIDC access token 作为 Bearer token 传给 Hub/IAM。Hub 配置使用 `casdoor_jwt`，会再做一次 JWKS 校验，测试阶段先把链路做扎实。

IAM 也要使用能验证 Bearer token 的模式：

```yaml
security:
  authn:
    mode: casdoor_jwt
```

如果 IAM 仍是 `gateway_trusted`，但 Gateway 又没有注入 IAM 所要求的 trusted headers，那么 `/v1/iam/me` 和分享对象查询会返回 401。这个问题不要通过放开 IAM authn 解决，直接把 IAM 的 issuer/audience 和 Hub Casdoor Application 对齐。

## 5. 应用清单

```bash
kubectl apply -k deploy/k8s/base
```

检查资源是否被 Gateway 接受：

```bash
kubectl get deploy,pod,svc -n aisphere -l app.kubernetes.io/name=aisphere-hub
kubectl get httproute,grpcroute -n aisphere
kubectl get securitypolicy,clienttrafficpolicy -n aisphere

kubectl describe httproute -n aisphere aisphere-hub-public
kubectl describe httproute -n aisphere aisphere-hub-protected
kubectl describe grpcroute -n aisphere aisphere-hub-grpc
kubectl describe securitypolicy -n aisphere aisphere-hub-http-security
kubectl describe securitypolicy -n aisphere aisphere-hub-grpc-security
```

重点看状态条件：

```text
Accepted=True
ResolvedRefs=True
Programmed=True
```

如果出现 `Conflicted=True`，先检查是否已经有另一个 `ClientTrafficPolicy` 指向 `aisphere-gateway`。本包沿用了 `aisphere-sanitize-headers` 这个名字，目的是和 IAM 的 gateway-wide header 清洗合并为同一份策略。

## 6. 验收顺序

先测 Pod 内部服务，再测 Gateway。这样出问题时能快速判断是应用配置还是入口配置。

### 6.1 后端和前端

```bash
kubectl rollout status -n aisphere deploy/aisphere-hub
kubectl rollout status -n aisphere deploy/aisphere-hub-frontend

kubectl port-forward -n aisphere svc/aisphere-hub 18001:18001
curl -i http://127.0.0.1:18001/healthz
curl -i http://127.0.0.1:18001/readyz

kubectl port-forward -n aisphere svc/aisphere-hub-frontend 3000:3000
curl -i http://127.0.0.1:3000/api
```

`readyz` 会检查 PostgreSQL、Redis 和 SpiceDB。MinIO 故障不会让整个 Hub not-ready，但上传、下载会单独失败。

### 6.2 HTTP OIDC

```bash
curl -i https://hub.example.com/healthz
curl -i https://hub.example.com/
```

预期：

- `/healthz` 返回 200，不跳登录。
- `/` 返回 302 到 Casdoor；浏览器完成登录后回到 `/oauth2/callback`，最后进入 Hub 页面。
- 页面调用 `/v1/authn/me`、`/v1/skills`、`/v1/iam/me` 时使用同一个 Gateway OIDC session。

检查伪造内部头是否被清理：

```bash
curl -i https://hub.example.com/v1/skills \
  -H 'x-aisphere-principal: user:admin' \
  -H 'x-aisphere-auth-verified: true'
```

伪造 header 不能绕过 OIDC/JWT。

### 6.3 gRPC

拿到 Casdoor access token 后：

```bash
grpcurl \
  -H 'Authorization: Bearer <access-token>' \
  grpc.hub.example.com:443 \
  list
```

当前服务没有启用 gRPC server reflection 时，`list` 可能返回 reflection 未实现。此时用本仓库 proto 调具体 RPC：

```bash
grpcurl \
  -H 'Authorization: Bearer <access-token>' \
  -import-path api \
  -proto skill/v1/skill.proto \
  -d '{"pageSize": 10}' \
  grpc.hub.example.com:443 \
  skill.v1.SkillService/ListSkills
```

不带 token 应返回 `Unauthenticated`；无权限应由 Hub/SpiceDB 返回 `PermissionDenied`。

## 7. 为什么没有直接启用 IAM gRPC ExtAuth

现在有个容易混淆的点：`GRPCRoute` 和 `SecurityPolicy.extAuth.grpc` 不是一回事。

- `GRPCRoute`：把客户端 gRPC 请求转发到 Hub 的 `19001`。
- `extAuth.grpc`：Envoy 在转发业务请求前，固定调用 `envoy.service.auth.v3.Authorization/Check` 做前置授权。

当前 `aisphere-iam:19080` 提供的是 `iam.v1.IAMAuthService`、Directory、Permission 等普通业务 RPC，没有注册 Envoy v3 `Authorization/Check`。所以直接这样配置是错的：

```yaml
extAuth:
  grpc:
    backendRefs:
      - name: aisphere-iam
        port: 19080
```

它不会自动把 IAM 的 `CheckPermission` 变成 Envoy Check，实际结果是 gRPC `UNIMPLEMENTED`，而 `failOpen: false` 会让所有业务请求失败。

`examples/iam-grpc-extauth-not-ready.yaml` 只记录未来接法，不在默认 kustomization 中。要正式启用，需要先在 IAM 做一层 Envoy adapter：

1. 实现 `envoy.service.auth.v3.AuthorizationServer`。
2. 从 `CheckRequest.attributes.request` 读取 method、path、host、Authorization、route metadata。
3. 验证 Casdoor identity，并映射为稳定的 Aisphere principal。
4. 根据 route/RPC 映射得到 resource + permission，调用 IAM/SpiceDB。
5. `OkResponse` 注入可信 principal/decision headers；deny 返回明确的 `Unauthenticated` 或 `PermissionDenied`。
6. Gateway 到 IAM 至少加 NetworkPolicy，生产再加 TLS/mTLS 和 `BackendTLSPolicy`。

这一步应该在 IAM/Kernel 单独开分支做，不能只靠 Hub 部署 YAML 假装已经实现。

## 8. 回滚

镜像用 `sha-*` 时，回滚只需要恢复两个 Deployment 的 image：

```bash
kubectl set image -n aisphere deploy/aisphere-hub \
  backend=registry.cn-beijing.aliyuncs.com/ainfracn/aisphere-hub:sha-<old-sha>

kubectl set image -n aisphere deploy/aisphere-hub-frontend \
  frontend=registry.cn-beijing.aliyuncs.com/ainfracn/aisphere-hub-frontend:sha-<old-sha>

kubectl rollout status -n aisphere deploy/aisphere-hub
kubectl rollout status -n aisphere deploy/aisphere-hub-frontend
```

如果只想撤掉入口：

```bash
kubectl delete httproute -n aisphere \
  aisphere-hub-public aisphere-hub-protected
kubectl delete grpcroute -n aisphere aisphere-hub-grpc
kubectl delete securitypolicy -n aisphere \
  aisphere-hub-http-security aisphere-hub-grpc-security
```

不要随手删除 gateway-wide 的 `aisphere-sanitize-headers`，IAM 和其他服务可能也在使用它。

## 9. 参考

- [Envoy Gateway OIDC Authentication](https://gateway.envoyproxy.io/docs/tasks/security/oidc/)
- [Envoy Gateway External Authorization](https://gateway.envoyproxy.io/docs/tasks/security/ext-auth/)
- [Envoy Gateway GRPCRoute](https://gateway.envoyproxy.io/docs/api/gateway_api/grpcroute/)
- [Envoy Gateway SecurityPolicy API](https://gateway.envoyproxy.io/latest/api/extension_types/#securitypolicy)
