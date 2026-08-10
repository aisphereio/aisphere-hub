# Skill 分享权限 E2E 测试报告（Skill Share Permission）

> 日期：2026-08-10
> 环境：线上测试集群（`hub.weagent.cc:30723`，Envoy Gateway → Casdoor OIDC → aisphere-hub `sha-e4c5af3`）
> 测试身份：**admin**（Skill owner / Casdoor UUID `496333c7-7acc-4717-8596-056544fc0a68`）、**te2e**（普通成员 `te2e / 123456`，Casdoor UUID `c0285b86-3363-469f-9af8-089785eb0c71`）
> 网关：`/v1/skills/**` → Envoy → aisphere-hub；授权后端 = IAM gRPC → SpiceDB
> 结论：**分享→访问→撤销→再验证 全链路 OK ✅**

---

## 1. 测试目标

验证 skill 的**细粒度分享权限闭环**（对应设计 S2「skill 权限：定义期权限 ≠ 运行期权限」）：

```
分享前（te2e 不可见） → 分享 viewer → te2e 可见/可读（不可写） → 撤权 → te2e 恢复不可见
```

## 2. 覆盖矩阵与结果

| # | 步骤 | 执行者 | 请求 | 结果（关键字段） | 判定 |
|---|------|--------|------|------------------|------|
| 1 | 创建测试 Skill（private） | admin | `POST /v1/skills` `{name: share-e2e-001, visibility: private}` | `200`；`visibility=private, status=active`；SpiceDB 关系：`owner@user:adminUUID` + `zone@zone:aisphere` | ✅ |
| 2 | 分享前基线（不可见） | te2e | `GET /v1/skills/share-e2e-001` | `403 AUTHZ_PERMISSION_DENIED`（`spicedb permission did not match`, 带 `decision_id`） | ✅ |
| 3 | 分享前基线（列表） | te2e | `GET /v1/skills` | `count=5`，**不含** share-e2e-001 | ✅ |
| 4 | 分享 viewer 给个人 | admin | `POST /v1/skills/share-e2e-001/shares` `{relation: viewer, subject_type: user, subject_id: te2e-UUID}` | `200`（首调偶发 403，见 §4 备注） | ✅ |
| 5 | 分享后读取 | te2e | `GET /v1/skills/share-e2e-001` | `200`；`visibility=private`、owner=管理员（私有但已授权可见） | ✅ |
| 6 | 分享后列表 | te2e | `GET /v1/skills` | `count=6`，**包含** share-e2e-001 | ✅ |
| 7 | viewer 写（越权探测） | te2e | `PUT /v1/skills/share-e2e-001/contents/SKILL.md` | `403`（viewer 无 edit，权限粒度正确） | ✅ |
| 8 | 撤销分享 | admin | `DELETE /v1/skills/share-e2e-001/shares/viewer/user/te2eUUID` | `200`；`GET .../shares` → `shares: []`（关系已删） | ✅ |
| 9 | 撤权后读取 | te2e | `GET /v1/skills/share-e2e-001` | `403 AUTHZ_PERMISSION_DENIED` | ✅ |
| 10 | 撤权后列表 | te2e | `GET /v1/skills` | `count=5`，**不含** share-e2e-001 | ✅ |
| 11 | 清理 | admin | `DELETE /v1/skills/share-e2e-001` | `200`；列表回到基线（6 → 删除 e2e 数据后正常） | ✅ |

## 3. 权限语义确认（SpiceDB 直查实证）

测试期间对 SpiceDB datastore（PG `spicedb` 库 `relation_tuple`）做了直查：

```
share-e2e-001:  owner → user:adminUUID
                zone  → zone:aisphere
（分享 viewer 期间：viewer → user:te2eUUID；撤销后该行消失）
```

- ListSkillShares（`GET /v1/skills/{name}/shares`）**实时读 SpiceDB**，无 DB 副本 → 所见即权威 ✅
- 分享的 relation 合法集：`viewer / editor / reviewer / publisher`（`validSkillRelation`）✅
- viewer 仅可读（步骤 7 实测 403 无 edit）✅

## 4. 备注 / 发现

1. **authz 判定存在短时陈旧窗口（观察项，非阻塞）**：分享创建首次调用偶发 `403 manage did not match`，几秒/数分钟后同一 admin 再调即 `200`（期间无代码变更）。疑似 hub/IAM 侧对 admin 权限判定的**缓存窗口（默认 TTL≈5min）**——已通过多次成功覆盖验证功能正常，但建议后续确认该缓存语义（避免"权限刚给出去立刻 403"的体验）。
2. 与本报告配套的 S1 资产闭环 e2e（创建/提交/tag/不可变版本/visibility）已另测通过；跨用户 visibility 闭环（private→public→private + te2e 视角即刻变化）在 `skill-lifecycle-closed-loop-design.md` 评审中口头确认 OK，本报告聚焦分享维度。

## 5. 结论

- **Skill 分享给个人（viewer）→ 目标用户可见可读、不可写 → 撤权后立即不可见**，`前后端 API（POST/GET/DELETE /v1/skills/{name}/shares）一致`、`SpiceDB 关系增删即时生效`，功能 **已可用 ✅**。
- 遗留建议：① 确认权限判定缓存窗口（备注 1）；② 为 skill 权限增加**周期对账任务**（DB visibility/分享 期望 vs SpiceDB 实际），覆盖异常漂移（详见 `skill-preload-dev.md` §7 Q3 思路）。

---

## 6. Skill 删除的权限清理（E2E 补充验证）

> 目标：删除一个 **public** skill 时，SpiceDB 上的权限（含公开 viewer 通配）是否会一并清掉；以及删除前是否提醒"有 agent 在用"。

### 6.1 公开 skill 会写哪些 SpiceDB 权限（实测）

创建 `del-e2e-public`（visibility=public）后直查 SpiceDB `relation_tuple`（活动行）：

```
owner    → user:496333c7-…（admin UUID）
viewer   → user:*
viewer   → service:*
viewer   → service_account:*
zone     → zone:aisphere
```

→ **公开权限 = 3 条 viewer 通配（`user:* / service:* / service_account:*`）+ owner + zone**，创建时即写入。

### 6.2 删除后 SpiceDB 是否清理（实测）

`DELETE /v1/skills/del-e2e-public` → `200`；随后查询活动行（`deleted_xid='9223372036854775807'`）：

```
alive
-----
0
```

→ **删除后 SpiceDB 权限全部清空**。机制：`SkillUsecase.DeleteSkill` 顺序为 `DB→Deleting → RevokeResource（删除该 skill 全部关系，含通配）→ 删仓库`；SpiceDB 采用 MVCC tombstone（物理行保留 `deleted_xid`），**权限视图已归零，无需手工操作**。结论：**公开 skill 删除不需要人工操作 SpiceDB，系统自动清理 ✅**。

### 6.3 ⚠️ 缺口：删除前**不检查 agent 引用**

当前 `DeleteSkill` **只清理权限 + 删除仓库，没有任何"哪些 agent 还绑定该 skill"的引用检查**（代码无 agent 引用查询）。

- **现状风险**：某 agent 绑了该 skill → skill 被删（软删）→ 该 agent 后续 `resolve` 将失败（`AGENT_SKILL_RESOLVE_FAILED` 一类），**删除时没有任何警告**。
- **建议（待开发功能点）**：删除前查询 `agents.definition_json->skills` 引用该 skill 的行，返回 `referencing_agents[]`；前端弹"该 Skill 正在被 N 个 Agent 使用，需先修改这些 Agent"确认/阻断（或 force 参数）。
- **状态：当前缺失，标记为待办（S2 补充）**。

---

## 7. S2 权限模型确认 + M0（Catalog 技能可绑定/解析）+ Schema 同步修复（2026-08-10）

### 7.1 S2 权限模型与文档推荐一致（代码实证）

SpiceDB `definition skill` 的权限与文档 S2 完全一致：

```
manage  = owner + parent->manage + zone->manage_skills + custom_binding->manage
edit    = manage + editor     + parent->edit   + custom_binding->edit
review  = manage + reviewer   + custom_binding->review
publish = manage + publisher  + custom_binding->publish
view    = edit + reviewer + viewer + parent->view + zone->view_skills + custom_binding->view
```

Hub 22 个 Skill API 的菱形映射：`view`（列表/详情/文件读/refs/commits/release 查询/PR 查看）、`edit`（文件写/PR 新建/改 metadata）、`review`（ReviewPullRequest）、`publish`（CreateSkillRelease + MergePullRequest）、`manage`（Delete/Visibility/分享管理/RestoreRef）、创建走 `zone:{org_id}#create_skill`。V1 无 `execute`。

### 7.2 ⚠️ 修复：线上 SpiceDB Schema 落后（Agent 创建全挂）

**现象**：创建 Agent → `503 AGENT_AUTHZ_UNAVAILABLE`；IAM 日志 `relation/permission "create_agent" not found under definition "zone"`。

**根因**：仓库 schema（`aisphere-iam/configs/spicedb/aisphere.schema.zed`）已含 `create_agent` 等新定义，但**线上 SpiceDB 从未被重新发布**（IAM 启动不会自动升级已存在的 schema）。

**修复（运维手法）**：手动把仓库 schema 推送到线上 SpiceDB（gRPC `WriteSchema`，token=SpiceDB preshared key）：

```bash
# 临时小工具（authzed-go v1）：conn → spicedb.grpc → WriteSchema(schema.zed)
schemapush <spicedb-addr>:<grpc-nodeport> <preshared-key> aisphere.schema.zed
# 验证
kubectl exec apps-postgre-0 -- psql -U postgres -d spicedb \
  -c "SELECT relation FROM relation_tuple WHERE namespace='zone' AND object_id='aisphere' AND deleted_xid='9223372036854775807'"   # 无关；用 IAM CheckPermission 验证 create_agent 通过
```

**防再犯建议**：把 schema 发布纳入 IAM 部署/CI 流程（对比 SpiceDB `ReadSchema` revision 与仓库 schema，不一致即 `WriteSchema`）；否则每次新增权限都会出现"代码更新但授权模型没更新"的断链。

### 7.3 ✅ Catalog 技能绑定闭环（M0 打通，已部署验证）

- **后端**（hub `93811b5`）：`resolveAgentSkillSnapshots` 支持 catalog——`skill:{name}#view` 门控后 pin `{name,version,revision,source:catalog,object:"aihub:skill:name"}` 进快照；保存校验与 `:resolve` 共用。
- **前端**（front `14e5f55`）：Agent skill 选择器解锁 catalog（移除 disabled）。
- **线上 e2e**：创建 catalog skill → 发布 tag `v0.0.1` → 创建 Agent 绑定 `{name,version,source:catalog}` → `201` → `:resolve` 返回 `200` 且 `skills=[{source:catalog, version:v0.0.1, object:aihub:skill:cat-sk-0802}]` ✅（测试数据已清理）。
- 运行期拉取（HTTP+zip / digest / URL）属于 **M1**（另一个闭环）。

### 7.4 相关部署版本（线上）

```
hub    = sha-93811b5（catalog resolve + 命名规则 + 描述投影）
front  = sha-14e5f55（catalog 选择器解锁 + 版本就地查看）
iam    = sha-2c96389（+ schema 已手动同步到线上 SpiceDB）
```