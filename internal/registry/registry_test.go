package registry

import (
	"testing"
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
