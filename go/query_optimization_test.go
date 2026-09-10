package main

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestOptimizedAPIDetailRecentOrderAndAccounting(t *testing.T) {
	s := NewRequestStatistics()
	s.maxDetailsPerModel = 80
	now := time.Now().UTC()
	// Late arrivals, equal timestamps and archived history must all count,
	// while the recent list contains only retained details in stable order.
	for i := 0; i < 360; i++ {
		s.Record(UsageRecord{Provider: "test", Model: fmt.Sprintf("model-%d", i%3),
			RequestedAt: now.Add(-time.Duration(i%11) * time.Minute),
			Detail:      UsageDetail{InputTokens: int64(i + 1)}})
	}
	for api := range s.apis {
		for _, rangeKey := range []string{"all", "24h"} {
			want := s.QueryEventsAt(EventsQuery{API: api, Range: rangeKey, Limit: 120}, now)
			for attempt := 0; attempt < 10; attempt++ {
				got := s.QueryAPIDetailAt(api, rangeKey, 120, 20, now)
				if got.TotalEvents != 360 || got.Summary.TotalRequests != 360 {
					t.Fatalf("archive lost: total=%d summary=%d", got.TotalEvents, got.Summary.TotalRequests)
				}
				if !reflect.DeepEqual(got.RecentEvents, want.Events) {
					t.Fatalf("recent ordering differs from indexed query (range=%s attempt=%d)", rangeKey, attempt)
				}
			}
		}
	}
}

func TestOptimizedAPIDetailKeepsFirstSourceProvider(t *testing.T) {
	s := NewRequestStatistics()
	now := time.Now()
	m := &modelStats{Details: []RequestDetail{
		{Timestamp: now.Add(-time.Minute), Source: "shared", Provider: "first"},
		{Timestamp: now, Source: "shared", Provider: "second"},
	}}
	s.apis["api"] = &apiStats{Models: map[string]*modelStats{"model": m, "nil-model": nil}}
	got := s.QueryAPIDetailAt("api", "24h", 10, 10, now)
	if len(got.SourceStats) != 1 || got.SourceStats[0].Provider != "first" {
		t.Fatalf("recent-first selection changed source aggregation: %+v", got.SourceStats)
	}
}

func TestQueryDetailPricerMatchesReference(t *testing.T) {
	s := NewRequestStatistics()
	s.pricingLocation = time.FixedZone("test-offset", 8*60*60)
	s.modelPrices = map[string]ModelPrice{
		"model": {Prompt: 3, Completion: 7, Cache: 1, CacheWrite: 2},
		"claude/model": {Prompt: 6, Completion: 8, Cache: 2, CacheWrite: 4,
			TimeRules: []ModelPriceRule{{Name: "free", Start: "08:00", End: "09:00", Prompt: floatPtrForTest(0)}}},
	}
	s.modelsDevPrices = map[string]ModelPrice{"fallback": {Prompt: 11}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for version := 0; version < 2; version++ {
		pricer := queryDetailPricer{stats: s}
		for _, model := range []string{"model", "fallback", "missing", "model"} {
			for _, provider := range []string{"openai", "claude", "claude", " OpenAI "} {
				for _, synthetic := range []bool{false, true} {
					for _, hour := range []int{0, 1, 0} {
						d := RequestDetail{Model: model, Provider: provider, TimestampSynthetic: synthetic,
							Timestamp: time.Date(2026, 9, 9, hour, 30, 0, 0, time.UTC),
							Tokens:    TokenStats{InputTokens: 100, OutputTokens: 20, CachedTokens: 10, CacheWriteTokens: 5}}
						totals := detailTotalsFromRequest(d)
						if got, want := pricer.cost("parent", d, totals), s.detailCostLocked("parent", d, totals); got != want {
							t.Fatalf("version=%d model=%s provider=%s hour=%d synthetic=%t: got=%g want=%g", version, model, provider, hour, synthetic, got, want)
						}
					}
				}
			}
		}
		// A new query after repricing must not use the preceding query's lookup.
		s.modelPrices["model"] = ModelPrice{Prompt: 100}
		s.modelPriceIndex = normalizedModelPriceIndex(s.modelPrices)
	}
}

func TestExportSingleCloneOwnsMutableMetadata(t *testing.T) {
	s := NewRequestStatistics()
	now := time.Now()
	m := &modelStats{Details: []RequestDetail{{Model: "model", Timestamp: now,
		Headers:     map[string][]string{"X-Test": {"original"}},
		Correlation: &ProtocolCorrelationMeta{InputMode: "original"},
		Tokens:      TokenStats{InputTokens: 10}}}}
	s.apis["api"] = &apiStats{Models: map[string]*modelStats{"model": m}}
	frozen := s.captureEventExport(EventsQuery{}, 0, now)
	if len(frozen.result.Events) != 1 || frozen.result.Events[0].CostUSD == nil {
		t.Fatal("missing frozen record or price")
	}
	want := cloneRequestDetails(frozen.result.Events)
	m.Details[0].Headers["X-Test"][0] = "live mutation"
	m.Details[0].Correlation.InputMode = "live mutation"
	other := s.captureEventExport(EventsQuery{}, 0, now)
	other.result.Events[0].Headers["X-Test"][0] = "other export mutation"
	other.result.Events[0].Correlation.InputMode = "other export mutation"
	*other.result.Events[0].CostUSD = 100
	if !reflect.DeepEqual(frozen.result.Events, want) {
		t.Fatal("snapshot aliases live records or another export")
	}
}

func TestAccountingExpiryReleasesBurstCapacity(t *testing.T) {
	s := NewRequestStatistics()
	s.maxDetailsPerModel = 1
	now := time.Now()
	for i := 0; i < 4096; i++ {
		at := now.Add(-48 * time.Hour)
		if i >= 4000 {
			at = now
		}
		s.Record(UsageRecord{Provider: "test", Model: "model", RequestedAt: at, Detail: UsageDetail{InputTokens: 1}})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retention = 24 * time.Hour
	s.pruneLocked(now, true)
	for _, api := range s.apis {
		m := api.Models["model"]
		if m == nil || m.accounting.count != 95 || m.accounting.capacity() > accountingBlockRecords || m.TotalRequests != 96 {
			t.Fatalf("expired ledger capacity/counters not compacted: %+v", m)
		}
		if len(m.accountingIdentities) != 1 || !s.nextDetailExpiry.Equal(now.Add(24*time.Hour)) {
			t.Fatal("compaction changed identity dictionary or expiry scheduling")
		}
	}
	s.pruneLocked(now.Add(25*time.Hour), true)
	if s.totalRequests != 0 {
		t.Fatalf("remaining requests after expiry = %d", s.totalRequests)
	}
}

func BenchmarkEventExportSnapshotCopies100k(b *testing.B) {
	s := buildBenchmarkStats(100000)
	now := time.Now()
	// Warm the shared index so this compares snapshot ownership allocations.
	s.captureEventExport(EventsQuery{}, 0, now)
	for _, extraCopy := range []bool{true, false} {
		b.Run(fmt.Sprintf("extra_copy=%t", extraCopy), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				frozen := s.captureEventExport(EventsQuery{}, 0, now)
				if extraCopy {
					frozen.result.Events = cloneRequestDetails(frozen.result.Events)
				}
				runtime.KeepAlive(frozen)
			}
		})
	}
}
