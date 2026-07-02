package multiplexer

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Herdr is the reliable Multiplexer backend (ADR-0001): herdr exposes agent
// status and pane output directly, so Idle detection and capture do not depend
// on guessing from terminal output.
type Herdr struct{}

// NewHerdr returns the herdr-backed Multiplexer.
func NewHerdr() *Herdr {
	return &Herdr{}
}

// herdrPane mirrors one entry of `herdr pane list --json`.
type herdrPane struct {
	ID      string `json:"id"`
	CWD     string `json:"cwd"`
	Command string `json:"command"`
}

// ListPanes enumerates herdr panes from its JSON pane list.
func (h *Herdr) ListPanes() ([]Pane, error) {
	out, err := exec.Command("herdr", "pane", "list", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr pane list: %w", err)
	}
	var raw []herdrPane
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse herdr pane list: %w", err)
	}
	panes := make([]Pane, 0, len(raw))
	for _, p := range raw {
		panes = append(panes, Pane{ID: p.ID, CWD: p.CWD, Command: p.Command})
	}
	return panes, nil
}

// Inject types text into the pane via herdr (used only for Requests, only
// while Idle).
func (h *Herdr) Inject(paneID, text string) error {
	if out, err := exec.Command("herdr", "pane", "run", paneID, text).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr pane run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Capture returns the pane's recent output, unwrapped, via herdr.
func (h *Herdr) Capture(paneID string) (string, error) {
	out, err := exec.Command("herdr", "pane", "read", paneID, "--source", "recent-unwrapped").Output()
	if err != nil {
		return "", fmt.Errorf("herdr pane read: %w", err)
	}
	return string(out), nil
}

// IsIdle blocks until herdr reports the pane's agent status as done, then
// returns true. herdr tracks agent status natively, so this is the reliable
// Idle signal; a wait failure reports not-Idle rather than an error the
// Watcher would abort on.
func (h *Herdr) IsIdle(paneID string) (bool, error) {
	if err := exec.Command("herdr", "wait", "agent-status", paneID, "--status", "done").Run(); err != nil {
		return false, nil
	}
	return true, nil
}
