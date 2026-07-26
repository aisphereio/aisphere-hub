# Agent 身份委托与通用特权操作权限模型

> 状态：设计评审中
> 基线日期：2026-07-26
> 适用仓库：`aisphere-hub`、`aisphere-iam`（SpiceDB schema）、`aisphere-git-cli`（`pkg/skillx`）、后续 `aisphere-runtime`（待建）
> 关联文档：
>
> - `docs/ai/sandbox-development-plan.md`（§2.2 控制面/执行面边界、PR13 Runtime Tool Protocol）
> - `docs/ai/kubernetes-environment-management-design.md`（§11 Sandbox 控制面）
> - `docs/ai/skill-release-control-plane.md`（skill 发版控制面）
> - `aisphere-iam/configs/spicedb/aisphere.schema.zed`（SpiceDB schema 权威定义）

---

## 1. 背景与动机

### 1.1 问题

Agent 在沙箱内需要执行一系列**需要凭据/权限的操作**（privileged operations）：

- 拉取已发布 skill（`skill.fetch`）—— 访问 Hub git endpoint
- 推送 skill 草稿 / 打 tag 发版（`skill.publish` / `git.push`）—— 写 Hub git
- 拉取 git 仓库（`git.pull` / `git.clone`）
- 登录外部服务（`external.login`）
- 执行 K8s 命令（未来）

这些操作散落各处、各自处理凭据会失控：凭据泄露、权限粒度不一、审计缺失、重复实现。

### 1.2 核心洞察：身份委托

**Agent 的权限 = 启动它的人的权限**（或启动时显式选定的身份）。

Agent 干的活本来就是用户授权它干的，它应当承袭启动者的权限范围（在用户授权的 subset 内），而不是给 agent 单独建一套权限。这带来三个直接好处：

1. **无需重复授权**：用户 U 是 `skill:ttt1#publisher`，U 启动的 agent 自动能 publish ttt1，不用再给 agent 建关系。
2. **覆盖"代表谁操作"**：U 可以选择以自己身份，或以有权代理的另一个身份（org/project service account）启动 agent —— 即"选择谁的权限"。
3. **审计自然**：每条审计记录是"U 委托 agent A，以 X 身份执行 op on target"，责任链清晰。

### 1.3 与零信任的关系

身份委托**不违反**零信任模型（sandbox-development-plan §2.2："禁止让 Sandbox 内 Agent 直接获得 K8s Credential、ServiceAccount Token 或任意 Hub 权限"）：

- Agent **仍然不持有任何凭据**。凭据由 Runtime 托管。
- Agent 发的是**意图**（"我要拉 ttt1@1.4.2"），Runtime 用**托管的委托凭据**代执行。
- 区别仅在于：Runtime 代执行时用的凭据是"**用户委托身份**"的凭据，而非 Runtime 自身的服务凭据。

即：**agent 零信任（无凭据）+ Runtime 代办（托管凭据）+ 凭据身份 = 启动者委托身份**。三者兼容。

---

## 2. 模型

### 2.1 总览

```
用户 U 启动 agent A
  ├─ 指定 acting_as: U（默认）或 X（U 有权代理的身份）
  └─ Hub 颁发 delegated credential（scoped token，subject = acting_as）
       → 交给 Runtime 托管（agent 永远拿不到）

agent A 在沙箱内发起特权操作 (tool call)
  → AISphere Runtime 收到
     ├─ 解析 acting_as 身份
     ├─ accessx.Guard.Require(Check{Subject=acting_as, Permission=tool, Resource=target})
     │    ├─ SpiceDB: acting_as 对 target 有 tool 权限?
     │    ├─ 放行 → OperationExecutor 用 acting_as 托管凭据代执行
     │    └─ 拒绝 → PERMISSION_DENIED
     └─ 审计: "U 委托 A，以 acting_as 身份执行 tool on target"
```

### 2.2 三个关键设计点

#### 2.2.1 身份选择（acting_as）

启动 agent 时定身份：

| 场景 | acting_as | 说明 |
|---|---|---|
| 个人操作 | U（启动者本人） | 默认。agent 以 U 身份行事，继承 U 的所有 SpiceDB 关系 |
| 代表组织/项目 | org/project 的 service_account | U 选一个有权代理的 SA，agent 以该 SA 身份行事 |
| 代表他人（代理） | 另一个 user（U 被授权代理的） | 需 U 对 target user 有 `delegate` 权限（见 §3.2） |

落地：agent 启动 API 加 `acting_as` 参数（`user:U` / `service_account:SA`，默认 = 启动者）。Hub 校验启动者对 acting_as 有委托权，然后颁发带该 subject 的 scoped token。

#### 2.2.2 凭据托管（Runtime 侧，agent 不碰）

即使 agent 承袭身份，**token 仍在 Runtime 侧托管**：

- Agent 进程内无任何 token / 凭据文件。
- Agent 发"意图"（tool call），Runtime 用托管凭据代执行。
- 托管凭据可吊销/过期，Runtime 自动续期。
- 每次凭据使用进审计。

好处：agent 被攻陷不泄露凭据；凭据生命周期由控制面管；调用全审计。

#### 2.2.3 权限继承（SpiceDB 关系天然支撑）

现状 SpiceDB schema 已用 `user`/`service`/`service_account`/`group#member` 作 subject（见 `aisphere.schema.zed`）：

- `skill` 有 `publisher`/`editor`/`viewer` relation + `publish`/`edit`/`view` permission
- `agent` 有 `operator`/`executor` relation + `operate`/`execute` permission
- `k8s_sandbox` 有 `owner`/`user` relation + `use`/`manage` permission

agent 以 acting_as 身份行事时，`accessx.Check(Subject=acting_as, Permission=publish, Resource=skill:ttt1)` ——acting_as 是 ttt1 的 publisher，自动放行。**不需要给 agent 单独建关系，继承 acting_as 的关系即可。**

---

## 3. 通用特权操作统一处理

### 3.1 Privileged Operation 抽象

所有需要权限的操作统一成一个模型：

| 字段 | 说明 | 示例 |
|---|---|---|
| `tool` | 操作名（SpiceDB permission 名） | `skill.fetch` / `skill.publish` / `git.pull` |
| `target` | 操作对象（SpiceDB resource） | `skill:ttt1` / `skill:ttt1` / `skill:ttt1` |
| `input` | 操作参数（JSON） | `{name,version,dest}` / `{version,notes}` / `{ref}` |
| `acting_as` | 委托身份 | `user:U` / `service_account:SA` |

调用链：

```
Agent → CallSandboxTool(sandboxId, tool, input)
  → Runtime: subject = acting_as (从 sandbox 启动上下文解析)
  → accessx.Guard.Require(Check{Subject, Permission=tool, Resource=target})
     ├─ SpiceDB 授权
     ├─ 放行 → OperationExecutor(input) 用托管凭据执行
     └─ 审计 (U 委托 A 以 acting_as 执行 tool on target)
```

### 3.2 SpiceDB schema 扩展

现状 `skill`/`agent`/`k8s_sandbox` 已有权限定义。需要补的：

**a. tool 级 permission（映射到特权操作）**

`skill` definition 已有 `publish`/`edit`/`view`，覆盖 skill.push/publish/fetch 的授权。但需显式补 `fetch`：

```zed
definition skill {
  // ... 现有 relations ...
  permission fetch = view  // 拉取 = 可见即可拉（fetch 是读操作）
  // publish / edit / view 已存在
}
```

`git.pull`/`git.push` 复用 `skill` 的 `view`/`edit`（git 操作的目标仍是 skill）。

**b. 委托关系（delegation）**

支持 U 启动 agent 以 X 身份行事，需 U 对 X 有委托权：

```zed
definition user {
  relation delegate: user | service_account  // U 可委托给这些身份行事
  permission can_act_as = delegate
}
```

启动 agent 时 `Check(Subject=U, Permission=can_act_as, Resource=user:X)` 放行才允许 `acting_as=X`。默认 `acting_as=U` 不需要此检查。

**c. agent → acting_as 的会话关系（审计用，可选）**

```zed
definition agent_session {
  relation agent: agent
  relation acting_as: user | service_account
  relation launched_by: user
  // 无 permission；纯审计载体
}
```

### 3.3 OperationExecutor 接口（Runtime 侧）

Runtime 侧统一执行器接口，每个特权操作注册一个实现：

```go
// OperationExecutor 执行一个特权操作。Runtime 持有 acting_as 的托管凭据，
// Executor 用该凭据代执行，agent 不接触凭据。
type OperationExecutor interface {
    // Execute 以 acting_as 身份执行操作。
    // credentialProvider 提供 acting_as 的托管凭据（token / SA key）。
    Execute(ctx context.Context, input json.RawMessage, acting_as authn.Principal,
        credentialProvider CredentialProvider) (output json.RawMessage, err error)
}
```

注册表：

| 操作 | Executor | 实现 |
|---|---|---|
| `skill.fetch` | `SkillFetchExecutor` | 调 `skillx.ResolveAndFetch`（已就绪） |
| `skill.publish` | `SkillPublishExecutor` | 调 skillx publish 逻辑 + CAS + 审计 tag |
| `git.pull` / `git.push` | `GitExecutor` | 原生 git + 托管凭据 |
| `external.login` | `ExternalLoginExecutor` | 外部服务登录代办 |

新增特权操作 = 注册一个 Executor + 一个 SpiceDB permission + 一个 tool schema，**框架不变**。

### 3.4 CredentialProvider（凭据托管）

Runtime 侧持有各身份的凭据，按 acting_as 提供：

```go
type CredentialProvider interface {
    // GitToken 返回 acting_as 访问 Hub git endpoint 的 Bearer token。
    GitToken(acting_as authn.Principal) (string, error)
    // ExternalCredential 返回 acting_as 访问外部服务的凭据。
    ExternalCredential(acting_as authn.Principal, service string) (Credential, error)
}
```

凭据来源：
- **user 身份**：Casdoor 颁发的 delegated scoped token（见 §4.1）
- **service_account 身份**：SA 的长期凭据 / Casdoor client-credentials token

Runtime 启动时从 Hub/IAM 领取委托凭据，缓存 + 自动续期，agent 进程不可访问。

---

## 4. 落地

### 4.1 Delegated Credential（委托凭据）机制

**新东西**，需 Casdoor 配合。两种实现路径（选一）：

**路径 A：Casdoor scoped token（推荐）**
- Hub 启动 agent 时，用启动者的 refresh token 向 Casdoor 换一个**带 `acting_as` claim 的短期 access token**（aud = git-cli，subject = acting_as，exp = 短期如 1h）。
- 该 token 只能用于 git endpoint（aud 隔离），不能调 /v1 REST。
- Runtime 拿这个 token 托管，代执行 git 操作时 credential helper 注入。
- Casdoor 需支持在 token 里定制 `acting_as` claim（Casdoor 支持 JWT custom claims，需配置）。

**路径 B：Hub 签发代理 token**
- Hub 自己签发一个短期 JWT（用 Hub 的 key），claim 带 `acting_as` + `delegated_by=U` + `exp`。
- git endpoint 的 authn 中间件除了校验 Casdoor token，也接受 Hub 签发的代理 token。
- 不依赖 Casdoor 定制，但 Hub 要管 key + token 生命周期。

**建议先走路径 A**（复用 Casdoor 现有 token 体系，aud 隔离已验证）。

### 4.2 Hub 控制面改动（不依赖 Runtime，可立即做）

| 改动 | 文件 | 说明 |
|---|---|---|
| `CallSandboxTool` 升级到 Tool 级权限 | `internal/biz/sandbox_usecase.go` | 现在只查 `sandbox.use`；升级为额外查 `{tool} on {target}`（从 input 解析 target） |
| `skill.publish` tool schema | `sandboxToolRegistry` | 和 `skill.fetch` 并列，定义 input schema（name/version/notes） |
| `git.pull` / `git.push` tool schema | `sandboxToolRegistry` | 定义 input schema（ref/branch） |
| agent 启动 API 加 `acting_as` | `api/agent/v1/agent.proto`（待建）+ service | 启动时校验 `can_act_as`，颁发委托凭据 |

前两项**现在就能做**（纯控制面，不依赖 Runtime），后两项依赖 agent API 仓库。

### 4.3 SpiceDB schema 改动（aisphere-iam）

```zed
// skill 加 fetch permission
definition skill {
  // ... 现有 ...
  permission fetch = view
}

// user 加委托关系
definition user {
  relation delegate: user | service_account
  permission can_act_as = delegate
}
```

### 4.4 Runtime 改动（待仓库建立）

- 实现 `OperationExecutor` 注册表 + 各 Executor
- 实现 `CredentialProvider`（从 Hub 领委托凭据，托管）
- 实现 Tool Policy（超时、输出上限、allowlist）
- 接 `accessx.Guard` 做 Tool 级授权
- 这对应 sandbox-development-plan PR13/14

---

## 5. 各特权操作的处理规格

### 5.1 skill.fetch（拉取已发布 skill）

| 维度 | 规格 |
|---|---|
| 时机 | (A) 沙箱启动预装（读 `aisphere.io/skills` annotation，Runtime/sidecar 代拉）或 (B) 运行时按需（agent 调 `skill.fetch`） |
| 权限 | `Check(Subject=acting_as, Permission=fetch, Resource=skill:{name})` —— fetch=view，可见即可拉 |
| 执行 | `SkillFetchExecutor` → `skillx.ResolveAndFetch(name, version)` |
| 凭据 | acting_as 的 git token（Runtime 托管） |
| 落地 | 共享 volume `/skills/{name}/`，agent 直接读 |
| 校验 | manifest SHA-256 与 Hub release 对齐（skillx 已实现） |
| 预装控制面 | ✅ 已就绪（`aisphere.io/skills` annotation，PR#60） |

### 5.2 skill.publish（打 tag 发版）

| 维度 | 规格 |
|---|---|
| 时机 | 运行时按需（agent 调 `skill.publish`）。**不可预装**（发版是对外动作） |
| 权限 | `Check(Subject=acting_as, Permission=publish, Resource=skill:{name})` |
| 执行 | `SkillPublishExecutor` → skillx publish 逻辑（CAS + 审计 tag + push） |
| 凭据 | acting_as 的 git token（需 write 权限） |
| 审计 | tag message 带 `AISphere-Publisher-ID=acting_as` + `AISphere-Delegated-By=U`（新增委托审计字段） |
| 风险 | 发版不可逆。建议初版要求 acting_as 是 `publisher`（非 manage 继承），且高风险 skill 可配二次审批 |

### 5.3 git.pull / git.push（草稿操作）

| 维度 | 规格 |
|---|---|
| 权限 | pull → `view` on skill；push → `edit` on skill |
| 执行 | `GitExecutor` → 原生 git + credential helper 注入托管 token |
| 凭据 | acting_as 的 git token |

### 5.4 external.login（外部服务登录）

| 维度 | 规格 |
|---|---|
| 权限 | `Check(Subject=acting_as, Permission=login, Resource=external_service:{name})` —— 需新 definition |
| 执行 | `ExternalLoginExecutor` → 用 acting_as 的外部凭据代办登录 |
| 凭据 | acting_as 的外部服务凭据（CredentialProvider.ExternalCredential） |

---

## 6. 审计

每次特权操作记录：

| 字段 | 值 |
|---|---|
| `launched_by` | 启动 agent 的用户 U |
| `agent` | agent 标识 |
| `acting_as` | 委托身份（user:U / service_account:SA） |
| `tool` | skill.fetch / skill.publish / ... |
| `target` | skill:ttt1 / ... |
| `decision` | allow / deny |
| `reason` | SpiceDB decision / 权限不足 |
| `trace_id` | 链路 |

skill.publish 的 tag message 额外带 `AISphere-Delegated-By`，让 Hub `releaseForTag` 解析时记录委托链。

---

## 7. 实施分期

### 阶段 1：Hub 控制面铺路（现在可做，不依赖 Runtime）
- [ ] `CallSandboxTool` 升级到 Tool 级权限检查
- [ ] `skill.publish` / `git.pull` / `git.push` tool schema 进 registry
- [ ] SpiceDB schema 加 `skill.fetch` permission + `user.delegate`/`can_act_as`
- [ ] 部署到测试环境，验证 `ListSandboxTools` 暴露新工具、权限检查生效

### 阶段 2：委托凭据机制（依赖 Casdoor 配合）
- [ ] Casdoor 支持 `acting_as` custom claim（路径 A）
- [ ] Hub 颁发 delegated scoped token 的 API
- [ ] agent 启动 API 加 `acting_as` 参数 + `can_act_as` 校验

### 阶段 3：Runtime 执行面（依赖 Runtime 仓库）
- [ ] 建 `aisphere-runtime` 仓库
- [ ] `OperationExecutor` 注册表 + `SkillFetchExecutor`（用 skillx）
- [ ] `CredentialProvider`（从 Hub 领委托凭据，托管）
- [ ] Tool Policy（超时/输出/allowlist）
- [ ] 接 `accessx.Guard` Tool 级授权
- [ ] 端到端：agent 调 skill.fetch → Runtime 代拉 → 落 volume

### 阶段 4：skill.publish 执行面 + 审计
- [ ] `SkillPublishExecutor` + 委托审计字段
- [ ] 高风险 skill 二次审批机制

---

## 8. 不在范围

- Agent 自身的权限管理（agent definition 的 operate/execute 已存在，本文档不改）
- 沙箱强隔离（gVisor/Kata，见 sandbox-development-plan PR16）
- 外部服务凭据的存储后端（CredentialProvider 实现，待 Runtime 设计）
- 非 skill 的 K8s 特权操作（K8s 命令执行，未来单独设计）

---

## 9. 与现有设计的关系

| 现有设计 | 本文档关系 |
|---|---|
| sandbox-development-plan §2.2（Hub=控制面/Runtime=执行面） | 遵守。Runtime 代执行，Hub 只管控制面 + 授权 |
| sandbox-development-plan PR13（Runtime Tool Protocol） | 本文档定义的 Privileged Operation 就是 PR13 tool 的特权子集 |
| sandbox-development-plan PR14（Workspace 工具） | workspace.* 是无特权工具（沙箱内文件操作），与本文档特权操作互补 |
| `aisphere.schema.zed`（SpiceDB schema） | 复用 skill/agent/k8s_sandbox 定义，补 fetch/delegate |
| `pkg/skillx`（Skill SDK） | Runtime 的 SkillFetchExecutor 直接 import |
| `aisphere.io/skills` CRD annotation（PR#60） | 预装式 skill.fetch 的声明来源 |
