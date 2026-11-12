package gateway

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"hashinex/internal/onebot"
	"hashinex/internal/store"
	"hashinex/internal/transport"
)

const observerGroupTTL = 2 * time.Minute

func (g *Gateway) pollObserverGroups(ctx context.Context, in *input, peer *transport.Peer) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for ctx.Err() == nil {
		frame, _ := json.Marshal(map[string]string{"action": "get_group_list", "echo": g.prefix + "-observer-groups"})
		peer.SendAPI(frame, time.Now().Add(g.limits.APITimeout))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (g *Gateway) acceptObserverGroups(in *input, peer *transport.Peer, frame onebot.Frame) bool {
	if !in.observes || string(frame.Echo) != strconv.Quote(g.prefix+"-observer-groups") {
		return false
	}
	var groups []struct {
		ID json.RawMessage `json:"group_id"`
	}
	if frame.RetCode == nil || *frame.RetCode != 0 || json.Unmarshal(frame.Data, &groups) != nil || groups == nil {
		return true
	}
	now := time.Now()
	current := make(map[string]time.Time, len(groups))
	for _, group := range groups {
		id := numericID(group.ID)
		if id <= 0 || id >= store.VirtualBase {
			return true
		}
		current[onebot.ID(group.ID)] = now
	}
	g.mu.Lock()
	if in.peer == peer {
		in.observerGroups = current
		in.groupListAt = now
	}
	g.mu.Unlock()
	return true
}

func (g *Gateway) rememberObserverGroup(in *input, group string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if in.peer == nil {
		return
	}
	if in.observerGroups == nil {
		in.observerGroups = make(map[string]time.Time)
	}
	in.observerGroups[group] = time.Now()
}

func (g *Gateway) observerGroupNotice(in *input, group string, frame onebot.Frame) {
	if onebot.ID(frame.UserID) != in.cfg.SelfID {
		return
	}
	switch frame.NoticeType {
	case "group_increase":
		g.rememberObserverGroup(in, group)
	case "group_decrease":
		g.mu.Lock()
		delete(in.observerGroups, group)
		g.mu.Unlock()
	}
}

func (g *Gateway) hasPrivateObserver(official *input) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for _, in := range g.inputs {
		if !in.observes || in.peer == nil {
			continue
		}
		for group, at := range in.observerGroups {
			if now.Sub(at) <= observerGroupTTL && filterAllows(official.cfg.GroupIDs, group) {
				return true
			}
		}
		if now.Sub(in.groupListAt) > observerGroupTTL {
			return true
		}
	}
	return false
}

func (g *Gateway) observesGroup(official *input, group int64) bool {
	if group == 0 {
		return g.hasPrivateObserver(official)
	}
	number := strconv.FormatInt(group, 10)
	since := time.Now().Add(-observerGroupTTL)
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, in := range g.inputs {
		if in.observes && in.peer != nil && in.observerGroups[number].After(since) {
			return true
		}
	}
	return false
}
