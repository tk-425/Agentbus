package multiplexer

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// tmux Idle detection is an approximation (behavioral rule 12, ADR-0001):
// tmux has no notion of agent status, and the target agents are full-screen
// TUIs with no shell prompt to detect. IsIdle therefore treats "captured
// output unchanged for settleWindow" as Idle, giving up after idleBound if
// the output never settles. A spinner or clock in the TUI defeats this —
// herdr is the reliable backend.
const (
	settleWindow = 750 * time.Millisecond // unchanged-output span read as Idle
	settlePoll   = 150 * time.Millisecond // capture sampling interval
	idleBound    = 5 * time.Second        // max time one IsIdle call may block
)

// Tmux is the best-effort Multiplexer backend driving the tmux CLI.
type Tmux struct{}

// NewTmux returns the tmux-backed Multiplexer.
func NewTmux() *Tmux {
	return &Tmux{}
}

// ListPanes enumerates all tmux panes across sessions.
func (x *Tmux) ListPanes() ([]Pane, error) {
	out, err := exec.Command(
		"tmux", "list-panes", "-a", "-F", "#{pane_id}\t#{pane_current_path}\t#{pane_current_command}",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	var panes []Pane
	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		panes = append(panes, Pane{ID: fields[0], CWD: fields[1], Command: fields[2]})
	}
	return panes, nil
}

// Inject types text into the pane followed by Enter (used only for Requests,
// only while Idle).
func (x *Tmux) Inject(paneID, text string) error {
	if out, err := exec.Command("tmux", "send-keys", "-t", paneID, text, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PressEnter sends a lone Enter keypress to the pane.
func (x *Tmux) PressEnter(paneID string) error {
	if out, err := exec.Command("tmux", "send-keys", "-t", paneID, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys Enter: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AwaitBusy approximates the working transition the way IsIdle approximates
// Idle: the pane is considered busy once its captured output changes from the
// snapshot taken at call time. Unchanged output for the whole timeout reports
// the transition as unobserved.
func (x *Tmux) AwaitBusy(paneID string, timeout time.Duration) (bool, error) {
	initial, err := x.Capture(paneID)
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(settlePoll)
		cur, err := x.Capture(paneID)
		if err != nil {
			return false, err
		}
		if cur != initial {
			return true, nil
		}
	}
	return false, nil
}

// Capture returns the pane's visible output.
func (x *Tmux) Capture(paneID string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", paneID).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return string(out), nil
}

// IsIdle reports the pane Idle once its captured output has been unchanged for
// settleWindow, sampling every settlePoll. If the output never settles within
// idleBound the pane is reported busy and the Watcher's own poll loop retries.
// See the package constants for why this is only an approximation.
func (x *Tmux) IsIdle(paneID string) (bool, error) {
	last, err := x.Capture(paneID)
	if err != nil {
		return false, err
	}
	settledSince := time.Now()
	deadline := settledSince.Add(idleBound)
	for time.Now().Before(deadline) {
		time.Sleep(settlePoll)
		cur, err := x.Capture(paneID)
		if err != nil {
			return false, err
		}
		if cur != last {
			last = cur
			settledSince = time.Now()
			continue
		}
		if time.Since(settledSince) >= settleWindow {
			return true, nil
		}
	}
	return false, nil
}
