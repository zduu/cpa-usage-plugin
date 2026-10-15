package main

import (
	"testing"
	"time"
)

// An Antigravity pool that was never charged reports its deadline as
// "now + window" on every query. Each observation therefore moves the deadline
// by about the elapsed time even though nothing was consumed. Once the slide
// passes the drift threshold of window/24, the panel read that rolling deadline
// as a reset card, archived an empty previous period and restarted the current
// one. The live credential showed exactly this: a previous 5-hour period with a
// single zero-usage observation.
func TestUnusedAntigravityPoolDoesNotArchiveRollingDeadline(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	s.startedAt = now.Add(-time.Hour)
	const window = int64(18000)
	observe := func(at time.Time) {
		t.Helper()
		o := quotaObservation{Provider: "antigravity", AuthIndex: "ag-index", AuthID: "antigravity.json",
			Group: "group", Name: "Claude and GPT models", Slot: "slot", Seconds: window,
			// The deadline keeps sliding forward while usage stays at zero.
			Reset: at.Add(time.Duration(window) * time.Second), ObservedAt: at, Used: 0, Unmapped: true}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.applyQuotaObservationLocked(o)
	}
	// 25 minutes apart, which is the spacing in the reported snapshot and well
	// past the 12.5-minute drift threshold for a five-hour window.
	for i := 0; i < 6; i++ {
		observe(now.Add(time.Duration(i) * 25 * time.Minute))
	}
	var w quotaWindow
	for _, candidate := range s.quota.Windows {
		w = candidate
	}
	if w.Previous != nil {
		t.Fatalf("rolling unused deadline produced a previous period: %+v", w.Previous)
	}
	if w.Current == nil || len(w.Current.Samples) != 6 || w.Current.EndedByReset {
		t.Fatalf("rolling unused deadline disturbed the current period: %+v", w.Current)
	}
	if !w.Current.End.Equal(now.Add(125*time.Minute + time.Duration(window)*time.Second)) {
		t.Fatalf("current deadline was not advanced: %v", w.Current.End)
	}
}

// A pool that really was charged and then reset still archives its consumed
// segment, so the guard must not discard genuine history.
func TestConsumedPoolStillArchivesResetPeriod(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	const window = int64(18000)
	reset := now.Add(time.Duration(window) * time.Second)
	observe := func(at, deadline time.Time, used float64) {
		t.Helper()
		o := quotaObservation{Provider: "antigravity", AuthIndex: "ag-index", AuthID: "antigravity.json",
			Group: "group", Name: "Gemini Models", Slot: "slot", Seconds: window,
			Reset: deadline, ObservedAt: at, Used: used, Unmapped: true}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.applyQuotaObservationLocked(o)
	}
	observe(now, reset, .4)
	// A reset card restarts the window: the deadline jumps a whole window out
	// from the card, which is far beyond the drift threshold, and usage drops
	// back to zero.
	card := now.Add(time.Hour)
	newReset := card.Add(time.Duration(window) * time.Second)
	observe(card, newReset, 0)
	for i := 1; i < 5; i++ {
		observe(card.Add(time.Duration(i)*time.Second), newReset, .05)
	}
	var w quotaWindow
	for _, candidate := range s.quota.Windows {
		w = candidate
	}
	if w.Previous == nil || !w.Previous.EndedByReset {
		t.Fatalf("consumed reset segment was not archived: %+v", w.Previous)
	}
	if !quotaSamplesConsumed(w.Previous.Samples) {
		t.Fatalf("archived period lost its consumed observations: %+v", w.Previous)
	}
	if w.Previous.Samples[0].Used != .4 {
		t.Fatalf("archived the wrong observations: %+v", w.Previous.Samples)
	}
	// A non-nil baseline is what the panel reports as the detected reset, so the
	// consumed case must still produce one.
	if _, baseline := quotaEffectivePeriod(w.Current); baseline == nil {
		t.Fatal("reset baseline missing for a consumed reset segment")
	}
}

// Snapshots written while the bug was live still carry the empty archive, so
// loading one has to clear it instead of rendering it forever.
func TestStoredEmptyResetArchiveIsClearedOnLoad(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	empty := &quotaSnapshot{Version: 1, StartedAt: start,
		Facts:   map[string]quotaFact{},
		Deleted: map[string]time.Time{},
		Windows: map[string]quotaWindow{}}
	o := quotaObservation{Provider: "antigravity", AuthIndex: "ag-index", AuthID: "antigravity.json",
		Group: "group", Slot: "slot", Seconds: 604800, Unmapped: true}
	empty.Windows[quotaWindowKey(o)] = quotaWindow{
		Provider: "antigravity", AuthIndex: "ag-index", AuthID: "antigravity.json", Group: "group", Slot: "slot",
		Seconds: 604800, Unmapped: true, UpdatedAt: now,
		Previous: &quotaPeriod{Start: start.Add(-time.Hour), End: start, EndedByReset: true, ResetTimeEstimated: true,
			Samples: []quotaObservation{{Provider: "antigravity", AuthIndex: "ag-index", AuthID: "antigravity.json",
				Group: "group", Slot: "slot", Seconds: 604800, Reset: start.Add(604800*time.Second - time.Hour),
				ObservedAt: start.Add(-time.Hour), Used: 0, Unmapped: true}}},
	}
	s := NewRequestStatistics()
	defer s.Close()
	if err := validateQuotaSnapshot(empty, nil); err != nil {
		t.Fatalf("fixture is not a valid snapshot: %v", err)
	}
	restored := repairQuotaPeriods(empty)
	for _, w := range restored.Windows {
		if w.Previous != nil {
			t.Fatalf("stored empty reset archive survived the load: %+v", w.Previous)
		}
	}
	if empty.Windows[quotaWindowKey(o)].Previous == nil {
		t.Fatal("repair mutated the caller's snapshot")
	}
}

func TestConsumedResetHistorySurvivesIdleRollingPollsAndRestore(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	first := now.Add(-4 * time.Hour)
	card := first.Add(time.Hour)
	observe := func(at time.Time, used float64) {
		t.Helper()
		o := quotaObservation{Provider: "antigravity", AuthIndex: "ag", AuthID: "ag.json", Group: "g", Slot: "s", Seconds: 18000, Reset: at.Add(5 * time.Hour), ObservedAt: at, Used: used, Unmapped: true}
		if !s.applyQuotaObservationLocked(o) {
			t.Fatalf("observation rejected: %+v", o)
		}
	}
	observe(first, .4)
	observe(card, 0)
	check := func(target *RequestStatistics) {
		t.Helper()
		for _, w := range target.quota.Windows {
			if w.Previous == nil || !w.Previous.End.Equal(card) || !quotaSamplesConsumed(w.Previous.Samples) {
				t.Fatalf("idle deadline erased consumed history: %+v", w.Previous)
			}
			current, baseline := quotaEffectivePeriod(w.Current)
			if !current.Start.Equal(card) || baseline == nil || *baseline != 0 {
				t.Fatalf("idle poll moved the real reset: %+v baseline=%v", current, baseline)
			}
		}
	}
	check(s)
	beforeIdle := s.Snapshot()
	for i := 1; i <= 100; i++ {
		observe(card.Add(time.Duration(i)*time.Minute), 0)
		check(s)
	}
	// Delayed observations refine the consumed archive without reviving a
	// fictitious later reset, even after the current samples were compacted.
	observe(first.Add(time.Minute), .5)
	check(s)
	backup := s.Snapshot()
	if err := validateQuotaSnapshot(backup.QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
	for _, oldFirst := range []bool{false, true} {
		dest := NewRequestStatistics()
		if oldFirst {
			dest.MergeSnapshot(beforeIdle)
		}
		dest.MergeSnapshot(backup)
		if !oldFirst {
			dest.MergeSnapshot(beforeIdle)
		}
		check(dest)
		dest.Close()
	}
	// Older builds could already have erased Previous. The retained reset
	// observations must recover it on load, without waiting for another poll.
	legacy := cloneQuotaSnapshot(backup.QuotaCycles)
	for key, w := range legacy.Windows {
		w.Previous = nil
		legacy.Windows[key] = w
	}
	dest := NewRequestStatistics()
	defer dest.Close()
	dest.restoreQuotaSnapshotLocked(StatisticsSnapshot{QuotaCycles: legacy})
	dest.pruneQuotaLocked(now) // The first dashboard read advances restored history.
	check(dest)
	// Resuming consumption can still establish a genuine subsequent reset.
	observe(card.Add(110*time.Minute), .3)
	secondCard := card.Add(150 * time.Minute)
	observe(secondCard, 0)
	for _, w := range s.quota.Windows {
		current, _ := quotaEffectivePeriod(w.Current)
		if w.Previous == nil || !w.Previous.Start.Equal(card) || !w.Previous.End.Equal(secondCard) || !current.Start.Equal(secondCard) {
			t.Fatalf("subsequent consumed reset was lost: previous=%+v current=%+v", w.Previous, current)
		}
	}
}
