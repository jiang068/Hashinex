package config

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ReverseListen = "reverse-listen"
	ForwardDial   = "forward-dial"
	ForwardListen = "forward-listen"
	ReverseDial   = "reverse-dial"
	Active        = "active"
	Observe       = "observe"
	RealQQ        = "real-qq"
	Virtual       = "adapter-virtual"
	OneBot11      = "onebot11"
	QQOfficial    = "qq-official"
	GScore        = "gscore"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type Endpoint struct {
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval,omitempty"`
	ReconnectInterval time.Duration `yaml:"reconnect_interval,omitempty"`
	ID                string        `yaml:"id"`
	Mode              string        `yaml:"ws_mode,omitempty"`
	URL               string        `yaml:"url,omitempty"`
	ListenPath        string        `yaml:"listen_path,omitempty"`
	TokenEnv          string        `yaml:"token_env,omitempty"`
	TokenRef          string        `yaml:"token_ref,omitempty"`
	Token             string        `yaml:"-"`
}

type Input struct {
	Endpoint         `yaml:",inline"`
	Protocol         string   `yaml:"protocol"`
	Role             string   `yaml:"role"`
	IdentitySource   string   `yaml:"identity_source"`
	SelfID           string   `yaml:"self_id"`
	AppID            string   `yaml:"app_id,omitempty"`
	SecretEnv        string   `yaml:"secret_env,omitempty"`
	SecretRef        string   `yaml:"secret_ref,omitempty"`
	Secret           string   `yaml:"-"`
	Events           string   `yaml:"events,omitempty"`
	APIOrigin        string   `yaml:"api_origin,omitempty"`
	GroupOpenIDs     []string `yaml:"group_openids,omitempty"`
	PrivateOpenIDs   []string `yaml:"private_openids,omitempty"`
	GroupIDs         []string `yaml:"group_ids,omitempty"`
	PrivateQQIDs     []string `yaml:"private_qq_ids,omitempty"`
	EnablePrivate    bool     `yaml:"enable_private,omitempty"`
	DisablePrivate   bool     `yaml:"disable_private,omitempty"`
	PrivateBindGroup string   `yaml:"private_bind_group,omitempty"`
	SilentGroups     []string `yaml:"silent_groups,omitempty"`
	BusinessGroups   []string `yaml:"business_groups,omitempty"`
	Intents          uint64   `yaml:"intents,omitempty"`
	AllowProactive   bool     `yaml:"allow_proactive,omitempty"`
	ObservedProfiles bool     `yaml:"observed_profiles,omitempty"`
}

func (in Input) PrivateEnabled() bool {
	return in.EnablePrivate && !in.DisablePrivate
}

func (in Input) Silent(group, native string) bool {
	if group == "" && native == "" {
		return in.Role == Observe
	}
	if slices.Contains(in.SilentGroups, group) || slices.Contains(in.SilentGroups, native) {
		return true
	}
	if slices.Contains(in.BusinessGroups, group) || slices.Contains(in.BusinessGroups, native) {
		return false
	}
	return in.Role == Observe
}

type Output struct {
	Endpoint         `yaml:",inline"`
	Protocol         string `yaml:"protocol"`
	OfficialFileRoot string `yaml:"official_file_root,omitempty"`
}

type Route struct {
	From               string   `yaml:"from"`
	To                 string   `yaml:"to,omitempty"`
	Outputs            []string `yaml:"outputs,omitempty"`
	Events             []string `yaml:"events,omitempty"`
	Groups             []string `yaml:"groups,omitempty"`
	Users              []string `yaml:"users,omitempty"`
	IncludePrivate     bool     `yaml:"include_private,omitempty"`
	AllowActions       bool     `yaml:"allow_actions"`
	SendGroupVia       string   `yaml:"send_group_via,omitempty"`
	SendGroupViaGroups []string `yaml:"send_group_via_groups,omitempty"`
	ImplicitReply      string   `yaml:"implicit_reply,omitempty"`
	Passthrough        bool     `yaml:"passthrough,omitempty"`
}

func (r Route) Targets() []string {
	if r.To != "" {
		return []string{r.To}
	}
	return r.Outputs
}

const (
	DefaultGScoreFrameBytes = 16 << 20
	maxFrameBytes           = 64 << 20
)

type Limits struct {
	FrameBytes       int64         `yaml:"frame_bytes"`
	GScoreFrameBytes int64         `yaml:"gscore_frame_bytes"`
	EventQueue       int           `yaml:"event_queue"`
	APIQueue         int           `yaml:"api_queue"`
	PendingPerPeer   int           `yaml:"pending_per_peer"`
	PendingPerInput  int           `yaml:"pending_per_input"`
	WriteTimeout     time.Duration `yaml:"write_timeout"`
	APITimeout       time.Duration `yaml:"api_timeout"`
	EventTTL         time.Duration `yaml:"event_ttl"`
	DedupTTL         time.Duration `yaml:"dedup_ttl"`
	DedupSize        int           `yaml:"dedup_size"`
	PingInterval     time.Duration `yaml:"ping_interval"`
}

type Config struct {
	Listen    string    `yaml:"listen"`
	PublicWS  string    `yaml:"public_ws_url,omitempty"`
	LogLevel  string    `yaml:"log_level"`
	Inputs    []Input   `yaml:"inputs"`
	Outputs   []Output  `yaml:"outputs"`
	Routes    []Route   `yaml:"routes"`
	Limits    Limits    `yaml:"limits"`
	Storage   Storage   `yaml:"storage"`
	Matching  Matching  `yaml:"matching"`
	Admin     Admin     `yaml:"admin"`
	Logging   Logging   `yaml:"logging"`
	TempFiles TempFiles `yaml:"tempfiles"`
}

type Admin struct {
	TokenEnv    string `yaml:"token_env,omitempty"`
	TokenRef    string `yaml:"token_ref,omitempty"`
	Token       string `yaml:"-"`
	AllowRemote bool   `yaml:"allow_remote,omitempty"`
}

type Logging struct {
	File    string `yaml:"file,omitempty"`
	MaxSize int64  `yaml:"max_size_bytes,omitempty"` // Legacy option accepted for existing configurations.
	Keep    int    `yaml:"keep"`
}

type Storage struct {
	SQLite      string        `yaml:"sqlite,omitempty"`
	MessageTTL  time.Duration `yaml:"message_ttl"`
	ReplyWindow time.Duration `yaml:"reply_window"`
}

type Matching struct {
	Mode              string        `yaml:"mode"`
	PairWindow        time.Duration `yaml:"pair_window"`
	EventTTL          time.Duration `yaml:"event_ttl"`
	EvidenceTTL       time.Duration `yaml:"evidence_ttl"`
	Successes         int           `yaml:"successes_required"`
	GroupSuccesses    int           `yaml:"group_successes_required"`
	GroupSenders      int           `yaml:"group_senders_required"`
	Distance          int           `yaml:"phash_max_distance"`
	ObservationInputs []string      `yaml:"observation_inputs,omitempty"`
	DefaultHashes     []string      `yaml:"default_avatar_phashes,omitempty"`
}

func (m Matching) Observes(in Input) bool {
	if in.Protocol != OneBot11 || in.IdentitySource != RealQQ {
		return false
	}
	if len(m.ObservationInputs) != 0 {
		return slices.Contains(m.ObservationInputs, in.ID)
	}
	return in.Role == Observe || len(in.SilentGroups) != 0
}

func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("配置文件超过 1 MiB。请缩减配置后重试。")
	}
	return Decode(raw, path)
}

func Decode(raw []byte, path string) (*Config, error) {
	cfg, err := ParseAt(raw, path)
	if err != nil {
		return nil, err
	}
	return resolve(cfg, path, nil)
}

func resolve(cfg *Config, path string, overrides map[string]string) (*Config, error) {
	cfg.ExpandGScoreOutputs()
	if err := cfg.UpgradeQQScopes(path); err != nil {
		return nil, err
	}
	secrets, err := readCredentials(cfg, path, overrides, true)
	if err != nil {
		return nil, err
	}
	if err := cfg.validate(secrets); err != nil {
		return nil, err
	}
	if cfg.Storage.SQLite != "" && !filepath.IsAbs(cfg.Storage.SQLite) {
		cfg.Storage.SQLite = filepath.Join(filepath.Dir(path), cfg.Storage.SQLite)
	}
	if cfg.Logging.File != "" && !filepath.IsAbs(cfg.Logging.File) {
		cfg.Logging.File = filepath.Join(filepath.Dir(path), cfg.Logging.File)
	}
	if cfg.TempFiles.Enabled && !filepath.IsAbs(cfg.TempFiles.Directory) {
		cfg.TempFiles.Directory = filepath.Join(filepath.Dir(path), cfg.TempFiles.Directory)
	}
	return cfg, nil
}

// Parse reads configuration with defaults, keeping paths and credential references as entered.
func Parse(raw []byte) (*Config, error) {
	return ParseAt(raw, "")
}

// ParseAt applies the defaults of the configuration's layout without resolving
// relative paths or credentials.
func ParseAt(raw []byte, path string) (*Config, error) {
	cfg := Config{
		Listen: "127.0.0.1:6700", LogLevel: "info",
		Storage:   Storage{MessageTTL: 24 * time.Hour, ReplyWindow: 30 * time.Second},
		Logging:   Logging{Keep: 3},
		TempFiles: TempFiles{Listen: "127.0.0.1:6701", Directory: "tempfiles", TTL: 10 * time.Minute, MaxBytes: 512 << 20},
		Matching: Matching{Mode: "off", PairWindow: 2 * time.Second, EventTTL: 5 * time.Second,
			EvidenceTTL: 24 * time.Hour, Successes: 3, GroupSuccesses: 3, GroupSenders: 2, Distance: 4},
		Limits: Limits{
			FrameBytes: 1 << 20, GScoreFrameBytes: DefaultGScoreFrameBytes, EventQueue: 128, APIQueue: 32,
			PendingPerPeer: 32, PendingPerInput: 128,
			WriteTimeout: 5 * time.Second, APITimeout: 12 * time.Second,
			EventTTL: 15 * time.Second, DedupTTL: time.Minute, DedupSize: 4096,
			PingInterval: 20 * time.Second,
		},
	}
	if portableGatewayConfig(path) {
		cfg.Storage.SQLite = "../data/hashinex.sqlite"
		cfg.Logging.File = "../data/hashinex.jsonl"
		cfg.TempFiles.Directory = "../data/tempfiles"
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, yamlConfigError(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, yamlConfigError(err)
		}
		return nil, fmt.Errorf("配置文件只能包含一份 YAML 配置。请移除多余的文档分隔线和内容。")
	}
	return &cfg, nil
}

func (cfg *Config) validate(secrets map[string]string) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("「网关服务 → 监听地址」（listen）须填写主机和端口，例如 127.0.0.1:6700。")
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return fmt.Errorf("「网关服务 → 监听地址」（listen）的端口须为 1 到 65535 的整数。")
	}
	if err := cfg.TempFiles.validate(port); err != nil {
		return err
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("「网关服务 → 日志级别」（log_level）请选择 debug、info、warn 或 error。")
	}
	if cfg.PublicWS != "" {
		address, err := wsURL(cfg.PublicWS)
		if err != nil || (address.Path != "" && address.Path != "/") || address.RawQuery != "" {
			return fmt.Errorf("「网关服务 → 对外 WebSocket 地址」（public_ws_url）只填 ws:// 或 wss:// 加主机和可选端口，请移除路径、查询参数和凭证。")
		}
	}
	if cfg.Admin.TokenEnv != "" || cfg.Admin.TokenRef != "" {
		cfg.Admin.Token = credential(secrets, cfg.Admin.TokenRef, cfg.Admin.TokenEnv)
		if len(cfg.Admin.Token) < 16 || strings.ContainsAny(cfg.Admin.Token, " \t\r\n") {
			return fmt.Errorf("「管理访问 → 管理令牌」至少需要 16 个字符，且不能包含空白。请重新填写令牌。")
		}
	}
	if cfg.Admin.AllowRemote && cfg.Admin.Token == "" {
		return fmt.Errorf("「管理访问 → 允许从其他主机访问 WebUI」（admin.allow_remote）需要管理令牌。请设置管理令牌，或关闭远程访问。")
	}
	if cfg.Logging.Keep < 1 {
		return fmt.Errorf("「文件日志 → 保留天数」（logging.keep）须为正整数，包含当天的日志文件。")
	}
	limits := cfg.Limits
	for _, limit := range []struct {
		field           string
		value, min, max int64
	}{
		{"frame_bytes", limits.FrameBytes, 1024, maxFrameBytes},
		{"gscore_frame_bytes", limits.GScoreFrameBytes, 1024, maxFrameBytes},
		{"event_queue", int64(limits.EventQueue), 1, 65536},
		{"api_queue", int64(limits.APIQueue), 1, 65536},
		{"dedup_size", int64(limits.DedupSize), 1, 1 << 20},
	} {
		if limit.value < limit.min || limit.value > limit.max {
			return fmt.Errorf("「队列与超时」的 limits.%s 须在 %d 到 %d 之间。", limit.field, limit.min, limit.max)
		}
	}
	if limits.PendingPerPeer < 1 || limits.PendingPerPeer > limits.APIQueue {
		return fmt.Errorf("单连接待处理数（limits.pending_per_peer）须至少为 1，且不能超过 API 队列大小（limits.api_queue，当前为 %d）。请调低前者或调高队列大小。", limits.APIQueue)
	}
	if limits.PendingPerInput < limits.PendingPerPeer || limits.PendingPerInput > 65536 {
		return fmt.Errorf("单输入待处理数（limits.pending_per_input）须至少等于单连接待处理数（limits.pending_per_peer，当前为 %d），且不能超过 65536。", limits.PendingPerPeer)
	}
	for _, limit := range []struct {
		field      string
		value, min time.Duration
	}{
		{"write_timeout", limits.WriteTimeout, 25 * time.Millisecond},
		{"api_timeout", limits.APITimeout, 25 * time.Millisecond},
		{"event_ttl", limits.EventTTL, time.Nanosecond},
		{"dedup_ttl", limits.DedupTTL, time.Nanosecond},
		{"ping_interval", limits.PingInterval, time.Nanosecond},
	} {
		if limit.value < limit.min {
			return fmt.Errorf("「队列与超时」的 limits.%s 须至少为 %s。", limit.field, limit.min)
		}
	}
	if cfg.Storage.MessageTTL <= 0 {
		return fmt.Errorf("「身份数据库 → 消息保留时间」（storage.message_ttl）须大于 0，例如 24h。")
	}
	if cfg.Storage.ReplyWindow <= 0 || cfg.Storage.ReplyWindow > cfg.Storage.MessageTTL {
		return fmt.Errorf("「身份数据库 → 隐式回复窗口」（storage.reply_window）须大于 0，且不能超过消息保留时间（storage.message_ttl，当前为 %s）。", cfg.Storage.MessageTTL)
	}
	inputs := make(map[string]Input, len(cfg.Inputs))
	apps := make(map[string]string)
	listeners := make(map[string]string)
	for i := range cfg.Inputs {
		input := &cfg.Inputs[i]
		if !namePattern.MatchString(input.ID) {
			return fmt.Errorf("第 %d 个输入的名称（inputs[].id）须为 1 到 64 个英文字母、数字、下划线或短横线，并以字母或数字开头。", i+1)
		}
		if _, exists := inputs[input.ID]; exists {
			return fmt.Errorf("输入 %q 的名称（id）重复。请给两个输入使用不同的名称，并更新引用该名称的路由和观察实例名单。", input.ID)
		}
		if err := input.Endpoint.validateTiming(input.Protocol == OneBot11, input.Mode == ForwardDial || input.Protocol == QQOfficial && input.Events == "gateway"); err != nil {
			return fmt.Errorf("输入 %q：%w", input.ID, err)
		}
		if input.Role == "" {
			input.Role = Active
		}
		if input.Role != Active && input.Role != Observe {
			return fmt.Errorf("输入 %q 的「默认群行为」（role）请选择业务 Bot（active）或静默观察（observe）。", input.ID)
		}
		if input.Protocol == OneBot11 && input.Role == Observe && input.IdentitySource != RealQQ {
			return fmt.Errorf("输入 %q 的「默认群行为」为静默观察（role: observe），要求「ID 来源」（identity_source）为真实 QQ。请接入提供真实 QQ 号的 OneBot，或改为业务 Bot。", input.ID)
		}
		if !validID(input.SelfID) {
			return fmt.Errorf("输入 %q 的 OneBot self_id 须为正整数。请填写该 Bot 的数字 ID。", input.ID)
		}
		for _, scope := range []struct {
			field, label, identity string
			ids                    []string
		}{
			{"silent_groups", "静默群", "QQ群号", input.SilentGroups},
			{"business_groups", "业务群", "QQ群号", input.BusinessGroups},
			{"group_ids", "群白名单", "QQ群号", input.GroupIDs},
			{"private_qq_ids", "私聊白名单", "QQ号", input.PrivateQQIDs},
		} {
			for _, id := range scope.ids {
				if !validQQID(id) {
					return fmt.Errorf("输入 %q 的「%s」（%s）须填写真实%s。请替换该列表中的无效号码、OpenID 或虚拟 ID。", input.ID, scope.label, scope.field, scope.identity)
				}
			}
		}
		switch input.Protocol {
		case OneBot11:
			if input.IdentitySource != RealQQ && input.IdentitySource != Virtual {
				return fmt.Errorf("OneBot 输入 %q 缺少有效的「ID 来源」（identity_source）。请按上游实际提供的 ID 选择真实 QQ（real-qq）或适配器虚拟 ID（adapter-virtual）。", input.ID)
			}
			if input.Mode != ReverseListen && input.Mode != ForwardDial {
				return fmt.Errorf("OneBot 输入 %q 的「连接方式」（ws_mode）请选择反向监听（reverse-listen）或正向拨号（forward-dial）。", input.ID)
			}
			if input.IdentitySource == Virtual && cfg.Storage.SQLite == "" {
				return fmt.Errorf("输入 %q 的「ID 来源」为适配器虚拟 ID，需要身份数据库。请在「系统设置 → 身份数据库」填写 SQLite 文件（storage.sqlite）。", input.ID)
			}
			if err := input.Endpoint.validate(host, port, input.Mode == ReverseListen, secrets); err != nil {
				return fmt.Errorf("「输入管理」%w", err)
			}
			if input.ListenPath != "" {
				if !validListenPath(input.ListenPath) {
					return fmt.Errorf("输入 %q 的「自定义连接路径」（listen_path）无效或占用了网关保留路径。请改用例如 /bot/napcat，或留空使用默认路径。", input.ID)
				}
				if existing := listeners[input.ListenPath]; existing != "" {
					return fmt.Errorf("输入 %q 的「自定义连接路径」（listen_path）与%s重复。请更换其中一个监听路径，或留空使用默认路径。", input.ID, existing)
				}
				listeners[input.ListenPath] = fmt.Sprintf("输入 %q 的 listen_path", input.ID)
			}
		case QQOfficial:
			if !validID(input.AppID) {
				return fmt.Errorf("QQ 官方输入 %q 的 AppID（app_id）须为正整数。请从 QQ 开放平台填写该 Bot 的 AppID。", input.ID)
			}
			if existing := apps[input.AppID]; existing != "" {
				return fmt.Errorf("QQ 官方输入 %q 与 %q 的 AppID（app_id）重复。请为每个官方 Bot 仅保留一个输入。", input.ID, existing)
			}
			if input.Mode != "" || input.URL != "" || input.ListenPath != "" || input.TokenEnv != "" || input.TokenRef != "" {
				return fmt.Errorf("输入 %q 已选择 QQ 官方 Bot，不能同时配置 OneBot 连接字段。请移除 ws_mode、url、listen_path、token_env 和 token_ref 中残留的 OneBot 设置。", input.ID)
			}
			if cfg.Storage.SQLite == "" {
				return fmt.Errorf("QQ 官方输入 %q 需要身份数据库。请在「系统设置 → 身份数据库」填写 SQLite 文件（storage.sqlite）。", input.ID)
			}
			input.Secret = credential(secrets, input.SecretRef, input.SecretEnv)
			if input.Secret == "" {
				return fmt.Errorf("QQ 官方输入 %q 缺少 AppSecret。请在该输入的 AppSecret 栏录入开放平台的密钥，或检查已有密钥引用。", input.ID)
			}
			if input.Events != "gateway" && input.Events != "webhook" {
				return fmt.Errorf("QQ 官方输入 %q 的「接收事件」（events）请选择官方 Gateway（gateway）或签名 Webhook（webhook）。", input.ID)
			}
			if input.APIOrigin != "" && input.APIOrigin != "bot" && input.APIOrigin != "sgroup" {
				return fmt.Errorf("QQ 官方输入 %q 的「HTTP API」（api_origin）请选择默认、api.bot.qq.com（bot）或 api.sgroup.qq.com（sgroup）。", input.ID)
			}
			if input.PrivateBindGroup != "" && !validID(input.PrivateBindGroup) {
				return fmt.Errorf("QQ 官方输入 %q 的旧字段 private_bind_group 须为数字群号。新版会自动查找绑定群，可删除这个旧字段。", input.ID)
			}
			if input.Intents == 0 {
				input.Intents = 1 << 25
			}
			apps[input.AppID] = input.ID
		default:
			return fmt.Errorf("输入 %q 的「接入协议」（protocol）请选择 OneBot v11（onebot11）或 QQ 官方 Bot（qq-official）。", input.ID)
		}
		inputs[input.ID] = *input
	}
	outputs := make(map[string]Output, len(cfg.Outputs))
	coreURLs := make(map[string]string)
	for i := range cfg.Outputs {
		output := &cfg.Outputs[i]
		if !namePattern.MatchString(output.ID) {
			return fmt.Errorf("第 %d 个输出的名称（outputs[].id）须为 1 到 64 个英文字母、数字、下划线或短横线，并以字母或数字开头。", i+1)
		}
		if _, exists := outputs[output.ID]; exists {
			return fmt.Errorf("输出 %q 的名称（id）重复。请给两个输出使用不同的名称，并更新相关路由。", output.ID)
		}
		if err := output.Endpoint.validateTiming(true, output.Mode == ReverseDial || output.Protocol == GScore); err != nil {
			return fmt.Errorf("输出 %q：%w", output.ID, err)
		}
		if output.OfficialFileRoot != "" && !filepath.IsAbs(output.OfficialFileRoot) {
			return fmt.Errorf("输出 %q 的「官 Bot 本地文件目录」（official_file_root）须填写网关进程能读取的绝对路径。", output.ID)
		}
		switch output.Protocol {
		case OneBot11:
			if output.Mode != ForwardListen && output.Mode != ReverseDial {
				return fmt.Errorf("OneBot 输出 %q 的「连接方式」（ws_mode）请选择正向监听（forward-listen）或反向拨号（reverse-dial）。", output.ID)
			}
			if err := output.Endpoint.validate(host, port, output.Mode == ForwardListen, secrets); err != nil {
				return fmt.Errorf("「输出管理」%w", err)
			}
			if output.ListenPath != "" && !validListenPath(strings.ReplaceAll(output.ListenPath, "{input}", "input")) {
				return fmt.Errorf("输出 %q 的「监听路径」（listen_path）无效或占用了网关保留路径。请改用例如 /astrbot/{input}，或留空使用默认路径。", output.ID)
			}
		case GScore:
			if output.Mode != "" || output.ListenPath != "" {
				return fmt.Errorf("gscore 输出 %q 使用直连。请移除 ws_mode 和 listen_path，只在「目标 WebSocket 地址」（url）填写 gscore 的完整地址。", output.ID)
			}
			output.Token = credential(secrets, output.TokenRef, output.TokenEnv)
			if output.TokenEnv != "" && output.Token == "" {
				return fmt.Errorf("gscore 输出 %q 的 token_env 未提供令牌。请在「输出管理」填写「连接令牌」，或设置该环境变量。", output.ID)
			}
			u, err := wsURL(output.URL)
			if err != nil || strings.ContainsAny(output.URL, "{}") {
				return fmt.Errorf("gscore 输出 %q 的「目标 WebSocket 地址」（url）须为完整的 ws:// 或 wss:// 地址。请将令牌填入独立的令牌栏，并将地址模板替换为实际路径。", output.ID)
			}
			address := strings.ToLower(u.Host) + u.EscapedPath()
			if existing := coreURLs[address]; existing != "" {
				return fmt.Errorf("gscore 输出 %q 与 %q 的「目标 WebSocket 地址」（url）相同。要共用连接，请让相关路由选择同一个输出。要独立连接，请为其中一个配置不同的目标地址。", existing, output.ID)
			}
			coreURLs[address] = output.ID
		default:
			return fmt.Errorf("输出 %q 的「输出类型」（protocol）请选择 OneBot v11（onebot11）或 gsuid_core 直连（gscore）。", output.ID)
		}
		outputs[output.ID] = *output
	}
	routes := make(map[[2]string]bool, len(cfg.Routes))
	coreBots := make(map[[2]string]string)
	for i, route := range cfg.Routes {
		input, exists := inputs[route.From]
		if !exists {
			if namePattern.MatchString(route.From) {
				return fmt.Errorf("第 %d 条路由引用的输入 %q 不存在（from）。请在「路由管理」重选「来源输入」，或恢复对应输入。", i+1, route.From)
			}
			return fmt.Errorf("第 %d 条路由缺少有效的「来源输入」（from）。请选择已配置的输入。", i+1)
		}
		routeName := fmt.Sprintf("第 %d 条路由（来源 %q）", i+1, route.From)
		if len(route.Targets()) == 0 {
			return fmt.Errorf("%s缺少「目标输出」。请在 outputs 中至少选择一个已配置的输出。", routeName)
		}
		if route.To != "" && len(route.Outputs) != 0 {
			return fmt.Errorf("%s同时填写了旧字段 to 和目标输出列表 outputs。请保留 outputs，删除 to。", routeName)
		}
		if len(route.SendGroupViaGroups) != 0 && route.SendGroupVia == "" {
			return fmt.Errorf("%s填写了「官 Bot 发送群号」（send_group_via_groups）。请在「群消息发送 Bot」选择官 Bot，或清空代发群号。", routeName)
		}
		for index, group := range route.SendGroupViaGroups {
			if !validQQID(group) {
				return fmt.Errorf("%s的「官 Bot 发送群号」（send_group_via_groups）第 %d 项无效。请填写真实 QQ 群号。", routeName, index+1)
			}
		}
		if route.SendGroupVia != "" {
			if input.Protocol != OneBot11 || input.IdentitySource != RealQQ {
				return fmt.Errorf("%s的「群消息发送 Bot」（send_group_via）要求来源是提供真实 QQ 号的 OneBot 输入。请重选来源，或将发送 Bot 设为「沿用来源 Bot」。", routeName)
			}
			if !route.AllowActions {
				return fmt.Errorf("%s已指定「群消息发送 Bot」（send_group_via），但「允许下游 API 回程」（allow_actions）已关闭。请开启 API 回程，或将发送 Bot 设为「沿用来源 Bot」。", routeName)
			}
			sender, exists := inputs[route.SendGroupVia]
			if !exists {
				if namePattern.MatchString(route.SendGroupVia) {
					return fmt.Errorf("%s指定的群消息发送 Bot %q 不存在。请重选已配置的 QQ 官方输入，或选「沿用来源 Bot」。", routeName, route.SendGroupVia)
				}
				return fmt.Errorf("%s的「群消息发送 Bot」（send_group_via）无效。请选择已配置的 QQ 官方输入。", routeName)
			}
			if sender.Protocol != QQOfficial {
				return fmt.Errorf("%s的发送 Bot %q 与「群消息发送 Bot」要求的 QQ 官方协议冲突。请改选一个 QQ 官方 Bot，或选「沿用来源 Bot」。", routeName, sender.ID)
			}
			if !sender.AllowProactive {
				return fmt.Errorf("%s指定了发送 Bot %q，但它的「允许主动发送」（allow_proactive）已关闭。请在「输入管理」开启该选项，或在路由里更换发送 Bot。", routeName, sender.ID)
			}
		}
		for _, target := range route.Targets() {
			output, exists := outputs[target]
			key := [2]string{route.From, target}
			if !exists {
				if namePattern.MatchString(target) {
					return fmt.Errorf("%s引用的输出 %q 不存在。请重选「目标输出」，或恢复对应输出。", routeName, target)
				}
				return fmt.Errorf("%s的「目标输出」（outputs）含无效名称。请选择已配置的输出。", routeName)
			}
			if routes[key] {
				return fmt.Errorf("来源 %q 到输出 %q 的路由重复。请将配置合并到同一条路由，每个目标只选一次。", route.From, target)
			}
			routes[key] = true
			if output.ListenPath != "" {
				path := strings.ReplaceAll(output.ListenPath, "{input}", route.From)
				if !validListenPath(path) {
					return fmt.Errorf("%s的输出 %q 展开后的监听路径（listen_path）无效。请在「输出管理」更换该路径，或留空使用默认路径。", routeName, target)
				}
				if existing := listeners[path]; existing != "" {
					return fmt.Errorf("%s的输出 %q 的监听路径与%s重复。请更换路径。同一输出接收多个来源时，可在 listen_path 中加上 {input}。", routeName, target, existing)
				}
				listeners[path] = fmt.Sprintf("来源 %q 到输出 %q 的 listen_path", route.From, target)
			}
			if output.Protocol == GScore {
				bot := [2]string{target, input.SelfID}
				if existing := coreBots[bot]; existing != "" {
					return fmt.Errorf("输入 %q 与 %q 共用 gscore 输出 %q，且 OneBot self_id 重复。请为两个输入设置各自的 Bot ID，或改用不同的 gscore 输出。", existing, route.From, target)
				}
				coreBots[bot] = route.From
			}
		}
		if route.ImplicitReply != "" && route.ImplicitReply != "unique-recent" {
			return fmt.Errorf("%s的「隐式回复」（implicit_reply）请选择关闭，或使用唯一的最近消息（unique-recent）。", routeName)
		}
		for index, kind := range route.Events {
			switch kind {
			case "message", "message_sent", "notice", "request":
			default:
				return fmt.Errorf("%s的「允许转发的事件」（events）第 %d 项无效。可选 message、message_sent、notice 或 request。", routeName, index+1)
			}
		}
		for _, scope := range []struct {
			field, label string
			ids          []string
		}{{"groups", "群号过滤", route.Groups}, {"users", "用户过滤", route.Users}} {
			for index, id := range scope.ids {
				if !validID(id) || (input.Protocol == QQOfficial && !validQQID(id)) {
					if input.Protocol == QQOfficial {
						return fmt.Errorf("%s的「%s」（%s）第 %d 项无效。QQ 官方输入须填写真实群号或 QQ 号。", routeName, scope.label, scope.field, index+1)
					}
					return fmt.Errorf("%s的「%s」（%s）第 %d 项无效。请填写下游看到的正整数 ID。", routeName, scope.label, scope.field, index+1)
				}
			}
		}
	}
	for _, in := range cfg.Inputs {
		if in.Mode == ForwardDial && dialHitsListener(in.URL, host, port, listeners) {
			return fmt.Errorf("输入 %q 的 WebSocket 地址（url）指向网关自己的监听接口。请填写上游适配器的 WebSocket 地址，或切换到反向监听。", in.ID)
		}
	}
	for _, out := range cfg.Outputs {
		if out.Mode == ReverseDial && dialHitsListener(out.URL, host, port, listeners) {
			return fmt.Errorf("输出 %q 的 WebSocket 地址（url）指向网关自己的监听接口。请填写下游应用的 WebSocket 地址，或切换到正向监听。", out.ID)
		}
	}
	m := cfg.Matching
	for index, id := range m.ObservationInputs {
		input, exists := inputs[id]
		if !exists {
			if namePattern.MatchString(id) {
				return fmt.Errorf("「静默匹配 → 观察实例白名单」（matching.observation_inputs）中的输入 %q 不存在。请更新该名称或从名单移除。", id)
			}
			return fmt.Errorf("「静默匹配 → 观察实例白名单」（matching.observation_inputs）第 %d 项名称无效。请填写已配置的输入名称。", index+1)
		}
		if !m.Observes(input) {
			return fmt.Errorf("「静默匹配 → 观察实例白名单」（matching.observation_inputs）中的输入 %q 必须使用 OneBot v11（protocol）并提供真实 QQ ID（identity_source: real-qq）。请选择符合条件的输入，或移除该项。", id)
		}
	}
	switch m.Mode {
	case "off":
	case "shadow", "auto":
		if cfg.Storage.SQLite == "" {
			return fmt.Errorf("「静默匹配」当前模式为 %s，需要 SQLite 文件（storage.sqlite）。请在「身份数据库」填写路径，或将匹配模式设为关闭（off）。", m.Mode)
		}
		if len(apps) == 0 {
			return fmt.Errorf("「静默匹配」当前模式为 %s，需要 QQ 官方输入。请在「输入管理」添加一个 QQ 官方 Bot，或将匹配模式设为关闭（off）。", m.Mode)
		}
		if !slices.ContainsFunc(cfg.Inputs, m.Observes) {
			return fmt.Errorf("「静默匹配」当前模式为 %s，但没有可用的观察输入。请在「观察实例白名单」（matching.observation_inputs）填写一个提供真实 QQ 号的 OneBot 输入名称，或将匹配模式设为关闭（off）。", m.Mode)
		}
	default:
		return fmt.Errorf("「静默匹配 → 匹配模式」（matching.mode）请选择关闭（off）、仅观察（shadow）或自动绑定（auto）。")
	}
	if m.PairWindow < 25*time.Millisecond || m.PairWindow > 10*time.Second {
		return fmt.Errorf("配对窗口（matching.pair_window）须在 25ms 到 10s 之间。")
	}
	if m.EventTTL <= 2*m.PairWindow || m.EventTTL > time.Minute {
		return fmt.Errorf("待配对事件保留时间（matching.event_ttl）须大于配对窗口（matching.pair_window）的两倍（当前为 %s），且最多 1m。请调整保留时间或缩短配对窗口。", 2*m.PairWindow)
	}
	if m.EvidenceTTL < m.EventTTL || m.EvidenceTTL > 30*24*time.Hour {
		return fmt.Errorf("匹配证据保留时间（matching.evidence_ttl）须至少等于事件保留时间（matching.event_ttl，当前为 %s），且最多 720h。", m.EventTTL)
	}
	if m.Successes < 2 || m.Successes > 100 {
		return fmt.Errorf("用户匹配证据数（matching.successes_required）须在 2 到 100 之间。")
	}
	if m.GroupSuccesses < 2 || m.GroupSuccesses > 100 {
		return fmt.Errorf("群匹配证据数（matching.group_successes_required）须在 2 到 100 之间。")
	}
	if m.GroupSenders < 1 || m.GroupSenders > m.GroupSuccesses {
		return fmt.Errorf("群匹配不同发送者数（matching.group_senders_required）须至少为 1，且不能超过群匹配证据数（matching.group_successes_required，当前为 %d）。", m.GroupSuccesses)
	}
	if m.Distance < 0 || m.Distance > 16 {
		return fmt.Errorf("最大 pHash 汉明距离（matching.phash_max_distance）须在 0 到 16 之间。")
	}
	if len(m.DefaultHashes) > 256 {
		return fmt.Errorf("「默认头像 pHash」（matching.default_avatar_phashes）最多可填 256 项，请缩减列表。")
	}
	for index, hash := range m.DefaultHashes {
		if _, err := strconv.ParseUint(hash, 16, 64); err != nil || len(hash) != 16 {
			return fmt.Errorf("「默认头像 pHash」（matching.default_avatar_phashes）第 %d 项无效。请填写 16 位十六进制值。", index+1)
		}
	}
	return nil
}

func validID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func (endpoint *Endpoint) validate(listenHost, listenPort string, listening bool, secrets map[string]string) error {
	if endpoint.TokenEnv != "" || endpoint.TokenRef != "" {
		endpoint.Token = credential(secrets, endpoint.TokenRef, endpoint.TokenEnv)
		if endpoint.Token == "" || strings.ContainsAny(endpoint.Token, " \t\r\n") {
			return fmt.Errorf("%q 的连接令牌缺失或包含空白。请重新填写令牌，或检查 token_env、token_ref 所引用的凭证。", endpoint.ID)
		}
	}
	if listening {
		if endpoint.URL != "" {
			return fmt.Errorf("%q 的「连接方式」是监听，和拨号地址（url）冲突。请清空 url，或将连接方式改为拨号。", endpoint.ID)
		}
		if !loopback(listenHost) && endpoint.Token == "" {
			return fmt.Errorf("%q 使用监听模式，且「网关服务 → 监听地址」（listen）允许外部连接。请为该实例设置连接令牌，或将网关监听地址改为本机回环地址。", endpoint.ID)
		}
		return nil
	}
	u, err := wsURL(endpoint.URL)
	if err != nil {
		return fmt.Errorf("%q 的 WebSocket 地址（url）须为完整的 ws:// 或 wss:// 地址。请将凭证放入独立的令牌栏。", endpoint.ID)
	}
	if endpoint.ListenPath != "" {
		return fmt.Errorf("%q 的「连接方式」是拨号，和监听路径（listen_path）冲突。请清空监听路径，或将连接方式改为监听。", endpoint.ID)
	}
	local := loopback(u.Hostname()) || u.Hostname() == listenHost
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "wss" {
			port = "443"
		}
	}
	if local && port == listenPort && strings.HasPrefix(u.Path, "/onebot/") {
		return fmt.Errorf("%q 的 WebSocket 地址（url）指向网关自己的 OneBot 监听接口。请填写对端应用的地址。", endpoint.ID)
	}
	return nil
}

func wsURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "ws" && u.Scheme != "wss") ||
		u.User != nil || u.Fragment != "" || u.Query().Has("access_token") || u.Query().Has("token") {
		return nil, fmt.Errorf("请填写完整的 ws:// 或 wss:// 地址，并将凭证填入独立的令牌栏。")
	}
	return u, nil
}

func dialHitsListener(address, host, port string, paths map[string]string) bool {
	u, _ := url.Parse(address)
	return u != nil && (loopback(u.Hostname()) || u.Hostname() == host) &&
		u.Port() == port && (paths[u.Path] != "" || strings.HasPrefix(u.Path, "/_hashinex/"))
}

var listenerPath = regexp.MustCompile(`^(/[A-Za-z0-9_.~-]+)+$`)

func validListenPath(path string) bool {
	if !listenerPath.MatchString(path) || path == "/healthz" {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	for _, reserved := range []string{"/admin", "/onebot/in", "/onebot/out", "/qq/webhook", "/_hashinex"} {
		if path == reserved || strings.HasPrefix(path, reserved+"/") {
			return false
		}
	}
	return true
}
