package main

import (
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestQuotaExhaustedCurrentUsesRecordedSpend(t *testing.T) {
	for _, mode := range []string{"continuous", "partial", "restart", "stale", "zero", "unknown", "late-unpriced", "empty", "unmapped"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			end := now.Add(time.Hour)
			s := NewRequestStatistics()
			defer s.Close()
			s.startedAt = end.Add(-6 * time.Hour)
			price := 3.57
			if mode == "zero" {
				price = 0
			}
			if mode != "unknown" {
				s.modelPrices = map[string]ModelPrice{"m": {Prompt: price}}
			}
			if mode == "partial" {
				s.startedAt = now.Add(-3 * time.Hour)
			}
			r := quotaTestRecord("first", "m", now.Add(-2*time.Hour), 500_000)
			if mode == "empty" {
				r.RequestedAt = end.Add(-6 * time.Hour) // discoverable credential, no usage in this period
			}
			s.Record(r)
			quotaTestObserve(s, now.Add(-110*time.Minute), end, .6, 18000)
			if mode != "empty" {
				s.Record(quotaTestRecord("second", "m", now.Add(-100*time.Minute), 500_000))
			}
			observed := now.Add(-time.Minute)
			if mode == "stale" {
				observed = now.Add(-90 * time.Minute)
			}
			quotaTestObserve(s, observed, end, 1, 18000)
			if mode == "restart" {
				s.quota.StartedAt = now
			}
			if mode == "late-unpriced" {
				s.Record(quotaTestRecord("late", "unpriced", now, 1_000_000))
			}
			if mode == "unmapped" {
				for k, w := range s.quota.Windows {
					w.Unmapped = true
					s.quota.Windows[k] = w
				}
			}
			check := func(target *RequestStatistics) {
				c := target.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
				if c.EstimatedTotalUSD != nil {
					t.Fatalf("exhaustion retained a projection: %v", *c.EstimatedTotalUSD)
				}
				unknown := mode == "unknown" || mode == "late-unpriced" || mode == "empty" || mode == "unmapped"
				if unknown {
					if c.ActualTotalUSD != nil {
						t.Fatal("unknown total was presented as known")
					}
				} else if c.ActualTotalUSD == nil || math.Abs(*c.ActualTotalUSD-price) > 1e-9 || *c.ActualTotalUSD != *c.Summary.CostUSD {
					t.Fatalf("capacity does not equal recorded spend: %+v", c)
				}
				if mode == "unmapped" {
					if c.EstimatedRemainingUSD != nil {
						t.Fatal("unmapped pool acquired a monetary amount")
					}
				} else if c.EstimatedRemainingUSD == nil || *c.EstimatedRemainingUSD != 0 {
					t.Fatal("exhausted pool must have zero remaining even without a calibration")
				}
			}
			check(s)
			raw, err := json.Marshal(s.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			var snapshot StatisticsSnapshot
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			restored := NewRequestStatistics()
			defer restored.Close()
			restored.modelPrices = s.modelPrices
			restored.restoreStorageSnapshotLocked(snapshot, now)
			restored.restoreQuotaSnapshotLocked(snapshot)
			check(restored)
		})
	}
}

func TestQuotaExhaustionTracksLateUsageRepricingAndRollover(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(time.Hour)
	s := NewRequestStatistics()
	defer s.Close()
	s.startedAt = end.Add(-6 * time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 10}}
	r := quotaTestRecord("initial", "m", now.Add(-time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, now, end, .9999, 18000)
	api := usageGroupKey(r)
	c := s.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	if c.ActualTotalUSD != nil || c.EstimatedTotalUSD == nil || math.Abs(*c.EstimatedTotalUSD-10/.9999) > 1e-9 {
		t.Fatal("near-full usage was treated as exhausted")
	}
	quotaTestObserve(s, now.Add(time.Second), end, 1, 18000)
	s.Record(quotaTestRecord("late", "m", now, 500_000))
	c = s.QueryAPIDetailAt(api, "all", 10, 10, now.Add(2*time.Second)).QuotaCycles[0].Groups[0].Current
	if c.ActualTotalUSD == nil || *c.ActualTotalUSD != 15 || c.EstimatedTotalUSD != nil {
		t.Fatal("late usage was not included in exhausted total")
	}
	s.priceStoragePath = filepath.Join(t.TempDir(), "prices.json")
	if _, err := s.UpsertModelPrice("m", ModelPrice{Prompt: 20}); err != nil {
		t.Fatal(err)
	}
	c = s.QueryAPIDetailAt(api, "all", 10, 10, now.Add(3*time.Second)).QuotaCycles[0].Groups[0].Current
	if c.ActualTotalUSD == nil || *c.ActualTotalUSD != 30 {
		t.Fatal("exhausted total did not follow repricing")
	}
	g := s.QueryAPIDetailAt(api, "all", 10, 10, end).QuotaCycles[0].Groups[0]
	if g.Current != nil || g.Previous.ActualTotalUSD == nil || *g.Previous.ActualTotalUSD != *c.ActualTotalUSD || g.Previous.EstimatedTotalUSD != nil {
		t.Fatal("rollover changed the exhausted total")
	}
}

func TestQuotaRecordedSpendFormulaAtZeroAndPartialUsage(t *testing.T) {
	for _, used := range []float64{0, .67} {
		s := NewRequestStatistics()
		defer s.Close()
		now := time.Now().UTC().Truncate(time.Second)
		s.modelPrices = map[string]ModelPrice{"m": {Prompt: 15.08488426}}
		r := quotaTestRecord("weekly", "m", now.Add(-time.Minute), 1_000_000)
		s.Record(r)
		quotaTestObserve(s, now, now.Add(time.Hour), used, 604800)
		c := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
		if used == 0 {
			if c.EstimatedTotalUSD != nil || c.EstimatedRemainingUSD != nil || c.ActualTotalUSD != nil {
				t.Fatal("zero usage must not yield a finite capacity")
			}
		} else if c.EstimatedTotalUSD == nil || c.EstimatedRemainingUSD == nil || math.Abs(*c.EstimatedTotalUSD-15.08488426/.67) > 1e-9 || math.Abs(*c.EstimatedRemainingUSD-(15.08488426/.67-15.08488426)) > 1e-9 {
			t.Fatalf("weekly screenshot regression: %+v", c)
		}
	}
}
