package matching

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/store"
)

func TestUniquePairRejectsCompetition(t *testing.T) {
	now := time.Now()
	first := Event{Namespace: "a", Key: "official", Time: now, Received: now}
	observation := Event{GroupID: 999, Key: "observation", Time: now, Received: now}
	otherGroup := Event{GroupID: 888, Key: "different group", Time: now, Received: now}
	if _, ok := uniquePair([]Event{first, observation, otherGroup}, first, 2*time.Second); ok {
		t.Fatal("unconfirmed group allowed an ambiguous pair")
	}
	first.KnownGroup = 999
	second := Event{Namespace: "a", Key: "other official", Time: now, Received: now, KnownGroup: 999}
	if _, ok := uniquePair([]Event{first, second, observation, otherGroup}, first, 2*time.Second); ok {
		t.Fatal("observer message used for competing official messages")
	}
	second.Namespace = "b"
	if got, ok := uniquePair([]Event{first, second, observation, otherGroup}, first, 2*time.Second); !ok || got.Key != observation.Key {
		t.Fatal("independent AppIDs should pair independently")
	}
}

type avatarTransport []byte

func (a avatarTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200, Body: io.NopCloser(bytes.NewReader(a)), ContentLength: int64(len(a)),
	}, nil
}

func TestShadowThenAutoWithIndependentAppIDs(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "matching.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Matching{
		Mode: "shadow", PairWindow: 100 * time.Millisecond, EventTTL: 450 * time.Millisecond,
		EvidenceTTL: time.Hour, Successes: 3, GroupSuccesses: 3, GroupSenders: 2, Distance: 4,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var body bytes.Buffer
	for y := range 64 {
		for x := range 64 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x*3 + y*11), G: uint8(x * 4), B: uint8(y * 4), A: 255})
		}
	}
	if err := png.Encode(&body, img); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"qq:10001", "qq:10002"} {
		err := db.SetBinding(t.Context(), store.Identity{Namespace: ns, Kind: "group", Native: "GROUP"}, 999, "test")
		if err != nil {
			t.Fatal(err)
		}
	}
	run := func(mode string, start, count int) {
		t.Helper()
		cfg.Mode = mode
		engine := New(cfg, db, log)
		engine.avatars.client.Transport = avatarTransport(body.Bytes())
		ctx, stop := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			engine.Run(ctx)
		}()
		defer func() { stop(); <-done }()
		for i := start; i < start+count; i++ {
			now := time.Now()
			text := fmt.Sprintf("unique message %d", i)
			key := fmt.Sprint(i)
			engine.Offer(Event{Text: text, Time: now, Received: now, GroupID: 999, UserID: 333, Key: "watch-" + key})
			for _, app := range []string{"10001", "10002"} {
				engine.Offer(Event{
					Namespace: "qq:" + app, AppID: app, UserNative: "OPEN_" + app, GroupNative: "GROUP", KnownGroup: 999,
					Text: text, Time: now, Received: now, Key: "official-" + key,
				})
			}
		}
		until := time.Now().Add(3 * time.Second)
		for time.Now().Before(until) && engine.matched.Load() < uint64(2*count) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := engine.matched.Load(); got != uint64(2*count) {
			t.Fatalf("accepted evidence count = %d, want %d", got, 2*count)
		}
	}
	run("shadow", 1, 3)
	for _, app := range []string{"10001", "10002"} {
		key := store.Identity{Namespace: "qq:" + app, Kind: "user", Native: "OPEN_" + app}
		record, err := db.Resolve(t.Context(), key)
		if err != nil || record.Real != 0 || record.Virtual < store.VirtualBase {
			t.Fatalf("shadow changed exposed identity: %+v %v", record, err)
		}
	}
	run("auto", 4, 1)
	for _, app := range []string{"10001", "10002"} {
		native, err := db.Reverse(t.Context(), "qq:"+app, "user", 333)
		if err != nil || native != "OPEN_"+app {
			t.Fatalf("could not reverse bound identity: %q %v", native, err)
		}
	}
}
