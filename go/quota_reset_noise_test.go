package main

import (
	"math"
	"strconv"
	"testing"
	"time"
)

// Codex reports weekly usage when a request is admitted. A long stream that
// completes after a shorter, later request carries an older, lower value.
func TestQuotaConcurrentStreamsDoNotLookLikeResetCard(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(5 * 24 * time.Hour)
	s.startedAt = now.Add(-3 * time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	record := func(id string, at time.Time, latency time.Duration, used int) UsageRecord {
		r := UsageRecord{RequestID: id, Provider: "codex", AuthType: "oauth", AuthID: "codex.json", AuthIndex: "codex-index",
			Model: "m", RequestedAt: at, Latency: latency, Detail: UsageDetail{InputTokens: 1_000_000, TotalTokens: 1_000_000},
			ResponseHeaders: map[string][]string{
				"X-Codex-Secondary-Used-Percent":   {strconv.Itoa(used)},
				"X-Codex-Secondary-Window-Minutes": {"10080"},
				"X-Codex-Secondary-Reset-At":       {strconv.FormatInt(reset.Unix(), 10)},
			}}
		s.Record(r)
		return r
	}
	start := now.Add(-2 * time.Hour)
	record("a", start, time.Minute, 20)
	record("b", start.Add(10*time.Minute), time.Minute, 20)
	// The long stream is admitted at 20% and completes after "c" saw 24%, more than rounding jitter.
	record("long", start.Add(20*time.Minute), 30*time.Minute, 20)
	record("c", start.Add(25*time.Minute), time.Minute, 24)
	last := record("d", start.Add(60*time.Minute), time.Minute, 24)
	cycle := s.QueryAPIDetailAt(usageGroupKey(last), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	if cycle.ResetBaselineUsedPercent != nil || cycle.Summary == nil || cycle.Summary.TotalRequests != 5 {
		t.Fatalf("concurrent streams were treated as a reset card: %+v", cycle)
	}
	quotaModelAssert(t, cycle.Summary.CostUSD, 5)
}

// Host quota queries and response headers can round the same usage
// differently. A sub-point decrease is noise, not a reset card.
func TestQuotaRoundingJitterIsNotResetCard(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	end := start.Add(7 * 24 * time.Hour)
	s.startedAt = start.Add(-time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 5}}
	s.Record(quotaTestRecord("old", "m", start.Add(time.Minute), 1_000_000))
	quotaTestObserve(s, start.Add(2*time.Minute), end, .214, 604800)
	quotaTestObserve(s, start.Add(3*time.Minute), end, .21, 604800)
	s.Record(quotaTestRecord("new", "m", start.Add(4*time.Minute), 1_000_000))
	quotaTestObserve(s, start.Add(5*time.Minute), end, .22, 604800)
	cycle := s.QueryAPIDetailAt(usageGroupKey(quotaTestRecord("", "m", now, 0)), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	if cycle.ResetBaselineUsedPercent != nil || cycle.Summary == nil || cycle.Summary.TotalRequests != 2 {
		t.Fatalf("rounding jitter was treated as a reset card: %+v", cycle)
	}
	if cycle.EstimatedTotalUSD == nil || math.Abs(*cycle.EstimatedTotalUSD-10/.22) > 1e-9 {
		t.Fatalf("estimate ignored pre-jitter spend: %v", cycle.EstimatedTotalUSD)
	}
}

// A reset card restarts the window. The moved deadline gives the exact reset
// time, so requests between the card and the next observation still count.
func TestQuotaResetCardDeadlineGivesExactBoundary(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-2 * 24 * time.Hour)
	week := int64(604800)
	s.startedAt = start.Add(-time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	s.Record(quotaTestRecord("before", "m", start.Add(time.Hour), 1_000_000))
	quotaTestObserve(s, start.Add(2*time.Hour), start.Add(7*24*time.Hour), .01, week)
	card := now.Add(-time.Hour)
	s.Record(quotaTestRecord("between", "m", card.Add(10*time.Minute), 1_000_000))
	quotaTestObserve(s, card.Add(20*time.Minute), card.Add(7*24*time.Hour), .01, week)
	cycle := s.QueryAPIDetailAt(usageGroupKey(quotaTestRecord("", "m", now, 0)), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	if !cycle.StartAt.Equal(card) || cycle.ResetBaselineUsedPercent == nil || cycle.Summary == nil || cycle.Summary.TotalRequests != 1 {
		t.Fatalf("moved deadline did not give the reset boundary: %+v", cycle)
	}
	quotaModelAssert(t, cycle.EstimatedTotalUSD, 100)
}

func TestQuotaResetDecreaseNeedsConfirmationAndZeroCounts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	end := start.Add(5 * time.Hour)
	for _, tc := range []struct {
		name  string
		used  []float64
		reset bool
	}{
		{"unconfirmed", []float64{.3, .1}, false},
		{"stale sample", []float64{.3, .1, .3}, false},
		{"confirmed", []float64{.3, .1, .12}, true},
		{"small usage to zero", []float64{.01, 0, 0}, true},
		{"zero at start", []float64{0, 0, .01}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &quotaPeriod{Start: start, End: end}
			for i, used := range tc.used {
				p.Samples = append(p.Samples, quotaObservation{ObservedAt: start.Add(time.Duration(i) * time.Minute), Reset: end, Used: used})
			}
			if _, got := quotaEffectivePeriod(p); (got != nil) != tc.reset {
				t.Fatalf("reset = %v, want %v", got != nil, tc.reset)
			}
		})
	}
}
