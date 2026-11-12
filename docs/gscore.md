# gscore 直连接入

链路是 **官方 Bot → Hashinex → gscore**。在 WebUI 的「输出管理」添加「gsuid_core 直连」，填入 gsuid_core 提供的完整 WebSocket 地址，例如 `ws://127.0.0.1:8765/ws/hashinex`。如果服务配置了 WebSocket 令牌，直接在同一表单填写。保存后重启网关，网关会自动发起连接。

每项 gscore 输出有独立的地址、令牌和连接。多个输入选择同一项输出时共用一条连接，gscore 回复通过 `bot_self_id` 返回对应 Bot。共用输出的输入需要各自的 `self_id`。旧的 `{input}` 地址会拆分成可单独编辑的输出。一个路由也可以同时选择 gsuid_core 和 AstrBot 等输出。

```yaml
outputs:
  - id: gscore
    protocol: gscore
    url: ws://127.0.0.1:8765/ws/hashinex
routes:
  - from: official-a
    outputs: [gscore]
    events: [message]
    allow_actions: true
  - from: official-b
    outputs: [gscore]
    events: [message]
    allow_actions: true
```

## 独立适配器

如需在另一台机器单独运行协议适配器，可配置一个 `forward-listen` OneBot 输出，把下方配置保存到适配器可执行文件旁的 `config/gscore.yaml`，然后启动 `hashinex-gscore`。也可使用 `-config` 指定配置文件。每只 Bot 使用独立的 OneBot 路由和 gscore WebSocket 名称。

```yaml
connections:
  - name: official-a
    self_id: "10001"
    onebot_url: ws://127.0.0.1:6700/onebot/out/gscore/official-a
    gscore_url: ws://127.0.0.1:8765/ws/Hashinex-A
```

`self_id` 填网关输入配置中相同的值。可选 `onebot_token_env` 与 `gscore_token_env` 分别填写令牌环境变量名。跨主机部署使用受控网络或 `wss`。

适配器使用 `bot_id=onebot` 上报消息，gscore 可继续用已映射的真实 QQ 号查询 OneBot 历史绑定。`bot_self_id` 使用当前 Bot 的 `self_id`。回复使用原消息的 `msg_id`，在同一条 Bot 会话提交 OneBot API 请求，实际发送权限与被动回复有效期由 Hashinex 和 QQ 校验。需要接收图片回复时，按 Base64 负载配置网关的 `limits.frame_bytes`。

入站支持群聊和私聊的常见消息段。官方 Bot 回程支持文本、图片、@ 和平台允许的引用。两只官方 Bot 若都配置相同测试群的业务路由，gscore 会分别收到两条事件。
