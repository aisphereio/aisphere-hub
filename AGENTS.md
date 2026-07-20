# Aisphere Hub Agent 规范

本仓库是 Kernel 体系下的 Hub 服务仓库，不是 Kernel layout 模板仓库。AI Agent 和人类开发者必须遵守以下约束。

## 1. 模块路径

- `go.mod` 必须使用 `module github.com/aisphereio/aisphere-hub`。
- 所有内部 import 必须使用 `github.com/aisphereio/aisphere-hub/...`。
- 禁止重新引入 `module aisphere-hub` 或 `import "aisphere-hub/..."`。
- 本地多仓联调用 `go.work`，不要把短模块路径作为长期方案。

## 2. Kernel contract 优先

- 新 RPC 必须先写 proto contract。
- 对外 RPC 必须同时声明 `google.api.http` 和 `aisphere.access.v1.policy`。
- 修改 proto 后必须运行 `make api && make proto-check`。
- 如果 Kernel generator 不能表达需求，先修 Kernel generator，再改 Hub 业务代码。

### 2.1 HTTP JSON 编码契约（camelCase / snake_case）

Kernel `transportx/http` 按 **请求 `Content-Type`** 选择 JSON codec，前端发送什么编码就必须能用什么编码解码，改 proto/HTTP handler 时不要破坏这个契约。

| 请求 Content-Type | codec | 接受的字段名 |
| --- | --- | --- |
| `application/json` | `encoding/json`（stdlib） | **仅 snake_case**（只认 `*.pb.go` struct tag，如 `json:"org_id,omitempty"`） |
| `application/protojson` | `google.golang.org/protobuf/encoding/protojson` | **camelCase 与 snake_case 都接受**（proto JSON 规范） |

- 前端 `aisphere-hub-front` 的 orval 生成类型是 camelCase（`orgId`、`projectId`、`displayName`），`JSON.stringify` 原样发出 camelCase 字段名。前端 `hubFetch` 因此**强制**把带 body 请求的 `Content-Type` 覆盖为 `application/protojson`（覆盖而非默认，因为 orval 生成函数硬编码了 `application/json`）。详见前端 `src/lib/api/README.md` 与 `openwiki/operations/api-layer.md`。
- 后端规则：
  - 生成的 HTTP handler（`*_http.pb.go` 经 `ctx.Bind`）走 `transportx` codec 路由，按上表行为执行，无需手写。
  - **禁止**新增用 `encoding/json` 直接解码 proto 请求体、却期望 camelCase 字段的手写 HTTP handler——前端发 camelCase 会静默丢字段（如 `orgId` 解析为空，触发 `ORG_ID_REQUIRED`）。如确需手写 handler，要么显式读 `application/protojson` 走 `protojson.Unmarshal`，要么要求前端按 snake_case 发送。
  - 响应侧统一走 protojson 编码，输出是合法 JSON，前端 `JSON.parse` 无需改动。
  - 改 proto 字段名后，前端契约受影响：snake_case（struct tag）和 camelCase（proto JSON 名）都会跟着变，跑 `scripts/sync-contract.mjs` + `npx orval --config orval.config.ts` 重新生成前端 client。

## 3. 访问控制硬规则

Hub 是业务服务，不能只依赖 Gateway 防护。

- HTTP/gRPC server 必须接入 authn middleware（`internal/server/authn_middleware.go` / `authn_grpc.go`）。
- `PUBLIC` 只允许登录跳转、授权码交换等明确公开接口。
- `AUTHENTICATED` 接口必须经过 authn middleware 验证 Bearer token 或 Gateway trusted headers。
- `AUTHORIZED` 接口必须经过 `accessx.Guard`，并写 audit。
- 不允许在 service 方法里长期手写 Bearer token 解析作为主鉴权链路。

## 4. AuthN 认证模式

Hub 支持两种认证模式（配置 `security.authn.mode`）：

| 模式 | 说明 | 适用场景 |
| --- | --- | --- |
| `casdoor_jwt` | 后端用 `kernel/authn/oidcx` 再验一次 JWT | 当前测试阶段 |
| `gateway_trusted` | 信任 Gateway 注入的 X-Aisphere-* headers | 生产推荐（需 mTLS/NetworkPolicy） |

详见 `docs/authn/`（`gateway-trusted-internal-token.md`、`casdoor-jwks-backend-verify.md`、`authn-auto-wiring.md`）。

## 5. Service/Biz/Data 分层

- `service` 只做 DTO 转换和调用 usecase，不写业务规则，不直接访问数据库/对象存储/SpiceDB。
- `biz` 负责用例编排、业务校验、状态机、权限语义和审计事件。
- `data` 负责持久化和 Kernel provider adapter 调用。PostgreSQL、MinIO、Redis、SpiceDB、Casdoor 等具体依赖只能在这里接入。
- 启动期 bootstrap 要幂等。Schema/bootstrap/relationship projection 可以在服务启动时修复，但必须可重复执行。

## 6. Skill 存储规范

- PostgreSQL 是 control plane：技能元数据、版本状态、文件索引、manifest。
- MinIO/S3 是 data plane：包内容、草稿文件内容、大对象、可下载产物。
- 文件写入采用 S3-first 或 staging + metadata transaction；DB 失败要补偿删除对象，S3 失败不能写入已成功的 DB 元数据。
- 下载接口使用 ETag/sha256/If-None-Match；大文件优先走 presigned URL 或 streaming。

## 7. 本地工具链

如果同时修改了 Kernel generator，先在本仓库安装本地 generator：

```powershell
make tools-local KERNEL_LOCAL=../kernel
make api
make proto-check
make test
```

## 8. 文档门禁

以下变化必须同步 README 或 `docs/*.md`：启动依赖、端口、Casdoor/SpiceDB/etcd 配置、access policy、Gateway route registry、Kernel generator 使用方式、认证模式变更。

## 9. 提交前检查

```powershell
cd E:\coding\aisphereio\aisphere-hub
go build ./...
go test ./... -count=1 -short
```