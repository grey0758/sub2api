# Secure custom admin embeds

Sub2API custom menu pages can embed external administration tools without forwarding the Sub2API login context. Keep the target page administrator-only and set `forward_context` to `false`:

```json
[
  {
    "id": "cliproxy-account-pool",
    "label": "CLIProxy 账号池",
    "url": "https://cliproxy.opencodex.uk/management.html#/account-pool",
    "visibility": "admin",
    "sort_order": 900,
    "forward_context": false
  },
  {
    "id": "cliproxy-phone-pool",
    "label": "CLIProxy 手机池",
    "url": "https://cliproxy.opencodex.uk/management.html#/phone-pool",
    "visibility": "admin",
    "sort_order": 901,
    "forward_context": false
  }
]
```

With this setting, Sub2API removes `user_id`, `token`, `src_host`, and `src_url` from the iframe URL while retaining the non-sensitive `theme`, `lang`, and `ui_mode=embedded` parameters. CLIProxy authentication stays independent inside the CLIProxy origin; do not put its management key in the menu URL or Sub2API configuration.

The target origin must also allow framing from the Sub2API origin through its `Content-Security-Policy frame-ancestors` and/or `X-Frame-Options` policy. If framing is blocked, change the CLIProxy response policy for the exact trusted Sub2API origin during a separately reviewed deployment rather than weakening it globally.

## 中文说明

以上配置将两个 CLIProxy 页面作为仅管理员可见的自定义菜单嵌入，并明确关闭 Sub2API 登录上下文转发。关闭后，iframe 链接不会携带 `user_id`、`token`、`src_host`、`src_url`，但仍保留 `theme`、`lang` 和 `ui_mode=embedded`。CLIProxy 的登录状态必须继续由 CLIProxy 自己管理，禁止把 management key 写进菜单 URL 或 Sub2API 配置。

目标站点还必须通过 `Content-Security-Policy frame-ancestors` 或 `X-Frame-Options` 允许对应的 Sub2API 来源嵌入。如果当前策略禁止 iframe，应在后续单独部署中只放行精确可信的 Sub2API origin，不要全局放开。
