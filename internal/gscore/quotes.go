package gscore

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"hashinex/internal/onebot"
)

const (
	quoteTimeout    = 3 * time.Second
	maxQuotePending = 16
	maxQuoteBytes   = 1 << 20
	maxQuoteParts   = 128
	maxForwardDepth = 3
	maxForwardCalls = 8
	forwardMarker   = "[合并转发]"
)

type quoteLookup struct {
	event        coreEvent
	id           string
	deadline     time.Time
	text         string
	images       []coreSegment
	nodes        []coreSegment
	forwardIndex int
	seen         map[string]bool
	bytes        int
}

type forwardReference struct {
	id    string
	depth int
}

func quotedID(content []coreSegment) string {
	for _, part := range content {
		if part.Type == "reply_id" {
			return part.Data.(string)
		}
	}
	return ""
}

func (q *quoteLookup) accept(data json.RawMessage) {
	if q.forwardIndex >= 0 {
		ref := q.nodes[q.forwardIndex].Data.(forwardReference)
		items := q.forwardItems(data, ref.depth+1)
		q.nodes = slices.Replace(q.nodes, q.forwardIndex, q.forwardIndex+1, items...)
		return
	}
	var quoted struct {
		onebot.Frame
		TargetID json.RawMessage `json:"target_id"`
	}
	if json.Unmarshal(data, &quoted) != nil || onebot.ID(quoted.MessageID) != q.id ||
		(quoted.SelfID != nil && onebot.ID(quoted.SelfID) != q.event.SelfID) {
		return
	}
	if q.event.GroupID != nil {
		if quoted.MessageType != "group" || onebot.ID(quoted.GroupID) != *q.event.GroupID {
			return
		}
	} else if quoted.MessageType != "private" ||
		(onebot.ID(quoted.UserID) != q.event.UserID && onebot.ID(quoted.TargetID) != q.event.UserID) {
		return
	}
	var text strings.Builder
	for _, part := range messageSegments(quoted.Message, quoted.RawMessage) {
		if len(q.images)+len(q.nodes) >= maxQuoteParts {
			break
		}
		switch part.Type {
		case "text":
			if value := part.value("text"); q.reserve(len(value)) {
				text.WriteString(value)
			}
		case "image":
			if item, ok := incomingSegment(part); ok && q.reserve(len(item.Data.(string))*2) {
				q.images = append(q.images, item)
			}
		case "forward", "forward_msg":
			q.nodes = append(q.nodes, quoteForward(part, 0))
		}
	}
	q.text = strings.TrimSpace(text.String())
}

func (q *quoteLookup) reserve(bytes int) bool {
	if bytes > maxQuoteBytes-q.bytes {
		return false
	}
	q.bytes += bytes
	return true
}

func quoteForward(part messagePart, depth int) coreSegment {
	id := cmp.Or(part.value("id"), part.value("message_id"), part.value("resid"))
	if id == "" || len(id) > 512 || depth >= maxForwardDepth {
		return coreSegment{"text", forwardMarker}
	}
	return coreSegment{"forward", forwardReference{id, depth}}
}

func (q *quoteLookup) nextForward() (obAction, bool) {
	for i, item := range q.nodes {
		ref, pending := item.Data.(forwardReference)
		if !pending {
			continue
		}
		if len(q.seen) >= maxForwardCalls || q.seen[ref.id] {
			q.nodes[i] = coreSegment{"text", forwardMarker}
			continue
		}
		if q.seen == nil {
			q.seen = make(map[string]bool)
		}
		q.seen[ref.id] = true
		q.forwardIndex = i
		return obAction{Name: "get_forward_msg", Params: map[string]any{"id": ref.id}}, true
	}
	return obAction{}, false
}

func (q *quoteLookup) forwardItems(data json.RawMessage, depth int) []coreSegment {
	var entries []json.RawMessage
	if len(data) != 0 && data[0] == '[' {
		_ = json.Unmarshal(data, &entries)
	} else {
		var envelope struct {
			Messages []json.RawMessage `json:"messages"`
			Data     struct {
				Messages []json.RawMessage `json:"messages"`
			} `json:"data"`
		}
		_ = json.Unmarshal(data, &envelope)
		entries = envelope.Messages
		if entries == nil {
			entries = envelope.Data.Messages
		}
	}
	var items []coreSegment
	capacity := maxQuoteParts - len(q.nodes) + 1
	for _, entry := range entries {
		if len(items) >= capacity {
			break
		}
		var node struct {
			Type     string          `json:"type"`
			Data     json.RawMessage `json:"data"`
			Content  json.RawMessage `json:"content"`
			Message  json.RawMessage `json:"message"`
			Name     string          `json:"name"`
			Nickname string          `json:"nickname"`
			Sender   struct {
				Nickname string `json:"nickname"`
			} `json:"sender"`
		}
		if json.Unmarshal(entry, &node) != nil {
			continue
		}
		if node.Type == "node" {
			payload := node.Data
			node.Data = nil
			if json.Unmarshal(payload, &node) != nil {
				continue
			}
		}
		name := strings.TrimSpace(cmp.Or(node.Sender.Nickname, node.Name, node.Nickname))
		if name != "" && q.reserve(len(name)*2+2) {
			items = append(items, coreSegment{"text", name + ":"})
		}
		content := node.Content
		if len(content) == 0 || string(content) == "null" {
			content = node.Message
		}
		for _, part := range messageSegments(content, "") {
			if len(items) >= capacity {
				break
			}
			switch part.Type {
			case "forward", "forward_msg":
				items = append(items, quoteForward(part, depth))
			case "file":
				name, url := cmp.Or(part.value("name"), part.value("file"), "file"), part.value("url")
				if url != "" && q.reserve(len(name)+len(url)+1) {
					items = append(items, coreSegment{"file", name + "|" + url})
				}
			case "text", "image", "record", "video":
				if item, ok := incomingSegment(part); ok && q.reserve(len(item.Data.(string))*2) {
					items = append(items, item)
				}
			}
		}
	}
	if len(items) == 0 {
		return []coreSegment{{"text", forwardMarker}}
	}
	return items
}

func (q *quoteLookup) finish() coreEvent {
	event := q.event
	if len(q.nodes) > 0 {
		for i, item := range q.nodes {
			if item.Type == "forward" {
				q.nodes[i] = coreSegment{"text", forwardMarker}
			}
		}
		event.Content = append(event.Content, coreSegment{"reply", forwardPreview(q.nodes)}, coreSegment{"node", q.nodes})
		return event
	}
	var preview []string
	if q.text != "" {
		preview = append(preview, q.text)
	}
	for _, image := range q.images {
		preview = append(preview, image.Data.(string))
	}
	event.Content = append(event.Content, coreSegment{"reply", strings.Join(preview, "\n")})
	event.Content = append(event.Content, q.images...)
	return event
}

func forwardPreview(nodes []coreSegment) string {
	lines := []string{forwardMarker}
	var speaker string
	for _, item := range nodes {
		var text string
		switch item.Type {
		case "text":
			text = strings.TrimSpace(item.Data.(string))
			if text == "" || text == forwardMarker {
				continue
			}
			if strings.HasSuffix(text, ":") || strings.HasSuffix(text, "：") {
				speaker = strings.TrimRight(text, ":：")
				continue
			}
		case "image":
			text = "[图片]"
		case "record":
			text = "[语音]"
		case "video":
			text = "[视频]"
		case "file":
			text = "[文件]"
		}
		if speaker != "" {
			text = speaker + "：" + text
			speaker = ""
		}
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}
