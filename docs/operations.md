# 部署与故障排查

## 目录与迁移

把可执行文件和运行目录放在源码仓库外。默认目录以可执行文件的位置为准，可以从任意工作目录启动。

```text
hashinex.exe
config/
  config.yaml
  config.yaml.secrets.json
data/
  hashinex.sqlite
  hashinex.jsonl
```

将仓库根目录的 [config.example.yaml](../config.example.yaml) 复制为 `config/config.yaml` 进行首次配置。密钥文件在 WebUI 保存密钥后生成。默认数据目录在启动时自动创建。显式配置的相对数据库和日志路径以 YAML 所在目录为准。源码构建可以把 `GOPATH`、`GOMODCACHE`、`GOCACHE`、`GOTMPDIR` 和临时目录设到独立缓存目录。

迁移时正常停止网关，复制整个 `config/` 和 `data/` 到目标机器的可执行文件旁，再执行 `hashinex -check` 和 `hashinex`。SQLite 使用 WAL，复制目录时一并带走存在的 `-wal` 和 `-shm` 文件。运行中的数据库应使用 SQLite 一致性备份工具。自定义外部路径、环境变量密钥和下游框架自身的数据库需随各自的部署一起迁移。

## 运行与故障排查

默认监听回环。跨设备连接使用受控网络或 TLS 反向代理。Webhook 代理仅发布回调路径，管理令牌和 OneBot 令牌分别设置。控制台输出 JSON，配置 `logging.file` 启用文件日志，可调节 `max_size_bytes` 和 `keep`。

HTTP 请求头读取时限为 5 秒，整个 HTTP 请求读取时限为 15 秒。WebSocket 升级后按连接的帧大小、写入超时和心跳设置运行。QQ Webhook 先检查签名头和声明的请求长度，再读取限长的正文并验证签名。

SQLite 启动时启用 WAL，并在事务中运行迁移。

| 现象 | 检查 |
| --- | --- |
| OneBot 拨入失败 | 核对路径、Bearer 令牌、Universal 角色和 `X-Self-ID`。每个端点只保留一个活动连接。 |
| 官方 Bot 无事件 | 核对 AppID、平台权限、Gateway READY 或 Webhook ACK，以及 IP 白名单。确认收到全量群消息还是仅 @。 |
| 匹配没有进展 | 核对观察端的同文消息、时钟、群映射、头像和冻结状态。 |
| 回复返回 1404 | 检查动作支持、目标反查、消息上下文和引用索引。 |
| 回复返回 1409 | 提供明确的消息上下文，或缩小触发业务回复的事件范围。 |
| 回复返回 1504 | 上游超时，结果可能未知。先核对实际发送情况。 |
| 投递失败增加 | 查目标输出日志、队列与过期统计。慢连接会被隔离。WS 写入成功仅表示数据写入连接。 |

CI 对 Linux amd64、Linux arm64 和 Windows amd64 执行编译检查，跨平台编译使用 `CGO_ENABLED=0`。
