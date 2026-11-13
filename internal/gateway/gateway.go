package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/gscore"
	"hashinex/internal/matching"
	"hashinex/internal/onebot"
	"hashinex/internal/qqofficial"
	"hashinex/internal/store"
	"hashinex/internal/tempfiles"
	"hashinex/internal/transport"
)

type input struct {
	cfg            config.Input
	livePolicy     atomic.Pointer[config.Input]
	timing         *transport.Timing
	observes       bool
	peer           *transport.Peer
	events         chan event
	routes         []*route
	pending        int
	official       *qqofficial.Client
	officialEvents chan officialEnvelope
	jobs           chan officialJob
	profileJobs    chan officialJob
	privatePrompts chan privatePrompt
	virtualJobs    chan virtualJob
	virtualReplies chan virtualReply
	online         atomic.Bool
	observerGroups map[string]time.Time
	groupListAt    time.Time
}

type event struct {
	raw           []byte
	original      []byte
	originalFrame *onebot.Frame
	frame         onebot.Frame
	deadline      time.Time
	ref           *store.Message
}

type route struct {
	cfg             config.Route
	livePolicy      atomic.Pointer[config.Route]
	timing          *transport.Timing
	input           *input
	groupSender     *input
	output          config.Output
	peer            *transport.Peer
	pending         int
	recent          []receipt
	recentAt        int
	incompleteUntil time.Time
	undelivered     atomic.Uint64
}

type call struct {
	request    string
	policy     *config.Route
	input      *input
	upstream   *transport.Peer
	route      *route
	downstream *transport.Peer
	echo       json.RawMessage
	action     string
	start      time.Time
	deadline   time.Time
}

type counters struct {
	received     atomic.Uint64
	enqueued     atomic.Uint64
	dropped      atomic.Uint64
	duplicate    atomic.Uint64
	observed     atomic.Uint64
	timeout      atomic.Uint64
	routed       atomic.Uint64
	passthrough  atomic.Uint64
	routingNanos atomic.Uint64
	perSecond    atomic.Uint64
}

type Gateway struct {
	ctx            context.Context
	stop           context.CancelFunc
	log            *slog.Logger
	listen         string
	generation     string
	limits         config.Limits
	storage        config.Storage
	shares         *tempfiles.Service
	shareTTL       time.Duration
	store          *store.Store
	matcher        *matching.Engine
	matching       config.Matching
	privateAvatars *matching.AvatarVerifier
	privateChecks  chan privateCheck
	admin          *admin
	inputs         map[string]*input
	routes         map[[2]string]*route
	workers        sync.WaitGroup
	// lifecycle serializes shutdown with registration of HTTP-upgraded connections.
	lifecycle     sync.Mutex
	closing       bool
	draining      atomic.Bool
	eventWork     atomic.Int64
	mu            sync.Mutex
	pending       map[string]call
	shareLookups  map[string]shareLookup
	prefix        string
	internalToken string
	serial        uint64
	stats         counters
}

func New(parent context.Context, cfg *config.Config, db *store.Store, log *slog.Logger, shares ...*tempfiles.Service) *Gateway {
	ctx, stop := context.WithCancel(parent)
	var random [32]byte
	rand.Read(random[:])
	g := &Gateway{
		ctx: ctx, stop: stop, log: log, limits: cfg.Limits, storage: cfg.Storage, store: db, shareTTL: cfg.TempFiles.TTL,
		listen: cfg.Listen, generation: time.Now().UTC().Format(time.RFC3339Nano),
		matching:      cfg.Matching,
		prefix:        hex.EncodeToString(random[:16]),
		internalToken: hex.EncodeToString(random[16:]),
		inputs:        make(map[string]*input), routes: make(map[[2]string]*route),
		pending: make(map[string]call),
	}
	if g.limits.GScoreFrameBytes == 0 {
		g.limits.GScoreFrameBytes = config.DefaultGScoreFrameBytes
	}
	if len(shares) != 0 {
		g.shares = shares[0]
		if g.shares != nil {
			g.shareLookups = make(map[string]shareLookup)
		}
	}
	if cfg.Matching.Mode == "shadow" || cfg.Matching.Mode == "auto" {
		g.matcher = matching.New(cfg.Matching, db, log)
		g.workers.Go(func() { g.matcher.Run(g.ctx) })
	}
	outputs := make(map[string]config.Output, len(cfg.Outputs))
	outputTiming := make(map[string]*transport.Timing, len(cfg.Outputs))
	for _, output := range cfg.Outputs {
		outputs[output.ID] = output
		outputTiming[output.ID] = transport.NewTiming(heartbeatInterval(output.Endpoint, g.limits.PingInterval), output.ReconnectInterval)
	}
	for _, entry := range cfg.Inputs {
		in := &input{cfg: entry, observes: cfg.Matching.Observes(entry), events: make(chan event, cfg.Limits.EventQueue)}
		in.timing = transport.NewTiming(heartbeatInterval(entry.Endpoint, g.limits.PingInterval), entry.ReconnectInterval)
		if entry.Protocol == config.QQOfficial {
			in.official = qqofficial.NewClient(entry.AppID, entry.Secret, entry.APIOrigin, cfg.Limits.APITimeout)
			in.officialEvents = make(chan officialEnvelope, cfg.Limits.EventQueue)
			in.jobs = make(chan officialJob, cfg.Limits.APIQueue)
			in.profileJobs = make(chan officialJob, cfg.Limits.APIQueue)
			if entry.PrivateEnabled() {
				in.privatePrompts = make(chan privatePrompt, cfg.Limits.APIQueue)
				if g.privateChecks == nil {
					g.privateChecks = make(chan privateCheck, cfg.Limits.APIQueue)
					if g.matcher != nil {
						g.privateAvatars = g.matcher.AvatarVerifier()
					} else {
						g.privateAvatars = matching.NewAvatarVerifier(cfg.Matching.DefaultHashes)
					}
				}
			}
		} else if entry.IdentitySource == config.Virtual {
			in.virtualJobs = make(chan virtualJob, cfg.Limits.APIQueue)
			in.virtualReplies = make(chan virtualReply, cfg.Limits.APIQueue)
		}
		g.inputs[entry.ID] = in
	}
	for _, entry := range cfg.Routes {
		source := g.inputs[entry.From]
		groupSender := g.inputs[entry.SendGroupVia]
		for _, target := range entry.Targets() {
			selected := entry
			selected.To = target
			r := &route{cfg: selected, input: source, groupSender: groupSender, output: outputs[target], timing: outputTiming[target]}
			source.routes = append(source.routes, r)
			g.routes[[2]string{target, entry.From}] = r
		}
	}
	if g.privateChecks != nil {
		g.workers.Go(g.runPrivateChecks)
	}
	for _, in := range g.inputs {
		if in.official != nil {
			g.startOfficial(in)
			continue
		}
		g.workers.Go(func() { g.processEvents(in) })
		if in.virtualJobs != nil {
			g.startVirtual(in)
		}
		if in.cfg.Mode == config.ForwardDial {
			g.workers.Go(func() { g.dialInput(in) })
		}
	}
	coreRoutes := make(map[string][]*route)
	for _, r := range g.routes {
		if r.output.Protocol == config.GScore {
			coreRoutes[r.output.ID] = append(coreRoutes[r.output.ID], r)
		} else if r.output.Mode == config.ReverseDial {
			g.workers.Go(func() { g.dialOutput(r) })
		}
	}
	for _, routes := range coreRoutes {
		g.workers.Go(func() { g.runGScore(cfg.Listen, routes) })
	}
	g.workers.Go(g.sweep)
	if db != nil {
		g.workers.Go(g.cleanStore)
	}
	return g
}

func (g *Gateway) Close() {
	g.lifecycle.Lock()
	g.closing = true
	g.stop()
	g.lifecycle.Unlock()
	g.workers.Wait()
}

func (g *Gateway) Drain(ctx context.Context) {
	g.draining.Store(true)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	quiet := false
	for {
		select {
		case <-ctx.Done():
			g.log.Warn("shutdown drain deadline reached")
			return
		case <-tick.C:
			g.mu.Lock()
			idle := len(g.pending) == 0 && g.eventWork.Load() == 0
			for _, in := range g.inputs {
				idle = idle && (in.peer == nil || in.peer.PendingWrites() == 0)
			}
			for _, r := range g.routes {
				idle = idle && (r.peer == nil || r.peer.PendingWrites() == 0)
			}
			g.mu.Unlock()
			if idle && quiet {
				return
			}
			quiet = idle
		}
	}
}

func (g *Gateway) beginEvent() bool {
	g.eventWork.Add(1)
	if g.draining.Load() {
		g.eventWork.Add(-1)
		return false
	}
	return true
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /onebot/in/{input}", g.acceptInput)
	mux.HandleFunc("GET /onebot/out/{output}/{input}", g.acceptOutput)
	mux.HandleFunc("GET /_hashinex/gscore/{output}/{input}", g.acceptManagedOutput)
	for id, in := range g.inputs {
		if in.cfg.ListenPath != "" {
			mux.HandleFunc("GET "+in.cfg.ListenPath, func(w http.ResponseWriter, req *http.Request) {
				req.SetPathValue("input", id)
				g.acceptInput(w, req)
			})
		}
	}
	for _, r := range g.routes {
		if r.output.ListenPath != "" {
			path := strings.ReplaceAll(r.output.ListenPath, "{input}", r.cfg.From)
			mux.HandleFunc("GET "+path, func(w http.ResponseWriter, req *http.Request) {
				req.SetPathValue("output", r.cfg.To)
				req.SetPathValue("input", r.cfg.From)
				g.acceptOutput(w, req)
			})
		}
	}
	mux.HandleFunc("GET /healthz", g.health)
	mux.HandleFunc("POST /qq/webhook/{input}", g.webhook)
	g.registerAdmin(mux)
	return mux
}

func (g *Gateway) runGScore(listen string, routes []*route) {
	host, port, _ := net.SplitHostPort(listen)
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() == nil {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}
	output := routes[0].output
	local := "ws://" + net.JoinHostPort(host, port) + "/_hashinex/gscore/" + output.ID + "/"
	targets := make([]gscore.Target, 0, len(routes))
	for _, r := range routes {
		targets = append(targets, gscore.Target{SelfID: r.input.cfg.SelfID, URL: local + r.cfg.From, Token: g.internalToken})
	}
	link := gscore.ManagedConnection(output.ID, output.URL, output.Token, targets, g.limits.GScoreFrameBytes, routes[0].timing)
	gscore.Run(g.ctx, []gscore.Connection{link}, g.log)
}

func (g *Gateway) health(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	var inputs, outputs int
	coreOnline := make(map[string]bool)
	for _, in := range g.inputs {
		if in.peer != nil || in.online.Load() {
			inputs++
		}
	}
	for _, r := range g.routes {
		if r.peer != nil {
			if r.output.Protocol == config.GScore {
				if coreOnline[r.output.ID] {
					continue
				}
				coreOnline[r.output.ID] = true
			}
			outputs++
		}
	}
	pending := len(g.pending)
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	status := map[string]any{
		"generation":    g.generation,
		"online_inputs": inputs, "online_outputs": outputs, "pending": pending,
		"received": g.stats.received.Load(), "enqueued": g.stats.enqueued.Load(),
		"dropped": g.stats.dropped.Load(), "duplicates": g.stats.duplicate.Load(),
		"observed": g.stats.observed.Load(), "api_timeouts": g.stats.timeout.Load(),
		"passthrough":         g.stats.passthrough.Load(),
		"messages_per_second": g.stats.perSecond.Load(),
	}
	if routed := g.stats.routed.Load(); routed > 0 {
		status["routing_latency_avg_ms"] = float64(g.stats.routingNanos.Load()) / float64(routed) / 1e6
	}
	if g.matcher != nil {
		for key, value := range g.matcher.Stats() {
			status[key] = value
		}
	}
	_ = json.NewEncoder(w).Encode(status)
}

func (g *Gateway) fromInput(in *input, peer *transport.Peer, raw []byte) {
	var frame onebot.Frame
	if err := json.Unmarshal(raw, &frame); err != nil {
		g.log.Warn("invalid upstream frame", "input", in.cfg.ID)
		return
	}
	if frame.PostType == "" {
		if g.acceptObserverGroups(in, peer, frame) {
			return
		}
		status := string(frame.Status)
		if frame.RetCode != nil && (status == `"ok"` || status == `"failed"` || status == `"async"`) {
			if g.acceptShareLookup(peer, frame) {
				return
			}
			if in.virtualReplies != nil {
				select {
				case in.virtualReplies <- virtualReply{peer: peer, echo: frame.Echo, raw: raw}:
				default:
					peer.Stop(errors.New("upstream API response queue full"))
				}
			} else {
				g.finish(in, peer, frame.Echo, raw)
			}
		}
		return
	}
	if onebot.ID(frame.SelfID) != in.cfg.SelfID {
		peer.Stop(errors.New("upstream event self_id mismatch"))
		return
	}
	switch frame.PostType {
	case "message", "message_sent", "notice", "request":
	default:
		return
	}
	if !g.beginEvent() {
		if !in.policy().Silent(onebot.ID(frame.GroupID), "") {
			g.stats.dropped.Add(1)
		}
		return
	}
	g.stats.received.Add(1)
	e := event{raw: raw, frame: frame, deadline: time.Now().Add(g.limits.EventTTL)}
	select {
	case in.events <- e:
	default:
		g.eventWork.Add(-1)
		g.stats.dropped.Add(1)
		if in.policy().Silent(onebot.ID(frame.GroupID), "") {
			return
		}
		g.log.Warn("upstream event queue full", "input", in.cfg.ID)
		peer.Stop(errors.New("upstream event queue full"))
	}
}

func (g *Gateway) reportUndelivered(r *route, count int) {
	r.undelivered.Add(uint64(count))
	g.stats.dropped.Add(uint64(count))
	g.log.Warn("downstream events undelivered", "input", r.cfg.From, "output", r.cfg.To, "count", count)
}

func (g *Gateway) nextKey() string {
	g.serial++
	return g.prefix + "-" + strconv.FormatUint(g.serial, 36)
}
