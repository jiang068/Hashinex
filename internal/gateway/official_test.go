package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/store"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOfficialWebhookIdentityAndReply(t *testing.T) {
	const secret = "test-webhook-secret"
	type send struct {
		Path string
		Body map[string]any
	}
	sent := make(chan send, 4)
	oldTransport := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.bot.qq.com" {
			return oldTransport.RoundTrip(req)
		}
		reply := `{"access_token":"test-access-token","expires_in":"7200"}`
		if req.URL.Path != "/app/getAppAccessToken" {
			if req.Header.Get("Authorization") != "QQBot test-access-token" || req.Method != http.MethodPost {
				return nil, fmt.Errorf("unexpected official request")
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				return nil, err
			}
			sent <- send{req.URL.Path, body}
			reply = `{"id":"sent-qq-1"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := testConfig()
	cfg.Inputs = []config.Input{
		{Endpoint: config.Endpoint{ID: "official-a"}, Protocol: config.QQOfficial,
			SelfID: "10001", AppID: "10001", Events: "webhook", Secret: secret, Role: config.Active},
		{Endpoint: config.Endpoint{ID: "official-b"}, Protocol: config.QQOfficial,
			SelfID: "10002", AppID: "10002", Events: "webhook", Secret: secret, Role: config.Active},
	}
	cfg.Routes = []config.Route{
		{From: "official-a", To: "left", AllowActions: true, ImplicitReply: "unique-recent"},
		{From: "official-b", To: "left", AllowActions: true, ImplicitReply: "unique-recent"},
	}
	cfg.Storage = config.Storage{MessageTTL: time.Hour, ReplyWindow: time.Minute}
	for _, identity := range []struct {
		key store.Identity
		id  int64
	}{
		{store.Identity{Namespace: "qq:10001", Kind: "group", Native: "GROUP_A"}, 999},
		{store.Identity{Namespace: "qq:10002", Kind: "group", Native: "GROUP_B"}, 999},
		{store.Identity{Namespace: "qq:10001", Kind: "user", Native: "USER_A"}, 333},
		{store.Identity{Namespace: "qq:10002", Kind: "user", Native: "USER_B"}, 333},
	} {
		if err := db.SetBinding(t.Context(), identity.key, identity.id, "test"); err != nil {
			t.Fatal(err)
		}
	}
	g := New(t.Context(), cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(g.Close)
	ws := "ws" + strings.TrimPrefix(server.URL, "http")
	outA := wsConnect(t, ws+"/onebot/out/left/official-a", "", "")
	outB := wsConnect(t, ws+"/onebot/out/left/official-b", "", "")
	_ = wsRead(t, outA)
	_ = wsRead(t, outB)
	callback := func(input, group, member, msgID string, valid bool) int {
		t.Helper()
		body := fmt.Sprintf(`{"op":0,"id":"event-1","t":"GROUP_MESSAGE_CREATE","d":{"id":%q,"group_openid":%q,"author":{"member_openid":%q},"timestamp":%q,"content":"hello world","message_scene":{"ext":["msg_idx=REFIDX_1"]}}}`,
			msgID, group, member, time.Now().Format(time.RFC3339Nano))
		return postCallback(t, server.URL+"/qq/webhook/"+input, body, secret, valid)
	}
	if status := callback("official-a", "GROUP_A", "USER_A", "msg-1", false); status != http.StatusUnauthorized {
		t.Fatalf("invalid signature returned %d", status)
	}
	if status := callback("official-a", "GROUP_A", "USER_A", "msg-1", true); status != http.StatusOK {
		t.Fatalf("signed webhook returned %d", status)
	}
	if status := callback("official-b", "GROUP_B", "USER_B", "other-msg", true); status != http.StatusOK {
		t.Fatalf("second signed webhook returned %d", status)
	}
	packetA, packetB := wsRead(t, outA), wsRead(t, outB)
	field(t, packetA, "self_id", "10001")
	field(t, packetB, "self_id", "10002")
	for _, packet := range []map[string]json.RawMessage{packetA, packetB} {
		field(t, packet, "user_id", "333")
		field(t, packet, "group_id", "999")
	}
	wsWrite(t, outB, fmt.Sprintf(`{"action":"get_msg","params":{"message_id":%s},"echo":"wrong bot"}`, packetA["message_id"]))
	field(t, wsRead(t, outB), "retcode", "1404")
	wsWrite(t, outA, `{"action":"send_group_msg","params":{"group_id":999,"message":"a reply"},"echo":"same"}`)
	field(t, wsRead(t, outA), "retcode", "0")
	wsWrite(t, outB, `{"action":"send_group_msg","params":{"group_id":999,"message":"another reply"},"echo":"same"}`)
	field(t, wsRead(t, outB), "retcode", "0")
	first, second := <-sent, <-sent
	if first.Path != "/v2/groups/GROUP_A/messages" || first.Body["msg_id"] != "msg-1" ||
		second.Path != "/v2/groups/GROUP_B/messages" || second.Body["msg_id"] != "other-msg" ||
		first.Body["msg_seq"] != float64(1) || second.Body["msg_seq"] != float64(1) {
		t.Fatal("incorrect official reply context")
	}
	if status := callback("official-a", "GROUP_A", "USER_A", "msg-2", true); status != http.StatusOK {
		t.Fatalf("second message returned %d", status)
	}
	_ = wsRead(t, outA)
	wsWrite(t, outA, `{"action":"send_group_msg","params":{"group_id":999,"message":"ambiguous"}}`)
	field(t, wsRead(t, outA), "retcode", "1409")
	wsWrite(t, outA, fmt.Sprintf(`{"action":"send_group_msg","params":{"group_id":999,"message":[{"type":"reply","data":{"id":%s}},{"type":"text","data":{"text":"quoted"}}]}}`, packetA["message_id"]))
	field(t, wsRead(t, outA), "retcode", "0")
	quoted := <-sent
	ref, _ := quoted.Body["message_reference"].(map[string]any)
	if ref["message_id"] != "REFIDX_1" || quoted.Body["msg_id"] != "msg-1" || quoted.Body["msg_seq"] != float64(2) {
		t.Fatal("quote index and passive reply credential were not preserved independently")
	}
}

func postCallback(t *testing.T, address, body, secret string, valid bool) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var seed [ed25519.SeedSize]byte
	for i := range seed {
		seed[i] = secret[i%len(secret)]
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed[:]), []byte(stamp+body))
	if !valid {
		sig[0] ^= 0xff
	}
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
	req.Header.Set("X-Signature-Timestamp", stamp)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}
