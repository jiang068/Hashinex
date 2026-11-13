package matching

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/store"
)

type Event struct {
	Namespace     string
	AppID         string
	UserNative    string
	GroupNative   string
	KnownGroup    int64
	UserBound     bool
	AllowedGroups []string
	UserID        int64
	GroupID       int64
	Key           string
	Text          string
	Time          time.Time
	Received      time.Time
	textHash      [sha256.Size]byte
	considered    bool
}

type pair struct {
	official Event
	observed Event
}

type Engine struct {
	cfg            config.Matching
	db             *store.Store
	log            *slog.Logger
	inbox          chan Event
	jobs           chan pair
	avatars        *AvatarVerifier
	dropped        atomic.Uint64
	paired         atomic.Uint64
	avatarRejected atomic.Uint64
	matched        atomic.Uint64
	bindings       atomic.Uint64
	conflicts      atomic.Uint64
}

func New(cfg config.Matching, db *store.Store, log *slog.Logger) *Engine {
	return &Engine{
		cfg: cfg, db: db, log: log, inbox: make(chan Event, 512), jobs: make(chan pair, 64),
		avatars: NewAvatarVerifier(cfg.DefaultHashes),
	}
}

func (e *Engine) AvatarVerifier() *AvatarVerifier {
	return e.avatars
}

func (e *Engine) Stats() map[string]uint64 {
	return map[string]uint64{
		"evidence_dropped": e.dropped.Load(), "match_evidence": e.matched.Load(),
		"match_pairs": e.paired.Load(), "avatar_rejected": e.avatarRejected.Load(),
		"auto_bindings": e.bindings.Load(), "match_conflicts": e.conflicts.Load(),
	}
}

func (e *Engine) Offer(event Event) {
	text := strings.TrimSpace(strings.ReplaceAll(event.Text, "\r\n", "\n"))
	if text == "" || event.Time.IsZero() ||
		event.Key == "" || absolute(event.Time.Sub(event.Received)) > 15*time.Second {
		return
	}
	event.textHash = sha256.Sum256([]byte(text))
	event.Text = ""
	select {
	case e.inbox <- event:
	default:
		e.dropped.Add(1)
	}
}

func (e *Engine) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case p := <-e.jobs:
					e.safeVerify(ctx, p)
				}
			}
		})
	}
	defer workers.Wait()
	tick := time.NewTicker(min(100*time.Millisecond, e.cfg.PairWindow/2))
	defer tick.Stop()
	buckets := make(map[[sha256.Size]byte][]Event)
	blocked := make(map[[sha256.Size]byte]time.Time)
	const perTextLimit = 48
	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-e.inbox:
			if until, ok := blocked[entry.textHash]; ok && entry.Received.Before(until) {
				e.dropped.Add(1)
				continue
			}
			queue := buckets[entry.textHash]
			if len(queue) >= perTextLimit {
				blocked[entry.textHash] = time.Now().Add(e.cfg.EventTTL + e.cfg.PairWindow)
				delete(buckets, entry.textHash)
				e.dropped.Add(uint64(len(queue) + 1))
				continue
			}
			if len(buckets)+len(blocked) >= 1024 && len(queue) == 0 {
				e.dropped.Add(1)
				continue
			}
			buckets[entry.textHash] = append(queue, entry)
		case now := <-tick.C:
			for text, until := range blocked {
				if !now.Before(until) {
					delete(blocked, text)
				}
			}
			for text, queue := range buckets {
				for i := range queue {
					official := &queue[i]
					age := now.Sub(official.Received)
					if official.Namespace == "" || official.considered || age < 2*e.cfg.PairWindow {
						continue
					}
					official.considered = true
					// Bound users in known groups still count toward pairing ambiguity.
					if age > e.cfg.EventTTL || official.UserBound {
						continue
					}
					if observed, ok := uniquePair(queue, *official, e.cfg.PairWindow); ok {
						select {
						case e.jobs <- pair{*official, observed}:
							e.paired.Add(1)
						default:
							e.dropped.Add(1)
						}
					}
				}
				kept := queue[:0]
				for _, entry := range queue {
					if now.Sub(entry.Received) <= e.cfg.EventTTL+e.cfg.PairWindow {
						kept = append(kept, entry)
					}
				}
				clear(queue[len(kept):])
				if len(kept) == 0 {
					delete(buckets, text)
				} else {
					buckets[text] = kept
				}
			}
		}
	}
}

func uniquePair(events []Event, official Event, window time.Duration) (Event, bool) {
	var found Event
	for _, other := range events {
		if other.Namespace != "" || !compatible(official, other, window) {
			continue
		}
		if found.Key != "" {
			return Event{}, false
		}
		found = other
	}
	if found.Key == "" {
		return Event{}, false
	}
	for _, other := range events {
		if other.Namespace == official.Namespace && other.Key != official.Key && compatible(other, found, window) {
			return Event{}, false
		}
	}
	return found, true
}

func compatible(official, observed Event, window time.Duration) bool {
	return (official.KnownGroup == 0 || official.KnownGroup == observed.GroupID) &&
		(len(official.AllowedGroups) == 0 || slices.Contains(official.AllowedGroups, strconv.FormatInt(observed.GroupID, 10))) &&
		absolute(official.Time.Sub(observed.Time)) <= window &&
		absolute(official.Received.Sub(observed.Received)) <= window
}

func absolute(t time.Duration) time.Duration {
	if t < 0 {
		return -t
	}
	return t
}

func (e *Engine) safeVerify(ctx context.Context, p pair) {
	defer func() {
		if recover() != nil {
			e.dropped.Add(1)
			e.log.Error("avatar worker panicked")
		}
	}()
	if time.Since(p.official.Received) > e.cfg.EventTTL+15*time.Second {
		e.dropped.Add(1)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	distance, ok := e.avatars.Verify(ctx, p.official.AppID, p.official.UserNative, p.observed.UserID, e.cfg.Distance)
	if !ok {
		e.avatarRejected.Add(1)
		return
	}
	since := time.Now().Add(-e.cfg.EvidenceTTL)
	group := store.Identity{Namespace: p.official.Namespace, Kind: "group", Native: p.official.GroupNative}
	record, err := e.db.Resolve(ctx, group)
	groupID := record.Real
	if err == nil && groupID == 0 && e.cfg.Mode == "shadow" {
		groupID, err = e.db.MatchingGroup(ctx, group, since, e.cfg.GroupSuccesses, e.cfg.GroupSenders)
	}
	if err != nil {
		e.log.Warn("matching group lookup failed", "namespace", group.Namespace, "reason", err)
		return
	}
	user := store.Identity{Namespace: p.official.Namespace, Kind: "user", Native: p.official.UserNative}
	if _, err := e.db.Resolve(ctx, user); err != nil {
		e.log.Warn("register observed identity failed", "reason", err)
		return
	}
	if err := e.db.RecordGroupSource(ctx, user, group.Native); err != nil {
		e.log.Warn("record observed group failed", "reason", err)
		return
	}
	evidence := store.Evidence{
		Identity: group, RealID: p.observed.GroupID, Sender: p.observed.UserID, Distance: distance,
		OfficialMessage: digest(p.official.Key), Observation: digest(p.observed.Key), At: p.official.Received,
	}
	threshold, minSenders := e.cfg.GroupSuccesses, e.cfg.GroupSenders
	if groupID == p.observed.GroupID {
		evidence.Identity = user
		evidence.RealID = p.observed.UserID
		threshold, minSenders = e.cfg.Successes, 1
	}
	result, err := e.db.AddEvidence(ctx, evidence, since, threshold, minSenders, e.cfg.Mode == "auto")
	if err != nil {
		e.log.Warn("save matching evidence failed", "namespace", group.Namespace, "reason", err)
		return
	}
	if !result.Accepted {
		return
	}
	e.matched.Add(1)
	if result.Frozen {
		e.conflicts.Add(1)
	}
	if result.Bound {
		e.bindings.Add(1)
	}
	e.log.Info("identity match progress", "namespace", evidence.Namespace, "kind", evidence.Kind,
		"candidate", digest(evidence.Native + ":" + strconv.FormatInt(evidence.RealID, 10))[:12],
		"count", result.Count, "threshold", threshold, "distance", distance, "frozen", result.Frozen, "bound", result.Bound)
}

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
