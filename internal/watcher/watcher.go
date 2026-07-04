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

		if err := mux.Inject(paneID, injectionText(msg)); err != nil {
			return err
		}

		// Confirm the agent actually accepted the Request: on a live TUI the
		// idle status flips to working only after the submission registers. If
		// the transition is never seen, the Enter may not have registered after
		// the paste; press it once more. A stray Enter into an idle agent's empty
		// composer is a no-op.
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
	}
	return notifyReplies(agent, paneID, mux, c)
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

// injectionText appends the reply-command instruction to the Request body on the
// same line, so backends that submit on newline cannot send a partial Request.
// The instruction names the exact reply command with the request ID pre-filled;
// the Recipient runs it to return its answer (ADR-0003). No marker text is
// injected, so the recipient's pane stays clean.
func injectionText(msg message.Message) string {
	return msg.Body + " [agentbus: when done, run: agentbus reply " + msg.ID +
		" \"<your answer>\" — replace <your answer> with your full reply. Return the requested result directly; do not summarize, paraphrase, or restate it unless the request explicitly asks for a summary.]"
}
