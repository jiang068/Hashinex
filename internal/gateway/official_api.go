package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"hashinex/internal/onebot"
	"hashinex/internal/qqofficial"
	"hashinex/internal/store"
)

type actionError struct {
	code int
	text string
}

func (e actionError) Error() string { return e.text }

func (g *Gateway) performOfficial(job officialJob) {
	g.mu.Lock()
	_, pending := g.pending[job.key]
	g.mu.Unlock()
	if !pending || !time.Now().Before(job.call.deadline) {
		return
	}
	ctx, cancel := context.WithDeadline(g.ctx, job.call.deadline)
	defer cancel()
	data, err := g.invokeOfficial(ctx, job.call, job.frame)
	var response []byte
	switch {
	case err == nil:
		response = onebot.Success(job.call.echo, data)
	default:
		var problem actionError
		var api *qqofficial.APIError
		switch {
		case errors.As(err, &problem):
			response = onebot.Failure(job.call.echo, problem.code, problem.text)
		case errors.Is(err, store.ErrUnmapped), errors.Is(err, store.ErrMessageNotFound), errors.Is(err, store.ErrProfileNotFound):
			response = onebot.Failure(job.call.echo, 1404, err.Error())
		case errors.As(err, &api) && api.Code == qqofficial.MissingAppPermission:
			response = onebot.Failure(job.call.echo, 1403, "QQ app lacks permission for this profile API (11253)")
		case errors.As(err, &api):
			response = onebot.Failure(job.call.echo, 1502, api.Error())
		case errors.Is(err, context.DeadlineExceeded):
			response = onebot.Failure(job.call.echo, 1504, "official action timed out; result unknown")
		default:
			g.log.Warn("official action failed", "input", job.call.input.cfg.ID, "request", job.key, "reason", err)
			response = onebot.Failure(job.call.echo, 1502, "official action failed; result may be unknown")
		}
	}
	g.mu.Lock()
	c, pending := g.pending[job.key]
	if pending {
		g.removeLocked(job.key, c)
	}
	g.mu.Unlock()
	if pending {
		g.respond(c.downstream, response)
	}
}

type actionParams struct {
	Type       string          `json:"message_type"`
	GroupID    json.RawMessage `json:"group_id"`
	UserID     json.RawMessage `json:"user_id"`
	Message    json.RawMessage `json:"message"`
	MessageID  json.RawMessage `json:"message_id"`
	AutoEscape bool            `json:"auto_escape"`
	NoCache    bool            `json:"no_cache"`
}

func (g *Gateway) invokeOfficial(ctx context.Context, c call, frame onebot.Frame) (any, error) {
	var p actionParams
	if len(frame.Params) != 0 && json.Unmarshal(frame.Params, &p) != nil {
		return nil, actionError{1400, "invalid action parameters"}
	}
	in := c.input
	switch frame.Action {
	case "get_login_info":
		var me struct {
			Nickname string `json:"username"`
		}
		if err := in.official.Do(ctx, http.MethodGet, "/users/@me", nil, &me); err != nil {
			return nil, err
		}
		return map[string]any{"user_id": json.Number(in.cfg.SelfID), "nickname": me.Nickname}, nil
	case "get_status":
		return map[string]bool{"online": in.online.Load(), "good": in.online.Load()}, nil
	case "get_version_info":
		return map[string]string{"app_name": "Hashinex", "protocol_version": "v11"}, nil
	case "send_msg", "send_group_msg", "send_private_msg":
		return g.sendOfficial(ctx, c, frame.Action, p)
	case "get_msg", "delete_msg":
		id := numericID(p.MessageID)
		msg, err := g.store.Message(ctx, in.namespace(), id, time.Now())
		if err != nil {
			return nil, err
		}
		if !g.messageAllowed(ctx, c.route, msg) {
			return nil, actionError{1403, "message is outside the route scope"}
		}
		if frame.Action == "get_msg" {
			if len(msg.Packet) == 0 {
				return nil, actionError{1404, "message payload unavailable"}
			}
			return msg.Packet, nil
		}
		if msg.Incoming {
			return nil, actionError{1403, "QQ permits recalling only the bot's own messages"}
		}
		return nil, in.official.Do(ctx, http.MethodDelete,
			qqofficial.ConversationPath(msg.ChatKind, msg.Peer)+"/messages/"+url.PathEscape(msg.NativeID), nil, nil)
	case "get_group_info":
		peer, id, err := g.officialGroupTarget(ctx, c, p.GroupID)
		if err != nil {
			return nil, err
		}
		var data struct {
			Name    string `json:"group_name"`
			Members *int   `json:"group_member_num"`
		}
		if err := in.official.Do(ctx, http.MethodGet, qqofficial.ConversationPath("group", peer)+"/info", nil, &data); err != nil {
			return nil, err
		}
		if data.Name == "" {
			return nil, actionError{1502, "QQ group info omitted group_name"}
		}
		profile := map[string]any{"group_id": id, "group_name": data.Name}
		if data.Members != nil {
			profile["member_count"] = *data.Members
		}
		return profile, nil
	case "get_group_member_info", "get_group_member_list":
		return g.officialGroupProfile(ctx, c, frame.Action, p)
	case "get_stranger_info":
		return g.officialUserProfile(ctx, c, p)
	default:
		return nil, actionError{1404, "action unsupported by this QQ adapter"}
	}
}

func (g *Gateway) sendOfficial(ctx context.Context, c call, action string, p actionParams) (any, error) {
	kind := p.Type
	switch action {
	case "send_group_msg":
		kind = "group"
	case "send_private_msg":
		kind = "private"
	case "send_msg":
		if kind == "" {
			if numericID(p.GroupID) > 0 {
				kind = "group"
			} else {
				kind = "private"
			}
		}
	}
	id := numericID(p.UserID)
	targetKind := "user"
	if kind == "group" {
		id, targetKind = numericID(p.GroupID), "group"
	} else if kind != "private" {
		return nil, actionError{1400, "invalid message_type"}
	}
	namespace := c.input.namespace()
	targetNamespace := namespace
	if targetKind == "user" {
		targetNamespace = c.input.userNamespace(kind)
	}
	peer, err := g.store.Reverse(ctx, targetNamespace, targetKind, id)
	if err != nil {
		return nil, err
	}
	record, err := g.store.Lookup(ctx, store.Identity{Namespace: targetNamespace, Kind: targetKind, Native: peer})
	if err != nil {
		return nil, err
	}
	if kind == "group" && !c.input.officialBusinessGroup(record.Real) ||
		kind == "private" && !c.input.officialPrivateAllowed(record.Real) {
		return nil, actionError{1403, "conversation outside the input scope"}
	}
	targetID := record.ExternalID()
	if targetID == 0 {
		targetID = id
	}
	target := strconv.FormatInt(targetID, 10)
	group := ""
	if kind == "group" {
		group = target
	}
	if !c.route.allowsConversation(kind, group) ||
		(kind == "private" && !filterAllows(c.route.cfg.Users, target)) {
		return nil, actionError{1403, "target is outside the route scope"}
	}
	segments, err := parseMessage(p.Message, p.AutoEscape)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	var quoteID int64
	var image string
	for _, seg := range segments {
		switch seg.Type {
		case "text":
			text.WriteString(seg.Data["text"])
		case "at":
			if kind != "group" {
				return nil, actionError{1400, "private messages cannot contain mentions"}
			}
			at, _ := strconv.ParseInt(seg.Data["qq"], 10, 64)
			native, err := g.store.Reverse(ctx, namespace, "user", at)
			if err != nil {
				return nil, err
			}
			text.WriteString("<@" + native + ">")
		case "reply":
			if quoteID != 0 {
				return nil, actionError{1400, "multiple quote segments"}
			}
			quoteID, _ = strconv.ParseInt(seg.Data["id"], 10, 64)
			if quoteID <= 0 {
				return nil, actionError{1400, "invalid quote ID"}
			}
		case "image":
			if image != "" {
				return nil, actionError{1400, "one QQ image per action is supported"}
			}
			image = seg.Data["file"]
			if image == "" {
				image = seg.Data["url"]
			}
			if image == "" {
				return nil, actionError{1400, "image source missing"}
			}
		default:
			return nil, actionError{1404, "message segment unsupported by this QQ adapter"}
		}
	}
	messageText := text.String()
	hasText := strings.TrimSpace(messageText) != ""
	if !hasText && image == "" {
		return nil, actionError{1400, "message content is empty"}
	}
	refID := numericID(p.MessageID)
	if p.MessageID != nil && refID <= 0 {
		return nil, actionError{1400, "invalid reply context ID"}
	}
	if refID == 0 {
		refID = quoteID
	}
	if refID == 0 && c.route.cfg.ImplicitReply == "unique-recent" {
		refID, err = g.recentReply(c, kind, peer, time.Now())
		if err != nil {
			return nil, err
		}
	}
	path := qqofficial.ConversationPath(kind, peer) + "/messages"
	body := map[string]any{"msg_type": 0}
	if hasText {
		body["content"] = messageText
	}
	var ref store.Message
	if refID != 0 {
		ref, err = g.store.Message(ctx, namespace, refID, time.Now())
		if err != nil {
			return nil, err
		}
		if ref.ChatKind != kind || ref.Peer != peer || !g.messageAllowed(ctx, c.route, ref) {
			return nil, actionError{1403, "reply context belongs to a different conversation or route"}
		}
	}
	if quoteID != 0 {
		quote := ref
		if quoteID != refID {
			quote, err = g.store.Message(ctx, namespace, quoteID, time.Now())
			if err != nil {
				return nil, err
			}
		}
		if quote.ChatKind != kind || quote.Peer != peer || !g.messageAllowed(ctx, c.route, quote) {
			return nil, actionError{1403, "quote belongs to a different conversation or route"}
		}
		if quote.ReferenceID == "" {
			return nil, actionError{1404, "QQ quote reference unavailable for this message"}
		}
		body["message_reference"] = map[string]string{"message_id": quote.ReferenceID}
	}
	if ref.Incoming {
		expires := ref.ReceivedAt.Add(g.storage.ReplyWindow)
		if !time.Now().Before(expires) {
			return nil, actionError{1404, "QQ passive reply context expired"}
		}
		seq, err := g.store.NextSequence(ctx, namespace, peer, ref.NativeID, expires)
		if err != nil {
			return nil, err
		}
		body["msg_id"], body["msg_seq"] = ref.NativeID, seq
	} else if !c.input.cfg.AllowProactive {
		return nil, actionError{1404, "no incoming QQ reply context; proactive sends disabled"}
	}
	if image != "" {
		fileInfo, err := c.input.official.UploadImage(ctx, kind, peer, image)
		if errors.Is(err, qqofficial.ErrInvalidImage) {
			return nil, actionError{1400, err.Error()}
		}
		if err != nil {
			return nil, err
		}
		body["msg_type"] = 7
		body["media"] = map[string]string{"file_info": fileInfo}
	}
	var sent struct {
		ID string `json:"id"`
	}
	if err := c.input.official.Do(ctx, http.MethodPost, path, body, &sent); err != nil {
		return nil, err
	}
	if sent.ID == "" {
		return nil, errors.New("QQ accepted send but omitted its message ID")
	}
	now := time.Now()
	snapshot := func(id int64) ([]byte, error) {
		data := map[string]any{"message_id": id, "message_type": kind, "time": now.Unix(),
			"user_id": json.Number(c.input.cfg.SelfID), "message": segments, "raw_message": cqString(segments)}
		if kind == "group" {
			data["group_id"] = targetID
		}
		return json.Marshal(data)
	}
	if strings.HasPrefix(image, "base64://") {
		// The sent handle remains usable for recall without persisting the full upload.
		snapshot = nil
	}
	msg, _, err := g.store.CreateMessage(ctx, store.Message{
		Namespace: namespace, ChatKind: kind, Peer: peer, NativeID: sent.ID,
		ReceivedAt: now, ExpiresAt: now.Add(g.storage.MessageTTL),
	}, snapshot)
	if err != nil {
		return nil, fmt.Errorf("QQ accepted send but storing its message handle failed: %w", err)
	}
	return map[string]int64{"message_id": msg.ID}, nil
}

func (in *input) officialBusinessGroup(id int64) bool {
	return in.officialGroupAllowed(id) && !in.cfg.Silent(strconv.FormatInt(id, 10), "")
}

func (g *Gateway) messageAllowed(ctx context.Context, r *route, msg store.Message) bool {
	in := r.input
	if msg.ChatKind == "private" {
		user, err := g.store.Lookup(ctx, store.Identity{Namespace: in.userNamespace("private"), Kind: "user", Native: msg.Peer})
		return err == nil && in.officialPrivateAllowed(user.Real) && r.allowsConversation("private", "") &&
			filterAllows(r.cfg.Users, strconv.FormatInt(user.Real, 10))
	}
	group, err := g.store.Lookup(ctx, store.Identity{Namespace: in.namespace(), Kind: "group", Native: msg.Peer})
	if err != nil || !in.officialBusinessGroup(group.Real) ||
		!r.allowsConversation("group", strconv.FormatInt(group.Real, 10)) {
		return false
	}
	if len(r.cfg.Users) == 0 {
		return true
	}
	if !msg.Incoming {
		return filterAllows(r.cfg.Users, in.cfg.SelfID)
	}
	user, err := g.store.Lookup(ctx, store.Identity{Namespace: in.namespace(), Kind: "user", Native: msg.UserNative})
	return err == nil && filterAllows(r.cfg.Users, strconv.FormatInt(user.Real, 10))
}

func filterAllows(ids []string, id string) bool {
	return len(ids) == 0 || slices.Contains(ids, id)
}

func numericID(raw json.RawMessage) int64 {
	id, _ := strconv.ParseInt(onebot.ID(raw), 10, 64)
	return id
}

func (g *Gateway) remember(r *route, ref *store.Message) {
	const capacity = 128
	entry := receipt{kind: ref.ChatKind, peer: ref.Peer, id: ref.ID, at: ref.ReceivedAt}
	if len(r.recent) < capacity {
		r.recent = append(r.recent, entry)
		return
	}
	r.incompleteUntil = r.recent[r.recentAt].at.Add(g.storage.ReplyWindow)
	r.recent[r.recentAt] = entry
	r.recentAt = (r.recentAt + 1) % capacity
}

func (g *Gateway) recentReply(c call, kind, peer string, now time.Time) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.route.peer != c.downstream || now.Before(c.route.incompleteUntil) {
		return 0, actionError{1409, "reply history unavailable; specify the message ID"}
	}
	var id int64
	for _, ref := range c.route.recent {
		if ref.kind != kind || ref.peer != peer || !now.Before(ref.at.Add(g.storage.ReplyWindow)) {
			continue
		}
		if id != 0 {
			return 0, actionError{1409, "multiple recent messages; specify the reply context"}
		}
		id = ref.id
	}
	return id, nil
}

func parseMessage(raw json.RawMessage, literal bool) ([]segment, error) {
	if len(raw) == 0 {
		return nil, actionError{1400, "message is required"}
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, actionError{1400, "invalid message text"}
		}
		if literal {
			return []segment{makeSegment("text", "text", text)}, nil
		}
		return parseCQ(text)
	}
	if raw[0] != '[' {
		return nil, actionError{1400, "message must be a string or segment array"}
	}
	var data []struct {
		Type string                     `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || len(data) > 128 {
		return nil, actionError{1400, "invalid or oversized message segments"}
	}
	out := make([]segment, 0, len(data))
	for _, part := range data {
		s := segment{Type: part.Type, Data: make(map[string]string)}
		for key, value := range part.Data {
			var text string
			if json.Unmarshal(value, &text) == nil && !bytes.Equal(value, []byte("null")) {
				s.Data[key] = text
			} else if id := onebot.ID(value); id != "" {
				s.Data[key] = id
			}
		}
		out = append(out, s)
	}
	return out, nil
}

var (
	cqTextDecoder  = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&amp;", "&")
	cqParamDecoder = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&#44;", ",", "&amp;", "&")
)

func parseCQ(text string) ([]segment, error) {
	var out []segment
	for text != "" {
		prefix, rest, has := strings.Cut(text, "[CQ:")
		if prefix != "" {
			out = append(out, makeSegment("text", "text", cqTextDecoder.Replace(prefix)))
		}
		if !has {
			break
		}
		cq, after, closed := strings.Cut(rest, "]")
		if !closed || len(out) > 126 {
			return nil, actionError{1400, "invalid or oversized CQ message"}
		}
		parts := strings.Split(cq, ",")
		s := segment{Type: parts[0], Data: make(map[string]string)}
		for _, part := range parts[1:] {
			key, value, ok := strings.Cut(part, "=")
			if !ok || key == "" {
				return nil, actionError{1400, "invalid CQ segment"}
			}
			s.Data[key] = cqParamDecoder.Replace(value)
		}
		out = append(out, s)
		text = after
	}
	return out, nil
}
