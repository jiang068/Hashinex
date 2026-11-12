package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"hashinex/internal/config"
	"hashinex/internal/transport"
)

var dialClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	expected, received := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(expected[:], received[:]) == 1
}

func (g *Gateway) track() bool {
	g.lifecycle.Lock()
	defer g.lifecycle.Unlock()
	if g.closing || g.draining.Load() || g.ctx.Err() != nil {
		return false
	}
	g.workers.Add(1)
	return true
}

func (g *Gateway) acceptInput(w http.ResponseWriter, r *http.Request) {
	in := g.inputs[r.PathValue("input")]
	if in == nil || in.cfg.Mode != config.ReverseListen || (in.cfg.ListenPath != "" && in.cfg.ListenPath != r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if !authorized(r, in.cfg.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-Self-ID") != in.cfg.SelfID {
		http.Error(w, "unexpected X-Self-ID", http.StatusForbidden)
		return
	}
	if !strings.EqualFold(r.Header.Get("X-Client-Role"), "Universal") {
		http.Error(w, "Universal role required", http.StatusBadRequest)
		return
	}
	if !g.track() {
		http.Error(w, "gateway stopping", http.StatusServiceUnavailable)
		return
	}
	defer g.workers.Done()
	conn, err := websocket.Accept(w, r, nil)
	if err == nil {
		g.runInput(in, conn)
	}
}

func (g *Gateway) acceptOutput(w http.ResponseWriter, req *http.Request) {
	r := g.routes[[2]string{req.PathValue("output"), req.PathValue("input")}]
	if r == nil || r.output.Mode != config.ForwardListen ||
		(r.output.ListenPath != "" && strings.ReplaceAll(r.output.ListenPath, "{input}", r.cfg.From) != req.URL.Path) {
		http.NotFound(w, req)
		return
	}
	g.upgradeOutput(w, req, r, r.output.Token)
}

func (g *Gateway) acceptManagedOutput(w http.ResponseWriter, req *http.Request) {
	r := g.routes[[2]string{req.PathValue("output"), req.PathValue("input")}]
	if r == nil || r.output.Protocol != config.GScore {
		http.NotFound(w, req)
		return
	}
	g.upgradeOutput(w, req, r, g.internalToken)
}

func (g *Gateway) upgradeOutput(w http.ResponseWriter, req *http.Request, r *route, token string) {
	if !authorized(req, token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !g.track() {
		http.Error(w, "gateway stopping", http.StatusServiceUnavailable)
		return
	}
	defer g.workers.Done()
	conn, err := websocket.Accept(w, req, nil)
	if err == nil {
		g.runOutput(r, conn)
	}
}

func (g *Gateway) runInput(in *input, conn *websocket.Conn) {
	log := g.log.With("input", in.cfg.ID)
	g.mu.Lock()
	if in.peer != nil || g.ctx.Err() != nil {
		g.mu.Unlock()
		_ = conn.Close(websocket.StatusPolicyViolation, "input already connected or stopping")
		return
	}
	peer := transport.NewPeer(g.ctx, conn, g.limits, log)
	in.peer = peer
	g.mu.Unlock()
	log.Info("upstream connected", "self_id", in.cfg.SelfID)
	defer g.detachInput(in, peer)
	if in.cfg.Observes() {
		ctx, cancel := context.WithCancel(g.ctx)
		defer cancel()
		g.workers.Go(func() { g.pollObserverGroups(ctx, in, peer) })
	}
	peer.Run(func(raw []byte) { g.fromInput(in, peer, raw) }, func(int) {})
}

func (g *Gateway) runOutput(r *route, conn *websocket.Conn) {
	log := g.log.With("input", r.cfg.From, "output", r.cfg.To)
	g.mu.Lock()
	if r.peer != nil || g.ctx.Err() != nil {
		g.mu.Unlock()
		_ = conn.Close(websocket.StatusPolicyViolation, "output already connected or stopping")
		return
	}
	peer := transport.NewPeer(g.ctx, conn, g.limits, log)
	connected := time.Now()
	lifecycle, _ := json.Marshal(map[string]any{
		"time": connected.Unix(), "self_id": json.Number(r.input.cfg.SelfID),
		"post_type": "meta_event", "meta_event_type": "lifecycle", "sub_type": "connect",
	})
	peer.SendEvent(lifecycle, connected.Add(g.limits.EventTTL))
	r.recent = nil
	r.recentAt = 0
	r.incompleteUntil = time.Time{}
	r.peer = peer
	g.mu.Unlock()
	log.Info("downstream connected")
	defer g.detachOutput(r, peer)
	peer.Run(func(raw []byte) { g.fromOutput(r, peer, raw) },
		func(count int) { g.reportUndelivered(r, count) })
}

func (g *Gateway) dialInput(in *input) {
	g.dial(in.cfg.Endpoint, nil, g.log.With("input", in.cfg.ID), func(conn *websocket.Conn) {
		g.runInput(in, conn)
	})
}

func (g *Gateway) dialOutput(r *route) {
	headers := make(http.Header)
	headers.Set("X-Self-ID", r.input.cfg.SelfID)
	headers.Set("X-Client-Role", "Universal")
	log := g.log.With("input", r.cfg.From, "output", r.cfg.To)
	g.dial(r.output.Endpoint, headers, log, func(conn *websocket.Conn) {
		g.runOutput(r, conn)
	})
}

func (g *Gateway) dial(endpoint config.Endpoint, headers http.Header, log *slog.Logger, run func(*websocket.Conn)) {
	if headers == nil {
		headers = make(http.Header)
	}
	if endpoint.Token != "" {
		headers.Set("Authorization", "Bearer "+endpoint.Token)
	}
	const stable = 30 * time.Second
	delay := time.Second
	var attempt int
	for g.ctx.Err() == nil && !g.draining.Load() {
		ctx, cancel := context.WithTimeout(g.ctx, g.limits.WriteTimeout)
		conn, response, err := websocket.Dial(ctx, endpoint.URL, &websocket.DialOptions{
			HTTPClient: dialClient, HTTPHeader: headers,
		})
		cancel()
		if err == nil {
			start := time.Now()
			run(conn)
			if time.Since(start) >= stable {
				delay, attempt = time.Second, 0
			}
		} else if g.ctx.Err() == nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			log.Warn("websocket dial failed", "http_status", status)
		}
		if g.ctx.Err() != nil {
			return
		}
		attempt++
		pause := delay + rand.N(delay/4)
		log.Info("websocket reconnect scheduled", "attempt", attempt, "delay", pause)
		timer := time.NewTimer(pause)
		select {
		case <-g.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

var (
	errSlowOutput = errors.New("downstream event queue full")
	errSlowAPI    = errors.New("downstream API response queue full")
)
