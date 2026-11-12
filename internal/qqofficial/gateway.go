package qqofficial

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type session struct {
	id  string
	seq int64
}

func (c *Client) RunGateway(ctx context.Context, intents uint64, frameLimit int64, ingest func(Dispatch) bool,
	online func(bool), log *slog.Logger) {
	var current session
	delay := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.gatewayOnce(ctx, intents, frameLimit, &current, ingest, online)
		online(false)
		if ctx.Err() != nil {
			return
		}
		code := websocket.CloseStatus(err)
		log.Warn("QQ gateway session ended", "reason", err, "close_code", code)
		if code == 4914 || code == 4915 {
			return
		}
		if code >= 4000 && code <= 4999 {
			current = session{}
		}
		if time.Since(start) > 30*time.Second {
			delay = time.Second
		}
		timer := time.NewTimer(delay + rand.N(delay/4))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func (c *Client) gatewayOnce(ctx context.Context, intents uint64, frameLimit int64, current *session,
	ingest func(Dispatch) bool, online func(bool)) error {
	var response struct {
		URL string `json:"url"`
	}
	if err := c.Do(ctx, http.MethodGet, "/gateway", nil, &response); err != nil {
		return err
	}
	address, err := url.Parse(response.URL)
	if err != nil || address.Scheme != "wss" || address.Hostname() == "" || address.User != nil {
		return errors.New("QQ returned an invalid Gateway URL")
	}
	token, err := c.AccessToken(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	handshake, finish := context.WithTimeout(ctx, 10*time.Second)
	// The dial client has a separate request lifetime from the ordinary HTTP API.
	conn, _, err := websocket.Dial(handshake, address.String(), &websocket.DialOptions{
		HTTPClient: &http.Client{CheckRedirect: c.http.CheckRedirect},
	})
	finish()
	if err != nil {
		return errors.New("QQ Gateway WebSocket dial failed")
	}
	defer conn.CloseNow()
	conn.SetReadLimit(frameLimit)

	helloCtx, cancelHello := context.WithTimeout(ctx, 10*time.Second)
	first, err := readDispatch(helloCtx, conn)
	cancelHello()
	if err != nil {
		return err
	}
	var hello struct {
		Interval int64 `json:"heartbeat_interval"`
	}
	if first.Op != 10 || json.Unmarshal(first.Data, &hello) != nil || hello.Interval < 1000 || hello.Interval > 300000 {
		return errors.New("invalid QQ Gateway Hello")
	}
	auth := "QQBot " + token
	if current.id == "" {
		err = writeControl(ctx, conn, 2, map[string]any{
			"token": auth, "intents": intents, "shard": [2]int{0, 1}, "properties": map[string]string{},
		})
	} else {
		err = writeControl(ctx, conn, 6, map[string]any{"token": auth, "session_id": current.id, "seq": current.seq})
	}
	if err != nil {
		return err
	}
	var seq atomic.Int64
	seq.Store(current.seq)
	var waiting atomic.Bool
	heartbeat := make(chan struct{})
	go func() {
		defer close(heartbeat)
		ticker := time.NewTicker(time.Duration(hello.Interval) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if waiting.Swap(true) || writeHeartbeat(ctx, conn, seq.Load()) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-heartbeat
	}()
	for {
		d, err := readDispatch(ctx, conn)
		if err != nil {
			return err
		}
		switch d.Op {
		case 0:
			switch d.Type {
			case "READY":
				var ready struct {
					SessionID string `json:"session_id"`
				}
				if json.Unmarshal(d.Data, &ready) != nil || ready.SessionID == "" {
					return errors.New("invalid QQ Gateway READY")
				}
				current.id = ready.SessionID
				online(true)
			case "RESUMED":
				online(true)
			default:
				if !ingest(d) {
					return errors.New("QQ event queue full")
				}
			}
			if d.Seq != nil {
				current.seq = *d.Seq
				seq.Store(*d.Seq)
			}
		case 1:
			if err := writeHeartbeat(ctx, conn, seq.Load()); err != nil {
				return err
			}
		case 7:
			return errors.New("QQ requested Gateway reconnect")
		case 9:
			*current = session{}
			return errors.New("QQ rejected Gateway session or intents")
		case 11:
			waiting.Store(false)
		}
	}
}

func writeHeartbeat(ctx context.Context, conn *websocket.Conn, seq int64) error {
	if seq == 0 {
		return writeControl(ctx, conn, 1, nil)
	}
	return writeControl(ctx, conn, 1, seq)
}

func writeControl(ctx context.Context, conn *websocket.Conn, op int, data any) error {
	b, err := json.Marshal(map[string]any{"op": op, "d": data})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

func readDispatch(ctx context.Context, conn *websocket.Conn) (Dispatch, error) {
	kind, raw, err := conn.Read(ctx)
	if err != nil {
		// Keep the close code without copying the remote close reason into logs.
		if code := websocket.CloseStatus(err); code != -1 {
			return Dispatch{}, websocket.CloseError{Code: code}
		}
		return Dispatch{}, errors.New("QQ Gateway read ended or timed out")
	}
	var payload Dispatch
	if kind != websocket.MessageText || json.Unmarshal(raw, &payload) != nil {
		return Dispatch{}, errors.New("invalid QQ Gateway frame")
	}
	payload.Raw = raw
	return payload, nil
}
