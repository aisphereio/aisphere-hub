# AIHub Kernel / Gateway / IAM 迁移说明

AIHub 旧版本已经部分使用 Kernel，但仍停留在旧开发形态：

- `go.mod` 依赖旧 Kernel，并通过 `replace ../kernel` 依赖本地 sibling repo。
- `buf.gen.yaml` 只生成 Go / gRPC / HTTP / OpenAPI，缺少 `protoc-gen-go-authz`、`protoc-gen-go-gateway`、`protoc-gen-go-kernel`。
- Skill proto 主要只有 `google.api.http`，缺少 `aisphere.access.v1.policy` 作为 Gateway/IAM/Kernel 中间件的单一事实来源。
- Hub 自己保留了一套 authn/authz API；后续平台认证授权应统一收敛到 `aisphere-iam`，Hub 只保留业务能力。

## 目标架构

```text
browser / client
  -> aisphere-gateway(public profile)
    -> route registry(etcd)
      -> aisphere-hub SkillService gRPC
        -> Kernel requestinfo/authn/access middleware
          -> Hub biz/data
          -> IAM / SpiceDB authorization model
```

Gateway 只做边界治理：

- route match
- public/internal/ops profile 过滤
- Authorization header 转发
- HTTP -> gRPC dispatch

资源级授权必须由 Hub 自己的 Kernel middleware 和 biz 层执行。Gateway 不应该解析 Skill 资源规则。

## Gateway 发布规则

`google.api.http` 只代表服务自身有 HTTP binding，不代表一定暴露到 Gateway。

Gateway manifest 生成规则：

```text
google.api.http + aisphere.access.v1.policy -> GatewayManifest route
只有 google.api.http 没有 access.policy -> 不进入 GatewayManifest
access.policy.gateway.publish = DISABLED -> 不进入 GatewayManifest
```

Hub 的默认 public Gateway 只发布 SkillService 的产品 API，不发布旧平台能力和内部能力：

```text
发布：
  /v1/skills*

不发布：
  /v1/authn/*
  /v1/authz/*
  /v1/audit/*
  /internal/dtm/*
  /healthz
  /readyz
  /metrics
  /debug/*
```

示例：

```proto
option (aisphere.access.v1.policy) = {
  exposure: AUTHORIZED
  authz: { action: "update" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }
  audit: { enabled: true event: "aihub.skill.update" risk: "medium" }
  gateway: { profiles: "public" profiles: "internal" tags: "skill" }
};
```

内部专属、直连专属接口使用：

```proto
option (aisphere.access.v1.policy) = {
  exposure: INTERNAL
  gateway: { publish: DISABLED tags: "dtm" }
};
```

## 已完成

- Hub 依赖升级到 `github.com/aisphereio/kernel v0.2.1`，后续需要升级到包含 Gateway 发布策略的 Kernel 新 tag。
- 删除 `replace github.com/aisphereio/kernel => ../kernel`，避免发布分支只能在本地 workspace 运行。
- Makefile 对齐 IAM/Gateway，安装 Kernel 生成器。
- `buf.gen.yaml` 增加：
  - `protoc-gen-go-errors`
  - `protoc-gen-go-authz`
  - `protoc-gen-go-gateway`
  - `protoc-gen-go-kernel`
  - `protoc-gen-grpc-gateway`
- vendored Kernel access proto：
  - `api/aisphere/options/v1/authz.proto`
  - `api/aisphere/access/v1/access.proto`
- `api/skill/v1/skill.proto` 已开始声明 `aisphere.access.v1.policy`。

## 下一阶段必须完成

### 1. 重新生成 API

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

### 2. Hub 启动时注册 Skill routes

Hub main 应像 IAM 一样，在配置了 Gateway route registry 时注册 generated module，并使用 public filter：

```go
serverx.RegisterServiceGatewayRoutesWithFilter(ctx, routeRegistry,
  gatewayx.PublicRouteFilter(),
  skillv1.SkillServiceKernelModule(),
)
```

### 3. Gateway 接入 Hub

Gateway 当前仍然偏 IAM 编译期接入。接入 Hub 前应继续推进 Gateway 泛化：

```text
protoc-gen-go-gateway
  -> generated request factory
  -> generated query binder
  -> generated invoker registration
Gateway main
  -> load modules instead of hand-writing business mappings
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
- Public Gateway route registry 必须使用 `gatewayx.PublicRouteFilter()`。
