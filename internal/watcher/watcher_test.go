package watcher

import (
	"testing"

	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
)

// agentPane is the pane bound to the recipient Agent instance under test.
const (
	watchAgent = "claude-1"
	watchPane  = "%1"
	requester  = "codex-1"
)

// setup wires a fresh Broker, Client, and Mock for one watcher scenario.
func setup(t *testing.T) (*client.Client, *multiplexer.Mock) {
	t.Helper()
	b := broker.New()
	c := client.New(b)
	mux := multiplexer.NewMock()
	mux.SetCapture(watchPane, "captured output")
	return c, mux
}

// TestReplyNeverInjected: a Reply sitting in the watched agent's inbox is never
// injected into its pane — replies are terminal and inbox-only (ADR-0001).
func TestReplyNeverInjected(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	c.Send(message.Message{ID: message.NewID(), Kind: message.KindReply, From: requester, To: watchAgent, Body: "a reply body"})

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if got := mux.Injected(watchPane); len(got) != 0 {
		t.Fatalf("reply was injected: %v", got)
	}
}

// TestInjectsOnlyAfterIdle: a Request to a busy-then-idle agent is injected only
// after the pane flips to Idle.
func TestInjectsOnlyAfterIdle(t *testing.T) {
	c, mux := setup(t)
	const busyPolls = 3
	mux.SetIdle(watchPane, true)
	mux.SetIdleAfter(watchPane, busyPolls) // report busy for the first 3 polls

	c.Send(message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "do work"})

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	injected := mux.Injected(watchPane)
	if len(injected) != 1 {
		t.Fatalf("injection count: got %d, want 1", len(injected))
	}
	if mux.IdleCalls(watchPane) <= busyPolls {
		t.Errorf("did not wait through busy polls: IsIdle called %d times, want > %d", mux.IdleCalls(watchPane), busyPolls)
	}
	for i, idle := range mux.InjectedWhileIdle(watchPane) {
		if !idle {
			t.Errorf("injection %d happened while pane was busy", i)
		}
	}
}

// TestNeverIdleNeverInjects: a Request to an agent that never goes Idle is not
// injected and is re-queued for a later pass — the watcher must never type into
// a busy pane (behavioral rule 2, User Story 1).
func TestNeverIdleNeverInjects(t *testing.T) {
	c, mux := setup(t)
	// Pane stays busy: SetIdle is never set true, so IsIdle always reports false.

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "do work"}
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if got := mux.Injected(watchPane); len(got) != 0 {
		t.Fatalf("request was injected into a never-idle pane: %v", got)
	}
	requeued := c.Inbox(watchAgent)
	if len(requeued) != 1 || requeued[0].ID != req.ID {
		t.Fatalf("request must be re-queued for the agent, got %+v", requeued)
	}
	if got := c.Inbox(requester); len(got) != 0 {
		t.Fatalf("no Reply should be produced for a never-injected Request, got %+v", got)
	}
}

// TestOneRequestOneReply: a single Request yields exactly one Reply in the
// requester's inbox.
func TestOneRequestOneReply(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "do work"}
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	inbox := c.Inbox(requester)
	if len(inbox) != 1 {
		t.Fatalf("requester inbox: got %d replies, want 1", len(inbox))
	}
	if inbox[0].Kind != message.KindReply || inbox[0].ReplyTo != req.ID {
		t.Errorf("inbox message is not the matching reply: %+v", inbox[0])
	}
}

// TestReplyProducesNothingFurther: feeding a Reply to the watcher causes no
// injection and no new message anywhere — a Reply is terminal.
func TestReplyProducesNothingFurther(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	c.Send(message.Message{ID: message.NewID(), Kind: message.KindReply, From: requester, To: watchAgent, Body: "terminal reply"})

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if got := mux.Injected(watchPane); len(got) != 0 {
		t.Errorf("reply caused an injection: %v", got)
	}
	if got := c.Inbox(requester); len(got) != 0 {
		t.Errorf("reply produced a new message for the requester: %v", got)
	}
	if got := c.Inbox(watchAgent); len(got) != 0 {
		t.Errorf("reply produced a new message for the agent: %v", got)
	}
}
