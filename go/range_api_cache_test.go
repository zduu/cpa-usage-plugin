package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"
)

func assertRangeAPIMatchesScan(t *testing.T, s *RequestStatistics, api, rangeKey, client string, now time.Time) {
	t.Helper()
	got := s.QueryAPIDetailForClientAPIAt(api, rangeKey, client, 23, 1000, now)
	want := s.referenceAPIDetailForClientAPIAt(api, rangeKey, client, 23, 1000, now)
	canonical := func(response APIDetailResponse) any {
		sort.Slice(response.ModelStats, func(i, j int) bool { return response.ModelStats[i].Model < response.ModelStats[j].Model })
		sort.Slice(response.SourceStats, func(i, j int) bool { return response.SourceStats[i].Source < response.SourceStats[j].Source })
		sort.Slice(response.ErrorStats, func(i, j int) bool {
			if response.ErrorStats[i].StatusCode != response.ErrorStats[j].StatusCode {
				return response.ErrorStats[i].StatusCode < response.ErrorStats[j].StatusCode
			}
			return response.ErrorStats[i].Failure < response.ErrorStats[j].Failure
		})
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	assertRangeJSONEqual(t, api+"/"+rangeKey+"/"+client, canonical(got), canonical(want))
}

func TestRangeBlockAPIDetailMatchesIndependentScan(t *testing.T) {
	s, now := rangeCacheFixture(t)
	client := clientAPISelectorForStat(ClientAPIStat{APIKeyHash: "hash-2", APIKey: "client-2"})
	for _, api := range []string{"openai · production", "claude · production", "missing"} {
		for _, rangeKey := range []string{"7h", "24h", "7d", "all"} {
			for _, selector := range []string{"", client, "invalid selector"} {
				for pass := 0; pass < 2; pass++ {
					assertRangeAPIMatchesScan(t, s, api, rangeKey, selector, now)
				}
			}
		}
	}
	api := "openai · production"
	m := s.apis[api].Models["alias-0"]
	for _, index := range []int{1000, len(m.Details)} {
		detail := m.accountingDetailAt(index)
		detail.Timestamp = now.Add(time.Minute)
		detail.Failed, detail.StatusCode, detail.Failure = true, 503, "changed after cache admission"
		m.setAccountingDetailAt(index, detail)
	}
	assertRangeAPIMatchesScan(t, s, api, "7d", "", now)
	if _, err := s.UpsertModelPrice("model-0", ModelPrice{Prompt: 8, Completion: 16}); err != nil {
		t.Fatal(err)
	}
	assertRangeAPIMatchesScan(t, s, api, "7d", client, now)
	s.retention = 2 * 24 * time.Hour
	s.pruneLocked(now, true)
	assertRangeAPIMatchesScan(t, s, api, "all", "", now)
}

func TestRangeBlockAPIDetailPreservesFirstEmptyProviderAndUnsortedRows(t *testing.T) {
	s := NewRequestStatistics()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	m := &modelStats{}
	s.apis["api"] = &apiStats{Models: map[string]*modelStats{"model": m}}
	for i := 0; i < 5000; i++ {
		provider := ""
		if i >= 100 {
			provider = "later"
		}
		m.Details = append(m.Details, RequestDetail{Model: "model", Provider: provider, Source: "shared",
			Timestamp: now.Add(-time.Duration((i*29)%5000) * time.Second),
			Failed:    i%17 == 0, Failure: fmt.Sprintf("failure-%d", i%7), Tokens: TokenStats{InputTokens: int64(i + 1)}})
	}
	s.rebuildAggregatesLocked()
	for pass := 0; pass < 2; pass++ {
		assertRangeAPIMatchesScan(t, s, "api", "24h", "", now)
		result := s.QueryAPIDetailAt("api", "24h", 23, 1000, now)
		if len(result.SourceStats) != 1 || result.SourceStats[0].Provider != "" {
			t.Fatalf("first source provider changed: %+v", result.SourceStats)
		}
	}
}

func TestRangeBlockAPIDetailRecentEventTimestampTies(t *testing.T) {
	s := NewRequestStatistics()
	s.retention = 0
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	m := &modelStats{}
	s.apis["api"] = &apiStats{Models: map[string]*modelStats{"model": m}}
	for i := 0; i < 5000; i++ {
		m.Details = append(m.Details, RequestDetail{Model: fmt.Sprintf("model-%d", i%3),
			Timestamp: now.Add(time.Duration(i/1000-4) * time.Second),
			APIKey:    fmt.Sprintf("client-%d", i%2), APIKeyHash: fmt.Sprintf("hash-%d", i%2),
			Tokens: TokenStats{InputTokens: int64(i + 1)}})
	}
	s.rebuildAggregatesLocked()
	client := clientAPISelectorForStat(ClientAPIStat{APIKeyHash: "hash-1", APIKey: "client-1"})
	for _, selector := range []string{"", client} {
		for pass := 0; pass < 2; pass++ {
			assertRangeAPIMatchesScan(t, s, "api", "7h", selector, now)
		}
	}
}

func TestRangeBlockAPIDetailCachedResultOwnership(t *testing.T) {
	s, now := rangeCacheFixture(t)
	api := "openai · production"
	m := s.apis[api].Models["alias-0"]
	detail := m.Details[len(m.Details)-1]
	detail.Timestamp = now
	detail.Headers = map[string][]string{"x-request-id": {"original"}}
	detail.Correlation = &ProtocolCorrelationMeta{SchemaVersion: 1, KnownFields: 1}
	m.setAccountingDetailAt(len(m.Details)-1, detail)
	// Capture an independently owned expected value before any caller mutation.
	want := s.QueryAPIDetailAt(api, "7d", 23, 1000, now)
	before := s.rangeAggregates.hits
	got := s.QueryAPIDetailAt(api, "7d", 23, 1000, now)
	if s.rangeAggregates.hits <= before || len(got.ModelStats) == 0 || len(got.RecentEvents) == 0 {
		t.Fatal("fixture did not hit the API detail cache")
	}
	got.ModelStats[0].Providers[0].InputTokens = 123456789
	got.ModelStats[0].Providers[0].Provider = "caller mutation"
	got.SourceStats[0].Source = "caller mutation"
	got.ErrorStats[0].Failure = "caller mutation"
	got.RecentEvents[0].Headers["x-request-id"][0] = "caller mutation"
	got.RecentEvents[0].Correlation.KnownFields = 0
	*got.RecentEvents[0].CostUSD = 123456789
	decode := func(value APIDetailResponse) any {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var result any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	assertRangeJSONEqual(t, "ownership", decode(s.QueryAPIDetailAt(api, "7d", 23, 1000, now)), decode(want))
}

func TestRangeBlockBenchmarkWritesToQueriedGroup(t *testing.T) {
	s := buildBenchmarkStats(1000)
	const api = "openai · openai-prod"
	m := s.apis[api].Models["gpt-4.1"]
	before := len(m.Details)
	s.Record(UsageRecord{Provider: "openai", Source: "openai-prod", Model: "gpt-4.1", RequestedAt: time.Now(), Detail: UsageDetail{InputTokens: 10}})
	if len(m.Details) != before+1 || len(s.apis) != 4 {
		t.Fatal("after-record benchmarks must write to their existing queried group")
	}
}

func TestRangeBlockRepricingFailureInvalidatesBothQueryCaches(t *testing.T) {
	s, now := rangeCacheFixture(t)
	assertRangeCacheMatchesScan(t, s, now, "7d", "")
	assertRangeAPIMatchesScan(t, s, "openai · production", "7d", "", now)
	s.priceStoragePath = t.TempDir() // replacing a directory with the price file fails
	if _, err := s.UpsertModelPrice("model-0", ModelPrice{Prompt: 99}); err == nil {
		t.Fatal("expected a price persistence error")
	}
	assertRangeCacheMatchesScan(t, s, now, "7d", "")
	assertRangeAPIMatchesScan(t, s, "openai · production", "7d", "", now)
}

func BenchmarkRangeSummaryBlockCache(b *testing.B) {
	for _, mode := range []string{"cold", "warm", "after-record"} {
		b.Run(mode, func(b *testing.B) {
			s := buildBenchmarkStats(100000)
			now := time.Now()
			if mode != "cold" {
				_ = s.SummaryWithoutDetailsForRangeAt("7d", now)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "cold" {
					s.invalidateRangeAggregatesLocked()
				}
				if mode == "after-record" {
					s.Record(UsageRecord{Provider: "openai", Source: "openai-prod", Model: "gpt-4.1", RequestedAt: now, Detail: UsageDetail{InputTokens: 10}})
				}
				clearBenchmarkSummaryRangeCache(s)
				_ = s.SummaryWithoutDetailsForRangeAt("7d", now)
			}
		})
	}
}

func BenchmarkAPIDetailBlockCache(b *testing.B) {
	for _, mode := range []string{"cold", "warm", "after-record"} {
		b.Run(mode, func(b *testing.B) {
			s := buildBenchmarkStats(100000)
			now := time.Now()
			if mode != "cold" {
				_ = s.QueryAPIDetailAt("openai · openai-prod", "7d", 120, 20, now)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "cold" {
					s.invalidateRangeAggregatesLocked()
				}
				if mode == "after-record" {
					s.Record(UsageRecord{Provider: "openai", Source: "openai-prod", Model: "gpt-4.1", RequestedAt: now, Detail: UsageDetail{InputTokens: 10}})
				}
				_ = s.QueryAPIDetailAt("openai · openai-prod", "7d", 120, 20, now)
			}
		})
	}
}
