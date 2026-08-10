# AISphere Skill 全生命周期小闭环设计总览

> 状态：Draft v0.1
> 目标：将 Skill 能力拆分成多个独立、可开发、可测试、可验收的小功能闭环，避免一次性实现“Skill 预加载”这一过大的功能集合。
> 原则：每个功能点必须明确 **Owner、数据模型、权限、接口、运行时行为、失败策略、测试标准** 后再开发。

---

# 0. 总体目标

Skill 的完整生命周期不是单一的“预加载”功能，而至少包含：

```text
Skill 创建/定义
      ↓
Skill 权限
      ↓
Skill 版本/发布
      ↓
Git / CLI 获取
      ↓
SkillSet 组织
      ↓
Agent 选择 Skill / SkillSet
      ↓
AgentRevision 固化
      ↓
Runtime Resolve
      ↓
Runtime 获取 Skill
      ↓
Skill Materialize
      ↓
Context / Prompt 注入摘要
      ↓
模型按需 load_skill
      ↓
Skill 引导 Tool 调用
      ↓
运行审计 / 更新 / 撤权
```

任何一个环节如果边界不清晰，后续都会出现：

* 缓存等同权限；
* Agent 定义者权限传递给运行者；
* SkillSet 修改导致 Agent 行为漂移；
* Runtime 使用人类 Git 凭据；
* Skill 自动获得 Tool 权限；
* Hub 拼 Prompt；
* Sandbox 持有 Hub/IAM 凭据；
* 版本声明不可变但实际文件内容漂移。

因此后续按以下小闭环分别完成。

---

# S1. Skill 资产定义闭环

## 1.1 Skill 是什么

Skill 是 Hub 管理的 AI Asset。

核心结构建议：

```text
Skill
├── id / name
├── displayName
├── description
├── owner
├── scope / visibility
├── repository
├── latestVersion
└── status

SkillVersion
├── version
├── revision
├── sourceRevision / gitCommit
├── manifestDigest
├── publishedAt
└── immutable content reference
```

其中：

* `Skill` 是可变 Catalog 对象；
* `SkillVersion` 是不可变版本；
* Agent 和 Runtime 最终只能使用 `SkillVersion`；
* Runtime 不允许运行 `latest`。

---

## 1.2 Skill 的“一句话简介”

每个 Skill 必须有一个**面向模型和人的简短说明**。

例如：

```text
name:
kubernetes-troubleshooting

description:
诊断 Kubernetes Pod、网络、调度、存储和日志相关故障。
```

建议：

> `SKILL.md frontmatter.description` 作为这句话的唯一事实源。

不要再额外维护：

```text
DB summary
SKILL.md description
Runtime description
Agent description
```

四份内容。

Hub 可以把该字段投影进数据库用于：

* 列表展示；
* 搜索；
* Agent Builder；
* SkillSet；
* Runtime Snapshot。

但事实源仍是 SkillVersion 中的 frontmatter。

---

## 1.3 Skill 内容

典型结构：

```text
skill-name/
├── SKILL.md
├── references/
├── examples/
├── templates/
└── assets/
```

其中：

```text
SKILL.md
```

至少包括：

```text
name
description
instructions
metadata
allowedTools（仅提示，不是授权）
```

---

## 1.4 Skill 创建

创建 Skill 时：

```text
Human Principal
      ↓
Hub
      ↓
IAM create_skill
      ↓
创建 Skill Catalog
      ↓
创建 Git repository
      ↓
写 owner relationship
```

需要保证：

```text
DB
Git Repo
IAM Relationship
```

的一致性。

---

# S2. Skill 权限闭环

Skill 当前至少需要：

```text
view
edit
review
publish
manage
```

V1 暂时**不新增 skill.execute**。

## 2.1 权限含义

### view

允许：

* 在 Skill Catalog 中看到；
* 查看 SKILL.md；
* 查看资源；
* Agent 绑定；
* Agent Runtime Resolve 时使用。

### edit

允许：

* Git push draft；
* 修改 SKILL.md；
* 修改资源。

### review

允许：

* Review Skill version。

### publish

允许：

* 创建不可变发布版本/tag。

### manage

允许：

* 权限管理；
* metadata；
* archive/delete 等管理操作。

---

## 2.2 非常重要：定义期和运行期不是同一个权限

例如：

```text
Alice
  创建 Agent
  有 private-skill#view

Bob
  可以执行 Alice 的 Agent
  但没有 private-skill#view
```

因此：

```text
Alice 创建 Agent 时能选择 Skill
```

不能推出：

```text
Bob 执行 Agent 时能使用 Skill
```

必须分别检查：

```text
Agent 编辑期：
skill.view(editor)

Agent Run Resolve：
skill.view(launcher)
```

这是 Skill 权限最重要的不变式之一。

---

# S3. Skill 的 Git / CLI 使用闭环

这个场景必须和 Runtime 获取 Skill 分开。

## 3.1 人类开发者获取 Skill

场景：

```text
aisphere skill clone xxx

或者：

git clone ...
```

这里使用的是：

> **Human Principal**

推荐链路：

```text
User
 ↓
aisphere login / git-aisphere-login
 ↓
OIDC / PKCE
 ↓
短期用户凭据
 ↓
Git endpoint
 ↓
IAM skill.view/edit
```

例如：

```text
clone/fetch → skill.view
push       → skill.edit
tag        → skill.publish
```

---

## 3.2 `aisphere` CLI 的定位

建议未来统一提供：

```bash
aisphere login

aisphere skill list
aisphere skill get
aisphere skill clone
aisphere skill pull
aisphere skill push
aisphere skill publish
```

CLI 本身不是权限中心。

它只是：

```text
Human UX
   ↓
Hub API / Git Protocol
   ↓
IAM
```

---

## 3.3 Runtime 不复用 Human Git Login

必须明确：

```text
Human Skill Development
≠
Runtime Skill Materialization
```

Runtime 不应该：

```text
调用 git-aisphere-login
保存用户 refresh token
复制用户 SSH key
使用用户 Git credential helper
```

Runtime 获取 Skill 必须是另外的服务身份/短期 artifact capability。

---

# S4. SkillSet 定义闭环

这是目前容易遗漏的一块。

SkillSet 不只是前端“多选快捷方式”，如果要成为长期能力，应作为独立 Control Plane Asset。

建议：

```text
SkillSet
├── id
├── name
├── description
├── scope
├── owner
├── revision
└── members[]
```

成员：

```text
SkillSetMember
├── skillId
├── versionPolicy
└── order?
```

---

## 4.1 SkillSet 的两种可能语义

必须先选择一种。

### 方案 A：动态集合

```text
SkillSet = skill-a latest
         + skill-b latest
```

Skill 发布新版本后 SkillSet 自动变化。

问题：

> Agent 行为会漂移。

不建议作为生产默认。

### 方案 B：版本化集合

```text
SkillSet revision=8

├── skill-a@1.2.0
└── skill-b@3.1.0
```

SkillSet 修改：

```text
revision 8 → revision 9
```

已有 AgentRevision 仍指向 revision 8。

**推荐 V1 使用方案 B。**

---

## 4.2 SkillSet 权限

如果 SkillSet 本身成为 Asset，可以拥有：

```text
skillset.view
skillset.edit
skillset.manage
```

但：

> 有 `skillset.view` 不能自动获得 SkillSet 成员的 `skill.view`。

运行时仍必须检查：

```text
for each member skill:
    skill.view(launcher)
```

SkillSet 只是集合，不是权限包。

---

# S5. Agent 前端选择 Skill 闭环

Agent Builder 中需要支持两种来源：

```text
直接选择 Skill

以及

选择 SkillSet
```

建议 UI 分开显示：

```text
Skills
├── Direct Skills
└── SkillSets
```

---

## 5.1 Skill 选择

前端列表：

```text
GET /v1/skills
```

只显示当前编辑者：

```text
skill.view = allowed
```

用户选择：

```text
kubernetes-troubleshooting
version=1.2.0
```

而不是只保存：

```text
kubernetes-troubleshooting
```

---

## 5.2 SkillSet 选择

用户选择：

```text
backend-engineer
revision=8
```

前端可以展开：

```text
backend-engineer
├── golang-style@2.0
├── postgres-dba@1.4
└── k8s-debug@3.1
```

让用户明确看到 Agent 将获得什么 Skill。

---

## 5.3 Direct Skill + SkillSet 同时存在

这是必须定义的冲突规则。

例如：

```text
SkillSet:
golang-style@1.0

Direct:
golang-style@2.0
```

建议 V1：

> 不允许静默覆盖。

Normalize AgentDefinition 时直接：

```text
AGENT_SKILL_VERSION_CONFLICT
```

用户必须明确解决。

---

# S6. Agent YAML / AgentDefinition Skill 模型

不要让前端状态成为事实源。

最终必须进入 AgentDefinition。

建议概念结构：

```yaml
skills:
  - name: kubernetes-troubleshooting
    version: 1.2.0

skillsets:
  - id: backend-engineer
    revision: 8
```

这里表达的是：

> 用户的声明。

Hub 在保存/发布 AgentVersion 时，可以生成一个 resolved projection：

```text
ResolvedSkillBindings
├── direct skill A
├── skillset X → skill B
├── skillset X → skill C
└── ...
```

---

## 6.1 为什么不要直接把 SkillSet 展平覆盖 YAML

因为需要保留 provenance：

```text
为什么 Agent 有这个 Skill？

Direct binding？

还是来自 backend-engineer SkillSet？
```

所以建议同时保留：

```text
declaration
```

和：

```text
resolved projection
```

---

# S7. AgentDefinition 保存期权限闭环

Agent 编辑者选择 Skill 时，前端已经过滤了一次。

但后端不能相信前端。

保存 AgentDefinition 时仍需要：

```text
skill exists
skill version exists
skill not deleted
```

是否在“保存时”再次检查 `skill.view` 可以确认。

推荐：

```text
保存时检查
```

这样避免用户手工构造 JSON 绕过 UI。

但这仍然不是 Runtime 授权。

真正执行时仍必须重新：

```text
skill.view(launcher)
```

---

# S8. Agent Run Resolve 闭环

运行用户：

```text
Launcher Principal
```

执行：

```text
POST Agent :resolve
```

Hub：

```text
1. agent.execute
2. resolve AgentRevision
3. expand SkillSet revisions
4. resolve all exact SkillVersions
5. skill.view(launcher) for every Skill
6. produce immutable SkillSnapshot
```

输出：

```text
SkillSnapshot[]
```

例如：

```json
{
  "name": "k8s-debug",
  "version": "1.2.0",
  "revision": "r193ab",
  "source": "catalog"
}
```

后续可以增加：

```text
artifactRef
digest
size
```

---

# S9. Runtime Skill 获取闭环

这一版明确：

> **暂时不考虑共享缓存优化。**

也就是说每个新 Session/Run：

```text
Resolve Agent
      ↓
拿到 SkillSnapshot
      ↓
逐个获取 Skill package
      ↓
校验
      ↓
Materialize
```

先把正确性做出来。

---

## 9.1 Runtime 使用什么方式获取 Skill

这里需要单独确认，至少存在三个候选：

### A. HTTP package

```text
Runtime
 → Hub/package service
 → skill.zip
```

优点：

* Runtime 已经有 HTTP + ZIP 代码；
* 协议简单；
* 后续容易替换为 MinIO/S3；
* 不需要 Git credentials。

当前推荐方向。

### B. Git clone

```text
Runtime
 → git clone tag
```

问题：

* Runtime 需要 Git credentials；
* Git CLI 子进程；
* working-tree semantics 比 Runtime 读取内容复杂；
* Runtime 不需要 commit/push 能力。

更适合：

```text
Developer
Sandbox
```

而不是 Runtime。

### C. `aisphere` CLI

Runtime：

```text
exec aisphere skill pull
```

不建议。

Runtime 应直接调用 Go SDK/API，不应该依赖 CLI 子进程作为核心数据面。

---

## 9.2 V1 推荐

```text
Human 开发：
Git / aisphere CLI

Runtime：
HTTP Skill Package API
```

两个场景严格分离。

---

# S10. Runtime 获取 Skill 的鉴权闭环

这里不能继续使用 Human Git 凭据。

推荐模型：

```text
Launcher Principal
       │
       │ resolve
       ▼
Hub
       │
       │ skill.view
       ▼
SkillSnapshot
       │
       │ short-lived artifact authorization
       ▼
Runtime Service Identity
       │
       ▼
Skill package endpoint
```

也就是：

```text
User Principal
```

负责：

> “这个 Run 有没有资格获得 Skill。”

而：

```text
Runtime Identity
```

负责：

> “当前下载请求是不是来自合法 Runtime。”

这两个身份不要混为一谈。

---

# S11. Runtime Skill Materialize 闭环

这一版可以非常简单。

例如：

```text
Run Workspace
└── .aisphere/
    └── skills/
        ├── k8s-debug/
        │   ├── SKILL.md
        │   └── references/
        └── golang-style/
            └── SKILL.md
```

必须满足：

```text
只包含当前 SkillSnapshot 中的 Skill
```

不能因为 Runtime 本地还有其他 Skill 就暴露给 Agent。

核心不变式：

> **Filesystem presence != authorization.**

---

# S12. “一句话 Skill 简介”注入 Context 闭环

这是你这次特别指出的重点。

假设 Agent 有：

```text
k8s-debug
golang-style
postgres-dba
```

模型初始化上下文里应该出现类似：

```xml
<available_skills>
  <skill name="k8s-debug">
    Diagnose Kubernetes pods, networking, storage and scheduling problems.
  </skill>

  <skill name="golang-style">
    Go coding and review conventions for AISphere projects.
  </skill>
</available_skills>
```

注意：

> 这里只放 Skill 的一句话 description。

不要默认把三个完整 `SKILL.md` 全部塞进 Context。

---

# S13. Skill Context Injection 应该由谁实现

推荐 Owner：

> **Runtime Context / Agent Assembly 层。**

不是 Hub。

不是 Sandbox。

不是前端。

职责：

```text
Hub
  负责 Skill 的 description 和 immutable SkillVersion

Runtime
  负责决定本次 Agent 看到哪些 Skill
  负责把 available skill descriptions 拼进模型上下文

ADK SkillToolset
  提供 load_skill / load_skill_resource 等模型能力
```

当前 AgentKit 已经把 Agent YAML `skills` 解析成 `skilltoolset`，并通过 `SkillSource` 创建 Skill Toolset。

因此不要重新开发第二套 Skill Agent Loop。

需要确认的是：

```text
AISphere Runtime
        ↓
Run-scoped SkillSource
        ↓
ADK skilltoolset
        ↓
ProcessRequest
        ↓
available skills
```

是否已经完全满足我们的 prompt 形态。

---

# S14. Skill Preload 模式

建议把“下载”和“Prompt Preload”彻底分开。

## Materialization

这一版：

```text
eager
```

Run 开始前把绑定 Skill 全部拿到。

## Context preload

默认：

```text
frontmatter / summary
```

即：

```text
name + description
```

只有模型真正需要时：

```text
load_skill(name)
```

再读取完整 instructions。

---

# S15. `load_skill` 闭环

模型看到：

```text
k8s-debug
```

决定需要它：

```text
load_skill("k8s-debug")
```

执行：

```text
Run-local SkillSource
        ↓
SKILL.md
        ↓
完整 instructions
        ↓
FunctionResponse / context
```

这里：

> 不需要再次调用 Hub/IAM。

原因：

Skill 在 Runtime Resolve / Materialize 时已经形成当前运行快照。

---

# S16. `load_skill_resource` 闭环

Skill instructions 可能引用：

```text
references/network-debug.md
templates/deployment.yaml
```

模型：

```text
load_skill_resource(
    skill="k8s-debug",
    path="references/network-debug.md"
)
```

必须严格：

```text
只能访问该 Skill root
```

防止：

```text
../
跨 Skill
访问 Runtime filesystem
```

---

# S17. Skill 与 Tool 的关系

Skill 可以声明：

```yaml
allowed-tools:
  - k8s.get_pods
  - k8s.logs
```

但：

```text
Skill.allowedTools
```

**不能成为授权。**

它只能用于：

* UI 提示；
* compatibility validation；
* Agent 发布 warning；
* 推荐 Tool。

例如：

```text
Skill:
k8s-debug

requires/recommends:
k8s.get_pods
k8s.logs
```

Agent 没绑定这些 Tool：

```text
warning:
Skill k8s-debug may not function completely
```

但不能自动：

```text
Grant Tool
```

---

# S18. Tool 运行期权限仍然独立

模型：

```text
Skill instructions
      ↓
决定调用 k8s.restart_pod
      ↓
Tool Broker
      ↓
IAM
Approval
Credential
Sandbox/Service
```

Skill 能做的是：

> 告诉模型“应该怎么做”。

Tool 系统决定：

> “本次 Principal 是否真的有权做”。

因此：

```text
Skill Authorization
≠
Tool Authorization
```

---

# S19. Skill 更新闭环

例如当前：

```text
k8s-debug@1.2.0
```

作者发布：

```text
1.3.0
```

必须明确：

### 已存在 AgentRevision

仍然：

```text
1.2.0
```

### 新建 AgentRevision

用户可以：

```text
升级到 1.3.0
```

Skill 版本绝对不能偷偷漂移。

---

# S20. SkillSet 更新闭环

同理：

```text
SkillSet backend@revision8
```

后来 SkillSet 加了：

```text
redis-debug
```

形成：

```text
revision9
```

已有 AgentRevision：

```text
backend@revision8
```

不能自动变 revision9。

否则 AgentRevision 就不是 immutable definition。

---

# S21. Skill 撤权闭环

例如 Bob：

```text
10:00
有 skill.view

10:01
创建 Session

10:02
Skill 已进入 Runtime

10:05
管理员撤销 skill.view
```

V1 建议：

```text
当前 Session / Run
继续

新的 Session / Run
resolve fail
```

即：

```text
disable_new_sessions
```

以后高安全策略可以支持：

```text
cancel_running
```

但不在 `load_skill()` 时重新 IAM。

---

# S22. Skill 删除 / 下架闭环

需要区分：

```text
disable
archive
delete
```

建议：

### disabled

新 Agent 不允许选择。

已有 Agent：

```text
新 Run 是否还能执行？
```

这是需要明确的策略。

推荐 V1：

```text
disabled = new Run fail
```

### archived

Catalog 不再默认展示，但已有 pin 可以继续执行。

### deleted

不允许再 resolve。

具体语义需要单独确认。

---

# S23. Builtin Skill 与 Catalog Skill

必须显式区分：

```text
source=builtin
source=catalog
```

不要继续：

```text
downloadUrl == ""
```

就推断成 builtin。

建议：

```text
SkillSnapshot
├── source
├── name
├── version
└── revision
```

Catalog 后续再增加：

```text
artifact
```

---

# S24. Skill 获取失败策略

Agent 明确绑定：

```text
skill A
skill B
```

V1 推荐全部视为：

```text
required
```

任何一个：

```text
权限失败
resolve 失败
下载失败
包校验失败
materialize 失败
```

则：

```text
Run Prepare Failed
Agent Loop 不启动
```

不要悄悄：

```text
去掉 B
继续执行
```

否则运行的 Agent 已经不再等于其定义。

---

# S25. Skill 审计闭环

至少要能回答：

```text
谁
在什么时间
执行了哪个 AgentRevision
最终 resolve 到哪些 SkillVersion
```

建议 Runtime ExecutionSnapshot 中记录：

```text
skills:
  - name
    version
    revision
    digest
    source
```

不需要记录完整 SKILL.md。

---

# S26. Skill 错误模型

建议逐步标准化：

```text
SKILL_NOT_FOUND
SKILL_VERSION_NOT_FOUND
SKILL_DISABLED
SKILL_VIEW_DENIED

AGENT_SKILL_VERSION_CONFLICT
AGENT_SKILLSET_NOT_FOUND
AGENT_SKILL_RESOLVE_FAILED

SKILL_PACKAGE_DOWNLOAD_FAILED
SKILL_PACKAGE_DIGEST_MISMATCH
SKILL_PACKAGE_INVALID

SKILL_MATERIALIZE_FAILED
SKILL_RESOURCE_NOT_FOUND
SKILL_RESOURCE_PATH_INVALID
```

不要所有场景都返回：

```text
AGENT_SKILL_SOURCE_UNAVAILABLE
```

---

# S27. 前端状态

Agent Builder 至少需要呈现：

```text
Skill name
一句话简介
Version
Source
Direct / SkillSet 来源
Status
```

例如：

```text
☑ Kubernetes Troubleshooting
  Diagnose Kubernetes workloads...
  v1.2.0
  Direct

☑ Golang Style
  AISphere Go development conventions...
  v2.1.0
  via Backend Engineer SkillSet
```

冲突必须显式提示。

---

# S28. 完整职责划分

```text
Hub
├── Skill Catalog
├── SkillVersion
├── SkillSet
├── AgentDefinition
├── AgentRevision
├── Resolve
└── Control-plane AuthZ

IAM
└── 谁可以 view/edit/publish/manage

Runtime
├── ExecutionSnapshot
├── Skill Resolver Client
├── Skill Materializer
├── Run-scoped SkillSource
├── Context Assembly
└── SkillToolset integration

Sandbox
├── Skill files read-only mount
└── executor capabilities

Model
├── available skill summaries
├── load_skill
└── load_skill_resource

Tool Broker
└── Skill instruction产生的真实动作授权
```

---

# S29. 当前代码基线

当前系统并不是从零开始。

AgentKit 已经支持：

```text
Agent YAML
   skills:[]
      ↓
resolveSkillToolsets()
      ↓
SkillSource
      ↓
skilltoolset.New()
```

当前 `resolveSkillToolsets()` 会根据 Agent 配置的 Skill 创建 SkillToolset。

Runtime 还已经存在 Agent request-scoped loader：

```text
ResolveAgentSnapshot
        ↓
materializeSnapshot
        ↓
Skill root
        ↓
configurable.FromConfig
```

Hub 当前真正的主要缺口是：

```text
resolveAgentSkillSnapshots()
```

现在只接受 builtin Skill，Catalog Skill 尚未打通 Runtime Resolve。

因此我们的工作重点不是重写 Skill Runtime，而是逐步补齐：

```text
Hub Catalog
→ Agent Binding
→ Resolve/AuthZ
→ Runtime Materialize
→ Context Injection
```

---

# S30. 推荐的小步实施顺序

不要再以：

```text
“Skill 预加载功能”
```

开一个大 PR。

建议拆成以下独立 Feature/PR。

## PR-S1：Skill Definition Contract

只解决：

```text
Skill / SkillVersion
一句话 description
version/revision
source
状态语义
```

验收：

```text
Skill 创建
Skill 查询
Skill 发布
description 单一事实源
```

---

## PR-S2：Human Git / CLI Access

只解决：

```text
aisphere login
git clone/fetch/push/tag
skill view/edit/publish IAM
```

验收：

```text
有权限能 clone/push
没权限明确 403
```

---

## PR-S3：SkillSet Contract

只解决：

```text
SkillSet
SkillSetRevision
成员 SkillVersion
权限
```

不涉及 Runtime。

---

## PR-S4：Agent Skill/SkillSet Binding

只解决：

```text
前端选择
AgentDefinition
Direct Skill
SkillSet
版本冲突
后端校验
```

不涉及 Runtime 下载。

---

## PR-S5：Run Resolve Skill Authorization

只解决：

```text
launcher principal
agent.execute
skill.view
SkillSet expansion
exact SkillVersion
SkillSnapshot
```

仍然可以不真正下载。

---

## PR-S6：Runtime Skill Fetch

第一版：

```text
每次 Session 直接下载
不优化 cache
```

解决：

```text
Runtime identity
package protocol
download
digest verify
materialize
```

---

## PR-S7：Context Summary Injection

只解决：

```text
Skill description
      ↓
available_skills
      ↓
LLM request
```

确认具体由：

```text
ADK SkillToolset
```

还是 AISphere Context Builder wrapper 完成。

原则：

> Runtime owns context assembly；Hub 不拼 Prompt。

---

## PR-S8：load_skill / resource 闭环

验证：

```text
available skill
    ↓
model load_skill
    ↓
SKILL.md
    ↓
load_skill_resource
```

全程本地，不回 Hub/IAM。

---

## PR-S9：Audit / Revoke / Error Semantics

最后统一：

```text
ExecutionSnapshot
audit
revoke
disable/archive/delete
错误码
```

---

# S31. 后续讨论顺序

建议我们接下来不要直接讨论 Runtime 下载。

按依赖顺序逐项确认：

```text
① Skill 本身到底是什么
   description / version / Git / IAM

② Human 怎么获取 Skill
   Git + aisphere CLI + AuthZ

③ SkillSet 是什么
   revision / membership / permission

④ Agent 怎么绑定 Skill 和 SkillSet
   YAML / UI / conflict

⑤ Run Resolve 怎么重新鉴权

⑥ Runtime 怎么拉

⑦ description 怎么进入模型 Context

⑧ load_skill 怎么工作
```

只有前一个点冻结，才进入下一个点。

---

# 最核心的系统不变式

最终整套 Skill 能力建议固定以下原则：

1. **Skill 是 Hub Asset，SkillVersion 是不可变执行输入。**
2. **SKILL.md frontmatter.description 是 Skill 一句话简介的事实源。**
3. **定义期权限不能替代 Run launcher 权限。**
4. **SkillSet 是集合，不是权限包。**
5. **AgentRevision 不允许依赖 floating latest。**
6. **Runtime 文件存在不等于获得授权。**
7. **Runtime 不使用人类 Git credential。**
8. **Hub 不负责拼模型 Prompt。**
9. **Skill summary 进入 Context，完整 instructions 默认按需 load。**
10. **Skill.allowedTools 不是 IAM Grant。**
11. **Skill 权限决定模型是否能得到方法；Tool 权限决定动作是否真的允许执行。**
12. **显式绑定 Skill 准备失败时，默认整个 Run fail closed。**
