package watcher

import (
	"strings"
	"testing"

	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
)

// agentPane is the pane bound to the recipient Agent instance under test.
const (
	watchAgent    = "claude-1"
	watchPane     = "%1"
	requester     = "codex-1"
	requesterPane = "%2"
)

// setup wires a fresh Broker, Client, and Mock for one watcher scenario.
func setup(t *testing.T) (*client.Client, *multiplexer.Mock) {
	t.Helper()
	b := broker.New()
	c := client.New(b)
	mux := multiplexer.NewMock()
	return c, mux
}

// TestInjectionTextNamesReplyCommandWithID: the injected Request leads with the
// body and appends the reply command with the request ID pre-filled — and no
// marker text (ADR-0003).
func TestInjectionTextNamesReplyCommandWithID(t *testing.T) {
	msg := message.Message{ID: "req-1", Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}

	text := injectionText(msg)

	if !strings.HasPrefix(text, "say pong") {
		t.Fatalf("injection must lead with the request body, got %q", text)
	}
	if !strings.Contains(text, "agentbus reply req-1") {
		t.Fatalf("injection must name the reply command with the request ID, got %q", text)
	}
	if strings.Contains(text, "<<") || strings.Contains(text, ">>") {
		t.Fatalf("injection must carry no marker text, got %q", text)
	}
}

// TestRequestInjectedWithoutProducingReply: a delivery pass injects the Request,
// confirms submission, and produces no Reply — the Recipient answers by running
// the reply command, not by the Watcher capturing the pane (ADR-0003).
func TestRequestInjectedWithoutProducingReply(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)
	mux.SetAwaitBusy(watchPane, true) // agent visibly accepts the Request

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	injected := mux.Injected(watchPane)
	if len(injected) != 1 {
		t.Fatalf("injection count: got %d, want 1 (%v)", len(injected), injected)
	}
	if injected[0] != injectionText(req) {
		t.Fatalf("injected text = %q, want %q", injected[0], injectionText(req))
	}
	if got := c.Inbox(requester); len(got) != 0 {
		t.Fatalf("watcher must not produce a Reply; requester inbox = %+v", got)
	}
}

// TestReplyBodyNeverInjected: a Reply sitting in the watched agent's inbox
// triggers only an arrival notification (ADR-0002) — the Reply body itself is
// never injected into the pane (ADR-0001), and the Reply stays inbox-readable.
func TestReplyBodyNeverInjected(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	c.Send(message.Message{ID: message.NewID(), Kind: message.KindReply, From: requester, To: watchAgent, Body: "a reply body"})

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	injected := mux.Injected(watchPane)
	if len(injected) != 1 {
		t.Fatalf("injection count: got %d, want 1 notification (%v)", len(injected), injected)
	}
	if strings.Contains(injected[0], "a reply body") {
		t.Fatalf("reply body was injected: %q", injected[0])
	}
	if !strings.Contains(injected[0], "[agentbus]") || !strings.Contains(injected[0], requester) ||
		!strings.Contains(injected[0], "agentbus inbox --name "+watchAgent) {
		t.Fatalf("notification missing provenance or read command: %q", injected[0])
	}
	if got := c.Inbox(watchAgent); len(got) != 1 || got[0].Body != "a reply body" {
		t.Fatalf("reply must remain inbox-readable after notification: %v", got)
	}
}

// TestReplyNotificationInjectedOnce: a second watcher pass must not re-announce
// an already-notified Reply.
func TestReplyNotificationInjectedOnce(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	c.Send(message.Message{ID: message.NewID(), Kind: message.KindReply, From: requester, To: watchAgent, Body: "a reply body"})

	for range 2 {
		if err := Watch(watchAgent, watchPane, mux, c); err != nil {
			t.Fatalf("watch: %v", err)
		}
	}

	if got := mux.Injected(watchPane); len(got) != 1 {
		t.Fatalf("injection count after two passes: got %d, want 1 (%v)", len(got), got)
	}
}

// TestReplyNotificationDeferredUntilIdle: a pane that never goes Idle receives
// no notification this pass, and the Reply stays pending so the next pass (with
// an Idle pane) announces it.
func TestReplyNotificationDeferredUntilIdle(t *testing.T) {
	c, mux := setup(t)
	// Pane stays busy: no notification may be injected.

	c.Send(message.Message{ID: message.NewID(), Kind: message.KindReply, From: requester, To: watchAgent, Body: "a reply body"})

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("busy watch: %v", err)
	}
	if got := mux.Injected(watchPane); len(got) != 0 {
		t.Fatalf("notification injected into busy pane: %v", got)
	}

	mux.SetIdle(watchPane, true)
	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("idle watch: %v", err)
	}
	if got := mux.Injected(watchPane); len(got) != 1 {
		t.Fatalf("deferred notification: got %d injections, want 1 (%v)", len(got), got)
	}
}

// TestNoNotificationForDrainedReply: a Reply read from the inbox before the
// watcher pass is gone — nothing to announce.
func TestNoNotificationForDrainedReply(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	c.Send(message.Message{ID: message.NewID(), Kind: message.KindReply, From: requester, To: watchAgent, Body: "a reply body"})
	if got := c.Inbox(watchAgent); len(got) != 1 {
		t.Fatalf("inbox drain: %v", got)
	}

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if got := mux.Injected(watchPane); len(got) != 0 {
		t.Fatalf("notification injected for drained reply: %v", got)
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

// TestEnterRetriedWhenAgentNeverLeavesIdle: if the agent never leaves Idle
// after injection — the paste-then-submit race where Enter did not register —
// the watcher presses Enter exactly once more, without re-typing the Request.
func TestEnterRetriedWhenAgentNeverLeavesIdle(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)
	// AwaitBusy defaults to false: the agent never visibly accepts the Request.

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if got := mux.EnterPresses(watchPane); got != 1 {
		t.Fatalf("enter retries: got %d, want 1", got)
	}
	if got := mux.Injected(watchPane); len(got) != 1 {
		t.Fatalf("request must not be re-typed on retry: %d injections (%v)", len(got), got)
	}
}

// TestNoEnterRetryWhenAgentAcceptsRequest: when the working transition is
// observed after injection, no extra Enter is pressed and no Reply is produced.
func TestNoEnterRetryWhenAgentAcceptsRequest(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)
	mux.SetAwaitBusy(watchPane, true)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if got := mux.EnterPresses(watchPane); got != 0 {
		t.Fatalf("enter retries: got %d, want 0", got)
	}
	if got := c.Inbox(requester); len(got) != 0 {
		t.Fatalf("watcher must not produce a Reply; requester inbox = %+v", got)
	}
}

// Reminder timing (the reply-grace / idle-grace windows) is driven by continuous
// Idle duration and is unit-tested at the broker Correlation seam with controlled
// timestamps (internal/broker/correlation_test.go). It is not re-driven here: the
// bounds are unexported in internal/broker and a live-time watcher pass cannot
// fast-forward the grace windows, so the watcher tests assert the injection-safety
// wiring — no Reminder before engagement and none while the Recipient is busy —
// which is what the Watcher itself is responsible for.

// TestNoReminderBeforeEngagement: idle passes before the Recipient ever engages
// the Request (no busy observation) inject no Reminder — Rule 2.
func TestNoReminderBeforeEngagement(t *testing.T) {
	c, mux := setup(t)
	mux.SetAwaitBusy(watchPane, true)
	mux.SetIdle(watchPane, true)

	req := message.Message{ID: "req-1", Kind: message.KindRequest, From: requester, To: watchAgent, Body: "do work"}
	c.Send(req)

	for range 3 { // deliver, then more Idle passes with no intervening busy edge
		if err := Watch(watchAgent, watchPane, mux, c); err != nil {
			t.Fatalf("watch: %v", err)
		}
	}

	if injected := mux.Injected(watchPane); len(injected) != 1 {
		t.Fatalf("only the Request should be injected before engagement, got %d: %v", len(injected), injected)
	}
}

// TestBusyRecipientNoReminderNoDiagnostic: a Recipient that engages the Request
// then stays busy gets no Reminder and no Diagnostic Reply.
func TestBusyRecipientNoReminderNoDiagnostic(t *testing.T) {
	c, mux := setup(t)
	mux.SetAwaitBusy(watchPane, true)

	req := message.Message{ID: "req-1", Kind: message.KindRequest, From: requester, To: watchAgent, Body: "do work"}
	c.Send(req)

	mux.SetIdle(watchPane, true)
	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("deliver watch: %v", err)
	}
	mux.SetIdle(watchPane, false) // Recipient engaged and stays busy
	for range 3 {
		if err := Watch(watchAgent, watchPane, mux, c); err != nil {
			t.Fatalf("busy watch: %v", err)
		}
	}

	if injected := mux.Injected(watchPane); len(injected) != 1 {
		t.Fatalf("busy Recipient should get only the Request, got %d: %v", len(injected), injected)
	}
	if got := c.Inbox(requester); len(got) != 0 {
		t.Fatalf("busy Recipient must yield no Diagnostic Reply, got %+v", got)
	}
}

// TestReplyProducesNothingFurther: a Reply is terminal — beyond its one-time
// arrival notification it causes no body injection and no new message anywhere.
func TestReplyProducesNothingFurther(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	c.Send(message.Message{ID: message.NewID(), Kind: message.KindReply, From: requester, To: watchAgent, Body: "terminal reply"})

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	for _, in := range mux.Injected(watchPane) {
		if strings.Contains(in, "terminal reply") {
			t.Errorf("reply body was injected: %q", in)
		}
	}
	if got := c.Inbox(requester); len(got) != 0 {
		t.Errorf("reply produced a new message for the requester: %v", got)
	}
	if got := c.Inbox(watchAgent); len(got) != 1 || got[0].Kind != message.KindReply || got[0].Body != "terminal reply" {
		t.Errorf("reply should remain in the agent inbox for human read: %v", got)
	}
}
