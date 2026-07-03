package watcher

import (
	"strings"
	"testing"
	"time"

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

// setup wires a fresh Broker, Client, and Mock for one watcher scenario, and
// shrinks the marker-poll bound so diagnostic-path tests do not wait out the
// real-world grace window.
func setup(t *testing.T) (*client.Client, *multiplexer.Mock) {
	t.Helper()
	savedInterval, savedAttempts := markerPollInterval, markerPollMaxAttempts
	markerPollInterval, markerPollMaxAttempts = time.Millisecond, 20
	t.Cleanup(func() {
		markerPollInterval, markerPollMaxAttempts = savedInterval, savedAttempts
	})
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

// TestReplyUsesOnlyMarkedOutput: a reply should contain only the text the agent
// wrapped in the Request's markers, not the pane content around it.
func TestReplyUsesOnlyMarkedOutput(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}
	mux.SetCaptureSequence(watchPane, []string{
		"startup banner\nold prompt",
		"startup banner\nold prompt\n\n<<AGENTBUS_REPLY " + req.ID + ">>\npong from claude\n<<AGENTBUS_END " + req.ID + ">>",
	})
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	inbox := c.Inbox(requester)
	if len(inbox) != 1 {
		t.Fatalf("requester inbox: got %d replies, want 1", len(inbox))
	}
	if inbox[0].Body != "pong from claude" {
		t.Fatalf("reply body = %q, want %q", inbox[0].Body, "pong from claude")
	}
}

// TestReplyWaitsForMarkedCapture: if the first post-idle captures do not yet
// contain the reply markers, watcher should keep reading until they appear and
// then extract the marked reply body.
func TestReplyWaitsForMarkedCapture(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "answer"}
	mux.SetCaptureSequence(watchPane, []string{
		"prompt before",
		"prompt before\n\nCooking...",
		"prompt before\n\n<<AGENTBUS_REPLY " + req.ID + ">>\nfinal answer\n<<AGENTBUS_END " + req.ID + ">>",
	})
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	inbox := c.Inbox(requester)
	if len(inbox) != 1 {
		t.Fatalf("requester inbox: got %d replies, want 1", len(inbox))
	}
	if inbox[0].Body != "final answer" {
		t.Fatalf("reply body = %q, want %q", inbox[0].Body, "final answer")
	}
}

// TestRequesterWatcherDoesNotDrainReplies: once a recipient watcher has
// produced a Reply, the requester's own watcher must not consume it before a
// human reads the requester's inbox.
func TestRequesterWatcherDoesNotDrainReplies(t *testing.T) {
	b := broker.New()
	c := client.New(b)
	mux := multiplexer.NewMock()
	mux.SetIdle(watchPane, true)
	mux.SetIdle(requesterPane, true)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "reply with pong"}
	mux.SetCapture(watchPane, "<<AGENTBUS_REPLY "+req.ID+">>\npong from claude\n<<AGENTBUS_END "+req.ID+">>")
	mux.SetCapture(requesterPane, "should never be captured")

	if err := c.Send(req); err != nil {
		t.Fatalf("send request: %v", err)
	}

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("recipient watch: %v", err)
	}
	if err := Watch(requester, requesterPane, mux, c); err != nil {
		t.Fatalf("requester watch: %v", err)
	}

	inbox := c.Inbox(requester)
	if len(inbox) != 1 {
		t.Fatalf("requester inbox after its own watcher ran: got %d replies, want 1", len(inbox))
	}
	if inbox[0].Kind != message.KindReply || inbox[0].Body != "pong from claude" || inbox[0].ReplyTo != req.ID {
		t.Fatalf("unexpected requester reply: %+v", inbox[0])
	}
	if got := mux.Injected(requesterPane); len(got) != 0 {
		t.Fatalf("requester watcher injected a terminal reply: %v", got)
	}
}

// TestReplyExtractsOnlyMarkedTextFromRepaintingTUI: live agent TUIs repaint the
// whole frame (spinner, status bar, prompt box), so a post-injection capture is
// never a prefix-extension of the pre-injection capture. The Reply must carry
// only the text the agent wrapped in the per-Request markers — never the frame.
func TestReplyExtractsOnlyMarkedTextFromRepaintingTUI(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}
	before := "╭──────────────╮\n│ ❯            │\n╰──────────────╯\n  claude · ●○○○ 12% (35k/1M) · idle"
	// The echoed instruction carries both markers on one line; the agent's real
	// reply puts each marker on its own line. Chrome around both has changed.
	after := "❯ say pong [agentbus: … <<AGENTBUS_REPLY " + req.ID + ">> … <<AGENTBUS_END " + req.ID + ">> …]\n" +
		"⏺ Sure.\n" +
		"<<AGENTBUS_REPLY " + req.ID + ">>\n" +
		"pong from claude\n" +
		"<<AGENTBUS_END " + req.ID + ">>\n" +
		"╭──────────────╮\n│ ❯            │\n╰──────────────╯\n  claude · ●●○○ 14% (37k/1M) · idle"
	mux.SetCaptureSequence(watchPane, []string{before, after, after})

	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	inbox := c.Inbox(requester)
	if len(inbox) != 1 {
		t.Fatalf("requester inbox: got %d replies, want 1", len(inbox))
	}
	if inbox[0].Body != "pong from claude" {
		t.Fatalf("reply body = %q, want %q", inbox[0].Body, "pong from claude")
	}
}

// TestMissingMarkersProduceDiagnosticReply: when the agent never prints the
// reply markers, the requester must receive a short diagnostic Reply naming the
// Request — not a dump of the pane frame.
func TestMissingMarkersProduceDiagnosticReply(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	frame := "╭──────────────╮\n│ ❯            │\n╰──────────────╯\n  claude · working"
	mux.SetCapture(watchPane, frame)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	inbox := c.Inbox(requester)
	if len(inbox) != 1 {
		t.Fatalf("requester inbox: got %d replies, want 1", len(inbox))
	}
	if strings.Contains(inbox[0].Body, "╭") {
		t.Fatalf("reply leaked the pane frame: %q", inbox[0].Body)
	}
	if !strings.Contains(inbox[0].Body, req.ID) {
		t.Errorf("diagnostic reply should name the request ID %s, got %q", req.ID, inbox[0].Body)
	}
}

// TestReplyStripsTrailingPaddingAndCursor: a TUI may pad captured lines to the
// pane width and paint its cursor block inside the marked region; neither
// belongs in the Reply body.
func TestReplyStripsTrailingPaddingAndCursor(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}
	mux.SetCapture(watchPane, "<<AGENTBUS_REPLY "+req.ID+">>\n"+
		"line one padded            \n"+
		"line two with cursor            █\n"+
		"<<AGENTBUS_END "+req.ID+">>")
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	inbox := c.Inbox(requester)
	if len(inbox) != 1 {
		t.Fatalf("requester inbox: got %d replies, want 1", len(inbox))
	}
	want := "line one padded\nline two with cursor"
	if inbox[0].Body != want {
		t.Fatalf("reply body = %q, want %q", inbox[0].Body, want)
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
// observed after injection, no extra Enter is pressed and the marked reply is
// extracted normally.
func TestNoEnterRetryWhenAgentAcceptsRequest(t *testing.T) {
	c, mux := setup(t)
	mux.SetIdle(watchPane, true)
	mux.SetAwaitBusy(watchPane, true)

	req := message.Message{ID: message.NewID(), Kind: message.KindRequest, From: requester, To: watchAgent, Body: "say pong"}
	mux.SetCapture(watchPane, "<<AGENTBUS_REPLY "+req.ID+">>\npong from claude\n<<AGENTBUS_END "+req.ID+">>")
	c.Send(req)

	if err := Watch(watchAgent, watchPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if got := mux.EnterPresses(watchPane); got != 0 {
		t.Fatalf("enter retries: got %d, want 0", got)
	}
	inbox := c.Inbox(requester)
	if len(inbox) != 1 || inbox[0].Body != "pong from claude" {
		t.Fatalf("requester inbox: %+v", inbox)
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
	if got := c.Inbox(watchAgent); len(got) != 1 || got[0].Kind != message.KindReply || got[0].Body != "terminal reply" {
		t.Errorf("reply should remain in the agent inbox for human read: %v", got)
	}
}
