package broker

import (
	"errors"
	"sync"
	"time"
)

// Reminder budget and idle-grace window bound how long an unclaimed Correlation
// waits before the broker gives up on a voluntary Reply. They are package vars so
// tests can shrink them, mirroring the tunable bounds in the watcher package.
var (
	maxReminders = 2
	idleGrace    = 60 * time.Second
)

// action is what the Correlation lifecycle asks the caller to do for one
// observation of the Recipient's Idle state.
type action int

const (
	actionNone     action = iota // nothing to do
	actionRemind                 // inject a Reminder for this Request
	actionDiagnose               // emit a terminal Diagnostic Reply and evict
)

// diagnosed identifies a Correlation the backstop has claimed for a Diagnostic
// Reply, carrying the routing the caller needs to reach the original Requester.
type diagnosed struct {
	id        string
	requester string
	recipient string
}

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

	// Lifecycle state driving Reminder eligibility and the idle-grace backstop.
	engaged   bool      // a busy observation has been seen (Recipient engaged the Request)
	wasIdle   bool      // Idle state of the previous observation
	reminders int       // Reminders emitted so far (capped at maxReminders)
	idleSince time.Time // start of the current continuous-Idle span; zero while busy
}

// advance folds one observation of the Recipient's Idle state into the
// Correlation's lifecycle and reports the resulting action. A busy observation
// marks the Recipient engaged and clears the idle-grace timer. Before engagement
// nothing fires (never remind before the Recipient engages the Request). Once
// engaged, each busy→idle edge emits a Reminder until the budget is spent; once
// the Recipient stays continuously Idle for idleGrace with the budget spent — or
// with no further edge since the last Reminder — the backstop asks for a
// Diagnostic Reply.
func (c *correlation) advance(idle bool, now time.Time) action {
	act := actionNone
	switch {
	case !idle:
		c.engaged = true
		c.idleSince = time.Time{}
	case c.engaged:
		edge := !c.wasIdle // busy→idle transition
		if c.idleSince.IsZero() || edge {
			c.idleSince = now
		}
		switch {
		case c.reminders < maxReminders && edge:
			c.reminders++
			act = actionRemind
		case now.Sub(c.idleSince) >= idleGrace:
			act = actionDiagnose
		}
	}
	c.wasIdle = idle
	return act
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

// enforce folds one observation of a Recipient's Idle state into every unclaimed
// Correlation targeting that Recipient, under a single lock. It returns the
// Request IDs the caller must inject a Reminder for, and — for each Correlation
// the idle-grace backstop fires on — a diagnosed value with the routing to reach
// its Requester. A diagnosed Correlation is claimed (evicted) here so a request
// ID still answers exactly one Reply; the caller does the reply routing off-lock.
func (c *correlations) enforce(recipient string, idle bool, now time.Time) (remind []string, diagnose []diagnosed) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, corr := range c.m {
		if corr.recipient != recipient {
			continue
		}
		switch corr.advance(idle, now) {
		case actionRemind:
			remind = append(remind, id)
			c.m[id] = corr
		case actionDiagnose:
			diagnose = append(diagnose, diagnosed{id: id, requester: corr.requester, recipient: corr.recipient})
			delete(c.m, id)
		default:
			c.m[id] = corr
		}
	}
	return remind, diagnose
}
