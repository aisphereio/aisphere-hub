# ADR-003: Tool Connector Contract V1

- Status: Accepted
- Date: 2026-08-07
- Scope: Hub Tool / ToolVersion / ExecutionSpec

## Context

Hub 当前 `ToolDefinition` 把多种概念混在两个结构里：

```text
ToolRuntimeDefinition
  type/server/name/url/method/package/entry_point/headers/config/credential_ref

ToolExecutionDefinition
  placement/runner/image/command/args/working_dir/filesystem/network/
  mounts/env/secret_refs/allow_hosts/deny_hosts/resources/capabilities
```

其中 `runtime.type` 当前甚至允许：

```text
builtin | mcp | openapi | http | function
```

而 `execution.runner` 又允许：

```text
builtin | mcp | stdio | http | container | wasm | python | binary
```

这把业务 Tool、发现协议、传输协议、执行位置、Sandbox package 和 credential 配置混成了一个可自由组合的矩阵，Runtime 只能靠猜测决定执行路径。

AISphere 尚未上线，因此采用破坏性收口，不保留这套结构作为长期兼容模型。

## Decision

Tool V1 分成三个互相独立的维度：

```text
Tool semantics      Tool/ToolVersion 描述“能力是什么”
Source/discovery    描述 ToolVersion 如何被导入/发布
Connector           描述 ToolInvocation 最终由谁执行
```

只有 Connector 可以选择 Runtime Adapter。

Hub ToolVersion 的 canonical Connector 只有：

```text
builtin
service
sandbox
mcp
http
```

`git/python/browser/database/openapi/stdio/container/function/skill` 等都不是 Connector kind。

## Target ToolVersion contract

```text
ToolVersion
├── version / revision / digest
├── modelContract
│   ├── name
│   ├── description
│   ├── inputSchema
│   └── outputSchema
├── connector                  # typed oneof
│   ├── builtin
│   ├── service
│   ├── sandbox
│   ├── mcp
│   └── http
├── policy
│   ├── targetResourceResolver
│   ├── requiredPermissions[]
│   ├── risk
│   ├── approvalMode
│   ├── timeout
│   ├── retry
│   └── idempotency
└── metadata
```

### BuiltinConnector

```text
builtinId
implementationVersion
descriptorDigest
```

Executable code is compiled into Runtime. Hub stores only the immutable catalog mirror.

### ServiceConnector

```text
service
operation
contractVersion
targetResolver?
```

`service` means a trusted first-party AISphere logical service operation. It never stores an arbitrary URL.

Examples:

```text
skill.get / skill.create / skill.share -> Hub service
knowledge.query                        -> future Retrieval service
file.metadata.get                      -> future File service
```

### SandboxConnector

```text
capability
requiredCapabilities[]
packageRef?
```

The model-facing Tool name is separate from the executor capability.

```text
ToolVersion: skill.pull
connector: sandbox
capability: git.checkout
```

The Sandbox capability must exist in the Sandbox capability manifest for the selected profile/lease. Runtime fails closed if it does not.

### MCPConnector

```text
connectionRef
remoteToolName
protocolVersion
discoveredSchemaDigest
```

MCP `tools/list` is a discovery source. Hub pins a discovered remote tool into an immutable ToolVersion; Runtime must not expose an entire MCP server dynamically without an Agent binding/version pin.

### HTTPConnector

```text
connectionRef
method
pathTemplate
requestMapping
responseMapping
```

`OpenAPI` is an importer that produces HTTP ToolVersions. Endpoint/base URL/credential belong to ToolConnection referenced by `connectionRef`, not directly to ToolVersion.

## Policy is not Connector configuration

Business authorization and risk policy must be removed from Sandbox execution config.

Current `execution.capabilities` is migrated to typed Tool policy:

```text
requiredPermissions[]
targetResourceResolver
```

Runtime Tool Broker performs:

```text
IAM target authorization
-> risk / approval
-> credential delegation
-> adapter invocation
-> output validation / redaction / audit
```

Sandbox validates lease/profile/executor capability/resource boundary only. It never becomes the business IAM authority.

## Current system Tool migration

Verified against the current Hub seed and Sandbox executor implementation.

| Tool | Current marker | Target connector | Target capability/operation | Current executability |
| --- | --- | --- | --- | --- |
| `workspace.read` | placement=sandbox, runtime=builtin | sandbox | `workspace.read` | supported |
| `workspace.write` | placement=sandbox, runtime=builtin | sandbox | `workspace.write` | supported |
| `workspace.list` | placement=sandbox, runtime=builtin | sandbox | `workspace.list` | supported |
| `workspace.search_files` | placement=sandbox, runtime=builtin | sandbox | `workspace.search_files` | supported |
| `workspace.search_text` | placement=sandbox, runtime=builtin | sandbox | `workspace.search_text` | supported |
| `browser.open` | placement=sandbox, runtime=builtin | sandbox | `browser.open` | supported |
| `skill.fetch` | placement=runtime, runtime=builtin | sandbox | `git.checkout` / `git.fetch` | **not implemented: fail closed** |
| `skill.pull` | placement=runtime, runtime=builtin | sandbox | `git.pull` | **not implemented: fail closed** |
| `skill.push` | placement=runtime, runtime=builtin | sandbox | `git.push` | **not implemented: fail closed** |
| `skill.tag` | placement=runtime, runtime=builtin | sandbox | `git.tag` | **not implemented: fail closed** |
| `skill.publish` | placement=runtime, runtime=builtin | sandbox | `git.tag` + `git.push` high-level executor contract | **not implemented: fail closed** |

The current five `skill.*` seeds are working-tree/Git operations, not Hub metadata operations. They need target Skill IAM checks and Runtime-delegated short-lived Git credentials, but actual workspace/Git execution belongs in Sandbox.

Future `skill.create/get/share/metadata/promote` operations that do not operate on a Sandbox working tree should use `service` instead.

## Current Sandbox capability facts

Current image actually implements:

```text
workspace.list
workspace.read
workspace.write
workspace.patch
workspace.delete
workspace.mkdir
workspace.search_files
workspace.search_text
shell.exec          # configuration gated
browser.status
browser.open
```

It does **not** currently implement:

```text
git.*
skill.*
python.exec
```

Hub must not seed or publish a system ToolVersion as executable merely because a future capability name has been designed.

## Model-visible capabilities that are not ordinary ToolVersions

The following do not use this Connector taxonomy:

```text
provider-native search/grounding/computer-use -> ModelProfile capability
sub-agent/handoff/task delegation             -> Agent/Runtime orchestration
preload_memory/retrieval prefetch              -> ContextPolicy/Context Builder
Run/Event/Approval/Credential internals         -> Runtime primitives
```

## Migration

1. Runtime defines and validates the typed five-connector domain. Completed in AgentKit Tool V1 branch.
2. Sandbox exposes executor Capability V1 and stops treating its manifest as a model Tool catalog. In progress in aisphere-sandbox stacked PR.
3. Replace Hub `ToolRuntimeDefinition + ToolExecutionDefinition` with typed `connector + policy` proto oneof.
4. Regenerate API/HTTP/Gateway contracts in the same commit as the proto change.
5. Data migration reads old immutable version JSON, converts supported definitions, and disables/fails unsupported definitions rather than guessing.
6. Runtime map compatibility parser is removed once Hub emits typed ExecutionSpec.
7. Add Git executor capabilities, then publish migrated `skill.*` versions.
8. MCP/OpenAPI discovery/import becomes explicit control-plane workflows producing immutable ToolVersions.

## Invariant

**Hub defines Tool semantics and immutable execution contract; Runtime authorizes and brokers each invocation; the selected Connector identifies exactly one execution backend; the backend never becomes the Tool definition authority.**
