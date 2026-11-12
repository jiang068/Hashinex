import { collections, systemSettings, protocols, roles, modes, matchingModes } from "./settings.js";

const get = id => document.getElementById(id);
const nav = [...document.querySelectorAll("[data-page]")];
const number = new Intl.NumberFormat("zh-CN");
const clock = new Intl.DateTimeFormat("zh-CN", { timeStyle: "medium", hour12: false });
let token = "";
let page = "overview";
let live = { inputs: [], outputs: [], logs: [] };
let settings;
let settingsTag = "";
let yamlTag = "";
let yamlDirty = false;
let busy = false;
let restarting = false;
let restartAddress = null;
let edit;
let noticeTimer;
let refreshJob;
let candidateRequest = 0;
let identityRequest = 0;
let routeInput = "";
let routeOutput = "";
const identityPage = { page: 1, size: 20 };
const candidatePage = { page: 1, size: 20 };
const changed = new Set();
const samples = [];

function element(tag, className, ...children) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  node.append(...children.filter(child => child != null));
  return node;
}

function button(label, callback, className = "") {
  const node = element("button", className, label);
  node.type = "button";
  node.addEventListener("click", callback);
  return node;
}

function notice(message, error = false) {
  const node = get("status");
  node.textContent = message;
  node.toggleAttribute("data-error", error);
  node.hidden = !message;
  clearTimeout(noticeTimer);
  if (message && !error) noticeTimer = setTimeout(() => { node.hidden = true; }, 5000);
}

function setSaveError(id, message = "") {
  const node = get(id);
  node.textContent = message;
  node.hidden = !message;
}

function attempt(operation) {
  return Promise.resolve().then(operation).catch(error => notice(error.message, true));
}

async function request(path, options = {}) {
  let response;
  try {
    response = await fetch(path, {
      ...options,
      cache: "no-store",
      headers: { Authorization: "Bearer " + token, "X-Hashinex-Admin": "1", ...options.headers }
    });
  } catch {
    throw new Error("无法连接网关。请检查网关是否运行，以及当前页面能否访问它。");
  }
  if (response.ok) return response;
  if (response.status === 401) throw Object.assign(new Error("管理令牌无效，请重新登录。"), { status: 401 });
  if (response.status === 412) throw new Error("配置已被其他页面或进程修改。请重新读取后再保存。");
  const detail = response.headers.get("Content-Type")?.startsWith("text/plain") ? (await response.text()).trim() : "";
  throw new Error(detail || "网关请求失败（HTTP " + response.status + "）。请检查网关状态后重试。");
}

const badge = (label, style = "") => element("span", "badge " + style, label);
const code = value => element("code", "mono", String(value ?? "—"));
const linkState = online => online ? badge("在线", "green") : badge("离线");
const role = value => roles[value || "active"] || value;
const kind = value => value === "group" ? "群" : "用户";
const actions = (...buttons) => element("div", "actions", ...buttons);
const count = value => number.format(value ?? 0);
const aliasName = namespace => {
  const input = live.inputs.find(item => item.namespace === namespace || item.private_namespace === namespace);
  if (!input) return namespace;
  if (!input.private_namespace) return input.id;
  return input.id + (input.private_namespace === namespace ? " · 私聊" : " · 群聊");
};

function table(id, columns, rows, emptyText) {
  const node = get(id);
  node.replaceChildren();
  const header = node.createTHead().insertRow();
  columns.forEach(label => {
    const th = element("th", "", label);
    th.scope = "col";
    header.append(th);
  });
  const body = node.createTBody();
  if (rows.length === 0) {
    const cell = body.insertRow().insertCell();
    cell.colSpan = columns.length;
    cell.className = "empty";
    cell.textContent = emptyText;
    return;
  }
  for (const values of rows) {
    const row = body.insertRow();
    values.forEach(value => row.insertCell().append(value instanceof Node ? value : String(value ?? "—")));
  }
}

function metric(label, value, total, detail) {
  return element("article", "metric",
    element("span", "metric-label", label),
    element("div", "metric-number", String(value), total == null ? null : element("small", "", " / " + total)),
    element("small", "metric-detail", detail));
}

function drawTraffic() {
  const canvas = get("traffic");
  if (!canvas.clientWidth || !samples.length) return;
  const width = canvas.clientWidth;
  const height = canvas.clientHeight;
  const scale = window.devicePixelRatio || 1;
  canvas.width = width * scale;
  canvas.height = height * scale;
  const ctx = canvas.getContext("2d");
  ctx.scale(scale, scale);
  const bounds = { left: 38, right: width - 12, top: 12, bottom: height - 28 };
  const step = Math.max(1, Math.ceil(Math.max(...samples.map(sample => sample.rate)) / 4));
  ctx.font = '11px Consolas, monospace';
  ctx.fillStyle = "#52645f";
  ctx.strokeStyle = "#e1ebe4";
  for (let i = 0; i <= 4; i++) {
    const y = bounds.bottom - (bounds.bottom - bounds.top) * i / 4;
    ctx.beginPath();
    ctx.moveTo(bounds.left, y);
    ctx.lineTo(bounds.right, y);
    ctx.stroke();
    ctx.fillText(String(i * step), 1, y + 3);
  }
  const points = samples.map((sample, index) => [
    bounds.right - (bounds.right - bounds.left) * (samples.length - 1 - index) / 29,
    bounds.bottom - (bounds.bottom - bounds.top) * sample.rate / (4 * step)
  ]);
  const [firstX, firstY] = points[0];
  const [lastX, lastY] = points.at(-1);
  const area = ctx.createLinearGradient(0, bounds.top, 0, bounds.bottom);
  area.addColorStop(0, "#77c7a575");
  area.addColorStop(1, "#dff3e96e");
  ctx.beginPath();
  ctx.moveTo(firstX, firstY);
  points.forEach(([x, y]) => ctx.lineTo(x, y));
  ctx.lineTo(lastX, bounds.bottom);
  ctx.lineTo(firstX, bounds.bottom);
  ctx.closePath();
  ctx.fillStyle = area;
  ctx.fill();
  ctx.beginPath();
  ctx.moveTo(firstX, firstY);
  points.forEach(([x, y]) => ctx.lineTo(x, y));
  ctx.strokeStyle = "#218962";
  ctx.lineWidth = 2;
  ctx.stroke();
  ctx.beginPath();
  ctx.arc(lastX, lastY, 3, 0, Math.PI * 2);
  ctx.fillStyle = "#218962";
  ctx.fill();
  ctx.fillStyle = "#52645f";
  ctx.fillText("−90s", bounds.left, height - 7);
  ctx.textAlign = "right";
  ctx.fillText("现在", bounds.right, height - 7);
  get("sample-range").textContent = "最近 90 秒";
  canvas.setAttribute("aria-label", "最近 90 秒消息流量。当前每秒 " + samples.at(-1).rate + " 条。");
}

function renderOverview(health) {
  const outputs = new Map();
  for (const item of live.outputs) {
    const key = item.protocol === "gscore" ? item.id : item.id + "\u0000" + item.input;
    const entry = outputs.get(key) || { id: item.id, inputs: [], online: false, writable: false };
    entry.inputs.push(item.input);
    entry.online ||= item.online;
    entry.writable ||= item.writable;
    outputs.set(key, entry);
  }
  get("metrics").replaceChildren(
    metric("在线输入", count(health.online_inputs), live.inputs.length, "当前接入的 Bot 实例"),
    metric("在线输出", count(health.online_outputs), outputs.size, "当前可用的后端连接"),
    metric("接收消息", count(health.received), null, "每秒 " + count(health.messages_per_second) + " 条"),
    metric("平均路由耗时", health.routing_latency_avg_ms == null ? "—" : health.routing_latency_avg_ms.toFixed(1) + " ms", null,
      "API 超时 " + count(health.api_timeouts) + " · 未投递 " + count(health.dropped))
  );
  const matching = get("matching-mode");
  matching.textContent = matchingModes[live.matching] || "—";
  matching.classList.toggle("green", live.matching === "auto");
  const stats = [
    ["自动绑定", count(health.auto_bindings)],
    ["有效证据", count(health.match_evidence)],
    ["身份冲突", count(health.match_conflicts)],
    ["头像未通过", count(health.avatar_rejected)],
    ["pHash 距离上限", live.avatar_distance ?? "—"]
  ];
  get("matching-metrics").replaceChildren(...stats.map(([name, value]) =>
    element("div", "", element("dt", "", name), element("dd", "", String(value)))));
  table("connections-table", ["实例", "方向", "协议 / 会话", "连接状态", "队列 / API"],
    [...live.inputs.map(item => [code(item.id), "输入", protocols[item.protocol], linkState(item.online), count(item.queued) + " 个事件"]),
     ...[...outputs.values()].map(item => [code(item.id), "输出", "来源 " + item.inputs.join("、"), linkState(item.online), item.writable ? "可回程" : "仅转发"])],
    "暂无连接。可以从输入管理添加 Bot。");
  drawTraffic();
}

function refresh() {
  if (refreshJob) return refreshJob;
  refreshJob = (async () => {
    const [stateResponse, healthResponse] = await Promise.all([request("api/state"), fetch("../healthz", { cache: "no-store" })]);
    if (!healthResponse.ok) throw new Error("网关状态读取失败。");
    const [state, health] = await Promise.all([stateResponse.json(), healthResponse.json()]);
    live = state;
    const now = Date.now();
    if (!samples.length || now - samples.at(-1).time >= 2400) {
      samples.push({ time: now, rate: health.messages_per_second || 0 });
    }
    while (samples.length > 30 || now - samples[0].time > 90000) samples.shift();
    renderOverview(health);
    get("refresh-time").textContent = clock.format(new Date());
    get("gateway-status").textContent = "网关运行中";
    get("gateway-status").parentElement.removeAttribute("data-error");
    renderRestart();
    if (page === "logs") renderLogs();
    if (settings && ["inputs", "outputs", "routes"].includes(page) && !get("edit-dialog").open &&
        !get(page).contains(document.activeElement)) renderCollection(page);
  })().catch(error => {
    get("gateway-status").textContent = "连接中断";
    get("gateway-status").parentElement.setAttribute("data-error", "");
    throw error;
  }).finally(() => { refreshJob = null; });
  return refreshJob;
}

function visiblePage(next) {
  const current = nav.find(item => item.dataset.page === next);
  if (!current) return;
  page = next;
  for (const item of nav) {
    const active = item === current;
    get(item.dataset.page).hidden = !active;
    if (active) item.setAttribute("aria-current", "page");
    else item.removeAttribute("aria-current");
  }
  get("breadcrumb").textContent = current.lastChild.textContent.trim();
  document.title = "Hashinex · " + get("breadcrumb").textContent;
}

async function loadPage() {
  if (page === "overview") drawTraffic();
  if (page === "identities") await renderIdentities();
  if (page === "candidates") await loadCandidates();
  if (page === "logs") renderLogs();
  if (["inputs", "outputs", "routes", "settings"].includes(page)) {
    await ensureSettings();
    renderSettings();
  }
}

function navigate(next) {
  if (next === page) return;
  history.replaceState(null, "", "#" + next);
  visiblePage(next);
  attempt(loadPage);
}

function renderLogs() {
  const filter = get("log-filter").value.trim().toLowerCase();
  const level = get("log-level").value;
  const rows = [];
  for (const line of [...(live.logs || [])].reverse()) {
    if (!line.toLowerCase().includes(filter)) continue;
    let fields;
    try { fields = JSON.parse(line); } catch { fields = { msg: line }; }
    if (level && fields.level !== level) continue;
    const { time, level: severity, msg, ...rest } = fields;
    const summary = Object.entries(rest).map(([key, value]) =>
      key + "=" + (typeof value === "object" ? JSON.stringify(value) : value)).join("  ");
    const date = time ? new Date(time) : null;
    const timeNode = element("time", "log-time mono", date && !Number.isNaN(date.getTime()) ? clock.format(date) : "—");
    if (time) timeNode.title = time;
    rows.push([timeNode, badge(severity || "—", severity === "ERROR" ? "red" : severity === "WARN" ? "amber" : "green"),
      element("div", "", msg ?? line, summary ? element("small", "log-details mono", summary) : null)]);
  }
  get("log-count").textContent = rows.length + " 条日志";
  table("log-list", ["时间", "级别", "事件"], rows, "没有符合条件的日志。");
}

function pager(id, state, result, load) {
  state.page = result.page;
  const pages = Math.max(1, Math.ceil(result.total / result.size));
  const size = element("select");
  size.setAttribute("aria-label", "每页条数");
  for (const value of [10, 20, 50, 100, 200]) {
    const option = element("option", "", value + " 条 / 页");
    option.value = value;
    size.append(option);
  }
  size.value = result.size;
  size.addEventListener("change", () => {
    state.page = 1;
    state.size = Number(size.value);
    attempt(load);
  });
  const previous = button("上一页", () => { state.page--; attempt(load); });
  const next = button("下一页", () => { state.page++; attempt(load); });
  previous.disabled = result.page <= 1;
  next.disabled = result.page >= pages;
  get(id).replaceChildren(element("span", "muted", "共 " + count(result.total) + " 条"),
    size, previous, element("span", "mono", result.page + " / " + pages), next);
}

function sourceGroups(sources) {
  const groups = new Map(sources.map(item => [item.real_id || item.native, item]));
  if (!groups.size) return "—";
  return element("div", "source-groups", ...[...groups.values()].map(item =>
    item.real_id ? code(item.real_id) : element("div", "", code(item.native), element("small", "log-details", "群号待确认"))));
}

async function renderIdentities() {
  const query = new URLSearchParams({ q: get("identity-query").value.trim(), kind: get("identity-kind").value,
    view: "entities", ...identityPage });
  const requestID = ++identityRequest;
  const result = await (await request("api/identities?" + query)).json();
  if (requestID !== identityRequest) return;
  table("identity-list", ["QQ 号 / 群号", "类型", "来源群", "关联身份", "操作"], result.rows.map(row => {
    const aliases = row.aliases.map(alias => element("div", "identity-alias",
      element("div", "", aliasName(alias.namespace), element("small", "log-details mono", alias.native),
        element("small", "log-details", alias.virtual_id ? alias.namespace + " · 虚拟 ID " + alias.virtual_id : alias.namespace)),
      button(row.bound ? "调整绑定" : "绑定", () => openBinding(alias))));
    const details = element("details", "identity-details",
      element("summary", "", row.aliases.length + " 个关联身份"), ...aliases);
    details.open = !row.bound || result.rows.length === 1;
    const sources = row.kind === "group"
      ? row.aliases.map(alias => ({ native: alias.native, real_id: row.bound ? row.id : 0 }))
      : row.aliases.flatMap(alias => alias.sources || []);
    return [element("div", "", code(row.id), row.bound ? null : element("small", "log-details", "未绑定 · 虚拟 ID")),
      kind(row.kind), sourceGroups(sources), details, row.bound ? button("添加关联", () => openBinding({ kind: row.kind, real_id: row.id })) : "—"];
  }), "没有找到符合条件的映射。");
  pager("identity-pager", identityPage, result, renderIdentities);
}

function renderCandidates(rows) {
  const summaries = [
    matchingModes[live.matching] || "—",
    "用户 " + live.user_threshold + " 条证据",
    "群 " + live.group_threshold + " 条证据 / " + live.group_sender_threshold + " 位发送者"
  ];
  get("candidate-summary").textContent = summaries.join(" · ");
  table("candidate-list", ["命名空间", "类型 / 原始 ID", "候选真实 ID", "来源群", "证据进度", "头像距离", "状态", "操作"],
    rows.map(row => {
      const threshold = (row.kind === "group" ? live.group_threshold : live.user_threshold) || 1;
      const progress = element("progress");
      progress.max = threshold;
      progress.value = row.count;
      const label = row.count + " / " + threshold + (row.kind === "group" ? " · " + row.senders + " 人" : "");
      progress.setAttribute("aria-label", label);
      const status = row.frozen ? badge("冲突冻结", "red") : row.bound ? badge("已绑定", "green") : badge("待确认", "amber");
      return [code(row.namespace), element("div", "", kind(row.kind), element("small", "log-details mono", row.native)),
        code(row.real_id), sourceGroups(row.kind === "group" ? [{ real_id: row.real_id, native: row.native }] : row.sources || []),
        element("div", "progress", label, progress), row.distance ?? "—", status,
        button(row.frozen ? "处理冲突" : row.bound ? "调整绑定" : "确认绑定", () => openBinding(row))];
    }), "没有符合条件的匹配证据。");
}

async function loadCandidates() {
  const requestID = ++candidateRequest;
  const query = new URLSearchParams({ q: get("candidate-query").value.trim(), status: get("candidate-filter").value, ...candidatePage });
  const result = await (await request("api/candidates?" + query)).json();
  if (requestID !== candidateRequest) return;
  renderCandidates(result.rows);
  pager("candidate-pager", candidatePage, result, loadCandidates);
}

let settingsJob;

function ensureSettings() {
  if (settings) return Promise.resolve();
  if (!settingsJob) {
    settingsJob = (async () => {
      const response = await request("api/settings");
      const document = await response.json();
      for (const key of Object.keys(collections)) document[key] ??= [];
      settings = document;
      settingsTag = response.headers.get("ETag");
    })().finally(() => { settingsJob = null; });
  }
  return settingsJob;
}

function editControls(key, index) {
  return actions(button("编辑", () => openCollection(key, index)),
    button("删除", () => removeEntry(key, index), "danger"));
}

const targets = route => route.outputs || (route.to ? [route.to] : []);
const isCore = output => output.protocol === "gscore";

function listenerURL(listen) {
  const separator = listen.lastIndexOf(":");
  if (separator < 0) return null;
  const host = listen.slice(0, separator);
  const address = ["", "0.0.0.0", "[::]"].includes(host) ? location.hostname : host;
  try {
    return new URL("http://" + address + ":" + listen.slice(separator + 1) + "/admin/");
  } catch {
    return null;
  }
}

function wsOrigin() {
  if (settings.public_ws_url) return settings.public_ws_url.replace(/\/$/, "");
  if (live.listen && settings.listen !== live.listen && location.protocol === "http:" &&
      listenerURL(live.listen)?.port === location.port) {
    const next = listenerURL(settings.listen);
    if (next) return next.origin.replace(/^http/, "ws");
  }
  return location.origin.replace(/^http/, "ws");
}

function reverseAddress(item) {
  const name = item.id?.trim();
  const custom = item.listen_path?.trim();
  if (custom && !custom.startsWith("/")) return "";
  const path = custom || (name ? "/onebot/in/" + name : "");
  return path ? wsOrigin() + path : "";
}

function outputAddress(output, input) {
  if (output.url) return output.url;
  const path = output.listen_path || "/onebot/out/" + output.id + "/{input}";
  return wsOrigin() + path.replaceAll("{input}", input);
}

function showRoutes(input = "", output = "") {
  routeInput = input;
  routeOutput = output;
  navigate("routes");
}

function routeFilter(id, entries, selected) {
  const select = get(id);
  const options = [["", select.dataset.empty], ...entries.map(item => [item.id, item.id])];
  select.replaceChildren(...options.map(([value, label]) => {
    const option = element("option", "", label);
    option.value = value;
    return option;
  }));
  select.value = entries.some(item => item.id === selected) ? selected : "";
  return select.value;
}

function displayAddress(address) {
  return element("div", "connection-address", code(address), navigator.clipboard && /^(https?|wss?):/.test(address)
    ? button("复制", () => attempt(async () => { await navigator.clipboard.writeText(address); notice("地址已复制。"); }), "text-button") : null);
}

function renderCollection(key) {
  const entries = settings[key];
  if (key === "inputs") {
    const current = new Map(live.inputs.map(item => [item.id, item]));
    table("input-list", ["输入", "协议 / 默认行为", "接入地址", "业务去向", "状态", "操作"], entries.map((item, index) => {
      const official = item.protocol === "qq-official";
      const endpoint = official ? (item.events === "webhook" ? location.origin + "/qq/webhook/" + item.id : "QQ Gateway") :
        (item.url || reverseAddress(item));
      const outputs = [...new Set(settings.routes.filter(route => route.from === item.id).flatMap(targets))];
      const sendRoutes = settings.routes.filter(route => route.send_group_via === item.id).length;
      return [
        element("div", "", code(item.id), element("small", "log-details mono", "self_id " + item.self_id)),
        element("div", "", protocols[item.protocol], element("small", "log-details", role(item.role)),
          item.silent_groups?.length || item.business_groups?.length ? element("small", "log-details",
            (item.silent_groups?.length || 0) + " 个静默群 · " + (item.business_groups?.length || 0) + " 个业务群") : null),
        element("div", "", displayAddress(endpoint), element("small", "log-details", official ? item.events : modes[item.ws_mode])),
        element("div", "", outputs.length ? button(outputs.join("、"), () => showRoutes(item.id), "text-button") : null,
          sendRoutes ? element("small", "log-details", "为 " + sendRoutes + " 条路由发送群消息") : null,
          !outputs.length && !sendRoutes ? "无业务路由" : null),
        current.has(item.id) ? linkState(current.get(item.id).online) : badge("待应用", "amber"), editControls(key, index)
      ];
    }), "暂无输入。添加一个 QQ 官方 Bot 或 OneBot 适配器。");
  }
  if (key === "outputs") {
    table("output-list", ["输出", "WebSocket 地址", "来源输入", "连接状态", "操作"], entries.map((item, index) => {
      const inputs = [...new Set(settings.routes.filter(route => targets(route).includes(item.id)).map(route => route.from))];
      const online = inputs.filter(input => live.outputs.some(session => session.id === item.id && session.input === input && session.online)).length;
      const addresses = item.url ? [displayAddress(item.url)] : inputs.length ? inputs.map(input => element("div", "endpoint-row",
        code(input), displayAddress(outputAddress(item, input)))) :
        [element("small", "muted", "添加路由后显示接入地址")];
      const status = !inputs.length ? badge("未接入路由") : isCore(item) ? (online === inputs.length ? linkState(true) : badge(online ? "正在接入" : "离线")) :
        badge(online + " / " + inputs.length, online ? "green" : "");
      return [element("div", "", code(item.id), element("small", "log-details", isCore(item) ? "gsuid_core 直连" : modes[item.ws_mode])),
        element("div", "endpoint-list", ...addresses),
        inputs.length ? element("div", "", button(inputs.join("、"), () => showRoutes("", item.id), "text-button"),
          isCore(item) && inputs.length > 1 ? element("small", "log-details", "共用 1 条连接") : null) : "—",
        status,
        editControls(key, index)];
    }), "暂无输出。添加 OneBot 后端后即可配置路由。");
  }
  if (key === "routes") {
    routeInput = routeFilter("route-from-filter", settings.inputs, routeInput);
    routeOutput = routeFilter("route-to-filter", settings.outputs, routeOutput);
    const routeState = new Map(live.outputs.map(output => [output.input + "\u0000" + output.id, output]));
    const rows = entries.flatMap((item, index) => {
      if ((routeInput && routeInput !== item.from) || (routeOutput && !targets(item).includes(routeOutput))) return [];
      const filters = [item.groups?.length && item.groups.length + " 个群", item.users?.length && item.users.length + " 个用户"].filter(Boolean);
      const outputs = targets(item).map(target => {
        const session = routeState.get(item.from + "\u0000" + target);
        return element("div", "actions", code(target), session ? linkState(session.online) : badge("待应用", "amber"));
      });
      const path = element("div", "route-path", code(item.from), element("span", "", "→"), element("div", "endpoint-list", ...outputs));
      const sendGroups = item.send_group_via_groups?.length;
      const actions = element("div", "", badge(item.allow_actions ? "已允许" : "已关闭", item.allow_actions ? "green" : ""),
        item.send_group_via ? element("small", "log-details", (sendGroups ? sendGroups + " 个群" : "全部路由群") + "由 ", code(item.send_group_via), " 发送") : null);
      return [[path, item.events?.join("、") || "全部事件", filters.join(" · ") || "无",
        badge(item.passthrough ? "原样透传" : "身份转换"),
        actions,
        editControls(key, index)]];
    });
    table("route-list", ["输入 → 输出", "事件", "过滤", "转发方式", "API 回程", "操作"], rows, "没有符合条件的路由。");
  }
}

function renderSettings() {
  if (["inputs", "outputs", "routes"].includes(page)) renderCollection(page);
  if (page === "settings") {
    get("settings-list").replaceChildren(...Object.entries(systemSettings).map(([key, section]) =>
      element("article", "panel", element("div", "panel-heading",
        element("div", "", element("h2", "", section.label), element("p", "", section.note)),
        button("编辑", () => openSystem(key))))));
  }
  get("draft-bar").hidden = !changed.size;
  get("draft-description").textContent = changed.size + " 项配置已加入草稿";
  get("save").disabled = busy;
  get("discard").disabled = busy;
  if (changed.size) get("yaml-panel").open = false;
  renderRestart();
}

function renderRestart() {
  const restart = get("restart");
  const pending = restarting || live.restarting;
  restart.hidden = !live.restart_available;
  restart.disabled = busy || pending || Boolean(restartAddress);
  restart.textContent = pending ? "正在重启…" : "重启网关";
  const banner = get("restart-note");
  banner.toggleAttribute("data-error", Boolean(live.restart_error));
  if (live.restart_error) {
    restartAddress = null;
    restart.disabled = busy || pending;
    banner.textContent = live.restart_error;
  } else if (restartAddress) {
    const link = element("a", "", restartAddress.href);
    link.href = restartAddress.href;
    banner.replaceChildren("网关将切换监听地址。请通过新地址打开控制台 ", link);
  } else if (pending) {
    banner.textContent = "正在重启网关并应用已保存的配置。";
  } else {
    banner.textContent = live.restart_needed ? "配置已保存，点击「重启网关」应用。" : "";
  }
  banner.hidden = !banner.textContent;
}

function markChanged(...keys) {
  keys.forEach(key => changed.add(key));
  renderSettings();
}

function allowFormEdit() {
  if (busy) return false;
  if (yamlDirty) {
    notice("高级 YAML 中有未保存的更改，请先保存或重新读取。", true);
    return false;
  }
  return true;
}

function removeEntry(key, index) {
  if (!allowFormEdit()) return;
  const item = settings[key][index];
  if (key === "inputs" && settings.routes.some(route => route.send_group_via === item.id)) {
    notice("该输入负责路由的群消息发送。请先在路由中更换发送 Bot。", true);
    return;
  }
  const related = key === "inputs" ? settings.routes.filter(route => route.from === item.id) :
    key === "outputs" ? settings.routes.filter(route => targets(route).includes(item.id)) : [];
  const observeList = settings.matching.observation_inputs || [];
  const observed = key === "inputs" && observeList.includes(item.id);
  const label = key === "routes" ? item.from + " → " + targets(item).join("、") : item.id;
  const detail = (related.length ? " 同时更新 " + related.length + " 条关联路由。" : "") +
    (observed ? " 同时从观察实例白名单移除该输入。" : "");
  if (!confirm("将「" + label + "」的删除加入配置草稿？" + detail)) return;
  settings[key].splice(index, 1);
  const keys = [key];
  if (related.length) {
    settings.routes = settings.routes.flatMap(route => {
      if (!related.includes(route)) return [route];
      if (key === "inputs") return [];
      const outputs = targets(route).filter(target => target !== item.id);
      if (!outputs.length) return [];
      const updated = { ...route, outputs };
      delete updated.to;
      return [updated];
    });
    keys.push("routes");
  }
  if (observed) {
    settings.matching.observation_inputs = observeList.filter(id => id !== item.id);
    keys.push("matching");
  }
  markChanged(...keys);
}

function showEdit(title, description, fields, values, onSubmit, submitLabel) {
  get("edit-title").textContent = title;
  get("edit-description").textContent = description;
  get("edit-error").hidden = true;
  get("apply-edit").textContent = submitLabel;
  get("apply-edit").disabled = false;
  const controls = fields.map(([key, label, spec]) => {
    let input, clear, copy, hint = spec.hint;
    if (spec.multiple) {
      input = element("fieldset", "target-options", element("legend", "", label));
      for (const item of settings[spec.source]) {
        const checkbox = element("input");
        checkbox.type = "checkbox";
        checkbox.value = item.id;
        checkbox.checked = (values[key] || []).includes(item.id);
        input.append(element("label", "", checkbox, item.id));
      }
      if (!settings[spec.source].length) input.append(element("small", "", "先在输出管理中添加输出。"));
    } else if (spec.options || spec.source) {
      const options = typeof spec.options === "function" ? spec.options(settings) :
        spec.options || [["", "请选择"], ...settings[spec.source].map(item => [item.id, item.id])];
      input = element("select");
      for (const option of options) {
        const [value, text] = Array.isArray(option) ? option : [option, option];
        const node = element("option", "", text);
        node.value = value;
        input.append(node);
      }
      input.value = String(values[key] ?? input.options[0]?.value ?? "");
    } else {
      input = element("input");
      input.type = spec.kind === "number" ? "number" : spec.kind === "boolean" ? "checkbox" : spec.kind === "password" ? "password" : "text";
      if (spec.kind === "boolean") input.checked = Boolean(values[key]);
      else if (spec.kind === "password") {
        const base = key.replace(/_value$/, "");
        const configured = values[base + "_ref"] || values[base + "_env"];
        input.autocomplete = "new-password";
        input.placeholder = values[key] ? "已录入草稿" : configured ? "已设置，留空保留" : "未设置";
        if (!spec.mandatory && (configured || values[key])) {
          clear = element("input");
          clear.type = "checkbox";
          clear.checked = values[key] === null;
          clear.addEventListener("change", () => { if (clear.checked) input.value = ""; });
          input.addEventListener("input", () => { if (input.value) clear.checked = false; });
        }
      }
      else input.value = Array.isArray(values[key]) ? values[key].join(", ") : values[key] ?? "";
      if (spec.kind === "number") input.step = "1";
      if (spec.pattern) input.pattern = spec.pattern;
      if (spec.placeholder) input.placeholder = spec.placeholder;
      if (spec.preview) {
        input.readOnly = true;
        input.placeholder = "填写输入名称后生成连接地址";
        copy = button(navigator.clipboard ? "复制地址" : "选中地址", () => attempt(async () => {
          if (navigator.clipboard) {
            await navigator.clipboard.writeText(input.value);
            notice("地址已复制。");
          } else {
            input.focus();
            input.select();
          }
        }));
      }
    }
    input.name = key;
    if (!spec.multiple) input.required = Boolean(spec.required);
    const contents = spec.multiple ? input : element("label", "field" + (spec.kind === "boolean" ? " check" : ""),
      ...(spec.kind === "boolean" ? [input, label] : [element("span", "", label),
        copy ? element("span", "connection-preview", input, copy) : input]),
      hint ? element("small", "", hint) : null);
    const field = element("div", spec.wide ? "wide" : "", contents,
      clear ? element("label", "clear-secret", clear, "清除令牌") : null);
    return { key, label, input, field, spec, clear, copy };
  });
  const viewValues = () => Object.fromEntries(controls.map(({ key, input }) => [key, input.value]));
  function visibility() {
    const values = viewValues();
    for (const { input, field, spec, copy } of controls) {
      const hidden = Boolean(spec.when && !spec.when(values));
      field.hidden = hidden;
      input.disabled = hidden;
      if (spec.preview) {
        input.value = hidden ? "" : reverseAddress(values);
        copy.disabled = !input.value;
      }
    }
  }
  get("edit-fields").replaceChildren(...controls.map(({ field }) => field));
  controls.forEach(({ input }) => {
    input.addEventListener("change", visibility);
    input.addEventListener("input", visibility);
  });
  visibility();
  edit = async () => {
    const updated = { ...values };
    for (const { key, label, input, spec, clear } of controls) {
      if (input.disabled || spec.preview) continue;
      const value = input.value?.trim() || "";
      if (spec.multiple) {
        updated[key] = [...input.querySelectorAll("input:checked")].map(option => option.value);
        if (spec.required && !updated[key].length) throw new Error("请选择至少一个目标输出。");
      } else if (spec.kind === "password") {
        const base = key.replace(/_value$/, "");
        if (clear?.checked) updated[key] = null;
        else if (input.value) updated[key] = input.value;
        else if (updated[key] === null) delete updated[key];
        if (spec.mandatory && !updated[key] && !updated[base + "_ref"] && !updated[base + "_env"]) {
          throw new Error("请填写「" + label + "」。");
        }
      } else if (spec.kind === "boolean") updated[key] = input.checked;
      else if (spec.kind === "number") updated[key] = Number(value);
      else if (spec.kind === "list") {
        updated[key] = value.split(/[\s,，]+/).filter(Boolean);
        if (spec.realIDs) {
          const invalid = updated[key].findIndex(id => !/^[1-9][0-9]{0,13}$/.test(id));
          if (invalid !== -1) throw new Error("「" + label + "」第 " + (invalid + 1) + " 项无效。请填写真实 QQ 号或群号。");
        }
      }
      else if (value || key === "real_id") updated[key] = value;
      else delete updated[key];
    }
    await onSubmit(updated);
  };
  get("edit-dialog").showModal();
}

function openCollection(key, index = -1) {
  if (!allowFormEdit()) return;
  const collection = collections[key];
  const prior = settings[key][index];
  const initial = { ...collection.initial, ...prior };
  if (key === "routes") {
    initial.outputs = prior ? targets(initial) : routeOutput ? [routeOutput] : [];
    if (!prior && routeInput) initial.from = routeInput;
  }
  if (key === "inputs" && !initial.role) initial.role = "active";
  if (key === "inputs" && initial.protocol === "qq-official" && !initial.ws_mode) initial.ws_mode = "reverse-listen";
  showEdit((prior ? "编辑" : "新增") + collection.label, "更改加入草稿后，可与其他配置一起保存。", collection.fields, initial, draft => {
    const item = collection.clean(draft, settings);
    const conflict = settings[key].find((other, i) => i !== index && (key === "routes"
      ? other.from === item.from && targets(other).some(target => targets(item).includes(target)) : other.id === item.id));
    if (conflict) {
      if (key === "routes") {
        const target = targets(item).find(output => targets(conflict).includes(output));
        throw new Error("已有从「" + item.from + "」到「" + target + "」的路由。请编辑已有路由，或选择其他输出。");
      }
      throw new Error(collection.label + "名称「" + item.id + "」已存在。请填写不同的名称。");
    }
    const keys = [key];
    if (prior) {
      settings[key][index] = item;
      if (prior.id !== item.id && ["inputs", "outputs"].includes(key)) {
        const affected = route => key === "inputs" ? route.from === prior.id || route.send_group_via === prior.id :
          targets(route).includes(prior.id);
        if (settings.routes.some(affected)) {
          settings.routes = settings.routes.map(route => {
            if (!affected(route)) return route;
            if (key === "inputs") {
              const updated = { ...route };
              if (updated.from === prior.id) updated.from = item.id;
              if (updated.send_group_via === prior.id) updated.send_group_via = item.id;
              return updated;
            }
            const updated = { ...route, outputs: targets(route).map(target => target === prior.id ? item.id : target) };
            delete updated.to;
            return updated;
          });
          keys.push("routes");
        }
        if (key === "inputs" && settings.matching.observation_inputs?.includes(prior.id)) {
          settings.matching.observation_inputs = settings.matching.observation_inputs.map(id => id === prior.id ? item.id : id);
          keys.push("matching");
        }
      }
    } else {
      settings[key].push(item);
    }
    markChanged(...keys);
  }, "加入草稿");
}

function openSystem(key) {
  if (!allowFormEdit()) return;
  const section = systemSettings[key];
  const values = key === "gateway" ? { listen: settings.listen, log_level: settings.log_level, public_ws_url: settings.public_ws_url } : settings[key] || {};
  showEdit(section.label, section.note, section.fields, values, draft => {
    if (key === "gateway") {
      Object.assign(settings, draft);
      settings.public_ws_url = draft.public_ws_url || "";
      markChanged("listen", "log_level", "public_ws_url");
    } else {
      settings[key] = draft;
      markChanged(key);
    }
  }, "加入草稿");
}

async function saveSettings() {
  if (busy || !changed.size) return;
  setSaveError("settings-save-error");
  busy = true;
  renderSettings();
  try {
    const edits = Object.fromEntries([...changed].map(key => [key, settings[key]]));
    const result = await request("api/settings", {
      method: "PUT", headers: { "If-Match": settingsTag, "Content-Type": "application/json" }, body: JSON.stringify(edits)
    });
    settingsTag = result.headers.get("ETag");
    changed.clear();
    for (const entry of [...settings.inputs, ...settings.outputs, settings.admin || {}]) {
      delete entry.secret_value;
      delete entry.token_value;
    }
    yamlTag = "";
    live.restart_needed = true;
    live.restart_error = "";
    settings = null;
    await ensureSettings();
    notice("配置已保存，点击「重启网关」应用。");
  } catch (error) {
    setSaveError("settings-save-error", error.message);
  } finally {
    busy = false;
    if (settings) renderSettings();
  }
}

async function restartGateway() {
  if (busy || live.restarting) return;
  if (changed.size || yamlDirty) throw new Error("请先保存或撤销未保存的更改，再重启网关。");
  restarting = busy = true;
  renderRestart();
  try {
    const response = await request("api/restart", { method: "POST" });
    const restart = await response.json();
    if (restart.listen_changed) {
      const address = listenerURL(restart.listen);
      if (address && address.origin !== location.origin) {
        restartAddress = address;
        return;
      }
    }
    const deadline = performance.now() + 20000;
    while (performance.now() < deadline) {
      await new Promise(resolve => setTimeout(resolve, 400));
      let health;
      try {
        const response = await fetch("../healthz", { cache: "no-store", signal: AbortSignal.timeout(1500) });
        if (response.ok) health = await response.json();
      } catch {}
      if (!health?.generation || health.generation === restart.generation) continue;
      settings = null;
      yamlTag = "";
      get("yaml-panel").open = false;
      try {
        await refresh();
      } catch (error) {
        if (error.status !== 401) throw error;
        token = "";
        get("workspace").hidden = true;
        get("login-screen").hidden = false;
        get("token").focus();
        notice("网关已重启，请使用新的管理令牌登录。");
        return;
      }
      await loadPage();
      if (live.restart_error) throw new Error(live.restart_error);
      notice("网关已重启，配置已应用。");
      return;
    }
    throw new Error("暂未连上重启后的网关，请稍后刷新。");
  } finally {
    restarting = busy = false;
    renderRestart();
  }
}

async function discardSettings() {
  if (busy || !confirm("撤销尚未保存的表单更改？")) return;
  const previous = settings;
  settings = null;
  try {
    await ensureSettings();
    changed.clear();
    setSaveError("settings-save-error");
    renderSettings();
  } catch (error) {
    settings = previous;
    throw error;
  }
}

async function loadYAML() {
  if (changed.size) throw new Error("请先保存或撤销表单草稿。");
  const response = await request("api/config");
  get("yaml").value = await response.text();
  yamlTag = response.headers.get("ETag");
  yamlDirty = false;
  setSaveError("yaml-save-error");
}

async function saveYAML() {
  if (busy) return;
  if (changed.size) throw new Error("请先保存或撤销表单草稿。");
  if (!yamlTag) throw new Error("请先读取配置。");
  setSaveError("yaml-save-error");
  busy = true;
  try {
    const result = await request("api/config", {
      method: "PUT", headers: { "If-Match": yamlTag, "Content-Type": "text/plain" }, body: get("yaml").value
    });
    yamlTag = result.headers.get("ETag");
    yamlDirty = false;
    live.restart_needed = true;
    live.restart_error = "";
    settings = null;
    await ensureSettings();
    renderSettings();
    notice("YAML 已保存，点击「重启网关」应用。");
  } catch (error) {
    setSaveError("yaml-save-error", error.message);
  } finally {
    busy = false;
    renderRestart();
  }
}

function openBinding(data = {}) {
  const namespaces = live.inputs.flatMap(input => [
    input.namespace && [input.namespace, aliasName(input.namespace)],
    data.kind !== "group" && input.private_namespace && [input.private_namespace, aliasName(input.private_namespace)]
  ].filter(Boolean));
  if (data.namespace && !namespaces.some(([value]) => value === data.namespace)) namespaces.push([data.namespace, data.namespace]);
  const fields = [
    ["namespace", "来源 Bot", { required: true, options: [["", "请选择"], ...namespaces] }],
    ["kind", "身份类型", { options: [["user", "用户"], ["group", "群"]] }],
    ["native", "原始 ID", { required: true, wide: true }],
    ["real_id", "真实 QQ 号或群号", { required: true, pattern: "[0-9]+", wide: true, hint: "输入 0 可解除绑定，并清除该原始 ID 的候选证据和冲突。" }]
  ];
  const values = Object.fromEntries(fields.map(([key]) => [key, data[key]]));
  showEdit(data.frozen ? "处理身份冲突" : "人工绑定", "更改即时写入身份数据库。", fields, values, async change => {
    await request("api/binding", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(change) });
    notice(change.real_id === "0" ? "绑定已解除。" : "绑定已保存。");
    attempt(page === "candidates" ? loadCandidates : renderIdentities);
  }, "保存绑定");
}

get("login").addEventListener("submit", event => {
  event.preventDefault();
  if (busy) return;
  busy = true;
  const submit = event.submitter;
  submit.disabled = true;
  token = get("token").value;
  attempt(async () => {
    try {
      await refresh();
    } catch (error) {
      token = "";
      throw error;
    } finally {
      busy = false;
      submit.disabled = false;
    }
    get("token").value = "";
    get("login-screen").hidden = true;
    get("workspace").hidden = false;
    visiblePage(location.hash.slice(1) || "overview");
    notice("");
    await loadPage();
  });
});

get("logout").addEventListener("click", () => {
  if (busy) return;
  if ((changed.size || yamlDirty) && !confirm("退出并放弃未保存的更改？")) return;
  changed.clear();
  yamlDirty = false;
  location.reload();
});
get("refresh").addEventListener("click", () => attempt(async () => {
  await refresh();
  if (page === "candidates") await loadCandidates();
  if (page === "identities") await renderIdentities();
  if (!changed.size && !yamlDirty && ["inputs", "outputs", "routes", "settings"].includes(page) && !busy) {
    settings = null;
    await ensureSettings();
    renderSettings();
  }
  notice("已刷新。");
}));
nav.forEach(item => item.addEventListener("click", () => navigate(item.dataset.page)));
document.querySelectorAll("[data-open]").forEach(item => item.addEventListener("click", () => navigate(item.dataset.open)));
document.querySelectorAll("[data-add]").forEach(item => item.addEventListener("click", () => attempt(async () => {
  await ensureSettings();
  openCollection(item.dataset.add);
})));
get("search").addEventListener("submit", event => {
  event.preventDefault();
  identityPage.page = 1;
  attempt(renderIdentities);
});
get("identity-kind").addEventListener("change", () => { identityPage.page = 1; attempt(renderIdentities); });
get("route-from-filter").addEventListener("change", event => {
  routeInput = event.target.value;
  renderCollection("routes");
});
get("route-to-filter").addEventListener("change", event => {
  routeOutput = event.target.value;
  renderCollection("routes");
});
get("new-binding").addEventListener("click", () => openBinding());
get("candidate-search").addEventListener("submit", event => {
  event.preventDefault();
  candidatePage.page = 1;
  attempt(loadCandidates);
});
get("candidate-filter").addEventListener("change", () => { candidatePage.page = 1; attempt(loadCandidates); });
get("edit-matching").addEventListener("click", () => attempt(async () => {
  await ensureSettings();
  openSystem("matching");
}));
get("log-filter").addEventListener("input", renderLogs);
get("log-level").addEventListener("change", renderLogs);
get("save").addEventListener("click", () => attempt(saveSettings));
get("restart").addEventListener("click", () => attempt(restartGateway));
get("discard").addEventListener("click", () => attempt(discardSettings));
get("yaml-panel").addEventListener("toggle", () => {
  const panel = get("yaml-panel");
  if (!panel.open) return;
  if (changed.size) {
    panel.open = false;
    notice("请先保存或撤销表单草稿。", true);
  } else if (!yamlTag) attempt(loadYAML);
});
get("yaml").addEventListener("input", () => { yamlDirty = true; });
get("reload-yaml").addEventListener("click", () => {
  if (busy || (yamlDirty && !confirm("重新读取配置会丢弃 YAML 更改，继续？"))) return;
  attempt(loadYAML);
});
get("editor").addEventListener("submit", event => {
  event.preventDefault();
  attempt(saveYAML);
});
const dialog = get("edit-dialog");
dialog.addEventListener("close", () => {
  dialog.querySelectorAll('input[type="password"]').forEach(input => { input.value = ""; });
});
get("close-dialog").addEventListener("click", () => dialog.close());
get("cancel-dialog").addEventListener("click", () => dialog.close());
get("edit-form").addEventListener("submit", async event => {
  event.preventDefault();
  const submit = get("apply-edit");
  submit.disabled = true;
  get("edit-error").hidden = true;
  try {
    await edit();
    dialog.close();
  } catch (error) {
    get("edit-error").textContent = error.message;
    get("edit-error").hidden = false;
  } finally { submit.disabled = false; }
});
window.addEventListener("beforeunload", event => {
  if (changed.size || yamlDirty) event.preventDefault();
});
new ResizeObserver(drawTraffic).observe(get("traffic"));
setInterval(() => {
  if (get("workspace").hidden || restarting) return;
  refresh().then(() => {
    if (page === "candidates" && !dialog.open && !get("candidates").contains(document.activeElement)) {
      return loadCandidates();
    }
  }).catch(() => {});
}, 3000);
