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
	ID         string `yaml:"id"`
	Mode       string `yaml:"ws_mode,omitempty"`
	URL        string `yaml:"url,omitempty"`
	ListenPath string `yaml:"listen_path,omitempty"`
	TokenEnv   string `yaml:"token_env,omitempty"`
	TokenRef   string `yaml:"token_ref,omitempty"`
	Token      string `yaml:"-"`
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

func (in Input) Observes() bool {
	return in.Protocol == OneBot11 && in.IdentitySource == RealQQ && (in.Role == Observe || len(in.SilentGroups) != 0)
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
	Endpoint `yaml:",inline"`
	Protocol string `yaml:"protocol"`
}

type Route struct {
	From           string   `yaml:"from"`
	To             string   `yaml:"to,omitempty"`
	Outputs        []string `yaml:"outputs,omitempty"`
	Events         []string `yaml:"events,omitempty"`
	Groups         []string `yaml:"groups,omitempty"`
	Users          []string `yaml:"users,omitempty"`
	IncludePrivate bool     `yaml:"include_private,omitempty"`
	AllowActions   bool     `yaml:"allow_actions"`
	ImplicitReply  string   `yaml:"implicit_reply,omitempty"`
	Passthrough    bool     `yaml:"passthrough,omitempty"`
}

func (r Route) Targets() []string {
	if r.To != "" {
		return []string{r.To}
	}
	return r.Outputs
}

type Limits struct {
	FrameBytes      int64         `yaml:"frame_bytes"`
	EventQueue      int           `yaml:"event_queue"`
	APIQueue        int           `yaml:"api_queue"`
	PendingPerPeer  int           `yaml:"pending_per_peer"`
	PendingPerInput int           `yaml:"pending_per_input"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	APITimeout      time.Duration `yaml:"api_timeout"`
	EventTTL        time.Duration `yaml:"event_ttl"`
	DedupTTL        time.Duration `yaml:"dedup_ttl"`
	DedupSize       int           `yaml:"dedup_size"`
	PingInterval    time.Duration `yaml:"ping_interval"`
}

type Config struct {
	Listen   string   `yaml:"listen"`
	PublicWS string   `yaml:"public_ws_url,omitempty"`
	LogLevel string   `yaml:"log_level"`
	Inputs   []Input  `yaml:"inputs"`
	Outputs  []Output `yaml:"outputs"`
	Routes   []Route  `yaml:"routes"`
	Limits   Limits   `yaml:"limits"`
	Storage  Storage  `yaml:"storage"`
	Matching Matching `yaml:"matching"`
	Admin    Admin    `yaml:"admin"`
	Logging  Logging  `yaml:"logging"`
}

type Admin struct {
	TokenEnv    string `yaml:"token_env,omitempty"`
	TokenRef    string `yaml:"token_ref,omitempty"`
	Token       string `yaml:"-"`
	AllowRemote bool   `yaml:"allow_remote,omitempty"`
}

type Logging struct {
	File    string `yaml:"file,omitempty"`
	MaxSize int64  `yaml:"max_size_bytes"`
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
		return nil, fmt.Errorf("configuration exceeds 1 MiB")
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
	secrets, err := readCredentials(cfg, path, overrides)
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
		Storage: Storage{MessageTTL: 24 * time.Hour, ReplyWindow: 30 * time.Second},
		Logging: Logging{MaxSize: 10 << 20, Keep: 3},
		Matching: Matching{Mode: "off", PairWindow: 2 * time.Second, EventTTL: 5 * time.Second,
			EvidenceTTL: 24 * time.Hour, Successes: 3, GroupSuccesses: 3, GroupSenders: 2, Distance: 4},
		Limits: Limits{
			FrameBytes: 1 << 20, EventQueue: 128, APIQueue: 32,
			PendingPerPeer: 32, PendingPerInput: 128,
			WriteTimeout: 5 * time.Second, APITimeout: 12 * time.Second,
			EventTTL: 15 * time.Second, DedupTTL: time.Minute, DedupSize: 4096,
			PingInterval: 20 * time.Second,
		},
	}
	if portableGatewayConfig(path) {
		cfg.Storage.SQLite = "../data/hashinex.sqlite"
		cfg.Logging.File = "../data/hashinex.jsonl"
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("configuration must contain one YAML document")
	}
	return &cfg, nil
}

func (cfg *Config) validate(secrets map[string]string) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen must be a host and port")
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return fmt.Errorf("listen must have a numeric port in 1..65535")
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log_level")
	}
	if cfg.PublicWS != "" {
		address, err := wsURL(cfg.PublicWS)
		if err != nil || (address.Path != "" && address.Path != "/") || address.RawQuery != "" {
			return fmt.Errorf("public_ws_url must be a ws or wss origin")
		}
	}
	if cfg.Admin.TokenEnv != "" || cfg.Admin.TokenRef != "" {
		cfg.Admin.Token = credential(secrets, cfg.Admin.TokenRef, cfg.Admin.TokenEnv)
		if len(cfg.Admin.Token) < 16 || strings.ContainsAny(cfg.Admin.Token, " \t\r\n") {
			return fmt.Errorf("admin token must contain at least 16 non-whitespace characters")
		}
	}
	if cfg.Admin.AllowRemote && cfg.Admin.Token == "" {
		return fmt.Errorf("remote admin access requires a management token")
	}
	if cfg.Logging.MaxSize < 4096 || cfg.Logging.MaxSize > 1<<30 || cfg.Logging.Keep < 1 || cfg.Logging.Keep > 20 {
		return fmt.Errorf("invalid file logging limits")
	}
	limits := cfg.Limits
	if limits.FrameBytes < 1024 || limits.FrameBytes > 16<<20 ||
		limits.EventQueue < 1 || limits.EventQueue > 65536 ||
		limits.APIQueue < 1 || limits.APIQueue > 65536 ||
		limits.PendingPerPeer < 1 || limits.PendingPerPeer > limits.APIQueue ||
		limits.PendingPerInput < limits.PendingPerPeer || limits.PendingPerInput > 65536 ||
		limits.DedupSize < 1 || limits.DedupSize > 1<<20 {
		return fmt.Errorf("invalid frame, queue, pending or dedup limits")
	}
	if limits.WriteTimeout < 25*time.Millisecond || limits.APITimeout < 25*time.Millisecond ||
		limits.EventTTL <= 0 || limits.DedupTTL <= 0 || limits.PingInterval <= 0 {
		return fmt.Errorf("all time limits must be positive")
	}
	if cfg.Storage.MessageTTL <= 0 || cfg.Storage.ReplyWindow <= 0 || cfg.Storage.ReplyWindow > cfg.Storage.MessageTTL {
		return fmt.Errorf("invalid storage time limits")
	}
	inputs := make(map[string]Input, len(cfg.Inputs))
	apps := make(map[string]bool)
	listeners := make(map[string]bool)
	for i := range cfg.Inputs {
		input := &cfg.Inputs[i]
		if _, exists := inputs[input.ID]; exists {
			return fmt.Errorf("duplicate input %q", input.ID)
		}
		if input.Role == "" {
			input.Role = Active
		}
		if input.Role != Active && input.Role != Observe {
			return fmt.Errorf("invalid role for input %q", input.ID)
		}
		if input.Protocol == OneBot11 && input.Role == Observe && input.IdentitySource != RealQQ {
			return fmt.Errorf("observe input %q must provide real QQ IDs", input.ID)
		}
		if !validID(input.SelfID) {
			return fmt.Errorf("invalid self_id for input %q", input.ID)
		}
		for _, ids := range [][]string{input.SilentGroups, input.BusinessGroups, input.GroupIDs, input.PrivateQQIDs} {
			for _, id := range ids {
				if !validQQID(id) {
					return fmt.Errorf("input %q group scopes and allowlists require real QQ group numbers or QQ IDs", input.ID)
				}
			}
		}
		switch input.Protocol {
		case OneBot11:
			if input.IdentitySource != RealQQ && input.IdentitySource != Virtual {
				return fmt.Errorf("input %q requires an explicit identity_source", input.ID)
			}
			if input.Mode != ReverseListen && input.Mode != ForwardDial {
				return fmt.Errorf("invalid OneBot WS mode for input %q", input.ID)
			}
			if input.IdentitySource == Virtual && cfg.Storage.SQLite == "" {
				return fmt.Errorf("storage.sqlite is required for virtual OneBot input %q", input.ID)
			}
			if err := input.Endpoint.validate(host, port, input.Mode == ReverseListen, secrets); err != nil {
				return err
			}
			if input.ListenPath != "" {
				if !validListenPath(input.ListenPath) || listeners[input.ListenPath] {
					return fmt.Errorf("invalid or duplicate listener path for input %q", input.ID)
				}
				listeners[input.ListenPath] = true
			}
		case QQOfficial:
			if !namePattern.MatchString(input.ID) || !validID(input.AppID) || apps[input.AppID] ||
				input.Mode != "" || input.URL != "" || input.ListenPath != "" || input.TokenEnv != "" || input.TokenRef != "" {
				return fmt.Errorf("invalid or duplicate official QQ input %q", input.ID)
			}
			if cfg.Storage.SQLite == "" {
				return fmt.Errorf("storage.sqlite is required for official QQ inputs")
			}
			input.Secret = credential(secrets, input.SecretRef, input.SecretEnv)
			if input.Secret == "" {
				return fmt.Errorf("AppSecret is required for input %q", input.ID)
			}
			if input.Events != "gateway" && input.Events != "webhook" {
				return fmt.Errorf("official input %q requires events: gateway or webhook", input.ID)
			}
			if input.APIOrigin != "" && input.APIOrigin != "bot" && input.APIOrigin != "sgroup" {
				return fmt.Errorf("official input %q has invalid api_origin", input.ID)
			}
			if input.PrivateBindGroup != "" && !validID(input.PrivateBindGroup) {
				return fmt.Errorf("official input %q requires a numeric QQ group number for private_bind_group", input.ID)
			}
			if input.Intents == 0 {
				input.Intents = 1 << 25
			}
			apps[input.AppID] = true
		default:
			return fmt.Errorf("unsupported protocol for input %q", input.ID)
		}
		inputs[input.ID] = *input
	}
	outputs := make(map[string]Output, len(cfg.Outputs))
	coreURLs := make(map[string]string)
	for i := range cfg.Outputs {
		output := &cfg.Outputs[i]
		if _, exists := outputs[output.ID]; exists {
			return fmt.Errorf("duplicate output %q", output.ID)
		}
		switch output.Protocol {
		case OneBot11:
			if output.Mode != ForwardListen && output.Mode != ReverseDial {
				return fmt.Errorf("output %q has an unsupported WS mode", output.ID)
			}
			if err := output.Endpoint.validate(host, port, output.Mode == ForwardListen, secrets); err != nil {
				return err
			}
			if output.ListenPath != "" && !validListenPath(strings.ReplaceAll(output.ListenPath, "{input}", "input")) {
				return fmt.Errorf("invalid listener path for output %q", output.ID)
			}
		case GScore:
			if !namePattern.MatchString(output.ID) || output.Mode != "" || output.ListenPath != "" {
				return fmt.Errorf("invalid gscore output %q", output.ID)
			}
			output.Token = credential(secrets, output.TokenRef, output.TokenEnv)
			if output.TokenEnv != "" && output.Token == "" {
				return fmt.Errorf("missing gscore token for %q", output.ID)
			}
			u, err := wsURL(output.URL)
			if err != nil || strings.ContainsAny(output.URL, "{}") {
				return fmt.Errorf("invalid gscore WebSocket URL for %q", output.ID)
			}
			address := strings.ToLower(u.Host) + u.EscapedPath()
			if existing := coreURLs[address]; existing != "" {
				return fmt.Errorf("gscore outputs %q and %q have the same address; select one output in both routes to share it", existing, output.ID)
			}
			coreURLs[address] = output.ID
		default:
			return fmt.Errorf("output %q has an unsupported protocol", output.ID)
		}
		outputs[output.ID] = *output
	}
	routes := make(map[[2]string]bool, len(cfg.Routes))
	coreBots := make(map[[2]string]bool)
	for _, route := range cfg.Routes {
		input, exists := inputs[route.From]
		if !exists || len(route.Targets()) == 0 || (route.To != "" && len(route.Outputs) != 0) {
			return fmt.Errorf("route %q requires a configured input and at least one output", route.From)
		}
		for _, target := range route.Targets() {
			output, exists := outputs[target]
			key := [2]string{route.From, target}
			if !exists || routes[key] {
				return fmt.Errorf("unknown or duplicate route %q to %q", route.From, target)
			}
			routes[key] = true
			if output.ListenPath != "" {
				path := strings.ReplaceAll(output.ListenPath, "{input}", route.From)
				if !validListenPath(path) || listeners[path] {
					return fmt.Errorf("invalid or duplicate listener path for route %q to %q", route.From, target)
				}
				listeners[path] = true
			}
			if output.Protocol == GScore {
				bot := [2]string{target, input.SelfID}
				if coreBots[bot] {
					return fmt.Errorf("inputs sharing gscore output %q must have distinct self_id values", target)
				}
				coreBots[bot] = true
			}
		}
		if route.ImplicitReply != "" && route.ImplicitReply != "unique-recent" {
			return fmt.Errorf("invalid implicit_reply in route %q to %q", route.From, route.To)
		}
		for _, kind := range route.Events {
			switch kind {
			case "message", "message_sent", "notice", "request":
			default:
				return fmt.Errorf("unsupported route event type %q", kind)
			}
		}
		for _, ids := range [][]string{route.Groups, route.Users} {
			for _, id := range ids {
				if !validID(id) || (input.Protocol == QQOfficial && !validQQID(id)) {
					return fmt.Errorf("invalid filter ID in route %q to %q", route.From, route.To)
				}
			}
		}
	}
	for _, in := range cfg.Inputs {
		if in.Mode == ForwardDial && dialHitsListener(in.URL, host, port, listeners) {
			return fmt.Errorf("dial endpoint %q points back to this gateway", in.ID)
		}
	}
	for _, out := range cfg.Outputs {
		if out.Mode == ReverseDial && dialHitsListener(out.URL, host, port, listeners) {
			return fmt.Errorf("dial endpoint %q points back to this gateway", out.ID)
		}
	}
	m := cfg.Matching
	switch m.Mode {
	case "off":
	case "shadow", "auto":
		if cfg.Storage.SQLite == "" || len(apps) == 0 {
			return fmt.Errorf("matching requires storage.sqlite and an official QQ input")
		}
		observers := 0
		for _, input := range cfg.Inputs {
			if input.Observes() {
				observers++
			}
		}
		if observers == 0 {
			return fmt.Errorf("matching requires a real QQ observation input")
		}
	default:
		return fmt.Errorf("invalid matching mode")
	}
	if m.PairWindow < 25*time.Millisecond || m.PairWindow > 10*time.Second ||
		m.EventTTL <= 2*m.PairWindow || m.EventTTL > time.Minute ||
		m.EvidenceTTL < m.EventTTL || m.EvidenceTTL > 30*24*time.Hour ||
		m.Successes < 2 || m.Successes > 100 ||
		m.GroupSuccesses < 2 || m.GroupSuccesses > 100 || m.GroupSenders < 1 || m.GroupSenders > m.GroupSuccesses ||
		m.Distance < 0 || m.Distance > 16 || len(m.DefaultHashes) > 256 {
		return fmt.Errorf("invalid matching limits")
	}
	for _, id := range m.ObservationInputs {
		if !inputs[id].Observes() {
			return fmt.Errorf("matching input %q is not an observation input", id)
		}
	}
	for _, hash := range m.DefaultHashes {
		if _, err := strconv.ParseUint(hash, 16, 64); err != nil || len(hash) != 16 {
			return fmt.Errorf("default_avatar_phashes must contain 16 digit hexadecimal hashes")
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
	if !namePattern.MatchString(endpoint.ID) {
		return fmt.Errorf("invalid endpoint ID %q", endpoint.ID)
	}
	if endpoint.TokenEnv != "" || endpoint.TokenRef != "" {
		endpoint.Token = credential(secrets, endpoint.TokenRef, endpoint.TokenEnv)
		if endpoint.Token == "" || strings.ContainsAny(endpoint.Token, " \t\r\n") {
			return fmt.Errorf("missing or invalid token in %q for %q", endpoint.TokenEnv, endpoint.ID)
		}
	}
	if listening {
		if endpoint.URL != "" {
			return fmt.Errorf("listener %q cannot specify a dial URL", endpoint.ID)
		}
		if !loopback(listenHost) && endpoint.Token == "" {
			return fmt.Errorf("listener %q requires a token for non-loopback access", endpoint.ID)
		}
		return nil
	}
	u, err := wsURL(endpoint.URL)
	if err != nil || endpoint.ListenPath != "" {
		return fmt.Errorf("invalid dial settings for %q", endpoint.ID)
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
		return fmt.Errorf("dial endpoint %q points back to this gateway", endpoint.ID)
	}
	return nil
}

func wsURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "ws" && u.Scheme != "wss") ||
		u.User != nil || u.Fragment != "" || u.Query().Has("access_token") || u.Query().Has("token") {
		return nil, fmt.Errorf("expected a ws or wss URL without credentials")
	}
	return u, nil
}

func dialHitsListener(address, host, port string, paths map[string]bool) bool {
	u, _ := url.Parse(address)
	return u != nil && (loopback(u.Hostname()) || u.Hostname() == host) &&
		u.Port() == port && (paths[u.Path] || strings.HasPrefix(u.Path, "/_hashinex/"))
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
