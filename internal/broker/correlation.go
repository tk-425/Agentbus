package broker

import (
	"errors"
	"sync"
)

// ErrUnknownRequest is returned when a reply names a request ID with no recorded
// correlation — the Request was never seen here, or its Reply was already
// produced and the correlation evicted.
var ErrUnknownRequest = errors.New("unknown request id")

// correlation remembers, per Request ID, who to route the Reply back to: the
// original Requester (origin of the Request) and the Recipient it targeted. The
// broker records an entry when it enqueues a Request for a local Recipient and
// evicts it once the matching Reply is produced, so a request ID answers exactly
// one Reply.
type correlation struct {
	requester string
	recipient string
}

// correlations is the mutex-guarded store keyed by Request ID.
type correlations struct {
	mu sync.Mutex
	m  map[string]correlation
}

func newCorrelations() *correlations {
	return &correlations{m: make(map[string]correlation)}
}

// record maps a Request ID to its Requester and Recipient.
func (c *correlations) record(id, requester, recipient string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[id] = correlation{requester: requester, recipient: recipient}
}

// claim atomically removes and returns the correlation for a Request ID,
// reporting whether one existed. Looking up and deleting under a single lock is
// what guarantees a request ID answers exactly one Reply even under concurrent
// replies: only the first caller sees ok == true, so only one terminal Reply is
// ever built; every later caller sees the ID as already answered.
func (c *correlations) claim(id string) (correlation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	corr, ok := c.m[id]
	if ok {
		delete(c.m, id)
	}
	return corr, ok
}
