package gateway

import (
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hashinex/internal/config"
	"hashinex/internal/store"
)

func TestVirtualAdapterRoundTrip(t *testing.T) {
	cfg := testConfig()
	cfg.Inputs[0].IdentitySource = config.Virtual
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "adapter.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, binding := range []struct {
		kind, native string
		real         int64
	}{{"group", "77701", 999}, {"user", "77702", 333}} {
		err := db.SetBinding(t.Context(), store.Identity{
			Namespace: "ob:a", Kind: binding.kind, Native: binding.native,
		}, binding.real, "test")
		if err != nil {
			t.Fatal(err)
		}
	}
	g := New(t.Context(), cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(g.Close)
	ws := "ws" + strings.TrimPrefix(server.URL, "http")
	up := wsConnect(t, ws+"/onebot/in/a", "10001", "test-token")
	out := wsConnect(t, ws+"/onebot/out/left/a", "", "")
	_ = wsRead(t, out)
	wsWrite(t, up, `{"post_type":"message","self_id":10001,"group_id":"77701","user_id":77702,"message_id":12,"raw_message":"[CQ:at,qq=77702]"}`)
	got := wsRead(t, out)
	field(t, got, "self_id", "10001")
	field(t, got, "group_id", `"999"`)
	field(t, got, "user_id", "333")
	field(t, got, "raw_message", `"[CQ:at,qq=333]"`)
	wsWrite(t, out, `{"action":"get_group_member_info","params":{"group_id":"999","user_id":333},"echo":8}`)
	request := wsRead(t, up)
	field(t, request, "params", `{"group_id":"77701","user_id":77702}`)
	wsWrite(t, up, fmt.Sprintf(`{"status":"ok","retcode":0,"data":{"group_id":77701,"user_id":"77702"},"echo":%s}`, request["echo"]))
	response := wsRead(t, out)
	field(t, response, "data", `{"group_id":999,"user_id":"333"}`)
	field(t, response, "echo", "8")
	wsWrite(t, out, `{"action":"send_group_msg","params":{"group_id":999,"message":"[CQ:at,qq=123]"},"echo":"unmapped"}`)
	field(t, wsRead(t, out), "retcode", "1404")
}
