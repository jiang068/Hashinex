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

## 野生 Bot 收消息，官 Bot 发群消息

1. 将野生 Bot 接为提供真实 QQ 号的 OneBot 输入，并放行业务群。
2. 让官 Bot 加入目标群，在其输入设置开启「允许主动发送」，并在「身份映射」中确认该官 Bot 的群 OpenID 已绑定到真实群号。官 Bot 配置了群白名单时，目标群必须在白名单内。目标群可继续在官 Bot 输入上保持静默。
3. 在「路由管理」选择野生 Bot 为来源、gscore 或 AstrBot 为输出，开启「允许下游 API 回程」，在「群消息发送 Bot」选择官 Bot。填写「官 Bot 发送群号」后，只有名单内的群改由官 Bot 发送，其他群仍由野生 Bot 回复。此字段留空时，官 Bot 负责该路由允许的所有群。保存后重启网关。

```yaml
routes:
  - from: napcat
    outputs: [gscore]
    allow_actions: true
    send_group_via: official-a
    send_group_via_groups: ["123456789", "987654321"]
```

「群号过滤」决定整条路由允许处理哪些群。仅切换部分群的发送 Bot 时，填写「官 Bot 发送群号」即可。仅需官 Bot 发消息的群，无需再为该官 Bot 配置通往同一后端的入站路由。

改由官 Bot 发送时，网关会移除野生 Bot 的消息 ID 和引用段。文本、图片与有映射的 @ 使用官 Bot 支持的消息格式。私聊发送、资料查询和撤回等其他动作仍交给来源 Bot。官 Bot 发送失败时，下游收到错误。清空「群消息发送 Bot」恢复使用来源 Bot 发群消息。

## AstrBot 与 AiBot

- AstrBot 的 OneBot v11 适配器可同时接入不同 `self_id` 的 Bot，基于原事件的回复按该事件的 `self_id` 选择连接。同一群事件经两个输入交给 AstrBot 时，可能处理两次。定时发送等缺少原事件的动作需要能选定发送 Bot。
- AstrBot 通过官 Bot 发送本地文件时，给 AstrBot 输出配置「官 Bot 本地文件目录」，并让网关能以相同路径读取文件。启用临时分享后，官 Bot 发送限时链接。 [文件发送配置](qq-official.md#发送普通文件) · [临时分享部署](tempfiles.md)
- AiBot 的发送器只保留一个 Bot 实例。使用野收官发时，在野生 Bot 路由选择 AiBot 输出，并在该路由配置「群消息发送 Bot」和代发群号。官 Bot 的直连业务路由只勾选需要它直接接收事件的其他输出。AiBot 的 OneBot 输出可使用 `reverse-dial`，地址填写它实际监听的完整反向 WebSocket 地址，例如 `ws://127.0.0.1:8080/onebot/v11/ws`。
- 野生 Bot 输入的静默群会停止向其所有业务输出投递该群事件。AiBot 要从野生 Bot 收某群的消息时，该群需允许转发。官 Bot 作为指定发送者时可保持入站静默，发送仍受目标群映射、平台权限和群白名单限制。
