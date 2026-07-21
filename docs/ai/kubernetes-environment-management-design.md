# AISphere Kubernetes 环境管理能力开发设计

> 状态：Design Draft  
> 目标版本：Kernel `v0.5.x`、Hub/Hub Front 下一能力版本  
> 涉及仓库：`aisphereio/kernel`、`aisphereio/aisphere-iam`、`aisphereio/aisphere-hub`、`aisphereio/aisphere-hub-frontend`  
> 范围：Kubernetes 集群接入、Namespace 生命周期、Namespace 可见性与分享、前端契约生成  
> 非范围：完整 Kubernetes 运维平台、任意资源浏览器、节点运维、监控平台、直接向终端用户签发 kubeconfig

> **实现状态**：
> - 原设计文档已通过 PR #17 合并到 `aisphere-hub` `main`（merge commit `1611b6b`）。
> - Kernel `kubernetesx` SDK 候选实现已在 `kernel` 仓库提交（含 Config/Credential/SSA/Probe/Discovery/Errors/Metrics + Fake + envtest + 真实集群集成测试），作为 Phase 1 候选，尚未作为正式 PR 合并。候选实现有两个待修问题（ServiceAccount 凭据合并丢失 Token/CA、Probe SSAR 对 cluster-scoped 资源传了 namespace），见 §15 Phase 1 备注，由 kernel 补丁 PR 修复。
> - IAM Schema / Hub Backend / Frontend 阶段均未开始，等待本文档冻结后进入。

---

## 1. 背景与目标

AISphere 需要在现有 Hub 中增加 Kubernetes 环境管理能力，使业务用户可以：

1. 接入和管理多个 Kubernetes 集群；
2. 创建、查询、更新和删除 Kubernetes Namespace；
3. 将 Namespace 设置为私有或公开；
4. 将私有 Namespace 分享给指定用户或 IAM Group；
5. 在后续阶段基于该能力创建 Pod、Job、CRD 实例、Agent Sandbox 和 OpenSandbox；
6. 保持现有 Proto-first、Kernel 自动装配、IAM/SpiceDB 授权和前端 SDK 自动生成范式。

本能力不是建设 Rancher/KubeSphere 式独立平台，而是在 AISphere 内建立一套可复用的 Kubernetes Provider SDK 和标准业务 API。

---

## 2. 现状校准

### 2.1 Kernel

Kernel 已采用规范驱动架构：业务声明 Proto 契约，Kernel 负责检查、生成、装配、治理和验证。当前运行时边界已经包含 `authn`、`authz`、`accessx`、`resourcex`、`taskx`、`dbx`、`objectstorex` 等能力，但尚未提供 Kubernetes SDK。

Kernel 当前 Go 版本为 `1.26.4`。本设计锁定：

```text
sigs.k8s.io/controller-runtime v0.24.x
k8s.io/api                       v0.36.x
k8s.io/apimachinery              v0.36.x
k8s.io/client-go                 v0.36.x
```

`controller-runtime v0.24` 与 `k8s.io/* v0.36`、Go 1.26 是官方测试组合。依赖必须按同一 minor 锁定，禁止混用不同 minor 的 Kubernetes 模块。

### 2.2 Hub Backend

Hub 已具备标准 Proto-first 链路：

```text
proto
  -> protoc-gen-go / grpc / http
  -> protoc-gen-go-authz
  -> protoc-gen-go-gateway
  -> protoc-gen-go-kernel
  -> protoc-gen-openapiv2
  -> internal/service
  -> internal/biz
  -> internal/data
```

Skill 模块已经验证了以下模式：

- Proto 中声明 HTTP、AuthN、AuthZ 和审计策略；
- Hub 拥有业务资源，IAM 不拥有 Hub 业务数据；
- IAM/SpiceDB 负责关系和权限判定；
- `public/private + subject sharing` 作为业务资源访问模型；
- `List` 操作由业务层对具体资源批量鉴权，避免未授权枚举。

Kubernetes Cluster 与 Namespace 应沿用该模式，不新增第二套 IAM。

### 2.3 Hub Frontend

Hub Frontend 已采用：

```text
Hub OpenAPI
  -> contract-lock.json / SHA-256 校验
  -> Orval
  -> src/lib/api/generated
  -> adapter
  -> React Query / 页面
```

新功能必须进入同一生成链路。禁止手写重复的 Cluster/Namespace HTTP Client。

### 2.4 IAM / SpiceDB

IAM 当前通过文件维护 SpiceDB Schema，并校验权限清单与 Schema 一致性。新增 Cluster/Namespace 定义属于加法变更，可通过受控 Schema 发布完成；同时必须同步更新 IAM 资源/权限清单及测试。

---

## 3. 核心架构决策

### 3.1 总体架构

```text
Hub Frontend
    |
    | Generated TypeScript SDK
    v
Hub HTTP / gRPC API
    |
    +-- ClusterService
    +-- NamespaceService
    |
    v
Hub Biz Layer
    |
    +-- IAM / SpiceDB authorization
    +-- Cluster credential store
    +-- Kubernetes client pool
    +-- PostgreSQL control-plane records
    |
    v
Kernel kubernetesx
    |
    +-- controller-runtime client
    +-- client-go discovery / dynamic / REST
    +-- Server-Side Apply
    +-- probe / capability detection
    |
    v
Kubernetes API Servers
```

### 3.2 责任边界

#### Kernel 负责

- Kubernetes REST Config 构造；
- Scheme 注册；
- Typed/Unstructured Client；
- Server-Side Apply；
- Discovery、ServerVersion、API capability；
- 超时、QPS/Burst、日志、指标、错误归一化；
- Fake Client 和 envtest 测试辅助；
- 后续 Pod、Job、CRD、Exec、Logs 等基础能力。

Kernel 不负责：

- 集群信息持久化；
- kubeconfig 存储；
- 组织、用户、分享和权限；
- Cluster/Namespace 产品语义；
- Hub API。

#### Hub 负责

- Cluster 和 Namespace 业务模型；
- PostgreSQL 控制面记录；
- CredentialStore；
- 多集群 Client Pool；
- Proto API 和业务校验；
- Namespace 创建、导入、同步和删除；
- SpiceDB 关系写入和权限检查；
- 审计、状态和错误展示。

#### IAM / SpiceDB 负责

- Cluster/Namespace 权限图；
- 用户、Group、Service Account 等主体；
- 关系写入、删除、读取和 CheckPermission；
- 权限清单、角色和 Schema 管理。

#### Frontend 负责

- Cluster/Namespace 产品页面；
- 使用生成 SDK 调用后端；
- IAM 主体选择器；
- 权限驱动的按钮状态和错误展示。

---

## 4. Kernel：`kubernetesx` SDK 设计

### 4.1 包位置

```text
kernel/
└── kubernetesx/
    ├── config.go
    ├── credential.go
    ├── client.go
    ├── factory.go
    ├── scheme.go
    ├── apply.go
    ├── discovery.go
    ├── probe.go
    ├── unstructured.go
    ├── errors.go
    ├── metrics.go
    ├── fake/
    │   └── client.go
    └── kubernetesx_test.go
```

同步修改：

```text
go.mod
go.sum
PACKAGE_INDEX.md
docs/contracts/kubernetesx.md
```

### 4.2 第一阶段不启动 Manager

Cluster/Namespace CRUD 是请求驱动操作，第一阶段直接使用非缓存 `controller-runtime/client.Client`：

```text
HTTP/gRPC Request
  -> authz
  -> kubernetesx.Client
  -> API Server
  -> response
```

不为每个远程集群启动独立 `Manager`、Informer 或 Cache，原因：

- 多集群 Manager 生命周期复杂；
- 每个集群均会建立 Watch 和缓存；
- Hub 多副本时会重复 Watch；
- Cluster 凭据轮换后需重启 Manager；
- Namespace CRUD 不依赖持续 Reconcile。

第一阶段使用 `taskx` 完成周期 Probe 和状态同步。后续只有在明确需要长期 Watch、WarmPool 或自定义 Controller 时，再增加独立 Controller Runtime 进程或专用 Provider 服务。

### 4.3 核心类型

```go
type Config struct {
    Host               string
    Kubeconfig         []byte
    Context            string
    QPS                float32
    Burst              int
    Timeout            time.Duration
    UserAgent          string
    FieldManager       string
    InsecureSkipVerify bool
}

type Client interface {
    Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
    List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
    Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
    Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
    Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
    Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error

    Apply(ctx context.Context, obj client.Object, opts ApplyOptions) error
    ApplyUnstructured(ctx context.Context, obj *unstructured.Unstructured, opts ApplyOptions) error

    ServerVersion(ctx context.Context) (VersionInfo, error)
    Discover(ctx context.Context) (Capabilities, error)
    Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error)

    Scheme() *runtime.Scheme
    RESTConfig() *rest.Config
    Dynamic() dynamic.Interface
    Discovery() discovery.DiscoveryInterface
}
```

`RESTConfig()`、`Dynamic()` 和 `Discovery()` 是基础设施层 escape hatch。Hub Biz 层不得直接依赖这些对象；仅 Hub Data Adapter 可使用。

### 4.4 Scheme

默认注册：

- `core/v1`；
- `apps/v1`；
- `batch/v1`；
- `networking.k8s.io/v1`；
- `rbac.authorization.k8s.io/v1`；
- `apiextensions.k8s.io/v1`。

支持业务注入第三方 Scheme：

```go
client, err := kubernetesx.New(cfg,
    kubernetesx.WithScheme(agentSandboxScheme),
    kubernetesx.WithScheme(kserveScheme),
)
```

对于未引入 Go 类型的第三方 CRD，使用 `unstructured.Unstructured` + GVK。

### 4.5 Server-Side Apply

所有由 AISphere 声明式管理的资源优先使用 SSA：

```text
FieldManager: aisphere-hub
```

后续按控制器拆分：

```text
aisphere-hub-namespace
aisphere-hub-sandbox
aisphere-hub-workload
aisphere-hub-network
```

规则：

1. 只 Apply AISphere 明确拥有的字段；
2. 默认不强占其他 Manager 的字段；
3. 对 AISphere 独占创建和管理的资源，可显式 `ForceOwnership=true`；
4. 冲突转换为 `KUBERNETES_FIELD_CONFLICT`，不得静默覆盖；
5. 保留 `managedFields`，不手工清除。

### 4.6 Probe

Probe 至少返回：

```go
type ProbeResult struct {
    Reachable       bool
    ServerVersion   VersionInfo
    ClusterUID      string
    APIs            []APIResource
    NamespaceAccess AccessReview
    Latency         time.Duration
    Warnings        []string
}
```

接入集群时验证：

- API Server 可达；
- TLS/CA 正确；
- 凭据有效；
- 能读取 ServerVersion；
- 能读取 Namespace（`list namespaces`，limit=1）；
- 能创建、更新、删除 Namespace，或明确标记为只读集群；
- 禁止依赖本地 `exec` credential plugin。

> Probe 的能力检测以 `SelfSubjectAccessReview`（SSAR）为主，**不执行真实 Namespace CRUD**。Kernel `kubernetesx.Probe` 实现为：可达性/版本/API 来自 discovery client；`CanView` 来自 `list namespaces`（limit=1，只读）；`CanCreate`/`CanUpdate`/`CanDelete` 来自对 `namespaces` 资源 create/update/delete 动作的 `SelfSubjectAccessReview`。真实写入只作为显式诊断选项（如 `ProbeRequest` 扩展一个 `DryRunCreate` 诊断标志），接入时默认不触发，避免产生审计噪声、触发 Admission Webhook/配额/Operator/安全策略。第一阶段无 Watch，最小权限不要求 `watch namespaces`。

### 4.7 错误归一化

Kernel 提供稳定错误分类：

```text
KUBERNETES_CONFIG_INVALID
KUBERNETES_CREDENTIAL_INVALID
KUBERNETES_UNAUTHORIZED
KUBERNETES_FORBIDDEN
KUBERNETES_NOT_FOUND
KUBERNETES_ALREADY_EXISTS
KUBERNETES_CONFLICT
KUBERNETES_FIELD_CONFLICT
KUBERNETES_TIMEOUT
KUBERNETES_UNREACHABLE
KUBERNETES_API_UNAVAILABLE
```

错误 metadata 至少包含：

```text
api_group
kind
namespace
name
reason
retryable
```

不得包含 kubeconfig、Token、Client Certificate 私钥。

### 4.8 测试

- Fake Client：纯单元测试；
- envtest：SSA、CRD、字段冲突测试；
- Kind：真实 API Server 集成测试；
- Go race：Client Pool 并发；
- 依赖兼容检查：所有 `k8s.io/*` 与 controller-runtime minor 一致。

---

## 5. Hub：Cluster 控制面设计

### 5.1 API 包

```text
api/kubernetes/v1/cluster.proto
api/kubernetes/v1/namespace.proto
```

Proto package：

```proto
package kubernetes.v1;
option go_package = "github.com/aisphereio/aisphere-hub/api/kubernetes/v1;kubernetesv1";
```

### 5.2 Cluster 资源

Cluster 是 AISphere 控制面资源，不等同于 kubeconfig 文件。

```proto
message Cluster {
  string id = 1;
  string name = 2;
  string display_name = 3;
  string description = 4;
  string org_id = 5;
  string server_url = 6;
  string distribution = 7;
  string kubernetes_version = 8;
  string status = 9;
  string health_message = 10;
  google.protobuf.Timestamp last_probe_time = 11;
  map<string, string> labels = 12;
  ClusterPermissions permissions = 13;
  google.protobuf.Timestamp create_time = 14;
  google.protobuf.Timestamp update_time = 15;
  int64 revision = 16;
  string owner_type = 17;
  string owner_id = 18;
}

message ClusterPermissions {
  bool can_view = 1;
  bool can_operate = 2;
  bool can_manage = 3;
  bool can_create_namespace = 4;
  bool can_delete = 5;
}
```

`Cluster` 字段说明：`owner_type`+`owner_id` 与 §7.2.2 owner tuple 对应（存 canonical 主体，见 §7.2.1 mapping）；`revision` 用于 §5.7.7 CAS；`ClusterPermissions` 字段集对齐 §7.1 Cluster schema 的 `view/operate/manage/create_namespace` 四个 permission + `can_delete` 派生（见 §7.6.2）。

> Cluster V1 无分享 API，`ClusterPermissions` 不含 `can_share`（避免冻结无实现语义的字段）。Namespace 侧 `NamespacePermissions` 含 `can_share`（见 §6.3），因为 Namespace 有 share API。

响应永远不返回：

- kubeconfig；
- Bearer Token；
- Client Key；
- Secret 原文。

### 5.3 Cluster API

```text
POST   /v1/clusters
GET    /v1/clusters
GET    /v1/clusters/{id}
PUT    /v1/clusters/{id}
DELETE /v1/clusters/{id}
POST   /v1/clusters/{id}:probe
POST   /v1/clusters/{id}:rotate-credential
```

建议 Proto Policy：

| RPC | Exposure | Resource | Action |
|---|---|---|---|
| CreateCluster | AUTHORIZED | `zone:{org_id}` | `create_cluster` |
| ListClusters | AUTHENTICATED | handler batch check | `view` |
| GetCluster | AUTHORIZED | `k8s_cluster:{id}` | `view` |
| UpdateCluster | AUTHORIZED | `k8s_cluster:{id}` | `manage` |
| DeleteCluster | AUTHORIZED | `k8s_cluster:{id}` | `manage` |
| ProbeCluster | AUTHORIZED | `k8s_cluster:{id}` | `operate` |
| RotateCredential | AUTHORIZED | `k8s_cluster:{id}` | `manage` |

#### 5.3.1 ListClusters 协议

`ListClusters` 采用 Cluster scoped 同款协议（DB 候选扫描 + BatchCheck，详见 §7.6.3/§7.6.4）：

```text
1. 候选集 = principal 可访问的 zone 下的所有未删除 Cluster（按 org_id+name 有序，keyset pagination）
   - 默认只扫 principal 所在 zone；带 `org_id` 参数时要求该 zone 的 `view_zone` 鉴权（见 §7.6.4），V1 不提供跨 zone 聚合
2. 单次最多扫描 max_scan（默认 1000），BatchCheck(k8s_cluster:{id}#view @ canonical_principal) 填页
3. next_page_token = keyset 游标（org_id+name），不嵌入 revision 快照（见 §7.6.1 约束）
4. 每条 Cluster 携带 ClusterPermissions（BatchCheck 一次性算出 can_view/can_operate/can_manage/can_create_namespace/can_delete，见 §7.6.2）
```

- BatchCheck 失败（SpiceDB 不可达）时 List 整体返回 503，不降级为"返回所有候选"；
- Cluster 数量增长后可改 LookupResources(k8s_cluster#view)（见 §7.6.5）。

#### 5.3.2 UpdateCluster / DeleteCluster / RotateCredential 契约

三个写操作请求必带 `expected_revision`，DB 更新 `WHERE revision = expected_revision`，不匹配返回 `REVISION_CONFLICT`（409）。成功后 `revision + 1`。

- `UpdateCluster` 请求带 `google.protobuf.FieldMask update_mask`，只更新掩码内字段（解决 PUT 清空未传字段问题）。未传 `update_mask` 返回 `INVALID_ARGUMENT`。可变字段白名单见 §5.7.4，修改不可变字段返回 `INVALID_ARGUMENT`。
- `DeleteCluster` 请求带 `expected_revision` + `DeletePolicy`（`DETACH_ONLY`/`CASCADE`，见 §5.7.5）。
- `RotateCredential` 请求带 `expected_revision` + 新 `ClusterCredentialInput`，流程见 §5.7.3。

#### 5.3.3 Hub 错误码（独立于 Kernel）

Hub 控制面错误使用 Hub 级错误码，不复用 Kernel `KUBERNETES_*`（Kernel 错误保留给 Kubernetes API/配置层，见 §4.7）。Hub `errorx` 新增：

```text
REVISION_CONFLICT          CAS 失败（expected_revision 不匹配，409）
INVALID_ARGUMENT           参数错误（FieldMask 未传、修改不可变字段、名称非法等，400）
UNSUPPORTED_PRINCIPAL_TYPE 主体类型不受支持（空 SubjectType、未知类型，401/403）
FAILED_PRECONDITION        状态不满足操作前提（有 Namespace 禁止硬删 Cluster、cluster_uid 不一致、凭据 AAD 损坏，412）
UNAUTHENTICATED            未认证（anonymous 主体访问 Cluster/Namespace API，401）
```

Kernel 透传错误（`KUBERNETES_CREDENTIAL_INVALID`/`KUBERNETES_CONFIG_INVALID`/`KUBERNETES_UNREACHABLE` 等）只在 Hub 调用 kernel `kubernetesx` API 后直接归一化返回时使用；Hub 自身的 CAS、FieldMask、principal 校验、生命周期状态判定一律用上表 Hub 级错误码。

### 5.4 Credential 输入

```proto
message ClusterCredentialInput {
  oneof source {
    bytes kubeconfig = 1;
    InClusterCredential in_cluster = 2;
    ServiceAccountCredential service_account = 3;
  }
}
```

安全要求：

- 请求体日志必须对 `credential` 全字段脱敏；
- kubeconfig 解析后必须 flatten；
- V1 禁止 `exec`、外部文件引用和不受控 auth-provider；
- 私钥只进入 CredentialStore；
- GET/List API 不返回 credential；
- 更新普通元数据不覆盖 credential；
- credential 轮换使用独立 RPC。

### 5.5 CredentialStore

Hub 定义接口（`ref` 由 Store 内部分配，AAD 由 Store 用完整 Locator 构造，调用方不传 `ref` 也不传 `aad.CredentialRef`，避免循环依赖）：

```go
type CredentialLocator struct {
    ClusterID          string
    CredentialRef      string      // Store 在 Put 内部分配（UUID）
    CredentialRevision int64       // 调用方传入目标 revision
}

type ClusterCredentialStore interface {
    // Put 加密 value：Store 内部分配 ref，用 {ClusterID, ref, credentialRevision}
    // 构造 AAD 加密，返回完整 Locator。Biz 用返回值写 Cluster 表。
    Put(
        ctx context.Context,
        clusterID string,
        credentialRevision int64,
        value Credential,
    ) (CredentialLocator, error)

    // Get 用 locator（含 ref + revision）重建 AAD 解密；
    // AAD 不匹配（ref/revision 漂移）返回 FAILED_PRECONDITION（凭据损坏，Hub 级错误码，见 §5.3.3）。
    Get(ctx context.Context, locator CredentialLocator) (Credential, error)

    Delete(ctx context.Context, ref string) error

    // RotateKey 批量重加密所有旧 key_version 的凭据到新 key_version（版本化 AEAD 的轮换路径）。
    RotateKey(ctx context.Context, fromVersion, toVersion string) (reencrypted int, err error)
}
```

调用方契约（创建/轮换）：
- **创建**：Biz 先 `revision=1` 调 `Put(clusterID, 1, cred)` → 拿 `Locator{ref, revision=1}` → DB 事务写 `credential_ref=locator.CredentialRef, credential_revision=1`；
- **轮换**：Biz 算出 `newRevision = current.credential_revision + 1`，调 `Put(clusterID, newRevision, newCred)` → 拿 `Locator` → 临时 Client Probe → CAS 写 DB（见 §5.7.3）；
- AAD 由 **Store 内部**用 Locator 构造，Biz 不参与 AAD 重建；`credentialRevision` 是**目标 revision**（Put 时定）。

V1 Provider：**版本化 AEAD**（V1 不引入 KMS/Vault，按版本化 AEAD 定稿；未来引入 KMS 后再升级为 envelope encryption）。

- AES-256-GCM；
- **主密钥直接加密 ciphertext**（无独立 DEK/KEK 两层），主密钥由环境变量或挂载 Secret 提供，按 `key_version` 版本化；
- 每条凭据独立随机 `nonce`；
- **AAD（附加认证数据）绑定 `cluster_id` + `credential_ref` + `credential_revision`**，防止密文被挪用到其他凭据或修订；AAD 不入库，由 **Store 内部**用 Locator 构造（Put 时 Store 已分配 ref + 接收 revision；Get 时 Store 用传入的 Locator 构造），Biz 不参与 AAD 重建；
- 数据库保存 `ciphertext` + `nonce` + `key_version` + `credential_revision`（不保存明文、不保存 AAD）；`credential_revision` 入库是为了让 Store 在 Get 时能从 DB 行重建完整 Locator（`cluster_id`/`ref`/`revision` 三者齐全）再构造 AAD；
- **`Get(locator)` 必须先校验数据库中的 `cluster_id`/`ref`/`credential_revision` 与传入 Locator 一致**（防止调用方传错 Locator 或 DB 行已被轮换更新），不一致返回 `FAILED_PRECONDITION`；校验通过后 Store 用该 Locator 构造 AAD 解密；
- **主密钥轮换**：新 `key_version` 加入；新写入用新版本；后台任务 `RotateKey` 批量重加密旧版本凭据（解密旧密文 → 用新主密钥重新加密 + 新 nonce → 写回 `ciphertext`/`nonce`/`key_version`）。版本化 AEAD 的轮换代价是全量解密+重加密（无 DEK rewrap 快路径），V1 凭据数量小可接受；
- **旧 key version 退役**：所有引用旧版本的凭据重加密完成且校验通过后，旧主密钥才可退役；退役前 `RotateKey` 任务必须可验证完成（扫描 `key_version=old` 行数为 0）。

> V1 不引入 KMS/Vault 时，DEK/KEK 两层 envelope 收益有限（轮换仍是全量重加密），且表结构需 `wrapped_dek` + 两个 nonce 复杂度高。故 V1 按版本化 AEAD 定稿：主密钥直接加密 + AAD 绑定 + 版本化轮换 + 旧 key 退役。未来引入 Vault/KMS 后再升级为 DEK/KEK envelope（届时 `Put` 增 `wrapped_dek` 列、轮换走 rewrap 路径），接口签名已预留 `CredentialLocator`（含 ref+revision）不破坏。

后续可增加 Vault/KMS Provider（届时升级为 envelope encryption）。Biz 层只依赖接口。

### 5.6 Client Pool

```text
internal/data/kubernetes_client_pool.go
```

> Client Pool 放 `internal/data/Resources` 初始化（与 objectstorex/dtmx 同范式），生命周期由 Resources 统一管理，`Close` 注册到 closers。Biz 层只依赖窄接口 `KubernetesProvider`（Probe/ApplyNamespace/DeleteNamespace/InvalidateCluster），不直接接触 kubeconfig、`kubernetesx.New`、缓存与连接关闭。Data 层内部持有真正的 Client Pool，`pool.Get(ctx, clusterID)` 返回 `kubernetesx.Client`。

能力：

- 按 Cluster ID 懒加载；
- LRU/TTL；
- 并发 singleflight；
- credential revision 感知；
- 轮换、更新、删除时主动 invalidate；
- plaintext credential 不进入日志；
- 每个 Client 独立 Transport 和连接池；
- 全局最大活跃 Client 数可配置。

### 5.7 Cluster 生命周期与状态机

#### 5.7.1 状态机

```text
CREATING  → READY | FAILED
READY     → PROBING → READY | DEGRADED
DEGRADED  → PROBING → READY | DEGRADED
READY     → DELETING → DELETED
DELETING  → DELETED（远端资源清理完成后）
任意状态   → DEGRADED（Probe 失败 / UID 变更 / 凭据失效）
```

`status` 字段取值：`CREATING`/`READY`/`PROBING`/`DEGRADED`/`DELETING`/`DELETED`/`FAILED`。

> 凭据轮换（§5.7.3）不引入 `ROTATING` 中间态：新凭据在切换前用临时 Client Probe 验证，CAS 切换是原子事务，状态保持 `READY`（或从 `DEGRADED` 升 `READY`）。轮换失败时 DB 与 Pool 未变更，无需回滚。

#### 5.7.2 创建顺序与补偿

```text
1. 校验 CredentialInput（kubernetesx.Credential.Validate + SSRF 边界，见 §12.x）
2. CredentialStore.Put → credential_ref + revision=1
3. DB 插入 k8s_clusters：status=CREATING, credential_ref, credential_revision=1
4. SpiceDB 写关系：k8s_cluster:{id}#zone@zone:{org_id} + #owner@{subject_type}:{subject_id}
5. Probe（SSAR 为主）→ 填 cluster_uid/kubernetes_version/status=READY
```

补偿：
- 第 2 步失败：无副作用；
- 第 3 步失败：删除已写入的 credential；
- 第 4 步失败：DB 标 `FAILED`，删除 credential，进入 repair queue 清理 SpiceDB（幂等）；
- 第 5 步失败：DB 标 `DEGRADED`（记录已创建，凭据/关系已写，待人工或 reconcile 重 Probe），不回滚已写入资源。

#### 5.7.3 凭据轮换（先验证新凭据，再 CAS 切换，无需回滚）

`Cluster.revision`（资源版本，§5.7.7 CAS 用）与 `credential_revision`（凭据版本，AAD 绑定用，§5.5）是两个独立计数器，轮换时分别递增。

```text
1. 读当前 cluster，校验请求 expected_revision == cluster.revision
   （CAS，不匹配返回 `REVISION_CONFLICT`）
2. newRevision = current.credential_revision + 1
   newLocator := CredentialStore.Put(clusterID, newRevision, newCred)
   （Store 内部分配 newLocator.CredentialRef 并用 {cluster_id, newLocator.CredentialRef, newRevision}
    构造 AAD 加密；Biz 不传 ref，避免循环依赖，见 §5.5）
3. 用 newLocator 构造临时 kubernetesx.Client（不走 ClientPool，独立 Transport），Probe(newCred) 验证新凭据
   - Probe 失败：Delete(newLocator.CredentialRef)，返回 `KUBERNETES_CREDENTIAL_INVALID`（kernel 透传，Probe 由 kubernetesx 执行），DB 与 Pool 未变更，无需回滚
   - Probe 返回的 cluster_uid 与 DB 记录不一致：Delete(newLocator.CredentialRef)，返回 `FAILED_PRECONDITION`（Hub 级判定，见 §5.3.3），
     拒绝切换（防止新凭据误接入不同集群，见 §5.7.6）
4. DB 事务（WHERE revision = expected_revision）：
   credential_ref = newLocator.CredentialRef
   credential_revision = newRevision
   revision = expected_revision + 1
   status = READY（轮换前已是 READY；若轮换前 DEGRADED 且 Probe 成功则升 READY）
   - CAS 失败（他人在此期间改过）→ Delete(newLocator.CredentialRef)，返回 `REVISION_CONFLICT`
5. ClientPool.Invalidate(clusterID)（按新 credential_revision 感知失效旧 Client，下次 Get 用 newLocator.CredentialRef）
6. 延迟清理 old_ref：CredentialStore.Delete(old_ref)（经 outbox 幂等执行，不阻塞响应）
```

要点：
- 第 3 步 Probe 在切换前完成且校验 cluster_uid，所以第 4 步 CAS 成功后无需回滚——新凭据已验证且确认是同一集群；
- `credential_revision` 递增保证 AAD 绑定新版本，旧密文（若被复制）用旧 AAD 解密失败；
- `revision` 与 `credential_revision` 在同一事务递增，外部观察者通过 `revision` 感知资源变更，通过 `credential_revision` 感知凭据变更；
- old_ref 延迟清理的理由是**回退窗口 + 异步清理**：轮换后若发现新凭据在长连接场景有问题，old_ref 仍可回退；且批量清理走 outbox 异步执行不阻塞响应。已构造的 `rest.Config/Transport` 不会在每次请求重读 DB 凭据，因此"在途 Client 认证失败"不是延迟删除的真实理由。

#### 5.7.4 可变字段白名单

```text
可变：display_name, description, labels（非 aisphere.io/* 保留前缀）
不可变：server_url, org_id, cluster_uid, id, credential_ref（经轮换 RPC 改）
```

`UpdateCluster` 使用 FieldMask（见 §7 API 冻结），未传字段不清空。修改不可变字段返回 `INVALID_ARGUMENT`。

#### 5.7.5 删除策略

- Cluster 下有非删除态 Namespace 时，默认**禁止硬删**，返回 `FAILED_PRECONDITION` 并列出 Namespace 数量；
- 删除选项：
  - `DETACH_ONLY`：仅解除 AISphere 管理（删 DB 记录 + SpiceDB 关系，保留远端 Namespace 与 Cluster 资源）；
  - `CASCADE`：级联删除所有 AISphere 管理的 Namespace（managed=true 的远端 Namespace 也删），需显式确认参数；
- 导入的 Namespace（managed=false）在 `CASCADE` 下默认只解除管理，不删远端；
- Cluster 本身不删远端 Kubernetes 集群（AISphere 无权销毁用户集群），只清理 AISphere 控制面记录与凭据；
- 删除异步：status=DELETING → 清理 Namespaces + SpiceDB 关系 + credential → DELETED。

#### 5.7.6 Cluster UID 变更处理

Probe 发现 `cluster_uid` 与 DB 记录不一致时：
- DB 标 `DEGRADED`，`health_message` 记录 UID 漂移；
- 不自动覆盖 cluster_uid（可能是集群重建、误接入不同集群、或 kube-system 重建）；
- 需人工确认后经 `UpdateCluster`（带 `expected_revision` CAS + 显式 `accept_new_uid` 标志）更新。

#### 5.7.7 并发控制（expected_revision + FieldMask）

- `Cluster` 资源响应携带 `revision`（已有字段）；
- `UpdateCluster`/`RotateCredential`/`DeleteCluster` 请求必带 `expected_revision`，DB 更新 `WHERE revision = expected_revision`，不匹配返回 `REVISION_CONFLICT`；
- `UpdateCluster` 请求带 `FieldMask`，只更新掩码内字段，解决 PUT 清空未传字段问题；
- `revision` 在每次成功更新后 `+1`。

---

## 6. Hub：Namespace 控制面设计

### 6.1 双层模型

必须区分：

1. **远端 Kubernetes Namespace**：Kubernetes API Server 中的真实资源；
2. **AISphere Namespace Record**：Hub 数据库中的控制面记录和权限对象。

AISphere Namespace 使用稳定 UUID 作为资源 ID：

```text
k8s_namespace:{namespace_id}
```

不要直接把 `cluster/name` 拼接为 SpiceDB Object ID。数据库保存：

```text
namespace_id -> cluster_id + kube_name
```

这样可避免名称字符、重命名和跨集群冲突问题。

### 6.2 Namespace API

```text
POST   /v1/clusters/{cluster_id}/namespaces
GET    /v1/clusters/{cluster_id}/namespaces
GET    /v1/namespaces
GET    /v1/namespaces/{id}
PUT    /v1/namespaces/{id}
DELETE /v1/namespaces/{id}
POST   /v1/namespaces/{id}:visibility
GET    /v1/namespaces/{id}/shares
POST   /v1/namespaces/{id}/shares
DELETE /v1/namespaces/{id}/shares/{relation}/{subject_type}/{subject_id}
POST   /v1/clusters/{cluster_id}/namespaces:sync
```

`GET /v1/namespaces` 用于“我的、分享给我的、公开的”聚合列表；`GET /v1/clusters/{cluster_id}/namespaces` 用于集群详情页。

### 6.3 Namespace 业务字段

```proto
message Namespace {
  string id = 1;
  string cluster_id = 2;
  string name = 3;
  string display_name = 4;
  string description = 5;
  NamespaceVisibility visibility = 6;
  NamespaceLifecycle lifecycle = 7;
  bool managed = 8;
  string kubernetes_uid = 9;
  string resource_version = 10;
  map<string, string> labels = 11;
  NamespacePermissions permissions = 12;
  string owner_id = 13;
  google.protobuf.Timestamp last_sync_time = 14;
  google.protobuf.Timestamp create_time = 15;
  google.protobuf.Timestamp update_time = 16;
  int64 revision = 17;
  string owner_type = 18;
  string visibility_sync_status = 19 [deprecated = true]; // 见 visibility_sync_status_enum
  NamespaceVisibility effective_visibility = 20;
  string created_by_type = 21;
  string created_by = 22;
  VisibilitySyncStatus visibility_sync_status_enum = 23;
}

enum VisibilitySyncStatus {
  VISIBILITY_SYNC_UNSPECIFIED = 0;
  VISIBILITY_SYNC_SYNCED = 1;
  VISIBILITY_SYNC_PUBLISHING = 2;
  VISIBILITY_SYNC_REVOKING = 3;
  VISIBILITY_SYNC_FAILED = 4;
}

enum EffectiveVisibility {
  EFFECTIVE_VISIBILITY_UNSPECIFIED = 0;
  EFFECTIVE_VISIBILITY_PUBLIC = 1;   // 三条 wildcard 全部存在
  EFFECTIVE_VISIBILITY_PRIVATE = 2;  // 三条 wildcard 全部不存在
  EFFECTIVE_VISIBILITY_PARTIAL = 3;  // 只存在部分 wildcard（投影中途或漂移）
  EFFECTIVE_VISIBILITY_UNKNOWN = 4;  // 查询失败或状态未知
}

message NamespacePermissions {
  bool can_view = 1;
  bool can_use = 2;
  bool can_edit = 3;
  bool can_manage = 4;
  bool can_share = 5;
  bool can_delete = 6;
}
```

字段说明：
- `visibility` 是 **desired**（DB 持久化的期望可见性），`visibility_sync_status_enum` 记录投影状态（Proto enum `VisibilitySyncStatus`：`SYNCED`/`PUBLISHING`/`REVOKING`/`SYNC_FAILED`，见 §7.5）；`string visibility_sync_status` 字段 19 标 `deprecated`，保留向后兼容但新代码用 enum 字段 23；
- `effective_visibility` 是 **SpiceDB 当前 effective**，类型是独立 enum `EffectiveVisibility`（不复用 `NamespaceVisibility`，因为投影可能部分写入/部分删除，二值枚举表达不了）：
  - `PUBLIC`：三条 wildcard viewer 关系（`user:*`/`service:*`/`service_account:*`）全部存在；
  - `PRIVATE`：三条全部不存在；
  - `PARTIAL`：只存在部分（投影中途崩溃、`PUBLISHING`/`REVOKING`/`SYNC_FAILED` 期间、或 reconcile 漂移未修复）；
  - `UNKNOWN`：SpiceDB 查询失败或状态未知（List/Get 时 SpiceDB 不可达，effective 无法推导）。
  - 前端展示可见性状态必须用 `effective_visibility` + `visibility_sync_status_enum`，不能用 `visibility`，否则会把"正在收窄/部分写入"错误展示成"已经私有/已经公开"；
- `owner_type`/`owner_id` 存 canonical 主体（见 §7.2.1），`created_by_type`/`created_by` 存原始 Principal 身份（审计可区分 agent/workflow/workload）；
- `revision` 用于 §5.7.7/§6.6 CAS；
- `NamespaceVisibility` 枚举：`PRIVATE`/`PUBLIC`（见 §7.3）。`NamespaceLifecycle` 枚举：`CREATING`/`READY`/`TERMINATING`/`FAILED`/`DELETED`。

> **ClusterPermissions 字段集**对齐 §7.1 Cluster schema 的 `view/operate/manage/create_namespace` 四个 permission + `can_delete` 派生（见 §7.6.2）。Cluster V1 无分享 API，`ClusterPermissions` 不含 `can_share`（避免冻结无实现语义的字段）。Namespace 侧 `NamespacePermissions` 含 `can_share`（见 §6.3），因为 Namespace 有 share API。

### 6.4 创建模式

支持：

```text
CREATE_NEW      创建远端 Namespace
IMPORT_EXISTING 导入已有 Namespace
```

创建流程：

```text
1. 认证 Principal
2. Check k8s_cluster:{cluster_id}#create_namespace
3. 校验 Namespace 名称和保留前缀
4. PostgreSQL 创建 CREATING 记录
5. 原子写入 SpiceDB：cluster + owner 关系
6. 使用 kubernetesx SSA 创建 Namespace
7. 更新 UID/resourceVersion/status=READY
8. 返回资源和 effective permissions
```

若第 6 步失败：

- 标记记录 `FAILED`；
- 删除刚写入的关系，失败时进入 repair queue；
- 不返回成功；
- 周期 reconcile 负责清理漂移。

### 6.5 Kubernetes Metadata

AISphere 创建的 Namespace 注入：

```yaml
metadata:
  labels:
    aisphere.io/managed-by: aisphere-hub
    aisphere.io/namespace-id: <uuid>
    aisphere.io/cluster-id: <uuid>
    aisphere.io/org-id: <zone-id>
```

`aisphere.io/*` 为保留前缀，客户端不得覆盖。

### 6.6 更新与删除

#### 更新

V1 允许更新：

- display name；
- description；
- AISphere 允许管理的 labels/annotations；
- visibility。

Kubernetes Namespace 名称不可修改。需要改名时创建新 Namespace 并迁移资源。

`UpdateNamespace` 请求必带 `expected_revision` + `google.protobuf.FieldMask update_mask`，与 §5.3.2 Cluster Update 同一 CAS + FieldMask 模式：
- DB 更新 `WHERE revision = expected_revision`，不匹配返回 `REVISION_CONFLICT`（409），成功后 `revision + 1`；
- `update_mask` 未传返回 `INVALID_ARGUMENT`；
- 未在 `update_mask` 内的字段保持原值，不因 PUT 清空；
- `visibility` 字段更新走 §7.5 可见性写入协议（DB desired + sync_status + Outbox），不是直接改 `visibility` 列后立即返回；响应 `202 Accepted` + `visibility_sync_status_enum`（见 §7.5.4a）；
- 修改不可变字段（`id`/`cluster_id`/`name`/`kubernetes_uid`/`managed`）返回 `INVALID_ARGUMENT`。

#### 删除

- AISphere 创建的 `managed=true` Namespace：默认删除远端 Namespace；
- 导入的 `managed=false` Namespace：默认仅解除 AISphere 管理；
- 删除导入 Namespace 的远端资源必须传显式确认参数；
- Kubernetes Namespace 删除是异步过程，状态进入 `TERMINATING`；
- 只有远端对象消失后才完成本地清理；
- Finalizer 阻塞必须在 UI 展示，不允许强制绕过 Finalizer 作为普通操作。
- `DeleteNamespace` 请求必带 `expected_revision`（CAS，同 §5.3.2），可选 `DeletePolicy`（`DETACH_ONLY`/`CASCADE`，语义对齐 §5.7.5 Cluster 删除策略）。

---

## 7. SpiceDB 权限模型

### 7.1 新增 Schema

建议在 IAM 增加：

```zed
definition k8s_cluster {
  relation zone: zone
  relation custom_binding: role_binding

  relation owner: user | service | service_account | group#member
  relation admin: user | service | service_account | group#member
  relation operator: user | service | service_account | group#member
  relation viewer: user | service | service_account | group#member

  permission manage = owner + admin + zone->manage_clusters + custom_binding->manage
  permission operate = manage + operator + custom_binding->operate
  permission view = operate + viewer + zone->view_zone + custom_binding->view
  permission create_namespace = manage + operator
}

definition k8s_namespace {
  relation cluster: k8s_cluster
  relation custom_binding: role_binding

  relation owner: user | service | service_account | group#member
  relation editor: user | service | service_account | group#member
  relation user: user | service | service_account | group#member
  relation viewer: user | user:* | service | service:* | service_account | service_account:* | group#member

  permission manage = owner + cluster->manage + custom_binding->manage
  permission edit = manage + editor + custom_binding->edit
  permission use = edit + user + custom_binding->operate
  permission view = use + viewer + custom_binding->view
}
```

> `k8s_cluster.manage` 使用 `zone->manage_clusters`（不使用 `zone->manage_permissions`）。`manage_permissions` 会让 zone 的"权限管理员"间接获得集群生命周期管理能力，权限过宽；`manage_permissions` 仅用于 IAM 权限管理本身，不得复用于集群管理。`manage_clusters` 与下文 `zone.manage_clusters` 能力定义一致。

同时在 `custom_role`、`role_binding`、`zone` 中增加 `create_cluster` 与 `manage_clusters`。按 IAM 现有 `create_skill`/`manage_skills` 的三层镜像模式（参照 `configs/spicedb/aisphere.schema.zed` 与 `configs/resource/defaults.yaml`），需在四处同步加法：

1. `custom_role`：`relation create_cluster: user:*` + `relation manage_clusters: user:*`，以及 `permission can_create_cluster = create_cluster` + `permission can_manage_clusters = manage_clusters`；
2. `role_binding`：`permission create_cluster = role->can_create_cluster & grantee` + `permission manage_clusters = role->can_manage_clusters & grantee`；
3. `zone`：`permission create_cluster` + `permission manage_clusters`（见下）；
4. `configs/resource/defaults.yaml` 的 `zone` `permissions:` 数组追加 `create_cluster, manage_clusters`（`permission-manifest-check` CI 强制 set-equal，遗漏即失败）。

建议权限：

```text
zone.create_cluster = owner + admin + platform->manage_control_plane + custom_binding->create_cluster
zone.manage_clusters = owner + admin + platform->manage_control_plane + custom_binding->manage_clusters
```

Cluster `manage` 继承 `zone->manage_clusters`，不复用通用 `manage_permissions`。最终 Schema 使用明确的资源能力，避免权限过宽。

### 7.2 默认关系与主体映射

#### 7.2.1 主体类型 canonical mapping

Kernel `authn.Principal`（`kernel/authn/types.go`）支持 `user`/`service`/`agent`/`workflow`/`workload`/`anonymous` 等 `SubjectType`。SpiceDB schema 的 Cluster/Namespace relation 只声明 `user | service | service_account | group#member` 四类主体。两者不直接 1:1，必须经 canonical mapping 投影，禁止把任意 `principal.SubjectType` 原样写入 SpiceDB tuple。

**canonical 主体类型映射**（SpiceDB relation 侧）：

| `authn.SubjectType` | SpiceDB 主体类型 | canonical SubjectID |
|---|---|---|
| `user` | `user` | `{SubjectID}` |
| `service` | `service` | `{SubjectID}` |
| `agent` | `service_account` | `agent/{SubjectID}` |
| `workflow` | `service_account` | `workflow/{SubjectID}` |
| `workload` | `service_account` | `workload/{SubjectID}` |
| `anonymous` | （拒绝） | — |
| 其他未知类型 | （拒绝） | — |

**canonical SubjectID 必须带原始类型前缀**（`agent/<id>`/`workflow/<id>`/`workload/<id>`），避免 `agent:123` 与 `workflow:123` 映射成同一 `service_account:123` 发生 ID 碰撞。前版"映射不丢信息"不成立——`custom_role` 也无法从 `service_account:123` 重新识别原始类型；带前缀后 SpiceDB tuple 仍用 `service_account` 主体类型（schema 不变），但 SubjectID 携带原始类型，`custom_role` 与审计可还原。

**Hub 写入/比较契约**：
- Hub 在写入 owner/share tuple 前经 `canonicalSubject(principal) (spicedbType, spicedbID, error)` 桥接；**空 `SubjectType` 直接拒绝**（返回 `UNSUPPORTED_PRINCIPAL_TYPE`），不 fallback 为 `user`（fallback 仅限 Hub 内部认证链路已确认是 user 的旧路径，Kubernetes API 新路径不沿用）；
- **`owner_type`/`owner_id` 存 canonical 身份**（与 SpiceDB tuple 一致，如 `owner_type=service_account, owner_id=agent/123`）；
- **`created_by_type`/`created_by` 存原始 Principal 身份**（如 `created_by_type=agent, created_by=123`），否则审计无法区分 agent/workflow/workload；
- Share 表 `created_by_type`/`created_by` 同样存原始身份；
- **全局列表 owner 比较**（§7.6.1"我创建的"分区）必须用 canonical 身份比较，不能拿原始 `principal.subject_type`/`subject_id` 与 `owner_type`/`owner_id` 直接比（会因 canonical 前缀不匹配而漏判）。

> **设计决策**：不把 `agent`/`workflow`/`workload` 直接加进 SpiceDB schema 主体类型，理由：(1) SpiceDB 主体类型膨胀会让 `viewer`/`owner` 等 relation 的 subject 类型列表变长，manifest 校验成本上升；(2) 这三类在 AISphere 语义里都由服务账号承载，映射到 `service_account` 主体类型 + 带前缀 SubjectID 不丢类型信息；(3) 未来若需要区分 agent 与 workload 的权限策略，通过 `custom_role` 角色绑定区分，不通过 SpiceDB 主体类型区分。

> **实现 gap**：Kernel `authn` 目前没有 `SubjectTypeServiceAccount = "service_account"` 常量，也无 `CanonicalSpiceDBSubject()` helper。后续 Kernel 补丁应在 `authn` 层提供该 helper（含前缀规则 + 空 SubjectType 拒绝），让映射逻辑下沉到 Kernel 而非散落在各 Hub usecase。Hub 实现时先按本节 mapping 表 + 前缀规则写 helper，待 kernel 补丁下沉。

#### 7.2.2 owner tuple

创建 Cluster：

```text
k8s_cluster:{cluster_id}#zone@zone:{org_id}
k8s_cluster:{cluster_id}#owner@{canonical_subject_type}:{canonical_subject_id}
```

创建 Namespace：

```text
k8s_namespace:{namespace_id}#cluster@k8s_cluster:{cluster_id}
k8s_namespace:{namespace_id}#owner@{canonical_subject_type}:{canonical_subject_id}
```

`canonical_subject_type`/`canonical_subject_id` 由 §7.2.1 mapping 从 `principal.SubjectType`/`principal.SubjectID` 派生（如 `agent:123` → `service_account:agent/123`）。数据库 `k8s_clusters`/`k8s_namespaces` 的 `owner_type`+`owner_id` 存 canonical 值（与 SpiceDB tuple 一致），`created_by_type`+`created_by` 存原始 Principal 身份（审计用）。

### 7.3 可见性

V1 定义：

- `PRIVATE`：仅 owner/editor/user/viewer 等显式关系可访问；
- `PUBLIC`：所有已认证主体可查看；
- 公开不自动授予 edit、use 或 manage。

公开关系必须覆盖所有已认证主体类型，与 Hub `skill_usecase.go` 的 Public 处理一致（对 `user:*`/`service:*`/`service_account:*` 三个 wildcard 同时授予 `viewer`）：

```text
k8s_namespace:{id}#viewer@user:*
k8s_namespace:{id}#viewer@service:*
k8s_namespace:{id}#viewer@service_account:*
```

若未来需要“公共可运行”，应新增独立 `PUBLIC_USE` 或 policy，不应让 `PUBLIC` 隐式扩大到执行权限。

### 7.4 分享

V1 允许分享关系：

```text
viewer
user
editor
```

禁止通过分享接口授予 owner。

用户分享：

```text
k8s_namespace:{id}#viewer@user:{user_id}
```

Group 分享：

```text
k8s_namespace:{id}#user@group:{group_id}#member
```

Hub 不展开 Group 成员，不管理 Group 生命周期。

### 7.5 可见性写入协议

#### 7.5.2 存储角色与写入协议（持久化 desired + 最终一致投影）

统一全文表述，明确各存储职责与 desired/effective 区分：

- **PostgreSQL `k8s_namespaces.visibility` 保存 desired（期望可见性）**：每次切换第一事务就把 `visibility` 写为目标值。`visibility_sync_status` 记录 SpiceDB 投影状态。`visibility` 的职责是**写入目标、审计、reconcile 重试依据**，不是权限判断源也不是产品展示唯一来源。
- **SpiceDB 是权限判断源（effective）**：运行时 `CheckPermission`/`BatchCheck`/`LookupResources` 只读 SpiceDB。SpiceDB 必须趋近 DB desired，漂移由 reconcile 修复。
- **产品展示**用 `visibility` + `visibility_sync_status_enum` + `effective_visibility` 三者组合（见 §6.3），不能只用 `visibility`，否则会把"正在收窄/部分写入"展示成已生效状态。
- **desired ≠ effective**：`visibility=PUBLIC + sync=PUBLISHING/SYNC_FAILED` 时 SpiceDB 可能仍无 wildcard 或只有部分（effective 为 `PRIVATE`/`PARTIAL`）；`visibility=PRIVATE + sync=REVOKING` 时 SpiceDB 可能仍有部分 wildcard（effective 为 `PUBLIC`/`PARTIAL`）。**任何 List/Check 都不能仅凭 DB `visibility` 放行或拒绝**，必须经 SpiceDB。
- **写入协议 = DB 事务先写 desired + sync_status + Outbox，再投影 SpiceDB，reconcile 修复 drift**：交互请求可同步投影 SpiceDB 以即时生效，Outbox 保证失败重试，reconcile 做最终一致性。所有写入幂等。

`visibility_sync_status` 取值：`SYNCED`（DB desired 与 SpiceDB effective 一致）、`PUBLISHING`（正在写 wildcard，desired=PUBLIC）、`REVOKING`（正在删 wildcard，desired=PRIVATE）、`SYNC_FAILED`（投影失败，按 desired 方向继续重试，详见 7.5.5）。

#### 7.5.3 PRIVATE → PUBLIC（持久化 desired=PUBLIC，再投影扩大权限）

```text
1. DB 事务：visibility=PUBLIC, visibility_sync_status=PUBLISHING，同事务写 Outbox event
   （此时 SpiceDB 仍无 wildcard，effective 仍 PRIVATE，权限未扩大）
2. 写 SpiceDB wildcard viewer（user:*/service:*/service_account:* 三条）
3. 成功：DB visibility_sync_status=SYNCED
   失败：补偿删除已写入的 wildcard（用 WithoutCancel ctx），
        DB visibility_sync_status=SYNC_FAILED（visibility 保持 PUBLIC=desired），进入 reconcile 重试
```

第 1 步只改 DB desired + Outbox，不触碰 SpiceDB，effective 未扩大。第 2 步失败时第 3 步补偿删除已写入的 wildcard，使 SpiceDB 回到无 wildcard（PRIVATE effective）。最坏情况：第 2 步部分写入后崩溃 → reconcile 检测 `PUBLISHING`/`SYNC_FAILED` + desired=PUBLIC，重试补齐 wildcard 至 SYNCED。desired 已是 PUBLIC，reconcile 方向明确（补写而非回滚 DB）。

#### 7.5.4 PUBLIC → PRIVATE（持久化 desired=PRIVATE，再投影收窄权限）

```text
1. DB 事务：visibility=PRIVATE, visibility_sync_status=REVOKING，同事务写 Outbox event
   （此时 SpiceDB 仍有 wildcard，effective 仍 PUBLIC，但 DB desired 已收窄为 PRIVATE）
2. 删 SpiceDB wildcard viewer（三条）
3. 成功：DB visibility_sync_status=SYNCED
   失败：DB visibility_sync_status=SYNC_FAILED（visibility 保持 PRIVATE=desired），进入 reconcile 重试
```

第 1 步把 desired 写成 PRIVATE + Outbox 持久化。第 2 步失败时 desired 已是 PRIVATE，reconcile 检测 `REVOKING`/`SYNC_FAILED` + desired=PRIVATE 重试删除 wildcard 至完成。`REVOKING` 期间 effective 仍 PUBLIC（wildcard 未删完），List/Check 必须经 SpiceDB 判定，不能凭 DB `visibility=PRIVATE` 直接放行或拒绝。

#### 7.5.4a Update visibility API 响应语义

`UpdateNamespace` 改 visibility 的响应：
- HTTP `202 Accepted`（非 200），表示"desired 已持久化，投影进行中"；
- 响应体携带当前 `visibility_sync_status`（`PUBLISHING`/`REVOKING`/`SYNCED`）；
- 调用方不得认为 202 返回时 effective 已变更——effective 收敛由 reconcile 完成，前端应轮询或等 `SYNCED`；
- 投影失败（`SYNC_FAILED`）不返回 5xx 让调用方以为变更不会生效——desired 已持久化，reconcile 会继续按 desired 重试，返回 202 + `SYNC_FAILED` 让调用方知道"已持久化但投影滞后"。

#### 7.5.5 reconcile 与 drift 修复

后台 reconcile（经 `taskx.Runtime`）周期扫描 `visibility_sync_status != SYNCED` 的记录，**按 DB desired 单向收敛**：

- `PUBLISHING`/`SYNC_FAILED` + desired=PUBLIC → 补写 wildcard 至三条齐全 → `SYNCED`；
- `REVOKING`/`SYNC_FAILED` + desired=PRIVATE → 补删 wildcard 至全部删除 → `SYNCED`；
- desired 与 SpiceDB observed 漂移（LookupSubjects 查 wildcard 数量与 desired 不符）→ 按 desired 修复。

reconcile 不改 `visibility`（desired 已在第一事务定），只改 `visibility_sync_status` 与 SpiceDB。数据库用于产品展示，SpiceDB 是权限判定源。

### 7.6 List 权限与权限映射

禁止只依赖数据库 visibility 过滤。

#### 7.6.1 List 协议

V1 分两种 List，分别用不同主路径，保证候选完整性：

**全局列表 `GET /v1/namespaces`（"我的/分享给我的/公开的"聚合）—— LookupResources 主路径：**

候选完整性要求：一条 Namespace 对主体 `P` 可见（`view=true`）的路径包括直接 owner、直接 share、`group#member` 间接分享、`custom_binding->view`、`cluster->manage/operate/view` 继承、`visibility=PUBLIC` 的 wildcard。DB 侧任何索引都不是授权候选集的超集。因此全局列表 V1 直接用 SpiceDB `LookupResources` 作为主路径，由权限图完整计算候选，DB 只负责 hydrate 字段。

Hub 已有 `LookupResources` 契约（`internal/biz/authz.go` `AuthzLookupResourcesRequest{Limit, Cursor}` → `AuthzLookupResourcesResult{Resources, NextCursor, ConsistencyToken}`，`internal/data/authz.go` 透传 kernel adapter）。流程：

```text
1. SpiceDB LookupResources(k8s_namespace#view @ canonical_principal, limit=page_size, cursor=page_token, consistency=req.consistency or minimize_latency)
   → 返回 view=true 的 Namespace ID 列表 + next_cursor + consistency_token
   （owner/group/custom_binding/cluster inheritance/public wildcard 全由权限图计算，不漏判）
2. 按返回 ID 批量 hydrate PostgreSQL（cluster_id/name/display_name/visibility/visibility_sync_status_enum/effective_visibility/...）
3. hydrate 后丢弃已删除的 ID（并发删除），对剩余 Namespace 批量 BatchCheck(use/edit/manage @ canonical_principal)
   **BatchCheck 必须复用第 1 步 LookupResources 返回的同一 consistency_token**，
   否则 view 结果与 use/edit/manage 结果可能来自不同授权版本（第一页 view=true 但 BatchCheck 时关系已被撤销）
4. 若 hydrate 丢弃了 ID 导致结果页未填满 page_size，**服务端继续补页**（不让前端承担）：
   - 使用同一 consistency_token，用第 1 步返回的 next_cursor 继续调 LookupResources；
   - 限制最多 `max_hydrate_rounds` 轮（Hub 配置，默认 3），防止 SpiceDB/DB 持续漂移导致死循环；
   - 每轮补页的结果 append 到结果集，BatchCheck 同样用同一 consistency_token；
   - 达到 `max_hydrate_rounds` 仍未填满：返回当前已收集的部分页 + next_page_token（指向最后一轮 LookupResources 的 cursor），前端发下一页继续；
   - 这样符合 `page_size` 语义（前端拿到的是已填满或尽力填满的有效页），不把 SpiceDB/DB 短暂漂移暴露给前端。
5. 返回 page_size 条（或部分页）+ next_page_token = 最后一轮 LookupResources 的 next_cursor + consistency_token
   （token 是 SpiceDB 一致性游标，与 DB 无关；前端 opaque 透传，下次请求可带 consistency_token 保证同一视图）
```

约束：
- `next_page_token` 是 SpiceDB LookupResources cursor，**不是 DB 游标**；前端 opaque 透传，不得假设是结果索引或 DB 偏移；
- **BatchCheck 与 LookupResources 必须用同一 consistency_token**，保证 view/use/edit/manage 来自同一授权快照；
- LookupResources 不可达或 BatchCheck 不可达时 List 整体返回 503，不降级；
- 全局列表**不按 DB `visibility` 过滤**（desired ≠ effective，见 §7.5.2）——PUBLIC 的可见性已由 LookupResources 通过 wildcard viewer 关系计算；
- 跨 zone：V1 仅 principal 所在 zone（`org_id == principal.org_id`），不提供跨 zone 聚合（见 §7.6.4）。

**Cluster 内列表 `GET /v1/clusters/{cluster_id}/namespaces` —— DB 全量候选 + BatchCheck 扫描：**

Cluster scoped 候选集是"该 cluster 下所有未删除 Namespace"，范围有限且可全量扫描，BatchCheck 兜底所有授权路径（group/custom_binding/cluster inheritance/public）。流程：

```text
1. 候选集 = 该 cluster_id 下所有未删除 Namespace（按 kube_name 有序，keyset pagination）
2. 单次请求最多扫描 max_scan 条候选（Hub 配置，默认 1000），防止无权限用户扫描整张表
3. 按顺序消费候选：批量 BatchCheck(k8s_namespace:{id}#view @ canonical_principal)，
   view=true 的填入结果页，view=false 的丢弃
4. 页填满 page_size 立即停止扫描，next_page_token 指向【最后一条已消费候选】
   （不是"最后一条已返回"——已消费但被 BatchCheck 过滤的候选也算游标推进，否则下次会重扫被过滤的行）
5. 达到 max_scan 上限但结果页未填满：返回当前已收集的结果（可能为 0 条）+ next_page_token
   指向最后一条已消费候选，前端可发下一页继续；返回 0 条时游标仍前进（避免无权限用户卡在第一页）
6. 每条 Namespace 携带 NamespacePermissions（BatchCheck 一次性算出 view/use/edit/manage，share/delete 见 7.6.2）
```

约束：
- `next_page_token` 是普通 keyset pagination 游标（`cluster_id + kube_name`），**不嵌入 revision 快照**——单条资源 revision 检测不了并发插入/删除，collection generation 留待后续。并发删除/重命名时游标按 keyset 语义推进（跳过已删除行），前端可能看到旧行消失但不重复或漏行；
- `max_scan` 是单次请求扫描上限，不是结果上限；页填满即停，未填满但达上限也返回（前端分页继续）；
- BatchCheck 失败（SpiceDB 不可达）时 List 整体返回 503，不降级为"返回所有候选"；
- Cluster scoped 列表同样**不按 DB `visibility` 过滤**（desired ≠ effective）。

#### 7.6.2 can_share / can_delete → SpiceDB permission 映射

`NamespacePermissions.can_share`/`can_delete` 不是 SpiceDB schema 的独立 permission，而是 Hub 在 BatchCheck 阶段对已有 permission 的派生：

```text
can_view    = CheckPermission(k8s_namespace:{id}#view    @ canonical_principal)
can_use     = CheckPermission(k8s_namespace:{id}#use     @ canonical_principal)
can_edit    = CheckPermission(k8s_namespace:{id}#edit    @ canonical_principal)
can_manage  = CheckPermission(k8s_namespace:{id}#manage  @ canonical_principal)
can_share   = can_manage  （只有 manage 可分享，避免 editor 滥分享提权）
can_delete  = can_manage  （只有 manage 可删除；managed=false 的导入 Namespace 删除远端需额外显式确认参数）
```

全局列表中 `can_view` 已由 LookupResources 保证（在结果集内的都 view=true），只需 BatchCheck `use/edit/manage`；Cluster scoped 列表 BatchCheck 一次性算出全部四个。

Cluster 侧（`can_share` 已删除，见 §5.2 `ClusterPermissions`）：

```text
can_view             = CheckPermission(k8s_cluster:{id}#view             @ canonical_principal)
can_operate          = CheckPermission(k8s_cluster:{id}#operate          @ canonical_principal)
can_manage           = CheckPermission(k8s_cluster:{id}#manage           @ canonical_principal)
can_create_namespace = CheckPermission(k8s_cluster:{id}#create_namespace @ canonical_principal)
can_delete           = can_manage
```

> **设计决策**：`can_share`/`can_delete` 派生自 `manage` 而非引入 SpiceDB 新 permission。理由：(1) 避免在 schema 里增加与 `manage` 语义重叠的 permission 造成 manifest 校验复杂化；(2) 分享与删除都是"可能影响资源可见性/存在性"的高危操作，与 `manage` 语义一致；(3) 未来若需要更细粒度（如 `editor` 可分享 `viewer` 但不可分享 `editor`），再在 schema 增加 `share` permission 并独立 BatchCheck，不破坏当前契约。Cluster V1 无分享 API，`ClusterPermissions` 不含 `can_share`。

前端不得自行推导权限，所有 `can_*` 字段以服务端返回为准。

#### 7.6.3 Cluster 列表 `GET /v1/clusters`

`ListClusters` 候选集完整性同 Namespace：Cluster 可见路径包括直接 owner、直接关系、`custom_binding`、`zone->view_zone` 继承。V1 采用 Cluster scoped 同款协议（DB 候选扫描 + BatchCheck），因为 Cluster 数量远小于 Namespace，且按 zone 范围收窄：

```text
1. 候选集 = principal 可访问的 zone 下的所有未删除 Cluster（按 org_id+name 有序，keyset pagination）
2. 单次最多扫描 max_scan（默认 1000），BatchCheck(k8s_cluster:{id}#view @ canonical_principal) 填页
3. next_page_token = keyset 游标（org_id+name），不嵌入 revision 快照
4. 每条 Cluster 携带 ClusterPermissions（BatchCheck 一次性算出）
```

Cluster 数量增长或需跨 zone 完整授权视图时，可改 LookupResources(k8s_cluster#view)，与 Namespace 全局列表同模式。

#### 7.6.4 跨 zone 鉴权

现有 `platform` definition（`aisphere.schema.zed`）无 `view_zone` permission，只有 `manage_identity`/`manage_control_plane`/`manage_permissions`，`zone#view_zone` 通过 `platform->manage_identity` 继承。

**V1 不提供跨 zone 聚合列表**（即不提供 `all_orgs=true` 参数）。理由：
- 跨 zone 聚合需对多个 zone 分别调 LookupResources 后合并，单 cursor 无法表达多 zone 游标，必须定义 opaque 复合 token（每 zone 独立 cursor + consistency_token + merge_key），契约与实现复杂度高；
- V1 用例集中在"我所在 zone 的 Cluster/Namespace 管理"，跨 zone 全局视图需求不明确；
- platform admin 需要跨 zone 视图时，V1 通过显式切 zone（带 `org_id` 参数 + 该 zone 的 `view_zone` 鉴权）逐个查看，不提供一次性聚合。

**V1 行为**：
- 所有 List 默认且仅在 principal 所在 zone（`org_id == principal.org_id`）范围；
- 请求带 `org_id` 参数时，要求 principal 在该 zone 有 `view_zone`（直接或经 `platform->manage_identity` 继承），否则 `PERMISSION_DENIED`；
- 不存在 `all_orgs` 参数，API 不接受该参数（传则 `INVALID_ARGUMENT`）。

**后续演进**：跨 zone 聚合作为独立能力设计，引入复合游标 token（`{zone_cursors: map<zone_id, cursor>, consistency_token, merge_key}`）+ `platform->manage_identity` 鉴权 + 多 zone 并发 LookupResources + 合并排序，不在 V1 范围。

#### 7.6.5 后续演进

V1 全局 Namespace 列表用 LookupResources、Cluster 列表用 BatchCheck 扫描。数据量增长后两者都可统一到 LookupResources（Cluster 列表改 `LookupResources(k8s_cluster#view)`），分页用 SpiceDB cursor + consistency token。独立授权索引表（`k8s_namespace_authorized_subjects`，由 reconcile 反向投影）作为备选，不在 V1 范围。

---

## 8. PostgreSQL 数据模型

### 8.1 `k8s_clusters`

```text
id UUID PK
org_id VARCHAR NOT NULL
name VARCHAR NOT NULL
display_name VARCHAR
description TEXT
server_url TEXT NOT NULL
credential_ref VARCHAR NOT NULL
credential_revision BIGINT NOT NULL
distribution VARCHAR
kubernetes_version VARCHAR
cluster_uid VARCHAR
status VARCHAR NOT NULL
health_message TEXT
labels_json JSONB
last_probe_at TIMESTAMPTZ
owner_type VARCHAR NOT NULL
owner_id VARCHAR NOT NULL
created_by_type VARCHAR NOT NULL
created_by VARCHAR NOT NULL
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
deleted_at TIMESTAMPTZ
revision BIGINT NOT NULL
```

`owner_type`/`owner_id` 存 **canonical** 身份（与 SpiceDB tuple 一致，支持未来所有权转移），`created_by_type`/`created_by` 存**原始 Principal 身份**（不可变审计，可区分 agent/workflow/workload，见 §7.2.1）。两对字段与 §5.2 `Cluster.owner_type`/`owner_id` proto 字段、§7.2.2 owner tuple 对齐。多数场景下 `owner_id == created_by`（canonical 后），但语义独立。

约束：

```text
UNIQUE(org_id, name) WHERE deleted_at IS NULL
UNIQUE(cluster_uid) WHERE cluster_uid IS NOT NULL AND deleted_at IS NULL
```

### 8.2 `k8s_cluster_credentials`

```text
ref UUID PK
cluster_id UUID NOT NULL
credential_revision BIGINT NOT NULL
ciphertext BYTEA NOT NULL
nonce BYTEA NOT NULL
key_version VARCHAR NOT NULL
credential_type VARCHAR NOT NULL
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
```

字段对齐 §5.5 版本化 AEAD：
- `credential_revision`：凭据版本，与 §5.5 AAD 绑定的 `credential_revision` 一致，Get 时用于重建 AAD；
- `ciphertext` + `nonce` + `key_version`：AEAD 三元组，单 nonce（无 DEK wrap nonce，因为 V1 无独立 DEK/KEK 两层）；
- 无 `wrapped_dek` 列（V1 不是 envelope encryption；未来升级 envelope 时再加）。

约束：

```text
UNIQUE(cluster_id, credential_revision)
```

### 8.3 `k8s_namespaces`

```text
id UUID PK
cluster_id UUID FK
kube_name VARCHAR NOT NULL
display_name VARCHAR
description TEXT
visibility VARCHAR NOT NULL
visibility_sync_status VARCHAR NOT NULL
lifecycle VARCHAR NOT NULL
managed BOOLEAN NOT NULL
kubernetes_uid VARCHAR
resource_version VARCHAR
labels_json JSONB
annotations_json JSONB
owner_type VARCHAR NOT NULL
owner_id VARCHAR NOT NULL
created_by_type VARCHAR NOT NULL
created_by VARCHAR NOT NULL
last_sync_at TIMESTAMPTZ
last_error_code VARCHAR
last_error_message TEXT
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
deleted_at TIMESTAMPTZ
revision BIGINT NOT NULL
```

`owner_type`/`owner_id`/`created_by_type`/`created_by` 语义同 §8.1 `k8s_clusters`。`visibility_sync_status` 见 §7.5。`revision` 见 §5.7.7/§6.6 CAS。

约束：

```text
UNIQUE(cluster_id, kube_name) WHERE deleted_at IS NULL
UNIQUE(cluster_id, kubernetes_uid) WHERE kubernetes_uid IS NOT NULL AND deleted_at IS NULL
```

### 8.4 `k8s_namespace_shares`

用于展示、审计和漂移修复的控制面事实：

```text
id UUID PK
namespace_id UUID NOT NULL
relation VARCHAR NOT NULL
subject_type VARCHAR NOT NULL
subject_id VARCHAR NOT NULL
subject_relation VARCHAR NOT NULL DEFAULT ''
sync_status VARCHAR NOT NULL
created_by_type VARCHAR NOT NULL
created_by VARCHAR NOT NULL
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
```

`subject_relation` 用 `NOT NULL DEFAULT ''` 而非可空：PostgreSQL 普通 UNIQUE 约束允许重复 NULL，会导致同一 `(namespace_id, relation, subject_type, subject_id, NULL)` 多行无约束。空字符串表示"无 subject_relation"（即 subject 是具体 user/service_account，非 group#member）。`created_by_type`/`created_by` 同 §8.1 canonical 主体审计。

唯一约束：

```text
UNIQUE(namespace_id, relation, subject_type, subject_id, subject_relation)
```

### 8.5 Outbox

关系写入建议进入通用 outbox：

```text
aggregate_type
aggregate_id
event_type
payload_json
status
retry_count
next_retry_at
```

V1 交互请求可同步投影 SpiceDB；Outbox 用于失败重试和最终一致性修复。所有写入必须幂等。

---

## 9. Hub 后端目录与开发任务

```text
api/kubernetes/v1/
├── cluster.proto
└── namespace.proto

internal/biz/
├── kubernetes_cluster.go
├── kubernetes_namespace.go
└── kubernetes_errors.go

internal/data/
├── kubernetes_cluster.go
├── kubernetes_namespace.go
├── kubernetes_client_pool.go
├── kubernetes_credential_store.go
├── kubernetes_authz_projection.go
└── kubernetes_reconcile.go

internal/service/
├── kubernetes_cluster.go
└── kubernetes_namespace.go

migrations/postgres/
└── 0000xx_kubernetes_environment.*.sql
```

同步修改：

```text
internal/conf
internal/data/resources.go
cmd/aisphere-hub/main.go
internal/server/http.go
internal/server/grpc.go
configs/*.yaml
Makefile / contract bundle
README.md
```

### 9.1 Service 范式

Service 只负责 Proto 与 Biz DTO 转换：

```go
type ClusterService struct {
    kubernetesv1.UnimplementedClusterServiceServer
    uc *biz.ClusterUsecase
}

func (s *ClusterService) RegisterHTTPServer(server *khttp.Server) {
    kubernetesv1.RegisterClusterServiceHTTPServer(server, s)
}
```

权限和资源模板声明在 Proto；复杂 List 过滤、public fallback 和关系写入在 Biz 层完成。

### 9.2 后台任务

使用 Kernel `taskx.Runtime`：

```text
cluster.probe
namespace.sync
namespace.delete.finalize
authz.relationship.repair
credential.cleanup
```

禁止用进程内 ticker 作为生产唯一调度器。

---

## 10. Frontend 设计

### 10.1 契约生成

后端：

```bash
make api
make proto-check
make contract-bundle
```

前端：

```bash
npm run contract:sync -- --ref <hub-commit-sha>
npm run api:check
npm run typecheck
npm run test
npm run build
```

生成目录：

```text
src/lib/api/generated/
```

适配层：

```text
src/lib/api/adapters/clusters.ts
src/lib/api/adapters/namespaces.ts
```

Adapter 只做：

- Proto JSON 字段规范化；
- 页面稳定 Domain Model；
- 分页 Token；
- 错误映射；
- Query 参数整理。

不得在 Adapter 重新实现后端权限逻辑。

### 10.2 页面

```text
/environment/clusters
/environment/clusters/[clusterId]
/environment/namespaces
/environment/namespaces/[namespaceId]
```

建议导航：

```text
环境管理
├── Kubernetes 集群
└── Namespace
```

### 10.3 Cluster 页面

- 集群列表；
- 状态、版本、Endpoint、最后探测时间；
- 创建/编辑；
- kubeconfig 上传；
- Probe 结果；
- Credential 轮换；
- 删除确认；
- Namespace 数量；
- 审计入口。

### 10.4 Namespace 页面

- 我的 Namespace；
- 分享给我的；
- 公开 Namespace；
- 按 Cluster、状态和可见性过滤；
- 创建/导入；
- Private/Public 切换；
- Share Dialog；
- IAM 用户/Group Picker；
- Viewer/User/Editor 角色；
- 删除和 Terminating 状态；
- Finalizer/同步错误展示。

### 10.5 权限 UX

后端返回 `permissions`，前端：

- `can_manage=false`：隐藏 Credential、删除和权限管理；
- `can_edit=false`：表单只读；
- `can_use=false`：禁止作为 Sandbox/Pod 目标；
- `can_view=true`：允许详情查看；
- 403 必须展示 Kernel `decision_id` 和 `required_permission`，便于排查。

### 10.6 Secret UX

- kubeconfig 只在创建/轮换表单出现；
- 不提供“查看原文”；
- 提交后立即清空浏览器状态；
- 禁止写入 localStorage；
- 前端日志和错误上报必须过滤 credential 字段。

---

## 11. 后续 Pod、CRD 与 Sandbox 扩展

完成 Cluster/Namespace 基础能力后，再新增 Provider Adapter：

```text
KubernetesProvider
├── NativePodProvider
├── JobProvider
├── GenericCRDProvider
├── AgentSandboxProvider
└── OpenSandboxProvider
```

业务调用只使用稳定环境模型：

```text
Environment
EnvironmentTemplate
EnvironmentInstance
```

第三方 CRD 类型不得泄漏到 Hub 通用业务 API。

典型流程：

```text
Namespace permission use
  -> create Sandbox/Pod
  -> kubernetesx Apply
  -> status sync
  -> TTL task cleanup
```

Namespace 的 `use` 权限将成为后续创建 Pod/Sandbox 的基础授权边界。

---

## 12. 安全设计

### 12.1 威胁边界

Cluster Credential 基本等同集群管理权限，必须视为最高敏感 Secret。

必须实现：

- 加密存储；
- mTLS/可信内部链路；
- 日志脱敏；
- Audit；
- 轮换；
- 最小权限 ServiceAccount；
- 超时和请求体大小限制；
- 禁止把 kubeconfig 返回前端；
- 禁止将 Docker Socket/Kubernetes REST Config 暴露给普通业务代码。

### 12.2 Kubernetes 权限

推荐为 AISphere 创建专用 ServiceAccount，而不是使用 cluster-admin。

V1 最小能力：

```text
get/list namespaces
create/update/patch/delete namespaces
get API discovery
create SelfSubjectAccessReviews
```

> 第一阶段无 Watch/Informer/Cache（见 §4.2），最小权限不要求 `watch namespaces`。后续若引入长期 Watch 再按需追加。

后续 Pod/Sandbox 能力使用独立 ClusterRole，并按功能增量授权。

### 12.3 访问入口

用户不能直接获取目标集群 kubeconfig。所有操作经过：

```text
Envoy Gateway
  -> Casdoor AuthN
  -> Hub accessx / SpiceDB
  -> kubernetesx
  -> Kubernetes API Server
```

Kubernetes RBAC 是底层 ServiceAccount 边界；SpiceDB 是 AISphere 用户级资源授权边界。

### 12.4 Cluster 接入安全边界（SSRF / 出网防护）

拥有 `create_cluster` 的用户可提交任意 `server_url` 与 kubeconfig，因此 Hub/Kernel 必须把 Cluster 接入视为不可信输入，按以下边界校验：

**传输与 TLS：**
- 强制 `https`，拒 `http`（kernel `Config.Validate`/`Credential.Validate` 拒绝非 https scheme）；
- 生产环境禁 `InsecureSkipVerify`（仅本地 dev 配置 gated 开放，kernel `Config.Validate` 按 `AllowInsecureDev` 标志拒绝）；
- TLS ServerName 仅用于"Hub 用 IP 连集群、但证书签的是内网域名"场景（见 kernel `Config.ServerName`），保留 CA 校验，不等于跳过证书校验；
- 证书必须由配置 CA 或系统根校验通过。

**kubeconfig 禁止字段（kernel `Credential.Validate` 已部分实现，补全）：**
- exec plugin（已禁）；
- 外部证书/私钥/CA 文件引用（已禁）；
- 外部 token 文件（已禁）；
- impersonation `Impersonate`/`ImpersonateUID`/`ImpersonateGroups`/`ImpersonateUserExtra`（已禁 Impersonate，补全其余字段）；
- `cluster.ProxyURL`（**新增**，禁止 kubeconfig 代理，防止流量被劫持到任意地址）；
- `file://` URI（已禁）。

**`server_url` 网络地址校验（Hub 接入层解析后校验，kernel `Config.Validate` 配合）：**
- 解析 `server_url` 主机名 → IP（DNS 解析）；
- 拒环回：`127.0.0.0/8`、`::1/128`；
- 拒链路本地：`169.254.0.0/16`（含云元数据 `169.254.169.254`）、IPv6 `fe80::/10`；
- 拒私网：`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、IPv6 ULA `fc00::/7`——除非 Hub 配置显式允许内网集群（`kubernetes.allow_private_cluster_cidrs`）；
- 拒 Hub 自身管理网段（配置 `kubernetes.forbidden_cidrs`）；
- **DNS 重绑定防护**：解析得到 IP 后，**不改写 `rest.Config.Host`**（改写会改变 HTTP Host 头与语义），而是通过自定义 `net.Dialer.DialContext` 把连接固定到已验证的 IP，保留原 URL 的 Host 与 TLS SNI。实现要点：
  - 在 `rest.Config.Dial` 注入自定义 `DialContext`，先解析 `server_url` 主机名得到候选 IP 列表，按上述规则过滤后选定一个 IP，后续该 Client 的所有连接都用此 IP（避免二次 DNS 解析返回不同地址）；
  - HTTP Host 头、TLS SNI、证书校验仍按原 `server_url` 主机名进行（配合 `Config.ServerName` 仅用于"Hub 用 IP 连集群、但证书签的是内网域名"场景）；
  - 处理环境代理（`HTTPS_PROXY`）：Hub 进程出网代理配置不得应用于 Cluster 接入（避免流量被劫持到代理可达的任意地址），`DialContext` 内显式绕过环境代理。

**网络策略分层（Biz 只依赖接口，实现放 Data 层）：**

```go
// internal/biz/cluster.go 依赖的窄接口
type EndpointPolicy interface {
    // Validate 在 CreateCluster/UpdateCluster 前校验 server_url，
    // 返回通过校验的 resolved IP（供 ClientPool 构造 DialContext 用）。
    Validate(ctx context.Context, serverURL string) (resolvedIPs []string, err error)
}

// internal/data/kubernetes_endpoint_policy.go 实现 EndpointPolicy
// 持有 forbidden_cidrs / allow_private_cluster_cidrs / allowed_cluster_egress 配置，
// 返回 resolvedIPs 供 ClientPool 在构造 kubernetesx.Client 时注入 DialContext。
```

理由：allowlist/forbidden_cidrs 是 Hub 部署配置，不属于 kernel 通用 SDK；Biz 层只声明"接入前需校验端点"的契约，具体网络规则与 DNS 解析放 Data 层，便于单测与配置驱动。

**Hub 出网 allowlist：**
- Hub 配置 `kubernetes.allowed_cluster_egress`（CIDR/域名集合），`server_url` 不在 allowlist 内则拒绝；
- allowlist 为空时按上述地址校验规则兜底。

**分层归属：**
- scheme/TLS/ProxyURL/impersonation/exec/外部文件校验 → kernel `Credential.Validate`/`Config.Validate`（后续 kernel 补丁 PR，本次设计文档定契约）；
- 网络地址/allowlist/DNS 重绑定/DialContext 固定 IP 校验 → Hub Data 层 `EndpointPolicy` 实现（见上），Biz 只依赖 `EndpointPolicy` 接口，因为 allowlist/forbidden_cidrs 是 Hub 部署配置，不属于 kernel 通用 SDK。

---

## 13. 可观测性

### 13.1 Metrics

```text
aisphere_kubernetes_request_total
  labels: cluster_id, operation, resource, result

aisphere_kubernetes_request_duration_seconds
  labels: cluster_id, operation, resource

aisphere_kubernetes_client_pool_size

aisphere_kubernetes_cluster_health

aisphere_kubernetes_namespace_sync_total

aisphere_kubernetes_authz_repair_total
```

严禁将 server_url、Token、Namespace 用户输入直接作为高基数 label。

### 13.2 Trace

Span：

```text
hub.cluster.create
hub.cluster.probe
hub.namespace.create
hub.namespace.visibility.update
hub.namespace.share.create
kubernetes.api.apply
kubernetes.api.delete
spicedb.check
spicedb.write_relationships
```

### 13.3 Audit

高风险操作：

- Cluster 创建/删除；
- Credential 轮换；
- Namespace 删除；
- Public/Private 切换；
- Share 创建/撤销；
- 导入已有 Namespace。

Audit 不记录 Secret 原文。

---

## 14. 测试矩阵

### 14.1 Kernel

- kubeconfig 解析；
- 禁止 exec plugin；
- Client 创建；
- typed/unstructured CRUD；
- SSA；
- Field conflict；
- Probe；
- 错误归一化；
- Fake Client/envtest/Kind。

### 14.2 Hub Backend

- Cluster CRUD；
- Credential 不可回读；
- Credential 轮换和 Client invalidate；
- Namespace CREATE_NEW/IMPORT_EXISTING；
- Reserved labels；
- Public/Private；
- User/Group share；
- List 未授权过滤；
- 删除和 Terminating；
- SpiceDB/DB/Kubernetes 漂移 repair；
- 多副本并发和 revision 冲突。

### 14.3 权限矩阵

| 主体 | Private View | Public View | Use | Edit | Manage |
|---|---:|---:|---:|---:|---:|
| Owner | Yes | Yes | Yes | Yes | Yes |
| Editor | Yes | Yes | Yes | Yes | No |
| User | Yes | Yes | Yes | No | No |
| Viewer | Yes | Yes | No | No | No |
| 未分享用户 | No | Yes | No | No | No |
| Group Member | 按关系 | Yes | 按关系 | 按关系 | No |
| Cluster Admin | Yes | Yes | Yes | Yes | Yes |

### 14.4 Frontend

- Contract lock；
- Orval 生成无 diff；
- Adapter 单测；
- 权限按钮；
- 403/401；
- Secret 表单；
- Public/Private；
- Share Dialog；
- Terminating/Error 页面；
- Production build。

### 14.5 E2E 验收

1. Zone Admin 接入 Kind/Test Cluster；
2. 普通无权限用户看不到 Cluster；
3. Cluster Owner 创建私有 Namespace；
4. 其他用户无法查看；
5. 分享 Viewer 后可查看但不能使用；
6. 分享 User 后可作为 Sandbox 目标；
7. 切换 Public 后所有登录用户可查看；
8. 切回 Private 后等待 `visibility_sync_status=SYNCED`，wildcard 访问必须失效（`REVOKING` 期间 effective 仍 PUBLIC，前端用 `effective_visibility` 展示"正在收窄"）；
9. 删除 Namespace 展示 Terminating，完成后不可访问；
10. Credential 轮换后旧 Client 被清理，Probe 成功。

---

## 15. 分阶段实施计划

### Phase 0：设计与契约冻结

- 评审本文档；
- 冻结资源命名、API Path、SpiceDB relations；
- 明确 CredentialStore V1；
- 输出任务拆分。

### Phase 1：Kernel `kubernetesx`

- 引入 controller-runtime/client-go；
- Config/Client/Factory/Scheme；
- SSA、Discovery、Probe；
- Errors/Metrics；
- Fake/envtest；
- 文档和 Package Index；
- 发布 Kernel 新版本。

> Kernel `kubernetesx` 候选实现已提交（含真实集群集成测试通过），但未作为正式 PR 合并。Kernel 已实现部分的契约对齐（§4.6 Probe SSAR 为主、§12.4 SSRF 边界中 kernel `Credential.Validate`/`Config.Validate` 待补全项）由 kernel 补丁 PR 落地，不在本设计文档 PR 内修改 kernel 代码。

> **Kernel 候选实现待修复问题（由 kernel 补丁 PR 修复）**：
> 1. `kubernetesx/config.go` ServiceAccount 合并只保留 `Host`，`Token`/`CA` 没进入任何 `Config`；`factory.go` 最终构造的是无凭据 Client。需在 SA 凭据合并时把 `Token` 写入 `rest.Config.BearerToken`、`CA` 写入 `rest.Config.CAData`/`TLSClientConfig`，否则 SA 接入的集群全部认证失败。
> 2. `kubernetesx/probe.go` 给 cluster-scoped `namespaces` 资源的 update SSAR 传了 `namespace` 字段。`namespaces` 是 cluster-scoped，SSAR 的 `ResourceAttributes.Namespace` 必须为空，否则 kube-apiserver 会按 namespace-scoped 语义判权限，结果不可信。需对 cluster-scoped 资源强制 `Namespace=""`。
> 这两项是 kernel 候选代码的实现 bug，不影响本设计文档契约（契约按修复后语义定），但阻塞 Phase 1 正式 PR 合并。

### Phase 2：IAM Schema

- `k8s_cluster`；
- `k8s_namespace`；
- `custom_role/role_binding/zone` 能力；
- `configs/resource/defaults.yaml`；
- permission-manifest-check；
- Schema/relationship 测试；
- 受控发布到测试 SpiceDB。

### Phase 3：Hub Cluster Backend

- DB migration；
- CredentialStore；
- Client Pool；
- Cluster Proto；
- Cluster CRUD/Probe/Rotate；
- generated code；
- AuthZ/Audit；
- integration tests。

### Phase 4：Hub Namespace Backend

- Namespace Proto；
- Namespace DB/Repo/Usecase；
- CREATE_NEW/IMPORT_EXISTING；
- Public/Private；
- Share CRUD；
- List batch authz；
- Sync/Delete jobs；
- drift repair。

### Phase 5：Hub Frontend

- 同步 OpenAPI Contract；
- Orval 生成 SDK；
- Cluster/Namespace adapters；
- 页面、Dialog、权限 UX；
- 测试和构建。

### Phase 6：Pod/CRD/Sandbox

- Namespace `use` 权限；
- Native Pod；
- Generic CRD；
- Agent Sandbox/OpenSandbox Adapter；
- Exec/Logs/File/TTL。

---

## 16. PR 拆分建议

### PR 1 — Kernel

```text
feat(kubernetesx): add controller-runtime based Kubernetes SDK
```

### PR 2 — IAM

```text
feat(authz): add Kubernetes cluster and namespace permission model
```

### PR 3 — Hub Backend Cluster

```text
feat(kubernetes): add cluster registry and credential lifecycle
```

### PR 4 — Hub Backend Namespace

```text
feat(kubernetes): add namespace lifecycle and SpiceDB sharing
```

### PR 5 — Hub Frontend Contract

```text
feat(api): generate Kubernetes management SDK from Hub OpenAPI
```

### PR 6 — Hub Frontend UI

```text
feat(environment): add cluster and namespace management UI
```

每个 PR 独立通过 CI，避免一次大改跨四个仓库无法定位问题。

---

## 17. 完成定义

本能力完成必须同时满足：

- Kernel 有稳定 `kubernetesx` SDK；
- Hub 不直接散落使用 client-go；
- Cluster/Namespace API 全部由 Proto 生成；
- Frontend API 全部由 OpenAPI + Orval 生成；
- Namespace 使用稳定 Hub UUID 作为 SpiceDB ID；
- Private/Public 和 User/Group Share 可用；
- List 不泄漏未授权资源；
- kubeconfig 不可回读、不进日志；
- SSA 字段所有权清晰；
- 多集群 Client 可安全轮换和淘汰；
- IAM Schema、权限清单和测试一致；
- E2E 权限矩阵全部通过；
- 后续 Pod/CRD/Sandbox 可直接复用 Namespace 授权边界。

---

## 18. 最终决策摘要

1. Kernel 新增 `kubernetesx`，基础实现为 `controller-runtime/client`；
2. 第一阶段不用多集群 Manager/Informer，采用直接 Client + `taskx`；
3. Hub 拥有 Cluster/Namespace 控制面数据；
4. IAM/SpiceDB 拥有访问关系和权限判定；
5. Namespace 使用稳定 UUID，不使用 `cluster/name` 作为权限对象 ID；
6. `PUBLIC` 仅授予所有已认证用户 `view`，不授予 `use/edit/manage`；
7. 分享支持 `viewer/user/editor`，禁止分享 owner；
8. 用户不获取 kubeconfig，所有 Kubernetes 操作通过 Hub；
9. 后端 Proto 生成 OpenAPI，前端 Orval 生成 SDK；
10. Cluster/Namespace 完成后，再实现 Pod、CRD 和 Sandbox。
