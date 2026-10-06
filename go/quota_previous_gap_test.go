package main

import (
	"testing"
	"time"
)

func TestQuotaPreviousSurvivesIdleGap(t *testing.T) {
	for _, mode := range []string{"live", "expired", "late", "merge-old-first", "merge-new-first"} {
		t.Run(mode, func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			end := now.Add(-49 * time.Hour)
			start := now.Add(-time.Hour)
			old := quotaTestRecord("old", "old-model", end.Add(-time.Minute), 7)
			s.Record(old)
			s.Record(quotaTestRecord("idle", "idle-model", end.Add(time.Minute), 13))
			s.Record(quotaTestRecord("new", "new-model", start.Add(time.Minute), 11))
			observeOld := func(target *RequestStatistics) { quotaTestObserve(target, end.Add(-time.Second), end, .8, 18000) }
			observeNew := func(target *RequestStatistics) { quotaTestObserve(target, now, start.Add(5*time.Hour), .2, 18000) }
			switch mode {
			case "live", "expired":
				observeOld(s)
				if mode == "expired" {
					s.retention = 24 * time.Hour
					s.pruneQuotaLocked(now)
				}
				observeNew(s)
			case "late":
				observeNew(s)
				observeOld(s)
			default:
				older, newer := NewRequestStatistics(), NewRequestStatistics()
				defer older.Close()
				defer newer.Close()
				observeOld(older)
				observeNew(newer)
				first, second := older.Snapshot(), newer.Snapshot()
				if mode == "merge-new-first" {
					first, second = second, first
				}
				for _, snapshot := range []StatisticsSnapshot{first, second, first, second} {
					if _, err := s.mergeSnapshotChecked(snapshot); err != nil {
						t.Fatal(err)
					}
				}
			}
			check := func(target *RequestStatistics) {
				t.Helper()
				group := target.QueryAPIDetailAt(usageGroupKey(old), "all", 10, 10, now).QuotaCycles[0].Groups[0]
				if group.Current == nil || group.Previous == nil {
					t.Fatal("idle gap discarded observed previous period")
				}
				if !group.Previous.EndAt.Equal(end) || !group.Current.StartAt.Equal(start) {
					t.Fatal("idle gap changed real period boundaries")
				}
				if group.Previous.Summary.TotalTokens != 7 || group.Current.Summary.TotalTokens != 11 || *group.Previous.UsedPercent != 80 {
					t.Fatal("period accounting mixed across idle gap")
				}
			}
			check(s)
			snapshot := s.Snapshot()
			if err := validateQuotaSnapshot(snapshot.QuotaCycles, nil); err != nil {
				t.Fatal(err)
			}
			restored := NewRequestStatistics()
			defer restored.Close()
			restored.restoreQuotaSnapshotLocked(snapshot)
			check(restored)
		})
	}
}

func TestQuotaPreviousRetainedUntilNextCompletion(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	oldEnd := now.Add(-49 * time.Hour)
	old := quotaTestRecord("old", "old-model", oldEnd.Add(-time.Minute), 7)
	s.Record(old)
	quotaTestObserve(s, oldEnd.Add(-time.Second), oldEnd, .8, 18000)
	s.retention = time.Hour
	s.pruneQuotaLocked(now)
	group := s.QueryAPIDetailAt(usageGroupKey(old), "all", 10, 10, now).QuotaCycles[0].Groups[0]
	if group.Current != nil || group.Previous == nil || group.Previous.Summary.TotalTokens != 7 {
		t.Fatal("idle cleanup lost the last completed period")
	}
	nextEnd := now.Add(4 * time.Hour)
	s.Record(quotaTestRecord("new", "new-model", now.Add(-time.Minute), 11))
	quotaTestObserve(s, now, nextEnd, .2, 18000)
	s.pruneQuotaLocked(nextEnd.Add(30 * 24 * time.Hour))
	group = s.QueryAPIDetailAt(usageGroupKey(old), "all", 10, 10, nextEnd.Add(30*24*time.Hour)).QuotaCycles[0].Groups[0]
	if group.Current != nil || group.Previous == nil || !group.Previous.EndAt.Equal(nextEnd) || group.Previous.Summary.TotalTokens != 11 {
		t.Fatal("next completion did not replace previous period or survive prolonged idle time")
	}
	if len(s.quota.Facts) != 1 {
		t.Fatal("superseded facts were not released")
	}
}

func TestQuotaLatestPreviousWinsOutOfOrder(t *testing.T) {
	for _, mode := range []string{"late", "merge"} {
		t.Run(mode, func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			quotaTestObserve(s, now, now.Add(time.Hour), .2, 18000)
			for _, age := range []time.Duration{72 * time.Hour, 24 * time.Hour, 48 * time.Hour} {
				end := now.Add(-age)
				if mode == "late" {
					quotaTestObserve(s, end.Add(-time.Minute), end, .8, 18000)
				} else {
					source := NewRequestStatistics()
					quotaTestObserve(source, end.Add(-time.Minute), end, .8, 18000)
					snapshot := source.Snapshot()
					source.Close()
					if _, err := s.mergeSnapshotChecked(snapshot); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, w := range s.quota.Windows {
				if w.Previous == nil || !w.Previous.End.Equal(now.Add(-24*time.Hour)) {
					t.Fatal("older delivery replaced latest completed period")
				}
			}
		})
	}
}
