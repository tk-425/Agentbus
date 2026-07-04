package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
)

// drainBody sends msg through the broker and returns the single delivered body.
func drainBody(t *testing.T, b *Broker, msg message.Message) string {
	t.Helper()
	if err := b.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := b.Inbox(msg.To)
	if len(got) != 1 {
		t.Fatalf("Inbox: want 1 message, got %d", len(got))
	}
	return got[0].Body
}

func TestSendTruncatesOverCapBodyOnce(t *testing.T) {
	b := New()
	body := strings.Repeat("x", maxBodyBytes+5000)

	out := drainBody(t, b, message.Message{Kind: message.KindReply, To: "claude-1", Body: body})

	if !strings.HasSuffix(out, truncationMarker) {
		t.Fatalf("over-cap body should end with the truncation marker")
	}
	prefix := strings.TrimSuffix(out, truncationMarker)
	if len(prefix) != maxBodyBytes {
		t.Fatalf("truncated body prefix = %d bytes, want %d", len(prefix), maxBodyBytes)
	}
	if strings.Count(out, truncationMarker) != 1 {
		t.Fatalf("marker should appear exactly once, got %d", strings.Count(out, truncationMarker))
	}
}

func TestSendLeavesAtAndUnderCapUnchanged(t *testing.T) {
	cases := map[string]string{
		"under-cap":   strings.Repeat("a", maxBodyBytes-1),
		"exactly-cap": strings.Repeat("a", maxBodyBytes),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out := drainBody(t, New(), message.Message{Kind: message.KindReply, To: "claude-1", Body: body})
			if out != body {
				t.Fatalf("body changed: len in=%d out=%d", len(body), len(out))
			}
			if strings.Contains(out, truncationMarker) {
				t.Fatalf("under/at-cap body must not be marked truncated")
			}
		})
	}
}

func TestSendTruncationIsIdempotent(t *testing.T) {
	b := New()
	body := strings.Repeat("x", maxBodyBytes+5000)

	once := drainBody(t, b, message.Message{Kind: message.KindReply, To: "claude-1", Body: body})
	twice := drainBody(t, b, message.Message{Kind: message.KindReply, To: "claude-1", Body: once})

	if once != twice {
		t.Fatalf("re-sending a truncated body must not change it: len %d vs %d", len(once), len(twice))
	}
	if strings.Count(twice, truncationMarker) != 1 {
		t.Fatalf("idempotent truncation must keep exactly one marker, got %d", strings.Count(twice, truncationMarker))
	}
}

func TestTruncateWritesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	before, _ := os.ReadDir(dir)
	_ = truncate(strings.Repeat("x", maxBodyBytes+5000))
	after, _ := os.ReadDir(dir)

	if len(after) != len(before) {
		t.Fatalf("truncate must not write overflow to disk: temp entries %d -> %d", len(before), len(after))
	}
}

// TestReplyRoutesTerminalReplyToCorrelatedRequester is the broker seam the
// tracer stood up: seeding a Request records a correlation, and Reply turns that
// correlation into a terminal Reply from the Recipient back to the Requester.
func TestReplyRoutesTerminalReplyToCorrelatedRequester(t *testing.T) {
	b := New()
	b.Registry.SetLocalProject("proj-a")
	// The Reply routes to the Requester, so the Requester must be resolvable.
	requester, err := b.Registry.RegisterType("proj-a", "codex", "%1")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	req := message.Message{ID: "req-1", Kind: message.KindRequest, From: requester, To: "claude-1", Body: "ping"}
	if err := b.Send(req); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if err := b.Reply("req-1", "pong"); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	got := b.Inbox(requester)
	if len(got) != 1 {
		t.Fatalf("Inbox(%s): want 1 Reply, got %d", requester, len(got))
	}
	reply := got[0]
	if reply.Kind != message.KindReply {
		t.Fatalf("reply Kind = %q, want %q", reply.Kind, message.KindReply)
	}
	if reply.From != "claude-1" {
		t.Fatalf("reply From = %q, want the Recipient %q", reply.From, "claude-1")
	}
	if reply.To != requester {
		t.Fatalf("reply To = %q, want the Requester %q", reply.To, requester)
	}
	if reply.Body != "pong" {
		t.Fatalf("reply Body = %q, want %q", reply.Body, "pong")
	}
	if reply.ReplyTo != "req-1" {
		t.Fatalf("reply ReplyTo = %q, want the request ID %q", reply.ReplyTo, "req-1")
	}
}

// TestReplyUnknownIDErrorsAndEnqueuesNothing: a reply naming an ID with no
// recorded correlation fails loudly and produces no Reply.
func TestReplyUnknownIDErrorsAndEnqueuesNothing(t *testing.T) {
	b := New()
	b.Registry.SetLocalProject("proj-a")

	if err := b.Reply("ghost", "pong"); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("Reply to unknown ID: want ErrUnknownRequest, got %v", err)
	}
	if stray := b.Inbox("codex-1"); len(stray) != 0 {
		t.Fatalf("unknown-ID reply must enqueue nothing, got %+v", stray)
	}
}

// TestReplyEvictsCorrelationSoIDAnswersOnce: a request ID answers exactly one
// Reply — a second Reply to the same ID is unknown once the first is produced.
func TestReplyEvictsCorrelationSoIDAnswersOnce(t *testing.T) {
	b := New()
	b.Registry.SetLocalProject("proj-a")
	requester, err := b.Registry.RegisterType("proj-a", "codex", "%1")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	if err := b.Send(message.Message{ID: "req-1", Kind: message.KindRequest, From: requester, To: "claude-1", Body: "ping"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := b.Reply("req-1", "pong"); err != nil {
		t.Fatalf("first Reply: %v", err)
	}
	if err := b.Reply("req-1", "again"); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("second Reply to evicted ID: want ErrUnknownRequest, got %v", err)
	}
}

// TestReplyIsAtomicUnderConcurrentReplies: several replies racing on the same
// request ID produce exactly one terminal Reply. The correlation is claimed
// (looked up and removed) under one lock, so only the first caller wins and the
// rest see the ID as already answered — the requester never gets a duplicate.
func TestReplyIsAtomicUnderConcurrentReplies(t *testing.T) {
	b := New()
	b.Registry.SetLocalProject("proj-a")
	requester, err := b.Registry.RegisterType("proj-a", "codex", "%1")
	if err != nil {
		t.Fatalf("RegisterType: %v", err)
	}
	if err := b.Send(message.Message{ID: "req-1", Kind: message.KindRequest, From: requester, To: "claude-1", Body: "ping"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	var successes int32
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			if err := b.Reply("req-1", fmt.Sprintf("pong-%d", i)); err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}(i)
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("concurrent replies: got %d successes, want exactly 1", successes)
	}
	if got := b.Inbox(requester); len(got) != 1 {
		t.Fatalf("concurrent replies must enqueue exactly one Reply, got %d", len(got))
	}
}

func TestServeShutdownRemovesProjectAgentsFromSharedRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbus.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	if err := db.Migrate(d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	b := New()
	b.AttachDB(d)
	b.Registry.SetLocalProject("proj-a")

	projectRoot := filepath.Join(t.TempDir(), "proj-a")
	portFile := filepath.Join(t.TempDir(), "port")
	errCh := make(chan error, 1)
	go func() {
		errCh <- b.Serve(projectRoot, portFile, multiplexer.NewMock())
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(portFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(portFile); err != nil {
		t.Fatalf("port file not created: %v", err)
	}

	if _, err := b.Register("proj-a", "claude", "%1"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var before int
	if err := d.QueryRow(`SELECT COUNT(*) FROM agents WHERE project = ?`, "proj-a").Scan(&before); err != nil {
		t.Fatalf("count agents before shutdown: %v", err)
	}
	if before != 1 {
		t.Fatalf("agent count before shutdown = %d, want 1", before)
	}

	if err := b.Shutdown(context.TODO()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Serve: %v", err)
	}

	var after int
	if err := d.QueryRow(`SELECT COUNT(*) FROM agents WHERE project = ?`, "proj-a").Scan(&after); err != nil {
		t.Fatalf("count agents after shutdown: %v", err)
	}
	if after != 0 {
		t.Fatalf("agent count after shutdown = %d, want 0", after)
	}
}
