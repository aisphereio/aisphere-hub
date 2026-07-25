# Agent Sandbox 后续开发计划

> 状态：开发中  
> 基线日期：2026-07-25  
> 适用仓库：`aisphere-hub`、`aisphere-hub-frontend`，后续涉及 AISphere Runtime  
> 关联文档：
>
> - `docs/ai/kubernetes-environment-management-design.md`
> - `docs/deploy/agent-sandbox/README.md`
>
> 本文档用于定义 Hub 集成 `kubernetes-sigs/agent-sandbox` 后的后续开发边界、优先级、PR 拆分和验收标准。它是执行计划，不替代架构设计文档。

---

## 1. 当前基线

当前 Hub 已完成 Agent Sandbox 管理的主体结构：

- Hub 已具备 Cluster、Namespace 环境管理能力；
- 已接入 `agent-sandbox` v0.5.2 的四类 CRD：
  - `Sandbox`
  - `SandboxTemplate`
  - `SandboxWarmPool`
  - `SandboxClaim`
- 后端已提供 `SandboxService`，覆盖 Template、Sandbox、WarmPool、Claim 和工具协议；
- PostgreSQL 已建立对应的四张控制面表；
- Provider 使用 `unstructured.Unstructured` 和 Server-Side Apply，不向业务 API 泄漏上游 CRD Go 类型；
- 前端已接入生成的 Orval SDK，并提供沙箱模板和沙箱运行环境页面；
- 普通 Sandbox 已在测试环境完成真实链路验证：

```text
Create SandboxTemplate
  -> Create Sandbox
  -> Hub DB record
  -> Sandbox CRD
  -> Pod Running
  -> List/Get/Sync
  -> Delete Sandbox
  -> CRD/Pod/DB cleanup
```

当前产品状态应定义为：

> 普通 Sandbox 核心 CRUD 已达到 MVP；WarmPool、Claim 和 Tool Execution 仍属于 Preview 或 Stub。

### 1.1 当前完成度判断

| 模块 | 状态 | 估算完成度 |
|---|---|---:|
| Cluster / Namespace 基础环境管理 | 已落地，可作为 Sandbox 前置能力 | 85% |
| SandboxTemplate | 后端、Provider、前端均已接入 | 70% |
| 普通 Sandbox CRUD | 已完成真实 K8s E2E | 80%–85% |
| Sandbox 状态同步 | 有手工 Sync，缺持续 Reconcile | 60% |
| WarmPool | API、数据库、Provider、页面存在，未完整 E2E | 40%–45% |
| SandboxClaim | API、数据库、Provider、页面存在，未完整 E2E | 35%–40% |
| Sandbox Tool | Schema 和 UI 存在，实际执行为 Stub | 20% |
| Suspend / Resume | 仅有状态模型，无动作 RPC | 15% |
| TTL / Idle / Quota | 基本未实现 | 10% |

---

## 2. 开发原则

### 2.1 先收敛 MVP，不做过早通用化

近期不进行大规模 `Environment` / `EnvironmentInstance` 抽象重构，也不同时接入 OpenSandbox。先把当前 Agent Sandbox 主链路做稳定：

```text
Cluster
  -> Namespace
  -> SandboxTemplate
  -> Sandbox
  -> Pod Ready
  -> Runtime Access
  -> Suspend/Delete/Cleanup
```

待 Agent Sandbox 达到稳定 Beta，再抽象公共 Environment Provider 层。

### 2.2 Hub 负责控制面，Runtime 负责执行面

职责边界必须保持：

```text
Hub
  - 环境目录
  - Template / Sandbox 生命周期
  - 权限
  - 状态
  - 审计
  - 配额

Runtime
  - Agent 身份和会话
  - Tool Policy
  - Workspace / Process / Browser 执行
  - 超时与输出限制
  - 活跃时间上报
```

禁止让 Sandbox 内 Agent 直接获得 K8s Credential、ServiceAccount Token 或任意 Hub 权限。Agent 默认零信任，所有带权限动作经 Runtime 执行。

### 2.3 真实状态以 K8s 为准

Server-Side Apply 成功只表示 API Server 接收资源，不代表 Pod Ready。Hub 的 `READY` 必须最终由 CRD condition 或 Pod 状态驱动。

### 2.4 前端不得宣称未实现能力成功

任何 Stub、Preview 或未完成 E2E 的能力必须显式标记。前端不能因后端返回 accepted 就展示“执行成功”。

---

## 3. 阶段一：收敛现有 Sandbox MVP

目标：修复当前 P0 问题，使普通 Sandbox 主链路可信、可维护、可持续迭代。

### PR 1：修复前端 Sandbox 创建逻辑

#### 问题

当前普通 Sandbox 创建按钮可能在未选择 Template 时被 WarmPool 数量错误启用，但表单并未发送 `warmPoolId`。

#### 修改

- 普通 Sandbox 只允许通过 `SandboxTemplate` 创建；
- 创建按钮条件调整为：

```ts
const canCreate = Boolean(
  activeNamespaceId &&
  sandboxForm.name.trim() &&
  sandboxForm.templateId
);
```

- WarmPool 获取 Sandbox 统一走 `CreateSandboxClaim`；
- 增加 DNS-1123 名称校验；
- Template 非 `READY` 时禁止选择；
- 创建失败时展示 `healthMessage` 和规范化错误；
- 创建成功后刷新并轮询状态，不直接假设 READY。

#### 验收标准

- 未选择 Template 时无法发送创建请求；
- 非法名称不会进入后端；
- 非 READY Template 不可用于创建；
- 创建后页面可观察 `CREATING -> READY/FAILED`。

### PR 2：显式处理 Tool Execution Stub

#### 问题

当前 `CallSandboxTool` 只验证权限和工具名，返回 accepted，并未真正执行工具。前端可能显示“调用成功”。

#### 修改

短期方案：

- 后端 `CallSandboxTool` 返回标准 `UNIMPLEMENTED`；
- 错误码建议使用 `SANDBOX_TOOL_EXECUTION_NOT_AVAILABLE`；
- 前端工具区标记“实验性，尚未连接 Runtime 执行器”；
- 隐藏或禁用实际调用按钮，仅保留工具 Schema 查看。

#### 验收标准

- 用户不会收到虚假的执行成功提示；
- API 语义清晰，可被 Runtime 接入后兼容替换；
- `ListSandboxTools` 仍可用于展示工具协议。

### PR 3：修复 WarmPool / Claim 删除授权契约

#### 问题

WarmPool 和 Claim 未创建独立 SpiceDB 对象，但删除 RPC 当前可能使用 `k8s_sandbox:{id}` 进行授权，与 Biz 层父 Namespace 授权模型不一致。

#### 修改

建议调整为显式 Namespace 作用域：

```http
DELETE /v1/namespaces/{namespace_id}/warm-pools/{id}
DELETE /v1/namespaces/{namespace_id}/sandbox-claims/{id}
```

授权使用：

```text
operate on k8s_namespace:{namespace_id}
```

Biz 层必须验证：

```text
resource.namespace_id == request.namespace_id
```

最终授权矩阵：

| 资源 | 创建 | 查看 | 删除/管理 |
|---|---|---|---|
| SandboxTemplate | Cluster `operate` | Cluster `view` | Cluster `operate` |
| Sandbox | Namespace `use` | Sandbox `use` | Sandbox `manage` |
| WarmPool | Namespace `use` | Namespace `use` | Namespace `operate` |
| SandboxClaim | Namespace `use` | Namespace `use` | Namespace `operate` |

#### 验收标准

- 无权限用户返回 403；
- 不能跨 Namespace 删除资源；
- Proto、OpenAPI、生成代码、前端 Adapter 同步更新；
- Contract check 无破坏性遗漏。

### PR 4：恢复完整 CI 和 Contract Gate

#### 修改

- 统一 `go.mod`、CI、contract lock 中的 Kernel 版本；
- 重新生成并提交：
  - protobuf Go 代码；
  - HTTP / gRPC bindings；
  - authz / gateway metadata；
  - Swagger / OpenAPI；
  - contract bundle；
  - 前端 Orval SDK；
- 补充 Sandbox 专项测试；
- 确保完整执行而非因版本校验提前跳过：

```text
make contract-check
go test ./... -count=1
go build ./...
Kubernetes manifests render
frontend lint
afrontend typecheck
frontend test
frontend build
container image build
```

> 注：上方 `afrontend typecheck` 实施时应使用仓库实际脚本名，例如 `npm run typecheck`；文档落地后可在首次执行 PR 中一并校正为准确命令。

#### 验收标准

- Hub 后端 main 全部质量门禁绿色；
- Hub frontend main 全部质量门禁绿色；
- Sandbox API 生成物无未提交 Diff；
- Sandbox 镜像构建成功。

---

## 4. 阶段二：跑通 WarmPool 和 SandboxClaim

目标：把现有 Preview 页面升级为真实可用能力。

### PR 5：校准 WarmPool CRD 与 Provider

#### 工作内容

从目标集群读取实际 CRD Schema：

```bash
kubectl get crd sandboxwarmpools.extensions.agents.x-k8s.io -o yaml
```

逐项确认：

- `apiVersion` / `kind`；
- `spec.replicas`；
- `spec.sandboxTemplateRef`；
- Template 的作用域要求；
- `status.readyReplicas`；
- Ready condition；
- Finalizer 和删除语义。

Provider 的字段必须以安装版本的 CRD OpenAPI Schema 为准，禁止只参考示例 YAML。

#### E2E

```text
Create SandboxTemplate
  -> Create WarmPool
  -> CRD accepted
  -> warm pods created
  -> readyReplicas == replicas
  -> Hub status READY
  -> Delete WarmPool
  -> CRD and pods removed
```

#### 验收标准

- WarmPool 可稳定创建和删除；
- `readyReplicas` 正确同步；
- Controller 错误可进入 `healthMessage`；
- 删除后不存在孤儿 Pod。

### PR 6：完成 SandboxClaim 与 Sandbox 关联

#### 设计

Claim 成功后必须在 Hub 中形成可操作的 Sandbox 记录：

```text
SandboxClaim
  -> sandbox_id
  -> Hub Sandbox
```

Claim Ready 后创建或 Upsert Sandbox：

```text
template_id = NULL
warm_pool_id = <pool id>
claim_id = <claim id>
```

同步字段：

- `sandbox_kube_name`；
- `sandbox_pod_ip`；
- Hub `sandbox_id`；
- lifecycle；
- owner；
- permissions。

需要明确删除语义：

- 默认删除 Claim 不自动删除已交付 Sandbox；
- 显式 cascade 才同时删除 Sandbox；
- 所有删除必须可审计和幂等。

#### E2E

```text
Create Template
  -> Create WarmPool
  -> Wait Ready
  -> Create Claim
  -> Claim Ready
  -> Hub Sandbox created/linked
  -> Sandbox usable
  -> Delete Claim
  -> Delete WarmPool
```

#### 验收标准

- Claim 能从 Pool 分配 Sandbox；
- `sandbox_id` 正确回填；
- Hub 可直接进入对应 Sandbox 详情；
- 删除策略无资源误删。

### PR 7：补齐 WarmPool / Claim 前端能力

WarmPool：

- 状态、期望副本、就绪副本；
- 错误展示；
- 删除；
- 手动刷新；
- 跳转到相关 Claim / Sandbox。

Claim：

- `PENDING / READY / FAILED`；
- 关联 Sandbox；
- 删除；
- 跳转 Sandbox 详情；
- 失败原因。

所有按钮必须以后端返回的 permissions 为准，禁止前端自行推断角色。

---

## 5. 阶段三：生命周期与状态调谐

目标：让 Hub 的状态真实反映 K8s，并支持暂停和恢复。

### PR 8：Sandbox Reconcile Worker

#### 当前问题

Apply 成功后立即标记 READY 过于乐观。

#### 状态流

```text
CREATING
  -> CRD_APPLIED
  -> POD_PENDING
  -> READY
```

对外枚举可保持简化，但 `READY` 必须来自 CRD `Ready=True` 或等价可信状态。

#### 实现

第一阶段采用周期 Reconcile，不急于引入长期 Informer：

- 每 5–10 秒扫描：
  - `CREATING`
  - `SUSPENDED`
  - `TERMINATING`
  - 长时间未同步记录
- 调用 Provider `GetSandboxStatus`；
- 同步：
  - lifecycle；
  - podName；
  - podIP；
  - nodeName；
  - image；
  - operatingMode；
  - healthMessage；
  - lastSyncAt。

使用 Kernel `taskx` 和 Redis ownership-checked lease，保证多副本只有一个有效调谐者。

#### 验收标准

- 创建接口返回 `CREATING`；
- Pod Ready 后自动进入 READY；
- ImagePull、调度或容器启动失败进入 FAILED；
- 不依赖人工点击 Sync。

### PR 9：Suspend / Resume

增加动作型 RPC：

```proto
rpc SuspendSandbox(SuspendSandboxRequest)
rpc ResumeSandbox(ResumeSandboxRequest)
```

HTTP：

```http
POST /v1/sandboxes/{id}:suspend
POST /v1/sandboxes/{id}:resume
```

权限：

```text
manage on k8s_sandbox:{id}
```

Provider 通过 SSA 修改 `spec.operatingMode`：

```yaml
spec:
  operatingMode: Suspended
```

或：

```yaml
spec:
  operatingMode: Running
```

状态机约束：

```text
READY -> SUSPENDED
SUSPENDED -> CREATING/READY
```

禁止：

```text
FAILED -> SUSPENDED
TERMINATING -> RESUME
DELETED -> RESUME
```

#### 验收标准

- 暂停和恢复行为与 Operator 一致；
- 状态由 Reconcile 最终确认；
- revision 冲突有明确错误；
- 操作进入 Audit。

### PR 10：Network Mode 和资源模板

`network_mode` 必须映射为真实网络策略，不能只更新数据库。

建议语义：

```text
OFFLINE -> 默认拒绝 egress
ONLINE  -> 按 Zone / Template 策略开放
```

实现可选：

- Kubernetes NetworkPolicy；
- CiliumNetworkPolicy；
- Runtime 出网代理和域名 allowlist。

SandboxTemplate 增加：

- CPU request / limit；
- Memory request / limit；
- Ephemeral storage；
- Workspace size；
- `runtimeClassName`；
- `serviceAccountName`；
- Network policy profile。

资源配置优先放在 Template，不要求用户每次创建 Sandbox 重复输入。

---

## 6. 阶段四：Serverless 生命周期与配额

目标：支持无流量休眠、按需恢复、到期回收和成本控制。

### PR 11：Idle Timeout 和 TTL

增加配置：

```text
idle_timeout_seconds
ttl_seconds
shutdown_policy
```

建议语义：

| 配置 | 行为 |
|---|---|
| `idle_timeout_seconds` | 超过指定时间无 Runtime 调用则 Suspend |
| `ttl_seconds` | 最大生命周期，到期删除 |
| `shutdown_policy=Retain` | 暂停或删除计算资源时保留 Workspace |
| `shutdown_policy=Delete` | 到期清理计算和持久化资源 |

需要记录：

```text
last_activity_at
last_runtime_call_at
expires_at
```

Runtime 每次成功访问 Sandbox 时上报活动时间。

Cleanup Worker：

```text
idle Sandbox -> Suspend
expired Sandbox -> Delete
TERMINATING timeout -> retry / orphan alert
```

### PR 12：Quota 和限流

至少支持：

- 每个用户最大 Sandbox 数；
- 每个 Namespace 最大 Sandbox 数；
- 每个用户最大 WarmPool 副本；
- 单 Sandbox CPU / 内存上限；
- 每日创建次数；
- 并发 Tool Call 数；
- 每次 Tool Call 输出和执行时长。

第一版可使用 Zone 级配置，后续再引入 `sandbox_quotas` 和 usage counter。

#### 验收标准

- 超配额请求在创建 K8s 资源前失败；
- 返回标准错误和当前用量；
- 多副本并发不会突破配额；
- 配额事件可审计。

---

## 7. 阶段五：Runtime Tool Gateway

目标：在零信任模型下，让 Agent 真正使用 Sandbox。

### PR 13：Runtime Tool Protocol

推荐调用链：

```text
Agent
  -> AISphere Runtime
  -> Tool Policy
  -> Sandbox Tool Gateway
  -> Sandbox Router / Sidecar / Pod
```

请求至少包含：

```json
{
  "sandboxId": "...",
  "tool": "workspace.read",
  "input": {},
  "traceId": "...",
  "timeoutSeconds": 30
}
```

Runtime 负责：

- 身份校验；
- Sandbox `use` 权限检查；
- Tool allowlist；
- 输入 Schema 校验；
- 路径限制；
- 超时和输出上限；
- 审计和 Trace；
- 更新 `last_activity_at`。

### PR 14：Workspace 工具

第一批实现：

```text
workspace.list
workspace.read
workspace.write
workspace.mkdir
workspace.delete
workspace.search_text
workspace.search_files
```

安全要求：

- Workspace 固定根目录；
- 禁止 `..` 路径逃逸；
- 禁止符号链接逃逸；
- 单文件和总输出大小限制；
- 删除目录必须显式 `recursive=true`；
- 写入路径策略；
- 超时；
- 并发限制。

实现优先级：

1. Sandbox 内 Sidecar HTTP 服务；
2. Sandbox Router；
3. Kubernetes Exec，仅作为过渡实现。

不建议长期由 Hub 直接使用 Kubernetes Exec，因为权限难收敛、审计粒度差、流式协议复杂。

### PR 15：Process / Browser 工具

Process：

```text
process.exec
process.status
process.kill
```

必须限制：

- 命令超时；
- 最大输出；
- 环境变量；
- 工作目录；
- 并发进程；
- 禁止特权；
- 禁止宿主挂载；
- 网络策略。

Browser 建议通过独立浏览器 Sidecar 实现，避免业务容器直接承担浏览器能力。

---

## 8. 阶段六：安全与生产化

### PR 16：运行时隔离

逐步支持：

```text
runc
gVisor
Kata
```

生产默认优先考虑 gVisor；Kata 仅在集群具备 KVM 时启用。

Template 增加 `runtime_class_name`，并实施准入校验：

- 禁止 privileged；
- 禁止 hostPID / hostIPC / hostNetwork；
- 禁止 hostPath；
- 禁止 Docker Socket；
- 删除高危 capabilities；
- `readOnlyRootFilesystem` 默认开启；
- seccomp `RuntimeDefault`；
- `runAsNonRoot`；
- 限制 ServiceAccount 和 Token 自动挂载。

### PR 17：最小权限 ServiceAccount

Hub Cluster Credential 不应长期使用 cluster-admin。

Hub 控制面权限仅限需要管理的资源：

```text
sandboxes
sandboxtemplates
sandboxwarmpools
sandboxclaims
pods get/list
networkpolicies
persistentvolumeclaims
```

Runtime Tool Gateway 使用独立 ServiceAccount。

原则：

```text
Hub Control Plane Credential
  != Runtime Tool Execution Credential
```

### PR 18：Metrics、Trace 和 Audit

Metrics：

```text
aisphere_sandbox_create_total
aisphere_sandbox_create_duration_seconds
aisphere_sandbox_status
aisphere_sandbox_reconcile_total
aisphere_sandbox_reconcile_failures_total
aisphere_sandbox_tool_calls_total
aisphere_sandbox_active_count
aisphere_warm_pool_ready_replicas
```

Trace：

```text
hub.sandbox.create
hub.sandbox.delete
hub.sandbox.suspend
hub.sandbox.resume
hub.warm_pool.create
runtime.sandbox.tool.call
kubernetes.sandbox.apply
```

Audit 至少记录：

- 创建者和 Template；
- Suspend / Resume / Delete；
- Tool 名称、结果、耗时和 trace_id；
- 配额拒绝；
- 权限拒绝。

禁止记录：

- kubeconfig；
- Token；
- Secret；
- 文件内容；
- 命令输出全文。

---

## 9. 建议 PR 顺序

| 顺序 | PR | 优先级 | 主要仓库 |
|---:|---|---|---|
| 1 | 修复 Sandbox 创建条件和校验 | P0 | frontend |
| 2 | Tool Stub 显式化 | P0 | hub + frontend |
| 3 | WarmPool / Claim 删除授权契约 | P0 | hub + frontend |
| 4 | Kernel / Contract / CI 对齐 | P0 | hub + frontend |
| 5 | WarmPool CRD 校准和 E2E | P0 | hub |
| 6 | Claim 与 Sandbox 关联和 E2E | P0 | hub |
| 7 | WarmPool / Claim 前端补齐 | P1 | frontend |
| 8 | Sandbox Reconcile Worker | P1 | hub |
| 9 | Suspend / Resume | P1 | hub + frontend |
| 10 | Network Mode 和资源模板 | P1 | hub + frontend |
| 11 | Idle Timeout / TTL | P1 | hub + runtime |
| 12 | Quota | P1 | hub + runtime |
| 13 | Runtime Tool Protocol | P1 | runtime + hub |
| 14 | Workspace Tool Execution | P1 | runtime |
| 15 | Process / Browser Tool | P2 | runtime |
| 16 | gVisor / Kata 和安全策略 | P1 | hub + deploy |
| 17 | 最小权限 ServiceAccount | P1 | deploy + hub |
| 18 | Metrics / Trace / Audit | P1 | hub + runtime |

---

## 10. 里程碑和发布口径

### Milestone 1：Sandbox MVP

范围：PR 1–4。

支持：

```text
Template create/list/delete
Direct Sandbox create/list/get/sync/delete
明确的权限和错误
完整 CI
```

发布口径：仅内部试用。

### Milestone 2：Sandbox Preview

范围：PR 5–9。

支持：

```text
WarmPool
SandboxClaim
真实状态 Reconcile
Suspend / Resume
```

发布口径：可提供给受控测试用户，不承诺生产 SLA。

### Milestone 3：Runtime Sandbox Beta

范围：PR 10–14。

支持：

```text
资源限制
网络模式
Idle / TTL
Quota
真实 Workspace Tool
```

发布口径：可接入 AISphere Agent Runtime，进行业务试点。

### Milestone 4：Production Ready

范围：PR 15–18，并完成专项压测和故障演练。

要求：

```text
强隔离
最小权限
完整审计
可观测性
自动清理
故障恢复
配额和限流
```

发布口径：生产环境正式能力。

---

## 11. 每个 PR 的统一完成定义

每个 Sandbox 相关 PR 必须满足：

- [ ] Proto / OpenAPI 是唯一 API 契约；
- [ ] 前端只使用生成 SDK，不手写同名路由和 DTO；
- [ ] 权限策略、Gateway metadata 和 Biz 二次校验一致；
- [ ] 所有写操作携带 revision 或等价并发保护；
- [ ] 数据库和远端 K8s 部分失败有明确补偿或 Reconcile 策略；
- [ ] Secret、Credential 和 Tool 输入输出不进入普通日志；
- [ ] 单元测试覆盖核心状态机和权限边界；
- [ ] 至少完成一次目标测试集群 E2E；
- [ ] CI、Contract、Build、Manifest Render、Container Build 全绿；
- [ ] 文档同步更新当前状态和已知限制。

---

## 12. 最近一轮执行范围

下一轮开发应严格聚焦以下四项：

1. 修复前端普通 Sandbox 创建条件；
2. 将 Tool Execution Stub 显式化；
3. 修复 WarmPool / Claim 删除权限和路由；
4. 恢复完整 CI 和契约生成链路。

完成后，再开始 WarmPool 和 SandboxClaim 的真实 E2E。未经 E2E 验证的能力不得从 Preview 提升为 Available。