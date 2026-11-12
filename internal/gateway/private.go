package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"hashinex/internal/onebot"
	"hashinex/internal/qqofficial"
	"hashinex/internal/store"
)

const privateCodePrefix = "HX-"
const privateChallengeTTL = 10 * time.Minute

var privateCodePattern = regexp.MustCompile(`^HX-[A-Z2-7]{16}$`)

func privateBindingCode(text string) string {
	code := strings.ToUpper(strings.TrimSpace(text))
	if !privateCodePattern.MatchString(code) {
		return ""
	}
	return code
}

type privatePrompt struct {
	peer, message string
	expires       time.Time
	code          string
}

func (g *Gateway) queuePrivatePrompt(ctx context.Context, in *input, msg qqofficial.Message, received time.Time) {
	if in.privatePrompts == nil {
		return
	}
	_, created, err := g.store.CreateMessage(ctx, store.Message{
		Namespace: in.namespace(), ChatKind: "private", Peer: msg.Peer, UserNative: msg.User,
		NativeID: msg.NativeID, EventID: msg.EventID, Incoming: true,
		ReceivedAt: received, ExpiresAt: received.Add(g.storage.MessageTTL),
	}, nil)
	if err != nil {
		g.recordOfficialDrop(in, "deduplicate private binding request", err)
		return
	}
	if !created {
		return
	}
	g.enqueuePrivatePrompt(in, privatePrompt{peer: msg.Peer, message: msg.NativeID, expires: received.Add(g.storage.ReplyWindow)})
}

func (g *Gateway) enqueuePrivatePrompt(in *input, prompt privatePrompt) {
	g.eventWork.Add(1)
	select {
	case in.privatePrompts <- prompt:
	default:
		g.eventWork.Add(-1)
		g.stats.dropped.Add(1)
		g.log.Warn("private reply queue full", "input", in.cfg.ID)
	}
}

func (g *Gateway) runPrivatePrompts(in *input) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case prompt := <-in.privatePrompts:
			g.guardWorker(in, func() {
				defer g.eventWork.Add(-1)
				deadline := time.Now().Add(g.limits.APITimeout)
				ctx, cancel := context.WithDeadline(g.ctx, minTime(deadline, prompt.expires))
				defer cancel()
				var err error
				if prompt.code == "" {
					err = g.sendPrivatePrompt(ctx, in, prompt)
				} else {
					err = g.sendPrivateConfirmation(ctx, in, prompt.code)
				}
				if err != nil {
					g.log.Warn("private binding prompt failed", "input", in.cfg.ID, "reason", err)
				}
			})
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (g *Gateway) sendPrivatePrompt(ctx context.Context, in *input, prompt privatePrompt) error {
	key := store.Identity{Namespace: in.userNamespace("private"), Kind: "user", Native: prompt.peer}
	user, err := g.store.Lookup(ctx, key)
	if err != nil || user.Real != 0 {
		return err
	}
	text := "私聊暂不可用"
	if g.hasPrivateObserver(in) {
		var entropy [10]byte
		rand.Read(entropy[:])
		code := privateCodePrefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(entropy[:])
		now := time.Now()
		code, err = g.store.PrivateChallenge(ctx, key, 0, code, now, now.Add(privateChallengeTTL))
		if err != nil || code == "" {
			return err
		}
		text = "请在你和我同在的群里 @我，发送 " + code + " 绑定账号"
	}
	if err := g.sendBindingReply(ctx, in, "private", prompt.peer, prompt.message, prompt.expires, text); err != nil {
		return err
	}
	g.log.Info("private binding prompt sent", "input", in.cfg.ID)
	return nil
}

func (g *Gateway) sendBindingReply(ctx context.Context, in *input, kind, peer, message string, expires time.Time, text string) error {
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	seq, err := g.store.NextSequence(ctx, in.namespace(), peer, message, expires)
	if err != nil {
		return err
	}
	body := map[string]any{"msg_type": 0, "content": text, "msg_id": message, "msg_seq": seq}
	return in.official.Do(ctx, http.MethodPost, qqofficial.ConversationPath(kind, peer)+"/messages", body, nil)
}

type privateCheck struct {
	code  string
	group int64
}

func officialPrivateCode(msg qqofficial.Message) string {
	text := strings.TrimSpace(msg.Text)
	match := qqofficial.MentionPattern.FindStringSubmatchIndex(text)
	if len(match) != 0 && match[0] == 0 {
		native := text[match[2]:match[3]]
		if msg.AtBot {
			return privateBindingCode(text[match[1]:])
		}
		for _, mention := range msg.Mentions {
			if mention.IsSelf && (mention.ID == native || mention.OpenID == native) {
				return privateBindingCode(text[match[1]:])
			}
		}
	}
	return privateBindingCode(text)
}

func (g *Gateway) observePrivateCode(in *input, e event, code string) {
	if code == "" || g.privateChecks == nil {
		return
	}
	group, user := numericID(e.frame.GroupID), numericID(e.frame.UserID)
	if group <= 0 || group >= store.VirtualBase || user <= 0 || user >= store.VirtualBase ||
		onebot.ID(e.frame.UserID) == in.cfg.SelfID {
		return
	}
	ctx, cancel := context.WithDeadline(g.ctx, e.deadline)
	defer cancel()
	saved, err := g.store.ObservePrivateCode(ctx, code, group, user, time.Now())
	if err != nil {
		g.log.Warn("private binding observation failed", "input", in.cfg.ID, "reason", err)
	} else if saved {
		g.queuePrivateCheck(privateCheck{code: code, group: group})
	}
}

func (g *Gateway) queuePrivateCheck(check privateCheck) {
	if g.privateChecks == nil {
		return
	}
	g.eventWork.Add(1)
	select {
	case g.privateChecks <- check:
	default:
		g.eventWork.Add(-1)
		g.stats.dropped.Add(1)
		g.log.Warn("private avatar verification queue full")
	}
}

func (g *Gateway) runPrivateChecks() {
	for {
		select {
		case <-g.ctx.Done():
			return
		case check := <-g.privateChecks:
			g.checkPrivateCode(check)
		}
	}
}

func (g *Gateway) checkPrivateCode(check privateCheck) {
	defer g.eventWork.Add(-1)
	ctx, cancel := context.WithTimeout(g.ctx, 8*time.Second)
	defer cancel()
	key, users, err := g.store.PrivateCandidates(ctx, check.code, check.group, time.Now())
	if err != nil {
		g.log.Warn("private binding lookup failed", "reason", err)
		return
	}
	if key.Namespace == "" {
		return
	}
	for _, in := range g.inputs {
		if in.official != nil && in.userNamespace("private") == key.Namespace && in.officialGroupAllowed(check.group) {
			for _, user := range users {
				if !in.officialPrivateAllowed(user) {
					continue
				}
				if g.verifyPrivateBinding(ctx, in, key, check, user) || ctx.Err() != nil {
					break
				}
			}
			return
		}
	}
}

func (g *Gateway) verifyPrivateBinding(ctx context.Context, in *input, key store.Identity, check privateCheck, user int64) bool {
	distance, matches := g.privateAvatars.Verify(ctx, in.cfg.AppID, key.Native, user, g.matching.Distance)
	if !matches {
		g.log.Info("private binding avatar rejected", "input", in.cfg.ID)
		return false
	}
	bound, err := g.store.RedeemPrivateChallenge(ctx, key, check.code, in.namespace(), check.group, user, time.Now())
	switch {
	case errors.Is(err, store.ErrBindingConflict):
		g.log.Warn("private account binding conflict")
	case err != nil:
		g.log.Warn("private account binding failed", "reason", err)
	case bound:
		g.log.Info("private account bound from observer", "input", in.cfg.ID, "avatar_distance", distance)
		g.queuePrivateConfirmation(in, check.code)
	}
	return bound
}

func (g *Gateway) rememberPrivateConfirmation(ctx context.Context, in *input, code string, msg qqofficial.Message, group int64, received time.Time) {
	if in.privatePrompts == nil {
		return
	}
	reply := store.PrivateConfirmation{Peer: msg.Peer, Message: msg.NativeID, Expires: received.Add(g.storage.ReplyWindow)}
	ready, err := g.store.RememberPrivateGroup(ctx, in.userNamespace("private"), code, group, reply, time.Now())
	if err != nil {
		g.log.Warn("could not save private binding confirmation", "input", in.cfg.ID, "reason", err)
	} else if ready {
		g.queuePrivateCheck(privateCheck{code: code, group: group})
	}
}

func (g *Gateway) queuePrivateConfirmation(in *input, code string) {
	g.enqueuePrivatePrompt(in, privatePrompt{code: code, expires: time.Now().Add(g.limits.APITimeout)})
}

func (g *Gateway) sendPrivateConfirmation(ctx context.Context, in *input, code string) error {
	reply, err := g.store.ClaimPrivateConfirmation(ctx, in.userNamespace("private"), code, time.Now())
	if err != nil || reply.Message == "" {
		return err
	}
	if err := g.sendBindingReply(ctx, in, "group", reply.Peer, reply.Message, reply.Expires, "绑定成功"); err != nil {
		return err
	}
	g.log.Info("private binding confirmed in group", "input", in.cfg.ID)
	return nil
}
