package qqofficial

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const MissingAppPermission = 11253

type GroupMember struct {
	OpenID   string `json:"member_openid"`
	Nickname string `json:"username"`
	Role     string `json:"member_role"`
	Joined   string `json:"joined_at"`
	Bot      bool   `json:"bot"`
}

func GroupRole(role string) string {
	switch role {
	case "owner", "admin", "member":
		return role
	default:
		return ""
	}
}

func (m GroupMember) JoinTime() int64 {
	joined, err := time.Parse(time.RFC3339, m.Joined)
	if err != nil {
		return 0
	}
	return joined.Unix()
}

func (c *Client) GroupMember(ctx context.Context, group, member string) (GroupMember, error) {
	var result GroupMember
	path := ConversationPath("group", group)
	if member == "" {
		path += "/bot_state"
	} else {
		path += "/members/" + url.PathEscape(member)
	}
	if err := c.Do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return GroupMember{}, err
	}
	if !nativePattern.MatchString(result.OpenID) || (member != "" && result.OpenID != member) {
		return GroupMember{}, errors.New("QQ member response omitted or mismatched its member OpenID")
	}
	return result, nil
}

func (c *Client) GroupMembers(ctx context.Context, group string) ([]GroupMember, error) {
	path := ConversationPath("group", group) + "/members"
	params := url.Values{"limit": {"30"}}
	var members []GroupMember
	seen := make(map[string]bool)
	for range 100 {
		var page struct {
			Members *[]GroupMember `json:"members"`
			Cursor  string         `json:"next_cursor"`
		}
		if err := c.Do(ctx, http.MethodGet, path+"?"+params.Encode(), nil, &page); err != nil {
			return nil, err
		}
		if page.Members == nil {
			return nil, errors.New("QQ member list omitted its members")
		}
		for _, member := range *page.Members {
			if !nativePattern.MatchString(member.OpenID) {
				return nil, errors.New("QQ member list contained an invalid member OpenID")
			}
			if !seen[member.OpenID] {
				seen[member.OpenID] = true
				members = append(members, member)
			}
		}
		if page.Cursor == "" {
			return members, nil
		}
		if page.Cursor == params.Get("cursor") || len(page.Cursor) > 1024 {
			return nil, errors.New("QQ member list returned an invalid cursor")
		}
		params.Set("cursor", page.Cursor)
	}
	return nil, errors.New("QQ member list exceeded its page limit")
}

func AvatarURL(appID, openID string) string {
	return "https://q.qlogo.cn/qqapp/" + url.PathEscape(appID) + "/" + url.PathEscape(openID) + "/100"
}

func QQAvatarURL(qq int64) string {
	return "https://q.qlogo.cn/g?b=qq&nk=" + strconv.FormatInt(qq, 10) + "&s=100"
}
