# OIDC 退出登录死循环排障记录

> 日期：2026-07-27
> 环境：Envoy Gateway v1.8.2 / Casdoor 2026.07.01
> 涉及：hub.weagent.cc（aisphere-hub 前端控制台）、Casdoor `aisphere-hub` 应用

## 症状

点击 hub 控制台侧边栏的「退出登录」按钮，浏览器**毫无反应**——页面不跳转、session 不清、用户停留在已登录态。打开 DevTools 才能看到 `https://hub.weagent.cc:30723/logout` 直接返回 `ERR_TOO_MANY_REDIRECTS`，最终死在 `casdoor.weagent.cc`。

修复过程中又遇到第二阶段症状：`/logout` 不再循环，但 Casdoor 返回 JSON 错误 `{"status":"error","msg":"重定向 URI：https://hub.weagent.cc:30723/ 在许可跳转列表中未找到"}`，浏览器停在错误页而非登录页。

## 架构背景

Hub 前端控制台跑在 `gateway_oidc` 模式下：Envoy Gateway 终结 OIDC，未登录请求 302 到 Casdoor，登录后 Envoy 种 session cookie（`OauthHMAC-*`、`Aisphere-Hub-AccessToken` 等，全部 HttpOnly）并注入 `x-aisphere-*` 受信头给后端。前端代码（`aisphere-hub-front/src/hooks/use-auth.ts` 的 `useLogout()`）退出时只做一件事：

```ts
window.location.href = GATEWAY_LOGOUT_PATH;  // 默认 "/logout"
```

即把退出动作完全交给 Envoy Gateway 的 `SecurityPolicy.oidc.logoutPath` 处理。前端不碰 token、不碰 cookie（都是 HttpOnly，JS 读不到）。

## 根因一：SecurityPolicy 缺 `endSessionEndpoint` → 死循环

`deploy/gateway/hub-console-security-policy.yaml` 原本只配了 `logoutPath: "/logout"`，没有 `provider.endSessionEndpoint`：

```yaml
oidc:
  provider:
    issuer: "https://casdoor.weagent.cc:30723"
    authorizationEndpoint: "https://casdoor.weagent.cc:30723/login/oauth/authorize"
    tokenEndpoint: "http://casdoor.aisphere:8000/api/login/oauth/access_token"
    # ❌ 缺 endSessionEndpoint
  logoutPath: "/logout"
```

### 为什么会循环

Envoy OIDC filter 对 `logoutPath` 的处理分两种情况（实测 + 读 `api/v1alpha1/oidc_types.go` 字段注释确认）：

- **未配 `endSessionEndpoint`**：访问 `logoutPath` 时 Envoy 只清自己的 session cookie，**不产生任何跳转**。但 `/logout` 本身受 OIDC 保护，cookie 刚被清掉 → 重新判定未登录 → 302 到 Casdoor 登录 → Casdoor SSO session 还在 → 自动签发 code → callback 种回 cookie → 回到 `/logout` → 又被清掉 → … → `ERR_TOO_MANY_REDIRECTS`。
- **配了 `endSessionEndpoint`**：Envoy 清完自己的 cookie 后，**302 到 `endSessionEndpoint`**（带上 `id_token_hint`、`client_id`、`post_logout_redirect_uri`），跳出 `/logout` 自循环。

### 修复

给 `hub-console-oidc` SecurityPolicy 的 `provider` 块补上 Casdoor 的 end-session 端点：

```yaml
oidc:
  provider:
    issuer: "https://casdoor.weagent.cc:30723"
    authorizationEndpoint: "https://casdoor.weagent.cc:30723/login/oauth/authorize"
    tokenEndpoint: "http://casdoor.aisphere:8000/api/login/oauth/access_token"
    endSessionEndpoint: "https://casdoor.weagent.cc:30723/api/logout"   # ✅ 补这一行
  logoutPath: "/logout"
```

Casdoor 的 end-session 端点从 `/.well-known/openid-configuration` 的 `end_session_endpoint` 字段取，这里是 `https://casdoor.weagent.cc:30723/api/logout`。

> ⚠️ **不要手动指定 `authorizationEndpoint`/`tokenEndpoint` 的同时又指望 Envoy 自动发现 `endSessionEndpoint`**。Envoy Gateway 的自动发现只在 authorizationEndpoint/tokenEndpoint **都未提供** 时才查 well-known；只要手动指定了任一个，自动发现就被跳过，`endSessionEndpoint` 必须显式写出来。`hub-console-oidc` 正是因为手动指定了 auth/token endpoint 而漏写了 endSessionEndpoint，才触发循环。

应用命令：

```bash
kubectl -n aisphere patch securitypolicy hub-console-oidc --type=merge \
  -p '{"spec":{"oidc":{"provider":{"endSessionEndpoint":"https://casdoor.weagent.cc:30723/api/logout"}}}}'
```

## 根因二：Casdoor 应用 `redirect_uris` 白名单缺 hub 根 URL → 跳转被拒

补上 `endSessionEndpoint` 后，`/logout` 不再循环，但 Casdoor 返回：

```json
{"status":"error","msg":"重定向 URI：https://hub.weagent.cc:30723/ 在许可跳转列表中未找到"}
```

### 原因

Envoy 跳到 Casdoor `/api/logout` 时带的 `post_logout_redirect_uri=https://hub.weagent.cc:30723/`（hub 根路径，带末尾 `/`）。Casdoor 的 `controllers/account.go` 里 `redirectToPostLogout()` 用 **`application.IsRedirectUriValid(redirectUri)`** 校验该 URL——Casdoor **没有独立的 post-logout redirect 列表**，它直接复用应用的 `redirect_uris` 白名单（见 `redirectToPostLogout` 源码）。

而 `aisphere-hub` 应用的 `redirect_uris` 原本只有：

```json
["https://hub.weagent.cc:30723/oauth2/callback", "http://localhost:3002", "https://api.weagent.cc:30723/v1/authn/exchange"]
```

没有 `https://hub.weagent.cc:30723/`，校验失败 → 报错不跳转 → 浏览器停在 JSON 错误页。

### 修复

在 Casdoor `aisphere-hub` 应用的 `redirect_uris` 里补上 hub 根 URL。可直接改 PostgreSQL（Casdoor 配置存在 `casdoor` 库的 `application` 表）：

```bash
kubectl -n aisphere exec apps-postgre-0 -- psql -U postgres -d casdoor -c \
"UPDATE application SET redirect_uris = '[\"https://hub.weagent.cc:30723/oauth2/callback\",\"https://hub.weagent.cc:30723/\",\"http://localhost:3002\",\"https://api.weagent.cc:30723/v1/authn/exchange\"]' WHERE client_id='bbdcfc272e2b990cb923';"
```

更推荐通过 Casdoor 管理 UI（admin 登录 `casdoor.weagent.cc:30082` → 应用 `aisphere-hub` → Redirect URIs）添加 `https://hub.weagent.cc:30723/`，避免直接改库被后续 UI 编辑覆盖。

> 注意末尾 `/` 必须与 Envoy 传的 `post_logout_redirect_uri` 完全一致。Envoy 默认用 `post_logout_redirect_uri=<redirectURL 的 origin>/`，即 `https://hub.weagent.cc:30723/`。

## 最终退出流程（修复后）

```
浏览器 GET /logout
  → Envoy 清自身 OIDC cookie（OauthHMAC/Aisphere-Hub-*/RefreshToken）
  → 302 到 https://casdoor.weagent.cc:30723/api/logout
        ?id_token_hint=<IDToken cookie 值>
        &client_id=bbdcfc272e2b990cb923
        &post_logout_redirect_uri=https://hub.weagent.cc:30723/
  → Casdoor 校验 post_logout_redirect_uri 通过（已在白名单）
  → 302 回 https://hub.weagent.cc:30723/
  → hub / 受 OIDC 保护，Envoy cookie 已清 → 302 到 Casdoor 登录页
  → 用户看到登录界面 ✅ 退出完成
```

## 验证方法

用 Playwright（或任意能清 cookie 的浏览器自动化）跑干净流程：

```js
const { chromium } = require('playwright');
const browser = await chromium.launch({ headless: true, proxy: { server: 'http://127.0.0.1:7897' } });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await ctx.newPage();

// 1. 登录
await page.goto('https://hub.weagent.cc:30723/');
await page.fill('input[type="text"]', 'admin');
await page.fill('input[type="password"]', '123');
await page.click('button:has-text("Sign In")');
await page.waitForURL(u => u.toString().includes('hub.weagent.cc'));

// 2. 退出
await page.goto('https://hub.weagent.cc:30723/logout');
await page.waitForTimeout(3000);

// 3. 确认 session cookie 已清
const cookies = await ctx.cookies();
const sessionLeft = cookies.filter(c => c.domain.includes('hub.weagent.cc'))
                           .some(c => /OauthHMAC|Aisphere-Hub-Access|RefreshToken/.test(c.name));
console.log('session cleared:', !sessionLeft);

// 4. 再访问 hub 应跳到 Casdoor 登录页
await page.goto('https://hub.weagent.cc:30723/');
console.log('at login page:', page.url().includes('casdoor'));
```

修复前：步骤 2 报 `ERR_TOO_MANY_REDIRECTS`（根因一）或 Casdoor JSON 错误（根因二）。
修复后：步骤 3 输出 `true`，步骤 4 输出 `true`。

## 踩过的坑（避免重复）

1. **`endSessionEndpoint` vs `logoutPath` 不是二选一**——实测 Envoy 设了 `endSessionEndpoint` 后**仍然会清自己的 cookie**（`logoutPath` 的原生行为不变），`endSessionEndpoint` 只是额外提供一个跳转目标。两者配合才完整：清 Envoy cookie + 跳 Casdoor 清 IdP session + 跳回 hub。
2. **不要试图让 Envoy 传 access token 当 `id_token_hint`**（曾尝试把 `cookieNames.idToken` 指向 `Aisphere-Hub-AccessToken` cookie）。Casdoor `/api/logout` 虽然把 `id_token_hint` 当 access token 调 `ExpireTokenByAccessToken`，但 Envoy cookie 里存的是 **Envoy 自己加密包装后的 token**，Casdoor token 表里查不到 → "Token not found, invalid accessToken"。退出能成功靠的是 Envoy 清 cookie，不是 Casdoor expire token。
3. **Casdoor `/login/oauth/logout` 是 SPA 路径，不能给 Envoy 当 `endSessionEndpoint`**——它卡在 "Loading" 不跳转，因为这是给用户在 Casdoor UI 手动点退出用的前端页面，没有服务端 302 行为。`endSessionEndpoint` 必须用 `/api/logout`（服务端 API）。
4. **Casdoor `/api/logout` 当 `id_token_hint` 为空且 Casdoor SSO session 不存在时，直接返回 `{"status":"ok"}` 不跳转**。Envoy OIDC 模式下用户从不直接登录 Casdoor，SSO session 不可靠，所以**不能依赖 Casdoor session 分支**清退出——必须靠 `post_logout_redirect_uri` 白名单 + Casdoor 302 跳回，再让 Envoy 的 `logoutPath` 清自己的 cookie。
5. **`ctx.cookies('https://host:port/')` 带 port 查询在 Playwright 里返回空**——查 cookie 用 `ctx.cookies()` 不带参数，再自己 filter domain。

## 涉及的其他组件

- `hub-security-policy.yaml`（保护 `api.weagent.cc` 的 API 路由）和 `deploy/k8s/base/securitypolicy-http.yaml`（模板）**同样缺 `endSessionEndpoint`**。它们不被前端退出按钮直接触发（前端只跳 hub.weagent.cc 的 `/logout`），但如果有其他客户端走 `/v1/authn/logout` 退出，会遇到同样的循环。建议一并补上 `endSessionEndpoint`。

## 相关文件

- `deploy/gateway/hub-console-security-policy.yaml` —— hub 前端控制台 OIDC SecurityPolicy（含修复 + 注释）
- `aisphere-hub-front/src/hooks/use-auth.ts` —— 前端 `useLogout()` 逻辑
- `aisphere-hub-front/docs/oidc-login-loop-troubleshooting.md` —— 登录循环（非退出循环）的排障记录，与本篇互补
- Casdoor 应用 `aisphere-hub`（client_id `bbdcfc272e2b990cb923`）的 `redirect_uris` 字段
