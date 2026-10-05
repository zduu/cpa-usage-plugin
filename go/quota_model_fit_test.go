package main

import (
	"testing"
	"time"
)

func quotaMixedFixture() (quotaWindow, *quotaPeriod, []quotaFact, time.Time) {
	w, p, _, _ := quotaModelFixture()
	p.Samples = []quotaObservation{{ObservedAt: p.Start, Used: .1, CollectionStartedAt: p.CollectionStartedAt}}
	var facts []quotaFact
	used := .1
	// Every interval uses both models, with different proportions.
	for i, pair := range [][2]int64{{100_000, 100_000}, {200_000, 100_000}, {100_000, 300_000}, {300_000, 200_000}} {
		pair[0] *= 2
		pair[1] *= 2
		for j, model := range []string{"a", "b"} {
			at := p.Start.Add(time.Duration(i*10+j+1) * time.Minute)
			facts = append(facts, quotaFact{Provider: w.Provider, AuthIndex: w.AuthIndex, AuthID: w.AuthID, Model: model,
				Timestamp: at, CompletedAt: at.Add(time.Second), Tokens: TokenStats{InputTokens: pair[j], TotalTokens: pair[j]}})
		}
		// A consumes 10% per million tokens; B consumes 20%.
		used += float64(pair[0])/1e6*.1 + float64(pair[1])/1e6*.2
		p.Samples = append(p.Samples, quotaObservation{ObservedAt: p.Start.Add(time.Duration((i+1)*10) * time.Minute), Used: used, CollectionStartedAt: p.CollectionStartedAt})
	}
	return w, p, facts, p.Samples[len(p.Samples)-1].ObservedAt
}

func TestQuotaMixedModelsIdentifyDifferentCapacitiesWithoutExclusiveIntervals(t *testing.T) {
	w, p, facts, now := quotaMixedFixture()
	rows := quotaModelTestResult(w, p, facts, quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 8}, "b": {Prompt: 40}}), now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 10_000_000)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 5_000_000)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 80)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalUSD, 200)
	rows = quotaModelTestResult(w, p, facts, quotaModelTestPrice(map[string]ModelPrice{"a": {Prompt: 16}}), now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 10_000_000)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalUSD, 160)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 5_000_000)
	if rows["b"].ModelOnlyTotalUSD != nil {
		t.Fatal("a missing model price must not prevent token fitting or become zero dollars")
	}
}

func TestQuotaMixedModelsRejectFixedProportionsAndUncertainFits(t *testing.T) {
	for _, name := range []string{"fixed", "almost-fixed", "noisy", "too-few", "crossing", "stale"} {
		t.Run(name, func(t *testing.T) {
			w, p, facts, now := quotaMixedFixture()
			switch name {
			case "fixed", "almost-fixed":
				for i := 0; i < len(facts); i += 2 {
					facts[i+1].Tokens = facts[i].Tokens
					if name == "almost-fixed" {
						facts[i+1].Tokens.InputTokens += int64(i)
						facts[i+1].Tokens.TotalTokens += int64(i)
					}
				}
			case "noisy":
				p.Samples = p.Samples[:4]
				facts = facts[:6]
				p.Samples[1].Used, p.Samples[2].Used, p.Samples[3].Used = .11, .8, .81
			case "too-few":
				p.Samples, facts = p.Samples[:3], facts[:4]
			case "crossing":
				facts[0].CompletedAt = now.Add(time.Minute)
			case "stale":
				now = now.Add(2 * time.Hour)
			}
			rows := quotaModelTestResult(w, p, facts, nil, now, true)
			for model, row := range rows {
				if row.ModelOnlyTotalTokens != nil || row.ModelOnlyTotalUSD != nil {
					t.Fatalf("%s received an unsupported estimate", model)
				}
			}
		})
	}
}

func TestQuotaMixedFitUsesNonnegativeCoefficients(t *testing.T) {
	var intervals []quotaModelInterval
	for _, pair := range [][2]float64{{1, 1}, {2, 1}, {1, 3}, {3, 2}} {
		intervals = append(intervals, quotaModelInterval{delta: .1*pair[0] - .001*pair[1], models: map[string]quotaIntervalModel{
			"a": {tokens: pair[0] * 1e6}, "b": {tokens: pair[1] * 1e6},
		}})
	}
	fit := fitQuotaMixedModels(intervals, quotaModelMinDelta)
	if _, ok := fit["b"]; ok {
		t.Fatal("negative quota coefficient was turned into a model capacity")
	}
	if fit["a"].tokens <= 0 {
		t.Fatal("valid positive model coefficient was lost")
	}
}

func TestQuotaMixedFitScalesSmallModelVolumes(t *testing.T) {
	w, p, facts, now := quotaMixedFixture()
	for i := 1; i < len(facts); i += 2 {
		facts[i].Tokens.InputTokens /= 1000
		facts[i].Tokens.TotalTokens /= 1000
	}
	rows := quotaModelTestResult(w, p, facts, nil, now, true)
	quotaModelAssert(t, rows["a"].ModelOnlyTotalTokens, 10_000_000)
	quotaModelAssert(t, rows["b"].ModelOnlyTotalTokens, 5_000)
}

func TestQuotaMixedPerfectFitStillHasQuantizationUncertainty(t *testing.T) {
	w, p, facts, now := quotaMixedFixture()
	rows := quotaModelTestResult(w, p, facts, nil, now, true)
	for _, model := range []string{"a", "b"} {
		row := rows[model]
		if row.ModelOnlyTokensLow == nil || row.ModelOnlyTokensHigh == nil ||
			*row.ModelOnlyTokensLow >= *row.ModelOnlyTotalTokens || *row.ModelOnlyTokensHigh <= *row.ModelOnlyTotalTokens {
			t.Fatal("perfect rounded fit incorrectly reported zero uncertainty")
		}
	}
	// The same exact proportions with only one tenth the quota signal do
	// not establish either model's capacity.
	for i := range p.Samples {
		p.Samples[i].Used = .1 + (p.Samples[i].Used-.1)/10
	}
	rows = quotaModelTestResult(w, p, facts, nil, now, true)
	if rows["a"].ModelOnlyTotalTokens != nil || rows["b"].ModelOnlyTotalTokens != nil {
		t.Fatal("tiny mixed signal produced a capacity")
	}
}

func TestQuotaMixedFitUsesSameThresholdForAllWindows(t *testing.T) {
	w, p, facts, now := quotaMixedFixture()
	// A contributes 7 points, sufficient in both short and weekly windows.
	for i := range p.Samples {
		p.Samples[i].Used = .1 + (p.Samples[i].Used-.1)/2
	}
	w.Seconds = 604800
	rows := quotaModelTestResult(w, p, facts, nil, now, true)
	for _, model := range []string{"a", "b"} {
		if rows[model].ModelOnlyTotalTokens == nil || rows[model].ModelOnlyTokensHigh == nil {
			t.Fatal("identifiable weekly model lacked an early reference range")
		}
	}
	w.Seconds = 18000
	rows = quotaModelTestResult(w, p, facts, nil, now, true)
	if rows["a"].ModelOnlyTotalTokens == nil {
		t.Fatal("short-window fit still requires ten points")
	}
}
