// Package broker holds the in-memory message bus: a per-agent queue plus the
// Broker that enqueues Requests/Replies and drains inboxes. HTTP transport and
// the single truncation point arrive in Tasks 6 and 4.
package broker

import (
	"sync"

	"github.com/tk-425/agentbus/internal/message"
)

// Queue is a thread-safe per-agent FIFO of Messages.
type Queue struct {
	mu       sync.Mutex
	byTo     map[string][]message.Message
	notified map[string]bool // message ID -> arrival notification injected (ADR-0002)
}

// NewQueue returns an empty Queue.
func NewQueue() *Queue {
	return &Queue{byTo: map[string][]message.Message{}, notified: map[string]bool{}}
}

// Enqueue appends msg to the FIFO for msg.To.
func (q *Queue) Enqueue(msg message.Message) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.byTo[msg.To] = append(q.byTo[msg.To], msg)
}

// Drain returns and clears all messages queued for agent, in FIFO order.
func (q *Queue) Drain(agent string) []message.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	msgs := q.byTo[agent]
	delete(q.byTo, agent)
	for _, msg := range msgs {
		delete(q.notified, msg.ID)
	}
	return msgs
}

// UnnotifiedReplies returns queued Replies for agent whose arrival has not yet
// been announced, without removing them. Marking is separate (MarkNotified) so
// a notification that cannot be injected this pass stays pending.
func (q *Queue) UnnotifiedReplies(agent string) []message.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	var replies []message.Message
	for _, msg := range q.byTo[agent] {
		if msg.Kind == message.KindReply && !q.notified[msg.ID] {
			replies = append(replies, msg)
		}
	}
	return replies
}

// MarkNotified records that arrival notifications for these message IDs were
// injected, so UnnotifiedReplies stops returning them.
func (q *Queue) MarkNotified(ids []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, id := range ids {
		q.notified[id] = true
	}
}

// DrainRequests returns and removes only Request messages queued for agent,
// preserving the FIFO order of both the returned Requests and any retained
// non-Request messages.
func (q *Queue) DrainRequests(agent string) []message.Message {
	q.mu.Lock()
	defer q.mu.Unlock()

	msgs := q.byTo[agent]
	if len(msgs) == 0 {
		return nil
	}

	requests := make([]message.Message, 0, len(msgs))
	kept := make([]message.Message, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Kind == message.KindRequest {
			requests = append(requests, msg)
			continue
		}
		kept = append(kept, msg)
	}

	if len(kept) == 0 {
		delete(q.byTo, agent)
	} else {
		q.byTo[agent] = kept
	}
	return requests
}
