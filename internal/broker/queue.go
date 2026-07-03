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
	mu    sync.Mutex
	byTo  map[string][]message.Message
}

// NewQueue returns an empty Queue.
func NewQueue() *Queue {
	return &Queue{byTo: map[string][]message.Message{}}
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
	return msgs
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


