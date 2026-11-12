# OneBot v11 接入

每个输入对应一个 Bot，要求声明 `self_id`。每条输出路由使用独立的 Bot 会话。

| 方向 | `ws_mode` | 地址与角色 |
| --- | --- | --- |
| 上游拨入 Hashinex | `reverse-listen` | `ws://HOST:6700/onebot/in/INPUT`，上游提供 `X-Self-ID` 和 `X-Client-Role: Universal` |
| Hashinex 拨入上游 | `forward-dial` | `inputs[].url` 指向上游 Universal 地址 |
| 下游拨入 Hashinex | `forward-listen` | `ws://HOST:6700/onebot/out/OUTPUT/INPUT` |
| Hashinex 拨入下游 | `reverse-dial` | `outputs[].url` 指向下游接收反向 Universal 的地址 |

在 WebUI 的输入和输出表单设置令牌，连接端使用 `Authorization: Bearer TOKEN`。现有 `token_env` 配置仍可用。监听非回环地址的 OneBot 端点必须配置令牌。监听端的 `listen_path` 可编辑，拨号端填写完整的 `ws://` 或 `wss://` 地址。控制台可通过 `public_ws_url` 指定对外显示的地址。 [正向 WS](https://github.com/botuniverse/onebot-11/blob/master/communication/ws.md) · [反向 WS](https://github.com/botuniverse/onebot-11/blob/master/communication/ws-reverse.md)

## NapCat 与 SnowLuma

将连接类型设为反向 Universal WebSocket 客户端，填写输入地址和令牌。在 Hashinex 填写 Bot 自己的 QQ 号作为 `self_id`。确认事件含真实 QQ 号与群号时设置 `identity_source: real-qq`。

WebUI 的输入编辑窗口提供可复制的完整「反向 WebSocket 连接地址」。路径留空时用 `/onebot/in/输入名称`，自定义路径时预览会同步更新。设置了 `public_ws_url` 时，预览使用该对外地址。保存后点击「重启网关」应用变更。

- 正常转发设置 `role: active`，添加至少一条路由。
- 设置 `role: observe` 可默认静默。通过 `business_groups` 指定例外群，或在业务 Bot 上用 `silent_groups` 指定静默群。静默群事件不发往业务路由。
- 官方转换适配器输出虚拟号时，设置 `identity_source: adapter-virtual` 并启用 SQLite。在 `ob:输入ID` 下分配稳定数字 ID，可在管理界面绑定真实 QQ 或群号。发消息、资料查询和 @ 等已支持动作会反查上游原始 ID。扩展动作需有相应的转换规则。

同一事件可以发到多个输出。用 `allow_actions: false` 将辅助输出设为只读，避免两套业务同时回复。`groups` 和 `users` 使用对外的数字 ID，过滤发生在身份转换之后。

路由设置了 `groups` 时默认只转发这些群的事件。设置 `include_private: true` 可同时转发私聊。私聊仍受该输入的私聊设置和路由的 `users` 过滤。

使用 `outputs: [gscore, astrbot]` 将一条路由发给多个后端，每个后端使用独立连接和 API 回程。旧的单输出 `to` 配置仍可读取。`passthrough: true` 发送原始事件帧，并记录转发计数。QQ 官方输入会原样发出 QQ 事件 JSON。使用 OneBot 协议的后端需要转换模式来处理 QQ 官方事件。
