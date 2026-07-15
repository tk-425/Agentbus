// Package watcher delivers Requests to an Agent instance and announces Replies to
// requesters. It enforces the request/reply asymmetry of ADR-0001: a Request is
// injected only while the agent is Idle, and a Reply is inbox-only and never
// injected. The injected Request carries a single trailing instruction naming the
// reply command with the request ID pre-filled; the Recipient returns its answer
// by running `agentbus reply <id>` (ADR-0003), so the Watcher never captures the
// pane to recover a Reply — its delivery job is to inject a clean Request while
// Idle and confirm submission, then announce any arrived Replies (ADR-0002).
package watcher

import (
	"fmt"
	"strings"
	"time"

	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
)

// idlePollInterval bounds how often a pass rechecks idle state. The bounded
// attempt counts keep the pass from blocking indefinitely. These are vars, not
// consts, so tests can shrink the bounds without waiting out real-world grace
// windows.
var (
	idlePollInterval = 5 * time.Millisecond
	idleMaxAttempts  = 200
	// busyGrace bounds how long after injection the watcher waits to see the
	// agent leave Idle — the confirmation that the Request was submitted.
	busyGrace = 3 * time.Second
)

// Watch performs one delivery pass for agent (bound to paneID): it drains the
// agent's Requests and, for each, waits until the pane is Idle, injects the
// Request, and confirms the agent accepted it. The Recipient returns its answer
// by running `agentbus reply <id>` (ADR-0003); the Watcher no longer captures the
// pane or builds Replies. Replies are never injected — their arrival is announced
// by notifyReplies at the end of the pass (ADR-0002).
func Watch(agent, paneID string, mux multiplexer.Multiplexer, c *client.Client) error {
	for _, msg := range c.Requests(agent) {
		idle, err := waitIdle(paneID, mux)
		if err != nil {
			return err
		}
		if !idle {
			// Idle was never confirmed within the bound. Never inject into a
			// non-idle pane (behavioral rule 2, User Story 1) — re-queue the
			// Request for a later pass rather than corrupting the agent's
			// in-progress work.
			if err := c.Send(msg); err != nil {
				return err
			}
			continue
		}

		if err := injectAndConfirm(paneID, injectionText(msg), mux); err != nil {
			return err
		}
	}
	if err := enforceReminders(agent, paneID, mux, c); err != nil {
		return err
	}
	return notifyReplies(agent, paneID, mux, c)
}

// injectAndConfirm injects text into an Idle pane and confirms the agent
// accepted it: on a live TUI the idle status flips to working only after the
// submission registers. If the transition is never seen, the Enter may not have
// registered after the paste; press it once more. A stray Enter into an idle
// agent's empty composer is a no-op. Callers must have confirmed the pane is Idle.
func injectAndConfirm(paneID, text string, mux multiplexer.Multiplexer) error {
	if err := mux.Inject(paneID, text); err != nil {
		return err
	}
	busy, err := mux.AwaitBusy(paneID, busyGrace)
	if err != nil {
		return err
	}
	if !busy {
		if err := mux.PressEnter(paneID); err != nil {
			return err
		}
		if _, err := mux.AwaitBusy(paneID, busyGrace); err != nil {
			return err
		}
	}
	return nil
}

// enforceReminders folds this pass's single observation of the Recipient's Idle
// state into the broker's Correlation lifecycle: it injects a standalone Reminder
// for each unclaimed Correlation the broker says has returned to Idle still
// unanswered (at most two per Correlation), while the broker emits any terminal
// Diagnostic Reply itself for a Correlation past the idle-grace backstop. A
// Reminder is only ever returned — and so injected — while the Recipient is Idle,
// preserving the request/reply asymmetry (ADR-0001): it carries no Reply content,
// only the request ID and the reply command.
func enforceReminders(agent, paneID string, mux multiplexer.Multiplexer, c *client.Client) error {
	idle, err := mux.IsIdle(paneID)
	if err != nil {
		return err
	}
	for _, id := range c.EnforceReplies(agent, idle, time.Now()) {
		// Re-confirm Idle before each injection: the previous Reminder in this pass
		// drove the pane busy, and injecting into a non-idle pane would break the
		// Idle-only injection safety (ADR-0001). A pane that has not returned to
		// Idle defers its remaining Reminders to a later pass.
		ready, err := waitIdle(paneID, mux)
		if err != nil {
			return err
		}
		if !ready {
			break
		}
		if err := injectAndConfirm(paneID, reminderText(id), mux); err != nil {
			return err
		}
	}
	return nil
}

// notifyReplies announces newly arrived Replies (ADR-0002): while the pane is
// Idle it injects a short notification naming the senders and the inbox read
// command. The Reply bodies are never injected and stay queued until read; a
// pane that never goes Idle this pass leaves the Replies unnotified for the
// next pass.
func notifyReplies(agent, paneID string, mux multiplexer.Multiplexer, c *client.Client) error {
	replies := c.UnnotifiedReplies(agent)
	if len(replies) == 0 {
		return nil
	}
	idle, err := waitIdle(paneID, mux)
	if err != nil || !idle {
		return err
	}
	if err := mux.Inject(paneID, notificationText(agent, replies)); err != nil {
		return err
	}
	ids := make([]string, len(replies))
	for i, msg := range replies {
		ids[i] = msg.ID
	}
	c.MarkNotified(ids)
	return nil
}

// notificationText carries only provenance and the read command — injected
// content is executed by the agent, so no Reply body may appear in it.
func notificationText(agent string, replies []message.Message) string {
	senders := make([]string, 0, len(replies))
	seen := map[string]bool{}
	for _, msg := range replies {
		if !seen[msg.From] {
			seen[msg.From] = true
			senders = append(senders, msg.From)
		}
	}
	label := "new reply"
	if len(replies) > 1 {
		label = fmt.Sprintf("%d new replies", len(replies))
	}
	return "[agentbus] " + label + " from " + strings.Join(senders, ", ") +
		" — run: agentbus inbox --name " + agent
}

// waitIdle polls the pane's idle state up to a bounded number of attempts,
// reporting whether idle was confirmed. A false return means the bound elapsed
// without the pane going Idle — the caller must not inject.
func waitIdle(paneID string, mux multiplexer.Multiplexer) (bool, error) {
	for range idleMaxAttempts {
		idle, err := mux.IsIdle(paneID)
		if err != nil {
			return false, err
		}
		if idle {
			return true, nil
		}
		time.Sleep(idlePollInterval)
	}
	return false, nil
}

// reminderText is a standalone Reminder nudging a Recipient that has not yet
// answered request id: it names only the request ID and the exact reply command,
// carrying no task content and no Reply content (ADR-0001), and no marker text.
func reminderText(id string) string {
	return "[agentbus: reminder — you have not yet replied to request " + id +
		". Run exactly: agentbus reply " + id + " \"<your answer>\". Replace <your answer> with your full reply.]"
}

// injectionText appends the reply-command instruction to the Request body on the
// same line, so backends that submit on newline cannot send a partial Request.
// The instruction names the exact reply command with the request ID pre-filled;
// the Recipient runs it to return its answer (ADR-0003). No marker text is
// injected, so the recipient's pane stays clean.
func injectionText(msg message.Message) string {
	return msg.Body + " [agentbus: required final step — you must run exactly: agentbus reply " + msg.ID +
		" \"<your answer>\". Replace <your answer> with your full reply. Do not end the task without running this command. Return the requested result directly; do not summarize, paraphrase, or restate it unless the request explicitly asks for a summary.]"
}
