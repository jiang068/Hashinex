package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"hashinex/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Inputs: []config.Input{
			{Endpoint: config.Endpoint{ID: "a", Mode: config.ReverseListen, Token: "test-token"}, SelfID: "10001", Role: config.Active},
			{Endpoint: config.Endpoint{ID: "b", Mode: config.ReverseListen}, SelfID: "10002", Role: config.Active},
			{Endpoint: config.Endpoint{ID: "observer", Mode: config.ReverseListen}, SelfID: "10003", Role: config.Observe},
		},
		Outputs: []config.Output{
			{Endpoint: config.Endpoint{ID: "left", Mode: config.ForwardListen}},
			{Endpoint: config.Endpoint{ID: "right", Mode: config.ForwardListen}},
		},
		Routes: []config.Route{
			{From: "a", To: "left", AllowActions: true, Groups: []string{"999"}},
			{From: "a", To: "right", AllowActions: true},
			{From: "b", To: "left", AllowActions: true},
		},
		Limits: config.Limits{
			FrameBytes: 1 << 20, EventQueue: 8, APIQueue: 8, PendingPerPeer: 4, PendingPerInput: 8,
			WriteTimeout: 2 * time.Second, APITimeout: time.Second, EventTTL: 3 * time.Second,
			DedupTTL: time.Minute, DedupSize: 8, PingInterval: time.Minute,
		},
	}
}

func testGateway(t *testing.T, cfg *config.Config) (*Gateway, string) {
	t.Helper()
	g := New(t.Context(), cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(g.Close)
	return g, "ws" + strings.TrimPrefix(server.URL, "http")
}

func wsConnect(t *testing.T, url, self, token string) *websocket.Conn {
	t.Helper()
	headers := make(http.Header)
	if self != "" {
		headers.Set("X-Self-ID", self)
		headers.Set("X-Client-Role", "Universal")
	}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func wsWrite(t *testing.T, conn *websocket.Conn, value string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(value)); err != nil {
		t.Fatal(err)
	}
}

func wsRead(t *testing.T, conn *websocket.Conn) map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func field(t *testing.T, frame map[string]json.RawMessage, key, want string) {
	t.Helper()
	if got := string(frame[key]); got != want {
		t.Fatalf("%s = %s, want %s", key, got, want)
	}
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for gateway state")
}

func TestRoutesAndEchoIsolation(t *testing.T) {
	g, addr := testGateway(t, testConfig())
	a := wsConnect(t, addr+"/onebot/in/a", "10001", "test-token")
	b := wsConnect(t, addr+"/onebot/in/b", "10002", "")
	watch := wsConnect(t, addr+"/onebot/in/observer", "10003", "")
	left := wsConnect(t, addr+"/onebot/out/left/a", "", "")
	right := wsConnect(t, addr+"/onebot/out/right/a", "", "")
	other := wsConnect(t, addr+"/onebot/out/left/b", "", "")
	field(t, wsRead(t, left), "self_id", "10001")
	field(t, wsRead(t, right), "self_id", "10001")
	field(t, wsRead(t, other), "self_id", "10002")

	wsWrite(t, a, `{"post_type":"message","self_id":10001,"group_id":999,"user_id":333,"message_id":1}`)
	field(t, wsRead(t, left), "message_id", "1")
	field(t, wsRead(t, right), "message_id", "1")
	wsWrite(t, a, `{"post_type":"message","self_id":10001,"group_id":999,"user_id":333,"message_id":"1"}`)
	wsWrite(t, a, `{"post_type":"message","self_id":10001,"group_id":998,"user_id":333,"message_id":2}`)
	field(t, wsRead(t, right), "message_id", "2")
	wsWrite(t, watch, `{"post_type":"message","self_id":10003,"group_id":999,"user_id":333,"message_id":1}`)
	eventually(t, func() bool { return g.stats.observed.Load() == 1 && g.stats.duplicate.Load() == 1 })

	wsWrite(t, left, `{"action":"get_status","echo":{"same":7}}`)
	wsWrite(t, right, `{"action":"get_status","echo":{"same":7}}`)
	wsWrite(t, other, `{"action":"get_status"}`)
	first, second := wsRead(t, a)["echo"], wsRead(t, a)["echo"]
	if string(first) == "" || string(second) == "" || string(first) == string(second) {
		t.Fatal("expected distinct internal request keys")
	}
	bKey := wsRead(t, b)["echo"]
	wsWrite(t, b, fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%s,"data":"wrong peer"}`, first))
	wsWrite(t, a, fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%s,"data":"a"}`, second))
	wsWrite(t, a, fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%s,"data":"a"}`, first))
	wsWrite(t, b, fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%s,"data":"b"}`, bKey))
	for _, out := range []*websocket.Conn{left, right} {
		frame := wsRead(t, out)
		field(t, frame, "echo", `{"same":7}`)
		field(t, frame, "data", `"a"`)
	}
	frame := wsRead(t, other)
	field(t, frame, "echo", "")
	field(t, frame, "data", `"b"`)
	wsWrite(t, left, `{"action":"get_status","params":{"self_id":10002},"echo":null}`)
	frame = wsRead(t, left)
	field(t, frame, "retcode", "1403")
	field(t, frame, "echo", "null")
}

func TestTimeoutAndReconnect(t *testing.T) {
	cfg := testConfig()
	cfg.Limits.APITimeout = 125 * time.Millisecond
	g, addr := testGateway(t, cfg)
	up := wsConnect(t, addr+"/onebot/in/a", "10001", "test-token")
	out := wsConnect(t, addr+"/onebot/out/left/a", "", "")
	_ = wsRead(t, out)
	wsWrite(t, out, `{"action":"get_status","echo":0}`)
	oldKey := wsRead(t, up)["echo"]
	frame := wsRead(t, out)
	field(t, frame, "retcode", "1504")
	field(t, frame, "echo", "0")
	wsWrite(t, up, fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%s}`, oldKey))
	wsWrite(t, out, `{"action":"get_status","echo":"disconnect"}`)
	_ = wsRead(t, up)
	_ = up.CloseNow()
	field(t, wsRead(t, out), "retcode", "1502")
	eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.inputs["a"].peer == nil && len(g.pending) == 0
	})
	up = wsConnect(t, addr+"/onebot/in/a", "10001", "test-token")
	wsWrite(t, out, `{"action":"get_status","echo":false}`)
	request := wsRead(t, up)
	wsWrite(t, up, fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%s}`, request["echo"]))
	frame = wsRead(t, out)
	field(t, frame, "echo", "false")
	field(t, frame, "retcode", "0")
}

func TestSlowDownstreamDoesNotStopHealthyRoute(t *testing.T) {
	cfg := testConfig()
	cfg.Limits.EventQueue = 2
	cfg.Limits.WriteTimeout = 250 * time.Millisecond
	g, addr := testGateway(t, cfg)
	up := wsConnect(t, addr+"/onebot/in/a", "10001", "test-token")
	fast := wsConnect(t, addr+"/onebot/out/left/a", "", "")
	slow := wsConnect(t, addr+"/onebot/out/right/a", "", "")
	_ = wsRead(t, fast)
	_ = wsRead(t, slow)
	payload := strings.Repeat("x", 192<<10)
	for i := 1; i <= 96; i++ {
		wsWrite(t, up, fmt.Sprintf(`{"post_type":"message","self_id":10001,"group_id":999,"message_id":%d,"message":"%s"}`, i, payload))
		field(t, wsRead(t, fast), "message_id", fmt.Sprint(i))
		if g.stats.dropped.Load() > 0 {
			break
		}
	}
	eventually(t, func() bool { return g.stats.dropped.Load() > 0 })
	wsWrite(t, fast, `{"action":"get_status","echo":"still online"}`)
	request := wsRead(t, up)
	wsWrite(t, up, fmt.Sprintf(`{"status":"ok","retcode":0,"echo":%s}`, request["echo"]))
	field(t, wsRead(t, fast), "echo", `"still online"`)
}
