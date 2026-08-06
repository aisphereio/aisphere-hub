# Hub Architecture Decisions

本目录是 `aisphere-hub` 当前架构方向的权威入口。

## 当前有效决策

- [ADR-001: AISphere 平台边界与 Hub 控制面职责](ADR-001-platform-boundaries.md)

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
