package transport

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"hashinex/internal/config"
)

type packet struct {
	raw      []byte
	deadline time.Time
}

type Peer struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	conn     *websocket.Conn
	events   chan packet
	api      chan packet
	limits   config.Limits
	log      *slog.Logger
	buffered atomic.Int64
}

func NewPeer(parent context.Context, conn *websocket.Conn, limits config.Limits, log *slog.Logger) *Peer {
	ctx, cancel := context.WithCancelCause(parent)
	conn.SetReadLimit(limits.FrameBytes)
	return &Peer{
		ctx: ctx, cancel: cancel, conn: conn, limits: limits, log: log,
		events: make(chan packet, limits.EventQueue), api: make(chan packet, limits.APIQueue),
	}
}

func (p *Peer) Stop(cause error) {
	p.cancel(cause)
}

func (p *Peer) SendEvent(raw []byte, deadline time.Time) bool {
	return p.enqueue(p.events, packet{raw: raw, deadline: deadline})
}

func (p *Peer) SendAPI(raw []byte, deadline time.Time) bool {
	return p.enqueue(p.api, packet{raw: raw, deadline: deadline})
}

func (p *Peer) enqueue(queue chan packet, msg packet) bool {
	if p.ctx.Err() != nil {
		return false
	}
	p.buffered.Add(1)
	select {
	case queue <- msg:
		return true
	case <-p.ctx.Done():
	default:
	}
	p.buffered.Add(-1)
	return false
}

func (p *Peer) PendingWrites() int64 {
	return p.buffered.Load()
}

func (p *Peer) Run(onFrame func([]byte), onUndelivered func(int)) {
	var workers sync.WaitGroup
	workers.Go(func() { p.guard("writer", func() { p.write(onUndelivered) }) })
	workers.Go(func() { p.guard("ping", p.ping) })
	defer func() {
		p.Stop(context.Canceled)
		_ = p.conn.CloseNow()
		workers.Wait()
		if queued := len(p.events); queued > 0 {
			onUndelivered(queued)
		}
	}()
	p.guard("reader", func() {
		for p.ctx.Err() == nil {
			kind, raw, err := p.conn.Read(p.ctx)
			if err != nil {
				switch {
				case errors.Is(err, websocket.ErrMessageTooBig):
					p.log.Warn("websocket message too large", "read_limit_bytes", p.limits.FrameBytes)
					p.Stop(websocket.ErrMessageTooBig)
				case websocket.CloseStatus(err) == websocket.StatusMessageTooBig:
					p.Stop(errors.New("websocket peer rejected an oversized message"))
				default:
					p.Stop(errors.New("websocket read ended"))
				}
				return
			}
			if kind != websocket.MessageText {
				p.Stop(errors.New("expected text frames"))
				return
			}
			onFrame(raw)
		}
	})
	p.log.Info("connection ended", "reason", context.Cause(p.ctx))
}

func (p *Peer) guard(worker string, run func()) {
	defer func() {
		if recover() != nil {
			p.log.Error("websocket worker panicked", "worker", worker)
			p.Stop(errors.New("worker panic"))
		}
	}()
	run()
}

func (p *Peer) write(onUndelivered func(int)) {
	for p.ctx.Err() == nil {
		var msg packet
		var event bool
		select {
		case msg = <-p.api:
		default:
			select {
			case <-p.ctx.Done():
				return
			case msg = <-p.api:
			case msg = <-p.events:
				event = true
			}
		}
		if !msg.deadline.IsZero() && time.Now().After(msg.deadline) {
			p.buffered.Add(-1)
			if event {
				onUndelivered(1)
				p.Stop(errors.New("event delivery expired"))
				return
			}
			// An expired upstream API request is settled by the gateway's timeout sweep.
			continue
		}
		deadline := time.Now().Add(p.limits.WriteTimeout)
		if !msg.deadline.IsZero() && msg.deadline.Before(deadline) {
			deadline = msg.deadline
		}
		ctx, cancel := context.WithDeadline(p.ctx, deadline)
		err := p.conn.Write(ctx, websocket.MessageText, msg.raw)
		cancel()
		p.buffered.Add(-1)
		if err != nil {
			if event {
				onUndelivered(1)
			}
			p.Stop(errors.New("websocket write failed"))
			return
		}
	}
}

func (p *Peer) ping() {
	ticker := time.NewTicker(p.limits.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(p.ctx, p.limits.WriteTimeout)
			err := p.conn.Ping(ctx)
			cancel()
			if err != nil {
				p.Stop(errors.New("websocket ping timeout"))
				return
			}
		}
	}
}
