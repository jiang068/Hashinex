# Hashinex

Hashinex 是一个独立运行的 QQ Bot 网关。它接收 QQ 官方 Bot 和 OneBot v11 事件，把不同官方 Bot 下的 OpenID 关联到真实 QQ 号或群号，再按路由将事件送往 AstrBot、AiBot、gsuid_core（gscore）等后端。身份映射由 Hashinex 的 SQLite 管理，后端可继续使用真实 QQ 号查找原有业务数据。

项目仍在功能迭代中。QQ 官方 Bot 的群聊、绑定后的私聊和 gscore 直连已完成实机联调。可接收的事件和可调用的动作取决于各 Bot 的平台权限。

## 快速启动

1. 使用 Go 1.26.11 或更新版本编译，在 Windows 或 Linux 上选择目标架构。编译指南将工具链、依赖缓存和产物放在源码仓库外。 [编译指南](docs/build.md)
2. 把可执行文件放到运行目录，复制公开样例为同目录下的 `config/config.yaml`。使用 `-check` 校验配置，再用 `-admin-setup` 首次启动。 [部署指南](docs/operations.md#首次启动) · [配置样例](config.example.yaml)
3. 在 WebUI 设置管理令牌，添加输入、输出和路由。保存后点击「应用配置」。保存失败时，页面会指出相关字段、实例和修改方法。 [本地 WebUI](http://127.0.0.1:6700/admin/)

## 接入与路由

| 场景 | 配置方式 |
| --- | --- |
| QQ 官方 Bot | 每只 Bot 建一个独立输入，填写 AppID 和 AppSecret，选择官方 Gateway 或签名 Webhook。 |
| NapCat、SnowLuma 等 OneBot 上游 | 建立 OneBot v11 输入。反向 WS 使用 WebUI 显示的完整地址，例如 `ws://127.0.0.1:6700/onebot/in/observer`。 |
| AstrBot、AiBot 等 OneBot 下游 | 建立 OneBot v11 输出，按下游配置选择监听或拨号。AstrBot 可接入不同 `self_id` 的 Bot，AiBot 使用单个来源会话。 |
| gsuid_core 直连 | 建立独立的 gscore 输出，填写 gsuid_core 的完整 WS 地址，例如 `ws://127.0.0.1:8765/ws/hashinex`。多个输入选择同一个 gscore 输出时共用连接。 |

一个输入可以通过路由同时发送到多个输出。路由可按事件、真实 QQ 号和群号过滤，也可控制下游 API 回程。各输入可按真实群号指定静默群和业务群。默认转换事件及身份，开启「原样透传事件」后保留上游格式和 ID，并记录转发计数。透传 QQ 官方输入时，下游需要能解析 QQ 官方原始 JSON。

使用野生 Bot 收消息时，可在路由里指定官 Bot 为部分群或全部群发送消息。名单外的群继续由来源 Bot 回复。 [配置步骤](docs/onebot.md#野生-bot-收消息官-bot-发群消息) · [AstrBot 与 AiBot 接入差异](docs/onebot.md#astrbot-与-aibot)

官 Bot 可以用限时链接分享文件、多图和合并转发消息。媒体上传失败或直发消息被 QQ 拒绝时，网关会尝试发送带默认标题的分享链接。分享服务使用独立 HTTP 端口，默认有效期为 10 分钟。NapCat 消息经 gscore 处理时，可读取引用正文和图片。 [临时分享配置](docs/tempfiles.md) · [gscore 引用适配](docs/gscore.md)

## 身份映射与私聊

- WebUI 以真实 QQ 号或真实群号聚合映射，展开后可查看各 Bot 的 OpenID。身份列表支持按来源群、QQ 号和 OpenID 检索，可选每页 10、20、50、100 或 200 条。
- 静默匹配通过观察 Bot 对照同一群消息，再比对双方头像。所有非空纯文本均可参与配对。使用真实 QQ 号的业务 Bot 加入观察实例白名单后，可以同时接收业务和提供匹配证据。已有绑定可在没有观察 Bot 的群里继续用于正常业务。 [匹配与头像验证](docs/matching.md)
- 官方 Bot 私聊默认关闭。开启后，已绑定用户的消息按路由转发。新用户可在与官 Bot 同在的群里使用一次性绑定码，网关通过观察 Bot 和头像确认身份，随后由官 Bot 在群里回复「绑定成功」。观察 Bot 不可用时，未绑定用户收到「私聊暂不可用」。

## 配置、数据与迁移

`config/` 保存网关配置和密钥，`data/` 保存 SQLite、日志和启用分享后生成的临时文件，两者默认位于可执行文件旁。迁移时先停止网关，再复制这两个目录到目标运行目录。运行配置、数据及其备份应留在源码仓库外。 [目录结构与迁移步骤](docs/operations.md)

## 源码目录

| 目录 | 内容 |
| --- | --- |
| `cmd/hashinex/` | 网关启动与运行管理 |
| `cmd/hashinex-gscore/` | 可选的独立 gscore 协议适配器 |
| `internal/gateway/` | 连接、路由、身份转换和内嵌 WebUI |
| `internal/qqofficial/`、`internal/onebot/`、`internal/gscore/` | 协议接入和消息格式 |
| `internal/config/`、`internal/matching/`、`internal/store/` | 配置、静默匹配和 SQLite 存储 |
| `internal/tempfiles/` | 临时分享页面、附件存储与过期清理 |

## 文档

- [QQ 官方 Bot 接入](docs/qq-official.md)
- [OneBot v11 接入](docs/onebot.md)
- [gscore 直连与独立适配器](docs/gscore.md)
- [官 Bot 临时分享与公网部署](docs/tempfiles.md)
- [静默匹配与头像验证](docs/matching.md)
- [Windows 和 Linux 编译指南](docs/build.md)
- [部署、升级与故障排查](docs/operations.md)
- [任务清单](todo.md) · [更新日志](log.md)

## 参考项目与致谢

感谢以下项目的作者和维护者为 Hashinex 的实现提供参考。

| 项目 | 参考内容 |
| --- | --- |
| [BotShepherd](https://github.com/Loping151/BotShepherd) | OneBot 多连接管理、事件分发和 `echo` 回程关联。 |
| [Gensokyo-NewQQ](https://github.com/Te-River/Gensokyo-NewQQ) | 用 AppID 与 OpenID 构造 QQ 官方头像地址，以及群消息的被动回复上下文。 |
| WuwaIdMigration | 用消息时间、文本和头像指纹配对用户的思路。 |
| [napcat-plugin-gscore-adapter](https://github.com/xiowo/napcat-plugin-gscore-adapter) | OneBot 与 gscore 的消息转换、引用上下文查询及合并转发节点解析。 |
| [gsuid_core（gscore）](https://github.com/Genshin-bots/gsuid_core) | 直连 WebSocket 的消息结构、回复关联，以及沿用 OneBot 平台标识与真实 QQ 号查询历史绑定。 |

## 许可证

Apache-2.0。见 [LICENSE](LICENSE) 与 [第三方依赖和版权声明](THIRD_PARTY_NOTICES.md)。
