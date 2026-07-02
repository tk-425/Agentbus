package db_test

import (
	"path/filepath"
	"testing"

	"github.com/tk-425/agentbus/internal/db"
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
