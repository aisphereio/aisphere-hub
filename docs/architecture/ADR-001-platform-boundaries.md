# ADR-001: AISphere 平台边界与 Hub 控制面职责

- 状态：Accepted
- 日期：2026-08-06
- 适用仓库：`aisphere-hub`

## 背景

AISphere 当前在 Hub、AgentKit Runtime 与 Sandbox 中重复建设 Agent、Skill、Tool、Model、Run、Approval、Environment 和执行能力，形成了两套控制面与两套 Agent Loop。继续以兼容方式叠加功能会进一步扩大职责重叠。

本 ADR 采用破坏性重构策略，先固定平台边界，再迁移代码。

## 决策

AISphere 分为四个后台平面：

1. **Hub：AI 资产控制面**，回答“什么可以运行”。
2. **Runtime：Agent 执行面**，回答“这一次如何运行”。
3. **Sandbox：隔离计算资源面**，回答“不可信动作在哪里执行”。
4. **IAM：身份与授权面**，回答“谁可以使用什么”。

统一 Console 可以聚合展示多个平面的数据，但不得改变数据所有权。

## Hub 拥有的对象

Hub 是以下定义资产的唯一事实源：

- `AgentDefinition`
- `AgentRevision`
- `SkillPackage`
- `SkillVersion`
- `ToolProvider`
- `ToolDefinition`
- `ToolVersion`
- `ModelProfile`
- `SandboxProfile`
- `PolicyTemplate`
- 资产发布、版本、可见性、分享关系与治理元数据

这些对象必须可版本化、可发布、可回滚，并在发布后保持不可变。

## Hub 的核心输出

Hub 的核心运行时契约是：

```text
Resolve Published AgentRevision
  -> immutable ExecutionSpec
```

`ExecutionSpec` 描述某个已发布 AgentRevision 的确定性依赖，包括固定版本的 Model、Skills、Tools、SandboxProfile 和策略引用。

Hub 可以进行定义级 IAM 校验，但不拥有一次 Run 的生命周期。

## Hub 不拥有的对象

以下对象不得在 Hub 中继续发展为事实源：

- `Conversation`
- `Session`
- `Run`
- `RunSnapshot` / `ExecutionSnapshot`
- `ApprovalRequest` / `ApprovalGrant`
- `ToolInvocation`
- `SandboxLease`
- `Workspace`
- `RuntimeEvent` / Runtime Trace
- 长期 Memory 与会话摘要

Hub 前端可以展示这些信息，但必须通过 Runtime 或 Sandbox API 查询。

## 禁止事项

后续不得在 Hub 中新增：

- Agent Loop 或模型调用逻辑
- 高频 Tool 数据面
- Sandbox Pod/PVC/Service 控制逻辑
- Run 状态机
- Approval 状态机
- Runtime Memory Store
- 以兼容为由复制 Runtime 数据

## 与 Runtime 的边界契约

Hub 向 Runtime 提供：

```text
AgentRevision + immutable ExecutionSpec
```

Runtime 自行创建并持久化：

```text
Run + ExecutionSnapshot + Approval + Invocation + Event
```

同一个 AgentRevision 可以产生多个 Run，但 Hub 不感知 Agent Loop 的中间步骤。

## 迁移原则

1. 新功能先按本 ADR 判断数据所有权。
2. Hub 中现有运行态页面保留为只读聚合视图。
3. 已存在的运行态表和 API 逐步迁移至 Runtime，不建立双写长期方案。
4. 所有已发布资产绑定具体版本和 digest，禁止运行时隐式选择 latest。
5. 共享契约逐步迁入独立、强类型的 contract 包，禁止关键字段继续依赖 `map[string]any`。

## 成功标准

- Hub 可以独立完成 Agent、Skill、Tool、ModelProfile、SandboxProfile 的创建与发布。
- Runtime 只依赖 Hub 的不可变 ExecutionSpec，不依赖 Hub 的运行状态。
- Hub 下线不影响已持久化 Run 的恢复执行。
- Hub 不持有 K8s 高权限，也不承接高频 Tool 调用。
