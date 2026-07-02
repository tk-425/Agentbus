// Package watcher delivers Requests to an Agent instance and returns Replies to
// requesters. It enforces the request/reply asymmetry of ADR-0001: a Request is
// injected only while the agent is Idle, and a Reply is inbox-only and never
// injected. Task 1 is a single delivery pass; Task 2 hardens idle gating and the
// one-request-one-reply guarantee.
package watcher

import (
	"time"

	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
)

// idlePollInterval bounds how often a pass rechecks idle state. The bounded
// attempt count below keeps the pass from blocking indefinitely; robust backoff
// arrives in Task 2.
const (
	idlePollInterval = 5 * time.Millisecond
	idleMaxAttempts  = 200
)

// Watch performs one delivery pass for agent (bound to paneID): it drains the
// agent's inbox and, for each Request, waits until the pane is Idle, injects the
// Request, captures the output, and sends a Reply back to the requester. Replies
// are never injected.
func Watch(agent, paneID string, mux multiplexer.Multiplexer, c *client.Client) error {
	for _, msg := range c.Inbox(agent) {
		if msg.Kind != message.KindRequest {
			// A Reply is terminal and inbox-only — never inject it.
			continue
		}

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

		if err := mux.Inject(paneID, msg.Body); err != nil {
			return err
		}

		output, err := mux.Capture(paneID)
		if err != nil {
			return err
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
	return nil
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
