package qqofficial

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

type Dispatch struct {
	Op   int             `json:"op"`
	ID   string          `json:"id"`
	Seq  *int64          `json:"s"`
	Type string          `json:"t"`
	Data json.RawMessage `json:"d"`
	Raw  []byte          `json:"-"`
}

type Message struct {
	Kind        string
	Peer        string
	User        string
	EventID     string
	NativeID    string
	ReferenceID string
	ReplyTo     string
	Text        string
	Nickname    string
	MemberRole  string
	UnionID     string
	AtBot       bool
	Time        time.Time
	Attachments []Attachment
	Mentions    []Mention
}

type Attachment struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

type Mention struct {
	ID     string `json:"id"`
	OpenID string `json:"member_openid"`
	IsSelf bool   `json:"is_you"`
}

var nativePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var MentionPattern = regexp.MustCompile(`<@!?([A-Za-z0-9_-]{1,128})>`)

var ErrUnsupportedEvent = errors.New("unsupported QQ event")

func (d Dispatch) Message() (Message, error) {
	var kind string
	switch d.Type {
	case "GROUP_MESSAGE_CREATE", "GROUP_AT_MESSAGE_CREATE":
		kind = "group"
	case "C2C_MESSAGE_CREATE":
		kind = "private"
	default:
		return Message{}, ErrUnsupportedEvent
	}
	var data struct {
		ID      string `json:"id"`
		Group   string `json:"group_openid"`
		Content string `json:"content"`
		Time    string `json:"timestamp"`
		Author  struct {
			ID       string `json:"id"`
			Member   string `json:"member_openid"`
			User     string `json:"user_openid"`
			Nickname string `json:"username"`
			Role     string `json:"member_role"`
			UnionID  string `json:"union_openid"`
		} `json:"author"`
		Scene struct {
			Extension []string `json:"ext"`
		} `json:"message_scene"`
		Attachments []Attachment `json:"attachments"`
		Mentions    []Mention    `json:"mentions"`
	}
	if err := json.Unmarshal(d.Data, &data); err != nil {
		return Message{}, errors.New("invalid QQ message event")
	}
	m := Message{
		Kind: kind, NativeID: data.ID, EventID: d.ID, Text: data.Content,
		Nickname: data.Author.Nickname, UnionID: data.Author.UnionID,
		AtBot: d.Type == "GROUP_AT_MESSAGE_CREATE", Attachments: data.Attachments, Mentions: data.Mentions,
	}
	if kind == "group" {
		m.Peer = data.Group
		m.User = data.Author.Member
		m.MemberRole = GroupRole(data.Author.Role)
	} else {
		m.User = data.Author.User
	}
	if m.User == "" {
		m.User = data.Author.ID
	}
	if kind == "private" {
		m.Peer = m.User
	}
	if !nativePattern.MatchString(m.User) || !nativePattern.MatchString(m.Peer) || len(m.NativeID) == 0 || len(m.NativeID) > 512 {
		return Message{}, errors.New("QQ message event lacks usable native identifiers")
	}
	m.Time, _ = time.Parse(time.RFC3339Nano, data.Time)
	for _, entry := range data.Scene.Extension {
		if value, ok := strings.CutPrefix(entry, "msg_idx="); ok {
			m.ReferenceID = value
		}
		if value, ok := strings.CutPrefix(entry, "ref_msg_idx="); ok {
			m.ReplyTo = value
		}
	}
	return m, nil
}
