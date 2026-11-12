# 部署、升级与故障排查

## 运行目录

按[编译指南](build.md)选择目标系统的二进制。在源码仓库外准备运行目录，放入可执行文件和仓库根目录的 [config.example.yaml](../config.example.yaml)。运行后的目录结构如下，Linux 可执行文件名为 `hashinex`。

```text
hashinex.exe
config.example.yaml
config/
  config.yaml
  config.yaml.secrets.json
data/
  hashinex.sqlite
  hashinex.jsonl
  tempfiles/
```

默认配置的位置以可执行文件为准，启动时可用 `-config` 指定其他 YAML。相对数据库和日志路径以 YAML 所在目录为准。默认数据目录在启动时自动创建。WebUI 保存密钥后生成 `config.yaml.secrets.json`，YAML 中保存密钥引用。配置、身份数据库、日志及其备份都应保留在运行目录或独立存储。

`data/tempfiles/` 在启用临时分享后创建。公网代理指向分享服务的独立端口。 [临时分享部署](tempfiles.md)

## 首次启动

在运行目录中执行。保留已有的 `config/config.yaml`。

Windows（PowerShell）

```powershell
New-Item -ItemType Directory -Force -Path config | Out-Null
if (!(Test-Path -LiteralPath "config/config.yaml")) {
    Copy-Item -LiteralPath "config.example.yaml" -Destination "config/config.yaml"
}
./hashinex.exe -check
if ($LASTEXITCODE -eq 0) { ./hashinex.exe -admin-setup }
```

Linux（Shell）

```sh
chmod +x ./hashinex
mkdir -p config
test -f config/config.yaml || cp config.example.yaml config/config.yaml
./hashinex -check && ./hashinex -admin-setup
```

1. 打开 [本地 WebUI](http://127.0.0.1:6700/admin/)，首次设置可留空登录。在「系统设置 → 管理访问」设置管理令牌。
2. 在 WebUI 添加输入、输出和路由。分别在对应表单保存 QQ AppSecret、OneBot 令牌和 gscore 令牌。
3. 点击「保存配置」，再点击「重启网关」应用。日常启动直接运行可执行文件。WebUI 和 [健康检查](http://127.0.0.1:6700/healthz) 可用于查看连接状态。

## 升级与迁移

1. 正常停止网关，备份整个 `config/` 和 `data/`。SQLite 使用 WAL，目录中存在 `-wal` 和 `-shm` 文件时一并复制。运行中的数据库使用 SQLite 一致性备份。
2. 升级时替换可执行文件。迁移时将对应系统的可执行文件和这两个目录一起放到新运行目录。自定义外部路径、环境变量密钥和下游框架的数据库随各自部署迁移。
3. 运行 `hashinex -check` 校验配置和凭证引用，再启动并检查输入输出是否上线。SQLite 在启动时启用 WAL，并在事务中运行需要的迁移。

默认监听本机回环。跨设备连接使用受控网络或 TLS 反向代理，Webhook 代理只发布回调路径。控制台输出 JSON，`logging.file` 可启用文件日志，`max_size_bytes` 和 `keep` 控制轮转。

## 故障排查

| 现象 | 检查 |
| --- | --- |
| OneBot 拨入失败 | 核对路径、Bearer 令牌、Universal 角色和 `X-Self-ID`。每个端点只保留一个活动连接。 |
| 官方 Bot 无事件 | 核对 AppID、平台权限、Gateway READY 或 Webhook ACK，以及 IP 白名单。确认收到全量群消息还是仅 @。 |
| WebUI 拒绝保存 | 根据保存按钮附近的中文提示，检查点名的字段及实例。YAML 格式错误会给出可用的行号。 |
| 匹配没有进展 | 核对观察端的同文消息、时钟、群映射、头像和冻结状态。 |
| 官 Bot 代发失败 | 检查来源输入的静默群、路由的代发群号、官 Bot 主动发送权限及群白名单。在「身份映射」确认真实群号已关联到该官 Bot 的群 OpenID。 |
| 官 Bot 未生成分享链接 | 检查「临时分享」已启用并重启生效、总容量足够、目标群由官 Bot 发送。日志 `temporary share listening` 表示分享端口已启动。 [临时分享部署](tempfiles.md) |
| 多图或合并消息超过 WebSocket 上限 | 内置 gscore 使用 `limits.gscore_frame_bytes`，其他连接使用 `limits.frame_bytes`。在「系统设置 → 队列与超时」调整后重启。 |
| AiBot 回复走错 Bot | AiBot 的发送器只保存一个 Bot 实例。野收官发时让 AiBot 只连接来源输入，并在该路由选择发送 Bot。 [OneBot 路由配置](onebot.md#astrbot-与-aibot) |
| 回复返回 1404 | 根据具体错误检查动作支持、发送 Bot 的目标映射、消息上下文和引用索引。 |
| 回复返回 1409 | 提供明确的消息上下文，或缩小触发业务回复的事件范围。 |
| 回复返回 1504 | 上游超时，结果可能未知。先核对实际发送情况。 |
| 投递失败增加 | 查目标输出日志、队列与过期统计。慢连接会被隔离。WS 写入成功仅表示数据写入连接。 |

HTTP 请求头读取时限为 5 秒，整个 HTTP 请求读取时限为 15 秒。WebSocket 升级后按连接的帧大小、写入超时和心跳设置运行。QQ Webhook 先检查签名头和声明的请求长度，再读取限长的正文并验证签名。
