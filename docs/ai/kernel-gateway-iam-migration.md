# AIHub Kernel / Gateway / IAM 迁移说明

AIHub 旧版本已经部分使用 Kernel，但仍停留在旧开发形态：

- `go.mod` 依赖 `github.com/aisphereio/kernel v0.0.9`，并通过 `replace ../kernel` 依赖本地 sibling repo。
- `buf.gen.yaml` 只生成 Go / gRPC / HTTP / OpenAPI，缺少 `protoc-gen-go-authz`、`protoc-gen-go-gateway`、`protoc-gen-go-kernel`。
- Skill proto 主要只有 `google.api.http`，缺少 `aisphere.access.v1.policy` 作为 Gateway/IAM/Kernel 中间件的单一事实来源。
- Hub 自己保留了一套 authn/authz API；后续平台认证授权应统一收敛到 `aisphere-iam`，Hub 只保留业务能力。

## 目标架构

```text
browser / client
  -> aisphere-gateway
    -> route registry(etcd)
      -> aisphere-hub SkillService gRPC
        -> Kernel requestinfo/authn/access middleware
          -> Hub biz/data
          -> IAM / SpiceDB authorization model
```

Gateway 只做边界治理：

- route match
- INTERNAL 屏蔽
- Authorization header 转发
- HTTP -> gRPC dispatch

资源级授权必须由 Hub 自己的 Kernel middleware 和 biz 层执行。Gateway 不应该解析 Skill 资源规则。

## 第一阶段已经完成

- Hub 依赖升级到 `github.com/aisphereio/kernel v0.2.1`。
- 删除 `replace github.com/aisphereio/kernel => ../kernel`，避免发布分支只能在本地 workspace 运行。
- Makefile 对齐 IAM/Gateway，安装 `kernel v0.2.1` 的生成器。
- `buf.gen.yaml` 增加：
  - `protoc-gen-go-errors`
  - `protoc-gen-go-authz`
  - `protoc-gen-go-gateway`
  - `protoc-gen-go-kernel`
  - `protoc-gen-grpc-gateway`
- vendored Kernel access proto：
  - `api/aisphere/options/v1/authz.proto`
  - `api/aisphere/access/v1/access.proto`

## 下一阶段必须完成

### 1. 给 `api/skill/v1/skill.proto` 补 access policy

读接口建议先用 `AUTHENTICATED + audit`，资源可见性仍由 biz 层处理：

```proto
option (aisphere.access.v1.policy) = {
  exposure: AUTHENTICATED
  audit: { enabled: true event: "aihub.skill.get" risk: "low" }
};
```

写接口用 `AUTHORIZED + authz`：

```proto
option (aisphere.access.v1.policy) = {
  exposure: AUTHORIZED
  authz: { action: "update" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }
  audit: { enabled: true event: "aihub.skill.update" risk: "medium" }
};
```

### 2. 重新生成 API

```powershell
cd E:\coding\aisphereio\aisphere-hub
make tools
make api
make proto-check
go mod tidy
go test ./...
```

生成后应出现类似文件：

```text
api/skill/v1/skill_authz.pb.go
api/skill/v1/skill_gateway.pb.go
api/skill/v1/skill_kernel.pb.go
```

### 3. Hub 启动时注册 Skill routes

Hub main 应像 IAM 一样，在配置了 Gateway route registry 时注册 generated module：

```go
serverx.RegisterServiceGatewayRoutes(ctx, routeRegistry,
  skillv1.SkillServiceKernelModule(),
)
```

### 4. Gateway 接入 Hub

Gateway 当前仍然硬编码 IAM invoker。接入 Hub 前应先做 Gateway 泛化：

```text
protoc-gen-go-gateway
  -> generated request factory
  -> generated query binder
  -> generated invoker registration
Gateway main
  -> load modules instead of import business repos manually
```

在 Gateway 泛化前，不要继续把 Hub 的 request factory 手写进 Gateway，否则 Gateway 会变成业务耦合仓库。

## 本地开发顺序

```powershell
cd E:\coding\aisphereio

git -C .\kernel pull
git -C .\aisphere-iam pull
git -C .\aisphere-gateway pull
git -C .\aisphere-hub pull

Remove-Item .\go.work -ErrorAction SilentlyContinue
Remove-Item .\go.work.sum -ErrorAction SilentlyContinue

go work init .\kernel .\aisphere-iam .\aisphere-gateway .\aisphere-hub
go work sync
```

然后在 Hub 内：

```powershell
cd E:\coding\aisphereio\aisphere-hub
make tools
make api
make proto-check
go mod tidy
go test ./...
```

## 发布规则

- main 分支不能依赖 `replace ../kernel`。
- main 分支不能提交 `config.local.yaml`、`.env`、二进制文件。
- Kernel 先 tag，Hub 再依赖正式 Kernel tag。
- Hub route contract 必须来自 proto，不从 Gateway 手写。
