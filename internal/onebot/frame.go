package onebot

import (
	"bytes"
	"encoding/json"
	"strconv"
)

type Frame struct {
	PostType    string          `json:"post_type"`
	Action      string          `json:"action"`
	Params      json.RawMessage `json:"params"`
	Echo        json.RawMessage `json:"echo"`
	Status      json.RawMessage `json:"status"`
	Data        json.RawMessage `json:"data"`
	RetCode     *int            `json:"retcode"`
	SelfID      json.RawMessage `json:"self_id"`
	GroupID     json.RawMessage `json:"group_id"`
	UserID      json.RawMessage `json:"user_id"`
	MessageID   json.RawMessage `json:"message_id"`
	MessageType string          `json:"message_type"`
	NoticeType  string          `json:"notice_type"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message"`
	Time        json.RawMessage `json:"time"`
}

func ID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return ""
		}
	} else {
		value = string(raw)
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || strconv.FormatInt(id, 10) != value {
		return ""
	}
	return value
}

func (f Frame) TargetMatches(selfID string) bool {
	if f.SelfID != nil && ID(f.SelfID) != selfID {
		return false
	}
	if len(f.Params) == 0 || bytes.Equal(f.Params, []byte("null")) {
		return true
	}
	var params struct {
		SelfID json.RawMessage `json:"self_id"`
	}
	return json.Unmarshal(f.Params, &params) == nil && (params.SelfID == nil || ID(params.SelfID) == selfID)
}

func RewriteEcho(raw []byte, echo json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if echo == nil {
		delete(fields, "echo")
	} else {
		fields["echo"] = echo
	}
	return json.Marshal(fields)
}

func Failure(echo json.RawMessage, code int, message string) []byte {
	response, _ := json.Marshal(struct {
		Status  string          `json:"status"`
		RetCode int             `json:"retcode"`
		Data    any             `json:"data"`
		Message string          `json:"message"`
		Wording string          `json:"wording"`
		Echo    json.RawMessage `json:"echo,omitempty"`
	}{"failed", code, nil, message, message, echo})
	return response
}

func Success(echo json.RawMessage, data any) []byte {
	reply, err := json.Marshal(struct {
		Status  string          `json:"status"`
		RetCode int             `json:"retcode"`
		Data    any             `json:"data"`
		Echo    json.RawMessage `json:"echo,omitempty"`
	}{"ok", 0, data, echo})
	if err != nil {
		return Failure(echo, 1500, "invalid action result")
	}
	return reply
}
