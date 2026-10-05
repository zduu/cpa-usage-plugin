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

func quotaModelTestResult(w quotaWindow, p *quotaPeriod, facts []quotaFact, pricing *pricingSnapshot) *quotaCycleDTO {
	dto := quotaBuildPeriod(w, p, facts, pricing)
	if dto.UsedPercent != nil && *dto.UsedPercent != 100 {
		dto.EstimatedTotalUSD, _ = quotaEstimate(dto)
	}
	applyQuotaModelEstimates(dto)
	return dto
}

func quotaModelRows(dto *quotaCycleDTO) map[string]quotaModelStat {
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

func TestQuotaModelCapacityUsesSharedBudgetAndWholePeriodAverage(t *testing.T) {
	w, p, facts, _ := quotaModelFixture()
	price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 40}})
	dto := quotaModelTestResult(w, p, facts, price)
	// Whole-period spend is $96 at 50% usage: both models share a $192 budget.
	// A costs $8 per 1M tokens; B costs $40 per 1M tokens.
	quotaModelAssert(t, dto.EstimatedTotalUSD, 192)
	rows := quotaModelRows(dto)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 24_000_000)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 4_800_000)

	// A's later request uses output tokens: its period average must include
	// both requests, even though the later request shares an interval with B.
	facts[2].Tokens = TokenStats{OutputTokens: 1_000_000, TotalTokens: 1_000_000}
	price = quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8, Completion: 32}, "b": {Prompt: 40}})
	dto = quotaModelTestResult(w, p, facts, price)
	quotaModelAssert(t, dto.EstimatedTotalUSD, 240)
	rows = quotaModelRows(dto)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 12_000_000)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 6_000_000)

	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Models []map[string]json.RawMessage `json:"model_stats"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, model := range payload.Models {
		for _, key := range []string{"model_only_estimated_total_usd", "model_only_tokens_low", "model_only_tokens_high", "model_only_usd_low", "model_only_usd_high"} {
			if _, ok := model[key]; ok {
				t.Fatalf("removed estimate field %s still returned", key)
			}
		}
	}
}

func TestQuotaModelCapacityDoesNotRequireCalibrationIntervals(t *testing.T) {
	for _, name := range []string{"single-observation", "plateau", "restart-gap", "synthetic-time", "unknown-completion"} {
		t.Run(name, func(t *testing.T) {
			w, p, facts, _ := quotaModelFixture()
			switch name {
			case "single-observation":
				p.Samples = p.Samples[len(p.Samples)-1:]
			case "plateau":
				for i := range p.Samples {
					p.Samples[i].Used = .5
				}
			case "restart-gap":
				p.CollectionStartedAt = p.Samples[2].ObservedAt
				for i := 2; i < len(p.Samples); i++ {
					p.Samples[i].CollectionStartedAt = p.CollectionStartedAt
				}
			case "synthetic-time":
				facts[0].TimestampSynthetic = true
			case "unknown-completion":
				facts[0].CompletedAt = time.Time{}
			}
			price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 40}})
			rows := quotaModelRows(quotaModelTestResult(w, p, facts, price))
			quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 24_000_000)
			quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 4_800_000)
		})
	}
}

func TestQuotaModelCapacityUnknownBudgetOrZeroCost(t *testing.T) {
	w, p, facts, _ := quotaModelFixture()
	for _, prices := range []map[string]ModelPrice{nil, {"a": {Prompt: 8}}, {"a": {}, "b": {}}} {
		dto := quotaModelTestResult(w, p, facts, quotaModelTestPrice(prices))
		for _, row := range dto.ModelStats {
			if row.ModelOnlyTotalTokens != nil {
				t.Fatal("unknown budget or zero model cost produced a token estimate")
			}
		}
	}
	dto := quotaModelTestResult(w, p, facts, quotaModelTestPrice(map[string]ModelPrice{"a": {}, "b": {Prompt: 40}}))
	rows := quotaModelRows(dto)
	quotaModelAssert(t, dto.EstimatedTotalUSD, 160)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 4_000_000)
	if rows["a"].ModelOnlyTotalTokens != nil {
		t.Fatal("free model received a finite budget-based token estimate")
	}

	zero := 0.0
	for _, total := range []*float64{nil, &zero} {
		dto.EstimatedTotalUSD = total
		applyQuotaModelEstimates(dto)
		if total == nil {
			if quotaModelRows(dto)["b"].ModelOnlyTotalTokens != nil {
				t.Fatal("unavailable budget retained a prior estimate")
			}
		} else {
			quotaModelAssert(t, quotaModelRows(dto)["b"].ModelOnlyTotalTokens, 0)
		}
	}
}

func TestQuotaModelCapacityUsesActualBudgetAtFullUsage(t *testing.T) {
	w, p, facts, _ := quotaModelFixture()
	p.Samples[len(p.Samples)-1].Used = 1
	dto := quotaModelTestResult(w, p, facts, quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 40}}))
	quotaModelAssert(t, dto.ActualTotalUSD, 96)
	if dto.EstimatedTotalUSD != nil {
		t.Fatal("full period should use its actual budget")
	}
	rows := quotaModelRows(dto)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 12_000_000)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 2_400_000)
	dto.EstimatedTotalUSD, dto.ActualTotalUSD = dto.ActualTotalUSD, nil
	applyQuotaModelEstimates(dto)
	for _, row := range dto.ModelStats {
		if row.ModelOnlyTotalTokens != nil {
			t.Fatal("unknown actual budget fell back to an estimated budget at 100%")
		}
	}
}

func TestQuotaModelCapacityInvalidValuesAndOverflowStayUnknown(t *testing.T) {
	validCost, validTotal := 10.0, 100.0
	for _, invalid := range []float64{-1, math.NaN(), math.Inf(1)} {
		for _, invalidBudget := range []bool{false, true} {
			dto := &quotaCycleDTO{EstimatedTotalUSD: &validTotal, ModelStats: []quotaModelStat{{ModelStat: ModelStat{TotalTokens: 1_000_000}, CostUSD: &validCost}}}
			if invalidBudget {
				dto.EstimatedTotalUSD = &invalid
			} else {
				dto.ModelStats[0].CostUSD = &invalid
			}
			applyQuotaModelEstimates(dto)
			if dto.ModelStats[0].ModelOnlyTotalTokens != nil {
				t.Fatal("invalid operand produced an estimate")
			}
		}
	}
	largeTotal, smallCost := 1e308, 1e-308
	dto := &quotaCycleDTO{EstimatedTotalUSD: &largeTotal, ModelStats: []quotaModelStat{{ModelStat: ModelStat{TotalTokens: 1_000_000}, CostUSD: &smallCost}}}
	applyQuotaModelEstimates(dto)
	if dto.ModelStats[0].ModelOnlyTotalTokens != nil {
		t.Fatal("overflowing capacity should be unknown")
	}
	if _, err := json.Marshal(dto); err != nil {
		t.Fatal(err)
	}
	dto.EstimatedTotalUSD = &validTotal
	dto.ModelStats[0].CostUSD = &validCost
	dto.ModelStats[0].TotalTokens = 0
	applyQuotaModelEstimates(dto)
	if dto.ModelStats[0].ModelOnlyTotalTokens != nil {
		t.Fatal("model without tokens received an estimate")
	}
}

func TestQuotaModelCapacityHasNoMinimumConsumptionThreshold(t *testing.T) {
	for _, seconds := range []int64{18000, 604800, 2592000} {
		for _, used := range []float64{0, .001, .01, .02, .03} {
			w, p, facts, _ := quotaModelFixture()
			w.Seconds = seconds
			p.Samples = p.Samples[:1]
			p.Samples[0].Used = used
			dto := quotaModelTestResult(w, p, facts[:1], quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}}))
			if used == 0 {
				if dto.ModelStats[0].ModelOnlyTotalTokens != nil {
					t.Fatal("zero used fraction produced an estimate")
				}
			} else {
				quotaModelAssert(t, dto.ModelStats[0].ModelOnlyTotalTokens, 1_000_000/used)
			}
		}
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
			quotaModelAssert(t, row.ModelOnlyTotalTokens, 15_000_000)
		}
	}
	quotaModelAssert(t, current.EstimatedTotalUSD, 120)
	// Repricing changes both the period budget and model average price.
	// Token capacity updates from preserved facts without new observations.
	if _, err := restored.UpsertModelPrice("a", ModelPrice{Prompt: 16}); err != nil {
		t.Fatal(err)
	}
	current = restored.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	for _, row := range current.ModelStats {
		if row.Model == "a" {
			quotaModelAssert(t, row.ModelOnlyTotalTokens, 8_750_000)
		}
	}
	quotaModelAssert(t, current.EstimatedTotalUSD, 140)
	stale := restored.QueryAPIDetailAt(api, "all", 10, 10, now.Add(2*time.Hour)).QuotaCycles[0].Groups[0].Current
	if stale.EstimatedTotalUSD != nil {
		t.Fatal("stale period retained its budget")
	}
	for _, row := range stale.ModelStats {
		if row.ModelOnlyTotalTokens != nil {
			t.Fatal("model capacity survived an unavailable period budget")
		}
	}
	previous := restored.QueryAPIDetailAt(api, "all", 10, 10, reset.Add(time.Minute)).QuotaCycles[0].Groups[0].Previous
	if previous == nil {
		t.Fatal("rollover lost the historical capacity")
	}
	for _, row := range previous.ModelStats {
		if row.Model == "a" {
			quotaModelAssert(t, row.ModelOnlyTotalTokens, 8_750_000)
		}
	}
}

func TestQuotaTotalAndRemainingUseSpendRatioWithoutThreshold(t *testing.T) {
	for _, seconds := range []int64{18000, 604800, 2592000} {
		for _, used := range []float64{.001, .01, .02, .03, .10} {
			for _, reset := range []bool{false, true} {
				w, p, facts, _ := quotaModelFixture()
				w.Seconds = seconds
				p.Samples = p.Samples[:3]
				p.Samples[0].Used = used / 4
				if reset {
					p.Samples[0].Used = .8
				}
				p.Samples[1].Used, p.Samples[2].Used = used/2, used
				price := quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 8}})
				dto := quotaBuildPeriod(w, p, facts[:2], price)
				total, remaining := quotaEstimate(dto)
				cost := 16.0
				if reset {
					cost = 8
				}
				quotaModelAssert(t, total, cost/used)
				quotaModelAssert(t, remaining, cost/used-cost)
			}
		}
	}
}
