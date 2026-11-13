package gscore

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"hashinex/internal/config"
	"hashinex/internal/onebot"
	"hashinex/internal/transport"
)

const (
	maxPending    = 32
	actionTimeout = 25 * time.Second
)

var dialClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func Run(ctx context.Context, connections []Connection, log *slog.Logger) {
	var workers sync.WaitGroup
	for _, link := range connections {
		workers.Go(func() {
			reconnect(ctx, link, log.With("connection", link.Name))
		})
	}
	workers.Wait()
}

func reconnect(ctx context.Context, link Connection, log *slog.Logger) {
	if link.timing == nil {
		link.timing = transport.NewTiming(0, 0)
	}
	delay := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		reason := session(ctx, link, log)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= 30*time.Second {
			delay = time.Second
		}
		log.Warn("gscore adapter disconnected", "reason", reason, "retry_in", link.timing.ReconnectDelay(delay))
		if !link.timing.WaitReconnect(ctx, time.Now(), delay) {
			return
		}
		delay = min(delay*2, 20*time.Second)
	}
}

func dial(ctx context.Context, address, token string, frameBytes int64) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{HTTPClient: dialClient, HTTPHeader: make(http.Header)}
	if token != "" {
		opts.HTTPHeader.Set("Authorization", "Bearer "+token)
	}
	conn, response, err := websocket.Dial(ctx, address, opts)
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("WebSocket handshake returned HTTP %d", response.StatusCode)
		}
		return nil, fmt.Errorf("WebSocket transport unavailable")
	}
	conn.SetReadLimit(frameBytes)
	return conn, nil
}

type packet struct {
	selfID string
	raw    []byte
	err    error
}

type pendingAction struct {
	reply    coreReply
	selfID   string
	deadline time.Time
}

func session(parent context.Context, link Connection, log *slog.Logger) string {
	frameBytes := cmp.Or(link.frameBytes, config.DefaultGScoreFrameBytes)
	core, err := dial(parent, link.CoreURL, "", frameBytes)
	if err != nil {
		return "gscore: " + err.Error()
	}
	defer core.CloseNow()
	targets := link.oneBotTargets()
	bots := make(map[string]*websocket.Conn, len(targets))
	for _, target := range targets {
		bot, err := dial(parent, target.URL, target.Token, frameBytes)
		if err != nil {
			return "OneBot: " + err.Error()
		}
		defer bot.CloseNow()
		bots[target.SelfID] = bot
	}
	log.Info("OneBot and gscore connected", "inputs", len(bots), "frame_bytes", frameBytes)
	ctx, cancel := context.WithCancel(parent)
	var readers sync.WaitGroup
	defer func() { cancel(); readers.Wait() }()
	frames := make(chan packet, max(4, len(bots)+1))
	startReader := func(selfID string, conn *websocket.Conn) {
		readers.Go(func() {
			for ctx.Err() == nil {
				_, data, err := conn.Read(ctx)
				select {
				case frames <- packet{selfID: selfID, raw: data, err: err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		})
	}
	startReader("", core)
	if link.timing != nil {
		readers.Go(func() {
			if err := link.timing.Heartbeat(ctx, func() error {
				ping, finish := context.WithTimeout(ctx, 5*time.Second)
				defer finish()
				return core.Ping(ping)
			}); err != nil && ctx.Err() == nil {
				log.Warn("gscore heartbeat failed")
				cancel()
			}
		})
	}
	for selfID, bot := range bots {
		startReader(selfID, bot)
	}
	var serial uint64
	pending := make(map[string]pendingAction)
	quotes := make(map[string]*quoteLookup)
	sendEvent := func(event coreEvent) error {
		if err := write(ctx, core, websocket.MessageBinary, event); err != nil {
			return err
		}
		log.Info("OneBot event delivered to gscore")
		return nil
	}
	requestQuote := func(quote *quoteLookup, action obAction) error {
		serial++
		action.Echo = strconv.FormatUint(serial, 10)
		if err := write(ctx, bots[quote.event.SelfID], websocket.MessageText, action); err != nil {
			return err
		}
		quotes[action.Echo] = quote
		return nil
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "stopped"
		case now := <-ticker.C:
			for echo, action := range pending {
				if !now.Before(action.deadline) {
					delete(pending, echo)
					log.Warn("OneBot reply API timed out")
					if err := receipt(ctx, core, action.reply, nil); err != nil {
						return "gscore receipt failed"
					}
				}
			}
			for echo, quote := range quotes {
				if !now.Before(quote.deadline) {
					delete(quotes, echo)
					log.Debug("OneBot quote lookup timed out")
					if err := sendEvent(quote.finish()); err != nil {
						return "gscore event send failed"
					}
				}
			}
		case frame := <-frames:
			if frame.err != nil {
				if frame.selfID == "" {
					if errors.Is(frame.err, websocket.ErrMessageTooBig) {
						return fmt.Sprintf("gscore WebSocket message exceeded the %d byte limit", frameBytes)
					}
					if websocket.CloseStatus(frame.err) == websocket.StatusMessageTooBig {
						return "gscore rejected an oversized WebSocket message"
					}
					return "gscore connection closed"
				}
				return "OneBot connection closed"
			}
			if frame.selfID == "" {
				var reply coreReply
				if err := json.Unmarshal(frame.raw, &reply); err != nil {
					log.Warn("invalid gscore frame")
					continue
				}
				if len(reply.Content) == 0 || strings.HasPrefix(reply.Content[0].Type, "log_") {
					continue
				}
				target := reply.SelfID
				if target == "" && len(targets) == 1 {
					target = targets[0].SelfID
				}
				bot := bots[target]
				if bot == nil {
					log.Warn("gscore reply has no matching Bot")
					if err := receipt(ctx, core, reply, nil); err != nil {
						return "gscore receipt failed"
					}
					continue
				}
				action, err := outgoing(reply, target)
				if err != nil || len(pending) >= maxPending {
					if err != nil {
						log.Warn("could not send gscore reply", "reason", err)
					} else {
						log.Warn("OneBot reply queue full")
					}
					if err := receipt(ctx, core, reply, nil); err != nil {
						return "gscore receipt failed"
					}
					continue
				}
				serial++
				action.Echo = strconv.FormatUint(serial, 10)
				body, err := json.Marshal(action)
				if err != nil || int64(len(body)) > frameBytes {
					if err != nil {
						log.Warn("could not encode gscore OneBot action", "action", action.Name)
					} else {
						log.Warn("gscore OneBot action exceeds frame limit", "action", action.Name,
							"bytes", len(body), "limit_bytes", frameBytes)
					}
					if err := receipt(ctx, core, reply, nil); err != nil {
						return "gscore receipt failed"
					}
					continue
				}
				if err := writeFrame(ctx, bot, websocket.MessageText, body); err != nil {
					return "OneBot action send failed"
				}
				reply.Content = nil
				pending[action.Echo] = pendingAction{reply: reply, selfID: target, deadline: time.Now().Add(actionTimeout)}
				continue
			}
			var message onebot.Frame
			if err := json.Unmarshal(frame.raw, &message); err != nil {
				log.Warn("invalid OneBot frame")
				continue
			}
			if message.RetCode != nil {
				echo := jsonString(message.Echo)
				if quote := quotes[echo]; quote != nil && quote.event.SelfID == frame.selfID {
					delete(quotes, echo)
					if time.Now().Before(quote.deadline) {
						if *message.RetCode == 0 {
							quote.accept(message.Data)
						} else {
							log.Debug("OneBot quote lookup failed", "retcode", *message.RetCode)
							quote.accept(nil)
						}
						if action, ok := quote.nextForward(); ok {
							if err := requestQuote(quote, action); err != nil {
								return "OneBot quote action send failed"
							}
							continue
						}
					}
					if err := sendEvent(quote.finish()); err != nil {
						return "gscore event send failed"
					}
					continue
				}
				action, ok := pending[echo]
				if !ok || action.selfID != frame.selfID {
					continue
				}
				delete(pending, echo)
				var sent any
				if *message.RetCode == 0 {
					var response struct {
						Data struct {
							MessageID json.RawMessage `json:"message_id"`
						} `json:"data"`
					}
					if json.Unmarshal(frame.raw, &response) == nil {
						if id := onebot.ID(response.Data.MessageID); id != "" {
							sent = id
						}
					}
					log.Info("gscore reply delivered by OneBot")
				} else {
					var response struct {
						Message string `json:"message"`
					}
					_ = json.Unmarshal(frame.raw, &response)
					log.Warn("OneBot rejected gscore reply", "retcode", *message.RetCode, "reason", response.Message)
				}
				if err := receipt(ctx, core, action.reply, sent); err != nil {
					return "gscore receipt failed"
				}
				continue
			}
			if event, ok := incoming(message, frame.raw, frame.selfID); ok {
				if id := quotedID(event.Content); id != "" && len(quotes) < maxQuotePending && len(frame.raw) <= maxQuoteBytes {
					quote := &quoteLookup{event: event, id: id, deadline: time.Now().Add(quoteTimeout), forwardIndex: -1}
					action := obAction{Name: "get_msg", Params: map[string]any{"message_id": json.Number(id)}}
					if err := requestQuote(quote, action); err != nil {
						return "OneBot quote action send failed"
					}
					continue
				}
				if err := sendEvent(event); err != nil {
					return "gscore event send failed"
				}
			}
		}
	}
}

func receipt(ctx context.Context, core *websocket.Conn, reply coreReply, id any) error {
	if reply.Echo == "" {
		return nil
	}
	event := coreEvent{
		BotID: platform, SelfID: reply.SelfID, UserType: "group", Sender: map[string]any{}, UserPM: 6,
		Content: []coreSegment{{"recall_message_id", map[string]any{"echo": reply.Echo, "id": id}}},
	}
	if reply.Type == "direct" {
		event.UserType, event.UserID = "direct", reply.Target
	} else {
		event.GroupID = &reply.Target
	}
	return write(ctx, core, websocket.MessageBinary, event)
}

func write(ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return writeFrame(ctx, conn, kind, body)
}

func writeFrame(ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(ctx, kind, body)
}
