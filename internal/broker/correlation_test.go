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

func TestCorrelationRemindsAfterReplyGrace(t *testing.T) {
	setBounds(t, 2, 15*time.Second, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	got := drive(c,
		obs(true, base),                     // idle before engagement -> none
		obs(false, base.Add(1)),             // busy -> engaged
		obs(true, base.Add(2)),              // continuous Idle begins; within reply-grace -> none
		obs(true, base.Add(10*time.Second)), // still within reply-grace -> none
		obs(true, base.Add(20*time.Second)), // >= replyGrace of unbroken Idle -> remind
	)

	want := []action{actionNone, actionNone, actionNone, actionNone, actionRemind}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation %d: got %v, want %v", i, got[i], want[i])
		}
	}
	if c.reminders != 1 {
		t.Fatalf("reminders = %d, want 1", c.reminders)
	}
}

// TestCorrelationNoRemindWithinReplyGrace is the regression for the live
// reminder-vs-reply race: a Recipient that has just gone Idle needs a moment to
// run its own `agentbus reply`. Reminding on that first Idle observation races
// the reply and produces a stale, post-reply Reminder.
func TestCorrelationNoRemindWithinReplyGrace(t *testing.T) {
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}
	c.advance(false, base.Add(1)) // engaged
	if a := c.advance(true, base.Add(2)); a == actionRemind {
		t.Fatalf("must not remind immediately on going Idle (no reply-grace); got %v", a)
	}
}

// setBounds shrinks the package Reminder budget and the reply-grace / idle-grace
// windows for a test and restores them afterward.
func setBounds(t *testing.T, reminders int, reply, idle time.Duration) {
	t.Helper()
	origR, origReply, origIdle := maxReminders, replyGrace, idleGrace
	maxReminders, replyGrace, idleGrace = reminders, reply, idle
	t.Cleanup(func() { maxReminders, replyGrace, idleGrace = origR, origReply, origIdle })
}

func TestCorrelationCapsRemindersAtBudget(t *testing.T) {
	setBounds(t, 2, 15*time.Second, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	got := drive(c,
		obs(false, base.Add(1)),             // engaged
		obs(true, base.Add(2)),              // Idle begins
		obs(true, base.Add(20*time.Second)), // >= replyGrace -> remind 1 (window restarts)
		obs(true, base.Add(40*time.Second)), // >= replyGrace again -> remind 2
		obs(true, base.Add(55*time.Second)), // budget spent, < idleGrace since remind 2 -> none
	)

	want := []action{actionNone, actionNone, actionRemind, actionRemind, actionNone}
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
	setBounds(t, 2, 15*time.Second, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	// Spend the budget across two reply-grace windows (idleSince ends at +40s).
	drive(c,
		obs(false, base.Add(1)),             // engaged
		obs(true, base.Add(2)),              // Idle begins
		obs(true, base.Add(20*time.Second)), // remind 1
		obs(true, base.Add(40*time.Second)), // remind 2, budget spent
	)
	// A busy observation resets the backstop clock — never diagnose while busy.
	if a := c.advance(false, base.Add(50*time.Second)); a != actionNone {
		t.Fatalf("busy must not diagnose, got %v", a)
	}
	// Re-Idle, but not yet idleGrace of unbroken Idle -> none.
	if a := c.advance(true, base.Add(51*time.Second)); a != actionNone {
		t.Fatalf("just re-idled, got %v", a)
	}
	if a := c.advance(true, base.Add(80*time.Second)); a != actionNone {
		t.Fatalf("only 29s of idle-grace elapsed, got %v", a)
	}
}

func TestCorrelationDiagnosesAfterBudgetSpentAndGrace(t *testing.T) {
	setBounds(t, 2, 15*time.Second, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	drive(c,
		obs(false, base.Add(1)),             // engaged
		obs(true, base.Add(2)),              // Idle begins
		obs(true, base.Add(20*time.Second)), // remind 1
		obs(true, base.Add(40*time.Second)), // remind 2, budget spent (idleSince = +40s)
	)
	// Stay continuously Idle past idleGrace since the last Reminder -> one diagnose.
	if a := c.advance(true, base.Add(40*time.Second+61*time.Second)); a != actionDiagnose {
		t.Fatalf("past idle-grace with budget spent: got %v, want actionDiagnose", a)
	}
}

// A hung-but-Idle Recipient that engages once then stays Idle forever still
// terminates: continuous-Idle time alone accrues both Reminders and then the
// Diagnostic — no busy→idle edge is required.
func TestCorrelationDiagnosesHungIdleRecipient(t *testing.T) {
	setBounds(t, 2, 15*time.Second, 60*time.Second)
	base := time.Unix(0, 0)
	c := &correlation{requester: "claude-1", recipient: "codex-1"}

	got := drive(c,
		obs(false, base.Add(1)),              // engaged (only busy observation ever)
		obs(true, base.Add(2)),               // Idle begins and never breaks
		obs(true, base.Add(20*time.Second)),  // remind 1
		obs(true, base.Add(40*time.Second)),  // remind 2 (budget spent)
		obs(true, base.Add(101*time.Second)), // >= idleGrace since remind 2 -> diagnose
	)

	want := []action{actionNone, actionNone, actionRemind, actionRemind, actionDiagnose}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestEnforceFiltersByRecipientAndClaimsDiagnosed(t *testing.T) {
	setBounds(t, 2, 15*time.Second, 60*time.Second)
	base := time.Unix(0, 0)
	cs := newCorrelations()
	cs.record("req-a", "claude-1", "codex-1")
	cs.record("req-b", "claude-2", "codex-1")
	cs.record("other", "claude-3", "gemini-1") // different Recipient, untouched

	// Engage both codex-1 Correlations, begin the Idle span, then cross reply-grace.
	cs.enforce("codex-1", false, base.Add(1))
	cs.enforce("codex-1", true, base.Add(2))
	remind, diagnose := cs.enforce("codex-1", true, base.Add(20*time.Second))

	if len(diagnose) != 0 {
		t.Fatalf("no diagnose expected while budget remains, got %d", len(diagnose))
	}
	if len(remind) != 2 {
		t.Fatalf("remind = %d ids, want 2 (both codex-1 Correlations)", len(remind))
	}

	// Spend the budget (second reply-grace window), then cross the idle-grace.
	cs.enforce("codex-1", true, base.Add(40*time.Second)) // remind 2, budget spent
	_, diagnose = cs.enforce("codex-1", true, base.Add(40*time.Second+61*time.Second))

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

// TestEnforceNoReminderWhenReplyLandsWithinGrace is the enforce-level regression
// for the reminder-vs-reply race: a Recipient that runs its own `agentbus reply`
// within the reply-grace window claims (evicts) the Correlation, so a later
// enforcement pass finds nothing and never injects a stale, post-reply Reminder.
func TestEnforceNoReminderWhenReplyLandsWithinGrace(t *testing.T) {
	setBounds(t, 2, 15*time.Second, 60*time.Second)
	base := time.Unix(0, 0)
	cs := newCorrelations()
	cs.record("req-1", "claude-1", "codex-1")

	cs.enforce("codex-1", false, base.Add(1))             // engaged (busy)
	remind, _ := cs.enforce("codex-1", true, base.Add(2)) // Idle begins, within reply-grace
	if len(remind) != 0 {
		t.Fatalf("no Reminder within reply-grace, got %v", remind)
	}

	// The Recipient replies within the grace window -> Correlation claimed.
	if _, ok := cs.claim("req-1"); !ok {
		t.Fatal("expected to claim the recorded Correlation")
	}

	// A later pass past both grace windows must find nothing to remind or diagnose.
	remind, diagnose := cs.enforce("codex-1", true, base.Add(120*time.Second))
	if len(remind) != 0 || len(diagnose) != 0 {
		t.Fatalf("claimed Correlation must yield no Reminder/Diagnostic, got remind=%v diagnose=%d", remind, len(diagnose))
	}
}
