package registry

import (
	"path/filepath"
	"testing"

	"github.com/tk-425/agentbus/internal/db"
)

// TestResolveByPane: a registered pane resolves back to its Agent instance, and
// each project's instance resolves independently even when names collide.
func TestResolveByPane(t *testing.T) {
	r := New()
	r.RegisterType("projA", "claude", "%1")
	r.RegisterType("projB", "claude", "%2")

	a, ok := r.ResolveByPane("%1")
	if !ok || a.Project != "projA" || a.Name != "claude-1" {
		t.Errorf("resolve %%1: got %+v ok=%v, want projA/claude-1", a, ok)
	}
	b, ok := r.ResolveByPane("%2")
	if !ok || b.Project != "projB" || b.Name != "claude-1" {
		t.Errorf("resolve %%2: got %+v ok=%v, want projB/claude-1", b, ok)
	}
	if _, ok := r.ResolveByPane("%nope"); ok {
		t.Errorf("unknown pane should not resolve")
	}
}

// TestFreedNameNotReused: after an Agent instance is unregistered, its name is
// not reissued within the session (the suffix counter does not roll back).
func TestFreedNameNotReused(t *testing.T) {
	r := New()

	first, _ := r.RegisterType("proj", "claude", "%1")
	if first != "claude-1" {
		t.Fatalf("first: got %q, want claude-1", first)
	}

	r.Unregister("proj", "claude-1")
	if _, ok := r.Lookup("claude-1"); ok {
		t.Errorf("claude-1 should be gone after unregister")
	}

	next, _ := r.RegisterType("proj", "claude", "%2")
	if next != "claude-2" {
		t.Errorf("freed name reused: got %q, want claude-2", next)
	}
}

// TestPerProjectUniqueness: the same bare type registered in two projects each
// yields claude-1, and both Agent instances coexist (names are unique per
// project, not globally).
func TestPerProjectUniqueness(t *testing.T) {
	r := New()

	a, err := r.RegisterType("projA", "claude", "%1")
	if err != nil {
		t.Fatalf("register in projA: %v", err)
	}
	b, err := r.RegisterType("projB", "claude", "%2")
	if err != nil {
		t.Fatalf("register in projB: %v", err)
	}
	if a != "claude-1" || b != "claude-1" {
		t.Fatalf("both should be claude-1: got %q and %q", a, b)
	}

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("both instances should coexist: got %d, want 2 (%+v)", len(all), all)
	}
	projects := map[string]bool{}
	for _, inst := range all {
		projects[inst.Project] = true
	}
	if !projects["projA"] || !projects["projB"] {
		t.Errorf("instances missing a project: %+v", all)
	}
}

// TestAutoSuffix: registering a bare agent type assigns the first free Agent
// instance name within the project (claude -> claude-1, then claude-2).
func TestAutoSuffix(t *testing.T) {
	r := New()

	first, err := r.RegisterType("proj", "claude", "%1")
	if err != nil {
		t.Fatalf("register claude: %v", err)
	}
	if first != "claude-1" {
		t.Errorf("first instance name: got %q, want %q", first, "claude-1")
	}

	second, err := r.RegisterType("proj", "claude", "%2")
	if err != nil {
		t.Fatalf("register claude again: %v", err)
	}
	if second != "claude-2" {
		t.Errorf("second instance name: got %q, want %q", second, "claude-2")
	}
}

func TestListSharedShowsCrossProjectAgentInstances(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "agentbus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := New()
	r.AttachDB(d, 7373)
	if _, err := r.RegisterType("proj-a", "claude", "%1"); err != nil {
		t.Fatalf("RegisterType proj-a: %v", err)
	}
	if _, err := r.RegisterType("proj-b", "codex", "%2"); err != nil {
		t.Fatalf("RegisterType proj-b: %v", err)
	}

	other := New()
	other.AttachDB(d, 0)
	all, err := other.ListShared()
	if err != nil {
		t.Fatalf("ListShared: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListShared length = %d, want 2", len(all))
	}
	if all[0].Project != "proj-a" || all[0].Name != "claude-1" {
		t.Fatalf("first shared agent = %+v, want proj-a/claude-1", all[0])
	}
	if all[1].Project != "proj-b" || all[1].Name != "codex-1" {
		t.Fatalf("second shared agent = %+v, want proj-b/codex-1", all[1])
	}
}

func TestSharedRegistryRoundTripsBackend(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "agentbus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := New()
	r.AttachDB(d, 7373)
	name, err := r.RegisterType("proj", "claude", "%1", "tmux")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}
	inst, ok := r.LookupShared("proj", name)
	if !ok || inst.Backend != "tmux" {
		t.Fatalf("LookupShared = %+v ok=%v, want backend tmux", inst, ok)
	}
}

// TestLookupSharedByPaneResolvesFromDB: a fresh registry with empty in-memory
// maps (as every out-of-process CLI invocation has) still resolves a pane to
// its Agent instance name through the shared DB — the identity path `whoami`
// depends on. The in-memory-only ResolveByPane cannot, confirming the DB path
// is necessary.
func TestLookupSharedByPaneResolvesFromDB(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "agentbus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	writer := New()
	writer.AttachDB(d, 7373)
	if _, err := writer.RegisterType("proj-a", "claude", "%pane7"); err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	fresh := New()
	fresh.AttachDB(d, 0)
	if _, ok := fresh.ResolveByPane("%pane7"); ok {
		t.Fatalf("ResolveByPane should miss on a fresh in-memory registry")
	}
	inst, ok := fresh.LookupSharedByPane("%pane7")
	if !ok || inst.Name != "claude-1" || inst.Project != "proj-a" {
		t.Fatalf("LookupSharedByPane = %+v ok=%v, want proj-a/claude-1", inst, ok)
	}
	if _, ok := fresh.LookupSharedByPane("%nope"); ok {
		t.Fatalf("unknown pane should not resolve")
	}
	if _, ok := fresh.LookupSharedByPane(""); ok {
		t.Fatalf("empty pane should not resolve")
	}
}

func TestResolveUnregisterTargetRemovesLocalAndQualifiedTargetsExactly(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "agentbus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := New()
	r.AttachDB(d, 7373)
	local, err := r.RegisterType("proj-a", "claude", "%1")
	if err != nil {
		t.Fatalf("RegisterType local: %v", err)
	}
	remote, err := r.RegisterType("proj-b", "claude", "%2")
	if err != nil {
		t.Fatalf("RegisterType remote: %v", err)
	}

	inst, err := r.ResolveUnregisterTarget("proj-a", local)
	if err != nil {
		t.Fatalf("ResolveUnregisterTarget local: %v", err)
	}
	if inst.Project != "proj-a" || inst.Name != local {
		t.Fatalf("local target mismatch: %+v", inst)
	}
	r.Unregister(inst.Project, inst.Name)
	if _, ok := r.LookupShared("proj-a", local); ok {
		t.Fatalf("local target should be removed")
	}
	if _, ok := r.LookupShared("proj-b", remote); !ok {
		t.Fatalf("qualified-unrelated target should remain present")
	}

	inst, err = r.ResolveUnregisterTarget("proj-a", remote+"@proj-b")
	if err != nil {
		t.Fatalf("ResolveUnregisterTarget qualified: %v", err)
	}
	if inst.Project != "proj-b" || inst.Name != remote {
		t.Fatalf("qualified target mismatch: %+v", inst)
	}
	r.Unregister(inst.Project, inst.Name)
	if _, ok := r.LookupShared("proj-b", remote); ok {
		t.Fatalf("qualified target should be removed")
	}
}

func TestResolveUnregisterTargetUnknownReturnsErrUnknownAgent(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "agentbus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := New()
	r.AttachDB(d, 0)
	if _, err := r.ResolveUnregisterTarget("proj-a", "ghost-1"); err == nil {
		t.Fatalf("ResolveUnregisterTarget should fail for unknown local target")
	}
	if _, err := r.ResolveUnregisterTarget("proj-a", "ghost-1@proj-b"); err == nil {
		t.Fatalf("ResolveUnregisterTarget should fail for unknown qualified target")
	}
}
