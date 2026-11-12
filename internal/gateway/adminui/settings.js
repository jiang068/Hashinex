const choices = (...options) => ({ options });
const required = { required: true };
const list = { kind: "list" };
const realIDs = { ...list, realIDs: true, wide: true };
const flag = { kind: "boolean" };
const number = { kind: "number", required: true };
const integer = { pattern: "[0-9]+" };
const positiveID = { ...required, pattern: "[1-9][0-9]*" };
const isOfficial = draft => draft.protocol === "qq-official";
const onebot = draft => !isOfficial(draft);
const dials = draft => ["forward-dial", "reverse-dial"].includes(draft.ws_mode);
const core = draft => draft.protocol === "gscore";
const password = { kind: "password", wide: true };
const listener = { wide: true, placeholder: "/bot/onebot" };
const name = { ...required, pattern: "[a-zA-Z0-9][a-zA-Z0-9_\\-]{0,63}" };
const only = (field, when) => ({ ...field, when });

export const protocols = { "onebot11": "OneBot v11", "qq-official": "QQ 官方 Bot", "gscore": "gsuid_core" };
export const roles = { active: "业务 Bot", observe: "静默观察" };
export const modes = {
  "reverse-listen": "反向 WebSocket · 监听",
  "forward-dial": "正向 WebSocket · 拨号",
  "forward-listen": "正向 WebSocket · 监听",
  "reverse-dial": "反向 WebSocket · 拨号"
};
export const matchingModes = { off: "已关闭", shadow: "仅观察", auto: "自动绑定" };
const inputModes = ["reverse-listen", "forward-dial"];
const outputModes = ["forward-listen", "reverse-dial"];
const entries = dictionary => Object.entries(dictionary);

export const collections = {
  inputs: {
    label: "输入",
    initial: { protocol: "onebot11", role: "active", identity_source: "real-qq", ws_mode: "reverse-listen", events: "gateway" },
    fields: [
      ["id", "输入名称", name],
      ["protocol", "接入协议", choices(...entries(protocols).filter(([key]) => key !== "gscore"))],
      ["self_id", "OneBot self_id", positiveID],
      ["role", "默认群行为", choices(...entries(roles))],
      ["identity_source", "ID 来源", only(choices(["real-qq", "真实 QQ"], ["adapter-virtual", "适配器虚拟 ID"]), onebot)],
      ["ws_mode", "连接方式", only(choices(...inputModes.map(key => [key, modes[key]])), onebot)],
      ["url", "WebSocket 地址", only({ ...required, wide: true, placeholder: "ws://127.0.0.1:端口/路径" }, draft => onebot(draft) && dials(draft))],
      ["connection_url", "反向 WebSocket 连接地址", only({ wide: true, preview: true, hint: "把完整地址填入上游适配器的反向 WebSocket 地址栏。" }, draft => onebot(draft) && draft.ws_mode === "reverse-listen")],
      ["listen_path", "自定义连接路径（可选）", only({ wide: true, hint: "留空时按输入名称生成。对外主机与端口可在「系统设置 → 网关服务」修改。" }, draft => onebot(draft) && draft.ws_mode === "reverse-listen")],
      ["token_value", "OneBot 令牌", only(password, onebot)],
      ["app_id", "AppID", only(positiveID, isOfficial)],
      ["secret_value", "AppSecret", only({ ...password, mandatory: true }, isOfficial)],
      ["events", "接收事件", only(choices(["gateway", "官方 Gateway"], ["webhook", "签名 Webhook"]), isOfficial)],
      ["api_origin", "HTTP API", only(choices(["", "默认"], ["bot", "api.bot.qq.com"], ["sgroup", "api.sgroup.qq.com"]), isOfficial)],
      ["intents", "Gateway Intents", only({ ...integer, hint: "留空使用网关默认值。" }, draft => isOfficial(draft) && draft.events === "gateway")],
      ["group_ids", "群白名单", only({ ...realIDs, hint: "填写真实 QQ 群号，用逗号或空白分隔。留空允许全部群。" }, isOfficial)],
      ["enable_private", "启用私聊", only(flag, isOfficial)],
      ["private_qq_ids", "私聊白名单", only({ ...realIDs, hint: "填写真实 QQ 号。留空允许所有完成绑定的用户。" }, isOfficial)],
      ["allow_proactive", "允许主动发送", only(flag, isOfficial)],
      ["silent_groups", "静默群", { ...realIDs, hint: "填写真实群号。静默群只参与观察与身份绑定。" }],
      ["business_groups", "业务群", { ...realIDs, hint: "填写真实群号。同一个群出现在两个列表时，静默优先。" }],
      ["observed_profiles", "允许返回已观察的用户资料", only({ ...flag, wide: true }, isOfficial)]
    ],
    clean(draft) {
      const item = { ...draft };
      delete item.disable_private;
      delete item.group_openids;
      delete item.private_openids;
      if (isOfficial(item)) {
        for (const key of ["identity_source", "ws_mode", "url", "listen_path", "token_env", "token_ref", "token_value"]) delete item[key];
      } else {
        for (const key of ["app_id", "secret_env", "secret_ref", "secret_value", "events", "api_origin", "intents", "group_ids", "enable_private", "private_bind_group", "private_qq_ids", "allow_proactive", "observed_profiles"]) delete item[key];
        if (!dials(item)) delete item.url;
        else delete item.listen_path;
      }
      return item;
    }
  },
  outputs: {
    label: "输出",
    initial: { protocol: "onebot11", ws_mode: "forward-listen" },
    fields: [
      ["id", "输出名称", name],
      ["protocol", "输出类型", choices(["onebot11", "OneBot v11（含 AstrBot）"], ["gscore", "gsuid_core 直连"])],
      ["ws_mode", "连接方式", only(choices(...outputModes.map(key => [key, modes[key]])), draft => !core(draft))],
      ["url", "目标 WebSocket 地址", only({ ...required, wide: true, placeholder: "ws://127.0.0.1:8765/ws/hashinex", hint: "多只 Bot 共用 gscore 时，在各自路由里选择同一个输出。" }, draft => dials(draft) || core(draft))],
      ["listen_path", "监听路径", only({ ...listener, placeholder: "/astrbot/{input}", hint: "留空使用默认路径。多个来源共用时，使用 {input} 区分。" }, draft => !core(draft) && !dials(draft))],
      ["token_value", "连接令牌", password]
    ],
    clean(draft) {
      const item = { ...draft };
      if (core(item)) {
        if (/[{}]/.test(item.url)) throw new Error("每个 gscore 输出填写一个完整地址。");
        delete item.ws_mode;
        delete item.listen_path;
      } else if (dials(item)) delete item.listen_path;
      else delete item.url;
      return item;
    }
  },
  routes: {
    label: "路由",
    initial: { allow_actions: true },
    fields: [
      ["from", "来源输入", { ...required, source: "inputs" }],
      ["outputs", "目标输出", { ...required, source: "outputs", multiple: true, wide: true }],
      ["events", "允许转发的事件", { ...list, wide: true, hint: "可选 message、message_sent、notice、request。留空允许全部事件。" }],
      ["groups", "群号过滤", { ...realIDs, hint: "填写真实 QQ 群号。留空允许全部群。" }],
      ["include_private", "配置群号过滤时也转发私聊", flag],
      ["users", "用户过滤", { ...realIDs, hint: "填写真实 QQ 号。留空允许全部用户。" }],
      ["allow_actions", "允许下游 API 回程", flag],
      ["passthrough", "原样透传事件", { ...flag, wide: true, hint: "保留上游事件格式与 ID。QQ 官方原始事件需要下游自行解析，gscore 与 AstrBot 的 OneBot 接入通常使用转换模式。" }],
      ["implicit_reply", "隐式回复", choices(["", "关闭"], ["unique-recent", "使用唯一的最近消息"])]
    ],
    clean(draft) {
      const item = { ...draft };
      delete item.to;
      return item;
    }
  }
};

export const systemSettings = {
  gateway: {
    label: "网关服务", note: "监听地址、对外 WebSocket 地址与日志级别",
    fields: [["listen", "监听地址", required], ["log_level", "日志级别", choices("debug", "info", "warn", "error")],
      ["public_ws_url", "对外 WebSocket 地址", { wide: true, placeholder: "wss://bot.example.com", hint: "填 ws:// 或 wss:// 加主机与端口。留空使用当前访问控制台的地址。" }]]
  },
  admin: {
    label: "管理访问", note: "管理令牌与远程访问",
    fields: [["token_value", "管理令牌", { ...password, mandatory: true, hint: "至少 16 个非空白字符，重启后使用新令牌登录。" }],
      ["allow_remote", "允许从其他主机访问 WebUI", flag]]
  },
  storage: {
    label: "身份数据库", note: "SQLite、消息保留时间、隐式回复窗口",
    fields: [["sqlite", "SQLite 文件", { wide: true }], ["message_ttl", "消息保留时间", required], ["reply_window", "隐式回复窗口", required]]
  },
  matching: {
    label: "静默匹配", note: "匹配模式、证据阈值、头像相似度",
    fields: [
      ["mode", "匹配模式", choices(...entries(matchingModes))],
      ["pair_window", "同一消息配对窗口", required],
      ["event_ttl", "待配对事件保留时间", required],
      ["evidence_ttl", "证据保留时间", required],
      ["successes_required", "用户匹配证据数", number],
      ["group_successes_required", "群匹配证据数", number],
      ["group_senders_required", "群匹配不同发送者数", number],
      ["phash_max_distance", "最大 pHash 汉明距离", number],
      ["observation_inputs", "观察实例白名单", { ...list, wide: true, hint: "输入实例名称用逗号或空白分隔。留空使用所有观察实例。" }],
      ["default_avatar_phashes", "默认头像 pHash", { ...list, wide: true, hint: "每项填 16 位十六进制，使用逗号或空白分隔。" }]
    ]
  },
  limits: {
    label: "队列与超时", note: "WebSocket、事件队列、API 回程和去重",
    fields: [
      ["frame_bytes", "单帧字节上限", number],
      ["event_queue", "事件队列长度", number],
      ["api_queue", "API 队列长度", number],
      ["pending_per_peer", "单个对端的待处理 API 数", number],
      ["pending_per_input", "单个输入的待处理 API 数", number],
      ["dedup_size", "去重缓存条数", number],
      ["write_timeout", "写入超时", required],
      ["api_timeout", "API 超时", required],
      ["event_ttl", "待转发事件保留时间", required],
      ["dedup_ttl", "去重保留时间", required],
      ["ping_interval", "心跳间隔", required]
    ]
  },
  logging: {
    label: "文件日志", note: "日志文件、大小和轮转保留",
    fields: [["file", "文件路径", { wide: true }], ["max_size_bytes", "轮转文件大小（字节）", number], ["keep", "保留文件数", number]]
  }
};
