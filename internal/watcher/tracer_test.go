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

// TestTracer proves the end-to-end happy path through every layer: a Request to
// an idle mock Agent instance is injected once, its captured output returns to
// the requester's inbox as exactly one Reply, and the Reply is never injected.
func TestTracer(t *testing.T) {
	const (
		requester    = "codex-1"
		recipient    = "claude-1"
		recipientPane = "%1"
		requestText  = "summarize the spec"
		captured     = "here is the summary"
	)

	b := broker.New()
	b.Registry.Register(registry.Instance{Name: recipient, Project: "proj", PaneID: recipientPane})
	c := client.New(b)

	mux := multiplexer.NewMock()
	mux.SetIdle(recipientPane, true)
	mux.SetCapture(recipientPane, captured)

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

	// Exactly one Reply lands in the requester's inbox.
	inbox := c.Inbox(requester)
	if len(inbox) != 1 {
		t.Fatalf("requester inbox: got %d messages, want 1", len(inbox))
	}
	reply := inbox[0]
	if reply.Kind != message.KindReply {
		t.Errorf("reply kind: got %q, want %q", reply.Kind, message.KindReply)
	}
	if reply.Body != captured {
		t.Errorf("reply body: got %q, want %q", reply.Body, captured)
	}
	if reply.ReplyTo != req.ID {
		t.Errorf("reply ReplyTo: got %q, want %q", reply.ReplyTo, req.ID)
	}

	// The request text was injected exactly once; the reply text never was.
	injected := mux.Injected(recipientPane)
	if len(injected) != 1 {
		t.Fatalf("injection log: got %d entries, want 1 (%v)", len(injected), injected)
	}
	if injected[0] != requestText {
		t.Errorf("injected text: got %q, want %q", injected[0], requestText)
	}
	for _, in := range injected {
		if strings.Contains(in, captured) {
			t.Errorf("reply text %q was injected into the pane", captured)
		}
	}
}
