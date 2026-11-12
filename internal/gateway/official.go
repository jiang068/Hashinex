package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"hashinex/internal/matching"
	"hashinex/internal/onebot"
	"hashinex/internal/qqofficial"
	"hashinex/internal/store"
)

type officialEnvelope struct {
	dispatch qqofficial.Dispatch
	received time.Time
}

type officialJob struct {
	key   string
	call  call
	frame onebot.Frame
}

type receipt struct {
	kind string
	peer string
	id   int64
	at   time.Time
}

func (in *input) namespace() string {
	return "qq:" + in.cfg.AppID
}

func (in *input) userNamespace(kind string) string {
	if kind == "private" {
		return in.namespace() + ":c2c"
	}
	return in.namespace()
}

func (in *input) officialGroupAllowed(id int64) bool {
	return filterAllows(in.cfg.GroupIDs, strconv.FormatInt(id, 10))
}

func (in *input) officialPrivateAllowed(id int64) bool {
	return in.cfg.PrivateEnabled() && id > 0 && filterAllows(in.cfg.PrivateQQIDs, strconv.FormatInt(id, 10))
}

func (g *Gateway) startOfficial(in *input) {
	if in.privatePrompts != nil {
		g.workers.Go(func() { g.runPrivatePrompts(in) })
	}
	g.workers.Go(func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			case e := <-in.officialEvents:
				g.guardWorker(in, func() {
					defer g.eventWork.Add(-1)
					g.convertOfficial(in, e)
				})
			}
		}
	})
	// Profile queries, including paginated reads, have a separate queue from replies.
	for _, queue := range []chan officialJob{in.jobs, in.profileJobs} {
		g.workers.Go(func() {
			for {
				select {
				case <-g.ctx.Done():
					return
				case job := <-queue:
					g.guardWorker(in, func() { g.performOfficial(job) })
				}
			}
		})
	}
	if in.cfg.Events == "gateway" {
		g.workers.Go(func() {
			in.official.RunGateway(g.ctx, in.cfg.Intents, g.limits.FrameBytes, func(d qqofficial.Dispatch) bool {
				return g.ingestOfficial(in, d)
			}, in.online.Store, g.log.With("input", in.cfg.ID))
		})
	}
}

func (g *Gateway) guardWorker(in *input, run func()) {
	defer func() {
		if recover() != nil {
			g.log.Error("input worker panicked", "input", in.cfg.ID)
		}
	}()
	run()
}

func (g *Gateway) webhook(w http.ResponseWriter, r *http.Request) {
	in := g.inputs[r.PathValue("input")]
	if in == nil || in.official == nil || in.cfg.Events != "webhook" {
		http.NotFound(w, r)
		return
	}
	in.official.Webhook(w, r, g.limits.FrameBytes, func(d qqofficial.Dispatch) bool {
		if !g.ingestOfficial(in, d) {
			return false
		}
		in.online.Store(true)
		return true
	})
}

func (g *Gateway) ingestOfficial(in *input, d qqofficial.Dispatch) bool {
	if g.ctx.Err() != nil || !g.beginEvent() {
		return false
	}
	select {
	case in.officialEvents <- officialEnvelope{dispatch: d, received: time.Now()}:
		return true
	default:
		g.eventWork.Add(-1)
		g.stats.dropped.Add(1)
		g.log.Warn("official event queue full", "input", in.cfg.ID)
		return false
	}
}

type segment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

func makeSegment(kind, key, value string) segment {
	return segment{Type: kind, Data: map[string]string{key: value}}
}

func (g *Gateway) convertOfficial(in *input, e officialEnvelope) {
	msg, err := e.dispatch.Message()
	if err != nil {
		if !errors.Is(err, qqofficial.ErrUnsupportedEvent) {
			g.stats.dropped.Add(1)
			g.log.Warn("invalid official message", "input", in.cfg.ID, "event", e.dispatch.Type)
		} else {
			g.log.Debug("unsupported official event", "input", in.cfg.ID, "event", e.dispatch.Type)
		}
		return
	}
	if msg.Kind == "private" && !in.cfg.PrivateEnabled() {
		return
	}
	deadline := e.received.Add(g.limits.EventTTL)
	ctx, cancel := context.WithDeadline(g.ctx, deadline)
	defer cancel()
	ns := in.namespace()
	var group int64
	var knownGroup int64
	var observed bool
	if msg.Kind == "group" {
		key := store.Identity{Namespace: ns, Kind: "group", Native: msg.Peer}
		resolved, err := g.store.Lookup(ctx, key)
		if err != nil {
			g.recordOfficialDrop(in, "resolve group", err)
			return
		}
		group, knownGroup = resolved.ExternalID(), resolved.Real
		if code := officialPrivateCode(msg); code != "" {
			if knownGroup > 0 && in.officialGroupAllowed(knownGroup) {
				g.rememberPrivateConfirmation(ctx, in, code, msg, knownGroup, e.received)
			}
			return
		}
		if (knownGroup == 0 || in.officialGroupAllowed(knownGroup)) && g.observesGroup(in, knownGroup) {
			g.offerOfficialMatch(in, e, msg, knownGroup)
			observed = knownGroup != 0
		}
		if !in.officialGroupAllowed(knownGroup) {
			return
		}
	}
	userKey := store.Identity{Namespace: in.userNamespace(msg.Kind), Kind: "user", Native: msg.User}
	user, err := g.store.Lookup(ctx, userKey)
	if err == nil && observed && user.ExternalID() == 0 {
		user, err = g.store.Resolve(ctx, userKey)
	}
	if err != nil {
		g.recordOfficialDrop(in, "resolve user", err)
		return
	}
	if msg.Kind == "private" && !in.officialPrivateAllowed(user.Real) {
		if user.Real == 0 {
			g.queuePrivatePrompt(ctx, in, msg, e.received)
		}
		return
	}
	if msg.Kind == "group" {
		if user.Real == 0 && !observed {
			return
		}
		if group == 0 {
			group = g.store.Transient(store.Identity{Namespace: ns, Kind: "group", Native: msg.Peer})
		}
		if knownGroup != 0 || user.Real != 0 {
			if err := g.store.RecordGroupSource(ctx, userKey, msg.Peer); err != nil {
				g.log.Warn("record identity source group failed", "input", in.cfg.ID, "reason", err)
			}
		}
	}
	g.stats.received.Add(1)
	silent := msg.Kind == "group" && in.cfg.Silent(strconv.FormatInt(knownGroup, 10), "")
	raw := e.dispatch.Raw
	if raw == nil {
		raw, _ = json.Marshal(e.dispatch)
	}
	var segments []segment
	if !silent && in.normalizesEvents() {
		segments, err = g.incomingSegments(ctx, in, msg)
		if err != nil {
			g.recordOfficialDrop(in, "convert message segments", err)
			segments = nil
		}
	}
	msgTime := msg.Time
	if msgTime.IsZero() {
		msgTime = e.received
	}
	ref, created, err := g.store.CreateMessage(ctx, store.Message{
		Namespace: ns, ChatKind: msg.Kind, Peer: msg.Peer, UserNative: msg.User,
		NativeID: msg.NativeID, EventID: msg.EventID, ReferenceID: msg.ReferenceID,
		Incoming: true, ReceivedAt: e.received, ExpiresAt: e.received.Add(g.storage.MessageTTL),
	}, func(id int64) ([]byte, error) {
		if segments == nil {
			return raw, nil
		}
		subtype := "friend"
		sender := map[string]any{"user_id": user.ExternalID(), "nickname": msg.Nickname}
		if msg.Kind == "group" {
			subtype = "normal"
			if msg.MemberRole != "" {
				sender["role"] = msg.MemberRole
			}
		}
		packet := map[string]any{
			"time": msgTime.Unix(), "self_id": json.Number(in.cfg.SelfID), "post_type": "message",
			"message_type": msg.Kind, "sub_type": subtype, "message_id": id, "user_id": user.ExternalID(),
			"message": segments, "raw_message": cqString(segments), "font": 0,
			"sender": sender,
		}
		if group != 0 {
			packet["group_id"] = group
		}
		return json.Marshal(packet)
	})
	if err != nil {
		g.recordOfficialDrop(in, "persist message context", err)
		return
	}
	if !created {
		g.stats.duplicate.Add(1)
		return
	}
	if silent {
		g.stats.observed.Add(1)
		return
	}
	frame := onebot.Frame{PostType: "message", MessageType: msg.Kind,
		UserID: json.RawMessage(strconv.FormatInt(user.ExternalID(), 10))}
	if group != 0 {
		frame.GroupID = json.RawMessage(strconv.FormatInt(group, 10))
	}
	var normalized []byte
	if segments != nil {
		normalized = ref.Packet
	}
	g.routeEvent(in, event{raw: normalized, original: raw, frame: frame, deadline: deadline, ref: &ref})
}

func (g *Gateway) offerOfficialMatch(in *input, e officialEnvelope, msg qqofficial.Message, group int64) {
	if g.matcher == nil || msg.Kind != "group" || msg.AtBot || len(msg.Attachments) != 0 || msg.ReplyTo != "" ||
		qqofficial.MentionPattern.MatchString(msg.Text) {
		return
	}
	g.matcher.Offer(matching.Event{
		Namespace: in.namespace(), AppID: in.cfg.AppID, UserNative: msg.User, GroupNative: msg.Peer,
		KnownGroup: group, AllowedGroups: in.cfg.GroupIDs, Key: msg.Peer + "\x00" + msg.NativeID,
		Text: msg.Text, Time: msg.Time, Received: e.received,
	})
}

func (g *Gateway) incomingSegments(ctx context.Context, in *input, msg qqofficial.Message) ([]segment, error) {
	segments := make([]segment, 0, 4)
	if msg.ReplyTo != "" {
		id, err := g.store.FindReference(ctx, in.namespace(), msg.Kind, msg.Peer, msg.ReplyTo, time.Now())
		if err == nil {
			segments = append(segments, makeSegment("reply", "id", strconv.FormatInt(id, 10)))
		}
	}
	selfMention := ""
	for _, mention := range msg.Mentions {
		if mention.IsSelf {
			selfMention = mention.OpenID
			if selfMention == "" {
				selfMention = mention.ID
			}
			break
		}
	}
	hasSelf := false
	rest := msg.Text
	for _, part := range qqofficial.MentionPattern.FindAllStringSubmatchIndex(msg.Text, -1) {
		length := part[1] - part[0]
		native := msg.Text[part[2]:part[3]]
		prefix, suffix, _ := strings.Cut(rest, msg.Text[part[0]:part[0]+length])
		if prefix != "" {
			segments = append(segments, makeSegment("text", "text", prefix))
		}
		rest = suffix
		external := in.cfg.SelfID
		if native == selfMention && selfMention != "" {
			hasSelf = true
		} else {
			key := store.Identity{Namespace: in.userNamespace(msg.Kind), Kind: "user", Native: native}
			mapped, err := g.store.Lookup(ctx, key)
			if err != nil {
				return nil, err
			}
			id := mapped.ExternalID()
			if id == 0 {
				id = g.store.Transient(key)
			}
			external = strconv.FormatInt(id, 10)
		}
		segments = append(segments, makeSegment("at", "qq", external))
	}
	if msg.AtBot && !hasSelf {
		at := makeSegment("at", "qq", in.cfg.SelfID)
		if len(segments) > 0 && segments[0].Type == "reply" {
			segments = append([]segment{segments[0], at}, segments[1:]...)
		} else {
			segments = append([]segment{at}, segments...)
		}
	}
	if rest != "" {
		segments = append(segments, makeSegment("text", "text", rest))
	}
	for _, attachment := range msg.Attachments {
		if strings.HasPrefix(attachment.ContentType, "image/") {
			address := attachment.URL
			if strings.HasPrefix(address, "//") {
				address = "https:" + address
			}
			u, err := url.Parse(address)
			if err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil {
				segments = append(segments, segment{Type: "image", Data: map[string]string{"file": address, "url": address}})
			}
		}
	}
	return segments, nil
}

var (
	cqTextEncoder  = strings.NewReplacer("&", "&amp;", "[", "&#91;", "]", "&#93;")
	cqParamEncoder = strings.NewReplacer("&", "&amp;", "[", "&#91;", "]", "&#93;", ",", "&#44;")
)

func cqString(segments []segment) string {
	var text strings.Builder
	for _, seg := range segments {
		switch seg.Type {
		case "text":
			_, _ = cqTextEncoder.WriteString(&text, seg.Data["text"])
		case "at":
			text.WriteString("[CQ:at,qq=")
			_, _ = cqParamEncoder.WriteString(&text, seg.Data["qq"])
			text.WriteByte(']')
		case "reply":
			text.WriteString("[CQ:reply,id=")
			_, _ = cqParamEncoder.WriteString(&text, seg.Data["id"])
			text.WriteByte(']')
		case "image":
			text.WriteString("[CQ:image,file=")
			_, _ = cqParamEncoder.WriteString(&text, seg.Data["file"])
			text.WriteByte(']')
		}
	}
	return text.String()
}

func (g *Gateway) recordOfficialDrop(in *input, operation string, err error) {
	g.stats.dropped.Add(1)
	g.log.Warn("official message not delivered", "input", in.cfg.ID, "operation", operation, "reason", err)
}

func (g *Gateway) cleanStore() {
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case now := <-timer.C:
			ctx, cancel := context.WithTimeout(g.ctx, 3*time.Second)
			ttl := g.matching.EvidenceTTL
			if ttl == 0 {
				ttl = 24 * time.Hour
			}
			err := g.store.Clean(ctx, now, now.Add(-ttl))
			cancel()
			if err != nil {
				g.log.Warn("database cleanup failed", "reason", err)
			}
		}
	}
}
