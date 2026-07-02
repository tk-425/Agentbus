package broker

import (
	"os"
	"strings"
	"testing"

	"github.com/tk-425/agentbus/internal/message"
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
