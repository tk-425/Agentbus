package multiplexer

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var herdrCommand = exec.Command

// injectSettle is the pause between typing a Request and pressing Enter.
// Agent TUIs process a paste asynchronously; an immediate Enter can arrive
// before the composer holds the text, leaving the Request typed but never
// submitted.
const injectSettle = 250 * time.Millisecond

// Herdr is the reliable Multiplexer backend (ADR-0001): herdr exposes agent
// status and pane output directly, so Idle detection and capture do not depend
// on guessing from terminal output.
type Herdr struct{}

// NewHerdr returns the herdr-backed Multiplexer.
func NewHerdr() *Herdr {
	return &Herdr{}
}

type herdrPane struct {
	PaneID        string `json:"pane_id"`
	Agent         string `json:"agent"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
}

type herdrPaneListEnvelope struct {
	Result struct {
		Panes []herdrPane `json:"panes"`
	} `json:"result"`
}

type herdrProcessInfoEnvelope struct {
	Result struct {
		ProcessInfo struct {
			ForegroundProcesses []struct {
				Argv0 string `json:"argv0"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	} `json:"result"`
}

// ListPanes enumerates herdr panes from its JSON pane list.
func (h *Herdr) ListPanes() ([]Pane, error) {
	out, err := herdrCommand("herdr", "pane", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr pane list: %w", err)
	}
	var raw herdrPaneListEnvelope
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse herdr pane list: %w", err)
	}
	panes := make([]Pane, 0, len(raw.Result.Panes))
	for _, p := range raw.Result.Panes {
		command := p.Agent
		if command == "" {
			var err error
			command, err = h.paneCommand(p.PaneID)
			if err != nil {
				return nil, err
			}
		}
		cwd := p.CWD
		if cwd == "" {
			cwd = p.ForegroundCWD
		}
		panes = append(panes, Pane{ID: p.PaneID, CWD: cwd, Command: command})
	}
	return panes, nil
}

func (h *Herdr) paneCommand(paneID string) (string, error) {
	out, err := herdrCommand("herdr", "pane", "process-info", "--pane", paneID).Output()
	if err != nil {
		return "", fmt.Errorf("herdr pane process-info %s: %w", paneID, err)
	}
	var raw herdrProcessInfoEnvelope
	if err := json.Unmarshal(out, &raw); err != nil {
		return "", fmt.Errorf("parse herdr pane process-info %s: %w", paneID, err)
	}
	if len(raw.Result.ProcessInfo.ForegroundProcesses) == 0 {
		return "", nil
	}
	return raw.Result.ProcessInfo.ForegroundProcesses[0].Argv0, nil
}

// Inject types text into the pane via herdr (used only for Requests, only
// while Idle). Agent TUIs are interactive full-screen apps, so use send-text
// followed by Enter rather than pane run, which is intended for shell commands.
func (h *Herdr) Inject(paneID, text string) error {
	if out, err := herdrCommand("herdr", "pane", "send-text", paneID, text).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr pane send-text: %w: %s", err, strings.TrimSpace(string(out)))
	}
	time.Sleep(injectSettle)
	return h.PressEnter(paneID)
}

// PressEnter sends a lone Enter keypress to the pane.
func (h *Herdr) PressEnter(paneID string) error {
	if out, err := herdrCommand("herdr", "pane", "send-keys", paneID, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("herdr pane send-keys Enter: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AwaitBusy blocks until herdr reports the pane's agent as working, or until
// timeout. A wait failure reports the transition as unobserved rather than an
// error the Watcher would abort on — the agent may simply have finished (or
// never started) within the window.
func (h *Herdr) AwaitBusy(paneID string, timeout time.Duration) (bool, error) {
	ms := strconv.FormatInt(timeout.Milliseconds(), 10)
	if err := herdrCommand("herdr", "wait", "agent-status", paneID, "--status", "working", "--timeout", ms).Run(); err != nil {
		return false, nil
	}
	return true, nil
}

type herdrAgentGetEnvelope struct {
	Result struct {
		Agent struct {
			AgentStatus string `json:"agent_status"`
		} `json:"agent"`
	} `json:"result"`
}

// agentStatus returns the pane's current agent status via herdr agent get.
func (h *Herdr) agentStatus(paneID string) (string, error) {
	out, err := herdrCommand("herdr", "agent", "get", paneID).Output()
	if err != nil {
		return "", fmt.Errorf("herdr agent get %s: %w", paneID, err)
	}
	var raw herdrAgentGetEnvelope
	if err := json.Unmarshal(out, &raw); err != nil {
		return "", fmt.Errorf("parse herdr agent get %s: %w", paneID, err)
	}
	return raw.Result.Agent.AgentStatus, nil
}

// idleWaitSlice bounds one blocking status wait inside IsIdle. The Watcher's
// own retry loop provides the long-horizon coverage; each slice re-checks the
// current status first, so a terminal status other than idle can never hang a
// watcher on a transition herdr will not emit.
const idleWaitSlice = 1 * time.Second

// IsIdle reports whether the pane's agent is not mid-task. Agents signal that
// with status idle or done — pi ends tasks at done and may never return to
// idle, so both count. When neither is current, block one bounded slice for
// the idle transition and let the caller's poll loop retry.
func (h *Herdr) IsIdle(paneID string) (bool, error) {
	if status, err := h.agentStatus(paneID); err == nil && (status == "idle" || status == "done") {
		return true, nil
	}
	ms := strconv.FormatInt(idleWaitSlice.Milliseconds(), 10)
	if err := herdrCommand("herdr", "wait", "agent-status", paneID, "--status", "idle", "--timeout", ms).Run(); err != nil {
		return false, nil
	}
	return true, nil
}
