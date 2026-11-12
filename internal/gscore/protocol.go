package gscore

import (
	"encoding/json"
	"errors"
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
	BotID     string `json:"bot_id"`
	SelfID    string `json:"bot_self_id"`
	MessageID string `json:"msg_id"`
	Type      string `json:"target_type"`
	Target    string `json:"target_id"`
	Echo      string `json:"echo"`
	Content   []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"content"`
}

type obSegment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
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

func incomingContent(frame onebot.Frame) []coreSegment {
	var parts []struct {
		Type string                     `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(frame.Message, &parts); err != nil {
		if plain := jsonString(frame.Message); plain != "" {
			return []coreSegment{{"text", plain}}
		}
		return nil
	}
	content := make([]coreSegment, 0, len(parts))
	for _, part := range parts {
		var value string
		kind := part.Type
		switch kind {
		case "text":
			value = jsonString(part.Data["text"])
		case "image", "record", "video":
			value = jsonString(part.Data["url"])
			if value == "" {
				value = jsonString(part.Data["file"])
			}
		case "at":
			value = onebot.ID(part.Data["qq"])
			if jsonString(part.Data["qq"]) == "all" {
				value = "all"
			}
		case "reply":
			kind, value = "reply_id", onebot.ID(part.Data["id"])
		}
		if value != "" {
			content = append(content, coreSegment{kind, value})
		}
	}
	return content
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
	message := make([]obSegment, 0, len(reply.Content))
	for _, part := range reply.Content {
		value := jsonString(part.Data)
		switch part.Type {
		case "group":
			continue
		case "text":
			message = append(message, obSegment{"text", map[string]string{"text": value}})
		case "image", "record", "video":
			message = append(message, obSegment{part.Type, map[string]string{"file": strings.TrimPrefix(value, "link://")}})
		case "at":
			if value == "" {
				value = onebot.ID(part.Data)
			}
			message = append(message, obSegment{"at", map[string]string{"qq": value}})
		case "reply", "reply_id":
			message = append(message, obSegment{"reply", map[string]string{"id": onebot.ID(part.Data)}})
		default:
			return obAction{}, errors.New("unsupported gscore message segment")
		}
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
	action.Params["message"] = message
	return action, nil
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
