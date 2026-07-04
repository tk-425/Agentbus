package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tk-425/agentbus/internal/agenttypes"
	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
	"github.com/tk-425/agentbus/internal/registry"
	versionpkg "github.com/tk-425/agentbus/internal/version"
)

func TestDiscoverCandidatesFiltersByProjectAndMatchesExactCommandBasename(t *testing.T) {
	defs := map[string]agenttypes.Definition{
		"claude": {ResponseWait: 2},
		"codex":  {ResponseWait: 2},
	}
	panes := []multiplexer.Pane{
		{ID: "%1", CWD: "/repo", Command: "claude"},
		{ID: "%2", CWD: "/repo/subdir", Command: "/usr/local/bin/codex"},
		{ID: "%3", CWD: "/other", Command: "claude"},
		{ID: "%4", CWD: "/repo", Command: "claude-helper"},
	}

	got := discoverCandidates("/repo", panes, defs, map[string]bool{})
	if len(got) != 2 {
		t.Fatalf("discoverCandidates length = %d, want 2", len(got))
	}
	if got[0].PaneID != "%1" || got[0].AgentType != "claude" {
		t.Fatalf("first candidate = %+v, want %%1/claude", got[0])
	}
	if got[1].PaneID != "%2" || got[1].AgentType != "codex" {
		t.Fatalf("second candidate = %+v, want %%2/codex", got[1])
	}
}

// TestDiscoverCandidatesResolvesAgentFromProcessTreeWhenCommandIsRetitled
// reproduces the tmux discovery bug: claude retitles its process to its version
// ("2.1.193"), so the pane's surface command matches no agent type, yet the real
// `claude` process is a direct child of the pane shell. Discovery must fall back
// to the process subtree and register claude, ignoring the npm/MCP grandchildren.
func TestDiscoverCandidatesResolvesAgentFromProcessTreeWhenCommandIsRetitled(t *testing.T) {
	restore := listProcesses
	defer func() { listProcesses = restore }()
	// Captured from a live tmux session: shell 90352 -> claude 92080 -> npm 92149.
	listProcesses = func() ([]procEntry, error) {
		return []procEntry{
			{pid: 90352, ppid: 88065, comm: "zsh"},
			{pid: 92080, ppid: 90352, comm: "claude"},
			{pid: 92149, ppid: 92080, comm: "npm exec tavily-"},
		}, nil
	}

	defs := map[string]agenttypes.Definition{"claude": {ResponseWait: 2}}
	panes := []multiplexer.Pane{{ID: "%1", CWD: "/repo", Command: "2.1.193", PID: 90352}}

	got := discoverCandidates("/repo", panes, defs, map[string]bool{})
	if len(got) != 1 {
		t.Fatalf("discoverCandidates length = %d, want 1", len(got))
	}
	if got[0].PaneID != "%1" || got[0].AgentType != "claude" {
		t.Fatalf("candidate = %+v, want %%1/claude", got[0])
	}
}

func TestDiscoverCandidatesSkipsAlreadyBoundPanesOnRerun(t *testing.T) {
	defs := map[string]agenttypes.Definition{
		"claude": {ResponseWait: 2},
	}
	panes := []multiplexer.Pane{{ID: "%1", CWD: "/repo", Command: "claude"}}
	bound := map[string]bool{}

	first := discoverCandidates("/repo", panes, defs, bound)
	if len(first) != 1 {
		t.Fatalf("first discoverCandidates length = %d, want 1", len(first))
	}
	bound[first[0].PaneID] = true

	second := discoverCandidates("/repo", panes, defs, bound)
	if len(second) != 0 {
		t.Fatalf("second discoverCandidates length = %d, want 0", len(second))
	}
}

func TestDiscoverUsesMockPanesAndRegistersEligibleOnesOnce(t *testing.T) {
	defs := map[string]agenttypes.Definition{
		"claude": {ResponseWait: 2},
		"codex":  {ResponseWait: 2},
	}
	mux := multiplexer.NewMock()
	mux.SetPanes([]multiplexer.Pane{
		{ID: "%1", CWD: filepath.Clean("/repo"), Command: "claude"},
		{ID: "%2", CWD: filepath.Clean("/repo/child"), Command: "codex"},
		{ID: "%3", CWD: filepath.Clean("/elsewhere"), Command: "claude"},
	})

	var registered []string
	bound := map[string]bool{}
	register := func(agentType, paneID string) (string, error) {
		registered = append(registered, agentType+":"+paneID)
		bound[paneID] = true
		return agentType + "-1", nil
	}

	created, err := discoverWith("/repo", mux, defs, bound, register)
	if err != nil {
		t.Fatalf("discoverWith first: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("discoverWith first length = %d, want 2", len(created))
	}
	if len(registered) != 2 {
		t.Fatalf("registered length = %d, want 2", len(registered))
	}

	again, err := discoverWith("/repo", mux, defs, bound, register)
	if err != nil {
		t.Fatalf("discoverWith second: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("discoverWith second length = %d, want 0", len(again))
	}
	if len(registered) != 2 {
		t.Fatalf("registered length after rerun = %d, want 2", len(registered))
	}
}

func TestAutoDiscoverOnceRegistersPreexistingEligiblePanes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	b := broker.New()
	b.Registry.SetLocalProject("repo")
	mux := multiplexer.NewMock()
	mux.SetPanes([]multiplexer.Pane{
		{ID: "%1", CWD: filepath.Clean(projectRoot), Command: "claude"},
		{ID: "%2", CWD: filepath.Clean(filepath.Join(projectRoot, "child")), Command: "codex"},
		{ID: "%3", CWD: filepath.Clean("/elsewhere"), Command: "claude"},
	})

	if err := autoDiscoverOnce(projectRoot, b, mux); err != nil {
		t.Fatalf("autoDiscoverOnce: %v", err)
	}

	got := b.Registry.All()
	if len(got) != 2 {
		t.Fatalf("registered agents = %d, want 2 (%+v)", len(got), got)
	}
	if _, ok := b.Registry.ResolveByPane("%1"); !ok {
		t.Fatalf("claude pane %%1 should be registered")
	}
	if _, ok := b.Registry.ResolveByPane("%2"); !ok {
		t.Fatalf("codex pane %%2 should be registered")
	}
	if _, ok := b.Registry.ResolveByPane("%3"); ok {
		t.Fatalf("out-of-project pane %%3 must not be registered")
	}
}

func TestAutoDiscoverOnceAddsLatePanesWithoutDuplicatingBoundOnes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	b := broker.New()
	b.Registry.SetLocalProject("repo")
	mux := multiplexer.NewMock()
	mux.SetPanes(nil)

	if err := autoDiscoverOnce(projectRoot, b, mux); err != nil {
		t.Fatalf("autoDiscoverOnce first: %v", err)
	}
	if got := len(b.Registry.All()); got != 0 {
		t.Fatalf("first pass registered %d agents, want 0", got)
	}

	mux.SetPanes([]multiplexer.Pane{{ID: "%1", CWD: filepath.Clean(projectRoot), Command: "claude"}})
	if err := autoDiscoverOnce(projectRoot, b, mux); err != nil {
		t.Fatalf("autoDiscoverOnce second: %v", err)
	}
	if got := len(b.Registry.All()); got != 1 {
		t.Fatalf("second pass registered %d agents, want 1", got)
	}

	mux.SetPanes([]multiplexer.Pane{
		{ID: "%1", CWD: filepath.Clean(projectRoot), Command: "claude"},
		{ID: "%2", CWD: filepath.Clean(projectRoot), Command: "codex"},
	})
	if err := autoDiscoverOnce(projectRoot, b, mux); err != nil {
		t.Fatalf("autoDiscoverOnce third: %v", err)
	}
	got := b.Registry.All()
	if len(got) != 2 {
		t.Fatalf("third pass registered %d agents, want 2 (%+v)", len(got), got)
	}
	if _, ok := b.Registry.ResolveByPane("%1"); !ok {
		t.Fatalf("pane %%1 should remain registered")
	}
	if _, ok := b.Registry.ResolveByPane("%2"); !ok {
		t.Fatalf("late pane %%2 should be newly registered")
	}
}

func TestAutoDiscoverOnceUnregistersPaneThatIsNoLongerDiscoverable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	b := broker.New()
	b.Registry.SetLocalProject("repo")
	mux := multiplexer.NewMock()
	mux.SetPanes([]multiplexer.Pane{{ID: "%1", CWD: filepath.Clean(projectRoot), Command: "claude"}})

	if err := autoDiscoverOnce(projectRoot, b, mux); err != nil {
		t.Fatalf("autoDiscoverOnce seed: %v", err)
	}
	if got := len(b.Registry.All()); got != 1 {
		t.Fatalf("seed pass registered %d agents, want 1", got)
	}

	mux.SetPanes([]multiplexer.Pane{{ID: "%1", CWD: filepath.Clean(projectRoot), Command: "zsh"}})
	if err := autoDiscoverOnce(projectRoot, b, mux); err != nil {
		t.Fatalf("autoDiscoverOnce reconcile: %v", err)
	}
	if got := len(b.Registry.All()); got != 0 {
		t.Fatalf("reconcile pass registered %d agents, want 0", got)
	}
}

func TestAutoDiscoverOnceReplacesPaneWhenAgentTypeChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	b := broker.New()
	b.Registry.SetLocalProject("repo")
	mux := multiplexer.NewMock()
	mux.SetPanes([]multiplexer.Pane{{ID: "%1", CWD: filepath.Clean(projectRoot), Command: "claude"}})

	if err := autoDiscoverOnce(projectRoot, b, mux); err != nil {
		t.Fatalf("autoDiscoverOnce seed: %v", err)
	}
	inst, ok := b.Registry.ResolveByPane("%1")
	if !ok || inst.Name != "claude-1" {
		t.Fatalf("seed registration = %+v, want claude-1 on %%1", inst)
	}

	mux.SetPanes([]multiplexer.Pane{{ID: "%1", CWD: filepath.Clean(projectRoot), Command: "codex"}})
	if err := autoDiscoverOnce(projectRoot, b, mux); err != nil {
		t.Fatalf("autoDiscoverOnce replace: %v", err)
	}
	inst, ok = b.Registry.ResolveByPane("%1")
	if !ok || inst.Name != "codex-1" {
		t.Fatalf("replacement registration = %+v, want codex-1 on %%1", inst)
	}
}

func TestRunVersionPrintsSharedVersionString(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := runVersion(cmd, nil); err != nil {
		t.Fatalf("runVersion: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != versionpkg.String {
		t.Fatalf("version output = %q, want %q", got, versionpkg.String)
	}
}

func TestRunStatusPrintsSingleLineSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(projectRoot)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd after Chdir: %v", err)
	}

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO brokers (project_root, port, multiplexer, pid) VALUES (?, ?, ?, ?)`, cwd, 7373, "tmux", 123); err != nil {
		t.Fatalf("insert broker: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO agents (project, name, broker_port, pane_id, registered_at) VALUES (?, ?, ?, ?, ?)`, "repo", "claude-1", 7373, "%1", "now"); err != nil {
		t.Fatalf("insert first agent: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO agents (project, name, broker_port, pane_id, registered_at) VALUES (?, ?, ?, ?, ?)`, "repo", "codex-1", 7373, "%2", "now"); err != nil {
		t.Fatalf("insert second agent: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO messages (id, kind, from_agent, to_agent, body, reply_to, created_at, read) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "m1", "request", "codex-1", "claude-1", "hi", "", "2026-07-01T00:00:00Z", 0); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runStatus(cmd, nil); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	got := strings.TrimSpace(out.String())
	want := "project=repo broker=up agents=2 history=1 version=" + versionpkg.String
	if got != want {
		t.Fatalf("status output = %q, want %q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("status output must be a single line, got %q", got)
	}
}

// TestRunningBrokerDetectsLiveStaleAndMissingRows: a brokers row with a live
// pid blocks a second start, a stale row (crashed broker) does not, and a
// project without a row does not.
func TestRunningBrokerDetectsLiveStaleAndMissingRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO brokers (project_root, port, multiplexer, pid) VALUES (?, ?, ?, ?)`, "/proj", 7373, "tmux", os.Getpid()); err != nil {
		t.Fatalf("insert broker: %v", err)
	}

	pid, port, live := runningBroker(d, "/proj")
	if !live || pid != os.Getpid() || port != 7373 {
		t.Fatalf("live row: got pid=%d port=%d live=%v, want pid=%d port=7373 live=true", pid, port, live, os.Getpid())
	}

	oldAlive := pidAlive
	defer func() { pidAlive = oldAlive }()
	pidAlive = func(int) bool { return false }
	if _, _, live := runningBroker(d, "/proj"); live {
		t.Fatal("stale row (dead pid) must not report a live broker")
	}
	pidAlive = oldAlive

	if _, _, live := runningBroker(d, "/other"); live {
		t.Fatal("missing row must not report a live broker")
	}
}

// TestRunStartIsNoOpWhenBrokerAlreadyRunning: a second `agentbus start` in the
// same project must report the live broker instead of launching another one.
func TestRunStartIsNoOpWhenBrokerAlreadyRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(projectRoot)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd after Chdir: %v", err)
	}

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO brokers (project_root, port, multiplexer, pid) VALUES (?, ?, ?, ?)`, cwd, 7373, "tmux", os.Getpid()); err != nil {
		t.Fatalf("insert broker: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runStart(&cobra.Command{}, nil); err != nil {
			t.Errorf("runStart: %v", err)
		}
	})
	want := fmt.Sprintf("broker already running (pid %d, port 7373)", os.Getpid())
	if strings.TrimSpace(out) != want {
		t.Fatalf("start output = %q, want %q", strings.TrimSpace(out), want)
	}
}

// TestRunStartAbortsWithoutMultiplexer asserts that foreground start fails with
// the no-multiplexer error and launches no broker when the current environment is
// neither tmux nor herdr — detection happens before any broker subprocess spawns,
// so no port file is written.
func TestRunStartAbortsWithoutMultiplexer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Neither multiplexer signal present: detection must fail.
	t.Setenv("TMUX", "")
	t.Setenv("HERDR_ENV", "")

	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(projectRoot)

	err := runStart(&cobra.Command{}, nil)
	if !errors.Is(err, multiplexer.ErrNoMultiplexer) {
		t.Fatalf("runStart error = %v, want ErrNoMultiplexer", err)
	}
	if _, statErr := os.Stat(broker.DefaultPortFile()); !os.IsNotExist(statErr) {
		t.Fatalf("port file exists after aborted start: stat err = %v, want not-exist", statErr)
	}
}

// TestResolvePaneIDReadsHerdrPaneID guards the regression where resolvePaneID
// read the nonexistent HERDR_PANE instead of the HERDR_PANE_ID herdr actually
// exports, leaving whoami/register unable to identify a pane under herdr.
func TestResolvePaneIDReadsHerdrPaneID(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	t.Setenv("HERDR_PANE", "")
	t.Setenv("HERDR_PANE_ID", "w6:p1")
	if got := resolvePaneID(""); got != "w6:p1" {
		t.Fatalf("resolvePaneID() = %q, want w6:p1 (from HERDR_PANE_ID)", got)
	}

	// Explicit --pane always wins over the environment.
	if got := resolvePaneID("explicit:pane"); got != "explicit:pane" {
		t.Fatalf("resolvePaneID(explicit) = %q, want explicit:pane", got)
	}

	// TMUX_PANE takes precedence when set (tmux backend).
	t.Setenv("TMUX_PANE", "%7")
	if got := resolvePaneID(""); got != "%7" {
		t.Fatalf("resolvePaneID() = %q, want %%7 (from TMUX_PANE)", got)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close reader: %v", err)
	}
	return string(raw)
}

func TestRunListPrintsRegisteredAgentsAcrossProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := registry.New()
	r.AttachDB(d, 7373)
	if _, err := r.RegisterType("proj-a", "claude", "%1"); err != nil {
		t.Fatalf("RegisterType proj-a: %v", err)
	}
	if _, err := r.RegisterType("proj-b", "codex", "%2"); err != nil {
		t.Fatalf("RegisterType proj-b: %v", err)
	}

	listAll = true
	defer func() { listAll = false }()

	out := captureStdout(t, func() {
		if err := runList(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runList: %v", err)
		}
	})
	if got := strings.TrimSpace(out); got != "claude-1@proj-a\ncodex-1@proj-b" {
		t.Fatalf("list output = %q", got)
	}
}

func TestRunListScopesToCurrentProjectByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "proj-a")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(projectRoot)

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := registry.New()
	r.AttachDB(d, 7373)
	if _, err := r.RegisterType("proj-a", "claude", "%1"); err != nil {
		t.Fatalf("RegisterType proj-a: %v", err)
	}
	if _, err := r.RegisterType("proj-b", "codex", "%2"); err != nil {
		t.Fatalf("RegisterType proj-b: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runList(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runList: %v", err)
		}
	})
	if got := strings.TrimSpace(out); got != "claude-1@proj-a" {
		t.Fatalf("scoped list output = %q, want only current project", got)
	}
}

// TestRunListPrintsNothingWhenNoInstanceInCurrentProject pins the spec success
// criterion that a scoped list with no matching instance prints nothing and exits
// zero — the registry holds only an instance under another project.
func TestRunListPrintsNothingWhenNoInstanceInCurrentProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "proj-empty")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(projectRoot)

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := registry.New()
	r.AttachDB(d, 7373)
	if _, err := r.RegisterType("proj-a", "claude", "%1"); err != nil {
		t.Fatalf("RegisterType proj-a: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runList(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runList: %v", err)
		}
	})
	if got := strings.TrimSpace(out); got != "" {
		t.Fatalf("scoped list output = %q, want empty", got)
	}
}

func TestRunUnregisterRemovesExactQualifiedTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "proj-a")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(projectRoot)

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	r := registry.New()
	r.AttachDB(d, 7373)
	if _, err := r.RegisterType("proj-a", "claude", "%1"); err != nil {
		t.Fatalf("RegisterType local: %v", err)
	}
	remote, err := r.RegisterType("proj-b", "claude", "%2")
	if err != nil {
		t.Fatalf("RegisterType remote: %v", err)
	}

	unregisterName = remote + "@proj-b"
	out := captureStdout(t, func() {
		if err := runUnregister(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runUnregister: %v", err)
		}
	})
	if got := strings.TrimSpace(out); got != "removed "+remote+"@proj-b" {
		t.Fatalf("unregister output = %q", got)
	}
	if _, ok := r.LookupShared("proj-b", remote); ok {
		t.Fatalf("qualified target should be removed")
	}
	if _, ok := r.LookupShared("proj-a", "claude-1"); !ok {
		t.Fatalf("local target should remain present")
	}
}

func TestRunLogPrintsRecentDurableHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.RecordMessage(d, message.Message{ID: "m1", Kind: message.KindRequest, From: "codex-1", To: "claude-1", Body: "hello", CreatedAt: time.Unix(10, 0).UTC()}); err != nil {
		t.Fatalf("RecordMessage: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runLog(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runLog: %v", err)
		}
	})
	if got := strings.TrimSpace(out); got != "[request] codex-1 -> claude-1: hello" {
		t.Fatalf("log output = %q", got)
	}
}

func TestRunAddAgentStoresValidatedCustomType(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	addAgentName = "Gemini"
	addAgentPromptPattern = ""
	addAgentResponseWait = 5
	out := captureStdout(t, func() {
		if err := runAddAgent(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runAddAgent: %v", err)
		}
	})
	if got := strings.TrimSpace(out); got != "added gemini" {
		t.Fatalf("add-agent output = %q", got)
	}

	defs, err := agenttypes.New(agentTypesPath()).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def, ok := defs["gemini"]
	if !ok {
		t.Fatalf("gemini should be stored")
	}
	if def.ResponseWait != 5 {
		t.Fatalf("gemini response wait = %d, want 5", def.ResponseWait)
	}
}

// TestRunReplyResolvesBrokerAndSubmits: the reply subcommand resolves the current
// pane's broker port from the shared registry, dials that broker, and submits the
// reply — landing a terminal Reply in the original requester's inbox. It supplies
// neither --from nor --to; the broker fills both from the recorded correlation.
func TestRunReplyResolvesBrokerAndSubmits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(projectRoot)

	// A live broker holding the request→requester correlation the reply resolves.
	b := broker.New()
	b.Registry.SetLocalProject("repo")
	requester, err := b.Registry.RegisterType("repo", "codex", "%2")
	if err != nil {
		t.Fatalf("RegisterType requester: %v", err)
	}
	if err := b.Send(message.Message{ID: "req-1", Kind: message.KindRequest, From: requester, To: "claude-1", Body: "ping"}); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}

	// The shared registry the command reads: the recipient pane %1 registered with
	// the live broker's port, so LookupSharedByPane resolves it to that broker.
	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	r := registry.New()
	r.AttachDB(d, port)
	if _, err := r.RegisterType("repo", "claude", "%1"); err != nil {
		t.Fatalf("RegisterType recipient: %v", err)
	}

	// The command runs from the recipient's pane.
	t.Setenv("TMUX_PANE", "%1")
	if err := runReply(&cobra.Command{}, []string{"req-1", "pong", "from", "claude"}); err != nil {
		t.Fatalf("runReply: %v", err)
	}

	got := b.Inbox(requester)
	if len(got) != 1 {
		t.Fatalf("requester inbox: got %d, want 1 terminal Reply", len(got))
	}
	if got[0].Kind != message.KindReply || got[0].From != "claude-1" || got[0].ReplyTo != "req-1" {
		t.Fatalf("unexpected reply: %+v", got[0])
	}
	if got[0].Body != "pong from claude" {
		t.Fatalf("reply body = %q, want %q", got[0].Body, "pong from claude")
	}
}

// TestRunReplyErrorsWhenPaneUnregistered: a reply from a pane with no registered
// Agent instance fails loudly rather than silently dropping.
func TestRunReplyErrorsWhenPaneUnregistered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMUX_PANE", "%9")

	err := runReply(&cobra.Command{}, []string{"req-1", "pong"})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("runReply from unregistered pane: got %v, want a not-registered error", err)
	}
}

// TestRunInboxUsesCurrentProjectBroker guards the multi-broker regression where
// inbox dialed the global port file and silently read another project's broker.
func TestRunInboxUsesCurrentProjectBroker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := filepath.Join(home, "repo")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Chdir(projectRoot)

	right := broker.New()
	if err := right.Send(message.Message{
		ID:        "reply-1",
		Kind:      message.KindReply,
		From:      "opencode-2",
		To:        "pi-2",
		Body:      "from the right broker",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed right broker: %v", err)
	}
	rightSrv := httptest.NewServer(right.Handler())
	defer rightSrv.Close()
	rightURL, err := url.Parse(rightSrv.URL)
	if err != nil {
		t.Fatalf("parse right server URL: %v", err)
	}
	rightPort, err := strconv.Atoi(rightURL.Port())
	if err != nil {
		t.Fatalf("parse right server port: %v", err)
	}

	wrong := broker.New()
	wrongSrv := httptest.NewServer(wrong.Handler())
	defer wrongSrv.Close()
	wrongURL, err := url.Parse(wrongSrv.URL)
	if err != nil {
		t.Fatalf("parse wrong server URL: %v", err)
	}
	wrongPort, err := strconv.Atoi(wrongURL.Port())
	if err != nil {
		t.Fatalf("parse wrong server port: %v", err)
	}

	d, err := db.Open(sharedDBPath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO brokers (project_root, port, multiplexer, pid) VALUES (?, ?, ?, ?)`,
		projectRoot, rightPort, "*multiplexer.Herdr", 12345,
	); err != nil {
		t.Fatalf("insert local broker row: %v", err)
	}
	if err := os.WriteFile(broker.DefaultPortFile(), []byte(strconv.Itoa(wrongPort)), 0o600); err != nil {
		t.Fatalf("write global port file: %v", err)
	}

	inboxName = "pi-2"
	inboxWait = false
	inboxTimeout = 30 * time.Second

	out := captureStdout(t, func() {
		if err := runInbox(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runInbox: %v", err)
		}
	})
	got := strings.TrimSpace(out)
	want := "[reply] from opencode-2: from the right broker"
	if got != want {
		t.Fatalf("inbox output = %q, want %q", got, want)
	}
}

func TestAdvertisedCommandsHaveConcreteHandlers(t *testing.T) {
	commands := []*cobra.Command{
		startCmd,
		stopCmd,
		registerCmd,
		unregisterCmd,
		sendCmd,
		replyCmd,
		inboxCmd,
		listCmd,
		statusCmd,
		versionCmd,
		logCmd,
		discoverCmd,
		addAgentCmd,
	}
	for _, cmd := range commands {
		if cmd.Run == nil && cmd.RunE == nil {
			t.Fatalf("command %q has no concrete handler", cmd.Use)
		}
	}
}
