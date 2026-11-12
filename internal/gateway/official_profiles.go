package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/qqofficial"
	"hashinex/internal/store"
)

func (g *Gateway) officialGroupTarget(ctx context.Context, c call, raw json.RawMessage) (string, int64, error) {
	ns := c.input.namespace()
	peer, err := g.store.Reverse(ctx, ns, "group", numericID(raw))
	if err != nil {
		return "", 0, err
	}
	record, err := g.store.Lookup(ctx, store.Identity{Namespace: ns, Kind: "group", Native: peer})
	if err != nil {
		return "", 0, err
	}
	id := record.ExternalID()
	if id == 0 {
		id = numericID(raw)
	}
	if !c.input.officialBusinessGroup(record.Real) || !filterAllows(c.route.cfg.Groups, strconv.FormatInt(id, 10)) {
		return "", 0, actionError{1403, "group is outside the route scope"}
	}
	return peer, id, nil
}

func (g *Gateway) officialGroupProfile(ctx context.Context, c call, action string, p actionParams) (any, error) {
	peer, groupID, err := g.officialGroupTarget(ctx, c, p.GroupID)
	if err != nil {
		return nil, err
	}
	in := c.input
	if action == "get_group_member_list" {
		return g.officialMemberList(ctx, c, peer, groupID, p.NoCache)
	}
	requested := numericID(p.UserID)
	if requested <= 0 {
		return nil, actionError{1400, "invalid user_id"}
	}
	selfID, _ := strconv.ParseInt(in.cfg.SelfID, 10, 64)
	if requested == selfID {
		if !filterAllows(c.route.cfg.Users, in.cfg.SelfID) {
			return nil, actionError{1403, "user is outside the route scope"}
		}
		member, err := in.official.GroupMember(ctx, peer, "")
		if err != nil {
			return nil, err
		}
		var profile struct {
			Nickname string `json:"username"`
		}
		if err := in.official.Do(ctx, http.MethodGet, "/users/@me", nil, &profile); err != nil {
			return nil, err
		}
		member.Nickname = profile.Nickname
		return officialMemberData(in.cfg.AppID, groupID, selfID, member), nil
	}
	ns := in.namespace()
	native, err := g.store.Reverse(ctx, ns, "user", requested)
	if err != nil {
		return nil, err
	}
	record, err := g.store.Lookup(ctx, store.Identity{Namespace: ns, Kind: "user", Native: native})
	if err != nil {
		return nil, err
	}
	userID := record.ExternalID()
	if userID == 0 {
		userID = requested
	}
	if !filterAllows(c.route.cfg.Users, strconv.FormatInt(userID, 10)) {
		return nil, actionError{1403, "user is outside the route scope"}
	}
	member, err := in.official.GroupMember(ctx, peer, native)
	if err == nil {
		return officialMemberData(in.cfg.AppID, groupID, userID, member), nil
	}
	if !allowObservedProfile(in, p.NoCache, err) {
		return nil, err
	}
	observed, err := g.store.ObservedUser(ctx, ns, "group", native, []string{peer}, time.Now())
	if err != nil {
		return nil, err
	}
	return observedProfileData(in.cfg.AppID, groupID, userID, observed), nil
}

func (g *Gateway) officialMemberList(ctx context.Context, c call, peer string, groupID int64, noCache bool) ([]map[string]any, error) {
	in := c.input
	members, err := in.official.GroupMembers(ctx, peer)
	if err != nil {
		if !allowObservedProfile(in, noCache, err) {
			return nil, err
		}
		profiles, err := g.store.ObservedGroupMembers(ctx, in.namespace(), peer, time.Now())
		if err != nil {
			return nil, err
		}
		result := make([]map[string]any, 0, len(profiles))
		for _, profile := range profiles {
			id, err := g.officialProfileID(ctx, c, profile.Native)
			if err != nil {
				return nil, err
			}
			if id != 0 {
				result = append(result, observedProfileData(in.cfg.AppID, groupID, id, profile))
			}
		}
		if len(result) == 0 {
			return nil, store.ErrProfileNotFound
		}
		return result, nil
	}
	var self string
	for _, member := range members {
		if member.Bot {
			state, err := in.official.GroupMember(ctx, peer, "")
			if err == nil {
				self = state.OpenID
			}
			break
		}
	}
	result := make([]map[string]any, 0, len(members))
	for _, member := range members {
		var id int64
		if self == member.OpenID {
			if filterAllows(c.route.cfg.Users, in.cfg.SelfID) {
				id, _ = strconv.ParseInt(in.cfg.SelfID, 10, 64)
			}
		} else {
			id, err = g.officialProfileID(ctx, c, member.OpenID)
			if err != nil {
				return nil, err
			}
		}
		if id != 0 {
			result = append(result, officialMemberData(in.cfg.AppID, groupID, id, member))
		}
	}
	return result, nil
}

func (g *Gateway) officialProfileID(ctx context.Context, c call, native string) (int64, error) {
	key := store.Identity{Namespace: c.input.namespace(), Kind: "user", Native: native}
	record, err := g.store.Lookup(ctx, key)
	if err != nil {
		return 0, err
	}
	id := record.ExternalID()
	if id == 0 && len(c.route.cfg.Users) == 0 {
		id = g.store.Transient(key)
	}
	if !filterAllows(c.route.cfg.Users, strconv.FormatInt(id, 10)) {
		return 0, nil
	}
	return id, nil
}

func (g *Gateway) officialUserProfile(ctx context.Context, c call, p actionParams) (any, error) {
	in := c.input
	if !in.cfg.ObservedProfiles || p.NoCache {
		return nil, actionError{1404, "QQ user lookup requires observed_profiles and permits only observed data"}
	}
	requested := numericID(p.UserID)
	if requested <= 0 {
		return nil, actionError{1400, "invalid user_id"}
	}
	groups := c.route.cfg.Groups
	if len(groups) == 0 {
		groups = in.cfg.GroupIDs
	}
	if len(groups) == 0 && in.cfg.Role == config.Observe {
		groups = in.cfg.BusinessGroups
	}
	var peers []string
	scoped := len(groups) > 0 || in.cfg.Role == config.Observe
	if scoped {
		peers = make([]string, 0, len(groups))
		for _, group := range groups {
			peer, _, err := g.officialGroupTarget(ctx, c, json.RawMessage(group))
			var denied actionError
			switch {
			case err == nil:
				peers = append(peers, peer)
			case errors.Is(err, store.ErrUnmapped), errors.As(err, &denied):
			default:
				return nil, err
			}
		}
	}
	for _, chat := range []struct {
		kind    string
		peers   []string
		allowed bool
	}{
		{"group", peers, !scoped || len(peers) != 0},
		{"private", nil, c.route.allowsConversation("private", "") && in.cfg.PrivateEnabled()},
	} {
		if !chat.allowed {
			continue
		}
		ns := in.userNamespace(chat.kind)
		native, err := g.store.Reverse(ctx, ns, "user", requested)
		if errors.Is(err, store.ErrUnmapped) {
			continue
		}
		if err != nil {
			return nil, err
		}
		record, err := g.store.Lookup(ctx, store.Identity{Namespace: ns, Kind: "user", Native: native})
		if err != nil {
			return nil, err
		}
		if chat.kind == "private" && !in.officialPrivateAllowed(record.Real) {
			continue
		}
		id := record.ExternalID()
		if !filterAllows(c.route.cfg.Users, strconv.FormatInt(id, 10)) {
			continue
		}
		observed, err := g.store.ObservedUser(ctx, in.namespace(), chat.kind, native, chat.peers, time.Now())
		if errors.Is(err, store.ErrProfileNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return observedProfileData(in.cfg.AppID, 0, id, observed), nil
	}
	return nil, store.ErrProfileNotFound
}

func allowObservedProfile(in *input, noCache bool, err error) bool {
	var api *qqofficial.APIError
	return in.cfg.ObservedProfiles && !noCache && errors.As(err, &api) && api.Code == qqofficial.MissingAppPermission
}

func officialMemberData(appID string, groupID, userID int64, member qqofficial.GroupMember) map[string]any {
	data := map[string]any{"group_id": groupID, "user_id": userID, "nickname": member.Nickname,
		"avatar": qqofficial.AvatarURL(appID, member.OpenID), "hashinex_source": "official"}
	if role := qqofficial.GroupRole(member.Role); role != "" {
		data["role"] = role
	}
	if joined := member.JoinTime(); joined != 0 {
		data["join_time"] = joined
	}
	return data
}

func observedProfileData(appID string, groupID, userID int64, profile store.ObservedProfile) map[string]any {
	data := map[string]any{"user_id": userID, "nickname": profile.Nickname, "last_sent_time": profile.LastSent,
		"avatar": qqofficial.AvatarURL(appID, profile.Native), "hashinex_source": "observed"}
	if groupID != 0 {
		data["group_id"] = groupID
	}
	return data
}
