# Agent Sandbox 部署（194 测试环境）

> **项目**：[kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) v0.5.2
> **目标服务器**：aisphere-dev（36.137.200.194）
> **文档日期**：2026-07-22
> **状态**：已部署并验证（runc 起步，gVisor 待升级）

一句话结论：agent-sandbox 是 K8s 原生的「沙箱工厂」Operator（CRD + 控制器），在 194 上已用一条 `kubectl apply` 装好；唯一卡点是控制器镜像在 `registry.k8s.io`（Google 源被墙），用国内镜像源拉取 + retag 解决。默认沙箱镜像没有全局开关，靠 **SandboxTemplate + WarmPool + SandboxClaim** 三件套实现「指定镜像、秒级领取」。

---

## 1. 它是什么

agent-sandbox 提供一套声明式 API 管理「单个、有状态、有稳定身份、可持久化、可休眠/恢复」的工作负载，专为 AI Agent 运行时、开发云环境、Notebook、执行不可信代码等场景设计。核心定位**不是**强隔离 VM，而是有稳定身份 + 持久存储 + 生命周期管理的单例 Pod 抽象；强隔离是**可选**的运行时特性。

CRD 全景：

```text
Core（核心）
└── Sandbox              ← 一个有稳定身份 + 持久存储的单例 Pod

Extensions（扩展，opt-in，本次已装）
├── SandboxTemplate      ← 可复用模板（镜像只写在这里一次）
├── SandboxClaim         ← 用户申领沙箱的声明式 API（不写镜像，从 WarmPool 领）
└── SandboxWarmPool      ← 预热池（秒级分配，省冷启动）
```

工作流：

```text
用户 apply Sandbox / SandboxClaim
  -> 控制器 Watch -> 调谐创建 Pod（可选 runtimeClassName: gvisor / kata）
  -> 注入稳定 hostname + 持久存储 -> 暴露稳定网络身份
  -> Agent 经 sandbox-router（HTTP 代理）或 SDK 连入执行
```

API 版本注意（与通用说法有差异，已核对仓库）：

| 项 | 通用说法 | 仓库实际 |
|---|---|---|
| API group | `sandbox.agent.k8s.io` | **`agents.x-k8s.io`**（扩展为 `extensions.agents.x-k8s.io`） |
| API version | `v1alpha1` | v1alpha1 **和 v1beta1**（README 示例用 v1beta1） |
| 安装方式 | `make deploy` / `config/default/` | 官方主推 **release YAML**（`k8s/` 目录），`make deploy` 是开发用 |

---

## 2. 194 环境评估（部署前现状）

> 以下为部署前通过 SSH 对 `root@36.137.200.194` 的只读核查结果。

| 项 | 现状 | 影响 |
|---|---|---|
| K8s 版本 | v1.31.11（单节点 control-plane `aisphere-dev`，sealos 建群） | ✅ 满足（要求 ≥1.28） |
| CPU / 内存 | 16 核 / 64GB | ✅ 充足 |
| 架构 / 内核 | x86_64 / Ubuntu 24.04.2 / 6.8.0-57-generic | ✅ 满足 |
| 容器运行时 | **Docker 28.3.3**（CRI，经 cri-dockerd），默认 runc | ✅ 核心安装零障碍；gVisor 走 Docker daemon.json 接入 |
| 存储 | 默认 SC `nfs`（nfs-subdir-external-provisioner，Delete） | ✅ PVC 可绑定 |
| RuntimeClass | 无 | 全新创建 |
| sandbox CRD / ns | 无 | 全新安装，无冲突 |
| cert-manager | 已装 | 可兜底 webhook 证书（实测本次安装未依赖它） |

### 强隔离运行时可行性（决策核心）

| 运行时 | 194 可行性 | 原因 |
|---|---|---|
| **Kata Containers**（VM 隔离） | ❌ 不可行 | 无 `/dev/kvm`，无 kvm 内核模块，CPU `vmx/svm` 标志数 = 0。云 VM 无嵌套虚拟化 |
| **gVisor**（用户态内核隔离） | ✅ 可行（需安装） | 不依赖 KVM，纯用户态。需装 runsc + 改 Docker daemon.json + 建 RuntimeClass |
| **runc**（默认，无隔离） | ✅ 立即可用 | 现状即是，先跑通流程 |

核查命令留痕：

```bash
ls /dev/kvm                         # -> 不存在
lsmod | grep -iE 'kvm|vhost'        # -> 无模块
grep -cE 'vmx|svm' /proc/cpuinfo    # -> 0
which kata-runtime runsc            # -> 均未安装
kubectl get runtimeclass            # -> No resources found
```

---

## 3. 部署步骤（实际执行记录）

### 3.1 安装控制器 + CRD

官方推荐用 `sandbox-with-extensions.yaml`（单一无冲突资产，控制器只声明一次且 extensions 已启用）。

194 能直连 GitHub release，在服务器上直接下载：

```bash
ssh root@36.137.200.194
cd /root
curl -fL -o sandbox-with-extensions.yaml \
  https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.2/sandbox-with-extensions.yaml

kubectl apply -f sandbox-with-extensions.yaml
```

预期输出（全部 created）：

```text
namespace/agent-sandbox-system created
customresourcedefinition.apiextensions.k8s.io/sandboxclaims.extensions.agents.x-k8s.io created
customresourcedefinition.apiextensions.k8s.io/sandboxes.agents.x-k8s.io created
customresourcedefinition.apiextensions.k8s.io/sandboxtemplates.extensions.agents.x-k8s.io created
customresourcedefinition.apiextensions.k8s.io/sandboxwarmpools.extensions.agents.x-k8s.io created
serviceaccount/agent-sandbox-controller created
role/clusterrole/rolebinding/clusterrolebinding ... created
service/agent-sandbox-controller created
service/agent-sandbox-webhook-service created
deployment.apps/agent-sandbox-controller created
```

manifest 只引用**一个镜像**：`registry.k8s.io/agent-sandbox/agent-sandbox-controller:v0.5.2`，`imagePullPolicy: IfNotPresent`。

### 3.2 镜像拉取问题与解决

控制器 Pod 进入 `ErrImagePull`：

```text
Failed to pull image "registry.k8s.io/agent-sandbox/agent-sandbox-controller:v0.5.2":
  Error response from daemon: Head "https://us-west2-docker.pkg.dev/...":
  dial tcp 64.233.188.82:443: i/o timeout
```

`registry.k8s.io` 重定向到 Google Artifact Registry（被墙）。194 的 Docker Hub 镜像加速器对它无效（mirror 只代理 Docker Hub，不代理 `registry.k8s.io`）。

**解法**：用国内镜像源拉取 + retag 成 deployment 期望的镜像名。控制器是 `IfNotPresent`，retag 后直接用本地镜像：

```bash
# 拉取（daocloud 镜像 registry.k8s.io 的代理）
docker pull m.daocloud.io/registry.k8s.io/agent-sandbox/agent-sandbox-controller:v0.5.2

# retag 成 deployment 期望的镜像名
docker tag m.daocloud.io/registry.k8s.io/agent-sandbox/agent-sandbox-controller:v0.5.2 \
           registry.k8s.io/agent-sandbox/agent-sandbox-controller:v0.5.2

# 删掉失败的 Pod，让它重建用本地镜像
kubectl delete pod -n agent-sandbox-system \
  $(kubectl get pods -n agent-sandbox-system -o jsonpath="{.items[0].metadata.name}")
```

### 3.3 修复 Docker Hub 镜像源（全局，影响所有 Docker Hub 拉取）

部署 Sandbox 时发现 Docker Hub 镜像（如 busybox）拉取极慢（86s 拉不完 2MB），根因是 `daemon.json` 第一个 mirror 已挂：

```bash
# 原配置（问题：dockerpull.com 已挂，超时 8s）
"registry-mirrors": [
  "https://dockerpull.com",      # <- 已挂，是卡顿元凶
  "https://docker.1ms.run",
  "https://docker.xuanyuan.me"
]
```

测速后换成国内可用源（daocloud 最快 0.1s）：

```bash
# 备份
cp /etc/docker/daemon.json /etc/docker/daemon.json.bak.$(date +%Y%m%d_%H%M%S)

# 改 registry-mirrors（其余配置不动）
cat > /etc/docker/daemon.json <<'EOF'
{
  ...（保留原 max-concurrent-downloads/log/exec-opts/insecure-registries/data-root）...
  "registry-mirrors": [
    "https://docker.m.daocloud.io",
    "https://hub.rat.dev",
    "https://docker.1ms.run"
  ]
}
EOF

systemctl restart docker
docker info | grep -A5 "Registry Mirrors"   # 验证生效
```

**效果**：busybox 拉取 86s → **419ms**。

⚠️ `systemctl restart docker` 会杀掉所有容器。194 上 116 个容器 / 59 个 Pod，apiserver 10s 恢复，etcd/minio/gitlab 等核心服务全部 Running。aisphere 业务服务（casdoor/spicedb/iam）重启后短暂 CrashLoopBackOff（重建顺序依赖），逐步自愈。改 mirror **必须重启 docker 才生效**（无热加载）。

### 3.4 验收安装

```bash
# 控制器 Pod
kubectl get pods -n agent-sandbox-system
#   agent-sandbox-controller-xxx   1/1   Running

kubectl wait --for=condition=Ready pod -l app=agent-sandbox-controller \
  -n agent-sandbox-system --timeout=120s

# 4 个 CRD
kubectl get crd | grep agents.x-k8s
#   sandboxclaims.extensions.agents.x-k8s.io
#   sandboxes.agents.x-k8s.io
#   sandboxtemplates.extensions.agents.x-k8s.io
#   sandboxwarmpools.extensions.agents.x-k8s.io

# 控制器日志确认 4 个 controller 都 Starting workers，无报错
kubectl logs -n agent-sandbox-system deploy/agent-sandbox-controller --tail=15
```

---

## 4. 创建沙箱（两种方式）

### 4.1 方式一：直接用 Sandbox（核心，需指定镜像）

```yaml
apiVersion: agents.x-k8s.io/v1beta1
kind: Sandbox
metadata:
  name: hello-world
  namespace: agent-sandbox-demo
spec:
  podTemplate:
    spec:
      containers:
      - name: my-container
        image: busybox:latest
        command: ["sh","-c","sleep 3600"]
      restartPolicy: Never
```

```bash
kubectl create namespace agent-sandbox-demo
kubectl apply -f hello-world.yaml
kubectl get sandbox -n agent-sandbox-demo        # 等 Ready: True
kubectl exec -n agent-sandbox-demo hello-world -- hostname   # -> hello-world（稳定身份）
```

镜像每次都要在 `podTemplate` 里写。详见 `manifests/hello-world.yaml`。

### 4.2 方式二：SandboxTemplate + WarmPool + SandboxClaim（推荐，指定默认镜像）

agent-sandbox **没有全局「默认镜像」开关**（已查证：控制器无 default-image flag、无 env、无 ConfigMap、无 defaulting webhook）。镜像 100% 由 `podTemplate.spec.containers[].image` 决定。「默认镜像」靠 SandboxTemplate 实现——镜像只在 Template 里写一次，用户通过 Claim 领取时无需写镜像。

```text
镜像定义一次 ──> SandboxTemplate（镜像写在这里）
                    │ 引用
                    ▼
               SandboxWarmPool（预热 N 个 Pod，用模板镜像）
                    │ 引用
                    ▼
      用户创建 SandboxClaim（不写任何镜像！）
                    │
                    ▼
          立刻领到沙箱，镜像 = 模板指定的
```

清单见 `manifests/` 目录，已验证可用的完整流程：

```bash
# 1. SandboxTemplate：镜像用国内地址，只写这一次
kubectl apply -f manifests/sandboxtemplate.yaml

# 2. WarmPool：预热 2 个
kubectl apply -f manifests/warmpool.yaml
kubectl get sandboxwarmpool -n agent-sandbox-demo   # READY=2 DESIRED=2

# 3. SandboxClaim：不写 image，只写 warmPoolRef，5s 领到
kubectl apply -f manifests/sandboxclaim.yaml
kubectl get sandboxclaim -n agent-sandbox-demo      # READY=True

# 验证领到的沙箱用的镜像 = 模板的国内地址（不是 Docker Hub 默认）
kubectl get sandbox -n agent-sandbox-demo -o jsonpath='{range .items[*]}{.metadata.name}{"  "}{.spec.podTemplate.spec.containers[0].image}{"\n"}{end}'
```

对比两种方式：

| | 直接 Sandbox | Template + Claim |
|---|---|---|
| 镜像写哪 | 每次在 podTemplate 里写 | 只在 Template 里写一次 |
| 用户要不要指定镜像 | 要 | **不要**，Claim 里只有 warmPoolRef |
| 分配速度 | 10-30s（拉镜像+启动） | **<5s**（预热池现成） |
| 适合 | 一次性/定制沙箱 | 批量、标准化、要「默认镜像」的场景 |

---

## 5. 升级到 gVisor 强隔离（待办）

Kata 在 194 不可行（无 KVM），强隔离走 gVisor。194 是 Docker CRI，官方 gVisor 快速入门基于 containerd，需按 Docker 方式接入。

### 5.1 安装 runsc（节点上）

```bash
# 参考 https://gvisor.dev/docs/user_guide/install/
# 下载 runsc + containerd-shim-runsc-v1 到 /usr/local/bin/
```

### 5.2 配置 Docker 使用 runsc

编辑 `/etc/docker/daemon.json`，追加 runsc 运行时：

```json
{
  "runtimes": {
    "runsc": { "path": "/usr/local/bin/runsc" }
  }
}
```

```bash
systemctl restart docker
docker info | grep -A2 Runtimes      # 期望看到 runsc
```

### 5.3 创建 RuntimeClass

```bash
kubectl apply -f manifests/runtimeclass-gvisor.yaml
kubectl get runtimeclass            # 期望 gvisor
```

### 5.4 给 Sandbox 启用隔离

在 Sandbox / SandboxTemplate 的 `podTemplate.spec` 下加 `runtimeClassName: gvisor`。验证：沙箱内 `uname -r` 应显示 gVisor 内核（非宿主 6.8.0-57），`dmesg` 受限。

---

## 6. 风险与注意事项

| 风险 | 说明 | 建议 |
|---|---|---|
| 单节点资源争用 | 控制器 + 沙箱 + 现有大量服务（gitlab/etcd/monitoring…）同节点 | 装前 `kubectl describe node aisphere-dev` 看剩余 allocatable；用 `--sandbox-concurrent-workers` 限流 |
| NFS 存储局限 | 默认 SC 是 NFS，不适合 Kata devmapper / 高 IO 块存储 | runc/gVisor 场景可接受；后续若有块存储需求需补 SC |
| Docker CRI | 官方示例多假设 containerd；gVisor 接入走 `daemon.json` 而非 `config.toml` | gVisor 配置见 §5.2 |
| 无 KVM | Kata 永久不可行 | 强隔离只能 gVisor；要 VM 级隔离需换有嵌套虚拟化的机器 |
| registry.k8s.io 被墙 | 控制器镜像每次更新都要手动 daocloud+retag | 或改 manifest 镜像地址为 daocloud 代理；或推到内网 sealos.hub:5000 |
| 重启 docker 影响 | 改 mirror 必须重启，会中断所有容器 | 低峰期操作；核心服务秒级自愈，有状态服务需确认恢复 |
| 生产隔离强度 | gVisor 仍是用户态，非 VM 级 | 结合 Cilium 网络策略 + 资源 limit + 最小权限 |
| 版本演进快 | v0.5.x 活跃开发中，API 仍含 v1alpha1 | 锁定版本，关注 `docs/api-migration-guide.md` |

---

## 7. 日常运维命令

```bash
# 看控制器
kubectl get pods -n agent-sandbox-system
kubectl logs -n agent-sandbox-system deploy/agent-sandbox-controller --tail=50

# 看沙箱
kubectl get sandbox -n agent-sandbox-demo
kubectl get sandboxclaim -n agent-sandbox-demo
kubectl get sandboxwarmpool -n agent-sandbox-demo

# 进沙箱
kubectl exec -n agent-sandbox-demo <sandbox-name> -- sh

# 清理单个沙箱
kubectl delete sandbox <name> -n agent-sandbox-demo
kubectl delete sandboxclaim <name> -n agent-sandbox-demo

# 卸载 agent-sandbox（保留 CRD 需手动删）
kubectl delete -f sandbox-with-extensions.yaml
```

---

## 8. 参考来源（均已核对）

- README.md（main 分支）：安装命令、CRD、API 版本、形态描述
- `docs/api.md`：`agents.x-k8s.io/v1alpha1`、`v1beta1`，`extensions.agents.x-k8s.io/*`
- `docs/configuration.md`：控制器并发参数（`--sandbox-concurrent-workers` 等）
- `examples/quickstart/{README.md, gvisor.md, kata-containers.md}`：隔离运行时设置
- `examples/hello-world-sandbox/hello-world.yaml`：最小 Sandbox 示例
- `clients/python/agentic-sandbox-client/python-sandbox-template.yaml`：SandboxTemplate 示例
- GitHub Releases API：最新稳定版 **v0.5.2**，资产 `sandbox.yaml` / `extensions.yaml` / `sandbox-with-extensions.yaml`
- 194 服务器 SSH 核查：集群 / 运行时 / KVM / 存储 / CRD / 命名空间现状
