// Package message defines the Request/Reply message model exchanged over the
// agentbus. A Request is injected into a recipient Agent instance while it is
// Idle; a Reply carries captured output back to the requester's inbox and is
// never injected. See ADR-0001 for the asymmetry rationale.
package message

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Kind distinguishes an injectable Request from a terminal, inbox-only Reply.
type Kind string

const (
	// KindRequest is injected into the target Agent instance while Idle.
	KindRequest Kind = "request"
	// KindReply carries captured output back to the requester; inbox-only.
	KindReply Kind = "reply"
)

// Message is a single Request or Reply on the bus.
type Message struct {
	ID        string
	Kind      Kind
	From      string
	To        string
	Body      string
	ReplyTo   string // ID of the Request this Reply answers; empty for a Request
	CreatedAt time.Time
}

// NewID returns a random hex message ID using crypto/rand, avoiding a UUID
// dependency.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read does not fail on supported platforms; fall back to
		// a time-derived value rather than panicking in this delivery path.
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
