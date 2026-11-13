package transport

import (
	"context"
	"sync"
	"time"
)

// Timing publishes connection settings to current sessions and retry loops.
type Timing struct {
	mu        sync.Mutex
	heartbeat time.Duration
	reconnect time.Duration
	changed   chan struct{}
}

func NewTiming(heartbeat, reconnect time.Duration) *Timing {
	return &Timing{heartbeat: heartbeat, reconnect: reconnect, changed: make(chan struct{})}
}

func (t *Timing) Set(heartbeat, reconnect time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.heartbeat == heartbeat && t.reconnect == reconnect {
		return
	}
	t.heartbeat, t.reconnect = heartbeat, reconnect
	close(t.changed)
	t.changed = make(chan struct{})
}

func (t *Timing) snapshot() (time.Duration, time.Duration, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.heartbeat, t.reconnect, t.changed
}

func (t *Timing) WaitReconnect(ctx context.Context, disconnected time.Time, backoff time.Duration) bool {
	for ctx.Err() == nil {
		_, delay, changed := t.snapshot()
		if delay == 0 {
			delay = backoff
		}
		timer := time.NewTimer(time.Until(disconnected.Add(delay)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-changed:
			timer.Stop()
		case <-timer.C:
			return ctx.Err() == nil
		}
	}
	return false
}

func (t *Timing) ReconnectDelay(backoff time.Duration) time.Duration {
	_, delay, _ := t.snapshot()
	if delay != 0 {
		return delay
	}
	return backoff
}

func (t *Timing) Heartbeat(ctx context.Context, ping func() error) error {
	last := time.Now()
	for ctx.Err() == nil {
		interval, _, changed := t.snapshot()
		if interval <= 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-changed:
				continue
			}
		}
		timer := time.NewTimer(time.Until(last.Add(interval)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-changed:
			timer.Stop()
		case <-timer.C:
			if err := ping(); err != nil {
				return err
			}
			last = time.Now()
		}
	}
	return nil
}
