package broker_test

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/message"
)

// newTestClient starts an httptest server on the broker's handler and returns an
// HTTP-backed client plus the server for cleanup. The broker serves proj-a so
// /send can resolve bare local names. No real port or port file is touched.
func newTestClient(t *testing.T) *client.Client {
	t.Helper()
	b := broker.New()
	b.Registry.SetLocalProject("proj-a")
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)
	return client.NewRemote(srv.URL)
}

func TestHTTPRegisterSendInboxAckRoundTrip(t *testing.T) {
	c := newTestClient(t)

	name, err := c.Register("proj-a", "claude", "%1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if name != "claude-1" {
		t.Fatalf("Register name = %q, want claude-1", name)
	}

	req := message.Message{ID: "m1", Kind: message.KindRequest, From: "codex-1", To: name, Body: "hi"}
	if err := c.Send(req); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := c.Inbox(name)
	if len(got) != 1 || got[0].Body != "hi" || got[0].From != "codex-1" {
		t.Fatalf("Inbox round-trip mismatch: %+v", got)
	}

	if err := c.Ack(name, "m1"); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	// Inbox is drain-on-read: a second read is empty.
	if rest := c.Inbox(name); len(rest) != 0 {
		t.Fatalf("inbox should be drained, got %d", len(rest))
	}
}

func TestHTTPSendTruncatesOnce(t *testing.T) {
	c := newTestClient(t)

	// /send routes and 404s unknown agents, so the recipient must be registered.
	name, err := c.Register("proj-a", "claude", "%1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	body := strings.Repeat("x", 32768+5000)
	if err := c.Send(message.Message{Kind: message.KindReply, To: name, Body: body}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := c.Inbox(name)
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d", len(got))
	}
	out := got[0].Body
	if len(out) <= 32768 {
		t.Fatalf("over-cap body should retain marker beyond cap, got len %d", len(out))
	}
	if strings.Count(out, "[truncated") != 1 {
		t.Fatalf("HTTP path must truncate exactly once via Broker.Send, got %d markers", strings.Count(out, "[truncated"))
	}
}

func TestHTTPInboxDrainLeavesDurableHistoryQueryable(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "agentbus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	b := broker.New()
	b.AttachDB(d)
	b.Registry.SetLocalProject("proj-a")
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)
	c := client.NewRemote(srv.URL)

	name, err := c.Register("proj-a", "claude", "%1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	msg := message.Message{ID: "m-durable", Kind: message.KindRequest, From: "codex-1", To: name, Body: "persist me"}
	if err := c.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := c.Inbox(name); len(got) != 1 {
		t.Fatalf("Inbox length = %d, want 1", len(got))
	}
	if rest := c.Inbox(name); len(rest) != 0 {
		t.Fatalf("Inbox should be drained, got %d", len(rest))
	}

	history, err := db.RecentMessages(d, 20)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("RecentMessages length = %d, want 1", len(history))
	}
	if history[0].ID != msg.ID || history[0].Body != "persist me" || history[0].To != name {
		t.Fatalf("durable history mismatch: %+v", history[0])
	}
}
