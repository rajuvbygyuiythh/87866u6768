package dash

import (
	"testing"

	"thundr/internal/bot"
)

// Session counters must accumulate per-worker DELTAS (no double counting on
// repeat snapshots) and reset on a fresh App (in-memory only).
func TestSessionStatsAccumulate(t *testing.T) {
	a := Open(t.TempDir())

	m, r, sn, tg := 0, 0, 0, 0
	liveBots.Lock()
	liveBots.m["u1"] = func() bot.Status {
		return bot.Status{Matches: m, Replies: r, SnapShares: sn, TelegramShares: tg}
	}
	liveBots.Unlock()
	defer func() { liveBots.Lock(); delete(liveBots.m, "u1"); liveBots.Unlock() }()

	m, r, sn, tg = 2, 5, 1, 1
	s1 := a.GetSnapshot()
	if s1.Stats.Chats != 2 || s1.Stats.Replies != 5 || s1.Stats.SnapShares != 1 || s1.Stats.TelegramShares != 1 {
		t.Fatalf("first accumulation wrong: %+v", s1.Stats)
	}

	// same snapshot again: deltas must NOT double count
	if s2 := a.GetSnapshot(); s2.Stats.Chats != 2 || s2.Stats.Replies != 5 {
		t.Fatalf("double counted: %+v", s2.Stats)
	}

	// worker counters advance: only the delta is added
	m, r, sn, tg = 4, 9, 2, 3
	s3 := a.GetSnapshot()
	if s3.Stats.Chats != 4 || s3.Stats.Replies != 9 || s3.Stats.SnapShares != 2 || s3.Stats.TelegramShares != 3 {
		t.Fatalf("delta accumulation wrong: %+v", s3.Stats)
	}

	// a fresh App starts from 0 (dashboard reopen semantics)
	a2 := Open(t.TempDir())
	m, r, sn, tg = 4, 9, 2, 3 // worker still running with same totals
	s4 := a2.GetSnapshot()
	if s4.Stats.Chats != 4 { // first sight of the worker = full delta from 0
		t.Fatalf("fresh app should count from 0, got %+v", s4.Stats)
	}
}
