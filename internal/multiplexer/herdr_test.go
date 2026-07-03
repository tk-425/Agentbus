package multiplexer

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"testing"
)

func TestParseHerdrPaneListEnvelopeUsesPaneCWDAndAgentWhenPresent(t *testing.T) {
	input := []byte(`{"id":"cli:pane:list","result":{"panes":[{"pane_id":"w6:p3","agent":"claude","cwd":"/repo","foreground_cwd":"/Users/terrykang"}],"type":"pane_list"}}`)

	var raw herdrPaneListEnvelope
	if err := json.Unmarshal(input, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(raw.Result.Panes) != 1 {
		t.Fatalf("panes length = %d, want 1", len(raw.Result.Panes))
	}
	pane := raw.Result.Panes[0]
	if pane.PaneID != "w6:p3" {
		t.Fatalf("pane id = %q, want w6:p3", pane.PaneID)
	}
	if pane.Agent != "claude" {
		t.Fatalf("agent = %q, want claude", pane.Agent)
	}
	cwd := pane.CWD
	if cwd == "" {
		cwd = pane.ForegroundCWD
	}
	if cwd != "/repo" {
		t.Fatalf("cwd = %q, want /repo", cwd)
	}
}

func TestParseHerdrPaneListEnvelopeFallsBackToForegroundCWD(t *testing.T) {
	input := []byte(`{"id":"cli:pane:list","result":{"panes":[{"pane_id":"w6:p1","foreground_cwd":"/repo"}],"type":"pane_list"}}`)

	var raw herdrPaneListEnvelope
	if err := json.Unmarshal(input, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pane := raw.Result.Panes[0]
	cwd := pane.CWD
	if cwd == "" {
		cwd = pane.ForegroundCWD
	}
	if cwd != "/repo" {
		t.Fatalf("cwd = %q, want /repo", cwd)
	}
}

func TestParseHerdrProcessInfoEnvelopeUsesForegroundArgv0(t *testing.T) {
	input := []byte(`{"id":"cli:pane:process_info","result":{"process_info":{"foreground_processes":[{"argv0":"pi","cwd":"/repo","name":"node","pid":123}],"pane_id":"w6:p1","shell_pid":3806},"type":"pane_process_info"}}`)

	var raw herdrProcessInfoEnvelope
	if err := json.Unmarshal(input, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	procs := raw.Result.ProcessInfo.ForegroundProcesses
	if len(procs) != 1 {
		t.Fatalf("foreground process count = %d, want 1", len(procs))
	}
	if procs[0].Argv0 != "pi" {
		t.Fatalf("argv0 = %q, want pi", procs[0].Argv0)
	}
}

// TestHerdrIsIdleTreatsDoneAsIdle: pi reports agent status done after a task
// and may never return to idle; IsIdle must treat done as Idle from the
// current status alone, without falling into the blocking wait.
func TestHerdrIsIdleTreatsDoneAsIdle(t *testing.T) {
	old := herdrCommand
	defer func() { herdrCommand = old }()

	var calls [][]string
	herdrCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if len(args) >= 2 && args[0] == "agent" && args[1] == "get" {
			return exec.Command("echo", `{"id":"cli:agent:get","result":{"agent":{"agent_status":"done","pane_id":"w6:pD"},"type":"agent_info"}}`)
		}
		return exec.Command("true")
	}

	idle, err := NewHerdr().IsIdle("w6:pD")
	if err != nil || !idle {
		t.Fatalf("IsIdle = %v, %v; want true, nil", idle, err)
	}
	for _, c := range calls {
		if len(c) >= 2 && c[1] == "wait" {
			t.Fatalf("blocking wait must not run for a terminal status: %v", calls)
		}
	}
}

// TestHerdrIsIdleWaitIsBounded: when the agent is working, the fallback idle
// wait must carry a --timeout so a status herdr never emits cannot hang a
// watcher forever.
func TestHerdrIsIdleWaitIsBounded(t *testing.T) {
	old := herdrCommand
	defer func() { herdrCommand = old }()

	var waitArgs []string
	herdrCommand = func(name string, args ...string) *exec.Cmd {
		if len(args) >= 2 && args[0] == "agent" && args[1] == "get" {
			return exec.Command("echo", `{"id":"cli:agent:get","result":{"agent":{"agent_status":"working","pane_id":"w6:p1"},"type":"agent_info"}}`)
		}
		if len(args) >= 1 && args[0] == "wait" {
			waitArgs = append([]string{name}, args...)
		}
		return exec.Command("true")
	}

	if _, err := NewHerdr().IsIdle("w6:p1"); err != nil {
		t.Fatalf("IsIdle: %v", err)
	}
	if len(waitArgs) == 0 {
		t.Fatal("working status must fall back to the idle wait")
	}
	bounded := false
	for _, a := range waitArgs {
		if a == "--timeout" {
			bounded = true
		}
	}
	if !bounded {
		t.Fatalf("idle wait must be bounded with --timeout: %v", waitArgs)
	}
}

func TestHerdrInjectUsesSendTextThenEnter(t *testing.T) {
	old := herdrCommand
	defer func() { herdrCommand = old }()

	var calls [][]string
	herdrCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("sh", "-c", "exit 0")
	}

	if err := NewHerdr().Inject("w6:p3", "hello world"); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	want := [][]string{
		{"herdr", "pane", "send-text", "w6:p3", "hello world"},
		{"herdr", "pane", "send-keys", "w6:p3", "Enter"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("Inject calls = %#v, want %#v", calls, want)
	}
}
