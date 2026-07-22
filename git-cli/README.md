# aisphere-git-cli

Native Git authentication for AISphere Hub Skill repositories.

Lets a human user run plain `git clone` / `pull` / `push` / `git lfs` against
the Hub git endpoint, authenticated by their own Casdoor identity via an OAuth
Authorization Code + PKCE flow. No `http.extraHeader`, no PAT, no username/
password prompt.

It ships **two binaries** (Git resolves them by name):

| Binary | Invoked by | Role |
| --- | --- | --- |
| `git-aisphere` | `git-aisphere <subcommand>` | User-facing login / logout / status / install / diagnose |
| `git-credential-aisphere` | Git during clone/fetch/push/LFS | credential helper: returns a Bearer access token |

---

## Quick Start（5 步上手）

```powershell
# 1. 构建（需要 Go 1.26+）
cd E:\coding\aisphereio\aisphere-git-cli
make build

# 2. 把两个二进制加入 PATH
$env:PATH = "E:\coding\aisphereio\aisphere-git-cli\bin;" + $env:PATH

# 3. 安装 credential helper 到全局 .gitconfig（一次性）
git-aisphere install

# 4. 登录（会打印 URL，复制到浏览器打开，在 Casdoor 登录）
git-aisphere login

# 5. 用原生 Git 操作（无需任何额外参数）
git clone https://api.weagent.cc:30723/git/<skill-name>.git
```

> **Linux/macOS 用户**：把 `$env:PATH = "..."` 换成
> `export PATH="/path/to/aisphere-git-cli/bin:$PATH"`，其余步骤相同。

---

## 构建

### 前置条件

- **Go 1.26+**
- **Git >= 2.46**（credential helper authtype/Bearer 协议是 2.46 才引入的；
  用 `git --version` 确认，不满足需先升级 Git）
- 本仓库在 `go.work` workspace 内（依赖 kernel / aisphere-iam 本地 replace）

### 编译

```bash
make build         # → bin/git-aisphere, bin/git-credential-aisphere
make tidy          # go mod tidy
make test          # go test ./...
make clean         # 删除 bin/
```

编译产物在 `bin/` 目录：

```
bin/
├── git-aisphere.exe              # Windows
├── git-credential-aisphere.exe   # Windows
# 或 Linux/macOS:
├── git-aisphere
├── git-credential-aisphere
```

> 如果 `make build` 无输出且 `bin/` 为空，可能是旧版 Make（GnuWin32 3.81）
> 的 for-loop 在 Windows cmd.exe 下失效。直接手动编译：
> ```bash
> go build -buildvcs=false -trimpath -ldflags "-s -w" -o bin/git-aisphere.exe ./cmd/git-aisphere
> go build -buildvcs=false -trimpath -ldflags "-s -w" -o bin/git-credential-aisphere.exe ./cmd/git-credential-aisphere
> ```

---

## 安装

### 1. 把二进制放到 PATH

**方式 A：临时（当前终端会话）**

```powershell
# PowerShell
$env:PATH = "E:\coding\aisphereio\aisphere-git-cli\bin;" + $env:PATH
```
```bash
# Bash / Git Bash
export PATH="/e/coding/aisphereio/aisphere-git-cli/bin:$PATH"
```

**方式 B：永久（推荐）**

把 `bin/` 目录加入系统环境变量 PATH，或把两个二进制复制到已在 PATH
的目录（如 `C:\Users\<you>\bin\`、`/usr/local/bin/`）。

验证：
```bash
git-credential-aisphere capability
# 应输出:
# version 0
# capability authtype
```

### 2. 安装 credential helper

```bash
git-aisphere install
```

这会在全局 `.gitconfig` 写入：

```ini
[credential "https://api.weagent.cc:30723/git"]
    helper =          # 空行：清除继承的 helper（如 Windows GCM），防止抢先
    helper = aisphere
    useHttpPath = true
```

验证安装成功：
```bash
git config --get-urlmatch credential.helper "https://api.weagent.cc:30723/git/test.git"
# 应输出: aisphere
```

> **注意**：`helper =`（空行）是关键——它清除系统级继承的 credential helper
> （如 Windows Git Credential Manager），确保只有 aisphere helper 处理
> AISphere 的 git 请求。如果空行丢失，GCM 会弹窗要求账号密码，导致
> "Jwt is missing" 错误。修复方法见下方[排障](#排障)。

---

## 使用

### 登录

```bash
git-aisphere login
```

流程：
1. CLI 在 `127.0.0.1:52731` 启动临时回调服务器
2. 生成 PKCE verifier/challenge + state
3. 打印 authorize URL 到终端（并尝试自动打开浏览器）
4. **如果浏览器没自动打开**，手动复制终端里的 URL 到浏览器
5. 在 Casdoor 登录页输入账号密码
6. Casdoor 回调 CLI → CLI 用 code + PKCE verifier 换 token（不带 client_secret）
7. token 存入 `~/.aisphere/credentials.json`（0600）

成功输出：
```
Logged in as: 管理员 (496333c7-7acc-4717-8596-056544fc0a68)
Git endpoint: https://api.weagent.cc:30723/git
Token expires in: in 167h
Refresh credential stored securely.
```

> **重要**：CLI 必须在打开浏览器的**同一台机器**上运行。因为回调地址是
> `http://127.0.0.1:52731/callback`（loopback），浏览器访问的是浏览器所在
> 机器的回环接口。SSH 远程服务器上运行 CLI 时，浏览器在本机打不开远程的
> 127.0.0.1。在本地机器运行 CLI 即可。

### Git 操作

登录后，所有原生 Git 命令直接用，无需额外参数：

```bash
# 克隆
git clone https://api.weagent.cc:30723/git/<skill-name>.git

# 拉取
git pull

# 推送
git push origin main

# LFS
git lfs pull
git lfs push origin main

# 查看远程
git ls-remote https://api.weagent.cc:30723/git/<skill-name>.git
```

credential helper 会在每次 Git 请求时自动注入 `Authorization: Bearer <token>`。
token 过期时自动刷新（跨进程文件锁保证并发只刷新一次）。

### 设置提交者身份（每个仓库一次）

Git commit 的 author 身份和认证 token 无关，需要单独配置。
**在每个 clone 出来的仓库内**执行（不带 `--global`，不影响其他项目）：

```bash
cd <skill-repo>
git config user.email "your-email@aisphere.io"
git config user.name "your-name"
```

之后该仓库内 `git commit -m "..."` 自动用这个身份，无需 `-c` 参数。

### 查看状态

```bash
git-aisphere status
```
显示当前登录主体、token 过期时间、refresh 是否可用。

### 诊断

```bash
git-aisphere diagnose
```
检查：Git 版本（>=2.46）、helper 是否在 PATH、Casdoor issuer/JWKS 可达性、
本地 session、Gateway /git 是否返回 401 Bearer challenge、.gitconfig 是否
正确安装。

### 登出

```bash
git-aisphere logout
```
删除本地 session（`~/.aisphere/credentials.json`）。

---

## 配置

所有默认值已针对生产环境调好。需要切换环境时用环境变量覆盖：

| 环境变量 | 默认值 | 含义 |
| --- | --- | --- |
| `AISPHERE_GIT_ISSUER` | `https://casdoor.weagent.cc:30723` | Casdoor issuer / JWT `iss` |
| `AISPHERE_GIT_CLIENT_ID` | `ec15766f6cb98b908433` | Casdoor app client id（也= JWT `aud`） |
| `AISPHERE_GIT_ENDPOINT` | `https://api.weagent.cc:30723/git` | Hub git origin（helper host/path 守卫） |
| `AISPHERE_GIT_CALLBACK_PORT` | `52731` | loopback OAuth 回调端口 |
| `AISPHERE_GIT_STORE_DIR` | `~/.aisphere` | credentials.json 目录（0700） |

---

## 排障

### `remote: Jwt is missing` / 弹出 GCM 桌面登录窗

**原因**：Windows 系统级 `credential.helper=manager`（Git Credential Manager）
抢先处理了 AISphere 的 git 请求，aisphere helper 没被调用。

**修复**：确保 `.gitconfig` 里有空行清继承：

```bash
# 检查
git config --global --get-all 'credential.https://api.weagent.cc:30723/git.helper'
# 应输出两行: 空行 + aisphere

# 如果缺空行或有多值，清理后重装：
git config --global --unset-all 'credential.https://api.weagent.cc:30723/git.helper'
git config --global --unset 'credential.https://api.weagent.cc:30723/git.usehttppath'
git config --global --replace-all 'credential.https://api.weagent.cc:30723/git.helper' ''
git config --global --add 'credential.https://api.weagent.cc:30723/git.helper' 'aisphere'
git config --global 'credential.https://api.weagent.cc:30723/git.usehttppath' 'true'
```

如果还有 GCM 残留的 `provider=generic`，也删掉：
```bash
git config --global --unset 'credential.https://api.weagent.cc:30723/git/<skill>.git.provider'
```

### `git-aisphere` 命令找不到

**原因**：二进制不在 PATH。

**修复**：
```powershell
# PowerShell
$env:PATH = "E:\coding\aisphereio\aisphere-git-cli\bin;" + $env:PATH
```
```bash
# Bash
export PATH="/e/coding/aisphereio/aisphere-git-cli/bin:$PATH"
```

### 登录卡住 / 超时

**原因**：浏览器没自动打开（Windows rundll32 在某些环境静默"成功"但不显示窗口）。

**修复**：看终端打印的 `https://casdoor.weagent.cc:30723/login/oauth/authorize?...`
URL，手动复制到浏览器打开。登录完成后终端会自动继续。

### `404 page not found`

**原因**：skill 仓库名不对。Git URL 里的名字要用 **repos 表的仓库名**
（如 `ttt1`），不是 skills 表的 skill name（如 `ttt`）。

**确认**：在 Hub Web 界面查看 skill 的 git 仓库地址，或问管理员。

### `403 AUTHZ_PERMISSION_DENIED`

**原因**：token 有效但你没有该 skill 的 `view`（读）或 `edit`（写）权限。

**修复**：在 Hub 界面申请权限，或联系 skill owner。

### `make build` 无输出 / bin 为空

**原因**：旧版 Make（GnuWin32 3.81）的 for-loop 在 Windows cmd.exe 下失效。

**修复**：直接手动编译（见[构建](#构建)章节的手动编译命令）。

### `Audiences in Jwt are not allowed`

**原因**：用了 Git CLI 的 token（aud=git-cli client_id）去打 `/v1/*` API 路由，
该路由只接受 web client 的 token。这是**有意的隔离**——Git token 只能用于
`/git`，不能用于 `/v1/*` API。

---

## 架构

```
git aisphere login  ─PKCE+browser─▶  Casdoor  ─access JWT─▶ ~/.aisphere/credentials.json

git clone/push      ─Bearer JWT──▶  Envoy /git (JWT-only) ─x-aisphere-*─▶ Hub ─▶ IAM ─▶ SpiceDB
```

- `/git` 路由用 **JWT-only** SecurityPolicy（无 OIDC 重定向），Git 永远不会
  收到 302/HTML 登录页
- Envoy 验证 Bearer JWT 签名（Casdoor JWKS），验证 `aud` 匹配 git-cli client_id
- 验证通过后，把 JWT 的 `id` claim（UUID）→ `x-aisphere-external-sub` 请求头
- Hub 用这个头重建 Principal → IAM → SpiceDB 授权（和浏览器路径归一到同一 UUID）
- credential helper 只对 `api.weagent.cc:30723` + `/git/` 路径返回 token
  （代码强制，非仅靠 .gitconfig），防止 token 泄漏到其他服务
- 客户端伪造的 `x-aisphere-*` 头被网关 ClientTrafficPolicy 全局剥除

### 安全模型

- **PKCE S256，无 client secret**——native public client（RFC 8252），不打包
  secret，refresh 也不带 secret
- **access token** 用于 Git Bearer 认证（不用 id_token）
- **refresh 跨进程文件锁**（`~/.aisphere/credentials.lock`），并发 LFS 请求
  只触发一次刷新
- **凭据文件 0600 / 目录 0700**（Unix）；Windows 为 MVP 明文文件，后续升级
  到 Windows Credential Manager
- `ephemeral=1` 防止 Git 把短期 token 交给其他 helper 的 `store`

### 文件结构

```
aisphere-git-cli/
├── cmd/
│   ├── git-aisphere/main.go          # login/logout/status/install/diagnose
│   └── git-credential-aisphere/main.go  # credential helper 协议入口
├── internal/
│   ├── config/config.go              # 配置 + 环境变量覆盖
│   ├── oauth/{pkce,flow,verify}.go   # PKCE + 授权码流程 + JWT claims 解析
│   ├── store/store.go                # ~/.aisphere/credentials.json (0600)
│   ├── store/filelock_{unix,windows}.go  # 跨进程文件锁
│   ├── credential/{protocol,helper}.go   # git credential helper KV 协议
│   ├── browser/browser.go            # 跨平台打开浏览器
│   └── gitconfig/gitconfig.go        # install: 写全局 .gitconfig
├── go.mod / go.sum / Makefile
└── README.md
```

---

## 服务端部署前置条件

本 CLI 依赖服务端的 Gateway 路由配置（`aisphere-hub/deploy/gateway/`）：

1. **`hub-git-route.yaml`** — 独立 `/git` HTTPRoute
   - `parentRefs`: `aisphere-gateway` / namespace `aisphere` / `sectionName: https`
   - backend: `aisphere-hub:18001`
2. **`hub-git-security-policy.yaml`** — JWT-only SecurityPolicy（无 `oidc` 块）
   - `targetRefs`: `HTTPRoute/hub-git-route`
   - `audiences`: `["ec15766f6cb98b908433"]`
   - `claimToHeaders`: `id` → `x-aisphere-external-sub`（UUID = SpiceDB key）
   - namespace: `aisphere`（不是 `aisphere-system`）
3. **`hub-http-route.yaml`** — `/git` 已从 OIDC protected route 移除
4. Casdoor 应用 `aisphere-git-cli`（Native/Public client，PKCE S256，
   redirect URI `http://127.0.0.1:52731/callback`，grant types 含
   `authorization_code` + `refresh_token`）

> **部署注意**：`hub-oidc` SecurityPolicy 的 `targetRefs` 必须指向
> `hub-api-protected-route`（不是已废弃的 `hub-http`），否则 `/v1/*`
> 浏览器登录会断。

---

## E2E 验收清单

| # | 场景 | 预期 |
| --- | --- | --- |
| 1 | `/git` 无 token | 401 + `WWW-Authenticate: Bearer`（无 302） |
| 2 | `/v1/*` 无 token | 302 OIDC 重定向（浏览器登录不破坏） |
| 3 | `git aisphere login` | 登录成功，显示用户名 + UUID |
| 4 | access_token claims | iss/aud/id(UUID)/exp 正确 |
| 5 | refresh 免 secret | HTTP 200 + 新 token |
| 6 | `git ls-remote` | 返回 refs |
| 7 | `git clone` | 克隆成功 |
| 8 | `git push` | push 成功 |
| 9 | `git fetch` | 无报错 |
| 10 | `git aisphere diagnose` | 全绿 |

---

## Known Follow-ups

- **随机 loopback 回调端口**（RFC 8252 §7.3）替代固定 52731
- **OS keychain 存储**（Windows Credential Manager / macOS Keychain /
  libsecret）替代 0600 文件
- **Hub-wide claim-mapping 统一**：现有 `hub-oidc-policy` 把 `sub`(用户名)
  → `x-aisphere-external-sub`，而 SpiceDB 按 UUID 授权。新 `/git` policy
  已用 `id`→`external-sub`（正确）。建议后续统一 `hub-oidc-policy`
- **Casdoor token 有效期收紧**：当前 `expireInHours=-1`（默认 7 天），
  建议改为 access 0.5h / refresh 720h
