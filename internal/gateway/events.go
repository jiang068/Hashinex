package gateway

import (
	"container/list"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"hashinex/internal/matching"
	"hashinex/internal/onebot"
	"hashinex/internal/store"
)

type eventKey struct {
	kind string
	id   string
}

type seenEvent struct {
	key eventKey
	at  time.Time
}

func (g *Gateway) processEvents(in *input) {
	recent := list.New()
	byKey := make(map[eventKey]time.Time)
	for g.ctx.Err() == nil {
		select {
		case <-g.ctx.Done():
			return
		case e := <-in.events:
			g.guardWorker(in, func() {
				defer g.eventWork.Add(-1)
				g.processEvent(in, e, recent, byKey)
			})
		}
	}
}

func (g *Gateway) processEvent(in *input, e event, recent *list.List, byKey map[eventKey]time.Time) {
	now := time.Now()
	if now.After(e.deadline) {
		g.stats.dropped.Add(1)
		g.log.Warn("input event expired before routing", "input", in.cfg.ID)
		return
	}
	for oldest := recent.Front(); oldest != nil; oldest = recent.Front() {
		entry := oldest.Value.(seenEvent)
		if now.Sub(entry.at) < g.limits.DedupTTL {
			break
		}
		delete(byKey, entry.key)
		recent.Remove(oldest)
	}
	if id := onebot.ID(e.frame.MessageID); id != "" && (e.frame.PostType == "message" || e.frame.PostType == "message_sent") {
		key := eventKey{kind: e.frame.PostType, id: id}
		if _, duplicate := byKey[key]; duplicate {
			g.stats.duplicate.Add(1)
			return
		}
		if recent.Len() >= g.limits.DedupSize {
			oldest := recent.Front()
			delete(byKey, oldest.Value.(seenEvent).key)
			recent.Remove(oldest)
		}
		byKey[key] = now
		recent.PushBack(seenEvent{key, now})
	}
	group := onebot.ID(e.frame.GroupID)
	observes := group != "" && in.observes
	groupMessage := e.frame.PostType == "message" && e.frame.MessageType == "group"
	parseObservation := observes && groupMessage && (g.matcher != nil || g.privateChecks != nil)
	var bindingCode string
	if observes {
		switch e.frame.PostType {
		case "message":
			g.rememberObserverGroup(in, group)
			if parseObservation {
				var text string
				text, bindingCode = groupEventText(e.frame, g.matcher != nil)
				g.observePrivateCode(in, e, bindingCode)
				g.offerObservation(in, e, text)
			}
		case "notice":
			g.observerGroupNotice(in, group, e.frame)
		}
	}
	if g.oneBotGroupSilent(in, group, e.deadline) {
		g.stats.observed.Add(1)
		return
	}
	if groupMessage {
		if !parseObservation {
			_, bindingCode = groupEventText(e.frame, false)
		}
		if bindingCode != "" {
			return
		}
	}
	if in.virtualJobs != nil && in.normalizesEvents() {
		e.original = e.raw
		ctx, cancel := context.WithDeadline(g.ctx, e.deadline)
		raw, err := g.rewriteVirtualEvent(ctx, in, e.raw)
		cancel()
		var normalized onebot.Frame
		if err != nil || json.Unmarshal(raw, &normalized) != nil {
			g.stats.dropped.Add(1)
			g.log.Warn("could not rewrite virtual OneBot event", "input", in.cfg.ID)
			e.raw = nil
			g.routeEvent(in, e)
			return
		}
		originalFrame := e.frame
		e.originalFrame = &originalFrame
		e.frame = normalized
		e.raw = raw
	}
	g.routeEvent(in, e)
}

func (g *Gateway) oneBotGroupSilent(in *input, group string, deadline time.Time) bool {
	native := group
	if group != "" && in.virtualJobs != nil && (len(in.cfg.SilentGroups) != 0 || len(in.cfg.BusinessGroups) != 0) {
		ctx, cancel := context.WithDeadline(g.ctx, deadline)
		defer cancel()
		record, err := g.store.Resolve(ctx, store.Identity{Namespace: in.virtualNamespace(), Kind: "group", Native: group})
		if err != nil {
			return true
		}
		group = strconv.FormatInt(record.ExternalID(), 10)
	}
	return in.cfg.Silent(group, native)
}

func (in *input) normalizesEvents() bool {
	for _, route := range in.routes {
		if !route.cfg.Passthrough {
			return true
		}
	}
	return false
}

func (g *Gateway) offerObservation(in *input, e event, text string) {
	if text == "" || g.matcher == nil {
		return
	}
	group, user := numericID(e.frame.GroupID), numericID(e.frame.UserID)
	id := onebot.ID(e.frame.MessageID)
	stamp, err := strconv.ParseInt(onebot.ID(e.frame.Time), 10, 64)
	if err != nil || id == "" || group <= 0 || group >= store.VirtualBase || user <= 0 || user >= store.VirtualBase ||
		onebot.ID(e.frame.UserID) == in.cfg.SelfID {
		return
	}
	g.matcher.Offer(matching.Event{
		GroupID: group, UserID: user, Text: text, Time: time.Unix(stamp, 0),
		Received: e.deadline.Add(-g.limits.EventTTL), Key: in.cfg.ID + "\x00" + strconv.FormatInt(group, 10) + "\x00" + id,
	})
}

// groupEventText extracts a binding code and, for observation, plain matching
// text. Mentions are allowed in binding messages but exclude plain matching.
func groupEventText(frame onebot.Frame, matchText bool) (text, code string) {
	var parts []segment
	var err error
	if len(frame.Message) == 0 {
		code = privateBindingCode(frame.RawMessage)
		if code != "" || !matchText {
			return "", code
		}
		parts, err = parseCQ(frame.RawMessage)
	} else {
		parts, err = parseMessage(frame.Message, false)
	}
	if err != nil {
		return "", ""
	}
	var content strings.Builder
	hasMention := false
	for _, part := range parts {
		switch part.Type {
		case "text":
			content.WriteString(part.Data["text"])
		case "at":
			hasMention = true
		default:
			return "", ""
		}
	}
	text = content.String()
	if len(frame.Message) != 0 {
		code = privateBindingCode(text)
	}
	if code != "" || hasMention || !matchText {
		return "", code
	}
	return text, ""
}

func (g *Gateway) routeEvent(in *input, e event) {
	g.stats.routingNanos.Add(uint64(max(0, time.Since(e.deadline.Add(-g.limits.EventTTL)))))
	g.stats.routed.Add(1)
	for _, r := range in.routes {
		raw, frame := e.raw, &e.frame
		if r.cfg.Passthrough && e.original != nil {
			raw = e.original
			if e.originalFrame != nil {
				frame = e.originalFrame
			}
		}
		if raw == nil || !filterAllows(r.cfg.Events, frame.PostType) {
			continue
		}
		if !r.allowsConversation(frame.MessageType, onebot.ID(frame.GroupID)) {
			continue
		}
		if !filterAllows(r.cfg.Users, onebot.ID(frame.UserID)) {
			continue
		}
		if time.Now().After(e.deadline) {
			g.reportUndelivered(r, 1)
			continue
		}
		g.mu.Lock()
		peer := r.peer
		queued := peer != nil && peer.SendEvent(raw, e.deadline)
		if queued && e.ref != nil && r.cfg.ImplicitReply == "unique-recent" {
			g.remember(r, e.ref)
		}
		g.mu.Unlock()
		if queued {
			g.stats.enqueued.Add(1)
			if r.cfg.Passthrough {
				g.stats.passthrough.Add(1)
			}
			continue
		}
		g.reportUndelivered(r, 1)
		if peer != nil {
			peer.Stop(errSlowOutput)
		}
	}
}

func (r *route) allowsConversation(kind, group string) bool {
	return (kind == "private" && r.cfg.IncludePrivate) || filterAllows(r.cfg.Groups, group)
}
