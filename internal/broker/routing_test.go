package broker_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/registry"
)

// newLocalBroker returns a broker whose registry serves project with one
// registered Agent instance of agentType, plus that instance's name.
func newLocalBroker(t *testing.T, project, agentType string) (*broker.Broker, string) {
	t.Helper()
	b := broker.New()
	b.Registry.SetLocalProject(project)
	name, err := b.Registry.RegisterType(project, agentType, "%1")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}
	return b, name
}

// newSharedDB opens a migrated temp-dir SQLite file standing in for the shared
// ~/.agentbus/agentbus.db — the real runtime file is never touched.
func newSharedDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "agentbus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return d
}

// newPeerBroker returns a broker serving project behind an httptest server,
// attached to the shared DB with the port it is actually reachable on, so
// cross-broker forwarding in tests hits the real handler.
func newPeerBroker(t *testing.T, d *sql.DB, project string) *broker.Broker {
	t.Helper()
	b := broker.New()
	b.AttachDB(d)
	b.Registry.SetLocalProject(project)
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	b.Registry.AttachDB(d, port)
	return b
}

func TestRoutingBareLocalNameEnqueuesLocally(t *testing.T) {
	b, name := newLocalBroker(t, "proj-a", "claude")

	req := message.Message{ID: "m1", Kind: message.KindRequest, From: "codex-1", To: name, Body: "hi"}
	if err := b.Route(req); err != nil {
		t.Fatalf("Route: %v", err)
	}

	got := b.Inbox(name)
	if len(got) != 1 || got[0].Body != "hi" || got[0].To != name {
		t.Fatalf("bare local name should enqueue on the local broker, got %+v", got)
	}
}

func TestRoutingNameAtProjectForwardsToPeerBroker(t *testing.T) {
	d := newSharedDB(t)
	a := newPeerBroker(t, d, "proj-a")
	peer := newPeerBroker(t, d, "proj-b")

	name, err := peer.Registry.RegisterType("proj-b", "claude", "%2")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	req := message.Message{ID: "m2", Kind: message.KindRequest, From: "codex-1", To: name + "@proj-b", Body: "cross"}
	if err := a.Route(req); err != nil {
		t.Fatalf("Route: %v", err)
	}

	got := peer.Inbox(name)
	if len(got) != 1 || got[0].Body != "cross" {
		t.Fatalf("name@project should forward to the peer broker's queue, got %+v", got)
	}
	if got[0].To != name {
		t.Fatalf("forwarded To should be the bare Agent instance name, got %q", got[0].To)
	}
	if stray := a.Inbox(name); len(stray) != 0 {
		t.Fatalf("cross-project Request must not enqueue on the sending broker, got %+v", stray)
	}
}

func TestRoutingUnknownAgentErrorsAndNeverEnqueues(t *testing.T) {
	d := newSharedDB(t)
	b := broker.New()
	b.Registry.SetLocalProject("proj-a")
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)
	b.Registry.AttachDB(d, srv.Listener.Addr().(*net.TCPAddr).Port)

	req := message.Message{ID: "m3", Kind: message.KindRequest, From: "codex-1", To: "ghost-1", Body: "boo"}
	if err := b.Route(req); !errors.Is(err, registry.ErrUnknownAgent) {
		t.Fatalf("Route to unregistered Agent instance: want ErrUnknownAgent, got %v", err)
	}
	if stray := b.Inbox("ghost-1"); len(stray) != 0 {
		t.Fatalf("unknown agent must never be enqueued, got %+v", stray)
	}

	// The HTTP /send path surfaces the same failure as 404 + error body.
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/send", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /send: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/send to unknown agent: want 404, got %d", resp.StatusCode)
	}
	errBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(errBody), "unknown agent") {
		t.Fatalf("/send 404 should carry a loud error body, got %q", errBody)
	}
	if stray := b.Inbox("ghost-1"); len(stray) != 0 {
		t.Fatalf("HTTP /send to unknown agent must never enqueue, got %+v", stray)
	}
}

func TestRoutingCrossProjectReplyRoutesToRequesterBroker(t *testing.T) {
	d := newSharedDB(t)
	requesterBroker := newPeerBroker(t, d, "proj-a")
	replierBroker := newPeerBroker(t, d, "proj-b")

	requester, err := requesterBroker.Registry.RegisterType("proj-a", "codex", "%1")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	// The Reply's To is the bare requester name from the original Request's From.
	// It is absent in proj-b's local registry, so the replier's broker must
	// resolve it through the shared registry and forward to the requester's broker.
	reply := message.Message{ID: "m4", Kind: message.KindReply, From: "claude-1", To: requester, Body: "done", ReplyTo: "m2"}
	if err := replierBroker.Route(reply); err != nil {
		t.Fatalf("Route: %v", err)
	}

	got := requesterBroker.Inbox(requester)
	if len(got) != 1 || got[0].Kind != message.KindReply || got[0].Body != "done" {
		t.Fatalf("cross-project Reply should land in the requester's inbox on its own broker, got %+v", got)
	}
	if stray := replierBroker.Inbox(requester); len(stray) != 0 {
		t.Fatalf("cross-project Reply must not enqueue on the replier's broker, got %+v", stray)
	}
}

func TestRoutingForwardedRequestPersistsDurableHistoryOnRecipientBroker(t *testing.T) {
	d := newSharedDB(t)
	senderBroker := newPeerBroker(t, d, "proj-a")
	recipientBroker := newPeerBroker(t, d, "proj-b")

	recipient, err := recipientBroker.Registry.RegisterType("proj-b", "claude", "%2")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	req := message.Message{ID: "m-forward", Kind: message.KindRequest, From: "codex-1", To: recipient + "@proj-b", Body: "cross"}
	if err := senderBroker.Route(req); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got := recipientBroker.Inbox(recipient); len(got) != 1 {
		t.Fatalf("Inbox length = %d, want 1", len(got))
	}

	history, err := db.RecentMessages(d, 20)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(history) == 0 {
		t.Fatalf("RecentMessages should include forwarded Request")
	}
	if history[0].ID != "m-forward" || history[0].To != recipient || history[0].Body != "cross" {
		t.Fatalf("forwarded durable history mismatch: %+v", history[0])
	}
	if history[0].Kind != message.KindRequest {
		t.Fatalf("forwarded durable history kind = %q, want %q", history[0].Kind, message.KindRequest)
	}
}
