# Hub Architecture Decisions

本目录是 `aisphere-hub` 当前架构方向的权威入口。

## 当前有效决策

- [ADR-001: AISphere 平台边界与 Hub 控制面职责](ADR-001-platform-boundaries.md)
- [ADR-002: Builtin Tool Catalog V1](ADR-002-builtin-tool-catalog-v1.md) — Builtin executable 归 Runtime，Hub 只做 system Tool catalog mirror/Agent binding；V1 登录用户可选择 Builtin，不做独立 Tool 资产 AuthZ。
- [ADR-003: Tool Connector Contract V1](ADR-003-tool-connector-contract-v1.md) — Tool semantics / source-discovery / execution connector 三轴分离；canonical connector 只有 `builtin/service/sandbox/mcp/http`，并给出现有 workspace/browser/skill seeds 的破坏性迁移表。

## 解释优先级

当旧设计文档、历史实现或兼容 API 与本目录中的 Accepted ADR 冲突时，以 Accepted ADR 为准。

当前方向：

```text
Hub = AI Asset Control Plane
Runtime = Agent Execution Plane
Sandbox = Isolated Compute Plane
IAM = Identity and Authorization Plane
```

Hub 后续只继续建设定义资产、版本、发布、分享和 Resolve 契约；Session、Run、Approval、Invocation、SandboxLease 和 Memory 等运行实例迁移到 Runtime/Sandbox 所属平面。

Tool 领域的下一步不是继续扩展旧 `runtime.type + execution.runner/placement` 枚举，而是将现有 proto 破坏性迁移为：

```text
ToolVersion
├── modelContract
├── connector oneof
│   ├── builtin
│   ├── service
│   ├── sandbox
│   ├── mcp
│   └── http
└── policy
```

Proto、generated API、biz/data/service conversion 和旧 version JSON 数据迁移必须在同一实现 PR 中完成；不允许出现 Hub 与 Runtime 各自猜 connector 的双事实模型。
