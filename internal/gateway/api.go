package gateway

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"hashinex/internal/onebot"
	"hashinex/internal/transport"
)

var actionPattern = regexp.MustCompile(`^[a-zA-Z0-9_.]{1,128}$`)

func (g *Gateway) fromOutput(r *route, peer *transport.Peer, raw []byte) {
	var frame onebot.Frame
	if err := json.Unmarshal(raw, &frame); err != nil || !actionPattern.MatchString(frame.Action) || frame.PostType != "" {
		g.respond(peer, onebot.Failure(frame.Echo, 1400, "expected a OneBot API action"))
		return
	}
	if !frame.TargetMatches(r.input.cfg.SelfID) {
		g.respond(peer, onebot.Failure(frame.Echo, 1403, "request target conflicts with the connection's self_id"))
		return
	}
	if !r.cfg.AllowActions {
		g.respond(peer, onebot.Failure(frame.Echo, 1403, "API actions are disabled for this route"))
		return
	}
	if r.input.official == nil {
		var params actionParams
		if json.Unmarshal(frame.Params, &params) == nil {
			group := onebot.ID(params.GroupID)
			silent := group != "" && r.input.cfg.Silent(group, "")
			if group != "" && r.cfg.Passthrough {
				silent = g.oneBotGroupSilent(r.input, group, time.Now().Add(g.limits.APITimeout))
			}
			if group != "" && (silent || !r.allowsConversation("group", group)) {
				g.respond(peer, onebot.Failure(frame.Echo, 1403, "group is outside the route scope or in silent mode"))
				return
			}
		}
	}
	c := call{
		input: r.input, route: r, downstream: peer, echo: frame.Echo, action: frame.Action,
		start: time.Now(),
	}
	c.deadline = c.start.Add(g.limits.APITimeout)
	g.mu.Lock()
	if r.peer != peer {
		g.mu.Unlock()
		return
	}
	if g.draining.Load() {
		g.mu.Unlock()
		g.respond(peer, onebot.Failure(frame.Echo, 1503, "gateway shutting down"))
		return
	}
	c.upstream = r.input.peer
	if c.upstream == nil && c.input.official == nil {
		g.mu.Unlock()
		g.respond(peer, onebot.Failure(frame.Echo, 1503, "upstream offline"))
		return
	}
	if r.pending >= g.limits.PendingPerPeer || r.input.pending >= g.limits.PendingPerInput {
		g.mu.Unlock()
		g.respond(peer, onebot.Failure(frame.Echo, 1503, "too many in-flight actions"))
		return
	}
	key := g.nextKey()
	r.pending++
	r.input.pending++
	g.pending[key] = c
	g.mu.Unlock()

	if c.input.official != nil {
		queue := c.input.jobs
		switch frame.Action {
		case "get_group_info", "get_group_member_info", "get_group_member_list", "get_stranger_info":
			queue = c.input.profileJobs
		}
		select {
		case queue <- officialJob{key: key, call: c, frame: frame}:
		default:
			g.reject(key, 1503, "official API queue full")
		}
		return
	}
	if c.input.virtualJobs != nil && !r.cfg.Passthrough {
		select {
		case c.input.virtualJobs <- virtualJob{key: key, call: c, raw: raw, frame: frame}:
		default:
			g.reject(key, 1503, "identity conversion queue full")
		}
		return
	}
	g.sendUpstream(key, c, raw)
}

func (g *Gateway) sendUpstream(key string, c call, raw []byte) {
	upstream, err := onebot.RewriteEcho(raw, json.RawMessage(`"`+key+`"`))
	if err != nil {
		g.reject(key, 1400, "invalid API action")
		return
	}
	if !c.upstream.SendAPI(upstream, c.deadline) {
		g.reject(key, 1503, "upstream API queue unavailable")
	}
}

func (g *Gateway) finish(in *input, peer *transport.Peer, rawEcho json.RawMessage, raw []byte) {
	var key string
	if len(rawEcho) == 0 || rawEcho[0] != '"' || json.Unmarshal(rawEcho, &key) != nil {
		return
	}
	g.mu.Lock()
	c, ok := g.pending[key]
	if !ok || c.upstream != peer {
		g.mu.Unlock()
		g.log.Debug("unmatched upstream response")
		return
	}
	if in.virtualReplies != nil && !c.route.cfg.Passthrough {
		g.mu.Unlock()
		ctx, cancel := context.WithDeadline(g.ctx, c.deadline)
		converted, err := g.rewriteVirtualResponse(ctx, in, c.action, raw)
		cancel()
		if err != nil {
			g.reject(key, 1502, "could not convert upstream identity response")
			return
		}
		raw = converted
		g.mu.Lock()
		if _, ok := g.pending[key]; !ok {
			g.mu.Unlock()
			return
		}
	}
	g.removeLocked(key, c)
	g.mu.Unlock()
	if time.Now().After(c.deadline) {
		g.timeout(key, c)
		return
	}
	reply, err := onebot.RewriteEcho(raw, c.echo)
	if err != nil {
		reply = onebot.Failure(c.echo, 1502, "invalid upstream response")
	}
	g.respond(c.downstream, reply)
	g.log.Debug("API call settled", "input", c.input.cfg.ID, "output", c.route.output.ID,
		"request", key, "action", c.action, "elapsed", time.Since(c.start))
}

func (g *Gateway) respond(peer *transport.Peer, reply []byte) {
	if !peer.SendAPI(reply, time.Time{}) {
		peer.Stop(errSlowAPI)
	}
}

func (g *Gateway) reject(key string, code int, reason string) {
	g.mu.Lock()
	c, ok := g.pending[key]
	if ok {
		g.removeLocked(key, c)
	}
	g.mu.Unlock()
	if ok {
		g.respond(c.downstream, onebot.Failure(c.echo, code, reason))
	}
}

func (g *Gateway) removeLocked(key string, c call) {
	delete(g.pending, key)
	c.input.pending--
	c.route.pending--
}

func (g *Gateway) detachInput(in *input, peer *transport.Peer) {
	g.mu.Lock()
	in.peer = nil
	in.observerGroups = nil
	in.groupListAt = time.Time{}
	var failed []call
	for key, c := range g.pending {
		if c.upstream == peer {
			g.removeLocked(key, c)
			failed = append(failed, c)
		}
	}
	g.mu.Unlock()
	for _, c := range failed {
		g.respond(c.downstream, onebot.Failure(c.echo, 1502, "upstream disconnected; action result unknown"))
	}
}

func (g *Gateway) detachOutput(r *route, peer *transport.Peer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r.peer = nil
	for key, c := range g.pending {
		if c.downstream == peer {
			g.removeLocked(key, c)
		}
	}
}

func (g *Gateway) sweep() {
	ticker := time.NewTicker(min(250*time.Millisecond, g.limits.APITimeout/2+1))
	defer ticker.Stop()
	rate := time.NewTicker(time.Second)
	defer rate.Stop()
	var last uint64
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-rate.C:
			current := g.stats.received.Load()
			g.stats.perSecond.Store(current - last)
			last = current
		case now := <-ticker.C:
			g.mu.Lock()
			var expired map[string]call
			for key, c := range g.pending {
				if !now.Before(c.deadline) {
					if expired == nil {
						expired = make(map[string]call)
					}
					expired[key] = c
					g.removeLocked(key, c)
				}
			}
			g.mu.Unlock()
			for key, c := range expired {
				g.timeout(key, c)
			}
		}
	}
}

func (g *Gateway) timeout(key string, c call) {
	g.stats.timeout.Add(1)
	g.log.Warn("API call timed out", "input", c.input.cfg.ID, "output", c.route.output.ID,
		"request", key, "action", c.action, "elapsed", time.Since(c.start))
	g.respond(c.downstream, onebot.Failure(c.echo, 1504, "upstream action timed out; result unknown"))
}
