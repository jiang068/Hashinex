package gateway

import (
	"context"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/transport"
)

func heartbeatInterval(endpoint config.Endpoint, fallback time.Duration) time.Duration {
	if endpoint.HeartbeatInterval != 0 {
		return endpoint.HeartbeatInterval
	}
	return fallback
}

func (r *route) policy() *config.Route {
	if policy := r.livePolicy.Load(); policy != nil {
		return policy
	}
	return &r.cfg
}

// ApplyLiveConfig applies compatible settings. The runtime validates the topology before calling it.
func (g *Gateway) ApplyLiveConfig(cfg *config.Config) {
	for _, input := range cfg.Inputs {
		in := g.inputs[input.ID]
		in.timing.Set(heartbeatInterval(input.Endpoint, cfg.Limits.PingInterval), input.ReconnectInterval)
		in.livePolicy.Store(&input)
	}
	outputs := make(map[string]config.Output, len(cfg.Outputs))
	for _, output := range cfg.Outputs {
		outputs[output.ID] = output
	}
	for _, rule := range cfg.Routes {
		for _, target := range rule.Targets() {
			r := g.routes[[2]string{target, rule.From}]
			output := outputs[target]
			r.timing.Set(heartbeatInterval(output.Endpoint, cfg.Limits.PingInterval), output.ReconnectInterval)
			r.livePolicy.Store(&rule)
		}
	}
}

func (in *input) policy() *config.Input {
	if policy := in.livePolicy.Load(); policy != nil {
		return policy
	}
	return &in.cfg
}

func (g *Gateway) ReconnectPeers(ctx context.Context) {
	g.mu.Lock()
	peers := make(map[*transport.Peer]struct{}, len(g.inputs)+len(g.routes))
	for _, in := range g.inputs {
		if in.peer != nil {
			peers[in.peer] = struct{}{}
		}
	}
	for _, r := range g.routes {
		if r.peer != nil {
			peers[r.peer] = struct{}{}
		}
	}
	g.mu.Unlock()
	for peer := range peers {
		peer.RequestRestart()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for len(peers) != 0 {
		for peer := range peers {
			if peer.Closed() {
				delete(peers, peer)
			}
		}
		if len(peers) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
