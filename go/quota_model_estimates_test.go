package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func quotaModelFixture() (quotaWindow, *quotaPeriod, []quotaFact, time.Time) {
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	p := &quotaPeriod{Start: start, End: start.Add(5 * time.Hour), CollectionStartedAt: start.Add(-time.Minute)}
	for i, used := range []float64{.1, .2, .4, .5} {
		p.Samples = append(p.Samples, quotaObservation{ObservedAt: start.Add(time.Duration(i) * 10 * time.Minute), Used: used, CollectionStartedAt: p.CollectionStartedAt})
	}
	w := quotaWindow{Provider: "claude", AuthIndex: "index", AuthID: "account", Seconds: 18000}
	facts := []quotaFact{}
	for i, model := range []string{"a", "b", "a", "b"} {
		minute := []int{5, 15, 25, 26}[i]
		at := start.Add(time.Duration(minute) * time.Minute)
		facts = append(facts, quotaFact{Provider: w.Provider, AuthIndex: w.AuthIndex, AuthID: w.AuthID, Model: model, Timestamp: at, CompletedAt: at.Add(time.Second), Tokens: TokenStats{InputTokens: 1_000_000, TotalTokens: 1_000_000}})
	}
	return w, p, facts, p.Samples[len(p.Samples)-1].ObservedAt
}

func quotaModelTestPrice(prices map[string]ModelPrice) *pricingSnapshot {
	s := NewRequestStatistics()
	defer s.Close()
	s.modelPrices = prices
	return s.pricingSnapshotLocked()
}

func quotaModelTestResult(w quotaWindow, p *quotaPeriod, facts []quotaFact, pricing *pricingSnapshot, now time.Time, current bool) map[string]quotaModelStat {
	dto := quotaBuildPeriod(w, p, facts, pricing)
	applyQuotaModelEstimates(dto, w, p, facts, pricing, now, current)
	rows := map[string]quotaModelStat{}
	for _, row := range dto.ModelStats {
		rows[row.Model] = row
	}
	return rows
}

func quotaModelAssert(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > math.Max(1e-7, math.Abs(want)*1e-12) {
		t.Fatalf("got %v, want %.8f", got, want)
	}
}

func TestQuotaModelExclusiveCapacityDoesNotCopySharedBudget(t *testing.T) {
	w, p, facts, now := quotaModelFixture()
	price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 40}})
	rows := quotaModelTestResult(w, p, facts, price, now, true)
	// A consumed 10% with $8; B consumed 20% with $40. The final interval
	// contains both models and must not influence either calibration.
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 80)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalUSD, 200)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 10_000_000)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 5_000_000)
	// Independent calibration can survive unknown prices of another model.
	rows = quotaModelTestResult(w, p, facts, quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}}), now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 80)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 5_000_000)
	if rows["b"].ModelOnlyTotalUSD != nil {
		t.Fatal("unpriced model received a dollar estimate")
	}
	rows = quotaModelTestResult(w, p, facts, quotaModelTestPrice(map[string]ModelPrice{"a": {}, "b": {}}), now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 0)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 10_000_000)
}

func TestQuotaModelPlateausAccumulateAllRequests(t *testing.T) {
	w, p, facts, now := quotaModelFixture()
	p.Samples = p.Samples[:3]
	p.Samples[1].Used = .1
	facts = facts[:2]
	facts[1].Model = "a"
	rows := quotaModelTestResult(w, p, facts, quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}}), now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 2_000_000/.3)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 16/.3)
	facts[1].Model = "b"
	rows = quotaModelTestResult(w, p, facts, nil, now, true)
	if rows["a"].ModelOnlyTotalTokens != nil || rows["b"].ModelOnlyTotalTokens != nil {
		t.Fatal("plateau with mixed models was attributed to one model")
	}
}

func TestQuotaModelRejectsUnreliableIntervals(t *testing.T) {
	for _, name := range []string{"single-observation", "no-increase", "crossing-start", "crossing-end", "crossing-period", "synthetic", "unknown-model", "unknown-completion", "restart", "before-collection", "decrease", "same-timestamp", "stale"} {
		t.Run(name, func(t *testing.T) {
			w, p, facts, now := quotaModelFixture()
			p.Samples = p.Samples[:2]
			facts = facts[:1]
			switch name {
			case "single-observation":
				p.Samples = p.Samples[:1]
			case "no-increase":
				p.Samples[1].Used = p.Samples[0].Used
			case "crossing-start", "crossing-period":
				facts[0].Timestamp = p.Start.Add(-time.Minute)
				facts[0].CompletedAt = p.Start.Add(time.Minute)
				if name == "crossing-start" {
					p.Start = p.Start.Add(-time.Hour)
				}
			case "crossing-end":
				facts[0].CompletedAt = p.Samples[1].ObservedAt.Add(time.Minute)
			case "synthetic":
				facts[0].TimestampSynthetic = true
			case "unknown-model":
				unknown := facts[0]
				unknown.Model = ""
				facts = append(facts, unknown)
			case "unknown-completion":
				facts[0].CompletedAt = time.Time{}
			case "restart":
				p.Samples[1].CollectionStartedAt = p.Start.Add(3 * time.Minute)
			case "before-collection":
				p.CollectionStartedAt = p.Start.Add(time.Minute)
				for i := range p.Samples {
					p.Samples[i].CollectionStartedAt = time.Time{}
				}
			case "decrease":
				p.Samples = append(p.Samples, quotaObservation{ObservedAt: now, Used: .05, CollectionStartedAt: p.CollectionStartedAt})
			case "same-timestamp":
				p.Samples = append(p.Samples, quotaObservation{ObservedAt: p.Samples[1].ObservedAt, Used: .3, CollectionStartedAt: p.CollectionStartedAt})
			case "stale":
				now = now.Add(2 * time.Hour)
			}
			rows := quotaModelTestResult(w, p, facts, nil, now, true)
			if rows["a"].ModelOnlyTotalTokens != nil || rows["a"].ModelOnlyTotalUSD != nil {
				t.Fatal("unreliable interval produced an estimate")
			}
		})
	}
}

func TestQuotaModelHistorySurvivesRestartWithoutCalibratingAcrossGap(t *testing.T) {
	w, p, facts, now := quotaModelFixture()
	price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 40}})
	// A's complete interval was recorded before the latest process start.
	// B used tokens during the restart gap; those tokens have no paired
	// observations and cannot be counted in either model's calibration.
	p.CollectionStartedAt = p.Start.Add(12 * time.Minute)
	for i := 2; i < len(p.Samples); i++ {
		p.Samples[i].CollectionStartedAt = p.CollectionStartedAt
	}
	facts = []quotaFact{facts[0], facts[1], facts[3]}
	rows := quotaModelTestResult(w, p, facts, price, now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 80)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 10_000_000)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalUSD, 400)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 10_000_000)
	// A single new observation cannot erase a completed historical estimate.
	p.Samples = p.Samples[:3]
	rows = quotaModelTestResult(w, p, facts, price, now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 80)
	if rows["b"].ModelOnlyTotalTokens != nil {
		t.Fatal("restart gap was used as a model-only interval")
	}
	// Staleness still applies separately to each model's old observations.
	rows = quotaModelTestResult(w, p, facts, price, now.Add(2*time.Hour), true)
	if rows["a"].ModelOnlyTotalUSD != nil {
		t.Fatal("stale historical estimate was retained in current period")
	}
	rows = quotaModelTestResult(w, p, facts, price, now.Add(2*time.Hour), false)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 80)
}

func TestQuotaModelCombinesIndependentIntervalsAndRecalibratesAfterDecrease(t *testing.T) {
	w, p, facts, now := quotaModelFixture()
	p.Samples = p.Samples[:3]
	facts = facts[:2]
	facts[1].Model = "a"
	facts[1].Tokens = TokenStats{InputTokens: 3_000_000, TotalTokens: 3_000_000}
	price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}})
	rows := quotaModelTestResult(w, p, facts, price, now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 4_000_000/.3)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 32/.3)
	// A usage correction starts a fresh calibration. Old independent
	// intervals cannot dilute the new model cost-to-quota ratio.
	p.Samples = append(p.Samples,
		quotaObservation{ObservedAt: now, Used: .05, CollectionStartedAt: p.CollectionStartedAt},
		quotaObservation{ObservedAt: now.Add(10 * time.Minute), Used: .3, CollectionStartedAt: p.CollectionStartedAt})
	newFact := facts[0]
	newFact.Timestamp = now.Add(5 * time.Minute)
	newFact.CompletedAt = newFact.Timestamp.Add(time.Second)
	facts = append(facts, newFact)
	rows = quotaModelTestResult(w, p, facts, price, now.Add(10*time.Minute), true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 4_000_000)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 32)
}

func TestQuotaModelPreviousRetainsCalibrationAndFailedZeroIsIgnored(t *testing.T) {
	w, p, facts, now := quotaModelFixture()
	p.Samples = p.Samples[:2]
	facts = facts[:1]
	failed := facts[0]
	failed.Model, failed.Failed, failed.Tokens = "failed-model", true, TokenStats{}
	facts = append(facts, failed)
	rows := quotaModelTestResult(w, p, facts, nil, now.Add(6*time.Hour), false)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 10_000_000)
	if rows["failed-model"].ModelOnlyTotalTokens != nil {
		t.Fatal("zero-token failure produced a calibration")
	}
}

func TestQuotaModelCapacityBackupRepricingAndRollover(t *testing.T) {
	s := NewRequestStatistics()
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	s.startedAt = start.Add(-time.Minute)
	s.modelPrices = map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 40}}
	reset := start.Add(5 * time.Hour)
	quotaTestObserve(s, start, reset, .1, 18000)
	r := quotaTestRecord("exclusive-a", "a", start.Add(5*time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, start.Add(10*time.Minute), reset, .2, 18000)
	s.Record(quotaTestRecord("exclusive-b", "b", start.Add(15*time.Minute), 1_000_000))
	now := start.Add(20 * time.Minute)
	quotaTestObserve(s, now, reset, .4, 18000)
	backup := s.Snapshot()
	s.Close()
	restored := NewRequestStatistics()
	defer restored.Close()
	if _, err := restored.mergeSnapshotChecked(backup); err != nil {
		t.Fatal(err)
	}
	// Usage backups retain observations and facts; prices come from the
	// separately managed model-price configuration.
	for model, price := range map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 40}} {
		if _, err := restored.UpsertModelPrice(model, price); err != nil {
			t.Fatal(err)
		}
	}
	api := usageGroupKey(r)
	current := restored.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	if len(current.ModelStats) != 2 {
		t.Fatal("backup lost models")
	}
	for _, row := range current.ModelStats {
		if row.Model == "a" {
			quotaModelAssert(t, row.ModelOnlyTotalUSD, 80)
		}
	}
	// Changing prices recalculates dollars from preserved facts, without
	// changing token capacity or requiring new quota observations.
	if _, err := restored.UpsertModelPrice("a", ModelPrice{Prompt: 16}); err != nil {
		t.Fatal(err)
	}
	current = restored.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	for _, row := range current.ModelStats {
		if row.Model == "a" {
			quotaModelAssert(t, row.ModelOnlyTotalUSD, 160)
			quotaModelAssert(t, row.ModelOnlyTotalTokens, 10_000_000)
		}
	}
	previous := restored.QueryAPIDetailAt(api, "all", 10, 10, reset.Add(time.Minute)).QuotaCycles[0].Groups[0].Previous
	if previous == nil {
		t.Fatal("rollover lost the historical calibration")
	}
	for _, row := range previous.ModelStats {
		if row.Model == "a" {
			quotaModelAssert(t, row.ModelOnlyTotalUSD, 160)
		}
	}
}

func TestQuotaModelQuantizedPlateauNeedsEnoughEvidence(t *testing.T) {
	w, p, facts, _ := quotaModelFixture()
	p.Samples[0].Used, p.Samples[1].Used, p.Samples[2].Used, p.Samples[3].Used = 0, .01, .01, .10
	facts = facts[:3]
	for i, tokens := range []int64{10_000_000, 2_000_000, 88_000_000} {
		facts[i].Model = "a"
		facts[i].Tokens = TokenStats{InputTokens: tokens, TotalTokens: tokens}
	}
	price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}})
	for _, count := range []int{2, 3} {
		copy := *p
		copy.Samples = p.Samples[:count]
		rows := quotaModelTestResult(w, &copy, facts[:count-1], price, copy.Samples[count-1].ObservedAt, true)
		if rows["a"].ModelOnlyTotalTokens != nil {
			t.Fatal("10M or 12M at 99% remaining is insufficient evidence")
		}
	}
	now := p.Samples[3].ObservedAt
	rows := quotaModelTestResult(w, p, facts, price, now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 1e9)
	quotaModelAssert(t, rows["a"].ModelOnlyTokensLow, 100e6/.11)
	quotaModelAssert(t, rows["a"].ModelOnlyTokensHigh, 100e6/.09)
	quotaModelAssert(t, rows["a"].ModelOnlyUSDLow, 800/.11)
	quotaModelAssert(t, rows["a"].ModelOnlyUSDHigh, 800/.09)
	// Switching models while the reading is unchanged cannot assign the
	// eventual increase to the new model alone.
	facts[2].Model = "b"
	rows = quotaModelTestResult(w, p, facts, price, now, true)
	if rows["a"].ModelOnlyTotalTokens != nil || rows["b"].ModelOnlyTotalTokens != nil {
		t.Fatal("mixed plateau was assigned to a single model")
	}
}

func TestQuotaModelManySmallTicksShareOnlyContiguousEndpointError(t *testing.T) {
	w, p, seed, _ := quotaModelFixture()
	p.Samples = p.Samples[:1]
	p.Samples[0].Used = 0
	var facts []quotaFact
	for i := 1; i <= 10; i++ {
		at := p.Start.Add(time.Duration(i) * time.Minute)
		p.Samples = append(p.Samples, quotaObservation{ObservedAt: at, Used: float64(i) / 100, CollectionStartedAt: p.CollectionStartedAt})
		f := seed[0]
		f.Timestamp, f.CompletedAt = at.Add(-30*time.Second), at.Add(-29*time.Second)
		facts = append(facts, f)
	}
	now := p.Samples[10].ObservedAt
	rows := quotaModelTestResult(w, p, facts, nil, now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 100e6)
	quotaModelAssert(t, rows["a"].ModelOnlyTokensLow, 10e6/.11)
	// Alternating models creates disconnected tiny intervals, not a single
	// large, precise observation.
	for i := 1; i < len(facts); i += 2 {
		facts[i].Model = "b"
	}
	rows = quotaModelTestResult(w, p, facts, nil, now, true)
	if rows["a"].ModelOnlyTotalTokens != nil || rows["b"].ModelOnlyTotalTokens != nil {
		t.Fatal("disconnected tiny intervals acquired false precision")
	}
}

func TestQuotaCapacityRangesRemainJSONSafeAtExtremePrices(t *testing.T) {
	w, p, facts, now := quotaModelFixture()
	p.Samples, facts = p.Samples[:2], facts[:1]
	price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 1.7e307}})
	dto := quotaBuildPeriod(w, p, facts, price)
	applyQuotaModelEstimates(dto, w, p, facts, price, now, true)
	if _, err := json.Marshal(dto); err != nil {
		t.Fatal(err)
	}
	if dto.ModelStats[0].ModelOnlyTotalUSD != nil {
		t.Fatal("overflowing dollar range should be unknown")
	}
	w, p, facts, now = quotaMixedFixture()
	price = quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 1e301}, "b": {Prompt: 1e301}})
	dto = quotaBuildPeriod(w, p, facts, price)
	applyQuotaModelEstimates(dto, w, p, facts, price, now, true)
	if _, err := json.Marshal(dto); err != nil {
		t.Fatal(err)
	}
	for _, row := range dto.ModelStats {
		if row.ModelOnlyUSDHigh == nil {
			t.Fatal("finite range lost to intermediate overflow")
		}
	}
}

func TestQuotaWeeklyCapacityUsesEarlierWiderEstimate(t *testing.T) {
	for _, seconds := range []int64{18000, 604800, 2592000} {
		w, p, facts, now := quotaModelFixture()
		w.Seconds = seconds
		p.Samples, facts = p.Samples[:2], facts[:1]
		p.Samples[0].Used, p.Samples[1].Used = .1, .13
		rows := quotaModelTestResult(w, p, facts, nil, now, true)
		if seconds < 604800 {
			if rows["a"].ModelOnlyTotalTokens != nil {
				t.Fatal("short-window threshold changed")
			}
			continue
		}
		quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 1e6/.03)
		quotaModelAssert(t, rows["a"].ModelOnlyTokensLow, 1e6/.04)
		quotaModelAssert(t, rows["a"].ModelOnlyTokensHigh, 1e6/.02)
		p.Samples[1].Used = .129
		rows = quotaModelTestResult(w, p, facts, nil, now, true)
		if rows["a"].ModelOnlyTotalTokens != nil {
			t.Fatal("less than three points produced an estimate")
		}
	}
}

func TestQuotaWeeklyResetEstimateUsesWindowThreshold(t *testing.T) {
	w, p, facts, _ := quotaModelFixture()
	p.Samples = p.Samples[:3]
	p.Samples[0].Used, p.Samples[1].Used, p.Samples[2].Used = .8, .2, .23
	price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 8}})
	w.Seconds = 604800
	dto := quotaBuildPeriod(w, p, facts[:2], price)
	total, remaining := quotaEstimate(dto)
	quotaModelAssert(t, total, 8/.03)
	quotaModelAssert(t, remaining, 8/.03*.77)
	w.Seconds = 18000
	total, _ = quotaEstimate(quotaBuildPeriod(w, p, facts[:2], price))
	if total != nil {
		t.Fatal("short reset window bypassed threshold")
	}
}
