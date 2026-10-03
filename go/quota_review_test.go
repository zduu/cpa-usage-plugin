package main

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestQuotaImportedDeletionUpdatesBothLedgers(t *testing.T) {
	for _, archived := range []bool{false, true} {
		for _, journal := range []bool{false, true} {
			t.Run(fmt.Sprintf("archived=%t/journal=%t", archived, journal), func(t *testing.T) {
				now := time.Now().UTC().Truncate(time.Second)
				source, dest := NewRequestStatistics(), NewRequestStatistics()
				defer source.Close()
				defer dest.Close()
				r := quotaTestRecord("deleted", "m", now.Add(-time.Minute), 1_000_000)
				source.Record(r)
				quotaTestObserve(source, now, now.Add(time.Hour), .5, 18000)
				dest.modelPrices = map[string]ModelPrice{"m": {Prompt: 7}}
				if archived {
					source.maxDetailsPerModel = 1
					source.Record(quotaTestRecord("kept", "m", now, 2_000_000))
				}
				backup := source.Snapshot()
				if _, err := dest.mergeSnapshotChecked(backup); err != nil {
					t.Fatal(err)
				}
				// Populate cached aggregates before removing the original event.
				dest.SummaryWithoutDetailsForRangeAt("24h", now)
				dest.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now)
				if !source.RemoveRecordedUsage(r) {
					t.Fatal("could not delete source request")
				}
				removed := source.Snapshot()
				for range 2 {
					if journal {
						if err := dest.replayPersistedDetailsLocked(quotaStorageRecords(removed.QuotaCycles), 0, false, now); err != nil {
							t.Fatal(err)
						}
					} else if _, err := dest.mergeSnapshotChecked(removed); err != nil {
						t.Fatal(err)
					}
					if dest.totalRequests != removed.TotalRequests || dest.totalTokens != removed.TotalTokens || len(dest.quota.Facts) != len(removed.QuotaCycles.Facts) {
						t.Fatalf("deletion diverged: main=%d tokens=%d facts=%d", dest.totalRequests, dest.totalTokens, len(dest.quota.Facts))
					}
					detail := dest.QueryAPIDetailAt(usageGroupKey(r), "24h", 10, 10, now.Add(time.Second))
					if detail.Summary.TotalRequests != removed.TotalRequests || detail.Summary.EstimatedCost != float64(removed.TotalTokens)*7/1_000_000 {
						t.Fatalf("cached aggregates retained deleted costs: %+v", detail.Summary)
					}
				}
				// Re-importing the old backup must not resurrect the deletion.
				if _, err := dest.mergeSnapshotChecked(backup); err != nil {
					t.Fatal(err)
				}
				if dest.totalRequests != removed.TotalRequests {
					t.Fatal("older backup resurrected deleted usage")
				}
			})
		}
	}
}

func TestQuotaPreviousCapacityAndPersistence(t *testing.T) {
	for _, used := range []float64{.4, 1} {
		for _, mode := range []string{"priced", "zero", "unknown", "partial", "legacy"} {
			t.Run(fmt.Sprintf("used=%g/%s", used, mode), func(t *testing.T) {
				now := time.Now().UTC().Truncate(time.Second)
				boundary := now.Add(-time.Minute)
				s := NewRequestStatistics()
				defer s.Close()
				s.startedAt = boundary.Add(-6 * time.Hour)
				price := float64(10)
				if mode == "zero" {
					price = 0
				}
				if mode != "unknown" {
					s.modelPrices = map[string]ModelPrice{"m": {Prompt: price}}
				}
				if mode == "partial" {
					s.startedAt = boundary.Add(-3 * time.Hour)
				}
				r := quotaTestRecord("first", "m", boundary.Add(-2*time.Hour), 1_000_000)
				s.Record(r)
				quotaTestObserve(s, boundary.Add(-time.Hour), boundary, .3, 18000)
				s.Record(quotaTestRecord("second", "m", boundary.Add(-30*time.Minute), 1_000_000))
				// An observation exactly at the end is still a valid final watermark.
				quotaTestObserve(s, boundary, boundary, used, 18000)
				quotaTestObserve(s, now, boundary.Add(5*time.Hour), .1, 18000)
				backup := s.Snapshot()
				if mode == "legacy" {
					for key, w := range backup.QuotaCycles.Windows {
						w.Previous.CollectionStartedAt = time.Time{}
						backup.QuotaCycles.Windows[key] = w
					}
				}
				raw, err := json.Marshal(backup)
				if err != nil {
					t.Fatal(err)
				}
				var decoded StatisticsSnapshot
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				restored := NewRequestStatistics()
				defer restored.Close()
				restored.modelPrices = s.modelPrices
				restored.restoreStorageSnapshotLocked(decoded, now)
				restored.restoreQuotaSnapshotLocked(decoded)
				for _, target := range []*RequestStatistics{s, restored} {
					p := target.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Previous
					if p == nil || *p.UsedPercent != used*100 {
						t.Fatal("previous watermark changed")
					}
					var amount *float64
					want := 2 * price / used
					if used == 1 {
						amount = p.ActualTotalUSD
						if p.EstimatedTotalUSD != nil {
							t.Fatal("fully used period must report actual capacity")
						}
					} else {
						amount = p.EstimatedTotalUSD
						if mode == "partial" {
							want = price / (used - .3)
						}
						if p.ActualTotalUSD != nil {
							t.Fatal("partially used period was labeled actual capacity")
						}
					}
					if mode == "unknown" {
						if amount != nil {
							t.Fatal("unknown prices became a quota amount")
						}
					} else if amount == nil || math.Abs(*amount-want) > 1e-9 {
						t.Fatalf("previous amount=%v, want %g", amount, want)
					}
				}
			})
		}
	}
}

func TestQuotaLateObservationRestoresAdjacentPrevious(t *testing.T) {
	for _, drift := range []time.Duration{-time.Second, 0, time.Second} {
		t.Run(drift.String(), func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			boundary := now.Add(-time.Minute)
			r := quotaTestRecord("old", "old", boundary.Add(-time.Minute), 7)
			s.Record(r)
			s.Record(quotaTestRecord("current", "current", boundary, 11))
			quotaTestObserve(s, now, boundary.Add(5*time.Hour), .2, 18000)
			quotaTestObserve(s, boundary.Add(-10*time.Second), boundary.Add(drift), .8, 18000)
			group := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0]
			if group.Previous == nil || group.Current == nil || !group.Previous.EndAt.Equal(group.Current.StartAt) {
				t.Fatal("delayed adjacent observation did not restore the previous window")
			}
			if group.Previous.Summary.TotalTokens != 7 || group.Current.Summary.TotalTokens != 11 || *group.Previous.UsedPercent != 80 || *group.Current.UsedPercent != 20 {
				t.Fatal("delayed observation changed period accounting or current watermark")
			}
			if err := validateQuotaSnapshot(s.Snapshot().QuotaCycles, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQuotaSnapshotMergeKeepsBothPeriodHistories(t *testing.T) {
	for _, newestFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(newestFirst), func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			boundary := now.Add(-time.Minute)
			old, newer, dest := NewRequestStatistics(), NewRequestStatistics(), NewRequestStatistics()
			defer old.Close()
			defer newer.Close()
			defer dest.Close()
			quotaTestObserve(old, boundary.Add(-time.Minute), boundary, .8, 18000)
			quotaTestObserve(newer, now, boundary.Add(5*time.Hour), .2, 18000)
			first, second := old.Snapshot(), newer.Snapshot()
			if newestFirst {
				first, second = second, first
			}
			for _, snapshot := range []StatisticsSnapshot{first, second, first, second} {
				if _, err := dest.mergeSnapshotChecked(snapshot); err != nil {
					t.Fatal(err)
				}
			}
			for _, w := range dest.quota.Windows {
				if w.Current == nil || w.Previous == nil || len(w.Previous.Samples) != 1 || len(w.Current.Samples) != 1 {
					t.Fatal("merging backups discarded an observed adjacent period")
				}
			}
		})
	}
}

func TestQuotaPreviousCalibrationSurvivesJournalAndRepricing(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	boundary := now.Add(-time.Minute)
	s := NewRequestStatistics()
	defer s.Close()
	s.startedAt = boundary.Add(-3 * time.Hour)
	var rows []persistedDetail
	for i, used := range []string{"0.3", "0.4"} {
		r := quotaTestRecord(fmt.Sprint(i), "m", boundary.Add(time.Duration(i-2)*time.Hour), 1_000_000)
		r.ResponseHeaders = map[string][]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": {used},
			"Anthropic-Ratelimit-Unified-5h-Reset":       {boundary.Format(time.RFC3339)},
		}
		d := requestDetailFromUsageRecord(r, r.RequestedAt, headerWhitelist{})
		observations := s.observeQuotaUsageLocked(r, now)
		rows = append(rows, persistedDetail{API: usageGroupKey(r), Model: "m", Detail: d, QuotaObservations: observations})
	}
	// Recover solely from the durable rows: there is no quota snapshot yet.
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []persistedDetail
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	defer restored.Close()
	restored.priceStoragePath = filepath.Join(t.TempDir(), "prices.json")
	if err := restored.replayPersistedDetailsLocked(decoded, 0, false, now); err != nil {
		t.Fatal(err)
	}
	for _, price := range []float64{5, 10} {
		if _, err := restored.UpsertModelPrice("m", ModelPrice{Prompt: price}); err != nil {
			t.Fatal(err)
		}
		p := restored.QueryAPIDetailAt(rows[0].API, "all", 10, 10, now).QuotaCycles[0].Groups[0].Previous
		if p == nil || p.EstimatedTotalUSD == nil || math.Abs(*p.EstimatedTotalUSD-price/.1) > 1e-9 || p.Summary.CostUSD == nil || *p.Summary.CostUSD != 2*price {
			t.Fatalf("journal lost historical calibration or repricing: %+v", p)
		}
	}
}

func TestQuotaLiveMemoryRetentionWithoutDashboard(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	s.retention = time.Hour
	now := time.Now().UTC()
	s.Record(quotaTestRecord("expired", "m", now.Add(-2*time.Hour), 7))
	s.Record(quotaTestRecord("retained", "m", now.Add(-time.Minute), 11))
	if s.totalRequests != 1 || len(s.quota.Facts) != 1 {
		t.Fatalf("memory retention diverged: requests=%d facts=%d", s.totalRequests, len(s.quota.Facts))
	}
	// Native observations must establish retention before the request is pruned.
	s.quota.NextPrune = time.Time{}
	r := quotaTestRecord("protected", "m", now.Add(-2*time.Hour), 13)
	r.ResponseHeaders = map[string][]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.5"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {now.Add(time.Hour).Format(time.RFC3339)},
	}
	s.Record(r)
	if s.totalRequests != 1 || len(s.quota.Facts) != 2 {
		t.Fatal("live pruning deleted usage protected by the request's own quota window")
	}
	// Frequent observation updates must not force a full fact scan per request.
	deadline := s.quota.NextPrune
	r.RequestID, r.RequestedAt = "next", now.Add(-time.Second)
	s.Record(r)
	if !s.quota.NextPrune.Equal(deadline) {
		t.Fatal("each live observation restarted the quota retention scan")
	}
}

func TestQuotaDashboardRestartInvalidatesConditionalResponses(t *testing.T) {
	previous := stats
	t.Cleanup(func() { stats = previous })
	now := time.Now()
	stats = NewRequestStatistics()
	defer stats.Close()
	firstSummary := dashboardSummaryETagForVersion(now, "all", 1)
	firstDetail := dashboardAPIDetailETagForVersion("claude", "all", 10, 10, now, 1)
	stats = NewRequestStatistics()
	defer stats.Close()
	if firstSummary == dashboardSummaryETagForVersion(now, "all", 1) || firstDetail == dashboardAPIDetailETagForVersion("claude", "all", 10, 10, now, 1) {
		t.Fatal("a restarted plugin can return 304 for a previous instance's quota data")
	}
}

func TestQuotaRevocationBeforeDelayedObservation(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("request", "m", now.Add(-time.Minute), 7)
	s.Record(r)
	o := quotaObservation{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID, Group: "shared", Slot: "5h", ObservedAt: now, Revoked: true}
	s.applyQuotaObservationLocked(o)
	backup := s.Snapshot()
	if err := validateQuotaSnapshot(backup.QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	defer restored.Close()
	if _, err := restored.mergeSnapshotChecked(backup); err != nil {
		t.Fatal(err)
	}
	quotaTestObserve(s, now.Add(-time.Second), now.Add(time.Hour), .4, 18000)
	if len(s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles) != 0 {
		t.Fatal("an older observation resurrected an explicitly revoked window")
	}
	// Equal observation times favor the explicit revocation as well.
	quotaTestObserve(restored, now, now.Add(time.Hour), .4, 18000)
	if len(restored.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles) != 0 {
		t.Fatal("replaying a revocation lost its ordering at equal timestamps")
	}
	quotaTestObserve(restored, now.Add(time.Second), now.Add(time.Hour), .5, 18000)
	if len(restored.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now.Add(time.Second)).QuotaCycles) != 1 {
		t.Fatal("a fresh observation could not restore a revoked window")
	}
}

func TestQuotaObservationOrderKeepsRevokedHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	boundary := now.Add(-time.Hour)
	base := quotaObservation{Provider: "claude", AuthIndex: "index", AuthID: "account", Group: "shared", Slot: "5h", Seconds: 18000}
	observations := make([]quotaObservation, 5)
	for i := range 4 {
		o := base
		o.Reset = boundary
		o.ObservedAt = boundary.Add(time.Duration(i-2) * time.Minute)
		o.Used = .2 + float64(i%2)*.2
		if i >= 2 {
			o.Reset = boundary.Add(5 * time.Hour)
			o.ObservedAt = boundary.Add(time.Duration(i) * time.Minute)
		}
		observations[i] = o
	}
	observations[4] = base
	observations[4].ObservedAt, observations[4].Revoked = now, true
	var check func(int)
	check = func(at int) {
		if at < len(observations) {
			for i := at; i < len(observations); i++ {
				observations[at], observations[i] = observations[i], observations[at]
				check(at + 1)
				observations[at], observations[i] = observations[i], observations[at]
			}
			return
		}
		s := NewRequestStatistics()
		defer s.Close()
		for _, o := range observations {
			s.applyQuotaObservationLocked(o)
		}
		w := s.quota.Windows[quotaWindowKey(base)]
		if !w.Hidden || !w.UpdatedAt.Equal(now) || w.Current == nil || w.Previous == nil || len(w.Current.Samples) != 2 || len(w.Previous.Samples) != 2 {
			t.Fatalf("delivery order lost revocation or period history: %+v", w)
		}
		if err := validateQuotaSnapshot(s.Snapshot().QuotaCycles, nil); err != nil {
			t.Fatal(err)
		}
	}
	check(0)
}

func TestQuotaSnapshotRevocationKeepsLatestHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	boundary := now.Add(-time.Hour)
	base := quotaObservation{Provider: "claude", AuthIndex: "index", AuthID: "account", Group: "shared", Slot: "5h", Seconds: 18000}
	for _, equalTime := range []bool{false, true} {
		for _, revokedFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("equal=%t/revokedFirst=%t", equalTime, revokedFirst), func(t *testing.T) {
				old, newer, dest := NewRequestStatistics(), NewRequestStatistics(), NewRequestStatistics()
				defer old.Close()
				defer newer.Close()
				defer dest.Close()
				o := base
				o.Reset, o.ObservedAt, o.Used = boundary, boundary.Add(-time.Minute), .8
				old.applyQuotaObservationLocked(o)
				o.Reset, o.ObservedAt, o.Used = boundary.Add(5*time.Hour), now.Add(-time.Minute), .2
				newer.applyQuotaObservationLocked(o)
				o.Revoked = true
				if !equalTime {
					o.ObservedAt = now
				}
				old.applyQuotaObservationLocked(o)
				first, second := newer.Snapshot(), old.Snapshot()
				if revokedFirst {
					first, second = second, first
				}
				for _, snapshot := range []StatisticsSnapshot{first, second, first, second} {
					if _, err := dest.mergeSnapshotChecked(snapshot); err != nil {
						t.Fatal(err)
					}
					if err := validateQuotaSnapshot(dest.captureQuotaSnapshotLocked(), nil); err != nil {
						t.Fatal(err)
					}
				}
				w := dest.quota.Windows[quotaWindowKey(base)]
				if !w.Hidden || !w.UpdatedAt.Equal(o.ObservedAt) || w.Current == nil || w.Previous == nil ||
					len(w.Current.Samples) != 1 || len(w.Previous.Samples) != 1 ||
					!w.Current.End.Equal(boundary.Add(5*time.Hour)) || !w.Previous.End.Equal(boundary) {
					t.Fatalf("snapshot order lost revocation or latest history: %+v", w)
				}
			})
		}
	}
}

func TestQuotaRevocationSurvivesExpiredHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := NewRequestStatistics()
	defer s.Close()
	r := quotaTestRecord("request", "m", now.Add(-time.Minute), 7)
	s.Record(r)
	quotaTestObserve(s, now.Add(-7*time.Hour), now.Add(-6*time.Hour), .8, 18000)
	s.applyQuotaObservationLocked(quotaObservation{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID,
		Group: "shared", Slot: "5h", ObservedAt: now, Revoked: true})
	s.quota.NextPrune = time.Time{}
	s.pruneQuotaLocked(now)
	backup := s.Snapshot()
	for _, w := range backup.QuotaCycles.Windows {
		if !w.Hidden || w.Current != nil || w.Previous != nil {
			t.Fatal("expired revoked history was retained instead of only its marker")
		}
	}
	restored := NewRequestStatistics()
	defer restored.Close()
	if _, err := restored.mergeSnapshotChecked(backup); err != nil {
		t.Fatal(err)
	}
	expired := NewRequestStatistics()
	defer expired.Close()
	if _, err := expired.mergeSnapshotChecked(backup); err != nil {
		t.Fatal(err)
	}
	expired.pruneQuotaLocked(now.Add(733 * 24 * time.Hour))
	if len(expired.quota.Windows) != 0 {
		t.Fatal("revocation markers were retained beyond the supported horizon")
	}
	for _, target := range []*RequestStatistics{s, restored} {
		quotaTestObserve(target, now.Add(-time.Second), now.Add(time.Hour), .4, 18000)
		if len(target.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles) != 0 {
			t.Fatal("pruning expired history discarded the newer revocation")
		}
		quotaTestObserve(target, now.Add(time.Second), now.Add(time.Hour), .5, 18000)
		if len(target.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now.Add(time.Second)).QuotaCycles) != 1 {
			t.Fatal("a fresh observation did not reopen the pruned revoked window")
		}
	}
}

func TestQuotaDeletionMergeKeepsLatestRetentionTime(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, newestFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(newestFirst), func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			s.retention = time.Hour
			older := &quotaSnapshot{Version: 1, Deleted: map[string]time.Time{"deleted": now.Add(-2 * time.Hour)}}
			newer := &quotaSnapshot{Version: 1, Deleted: map[string]time.Time{"deleted": now}}
			first, second := older, newer
			if newestFirst {
				first, second = second, first
			}
			s.mergeQuotaSnapshotLocked(first)
			s.mergeQuotaSnapshotLocked(second)
			s.pruneQuotaLocked(now)
			if !s.quota.Deleted["deleted"].Equal(now) {
				t.Fatal("importing an older deletion shortened the latest deletion's retention")
			}
		})
	}
}
