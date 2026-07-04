package watcher

import (
	"strings"
	"testing"

	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
	"github.com/tk-425/agentbus/internal/registry"
)

// TestTracer proves the watcher's delivery path through every layer: a Request to
// an idle mock Agent instance is injected exactly once with the reply-command
// instruction (ID pre-filled, no marker text), submission is confirmed, and the
// watcher produces no Reply — the Recipient returns its answer by running
// `agentbus reply <id>` (ADR-0003), not via pane capture.
func TestTracer(t *testing.T) {
	const (
		requester     = "codex-1"
		recipient     = "claude-1"
		recipientPane = "%1"
		requestText   = "summarize the spec"
	)

	b := broker.New()
	b.Registry.Register(registry.Instance{Name: recipient, Project: "proj", PaneID: recipientPane})
	c := client.New(b)

	mux := multiplexer.NewMock()
	mux.SetIdle(recipientPane, true)
	mux.SetAwaitBusy(recipientPane, true)

	// Requester sends a Request to the recipient.
	req := message.Message{
		ID:   message.NewID(),
		Kind: message.KindRequest,
		From: requester,
		To:   recipient,
		Body: requestText,
	}
	if err := c.Send(req); err != nil {
		t.Fatalf("send request: %v", err)
	}

	// Recipient's watcher runs one delivery pass.
	if err := Watch(recipient, recipientPane, mux, c); err != nil {
		t.Fatalf("watch: %v", err)
	}

	// The watcher builds no Reply; the requester's inbox stays empty until the
	// recipient runs the reply command.
	if inbox := c.Inbox(requester); len(inbox) != 0 {
		t.Fatalf("watcher must not produce a Reply; requester inbox = %+v", inbox)
	}

	// The request text was injected exactly once, leading with the body and
	// naming the reply command with the request ID — and no marker text.
	injected := mux.Injected(recipientPane)
	if len(injected) != 1 {
		t.Fatalf("injection log: got %d entries, want 1 (%v)", len(injected), injected)
	}
	if injected[0] != injectionText(req) {
		t.Errorf("injected text: got %q, want %q", injected[0], injectionText(req))
	}
	if !strings.HasPrefix(injected[0], requestText) {
		t.Errorf("injected text must lead with the request body, got %q", injected[0])
	}
	if !strings.Contains(injected[0], "agentbus reply "+req.ID) {
		t.Errorf("injected text must name the reply command with the request ID, got %q", injected[0])
	}
	if strings.Contains(injected[0], "<<") || strings.Contains(injected[0], ">>") {
		t.Errorf("injected text must carry no marker text, got %q", injected[0])
	}
}
