package db_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/registry"
)

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbus.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := db.Migrate(d); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatalf("second Migrate must be a no-op, got: %v", err)
	}
}

func TestRegistryRoundTripsThroughSharedLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbus.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := registry.New()
	r.AttachDB(d, 7373)
	name, err := r.RegisterType("proj-a", "claude", "%1")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	// A second registry sharing the same DB resolves the agent it never saw
	// in memory — proving the shared store is the cross-broker source of truth.
	other := registry.New()
	other.AttachDB(d, 9999)
	inst, ok := other.LookupShared("proj-a", name)
	if !ok {
		t.Fatalf("LookupShared(%q, %q) not found via shared store", "proj-a", name)
	}
	if inst.Project != "proj-a" || inst.Name != name || inst.PaneID != "%1" {
		t.Fatalf("round-trip mismatch: got %+v", inst)
	}

	// Unregister write-through removes the shared row.
	r.Unregister("proj-a", name)
	if _, ok := other.LookupShared("proj-a", name); ok {
		t.Fatalf("LookupShared should miss after Unregister")
	}
}

func TestRecentMessagesReturnsDurableHistoryNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbus.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	older := message.Message{ID: "m1", Kind: message.KindRequest, From: "codex-1", To: "claude-1", Body: "first", CreatedAt: time.Unix(10, 0).UTC()}
	newer := message.Message{ID: "m2", Kind: message.KindReply, From: "claude-1", To: "codex-1", Body: "second", ReplyTo: "m1", CreatedAt: time.Unix(20, 0).UTC()}
	if err := db.RecordMessage(d, older); err != nil {
		t.Fatalf("RecordMessage older: %v", err)
	}
	if err := db.RecordMessage(d, newer); err != nil {
		t.Fatalf("RecordMessage newer: %v", err)
	}

	history, err := db.RecentMessages(d, 20)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("RecentMessages length = %d, want 2", len(history))
	}
	if history[0].ID != "m2" || history[1].ID != "m1" {
		t.Fatalf("RecentMessages order = %+v, want newest first", history)
	}
}
