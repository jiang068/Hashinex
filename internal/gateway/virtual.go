package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/onebot"
	"hashinex/internal/store"
	"hashinex/internal/transport"
)

type virtualJob struct {
	key         string
	call        call
	raw         []byte
	frame       onebot.Frame
	scopedGroup string
	inputPolicy *config.Input
}

type virtualReply struct {
	peer *transport.Peer
	echo json.RawMessage
	raw  []byte
}

func (in *input) virtualNamespace() string {
	return "ob:" + in.cfg.ID
}

func (g *Gateway) startVirtual(in *input) {
	g.workers.Go(func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			case job := <-in.virtualJobs:
				g.guardWorker(in, func() { g.performVirtual(job) })
			}
		}
	})
	g.workers.Go(func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			case reply := <-in.virtualReplies:
				g.guardWorker(in, func() { g.finish(in, reply.peer, reply.echo, reply.raw) })
			}
		}
	})
}

type idConverter func(kind string, value json.RawMessage) (json.RawMessage, error)

func (g *Gateway) virtualConverter(ctx context.Context, in *input, outgoing bool) idConverter {
	return func(kind string, value json.RawMessage) (json.RawMessage, error) {
		id := onebot.ID(value)
		if len(value) == 0 || string(value) == "null" || id == "0" || (kind == "user" && id == in.cfg.SelfID) {
			return value, nil
		}
		if id == "" {
			return nil, errors.New("non-numeric ID on virtual OneBot input")
		}
		target := id
		if outgoing {
			numeric, _ := strconv.ParseInt(id, 10, 64)
			native, err := g.store.Reverse(ctx, in.virtualNamespace(), kind, numeric)
			if err != nil {
				return nil, err
			}
			if onebot.ID(json.RawMessage(native)) == "" {
				return nil, errors.New("invalid mapped OneBot native ID")
			}
			target = native
		} else {
			r, err := g.store.Resolve(ctx, store.Identity{Namespace: in.virtualNamespace(), Kind: kind, Native: id})
			if err != nil {
				return nil, err
			}
			target = strconv.FormatInt(r.ExternalID(), 10)
		}
		if value[0] == '"' {
			return json.RawMessage(`"` + target + `"`), nil
		}
		return json.RawMessage(target), nil
	}
}

func (g *Gateway) rewriteVirtualEvent(ctx context.Context, in *input, raw []byte) ([]byte, error) {
	return convertEntity(raw, g.virtualConverter(ctx, in, false))
}

func (g *Gateway) performVirtual(job virtualJob) {
	g.mu.Lock()
	_, exists := g.pending[job.key]
	g.mu.Unlock()
	if !exists {
		return
	}
	if job.scopedGroup != "" {
		silent, err := g.oneBotGroupSilent(job.call.input, job.inputPolicy, job.scopedGroup, job.call.deadline)
		if !time.Now().Before(job.call.deadline) {
			return
		}
		if err != nil {
			g.reject(job.key, 1502, "could not resolve group scope")
			return
		}
		if silent || !allowsConversation(job.call.policy, "group", job.scopedGroup) {
			g.reject(job.key, 1403, "group is outside the route scope or in silent mode")
			return
		}
		g.sendUpstream(job.key, job.call, job.raw)
		return
	}
	switch job.frame.Action {
	case "get_forward_msg":
		if job.call.route.output.Protocol != config.GScore {
			g.reject(job.key, 1404, "no identity conversion rule for this OneBot action")
			return
		}
		// gscore uses the opaque forward ID and extracts display content without member IDs.
		g.sendUpstream(job.key, job.call, job.raw)
		return
	case "send_msg", "send_group_msg", "send_private_msg", "delete_msg", "get_msg",
		"get_login_info", "get_status", "get_version_info", "get_group_info", "get_group_list",
		"get_group_member_info", "get_group_member_list", "get_stranger_info", "get_friend_list",
		"can_send_image", "can_send_record", "set_group_ban", "set_group_kick", "set_group_card", "set_group_name":
	default:
		g.reject(job.key, 1404, "no identity conversion rule for this OneBot action")
		return
	}
	if len(job.frame.Params) == 0 || string(job.frame.Params) == "null" {
		g.sendUpstream(job.key, job.call, job.raw)
		return
	}
	ctx, cancel := context.WithDeadline(g.ctx, job.call.deadline)
	defer cancel()
	params, err := convertEntity(job.frame.Params, g.virtualConverter(ctx, job.call.input, true))
	if err != nil {
		if errors.Is(err, store.ErrUnmapped) {
			g.reject(job.key, 1404, "no identity mapping for this Bot")
		} else {
			g.reject(job.key, 1502, "could not convert requested identity")
		}
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(job.raw, &raw); err != nil {
		g.reject(job.key, 1400, "invalid action frame")
		return
	}
	raw["params"] = params
	packet, err := json.Marshal(raw)
	if err != nil {
		g.reject(job.key, 1400, "could not encode action frame")
		return
	}
	g.sendUpstream(job.key, job.call, packet)
}

func (g *Gateway) rewriteVirtualResponse(ctx context.Context, in *input, action string, raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	data := fields["data"]
	if len(data) == 0 || string(data) == "null" || string(fields["status"]) != `"ok"` {
		return raw, nil
	}
	convert := g.virtualConverter(ctx, in, false)
	switch action {
	case "get_group_info", "get_group_member_info", "get_stranger_info", "get_msg", "get_login_info":
		converted, err := convertEntity(data, convert)
		if err != nil {
			return nil, err
		}
		fields["data"] = converted
	case "get_group_member_list", "get_group_list", "get_friend_list":
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, err
		}
		for i, item := range items {
			converted, err := convertEntity(item, convert)
			if err != nil {
				return nil, err
			}
			items[i] = converted
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			return nil, err
		}
		fields["data"] = encoded
	default:
		return raw, nil
	}
	return json.Marshal(fields)
}

func convertEntity(raw json.RawMessage, convert idConverter) (json.RawMessage, error) {
	var entity map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entity); err != nil {
		return nil, err
	}
	for field, kind := range map[string]string{
		"user_id": "user", "group_id": "group", "operator_id": "user", "target_id": "user",
	} {
		if value, ok := entity[field]; ok {
			newValue, err := convert(kind, value)
			if err != nil {
				return nil, err
			}
			entity[field] = newValue
		}
	}
	if sender := entity["sender"]; len(sender) != 0 && string(sender) != "null" {
		newValue, err := convertEntity(sender, convert)
		if err != nil {
			return nil, err
		}
		entity["sender"] = newValue
	}
	for _, field := range []string{"message", "raw_message"} {
		if data := entity[field]; len(data) != 0 {
			newValue, err := convertMessageIDs(data, convert)
			if err != nil {
				return nil, err
			}
			entity[field] = newValue
		}
	}
	return json.Marshal(entity)
}

var cqMention = regexp.MustCompile(`\[CQ:at,[^\]\r\n]*\]`)

func convertMessageIDs(raw json.RawMessage, convert idConverter) (json.RawMessage, error) {
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		var failed error
		result := cqMention.ReplaceAllStringFunc(text, func(code string) string {
			if failed != nil {
				return code
			}
			params := strings.Split(strings.TrimSuffix(code, "]"), ",")
			for i, parameter := range params {
				if id, ok := strings.CutPrefix(parameter, "qq="); ok && id != "all" {
					var value json.RawMessage
					value, failed = convert("user", json.RawMessage(id))
					params[i] = "qq=" + string(value)
					return strings.Join(params, ",") + "]"
				}
			}
			return code
		})
		if failed != nil {
			return nil, failed
		}
		return json.Marshal(result)
	}
	if raw[0] != '[' {
		return raw, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	for _, part := range parts {
		if string(part["type"]) != `"at"` {
			continue
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(part["data"], &data); err != nil {
			return nil, err
		}
		id := data["qq"]
		if len(id) == 0 || string(id) == `"all"` {
			continue
		}
		value, err := convert("user", id)
		if err != nil {
			return nil, err
		}
		data["qq"] = value
		part["data"], err = json.Marshal(data)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(parts)
}
