package gateway

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hashinex/internal/onebot"
	"hashinex/internal/tempfiles"
	"hashinex/internal/transport"
)

func forwardSend(action string) bool {
	switch action {
	case "send_forward_msg", "send_group_forward_msg", "send_private_forward_msg":
		return true
	default:
		return false
	}
}

func shareRequired(segments []segment) bool {
	images := 0
	for _, part := range segments {
		switch part.Type {
		case "file", "record", "video", "node", "nodes", "forward", "forward_msg":
			return true
		case "image":
			images++
			if images > 1 {
				return true
			}
		}
	}
	return false
}

func shareDuration(ttl time.Duration) string {
	if ttl%time.Minute == 0 {
		return strconv.FormatInt(int64(ttl/time.Minute), 10) + " 分钟"
	}
	return strconv.FormatInt(int64(ttl/time.Second), 10) + " 秒"
}

type officialShare struct {
	title   string
	address string
}

func (s officialShare) content(ttl time.Duration) string {
	return cmp.Or(s.title, "查看临时消息") + "（有效期 " + shareDuration(ttl) + "）\n" + s.address
}

type shareBuilder struct {
	g      *Gateway
	call   call
	kind   string
	peer   string
	target int64
	title  string
	parts  []tempfiles.Part
	files  []*localOfficialFile
}

func (g *Gateway) publishOfficialShare(ctx context.Context, c call, kind, peer string, target int64, raw json.RawMessage, literal bool) (officialShare, error) {
	b := shareBuilder{g: g, call: c, kind: kind, peer: peer, target: target}
	defer func() {
		for _, file := range b.files {
			file.close()
		}
	}()
	if err := b.appendMessage(ctx, raw, 0, literal); err != nil {
		return officialShare{}, err
	}
	address, err := g.shares.Publish(ctx, b.parts)
	if err != nil {
		switch {
		case errors.Is(err, tempfiles.ErrCapacity):
			return officialShare{}, actionError{1503, "临时分享目录容量不足。请等待旧链接过期，或在「临时分享」调高总容量。"}
		case errors.Is(err, tempfiles.ErrAsset):
			return officialShare{}, actionError{1400, "临时分享内容为空、过大或包含无效的本地媒体。请检查文件和图片格式。"}
		case ctx.Err() != nil:
			return officialShare{}, ctx.Err()
		default:
			g.log.Warn("temporary share storage failed", "input", c.input.cfg.ID, "output", c.route.output.ID)
			return officialShare{}, actionError{1502, "临时分享写入失败。请检查分享目录权限和磁盘空间。"}
		}
	}
	return officialShare{title: b.linkTitle(), address: address}, nil
}

func (b *shareBuilder) linkTitle() string {
	title := b.title
	if title == "" {
		for _, part := range b.parts {
			if part.Kind == "file" && strings.TrimSpace(part.Name) != "" {
				title = part.Name
				break
			}
		}
	}
	title = strings.Join(strings.Fields(title), " ")
	const maxTitleRunes = 120
	count := 0
	for i := range title {
		if count == maxTitleRunes {
			return title[:i] + "…"
		}
		count++
	}
	return title
}

func (b *shareBuilder) add(part tempfiles.Part) error {
	if len(b.parts) >= tempfiles.MaxParts {
		return actionError{1400, "一条临时分享最多支持 256 个内容段。"}
	}
	b.parts = append(b.parts, part)
	return nil
}

func (b *shareBuilder) appendMessage(ctx context.Context, raw json.RawMessage, depth int, literal bool) error {
	if depth > 6 {
		return actionError{1400, "合并消息嵌套超过 6 层。"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) == 0 {
		return actionError{1400, "临时分享缺少消息内容。"}
	}
	if raw[0] == '"' {
		parts, err := parseMessage(raw, literal)
		if err != nil {
			return err
		}
		for _, part := range parts {
			switch part.Type {
			case "node", "nodes", "forward", "forward_msg":
				data, _ := json.Marshal(part.Data)
				if err := b.appendNode(ctx, part.Type, data, depth); err != nil {
					return err
				}
				continue
			}
			if err := b.appendPart(part.Type, part.Data); err != nil {
				return err
			}
		}
		return nil
	}
	var parts []struct {
		Type     string          `json:"type"`
		Data     json.RawMessage `json:"data"`
		Messages json.RawMessage `json:"messages"`
	}
	if raw[0] != '[' || json.Unmarshal(raw, &parts) != nil || len(parts) > tempfiles.MaxParts {
		return actionError{1400, "临时分享需要有效的 OneBot 消息段数组。"}
	}
	for _, part := range parts {
		if len(part.Messages) != 0 {
			if err := b.appendMessage(ctx, part.Messages, depth+1, false); err != nil {
				return err
			}
			continue
		}
		switch part.Type {
		case "node", "nodes", "forward", "forward_msg":
			if err := b.appendNode(ctx, part.Type, part.Data, depth); err != nil {
				return err
			}
		default:
			fields, err := shareFields(part.Data)
			if err != nil {
				return err
			}
			if err := b.appendPart(part.Type, fields); err != nil {
				return err
			}
		}
	}
	return nil
}

func shareFields(raw json.RawMessage) (map[string]string, error) {
	var values map[string]json.RawMessage
	if len(raw) != 0 && json.Unmarshal(raw, &values) != nil {
		return nil, actionError{1400, "临时分享的消息段数据格式无效。"}
	}
	fields := make(map[string]string, len(values))
	for key, value := range values {
		if value[0] == '"' {
			var text string
			_ = json.Unmarshal(value, &text)
			fields[key] = text
		} else if id := onebot.ID(value); id != "" {
			fields[key] = id
		}
	}
	return fields, nil
}

func (b *shareBuilder) appendNode(ctx context.Context, kind string, raw json.RawMessage, depth int) error {
	var node struct {
		ID       json.RawMessage `json:"id"`
		Content  json.RawMessage `json:"content"`
		Message  json.RawMessage `json:"message"`
		Messages json.RawMessage `json:"messages"`
		Name     string          `json:"name"`
		Nickname string          `json:"nickname"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return actionError{1400, "合并转发节点格式无效。"}
	}
	content := node.Content
	for _, alternative := range []json.RawMessage{node.Message, node.Messages} {
		if len(content) == 0 || string(content) == "null" {
			content = alternative
		}
	}
	name := strings.TrimSpace(cmp.Or(node.Nickname, node.Name))
	if len(content) == 0 || string(content) == "null" {
		if kind != "node" || numericID(node.ID) == 0 {
			return actionError{1404, "合并转发缺少消息正文。请让下游提供节点内容或原消息 ID。"}
		}
		var err error
		content, name, err = b.history(ctx, node.ID)
		if err != nil {
			return err
		}
	}
	if name != "" {
		if err := b.add(tempfiles.Part{Kind: "heading", Text: name}); err != nil {
			return err
		}
	}
	return b.appendMessage(ctx, content, depth+1, false)
}

func (b *shareBuilder) appendPart(kind string, fields map[string]string) error {
	switch kind {
	case "text":
		if text := fields["text"]; strings.TrimSpace(text) != "" {
			if b.title == "" {
				b.title = text
			}
			return b.add(tempfiles.Part{Kind: "text", Text: text})
		}
	case "at":
		if qq := fields["qq"]; qq != "" {
			return b.add(tempfiles.Part{Kind: "text", Text: "@" + qq})
		}
	case "image", "file", "record", "video":
		return b.addMedia(kind, fields)
	case "reply":
		return nil
	case "face":
		return b.add(tempfiles.Part{Kind: "text", Text: "[表情]"})
	default:
		return b.add(tempfiles.Part{Kind: "text", Text: "[此消息段无法预览]"})
	}
	return nil
}

func (b *shareBuilder) addMedia(kind string, fields map[string]string) error {
	source := cmp.Or(fields["file"], fields["url"])
	if kind == "image" && !strings.HasPrefix(source, "base64://") && !strings.HasPrefix(source, "file://") && !filepath.IsAbs(source) {
		source = cmp.Or(fields["url"], source)
	}
	source = strings.TrimPrefix(source, "link://")
	name := cmp.Or(fields["name"], fields["file_name"])
	part := tempfiles.Part{Kind: "file"}
	if kind == "image" {
		part.Kind = "image"
	}
	if encoded, ok := strings.CutPrefix(source, "base64://"); ok {
		part.Name = cmp.Or(name, kind)
		if !validOfficialFilename(part.Name) {
			return actionError{1400, "临时分享的文件名格式无效。"}
		}
		if strings.ContainsAny(encoded, "\r\n") {
			encoded = strings.NewReplacer("\r", "", "\n", "").Replace(encoded)
		}
		codec, size := base64.StdEncoding, 0
		if strings.Contains(encoded, "=") {
			size = codec.DecodedLen(len(encoded)) - (len(encoded) - len(strings.TrimRight(encoded, "=")))
		} else {
			codec = base64.RawStdEncoding
			size = codec.DecodedLen(len(encoded))
		}
		part.Source = &tempfiles.Source{Size: int64(size), Reader: base64.NewDecoder(codec, strings.NewReader(encoded))}
		return b.add(part)
	}
	location, err := officialFilePath(source, name)
	if err != nil {
		return err
	}
	part.Name = location.name
	if location.address != "" {
		if !tempfiles.WebURL(location.address) {
			return actionError{1400, "临时分享中的网络附件需要可公开访问的 HTTP(S) 地址。"}
		}
		part.URL = location.address
		return b.add(part)
	}
	local, err := b.call.openOfficialFile(location.localPath)
	if err != nil {
		return err
	}
	b.files = append(b.files, local)
	part.Source = &tempfiles.Source{Size: local.info.Size(), Reader: local.file}
	return b.add(part)
}

func (b *shareBuilder) history(ctx context.Context, id json.RawMessage) (json.RawMessage, string, error) {
	unavailable := actionError{1404, "无法读取合并转发引用的原消息。请检查消息是否在当前会话，或让下游提供节点正文。"}
	in := b.call.route.input
	var raw json.RawMessage
	if in.official != nil {
		message, err := b.g.store.Message(ctx, in.namespace(), numericID(id), time.Now())
		if err != nil || message.ChatKind != b.kind || message.Peer != b.peer || !b.g.messageAllowed(ctx, b.call.route, message) {
			return nil, "", unavailable
		}
		raw = message.Packet
	} else {
		raw = b.g.lookupShareMessage(ctx, in, id)
	}
	var message struct {
		onebot.Frame
		Sender struct {
			Card     string `json:"card"`
			Nickname string `json:"nickname"`
		} `json:"sender"`
	}
	if json.Unmarshal(raw, &message) != nil || message.MessageType != b.kind ||
		numericID(message.MessageID) != numericID(id) ||
		(in.official == nil && (b.kind != "group" || numericID(message.GroupID) != b.target)) {
		return nil, "", unavailable
	}
	if len(message.Message) == 0 && message.RawMessage != "" {
		message.Message, _ = json.Marshal(message.RawMessage)
	}
	if len(message.Message) == 0 {
		return nil, "", unavailable
	}
	return message.Message, cmp.Or(message.Sender.Card, message.Sender.Nickname), nil
}

type shareLookup struct {
	peer   *transport.Peer
	result chan json.RawMessage
}

func (g *Gateway) lookupShareMessage(ctx context.Context, in *input, id json.RawMessage) json.RawMessage {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	g.mu.Lock()
	peer := in.peer
	if peer == nil {
		g.mu.Unlock()
		return nil
	}
	echo := "share-" + g.nextKey()
	result := make(chan json.RawMessage, 1)
	g.shareLookups[echo] = shareLookup{peer, result}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.shareLookups, echo)
		g.mu.Unlock()
	}()
	frame, _ := json.Marshal(map[string]any{
		"action": "get_msg", "echo": echo, "params": map[string]int64{"message_id": numericID(id)},
	})
	deadline, _ := ctx.Deadline()
	if !peer.SendAPI(frame, deadline) {
		return nil
	}
	select {
	case data := <-result:
		return data
	case <-ctx.Done():
		return nil
	}
}

func (g *Gateway) acceptShareLookup(peer *transport.Peer, frame onebot.Frame) bool {
	if !strings.HasPrefix(string(frame.Echo), `"share-`) {
		return false
	}
	var echo string
	if json.Unmarshal(frame.Echo, &echo) != nil {
		return true
	}
	g.mu.Lock()
	lookup, ok := g.shareLookups[echo]
	if ok && lookup.peer == peer {
		delete(g.shareLookups, echo)
		if *frame.RetCode == 0 {
			lookup.result <- frame.Data
		} else {
			lookup.result <- nil
		}
	}
	g.mu.Unlock()
	return true
}
