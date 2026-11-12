package gscore

import (
	"cmp"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"hashinex/internal/onebot"
)

const platform = "onebot"

type coreSegment struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type coreEvent struct {
	BotID     string         `json:"bot_id"`
	SelfID    string         `json:"bot_self_id"`
	MessageID string         `json:"msg_id"`
	UserType  string         `json:"user_type"`
	GroupID   *string        `json:"group_id"`
	UserID    string         `json:"user_id"`
	Sender    map[string]any `json:"sender"`
	UserPM    int            `json:"user_pm"`
	Content   []coreSegment  `json:"content"`
}

type coreReply struct {
	BotID     string          `json:"bot_id"`
	SelfID    string          `json:"bot_self_id"`
	MessageID string          `json:"msg_id"`
	Type      string          `json:"target_type"`
	Target    string          `json:"target_id"`
	Echo      string          `json:"echo"`
	Content   []coreReplyPart `json:"content"`
}

type coreReplyPart struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type obSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type obAction struct {
	Name   string         `json:"action"`
	Params map[string]any `json:"params"`
	Echo   string         `json:"echo"`
}

func incoming(frame onebot.Frame, raw []byte, selfID string) (coreEvent, bool) {
	if frame.PostType != "message" || onebot.ID(frame.SelfID) != selfID {
		return coreEvent{}, false
	}
	user := onebot.ID(frame.UserID)
	msgID := onebot.ID(frame.MessageID)
	if !positive(user) || msgID == "" || user == selfID {
		return coreEvent{}, false
	}
	event := coreEvent{BotID: platform, SelfID: selfID, MessageID: msgID, UserID: user, UserPM: 6}
	switch frame.MessageType {
	case "group":
		group := onebot.ID(frame.GroupID)
		if !positive(group) {
			return coreEvent{}, false
		}
		event.UserType, event.GroupID = "group", &group
	case "private":
		event.UserType = "direct"
	default:
		return coreEvent{}, false
	}

	var metadata struct {
		Sender struct {
			Nickname string `json:"nickname"`
			Card     string `json:"card"`
			Role     string `json:"role"`
		} `json:"sender"`
	}
	_ = json.Unmarshal(raw, &metadata)
	nickname := metadata.Sender.Nickname
	if event.UserType == "group" && metadata.Sender.Card != "" {
		nickname = metadata.Sender.Card
	}
	event.Sender = map[string]any{"user_id": user, "nickname": nickname}
	id, _ := strconv.ParseInt(user, 10, 64)
	if id < 100_000_000_000_000 {
		event.Sender["avatar"] = "https://q1.qlogo.cn/g?b=qq&nk=" + user + "&s=640"
	}
	if event.UserType == "group" {
		switch metadata.Sender.Role {
		case "owner":
			event.UserPM = 2
		case "admin":
			event.UserPM = 3
		}
	}
	event.Content = incomingContent(frame)
	return event, len(event.Content) != 0
}

type messagePart struct {
	Type string                     `json:"type"`
	Data map[string]json.RawMessage `json:"data"`
	cq   map[string]string
}

func (p messagePart) value(key string) string {
	if p.cq != nil {
		return p.cq[key]
	}
	raw := p.Data[key]
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		return jsonString(raw)
	}
	return onebot.ID(raw)
}

func messageSegments(message json.RawMessage, fallback string) []messagePart {
	if len(message) == 0 || message[0] != '[' {
		text := cmp.Or(jsonString(message), fallback)
		parts, err := onebot.ParseCQ(text)
		if err != nil {
			return []messagePart{{Type: "text", cq: map[string]string{"text": text}}}
		}
		segments := make([]messagePart, 0, len(parts))
		for _, part := range parts {
			segments = append(segments, messagePart{Type: part.Type, cq: part.Data})
		}
		return segments
	}
	var parts []messagePart
	if err := json.Unmarshal(message, &parts); err != nil {
		return nil
	}
	return parts
}

func incomingContent(frame onebot.Frame) []coreSegment {
	parts := messageSegments(frame.Message, frame.RawMessage)
	content := make([]coreSegment, 0, len(parts))
	for _, part := range parts {
		if item, ok := incomingSegment(part); ok {
			content = append(content, item)
		}
	}
	return content
}

func incomingSegment(part messagePart) (coreSegment, bool) {
	kind, value := part.Type, ""
	switch kind {
	case "text":
		value = part.value("text")
	case "image", "record", "video":
		value = cmp.Or(part.value("url"), part.value("file"))
	case "at":
		value = part.value("qq")
		if value != "all" {
			value = onebot.ID(json.RawMessage(value))
		}
	case "reply":
		kind = "reply_id"
		value = onebot.ID(json.RawMessage(part.value("id")))
		if value == "0" {
			value = ""
		}
	}
	return coreSegment{kind, value}, value != ""
}

func outgoing(reply coreReply, selfID string) (obAction, error) {
	if reply.BotID != platform || (reply.SelfID != "" && reply.SelfID != selfID) {
		return obAction{}, errors.New("gscore reply targets a different Bot")
	}
	if !positive(reply.Target) {
		return obAction{}, errors.New("gscore reply is missing its target ID")
	}
	action := obAction{Params: make(map[string]any)}
	switch reply.Type {
	case "group":
		action.Name, action.Params["group_id"] = "send_group_msg", reply.Target
	case "direct":
		action.Name, action.Params["user_id"] = "send_private_msg", reply.Target
	default:
		return obAction{}, errors.New("unsupported gscore conversation type")
	}
	message, err := outgoingSegments(reply.Content, selfID, 0)
	if err != nil {
		return obAction{}, err
	}
	if len(message) == 0 {
		return obAction{}, errors.New("gscore reply has no message segments")
	}
	if reply.MessageID != "" {
		id, err := strconv.ParseInt(reply.MessageID, 10, 64)
		if err != nil || id == 0 {
			return obAction{}, errors.New("invalid gscore reply context ID")
		}
		action.Params["message_id"] = id
	}
	if nodes, hasNodes := forwardNodes(message, selfID); hasNodes {
		action.Name = strings.TrimSuffix(action.Name, "_msg") + "_forward_msg"
		action.Params["messages"] = nodes
	} else {
		action.Params["message"] = message
	}
	return action, nil
}

func outgoingSegments(content []coreReplyPart, selfID string, depth int) ([]obSegment, error) {
	if depth > 6 || len(content) > 256 {
		return nil, errors.New("gscore message exceeds forward segment limits")
	}
	message := make([]obSegment, 0, len(content))
	for _, part := range content {
		value := jsonString(part.Data)
		switch part.Type {
		case "group":
			continue
		case "text":
			message = append(message, obSegment{"text", map[string]any{"text": value}})
		case "image", "record", "video":
			message = append(message, obSegment{part.Type, map[string]any{"file": strings.TrimPrefix(value, "link://")}})
		case "at":
			message = append(message, obSegment{"at", map[string]any{"qq": cmp.Or(value, onebot.ID(part.Data))}})
		case "reply", "reply_id":
			message = append(message, obSegment{"reply", map[string]any{"id": onebot.ID(part.Data)}})
		case "file":
			name, source, ok := strings.Cut(value, "|")
			if !ok || source == "" {
				return nil, errors.New("invalid gscore file segment")
			}
			source = strings.TrimPrefix(source, "link://")
			if !strings.Contains(source, "://") && !filepath.IsAbs(source) {
				source = "base64://" + source
			}
			message = append(message, obSegment{"file", map[string]any{"name": name, "file": source}})
		case "node":
			var children []coreReplyPart
			if json.Unmarshal(part.Data, &children) != nil || len(children) == 0 {
				return nil, errors.New("invalid gscore forward node")
			}
			segments, err := outgoingSegments(children, selfID, depth+1)
			if err != nil {
				return nil, err
			}
			message = append(message, makeForwardNode(selfID, segments))
		default:
			return nil, errors.New("unsupported gscore message segment")
		}
	}
	return message, nil
}

func makeForwardNode(selfID string, content []obSegment) obSegment {
	return obSegment{"node", map[string]any{"user_id": selfID, "nickname": "gscore", "content": content}}
}

func forwardNodes(message []obSegment, selfID string) ([]obSegment, bool) {
	hasNodes := false
	for _, part := range message {
		if part.Type == "node" {
			hasNodes = true
			break
		}
	}
	if !hasNodes {
		return nil, false
	}
	var nodes []obSegment
	start := 0
	for i, part := range message {
		if part.Type == "node" {
			if start < i {
				nodes = append(nodes, makeForwardNode(selfID, message[start:i]))
			}
			nodes = append(nodes, part)
			start = i + 1
		}
	}
	if start < len(message) {
		nodes = append(nodes, makeForwardNode(selfID, message[start:]))
	}
	return nodes, true
}

func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func positive(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}
