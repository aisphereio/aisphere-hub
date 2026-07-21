# AISphere Kubernetes 环境管理能力开发设计

> 状态：Design Draft  
> 目标版本：Kernel `v0.5.x`、Hub/Hub Front 下一能力版本  
> 涉及仓库：`aisphereio/kernel`、`aisphereio/aisphere-iam`、`aisphereio/aisphere-hub`、`aisphereio/aisphere-hub-frontend`  
> 范围：Kubernetes 集群接入、Namespace 生命周期、Namespace 可见性与分享、前端契约生成  
> 非范围：完整 Kubernetes 运维平台、任意资源浏览器、节点运维、监控平台、直接向终端用户签发 kubeconfig

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
- 能读取 Namespace；
- 能创建、更新、删除 Namespace，或明确标记为只读集群；
- 禁止依赖本地 `exec` credential plugin。

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
}
```

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

`ListClusters` 必须先按 `org_id` 查询候选行，再对具体 Cluster 批量执行 `view` 权限检查，禁止返回未授权集群。

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

Hub 定义接口：

```go
type ClusterCredentialStore interface {
    Put(ctx context.Context, clusterID string, value Credential) (ref string, err error)
    Get(ctx context.Context, ref string) (Credential, error)
    Delete(ctx context.Context, ref string) error
}
```

V1 Provider：

- PostgreSQL encrypted envelope；
- AES-256-GCM；
- 主密钥由环境变量或挂载 Secret 提供；
- 每条凭据独立 nonce；
- 数据库只保存 ciphertext、nonce、key version。

后续可增加 Vault/KMS Provider。Biz 层只依赖接口。

### 5.6 Client Pool

```text
internal/data/kubernetes_client_pool.go
```

能力：

- 按 Cluster ID 懒加载；
- LRU/TTL；
- 并发 singleflight；
- credential revision 感知；
- 轮换、更新、删除时主动 invalidate；
- plaintext credential 不进入日志；
- 每个 Client 独立 Transport 和连接池；
- 全局最大活跃 Client 数可配置。

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
}
```

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

#### 删除

- AISphere 创建的 `managed=true` Namespace：默认删除远端 Namespace；
- 导入的 `managed=false` Namespace：默认仅解除 AISphere 管理；
- 删除导入 Namespace 的远端资源必须传显式确认参数；
- Kubernetes Namespace 删除是异步过程，状态进入 `TERMINATING`；
- 只有远端对象消失后才完成本地清理；
- Finalizer 阻塞必须在 UI 展示，不允许强制绕过 Finalizer 作为普通操作。

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

  permission manage = owner + admin + zone->manage_permissions + custom_binding->manage
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

同时在 `custom_role`、`role_binding`、`zone` 中增加：

```text
create_cluster
manage_clusters
```

建议权限：

```text
zone.create_cluster = owner + admin + platform->manage_control_plane + custom_binding->create_cluster
zone.manage_clusters = owner + admin + platform->manage_control_plane + custom_binding->manage_clusters
```

Cluster `manage` 可以继承 `zone->manage_clusters`，而不是复用通用 `manage_permissions`。最终 Schema 应使用明确的资源能力，避免权限过宽。

### 7.2 默认关系

创建 Cluster：

```text
k8s_cluster:{cluster_id}#zone@zone:{org_id}
k8s_cluster:{cluster_id}#owner@user:{principal.subject_id}
```

创建 Namespace：

```text
k8s_namespace:{namespace_id}#cluster@k8s_cluster:{cluster_id}
k8s_namespace:{namespace_id}#owner@user:{principal.subject_id}
```

### 7.3 可见性

V1 定义：

- `PRIVATE`：仅 owner/editor/user/viewer 等显式关系可访问；
- `PUBLIC`：所有已认证用户可查看；
- 公开不自动授予 edit、use 或 manage。

公开关系：

```text
k8s_namespace:{id}#viewer@user:*
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

### 7.5 可见性更新顺序

为保证失败关闭：

#### PRIVATE -> PUBLIC

```text
1. 写 wildcard viewer 关系
2. 更新数据库 visibility=PUBLIC
```

#### PUBLIC -> PRIVATE

```text
1. 删除 wildcard viewer 关系
2. 更新数据库 visibility=PRIVATE
```

数据库用于产品展示，SpiceDB 是权限判定源。后台 reconcile 检测并修复 visibility drift。

### 7.6 List 权限

禁止只依赖数据库 visibility 过滤。

```text
查询候选资源
  -> SpiceDB LookupResources 或 BatchCheck
  -> 仅返回 view=true 的资源
```

每条响应增加：

```proto
message NamespacePermissions {
  bool can_view = 1;
  bool can_use = 2;
  bool can_edit = 3;
  bool can_manage = 4;
  bool can_share = 5;
  bool can_delete = 6;
}
```

前端不得自行推导权限。

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
created_by VARCHAR NOT NULL
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
deleted_at TIMESTAMPTZ
revision BIGINT NOT NULL
```

约束：

```text
UNIQUE(org_id, name) WHERE deleted_at IS NULL
UNIQUE(cluster_uid) WHERE cluster_uid IS NOT NULL AND deleted_at IS NULL
```

### 8.2 `k8s_cluster_credentials`

```text
ref UUID PK
cluster_id UUID NOT NULL
ciphertext BYTEA NOT NULL
nonce BYTEA NOT NULL
key_version VARCHAR NOT NULL
credential_type VARCHAR NOT NULL
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
```

### 8.3 `k8s_namespaces`

```text
id UUID PK
cluster_id UUID FK
kube_name VARCHAR NOT NULL
display_name VARCHAR
description TEXT
visibility VARCHAR NOT NULL
lifecycle VARCHAR NOT NULL
managed BOOLEAN NOT NULL
kubernetes_uid VARCHAR
resource_version VARCHAR
labels_json JSONB
annotations_json JSONB
owner_id VARCHAR NOT NULL
last_sync_at TIMESTAMPTZ
last_error_code VARCHAR
last_error_message TEXT
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
deleted_at TIMESTAMPTZ
revision BIGINT NOT NULL
```

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
subject_relation VARCHAR
sync_status VARCHAR NOT NULL
created_by VARCHAR NOT NULL
created_at TIMESTAMPTZ
updated_at TIMESTAMPTZ
```

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
get/list/watch namespaces
create/update/patch/delete namespaces
get API discovery
create SelfSubjectAccessReview
```

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
8. 切回 Private 后 wildcard 访问立即失效；
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
