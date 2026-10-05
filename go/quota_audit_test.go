package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestQuotaLiveExecutionsWithReusedRequestIDSurviveBackup(t *testing.T) {
	for _, identical := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "identical"}[identical], func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			first := quotaTestRecord("reused-host-request", "m", now.Add(-time.Minute), 100)
			second := first
			if !identical {
				first.Failed = true
				second.RequestedAt = first.RequestedAt.Add(time.Second)
				second.Detail.InputTokens, second.Detail.TotalTokens = 200, 200
			}
			s.Record(first)
			s.Record(second)
			if s.totalRequests != 2 || len(s.quota.Facts) != 2 {
				t.Fatalf("live ledgers diverged: requests=%d quota facts=%d", s.totalRequests, len(s.quota.Facts))
			}
			backup := s.Snapshot()
			if err := validateQuotaImport(backup, nil, false); err != nil {
				t.Fatalf("plugin rejected its own backup: %v", err)
			}
			restored := NewRequestStatistics()
			defer restored.Close()
			for range 2 {
				if _, err := restored.mergeSnapshotChecked(backup); err != nil {
					t.Fatal(err)
				}
			}
			if restored.totalRequests != 2 || len(restored.quota.Facts) != 2 || restored.totalTokens != s.totalTokens {
				t.Fatal("backup lost or duplicated a live execution")
			}
			if !s.RemoveRecordedUsage(first) || s.totalRequests != 1 || len(s.quota.Facts) != 1 {
				t.Fatal("removing one execution changed another execution's quota fact")
			}
			s.Record(first)
			if s.totalRequests != 2 || len(s.quota.Facts) != 2 {
				t.Fatal("an old deletion suppressed a new execution with a reused host ID")
			}
		})
	}
}

func TestQuotaNegativeNativeTokenFieldsDoNotInvalidateBackup(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	r := quotaTestRecord("negative-tokens", "m", time.Now().UTC().Add(-time.Minute), 10)
	r.Detail.InputTokens, r.Detail.OutputTokens, r.Detail.ReasoningTokens = -1, -2, -3
	s.Record(r)
	backup := s.Snapshot()
	if err := validateQuotaImport(backup, nil, false); err != nil {
		t.Fatalf("native token counters made the plugin's own backup invalid: %v", err)
	}
	for _, f := range backup.QuotaCycles.Facts {
		if f.Tokens.InputTokens != 0 || f.Tokens.OutputTokens != 0 || f.Tokens.ReasoningTokens != 0 || f.Tokens.TotalTokens != 10 {
			t.Fatalf("unexpected normalized token counters: %+v", f.Tokens)
		}
	}
}

func TestQuotaUnboundedNativeTimestampDoesNotBreakExports(t *testing.T) {
	for _, raw := range []string{`1e30`, `"1e30"`, `-1e30`, `"-1e30"`} {
		t.Run(raw, func(t *testing.T) {
			var record UsageRecord
			if err := json.Unmarshal([]byte(`{"provider":"claude","auth_id":"account.json","model":"m","requested_at":`+raw+`}`), &record); err != nil {
				t.Fatal(err)
			}
			s := NewRequestStatistics()
			defer s.Close()
			s.retention = 0
			s.Record(record)
			if _, err := json.Marshal(s.Snapshot()); err != nil {
				t.Fatalf("one malformed native timestamp broke the full export: %v", err)
			}
			if !record.RequestedAt.IsZero() {
				t.Fatalf("out-of-range timestamp must use the existing missing-time fallback, got %v", record.RequestedAt)
			}
		})
	}
}

func TestQuotaImportedTokenComponentsRequirePricesWithoutTotal(t *testing.T) {
	for _, tokens := range []TokenStats{{InputTokens: 100}, {OutputTokens: 100}, {CachedTokens: 100}, {CacheWriteTokens: 100}} {
		s := NewRequestStatistics()
		now := time.Now().UTC().Truncate(time.Second)
		r := quotaTestRecord("components", "m", now.Add(-time.Minute), 100)
		s.Record(r)
		quotaTestObserve(s, now, now.Add(time.Hour), .5, 18000)
		backup := s.Snapshot()
		s.Close()
		// A quota-only backup may retain token components without a total.
		backup.APIs = nil
		for id, f := range backup.QuotaCycles.Facts {
			f.Tokens = tokens
			backup.QuotaCycles.Facts[id] = f
		}
		restored := NewRequestStatistics()
		if _, err := restored.mergeSnapshotChecked(backup); err != nil {
			restored.Close()
			t.Fatal(err)
		}
		period := restored.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
		if period.Summary.CostUSD != nil || period.ModelStats[0].CostUSD != nil || period.EstimatedTotalUSD != nil {
			restored.Close()
			t.Fatalf("missing total_tokens turned an unknown price into a free price: %+v", tokens)
		}
		restored.modelPrices = map[string]ModelPrice{"m": {}}
		period = restored.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
		restored.Close()
		if period.Summary.CostUSD == nil || *period.Summary.CostUSD != 0 {
			t.Fatal("an explicitly free model must remain zero-priced")
		}
	}
}

func TestNativeTimestampNumericUnitsAndJSONBounds(t *testing.T) {
	at := time.Date(2026, 10, 4, 1, 2, 3, 125000000, time.UTC)
	for _, value := range []float64{float64(at.Unix()) + .125, float64(at.UnixMilli()), float64(at.UnixNano())} {
		got := unixTimeFromFlexibleNumber(value)
		if delta := got.Sub(at); delta < -time.Microsecond || delta > time.Microsecond {
			t.Fatalf("numeric timestamp %v lost its units: %v", value, got)
		}
	}
	for _, tc := range []struct {
		millis float64
		year   int
	}{
		{-62167219200000, 0},
		{253402300799000, 9999},
	} {
		got := unixTimeFromFlexibleNumber(tc.millis)
		if got.IsZero() || got.Year() != tc.year {
			t.Fatalf("valid JSON time boundary rejected: %v", got)
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []float64{-62167219201000, 253402300800000} {
		if got := unixTimeFromFlexibleNumber(value); !got.IsZero() {
			t.Fatalf("time outside JSON bounds accepted: %v", got)
		}
	}
}

func TestQuotaResetDeadlineShiftMergesHistoryInEitherImportOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, shift := range []time.Duration{time.Hour, -time.Hour} {
			t.Run(shift.String()+map[bool]string{false: "/old-first", true: "/new-first"}[reverse], func(t *testing.T) {
				now := time.Now().UTC().Truncate(time.Second)
				resetAt := now.Add(-30 * time.Minute)
				end := now.Add(3 * time.Hour)
				old := NewRequestStatistics()
				defer old.Close()
				old.startedAt = now.Add(-4 * time.Hour)
				r := quotaTestRecord("before-reset-import", "m", now.Add(-40*time.Minute), 10_000_000)
				old.Record(r)
				quotaTestObserve(old, now.Add(-35*time.Minute), end, .8, 18000)
				newer := NewRequestStatistics()
				defer newer.Close()
				newer.startedAt = old.startedAt
				quotaTestObserve(newer, resetAt, end.Add(shift), .2, 18000)
				newer.Record(quotaTestRecord("after-reset-import", "m", now.Add(-time.Minute), 1_000_000))
				quotaTestObserve(newer, now, end.Add(shift), .3, 18000)
				snapshots := []StatisticsSnapshot{old.Snapshot(), newer.Snapshot()}
				if reverse {
					snapshots[0], snapshots[1] = snapshots[1], snapshots[0]
				}
				dest := NewRequestStatistics()
				defer dest.Close()
				dest.modelPrices = map[string]ModelPrice{"m": {Prompt: 5}}
				for range 2 {
					for _, snapshot := range snapshots {
						if _, err := dest.mergeSnapshotChecked(snapshot); err != nil {
							t.Fatal(err)
						}
					}
				}
				cycle := dest.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
				if cycle.Summary == nil || cycle.Summary.TotalRequests != 1 || !cycle.StartAt.Equal(resetAt) {
					t.Fatalf("import lost reset boundary and included old requests: start=%v summary=%+v", cycle.StartAt, cycle.Summary)
				}
				quotaModelAssert(t, cycle.EstimatedTotalUSD, 5/.3)
				if dest.totalRequests != 2 {
					t.Fatal("history was deleted instead of excluded from reset segment")
				}
				if !cycle.EndAt.Equal(end.Add(shift)) {
					t.Fatal("older import replaced the new deadline")
				}
				backup := dest.Snapshot()
				if err := validateQuotaSnapshot(backup.QuotaCycles, nil); err != nil {
					t.Fatal(err)
				}
				for _, w := range backup.QuotaCycles.Windows {
					if len(w.Current.Samples) != 3 {
						t.Fatal("repeated import duplicated or lost observations")
					}
				}
			})
		}
	}
}
