# Skill 预加载（catalog skill）开发实施文档

> 对应设计：`docs/ai/skill-preload-design.md`（决策：会话级拉取、清单式注入、加载期鉴权/运行期不鉴权）。
> 本文档是**给开发者的实施蓝本**：不变式 → 权限矩阵 → 接口契约 → 代码改动点 → 顺序与验收 → 测试。
> 状态：v0.1，未开工。

---

## 1. 全链不变式（先立规矩，改代码不能破）

**I-1 全链同一身份**：从「定义 agent 时前端可见技能」到「resolve 校验」到「运行时下载」到最后「沙箱落盘」，**全程使用同一个 principal**（发起会话的用户）。
证据（现有代码已实现，无需改动）：
- `SkillUsecase.ListSkills`：对每个技能 BatchCheck `skill:{name}#view` 过滤（`biz/skill_usecase.go:181-224`）→ 前端/定义器只能看见该用户 `view` 权限内的技能。
- 工具同理（`biz/tool.go:319-361`）。
- 结论：**「设计时可见 ⇒ 已鉴权」成立**，运行期复用同一 principal 拉取无越权风险。

**I2 内容不可变 + pin 校验**：技能内容以 `version + revision + sha256` 钉死；下载前必须校验包 sha256（防篡改、防走私）。未校验通过 → 不进入缓存、不注入 prompt。

**I3 fail-closed**：resolve/下载/校验任一环节失败 → 该技能不进 `available skills`、会话可继续但该技能不可用；原则从不落到"没权限也给了内容"。

**I4 运行期零鉴权**：`load_skill / load_skill_resource` 只读本地缓存，不回查 hub（I2+I4 的 "pin+sha256" 是运行期安全的依据）。

---

## 1. 权限矩阵（改动面一目了然）

| # | 环节 | 资源对象 | 权限 | 现状 | 改动 |
|---|---|---|---|---|---|
| 1 | 定义期列目录 | `skill:{name}` | `view` | ✅ 已有（ListSkills BatchCheck） | — |
| 2 | 定义期保存绑定 | 无校验（存 name+version 引用，不下载） | — | ✅ 已有 | — |
| 3 | resolve 期校验 | `skill:{name}` | `view` | ❌ 仅 builtin 白名单（`resolver.go:79-97`） | **M0：改为 catalog + skill:view 校验，产出 pin+URL** |
| 4 | 下载期校验 | （URL 自身） | HMAC 签名 + 过期 | ❌ 无下载端点 | **M1/M3：签名 URL 端点** |
| 5 | 运行期 `load_skill` | 本地缓存文件 | 不鉴权（I4） | ✅ 已有 | — |
| 6 | 发布新版本 | `skill:{name}` | `publish` | ✅ 已有 | — |

> 可选（非本期）：如需区分"目录可读"和"可被 agent 执行"，新增 `skill:execute` 动词。默认复用 `view`，**不加新动词**，保持最小改动。

---

## 2. 接口契约

### 2.1 resolve 返回的技能项（现有形状 → 目标形状）

现有（`agent_http_resolver.go` `resolveAgentSkillSnapshots`，builtin only、无 URL）：
```json
{ "name": "browser.automate", "version": "builtin-v1", "revision": "builtin-v1",
  "source": "builtin", "object": "aisphere://builtin-skills/browser.automate" }
```

目标（catalog 技能）——按 `runtime` 侧 `SkillSnapshotItem` 的 json 字段映射（`aihubruntime/client.go`），字段名对齐：
```json
{ "name": "my.skill", "version": "1.2.0", "revision": "r-8f3a2d",
  "source": "catalog",
  "sha256": "e3b0c4…", "md5": "d41d8c…", "size": 4096,
  "downloadUrl": "https://api.weagent.cc:30723/v1/skills/my.skill/pack?v=1.2.0&sig=…&exp=1785…&rt=rt-abc" }
```

- builtin 保持现状（`downloadUrl` 空 → runtime 判定"镜像内置、不下载"，`client.go:520-525` 特性）。
- **sha256 是包级（打包 tar/zip 后的）hash**，与 `SKILL.md` 的 tag manifest hash 不同，需在打包时计算。

### 2.2 新增下载端点

```
GET /v1/skills/{skill}/packages?version={v}&sig={hmac}&exp={unix}&rt={runtimeId}
```
- **鉴权**：`sig = HMAC-SHA256(secret, "{skill}|{version}|{runtimeId}|{exp}")`；校验过期（默认 10min）；**不校验 bearer principal**——用户鉴权已发生在 resolve（I1），端点只防 URL 伪造/重放。secret 用 hub 既有的内部凭据配置（`X-Aisphere-Internal-Token` 同源）。
- **响应**：`application/zip`（runtime 端 `ensureCached → unzipSkillPack` 已假定 zip）。zip 内结构 = 技能仓库工作树（含 `SKILL.md`）。
- **错误码**：`403 SKILL_PACK_FORBIDDEN`（签名错/过期）/ `404` / `409 SKILL_VERSION_CHANGED`（version 的 sha 与 pin 不符）/ 500 打包失败。
- 路由放行：该路径需在 Envoy Gateway 受保护路由内（`hub-http-route` 的 `/v1/skills/` 已覆盖），但** endpoint 自身鉴权不依赖网关**（服务间带签名 URL 直调）。

### 2.3 runtime 侧改动

按现状（`aihubruntime/client.go`）**理论上零新增**：
- `ResolveAgentSnapshot`（v1）响应 → `SkillSnapshotItem` 直接映射（字段名已对齐）；
- `ensureCached`（`client.go:759` 对）判定 `DownloadURL != ""` → 下载包 → 验 sha256 → `unzipSkillPack` → 缓存目录；
- builtin（URL 空）→ 跳过，镜像内置。
- 待实现处的**校验**：`ensureCached` 目前是否验 sha256——不验则在 M1 补一行（`item.SHA256` vs zip hash 比对 + 失败拒绝）。

### 2.4 🔧 传输层选型：技能包用"HTTP+zip"还是"git"

**结论：主通道用 HTTP 下载 zip 包（runtime 现成链路）；git 只留给"开发/编辑"场景；不做独立 CLI 下载器。**

现状盘点（代码实证）：
- **runtime 本来就内置 zip 下载器**：`ensureCached` 用 `http.NewRequest` GET URL（`client.go:913`）→ `unzipSkillPackage`（`client.go:1063`，`archive/zip`，处理「含顶层目录」「SKILL.md 在根」两种包布局，内部防 `..`、跳过目录项）。这已经是"打包→sha 校验→解包→软链"的完整体，**不需要新写下载二进制**。
- **hub 技能是 git 仓库**（Soft Serve 裸库 + annotated tag 发布），但代码里已有三处"打包"能力的前置：对象存储包方案文档 `docs/ai/skill-s3-first-dtm.md`（`skills/{skill}/versions/{v}/package.zip` + `manifest.json` + sha256，**设计完成未实现**）。
- **git 出现在这些场景**：技能编辑器/CLI（`gitengine/protocol.go`）、沙箱内置 `skill.fetch`（设计里用 `git clone --depth 1 --branch <tag>`，`sandbox` 侧预留）——那是"人/沙箱拿源码"的场景，**不是 runtime 批量发料理的通道**。

#### 三方案对比

| 维度 | A. HTTP+zip（内置） | B. git clone/fetch | C. 独立 CLI 下载二进制 |
|---|---|---|---|
| 现有代码 | ✅ ensureCached/unzip 现成 | 编辑器/CLI 已有；runtime 侧无 | ✗ 重造轮子 |
| 鉴权 | 签名 URL + 过期，简单 | 要 git 凭据（credential helper/SSH 密钥）分发，重 | 同 A，但多一层进程 |
| 性能 | 一个 GET，服务端打包/对象存储直出 | 每次全量/增量 fetch，协议开销大 | 同 A（进程间多一跳） |
| sha256 校验 | 包级直接比对 | 靠 commit 校验（对模型运行不直观） | 同 A |
| 不可变 | zip 即 artifact，可 CDN/对象存储预签名 | tag/commit 不可变，但"读取工作树"每动 Noun | 同 A |
| 适用 | **runtime 预加载主通道** | 开发者/沙箱内"按需拿源码" | 无 |

#### 落地与演进
- **M1 落地**：hub 下载端点**实时打包**（git 读 tag 树 → zip → sha256），小体积技能够用；
- **演进（后置）**：发布时**预生成** `package.zip` + `manifest.json` 落对象存储（复用既有 `skill-s3-first-dtm.md` 设计，MinIO client 已在 data.go 初始化），下载端点退化为"生成/转发**MinIO 预签名 URL**"——runtime 端代码一行不改。
- **git 的定位**：编辑器发版、CLI、以及沙箱 `skill.fetch`（取技能源码在自己跑）保留 git；会话预加载**不走 git**。

---

## 3. 代码改动点（按仓库/文件）

### hub（`aisphere-hub`）
| 文件 | 改动 |
|---|---|
| `internal/server/agent_http_resolver.go` | `resolveAgentSkillSnapshots`：支持 `source != "builtin"`；对每个 skill 做 `skill:{name}#view` BatchCheck（复用 `requirePermission` 同一通道）；读技能 manifest（git tag 解析出 sha/size/revision）；调用 pack 签名生成 `downloadUrl` |
| `internal/server/agent_http_endpoints.go` | 无（resolve 出口字段随 resolver 输出扩展） |
| `internal/biz/skill_usecase.go` | 新增 `BuildSkillPackage(ctx, name, version, principal) (*Pack, error)`：git 读 tag 树 → 打包 zip → 算 sha256/size；负责调用签名 |
| `internal/server/`（新） | `skill_pack_handler.go`：`GET /v1/skills/{name}/packages` 端点：验 `sig/exp/runtime` → `BuildSkillPack` → 流式返回 zip |
| `internal/server/http.go`（路由注册） | 注册新端点（放 `/v1/skills/` 前缀下，受既有 authn 中间件约束或独立签名校验中间件） |
| `internal/conf` | 新增 `skills.pack:` 配置（签名 secret/过期时长）

### runtime（`aisphere-agentkit`）——预期 0~2 处
| 文件 | 改动 |
|---|---|
| `internal/aihubruntime/client.go` | （如果现状没验 sha）`ensureCached` 增 sha256 比对；其余用现有链路。 |
| `internal/runtimeconfig/config.go` | 无（沿用 `skills.aihub.*`）；联调时把 `sync_on_start` 留 false（会话级） |

### front（`aisphere-hub-front`）
| 文件 | 改动 |
|---|---|
| `components/aihub/agent-skill-prompt-editor.tsx` | 解锁 catalog 技能 checkbox（已带 latestVersion/default；绑定改用 `version` 下拉对齐后端）；保留"版本不存在/已删/已禁用"提示 |
| `components/pages/skills-page.tsx` | （可选）已授权/未授权徽标 |

### deploy
| 文件 | 改动 |
|---|---|
| `deploy/gateway/hub-http-route.yaml` | `/v1/skills/` 已覆盖，无需改（验证一下 protected 路由包含 `/v1/skills/`） |
| `deploy/config.yaml`、runtime 的 adk.yaml | 配置 `skills.pack.secret`（env引用），保持 `sync_on_start: false` |

---

## 4. 实施顺序与验收（小步推进）

### M0「目录 pin 打通」 （半天）
**改动**：`resolveAgentSkillSnapshots` 支持 catalog（view 校验 + pin 四元组），暂不真下载（URL 可先占位）；前端解锁 checkbox。
**验收**：
- agent 绑 catalog 技能 → `:resolve` 返回 `{name,version,revision,sha256,size,downloadUrl}`，非 view 技能拒绝（不同用户分别测）。
- 前端可选、可选、可 despin。
- 回归：builtin 绑定行为不变。

### M1「下载闭环」（1~2 天）
**改动**：`BuildSkillPack` + 签名 URL + 端点；runtime `ensureCached` 接 URL（+ sha 校验）。
**验收**：单测覆盖「resolve → 下载 → 缓存目录出现 → `load_skill` 内容正确」；闭环 e2e 见 §5。

### M2「注入确认」（并入 M0/M1）
**改动**：无（现有 `<available_skills>` 已满足清单式）；仅验证绑定技能 frontmatter 在本地后可见于 prompt。
**验收**：llm 请求 dump 里能看到新技能。

### M3「下载鉴权硬化 + 审计」（1 天）
**改动**：签名过期（10min）→ 401；重放/盗链拒绝；加审计日志（who resolved 哪个 skill@version、s ip、rt）。
**验收**：过期 URL 401；篡改 `sig` 401；正常链路日志有审计。

### 里程碑（本期范围）
- 本期做到 **M0+M1+M2**（功能闭环 + 权限闭环）；
- M3 是加固可后置；M4 前端徽标可选。

---

## 5. 测试计划

### 单测（hub）
| 用例 | 断言 |
|---|---|
| 目录列表过滤 | 不同 principal 拿到的 ListSkills 不同（已有，back up） |
| resolve catalog 技能（有 view） | 返回 pin+URL，字段齐全 |
| resolve 无 view | `AGENT_SKILL_SOURCE_UNAVAILABLE`（或新增了一段错误码） |
| pack 打包 | zip 包含 SKILL.md + 资源；sha256/size 正确 |
| 签名 | 正确 sig 通过；改一个字节 401；过期 401 |

### 集成（runtime ↔ hub 假服务器）
- resolve 返回 pin → ensureCached 下载命中 → overridePath;
- sha256 不符 → 拒绝缓存 + 技能不可用（I3）;
- 重复会话 → 二次不下载（缓存命中）.

### 端到端（测试集群，闭环）
1. 用户 A 创建技能 `private/cap1`（private）+ 发布 `v1.0.0`；
2. 用户 B（无权限）在技能页**看不到** `cap1`；创建 agent 时勾选**不可见**；
3. A 创建 agent 绑 `cap1`，resolve → 正常；运行 → 沙箱 `.aisphere/skills/cap1/SKILL.md` 存在；`<available_skills>` 含 cap1；模型按 load_skill 使用；
4. A 撤销 B（或 B 被移除 view）后，B 的 ** 新会话 ** resolve 报错；已有会话照常（运行期不鉴权 I4）；
5. 发布 `v1.1.0` → 新会话解析到新 sha；旧 cache 淘汰；
6. 篡改 zip/token → 401/拒绝。

---

## 6. 风险与回滚

| 风险 | 说明 | 缓解/回滚 |
|---|---|---|
| 打包依赖 git 对象读取性能 | 每包 zip 实时构建 | 先按需构建 + 设组件缓存（LRU by sha256）；后端再降级为构建期预打包 |
| 前端解锁后用户绑了不可下载技能 | resolve 失败 | 前端按 `downloadUrl` 存在性提示；后端错 409 文案清晰 |
| runtime 缓存膨胀 | 多版本多技能 | 沿用既有 `pruneOldVersions`（已实现），按 skillset 限制保留版本数 |
| hub 包 sha 与运行时下载 sha 不一致 | 边界 | 单测覆盖 + 错误码 `409` 提示发版不一致 |

**回滚**：M0/M1 均为前端可关开关（`skills.aihub.sync_on_demand`? 不用 —— 回滚 = 把 hub 部署回上一 sha、前端开关恢复禁用状态），无迁移结构（技能名/版本仍是 string 引用）。

---

## 7. 与既有计划的对应

- 本功能 = 原推进计划里 **B5（agent definition.skills 绑定）+ F3（skill 勾选 UI）+ B6 前置（ResolveTool/pin 可回放）+ 预加载**的合体，把"技能 catalog → agent → runtime → 沙箱"补成实线。
- 前置条件：hub 的 skills git 仓库（tag+manifest）已可用（§0 I2 依赖）。

---

## 附：一次完整调用链（供读代码时对照）

```
1 前端(agent表单) GET /v1/skills?pageSize=100          → ListSkills(view) → 可见清单
2 前端绑定 {name,version}                             → 保存 definition
3 前端 :resolve {approvalConfirmed, approvedTools}    → resolver:
     resolveAgentSkillSnapshots → BatchCheck(skill#view)
     → pin {name,version,revision,sha256,size,downloadUrl 签名}
4 runtime session:  ResolveAgentSnapshot(v1) → SkillSnapshotItem
     SessionAgentLoader.materializeSnapshot → ensureCached(URL) → unzip → activate
5 runtime EnsureSession → Sandbox → copySkillToSandbox → .aisphere/skills/<name>
6 sandbox LLM 循环: skilltoolset.ProcessRequest → <available_skills> 含 cap1
7 模型 call load_skill("cap1") → 读沙箱内 SKILL.md → 执行
```
---

## 状态更新（2026-08-11）— M1（Hub 侧）已落地，runtime 侧就绪，等待服务间 TLS

### 已完成并线上验证（hub 侧加载期下载契约）

| # | 功能 | 状态 |
|---|---|---|
| 1 | `:resolve` 对 catalog 技能返回 `downloadUrl / sha256 / md5 / size`（以 launcher 身份过 `skill:view` 后） | ✅ 部署（`sha-fa55edd`，含 M0 resolve + M1 契约） |
| 2 | 技能包生成：确定性 zip（树序）+ 包级 sha256/md</qsize（`git rev-parse` 兼容 annotated tag） | ✅ |
| 3 | HMAC 签名下载 URL（TTL 默认 10m，绑定请求主体 ID） | ✅（单测：篡改/过期拒绝） |
| 4 | `GET /v1/skills/{name}/packages` 端点：验签 + 过期校验 → 返回 zip + `X-Content-SHA256` | ✅（线上 200/篡改 400） |
| 5 | 密钥：`SKILL_PACK_SECRET` env（不进 ConfigMap） | ✅ |

### runtime 侧对接（代码链路就绪，未做完整联调）

- v1 resolve 已把 `downloadUrl/sha256` 映射进 `SkillSnapshotItem`；`resolveURL` 会拼到 hub 内网地址；`ensureCached` **已有 sha256 校验** + 下载→解压→激活；会话 `CacheAgentSnapshotSkills` 在沙箱建会时调用。

### ⚠️ 待办：runtime → hub 的内部可信身份（服务间 TLS 后即可信任）

实测（2026-08-11）：runtime（或任何内部方）以 `X-Aisphere-Auth-Verified/Subject` 直连 hub 时 `:resolve` 返回 **403** —— hub 不再信任"伪造的内部头"（只有 Envoy 注入的可信）。这是正确方向，但意味着**内部 service 需要正式身份**。

**方案（已定方向）：服务间 TLS / 受信网络内部鉴权**。当 runtime↔hub（集群内）以 mTLS 或内部受信凭证互认后，hub 可对**来自受信内部客户端**的请求做 `trusted headers` 校验（等价 Envoy），此时 runtime 携带生成的服务身份（如 `X-Aisphere-Subject: service:agent-runtime` + 匹配 SpiceDB 的 `agent-runtime` service 主体与授权）即可全通。

待办清单（M1 接续）：
- [ ] hub：识别受信内部客户端（mTLS / 内部 token）→ 对这类请求启用 trusted-headers principal 解析
- [ ] runtime：配置内部身份（token / 证书），请求带该主体
- [ ] SpiceDB：`agent 执行授权`（service_account subject）与 `skill` 下载授权链路核对
- [ ] 端到端复验：session 建 → 下载+sha 校验 → 提取 → 沙箱 `.aisphere/skills` 落地 → `load_skill` 可用


---

## 状态更新（2026-08-12）— S5 Direct Skill 闭环完成 ✅（端到端验证通过）

### 本轮修复的问题链（按 E2E 实测逐步触发的顺序）

1. **runtime→hub 内部可信身份**（上轮已定方案，本轮落地）
   - hub 新增 `internal_service_trusted` Filter：持有 `AISPHERE_INTERNAL_TOKEN`（env 注入 conf.Authn.Internal）的请求被标记 `X-Aisphere-Auth-Verified: true` 并清除携带 token 头，进入 trusted-headers principal 解析 → resolve 403 解决。
   - runtime ConfigMap `extra_headers` 带 `X-Aisphere-Internal-Token: aisphere-internal-token-2026`（测试环境值）。

2. **model 快照 shape（`json: cannot unmarshal object ... of type string`）**
   - Hub v1 resolve 的 `model` 是 v2 资源快照 `{model:{code}, profile:{id,code}, endpoint:{baseUrl,adapter,apiFormat,providerModelId,credentialRef}, reasoning}`。
   - runtime `resolveAgentResponse.Model` 改 `json.RawMessage` + `normalizeModelSpec` 支持 flat / nested / **v2 endpoint** 三种 shape：`endpoint.providerModelId` 覆盖内部 UUID、`baseUrl/adapter` 进连接、`credential_ref` 经 metadata 透传给 adapter 作 API key。

3. **execution plan ledger 安全校验误伤**（`forbidden credential field $.authorization`）
   - Hub resolve 返回 `authorization` 子树（principalSubject/tool approvals）是执行上下文，但键名与 HTTP `Authorization` 头同名触发 credential scanner。
   - 修复：运行时内存中保留子树做强制；归档 source spec 前 `stripExecutionPlanAuthorization` 剥离该键（新增剥离函数 + 单测），ledger 只落不敏感字段。

### 端到端验证证据（线上 agent-runtime）

```
POST /api/apps/close-ag-1/users/admin/sessions/close-s1        → 200（sandbox profile default-python-offline 分配）
POST /api/run {appName:close-ag-1, sessionId:close-s1, ...}  → 200
  modelVersion: deepseek-v4-flash   finishReason: STOP（真实模型调用）
```
skill rt-fn-1 v1.0.0 落地证据：
```
/opt/agentkit/skills/.aihub/skills/rt-fn-1/versions/v1.0.0/SKILL.md
/opt/agentkit/skills/.aihub/skills/rt-fn-1/versions/v1.0.0/.aihub-version.json
  { sha256:0d25d536..., md5:2ae206920f..., size:226,
    downloadUrl:"/v1/skills/rt-fn-1/packages?name=rt-fn-1&ref=v1.0.0&rt=<uid>&exp=...&sig=..." }
/opt/agentkit/skills/.aihub/sessions/close-s1/runtime-skills/rt-fn-1/SKILL.md   （会话级隔离挂载）
```

### runtime 侧交付（agentkit main，均已 CI→ACR→k8s rollout）
- `8dd5b30` normalizeModelSpec（v2 endpoint shape）
- `5694a3c` execution plan authorization 剥离
- `1efc5ab` endpoint.providerModelId 覆盖内部 UUID

### 收尾
- 测试资源（skill rt-fn-1 / agent close-ag-1 / session）保留待清理
- 下一步候选：S4/Skillset（按计划暂停）、或 frontend catalog skill 绑定 UI 全链路复验

---

## 状态更新（2026-08-12）— S7/S22 落地 + catalog 前端绑定恢复

### 问题
- **S7**：AgentDefinition 保存期只校验格式 + skill:view，不校验 skill/version 存在 → 手工 JSON 可写不存在版本，运行期才炸。
- **S22**：disable/已删除的 skill 新 run resolve 不 fail（builtin/catalog 分支都没有 status/version 检查，对比 tool 有 AGENT_TOOL_DISABLED、model 有 AGENT_MODEL_PROFILE_DISABLED）。
- **前端 catalog 绑定断链**：`/v1/skills` 列表不返回版本字段 → `agent-skill-prompt-editor` 中 catalog 项的 `version` 恒为 `''` → 被 `.filter(skill => skill.name && skill.version)` 全滤掉 → UI 只能绑 builtin skill。

### 修复（hub commit 1eb041f + front ab85b32，均已部署）
| 项 | 实现 |
|---|---|
| S7+S22 | `resolveAgentSkillSnapshots` catalog 分支补：`GetSkill(name)` 存在 + `status=active`（否则 SKILL_DISABLED）；`GetRelease(name, version)` 校验发布版本存在（否则 SKILL_RELEASE_NOT_FOUND）。save（create/update → validateToolBindings）与 resolve 同路径，一处覆盖两头。注入 `data.NewSkillRepo` + `git`（agentSkillReleaseResolver）。 |
| latestVersion | proto Skill 加 `latest_version=13`；service 层 `latestStableReleaseVersion` 取最高非 prerelease canonical tag（Masterminds/semver）；前端 V1Skill 补字段 + 已有 normalizeSkill 透传。 |

### 验证证据（线上）
- API：`GET /v1/skills/rt-fn-1` → `"latestVersion":"v1.0.0"` ✅
- S22：POST agent 绑定 `rt-fn-1@v9.9.9` → **404 SKILL_RELEASE_NOT_FOUND** ✅
- S7：POST agent 绑定 `rt-fn-1@v1.0.0` → **201** ✅；不存在的 skill → 403（view 先行）✅
- UI（hub.weagent.cc, admin 会话）：Agent Editor 显示 `cccc v1.0.1 · catalog` 等全部 catalog 选项；勾选 cccc 保存成功 → `Agent close-ag-1 updated`（v1→v2，definition 含 `{name:cccc, source:catalog, version:v1.0.1}`）✅

### 遗留小瑕疵（非本次引入）
- Skill 列表页"最新"列显示 `vv1.0.0` 双前缀（前端渲染往 release tag 前又加了一次 v）——纯展示层，可选修。
- Agent close-ag-1 目前在测试中加了 `cccc@v1.0.1` 绑定（UI 验证产物，待清理）。
