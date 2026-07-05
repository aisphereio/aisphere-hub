# Hub Gateway Trusted AuthN

Hub 默认由 Gateway 访问。Gateway 完成 Casdoor JWT 校验后注入 Principal 和 `X-Aisphere-Internal-Token`。

Hub 的 `gateway_trusted` 模式只接受：

1. `X-Aisphere-Internal-Token` 与配置一致；
2. `X-Aisphere-Auth-Verified=true`；
3. `X-Aisphere-Subject` 等 Principal headers 存在。

这样 Hub 不需要处理登录、session、refresh token，也不需要每次访问 Casdoor。测试或高安全部署可以切回 `casdoor_jwt`，让 Hub 使用 Kernel `authn/oidcx` 再验一次 JWT。
