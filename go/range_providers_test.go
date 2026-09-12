package main

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestRangeClientProviderCountersMatchIncrementalStatistics(t *testing.T) {
	now := time.Now()
	s := NewRequestStatistics()
	s.maxDetailsPerModel = 2
	providers := []string{" OpenAI ", "openai", "Anthropic", "", "Σ", "ς", "α", "Beta", "Gamma", "Delta", "Theta", "Omega"}
	for client := 0; client < 3; client++ {
		for model, count := range map[string]int{"one-provider": 2, "few-providers": 6, "many-providers": len(providers)} {
			for i := 0; i < 3*count; i++ {
				d := RequestDetail{Model: model, Provider: providers[i%count], APIKey: "masked***client", APIKeyHash: fmt.Sprint(client),
					Timestamp: now.Add(time.Duration(i-1000) * time.Second), Failed: i%3 == 0,
					Tokens: TokenStats{InputTokens: 11, OutputTokens: 7, ReasoningTokens: 3, CachedTokens: 2, CacheWriteTokens: 5}}
				s.recordDetailLocked("upstream", model, d, requestDedupKey{}, now, false)
			}
		}
	}
	s.pruneLocked(now, true)
	// The independent incremental path uses provider maps and includes the
	// same archived rows; range queries use the new temporary slice path.
	want := s.SummaryWithoutDetailsAt(now).ClientAPIStats
	got := s.SummaryWithoutDetailsForRangeAt("24h", now).ClientAPIStats
	normalize := func(stats []ClientAPIStat) {
		sort.Slice(stats, func(i, j int) bool { return stats[i].Selector < stats[j].Selector })
		for i := range stats {
			sort.Slice(stats[i].Models, func(a, b int) bool { return stats[i].Models[a].Model < stats[i].Models[b].Model })
			for j := range stats[i].Models {
				// The first encountered display spelling can differ: range
				// scans visit visible rows before the archived rows, whereas
				// incremental aggregates follow arrival order. Identity and
				// counters must nevertheless match exactly.
				providers := stats[i].Models[j].Providers
				for k := range providers {
					providers[k].Provider = modelProviderStatsKey(providers[k].Provider)
				}
				sortRangeModelProviders(providers)
			}
		}
	}
	normalize(want)
	normalize(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("range provider counters differ from independent incremental counters:\ngot=%+v\nwant=%+v", got, want)
	}
	if len(got) != 3 || got[0].TotalRequests != 60 || len(got[0].Models) != 3 {
		t.Fatalf("client identity or request counts changed: %+v", got)
	}
	// Caller changes must not mutate the range cache, incremental counters,
	// or another response sharing the temporary provider storage.
	got[0].Models[0].Providers[0].TotalRequests = -1
	got[0].Models[0].Providers[0].Provider = "mutated"
	again := s.SummaryWithoutDetailsForRangeAt("24h", now).ClientAPIStats
	normalize(again)
	if !reflect.DeepEqual(again, want) {
		t.Fatal("caller mutation escaped into the cached provider statistics")
	}
}
