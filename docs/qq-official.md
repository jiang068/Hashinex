# QQ 官方 Bot 接入

在开放平台取得 AppID 和 AppSecret 后，为每个 Bot 创建独立输入。以下为示例值。

```yaml
storage:
  sqlite: "../data/hashinex.sqlite"
inputs:
  - id: official-a
    protocol: qq-official
    app_id: "10001"
    self_id: "10001"
    secret_env: HASHINEX_QQ_A_SECRET
    events: gateway
    allow_proactive: false
```

- `events: gateway` 使用官方 Gateway，默认请求群与单聊事件位 `1 << 25`。只有获批的场景才修改 `intents`。以实际收到 READY 和目标事件为准。 [官方事件文档](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/event-emit.html)
- `events: webhook` 使用 `POST /qq/webhook/INPUT`，通过 HTTPS 反向代理将对应路径公开给平台。网关校验 AppID 头（若有）、时间戳与签名，支持平台回调地址验证。 [官方回调说明](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/event-emit.html)
- OpenAPI 默认使用 `api.bot.qq.com`。如果消息路径在该域名发生连接中断，可以为实例配置 `api_origin: sgroup` 选择官方 `api.sgroup.qq.com`。发送动作由所选域名处理，网关不会在结果未知时自动切换域名并重发。
- 群事件支持 `GROUP_MESSAGE_CREATE` 和 `GROUP_AT_MESSAGE_CREATE`，私聊支持 `C2C_MESSAGE_CREATE`。普通群消息权限需逐 AppID 确认。`self_id` 是供 OneBot 使用的固定 Bot ID，事件和握手保持一致。
- `group_ids` 填真实 QQ 群号，`private_qq_ids` 填真实 QQ 号。群白名单根据已确认的群映射放行，私聊白名单在识别出真实 QQ 号后生效。留空允许该场景中的全部身份，私聊默认关闭，可用 `enable_private: true` 开启。旧配置中已有真实映射的条目会转换成真实 ID。
- 已绑定真实 QQ 号的私聊按路由正常转发。未绑定用户收到一次性绑定码后，在十分钟内到自己和官 Bot 都在的群里 @ 官 Bot 发送该码。发码的官 Bot 和观察 Bot 都要在同一个已映射群收到该码。网关验证私聊 OpenID 对应头像和观察到的真实 QQ 头像后，锁定绑定群、写入账号绑定，并由官 Bot 在该群回复「绑定成功」。没有在线观察 Bot 或根据群列表确认没有共同群时，私聊回复「私聊暂不可用」。旧的 `private_bind_group` 配置可以删除。
- 每个输入可分别用真实群号设置 `silent_groups` 和 `business_groups`。静默群仍可用于匹配和私聊绑定，业务事件不会转发到后端。路由明确指定该官 Bot 发送群消息时，静默群可接收代发消息，发送仍受官 Bot 群白名单约束。

## API

支持 `send_msg`、`send_group_msg`、`send_private_msg`、`get_msg`、`delete_msg`、`get_login_info`、`get_status`、`get_version_info`、`get_group_info`、`get_group_member_info` 和 `get_group_member_list`。发送支持文本、已知成员 @、引用，以及每次一个 HTTPS 或 Base64 图片。入站图片仅接受事件中的 HTTPS 地址。使用 Base64 时还需按负载调高 OneBot `limits.frame_bytes`。

群成员查询优先走 QQ 官方接口。查询 Bot 自身使用群内状态接口，并返回配置中的 `self_id`。群与成员 ID 按当前映射转换，结果受输入和路由范围限制。

官方输入设置 `observed_profiles: true` 后，群成员接口遇到应用权限错误 `11253` 时可查询近期在该群发过消息的用户，并启用 `get_stranger_info` 查询路由范围内已观察的用户。记录随消息保留期过期。结果用 `hashinex_source: observed` 标明来源，成员列表可能不完整，角色和群名片等未知字段省略。请求设置 `no_cache: true` 时只允许官方查询。资料请求使用独立队列，群列表分页期间仍可处理回复。

- 发送参数可以带 `message_id` 作为被动回复上下文。OneBot `reply` 消息段需要原事件提供引用索引。网关分别处理被动发送凭据和引用索引。
- `implicit_reply: unique-recent` 仅在该连接同一会话有唯一近期消息时推断被动回复上下文。出现多条候选时返回 `1409`，需提供明确的消息 ID。
- 主动发送默认关闭。权限确认后可设置 `allow_proactive: true`。平台错误或超时会返回失败。发送超时后先确认消息是否已经送达。

群用户和私聊用户分别使用 `qq:AppID` 和 `qq:AppID:c2c` 识别来源。WebUI 按真实 QQ 号和群号归并所有来源，可以展开查看、调整和添加关联。AppSecret 可直接在输入表单中设置。 [官方凭证](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/access-token.html) · [官方消息收发](https://bot.q.qq.com/wiki/develop/api-v2/server-inter/message/overview.html)
