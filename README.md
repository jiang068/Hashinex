# Hashinex

Hashinex 是一个独立运行的 QQ Bot 网关。它接收 QQ 官方 Bot 和 OneBot v11 事件，把同一用户在不同官方 Bot 下的 OpenID 关联到真实 QQ 号，再按路由将事件送往 AstrBot、gsuid_core（gscore）等后端。后端可继续用真实 QQ 号查找原有业务数据，身份映射由 Hashinex 自己的 SQLite 管理。

项目仍在功能迭代中。QQ 官方 Bot 的群聊、绑定后的私聊和 gscore 直连已完成实机联调。可接收的事件和可调用的动作取决于各 Bot 的平台权限。

## 快速启动

需要 Go 1.26.11 或更高版本。在源码根目录执行以下命令。Windows 示例将开发用可执行文件和运行目录放在源码仓库旁边。

```powershell
$run = "../cache/hashinex-dev"
New-Item -ItemType Directory -Force "$run/config" | Out-Null
go build -trimpath -o "$run/hashinex.exe" ./cmd/hashinex
if (!(Test-Path "$run/config/config.yaml")) {
    Copy-Item config.example.yaml "$run/config/config.yaml"
}
& "$run/hashinex.exe" -check
& "$run/hashinex.exe" -admin-setup
```

在 Linux 或 macOS 上，使用同一构建命令并省略文件名的 `.exe` 后缀。运行时默认按可执行文件的位置找 `config/config.yaml`，也可以用 `-config` 指定其他 YAML。

1. 打开 [本地 WebUI](http://127.0.0.1:6700/admin/)。首次启动时留空登录，在「系统设置 → 管理访问」设置管理令牌。
2. 在 WebUI 添加输入、输出和路由。QQ AppSecret、OneBot 令牌和 gscore 令牌都可在对应表单里保存。
3. 保存草稿后点击「重启网关」应用变更。完成首次配置后，日常直接运行可执行文件。可通过 WebUI 或 `/healthz` 查看连接状态。

## 接入与路由

| 场景 | 配置方式 |
| --- | --- |
| QQ 官方 Bot | 每只 Bot 建一个独立输入，填写 AppID 和 AppSecret，选择官方 Gateway 或签名 Webhook。 |
| NapCat、SnowLuma 等 OneBot 上游 | 建立 OneBot v11 输入。反向 WS 使用 WebUI 显示的完整地址，例如 `ws://127.0.0.1:6700/onebot/in/observer`。 |
| AstrBot 等 OneBot 下游 | 建立 OneBot v11 输出，按下游配置选择监听或拨号。 |
| gsuid_core 直连 | 建立独立的 gscore 输出，填写 gsuid_core 的完整 WS 地址，例如 `ws://127.0.0.1:8765/ws/hashinex`。多个输入选择同一个 gscore 输出时共用连接。 |

一个输入可以通过路由同时发送到多个输出。路由可按事件、真实 QQ 号和群号过滤，也可控制下游 API 回程。各输入可按真实群号指定静默群和业务群。默认转换事件及身份，开启「原样透传事件」后保留上游格式和 ID，并记录转发计数。透传 QQ 官方输入时，下游需要能解析 QQ 官方原始 JSON。

## 身份映射与私聊

- WebUI 以真实 QQ 号或真实群号聚合映射，展开后可查看各 Bot 的 OpenID。身份列表支持按来源群、QQ 号和 OpenID 检索，可选每页 10、20、50、100 或 200 条。
- 静默匹配通过观察 Bot 对照同一群消息，再比对双方头像。所有非空文本均可参与配对。可先用 `shadow` 查看候选，再按需要开启 `auto`。观察范围内的群才登记待匹配身份，匹配成功后清理待匹配记录。已有绑定可在没有观察 Bot 的群里继续用于正常业务。
- 官方 Bot 私聊默认关闭。开启后，已绑定用户的消息按路由转发。新用户可在与官 Bot 同在的群里使用一次性绑定码，网关通过观察 Bot 和头像确认身份，随后由官 Bot 在群里回复「绑定成功」。观察 Bot 不可用时，未绑定用户收到「私聊暂不可用」。

## 配置、数据与迁移

```text
hashinex.exe
config/
  config.yaml
  config.yaml.secrets.json
data/
  hashinex.sqlite
  hashinex.jsonl
```

- [config.example.yaml](config.example.yaml) 是公开样例。实际配置可能包含 Bot 标识、群号和接入地址。WebUI 保存的密钥写入同目录的 `config.yaml.secrets.json`，YAML 里保存引用。
- `data/` 保存身份映射、匹配记录、回复上下文和日志。数据库及日志可能包含账号标识和消息数据。实际运行目录及备份应放在源码仓库外。
- 迁移时先正常停止网关，把整个 `config/` 和 `data/` 复制到目标可执行文件旁，运行 `hashinex -check` 后启动。自定义外部路径、环境变量密钥和下游业务库随各自的部署迁移。

## 源码目录

| 目录 | 内容 |
| --- | --- |
| `cmd/hashinex/` | 网关启动与运行管理 |
| `cmd/hashinex-gscore/` | 可选的独立 gscore 协议适配器 |
| `internal/gateway/` | 连接、路由、身份转换和内嵌 WebUI |
| `internal/qqofficial/`、`internal/onebot/`、`internal/gscore/` | 协议接入和消息格式 |
| `internal/config/`、`internal/matching/`、`internal/store/` | 配置、静默匹配和 SQLite 存储 |

## 文档

- [QQ 官方 Bot 接入](docs/qq-official.md)
- [OneBot v11 接入](docs/onebot.md)
- [gscore 直连与独立适配器](docs/gscore.md)
- [静默匹配与头像验证](docs/matching.md)
- [部署、迁移与故障排查](docs/operations.md)

## 参考项目与致谢

感谢以下项目的作者和维护者为 Hashinex 的实现提供参考。

| 项目 | 参考内容 |
| --- | --- |
| [BotShepherd](https://github.com/Loping151/BotShepherd) | OneBot 多连接管理、事件分发和 `echo` 回程关联。 |
| [Gensokyo-NewQQ](https://github.com/Te-River/Gensokyo-NewQQ) | 用 AppID 与 OpenID 构造 QQ 官方头像地址，以及群消息的被动回复上下文。 |
| WuwaIdMigration | 用消息时间、文本和头像指纹配对用户的思路。 |
| [napcat-plugin-gscore-adapter](https://github.com/xiowo/napcat-plugin-gscore-adapter) | OneBot 事件和消息段转换成 gscore 格式，以及 gscore 回复转回 OneBot 发送动作。 |
| [gsuid_core（gscore）](https://github.com/Genshin-bots/gsuid_core) | 直连 WebSocket 的消息结构、回复关联，以及沿用 OneBot 平台标识与真实 QQ 号查询历史绑定。 |

## 许可证

Apache-2.0。见 [LICENSE](LICENSE) 与 [第三方依赖和版权声明](THIRD_PARTY_NOTICES.md)。
