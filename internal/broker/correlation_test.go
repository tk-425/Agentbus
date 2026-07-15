package broker

import (
	"testing"
	"time"
)

// drive feeds a sequence of (idle, now) observations to a single correlation and
// returns the action produced on each observation, so a test can assert the
// lifecycle a scripted Recipient walks through.
func drive(c *correlation, obs ...struct {
	idle bool
	now  time.Time
}) []action {
	got := make([]action, len(obs))
	for i, o := range obs {
		got[i] = c.advance(o.idle, o.now)
	}
	return got
}

func obs(idle bool, now time.Time) struct {
	idle bool
	now  time.Time
} {
	return struct {
		idle bool
		now  time.Time
	}{idle: idle, now: now}
}

func TestCorrelationRemindsOnFirstIdleEdge(t *testing.T) {
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	// Idle before the Recipient ever engages the Request must not remind.
	got := drive(c,
		obs(true, base),         // idle, not engaged yet -> none
		obs(false, base.Add(1)), // busy -> engaged
		obs(true, base.Add(2)),  // busy->idle edge -> remind
	)

	want := []action{actionNone, actionNone, actionRemind}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation %d: got %v, want %v", i, got[i], want[i])
		}
	}
	if c.reminders != 1 {
		t.Fatalf("reminders = %d, want 1", c.reminders)
	}
}

// setBounds shrinks the package Reminder/idle-grace bounds for a test and
// restores them afterward.
func setBounds(t *testing.T, reminders int, grace time.Duration) {
	t.Helper()
	origR, origG := maxReminders, idleGrace
	maxReminders, idleGrace = reminders, grace
	t.Cleanup(func() { maxReminders, idleGrace = origR, origG })
}

func TestCorrelationCapsRemindersAtBudget(t *testing.T) {
	setBounds(t, 2, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	got := drive(c,
		obs(false, base.Add(1)), // engaged
		obs(true, base.Add(2)),  // edge 1 -> remind
		obs(false, base.Add(3)), // busy again
		obs(true, base.Add(4)),  // edge 2 -> remind
		obs(false, base.Add(5)), // busy again
		obs(true, base.Add(6)),  // edge 3 -> budget spent, not yet past grace -> none
	)

	want := []action{actionNone, actionRemind, actionNone, actionRemind, actionNone, actionNone}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation %d: got %v, want %v", i, got[i], want[i])
		}
	}
	if c.reminders != 2 {
		t.Fatalf("reminders = %d, want 2 (capped)", c.reminders)
	}
}

func TestCorrelationNoDiagnoseWhileBusyOrBeforeGrace(t *testing.T) {
	setBounds(t, 2, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	got := drive(c,
		obs(false, base.Add(1)),             // engaged
		obs(true, base.Add(2)),              // edge -> remind (idleSince = +2)
		obs(false, base.Add(3*time.Second)), // busy clears idleSince -> none
		obs(true, base.Add(4*time.Second)),  // edge -> remind (budget now spent)
		obs(true, base.Add(30*time.Second)), // idle, only 26s of grace -> none
	)

	want := []action{actionNone, actionRemind, actionNone, actionRemind, actionNone}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestCorrelationDiagnosesAfterBudgetSpentAndGrace(t *testing.T) {
	setBounds(t, 2, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	drive(c,
		obs(false, base.Add(1)), // engaged
		obs(true, base.Add(2)),  // edge 1 -> remind
		obs(false, base.Add(3)), // busy
		obs(true, base.Add(4)),  // edge 2 -> remind (budget spent, idleSince = +4ns)
	)
	// Stay continuously Idle past the grace window -> exactly one diagnose.
	if a := c.advance(true, base.Add(4+61*time.Second)); a != actionDiagnose {
		t.Fatalf("past grace with budget spent: got %v, want actionDiagnose", a)
	}
}

// A hung-but-Idle Recipient that engages once, gets one Reminder, then never
// produces a second busy→idle edge must still terminate: after the idle-grace
// window with no edge since the last Reminder, the backstop fires once.
func TestCorrelationDiagnosesHungIdleRecipient(t *testing.T) {
	setBounds(t, 2, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	got := drive(c,
		obs(false, base.Add(1)),             // engaged
		obs(true, base.Add(2)),              // edge -> remind (only one ever)
		obs(true, base.Add(30*time.Second)), // still idle, no edge, within grace -> none
		obs(true, base.Add(90*time.Second)), // past grace, budget remaining but no edge -> diagnose
	)

	want := []action{actionNone, actionRemind, actionNone, actionDiagnose}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestEnforceFiltersByRecipientAndClaimsDiagnosed(t *testing.T) {
	setBounds(t, 2, 60*time.Second)
	base := time.Unix(0, 0)
	cs := newCorrelations()
	cs.record("req-a", "claude-1", "codex-1")
	cs.record("req-b", "claude-2", "codex-1")
	cs.record("other", "claude-3", "gemini-1") // different Recipient, untouched

	// Engage both codex-1 Correlations, then drive one busy→idle edge.
	cs.enforce("codex-1", false, base.Add(1))
	remind, diagnose := cs.enforce("codex-1", true, base.Add(2))

	if len(diagnose) != 0 {
		t.Fatalf("no diagnose expected on first edge, got %d", len(diagnose))
	}
	if len(remind) != 2 {
		t.Fatalf("remind = %d ids, want 2 (both codex-1 Correlations)", len(remind))
	}

	// Spend the budget and cross the grace window.
	cs.enforce("codex-1", false, base.Add(3))
	cs.enforce("codex-1", true, base.Add(4)) // edge 2 -> remind, budget spent
	_, diagnose = cs.enforce("codex-1", true, base.Add(4+61*time.Second))

	if len(diagnose) != 2 {
		t.Fatalf("diagnose = %d, want 2 backstopped Correlations", len(diagnose))
	}
	// Diagnosed Correlations are evicted: a later claim finds nothing.
	if _, ok := cs.claim(diagnose[0].id); ok {
		t.Fatalf("diagnosed Correlation %q should already be evicted", diagnose[0].id)
	}
	// The unrelated Recipient's Correlation is still recorded.
	if _, ok := cs.claim("other"); !ok {
		t.Fatalf("gemini-1 Correlation should be untouched by codex-1 enforcement")
	}
}
