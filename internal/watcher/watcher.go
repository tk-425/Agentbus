// Package watcher delivers Requests to an Agent instance and returns Replies to
// requesters. It enforces the request/reply asymmetry of ADR-0001: a Request is
// injected only while the agent is Idle, and a Reply is inbox-only and never
// injected. Live agent panes are full-screen TUIs whose captures are screen
// repaints, not an append-only transcript, so the Reply body is recovered via
// per-Request marker lines the agent is instructed to print — never via a
// before/after capture delta.
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
	// markerPoll* bound how long the watcher rereads the capture waiting for
	// the agent's marked reply to appear after Idle is confirmed.
	markerPollInterval    = 100 * time.Millisecond
	markerPollMaxAttempts = 50
	// busyGrace bounds how long after injection the watcher waits to see the
	// agent leave Idle — the confirmation that the Request was submitted.
	busyGrace = 3 * time.Second
)

// Marker lines locate the agent's actual answer inside a live TUI frame. They
// are keyed by Request ID so stale markers from earlier Requests in the same
// scrollback can never match.
const (
	replyStartPrefix = "<<AGENTBUS_REPLY "
	replyEndPrefix   = "<<AGENTBUS_END "
	markerSuffix     = ">>"
)

func startMarker(id string) string { return replyStartPrefix + id + markerSuffix }
func endMarker(id string) string   { return replyEndPrefix + id + markerSuffix }

// Watch performs one delivery pass for agent (bound to paneID): it drains the
// agent's inbox and, for each Request, waits until the pane is Idle, injects the
// Request, captures the output, and sends a Reply back to the requester. Replies
// are never injected.
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
		// idle status flips to working only after the submission registers, and
		// a waitIdle issued before that flip returns immediately — producing a
		// premature (empty) reply. If the transition is never seen, the Enter
		// may not have registered after the paste; press it once more. A stray
		// Enter into an idle agent's empty composer is a no-op.
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

		idle, err = waitIdle(paneID, mux)
		if err != nil {
			return err
		}
		if !idle {
			if err := c.Send(msg); err != nil {
				return err
			}
			continue
		}

		output, found, err := waitMarkedReply(paneID, mux, msg.ID)
		if err != nil {
			return err
		}
		if !found {
			output = "[agentbus] no marked reply for request " + msg.ID +
				": the agent did not print the reply markers"
		}

		reply := message.Message{
			ID:        message.NewID(),
			Kind:      message.KindReply,
			From:      agent,
			To:        msg.From,
			Body:      output,
			ReplyTo:   msg.ID,
			CreatedAt: time.Now().UTC(),
		}
		if err := c.Send(reply); err != nil {
			return err
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

// injectionText appends the marker-protocol instruction to the Request body on
// the same line, so backends that submit on newline cannot send a partial
// Request.
func injectionText(msg message.Message) string {
	return msg.Body + " [agentbus: when your answer is complete, print it wrapped between two lines — one containing only " +
		startMarker(msg.ID) + " and one containing only " + endMarker(msg.ID) + "]"
}

// waitMarkedReply polls the pane capture until it contains a marked reply for
// the Request, up to a bounded number of attempts. A false return means the
// agent never printed the markers within the bound.
func waitMarkedReply(paneID string, mux multiplexer.Multiplexer, id string) (string, bool, error) {
	for range markerPollMaxAttempts {
		cur, err := mux.Capture(paneID)
		if err != nil {
			return "", false, err
		}
		if body, ok := extractReply(cur, id); ok {
			return body, true, nil
		}
		time.Sleep(markerPollInterval)
	}
	return "", false, nil
}

// extractReply returns the text between the Request's marker lines. A line
// carrying both markers is the echoed injection instruction, not a reply, and
// is skipped. Markers are matched by containment so TUI chrome around a marker
// line does not break extraction, and the last complete pair wins so an echoed
// or re-wrapped earlier pair in the same frame cannot shadow the real reply.
func extractReply(capture, id string) (string, bool) {
	start := startMarker(id)
	end := endMarker(id)
	lines := strings.Split(capture, "\n")
	body := ""
	found := false
	startIdx := -1
	for i, line := range lines {
		hasStart := strings.Contains(line, start)
		hasEnd := strings.Contains(line, end)
		switch {
		case hasStart && hasEnd:
			startIdx = -1
		case hasStart:
			startIdx = i
		case hasEnd && startIdx >= 0:
			body = joinTrimmed(lines[startIdx+1 : i])
			found = true
			startIdx = -1
		}
	}
	return strings.TrimSpace(body), found
}

// joinTrimmed right-trims each captured line and strips a trailing cursor
// block glyph: a TUI may pad lines to pane width and paint its cursor cell
// inside the marked region, and neither is part of the agent's answer.
func joinTrimmed(lines []string) string {
	out := make([]string, len(lines))
	for i, line := range lines {
		line = strings.TrimRight(line, " \t")
		line = strings.TrimSuffix(line, "█")
		out[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(out, "\n")
}
