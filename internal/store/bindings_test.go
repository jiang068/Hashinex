package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestEvidenceDedupConflictAndRevocation(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "identity.sqlite")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	key := Identity{"qq:42", "user", "OPENID"}
	record, err := db.Resolve(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ev := Evidence{Identity: key, RealID: 333, Sender: 333, OfficialMessage: "event-a", Observation: "obs-a", At: now}
	progress, err := db.AddEvidence(ctx, ev, now.Add(-time.Hour), 2, 1, true)
	if err != nil || progress.Count != 1 || progress.Bound {
		t.Fatalf("first evidence: %+v %v", progress, err)
	}
	ev.OfficialMessage = "duplicate observation"
	progress, err = db.AddEvidence(ctx, ev, now.Add(-time.Hour), 2, 1, true)
	if err != nil || progress.Accepted {
		t.Fatalf("accepted duplicate evidence: %+v %v", progress, err)
	}
	ev.RealID, ev.Sender, ev.Observation = 444, 444, "obs-b"
	progress, err = db.AddEvidence(ctx, ev, now.Add(-time.Hour), 2, 1, true)
	if err != nil || !progress.Frozen || progress.Bound {
		t.Fatalf("conflict was not frozen: %+v %v", progress, err)
	}
	if err := db.SetBinding(ctx, key, 333, "test"); err != nil {
		t.Fatal(err)
	}
	mapped, err := db.Resolve(ctx, key)
	if err != nil || mapped.ExternalID() != 333 || mapped.Virtual != record.Virtual {
		t.Fatalf("manual bind: %+v %v", mapped, err)
	}
	if err := db.SetBinding(ctx, key, 0, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Reverse(ctx, key.Namespace, key.Kind, 333); !errors.Is(err, ErrUnmapped) {
		t.Fatalf("revocation left old reverse mapping: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Resolve(ctx, key)
	if err != nil || reopened.Virtual != record.Virtual || reopened.Real != 0 {
		t.Fatalf("reopen changed persisted identity: %+v %v", reopened, err)
	}
}
