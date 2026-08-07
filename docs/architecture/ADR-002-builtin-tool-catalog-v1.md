# ADR-002: Builtin Tool Catalog V1

- Status: Accepted
- Date: 2026-08-07
- Scope: Hub / Runtime

## Decision

Hub 是 Builtin Tool 的 **catalog mirror / binding control plane**，不是 Builtin executable code 的 Owner。

Builtin executable code 由 AISphere Runtime 拥有并编译进 Runtime binary。Runtime 导出 descriptor-only manifest，Hub 将其镜像为 system/builtin ToolVersion 供 AgentDefinition/AgentRevision 选择。

```text
Runtime source
  -> BuiltinDescriptor + implementation
  -> agentkit-runtime image
  -> Builtin manifest
  -> Hub system Tool catalog mirror

AgentRevision.Tools
  -> explicit ToolVersion binding
  -> ExecutionSpec
  -> Runtime exact local implementation lookup
```

Hub 不保存、下载或向 Runtime 传输 Builtin Go/Python/二进制代码。

## Selection

Builtin Tool 是“平台内置、Agent 可选”，不是“所有 Agent 默认拥有”。

- system Builtin 可以出现在所有已认证用户的 Tool catalog 中；
- Agent 创建/编辑时显式选择 Builtin；
- AgentRevision 必须记录实际选择的 ToolVersion；
- Hub Resolve 只输出 AgentRevision 已绑定的 Tool；
- Runtime 不允许隐藏注入额外 Builtin。

## V1 authorization

Builtin Tool **选择本身不做独立 Tool 资产 AuthZ**。

只要用户已经通过 AISphere AuthN，且有权限创建/编辑对应 Agent，就可以选择 system/builtin Tool。

因此 V1：

- system/builtin Tool 无需 `tool.execute` 才能被 Agent 绑定；
- Builtin 不要求 `tool#viewer/executor` 关系来完成选择；
- 不新增 Builtin catalog 的 per-call asset authorization；
- Agent Tool binding 是能力装配，不是 IAM grant。

当前 Hub `resolveTool` 已按此规则执行：只有非 system 且非 builtin Tool 才要求 `tool.execute`。

此规则不影响目标资源权限。若 Builtin 实际操作 Skill、Git repository、K8s resource 等受保护资源，具体资源服务仍必须使用可信 Principal 对具体 resource/action 做 IAM 检查并 fail closed。

## Descriptor mirror

Runtime manifest 至少包含：

```text
BuiltinDescriptor
├── id
├── implementationVersion
├── model
│   ├── name
│   ├── description
│   ├── inputSchema
│   └── outputSchema
├── annotations
└── digest
```

Hub 将 descriptor 镜像为 immutable system ToolVersion。Hub 不能手工修改 code-owned Builtin schema 后继续声称它对应同一个 implementationVersion/digest。

## Migration

当前 Hub `builtinToolSeeds` 仍是过渡实现，并且混合了 Runtime Builtin 与 Sandbox capability。迁移顺序：

1. Runtime 建立 code-owned `BuiltinRegistry` 与 manifest；
2. 盘点当前 seeds，将真正的 Runtime Builtin 与 `workspace.*` / browser / shell / python 等 Sandbox capability 分开；
3. Hub 增加 Runtime manifest reconcile/import；
4. system Builtin ToolVersion 改由 Runtime manifest 生成；
5. 删除 Hub 对 Builtin schema 的手工双写 source；
6. AgentRevision/ExecutionSpec pin descriptor digest + implementationVersion。

在第 3-5 步完成前，不继续向 Hub `builtinToolSeeds` 增加新的 Runtime Builtin 定义。
