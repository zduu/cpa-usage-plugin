package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestQuotaClaudeExhaustionSignals(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, utilization, status, claim, windowStatus string
		five, week                                     float64
	}{
		{"rounded", ".99", "allowed", "five_hour", "", .99, .2},
		{"five-hour rejection", ".99", "rejected", "five_hour", "", 1, .2},
		{"weekly rejection", ".99", "rejected", "seven_day", "", .99, 1},
		{"unidentified rejection", ".99", "rejected", "", "", .99, .2},
		{"window rejection", ".99", "", "", "rejected", 1, .2},
		{"rejection without utilization", "", "", "", "rejected", 1, .2},
		{"above capacity", "1.01", "", "", "", 1, .2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			s.modelPrices = map[string]ModelPrice{"m": {Prompt: 10}}
			r := quotaTestRecord("admitted", "m", now.Add(-time.Minute), 1_000_000)
			s.Record(r)
			r.RequestID, r.RequestedAt, r.Failed = "quota-response", now, true
			r.Detail = UsageDetail{}
			r.ResponseHeaders = map[string][]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization":       {tc.utilization},
				"Anthropic-Ratelimit-Unified-5h-Reset":             {now.Add(time.Hour).Format(time.RFC3339)},
				"Anthropic-Ratelimit-Unified-7d-Utilization":       {".2"},
				"Anthropic-Ratelimit-Unified-7d-Reset":             {now.Add(24 * time.Hour).Format(time.RFC3339)},
				"Anthropic-Ratelimit-Unified-Status":               {tc.status},
				"Anthropic-Ratelimit-Unified-Representative-Claim": {tc.claim},
				"Anthropic-Ratelimit-Unified-5h-Status":            {tc.windowStatus},
			}
			s.Record(r)
			current := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
			quotaModelAssert(t, current.UsedPercent, tc.five*100)
			if tc.five == 1 {
				quotaModelAssert(t, current.ActualTotalUSD, 10)
				quotaModelAssert(t, current.EstimatedRemainingUSD, 0)
			}
			groups := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now.Add(90*time.Minute)).QuotaCycles[0].Groups
			// Five-hour rollover preserves the final amount, including rounded rejection.
			five, week := groups[0].Previous, &groups[1].Current.quotaCycleDTO
			quotaModelAssert(t, five.UsedPercent, tc.five*100)
			quotaModelAssert(t, week.UsedPercent, tc.week*100)
			if tc.five == 1 {
				quotaModelAssert(t, five.ActualTotalUSD, 10)
			} else {
				quotaModelAssert(t, five.EstimatedTotalUSD, 10/tc.five)
			}
		})
	}
}

func TestQuotaExhaustionSurvivesRoundedObservations(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 10}}
	r := quotaTestRecord("full", "m", now.Add(-time.Hour), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, now.Add(-time.Hour), end, .5, 18000)
	quotaTestObserve(s, now.Add(-30*time.Minute), end, 1, 18000)
	for i := 100; i > 0; i-- {
		quotaTestObserve(s, now.Add(-time.Duration(i)*time.Second), end, .99, 18000)
	}
	c := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	quotaModelAssert(t, c.UsedPercent, 100)
	quotaModelAssert(t, c.ActualTotalUSD, 10)
	if err := validateQuotaSnapshot(s.Snapshot().QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaConsecutiveEarlyResetsKeepMostRecentSegment(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-3 * time.Hour)
	end := start.Add(5 * time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	r := quotaTestRecord("first", "m", start.Add(time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, start.Add(2*time.Minute), end, .8, 18000)
	firstCard := start.Add(time.Hour)
	quotaTestObserve(s, firstCard, end, .1, 18000)
	quotaTestObserve(s, firstCard.Add(time.Second), end, .1, 18000)
	s.Record(quotaTestRecord("second", "m", firstCard, 2_000_000))
	for i := 2; i < 100; i++ {
		quotaTestObserve(s, firstCard.Add(time.Duration(i)*time.Second), end, .8, 18000)
	}
	secondCard := firstCard.Add(time.Hour)
	quotaTestObserve(s, secondCard, end, .1, 18000)
	quotaTestObserve(s, secondCard.Add(time.Second), end, .1, 18000)
	s.Record(quotaTestRecord("third", "m", secondCard, 3_000_000))
	for i := 2; i < 100; i++ {
		quotaTestObserve(s, secondCard.Add(time.Duration(i)*time.Second), end, .2, 18000)
	}
	g := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0]
	if !g.Previous.StartAt.Equal(firstCard) || !g.Previous.EndAt.Equal(secondCard) || !g.Current.StartAt.Equal(secondCard) {
		t.Fatalf("consecutive reset boundaries changed: previous=%+v current=%+v", g.Previous, g.Current)
	}
	quotaModelAssert(t, g.Previous.Summary.CostUSD, 2)
	quotaModelAssert(t, g.Current.Summary.CostUSD, 3)
}

func TestQuotaLateDecreaseRefinesBothResetBoundaries(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-2 * time.Hour)
	end := start.Add(5 * time.Hour)
	card := start.Add(time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	r := quotaTestRecord("old", "m", start.Add(time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, start.Add(2*time.Minute), end, .8, 18000)
	s.Record(quotaTestRecord("between", "m", card.Add(-time.Minute), 2_000_000))
	quotaTestObserve(s, card, end, .1, 18000)
	quotaTestObserve(s, card.Add(time.Minute), end, .1, 18000)
	// A late response proves the reset was already observed five minutes earlier.
	quotaTestObserve(s, card.Add(-5*time.Minute), end, .1, 18000)
	g := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0]
	boundary := card.Add(-5 * time.Minute)
	if !g.Previous.EndAt.Equal(boundary) || !g.Current.StartAt.Equal(boundary) {
		t.Fatalf("refined reset split overlaps: previous=%+v current=%+v", g.Previous, g.Current)
	}
	quotaModelAssert(t, g.Previous.Summary.CostUSD, 1)
	quotaModelAssert(t, g.Current.Summary.CostUSD, 2)
}

func TestQuotaResetDeadlineDriftPreservesArchiveStart(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-2 * time.Hour)
	card := start.Add(time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	r := quotaTestRecord("old", "m", start.Add(time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, start.Add(2*time.Minute), start.Add(5*time.Hour), .8, 18000)
	quotaTestObserve(s, card.Add(time.Minute), card.Add(5*time.Hour), .1, 18000)
	quotaTestObserve(s, card.Add(2*time.Minute), card.Add(5*time.Hour+time.Second), .2, 18000)
	g := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0]
	if !g.Previous.StartAt.Equal(start) || !g.Previous.EndAt.Equal(card.Add(time.Second)) {
		t.Fatalf("deadline drift truncated reset archive: %+v", g.Previous)
	}
	quotaModelAssert(t, g.Previous.Summary.CostUSD, 1)
}

func TestQuotaEarlyResetArchiveLifecycle(t *testing.T) {
	for _, seconds := range []int64{18000, 604800} {
		for _, moved := range []bool{false, true} {
			t.Run(time.Duration(seconds*int64(time.Second)).String()+map[bool]string{false: "-decrease", true: "-deadline"}[moved], func(t *testing.T) {
				now := time.Now().UTC().Truncate(time.Second)
				elapsed := time.Hour
				if seconds == 604800 {
					elapsed = 24 * time.Hour
				}
				start := now.Add(-2 * elapsed)
				end := start.Add(time.Duration(seconds) * time.Second)
				card := start.Add(elapsed)
				s := NewRequestStatistics()
				defer s.Close()
				s.startedAt = start.Add(-time.Hour)
				s.modelPrices = map[string]ModelPrice{"old": {Prompt: 10}, "new": {Prompt: 5}}
				r := quotaTestRecord("old", "old", start.Add(time.Minute), 1_000_000)
				s.Record(r)
				quotaTestObserve(s, start.Add(2*time.Minute), end, .99, seconds)
				before := s.Snapshot()
				if moved {
					end = card.Add(time.Duration(seconds) * time.Second)
				}
				quotaTestObserve(s, card, end, .1, seconds)
				s.Record(quotaTestRecord("new", "new", card, 1_000_000))
				quotaTestObserve(s, card.Add(time.Minute), end, .2, seconds)
				// A delayed response must update the old segment without crossing the card.
				s.Record(quotaTestRecord("late-old", "old", card.Add(-time.Minute), 500_000))
				quotaTestObserve(s, card.Add(-time.Minute), start.Add(time.Duration(seconds)*time.Second), 1, seconds)
				for i := 2; i < 100; i++ {
					quotaTestObserve(s, card.Add(time.Duration(i)*time.Second), end, .2, seconds)
				}
				api := usageGroupKey(r)
				check := func(target *RequestStatistics) {
					t.Helper()
					group := target.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0]
					old, current := group.Previous, group.Current
					if old == nil || current == nil || !old.EndedByReset || !old.StartAt.Equal(start) || !old.EndAt.Equal(card) || !current.StartAt.Equal(card) || old.ResetTimeEstimated == moved {
						t.Fatalf("early reset boundaries lost: %+v", group)
					}
					if old.Summary == nil || old.Summary.TotalRequests != 2 || current.Summary.TotalRequests != 1 || old.ModelStats[0].Model != "old" || current.ModelStats[0].Model != "new" {
						t.Fatalf("early reset accounting mixed: old=%+v current=%+v", old, current)
					}
					quotaModelAssert(t, old.ActualTotalUSD, 15)
					quotaModelAssert(t, current.EstimatedTotalUSD, 25)
				}
				check(s)
				backup := s.Snapshot()
				raw, err := json.Marshal(backup)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &backup); err != nil {
					t.Fatal(err)
				}
				if err := validateQuotaSnapshot(backup.QuotaCycles, nil); err != nil {
					t.Fatal(err)
				}
				for _, mode := range []string{"restore", "merge old first", "merge old last", "journal", "legacy"} {
					t.Run(mode, func(t *testing.T) {
						dest := NewRequestStatistics()
						defer dest.Close()
						// Retained quota facts and observations do not depend on main ledger retention.
						dest.modelPrices = s.modelPrices
						switch mode {
						case "restore":
							dest.restoreQuotaSnapshotLocked(backup)
						case "legacy":
							legacy := cloneQuotaSnapshot(backup.QuotaCycles)
							for key, w := range legacy.Windows {
								w.Previous = nil
								legacy.Windows[key] = w
							}
							dest.restoreQuotaSnapshotLocked(StatisticsSnapshot{QuotaCycles: legacy})
						case "journal":
							if err := dest.replayPersistedDetailsLocked(quotaStorageRecords(backup.QuotaCycles), 0, false, now); err != nil {
								t.Fatal(err)
							}
						default:
							first, second := before.QuotaCycles, backup.QuotaCycles
							if mode == "merge old last" {
								first, second = second, first
							}
							dest.mergeQuotaSnapshotLocked(first)
							dest.mergeQuotaSnapshotLocked(second)
							dest.mergeQuotaSnapshotLocked(backup.QuotaCycles)
						}
						check(dest)
						if err := validateQuotaSnapshot(dest.Snapshot().QuotaCycles, nil); err != nil {
							t.Fatal(err)
						}
						group := dest.QueryAPIDetailAt(api, "all", 10, 10, end).QuotaCycles[0].Groups[0]
						if group.Current != nil || group.Previous == nil || group.Previous.EndedByReset || !group.Previous.StartAt.Equal(card) {
							t.Fatalf("natural rollover lost latest segment: %+v", group)
						}
					})
				}
			})
		}
	}
}

func TestQuotaUnconfirmedResetAfterExhaustionShowsLatest(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(2 * time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 10}}
	r := quotaTestRecord("full", "m", now.Add(-2*time.Hour), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, now.Add(-2*time.Hour), end, .5, 18000)
	quotaTestObserve(s, now.Add(-time.Hour), end, 1, 18000)
	// A card that keeps the deadline is confirmed only by the next sample.
	quotaTestObserve(s, now.Add(-time.Minute), end, .05, 18000)
	c := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	quotaModelAssert(t, c.UsedPercent, 5)
}

func TestQuotaLateDecreaseAfterConsecutiveResetsKeepsArchiveStart(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-3 * time.Hour)
	end := start.Add(5 * time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	r := quotaTestRecord("first", "m", start.Add(time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, start.Add(2*time.Minute), end, .8, 18000)
	firstCard := start.Add(time.Hour)
	quotaTestObserve(s, firstCard, end, .1, 18000)
	quotaTestObserve(s, firstCard.Add(time.Second), end, .1, 18000)
	s.Record(quotaTestRecord("second", "m", firstCard, 2_000_000))
	for i := 2; i < 100; i++ {
		quotaTestObserve(s, firstCard.Add(time.Duration(i)*time.Second), end, .8, 18000)
	}
	secondCard := firstCard.Add(time.Hour)
	quotaTestObserve(s, secondCard, end, .1, 18000)
	quotaTestObserve(s, secondCard.Add(time.Second), end, .1, 18000)
	s.Record(quotaTestRecord("third", "m", secondCard, 3_000_000))
	for i := 2; i < 100; i++ {
		quotaTestObserve(s, secondCard.Add(time.Duration(i)*time.Second), end, .2, 18000)
	}
	// Current has compacted the first reset; only the archive still proves it.
	boundary := secondCard.Add(-5 * time.Minute)
	quotaTestObserve(s, boundary, end, .1, 18000)
	g := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0]
	if !g.Previous.StartAt.Equal(firstCard) || !g.Previous.EndAt.Equal(boundary) || !g.Current.StartAt.Equal(boundary) {
		t.Fatalf("late decrease moved archive start: previous=%+v current=%+v", g.Previous, g.Current)
	}
	quotaModelAssert(t, g.Previous.Summary.CostUSD, 2)
	quotaModelAssert(t, g.Current.Summary.CostUSD, 3)
	if err := validateQuotaSnapshot(s.Snapshot().QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaMergeSkipsArchiveEmptiedByAlignment(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	w := quotaWindow{Seconds: 18000, Current: &quotaPeriod{Start: start, End: start.Add(5 * time.Hour)}}
	in := quotaWindow{Seconds: 18000, Previous: &quotaPeriod{EndedByReset: true, Start: start.Add(10 * time.Second), End: start.Add(30 * time.Second)}}
	mergeQuotaWindowHistory(&w, in)
	if w.Previous != nil && !w.Previous.Start.Before(w.Previous.End) {
		t.Fatalf("merge installed inverted period: %+v", w.Previous)
	}
}

func TestQuotaLateArchiveSampleSkipsLaterWindow(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-4 * time.Hour)
	e1 := start.Add(5 * time.Hour)
	card := start.Add(time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	r := quotaTestRecord("old", "m", start.Add(time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, start.Add(2*time.Minute), e1, .8, 18000)
	quotaTestObserve(s, card, e1, .1, 18000)
	quotaTestObserve(s, card.Add(time.Minute), e1, .2, 18000)
	s.mu.Lock()
	for key, w := range s.quota.Windows {
		if w.Previous == nil || !w.Previous.EndedByReset {
			s.mu.Unlock()
			t.Fatalf("no archive: %+v", w.Previous)
		}
		o := w.Current.Samples[len(w.Current.Samples)-1]
		o.ObservedAt, o.Reset, o.Used = e1.Add(time.Minute), e1.Add(5*time.Hour), .3
		w.Current = &quotaPeriod{Start: e1, End: e1.Add(5 * time.Hour), Samples: []quotaObservation{o}}
		w.UpdatedAt = o.ObservedAt
		s.quota.Windows[key] = w
	}
	s.mu.Unlock()
	// A backup merge can pair the archive with a later natural window.
	quotaTestObserve(s, card.Add(-time.Minute), e1, .9, 18000)
	g := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0]
	if !g.Previous.EndAt.Equal(card) || !g.Current.StartAt.Equal(e1) || g.Current.ResetBaselineUsedPercent != nil {
		t.Fatal("late archive sample corrupted the next window")
	}
}

func TestQuotaInvalidPeriodDoesNotRejectStorageSnapshot(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-2 * time.Hour)
	card := start.Add(time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	r := quotaTestRecord("old", "m", start.Add(time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, start.Add(2*time.Minute), start.Add(5*time.Hour), .8, 18000)
	quotaTestObserve(s, card, start.Add(5*time.Hour), .1, 18000)
	quotaTestObserve(s, card.Add(time.Minute), start.Add(5*time.Hour), .2, 18000)
	usage := s.Snapshot()
	usage.QuotaCycles = cloneQuotaSnapshot(usage.QuotaCycles)
	for key, w := range usage.QuotaCycles.Windows {
		if w.Current == nil || w.Previous == nil {
			t.Fatalf("missing periods: %+v", w)
		}
		// A malformed current period must cost only that period.
		w.Current.Start = w.Current.Start.Add(time.Second)
		usage.QuotaCycles.Windows[key] = w
	}
	if err := validateQuotaSnapshot(usage.QuotaCycles, nil); err == nil {
		t.Fatal("corrupted period passed validation")
	}
	payload, err := json.Marshal(persistedStorageSnapshot{Version: currentStorageSnapshotVersion, GeneratedAt: now.Format(time.RFC3339Nano), Usage: usage})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(storageSnapshotPath(dir), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := NewRequestStatistics()
	defer dest.Close()
	dest.mu.Lock()
	_, err = dest.loadStorageSnapshotLocked(dir, now)
	dest.mu.Unlock()
	if err != nil {
		t.Fatalf("one invalid quota period rejected the snapshot: %v", err)
	}
	restored := dest.Snapshot()
	if restored.TotalRequests != 1 || len(restored.QuotaCycles.Facts) != 1 {
		t.Fatalf("ledger or quota facts lost: requests=%d facts=%d", restored.TotalRequests, len(restored.QuotaCycles.Facts))
	}
	for _, w := range restored.QuotaCycles.Windows {
		if w.Current != nil || w.Previous == nil || !w.Previous.EndedByReset {
			t.Fatalf("expected only the invalid period dropped: %+v", w)
		}
	}
	if err := validateQuotaSnapshot(restored.QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
	// Importing the same backup also succeeds instead of failing the request.
	imported := NewRequestStatistics()
	defer imported.Close()
	if _, err := imported.mergeSnapshotChecked(usage); err != nil {
		t.Fatalf("import rejected backup with one invalid period: %v", err)
	}
}

func TestQuotaOversizedLegacyPeriodIsCompactedOnLoad(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(6 * 24 * time.Hour)
	r := quotaTestRecord("r", "m", now.Add(-time.Hour), 1_000)
	s.Record(r)
	for i := 100; i > 0; i-- {
		quotaTestObserve(s, now.Add(-time.Duration(i)*time.Minute), end, .2, 604800)
	}
	usage := s.Snapshot()
	usage.QuotaCycles = cloneQuotaSnapshot(usage.QuotaCycles)
	for key, w := range usage.QuotaCycles.Windows {
		// v2.7.1 kept the last 60 samples plus up to five anchors: 65 in all.
		extra := w.Current.Samples[0]
		extra.ObservedAt = extra.ObservedAt.Add(-time.Hour)
		w.Current.Samples = append([]quotaObservation{extra}, w.Current.Samples...)
		usage.QuotaCycles.Windows[key] = w
	}
	if err := validateQuotaSnapshot(usage.QuotaCycles, nil); err == nil {
		t.Fatal("65-sample period passed validation")
	}
	payload, err := json.Marshal(persistedStorageSnapshot{Version: currentStorageSnapshotVersion, GeneratedAt: now.Format(time.RFC3339Nano), Usage: usage})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(storageSnapshotPath(dir), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := NewRequestStatistics()
	defer dest.Close()
	dest.mu.Lock()
	_, err = dest.loadStorageSnapshotLocked(dir, now)
	dest.mu.Unlock()
	if err != nil {
		t.Fatalf("legacy 65-sample period rejected the snapshot: %v", err)
	}
	for _, w := range dest.Snapshot().QuotaCycles.Windows {
		if w.Current == nil || len(w.Current.Samples) != 64 {
			t.Fatalf("expected the period kept and compacted to 64 samples: %+v", w.Current)
		}
	}
}
