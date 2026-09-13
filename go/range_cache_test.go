package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func rangeCacheFixture(t *testing.T) (*RequestStatistics, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 13, 12, 34, 56, 789, time.UTC)
	s := NewRequestStatistics()
	s.retention, s.dedupWindow = 0, 0
	s.maxDetailsPerModel = 5000
	s.priceStoragePath = filepath.Join(t.TempDir(), "prices.json")
	s.pricingLocation = time.FixedZone("pricing", 8*3600)
	s.pricingTimezone = "Asia/Shanghai"
	zones := []*time.Location{time.UTC, time.FixedZone("east", 8*3600), time.FixedZone("west", -7*3600), time.FixedZone("half", 19800)}
	for i := 0; i < 32000; i++ {
		provider := []string{"openai", "claude"}[i%2]
		group := fmt.Sprintf("alias-%d", (i/2)%2)
		api := provider + " · production"
		if s.apis[api] == nil {
			s.apis[api] = &apiStats{Models: make(map[string]*modelStats)}
		}
		if s.apis[api].Models[group] == nil {
			s.apis[api].Models[group] = &modelStats{}
		}
		detail := RequestDetail{
			Model: fmt.Sprintf("model-%d", (i/2)%2), Provider: provider,
			Timestamp: now.Add(-10*24*time.Hour + time.Duration(i)*27*time.Second).In(zones[i%len(zones)]),
			Source:    provider + fmt.Sprintf("-source-%d", i%7), AuthIndex: fmt.Sprintf("auth-%d", i%11),
			APIKey: fmt.Sprintf("client-%d", i%5), APIKeyHash: fmt.Sprintf("hash-%d", i%5),
			LatencyMs: int64(i % 67), TTFTMs: int64(i % 17), Failed: i%19 == 0,
			Tokens: TokenStats{InputTokens: int64(20 + i%79), OutputTokens: int64(i % 41), CachedTokens: int64(i % 13), CacheWriteTokens: int64(i % 9), ReasoningTokens: int64(i % 3)},
		}
		if i%23 == 0 {
			detail.APIKeyHash = "" // legacy label coalescing is finalized after all blocks
		}
		if i%31 == 0 {
			detail.TimestampSynthetic = true
		}
		s.apis[api].Models[group].Details = append(s.apis[api].Models[group].Details, detail)
	}
	s.rebuildAggregatesLocked()
	s.pruneLocked(now, true)
	for _, model := range []string{"model-0", "model-1", "claude/model-0"} {
		if _, err := s.UpsertModelPrice(model, ModelPrice{Prompt: 1.25, Completion: 7, Cache: 0.1, CacheWrite: 2,
			TimeRules: []ModelPriceRule{{Name: "weekday overnight", Days: []int{1, 2, 3, 4, 5}, Start: "22:00", End: "06:00", Prompt: floatPtrForTest(0), Completion: floatPtrForTest(3)}}}); err != nil {
			t.Fatal(err)
		}
	}
	return s, now
}

func canonicalRangeSummary(summary DashboardSummary) DashboardSummary {
	sort.Slice(summary.ModelStats, func(i, j int) bool { return summary.ModelStats[i].Model < summary.ModelStats[j].Model })
	sort.Slice(summary.SourceStats, func(i, j int) bool { return summary.SourceStats[i].Source < summary.SourceStats[j].Source })
	sort.Slice(summary.CredentialStats, func(i, j int) bool {
		return summary.CredentialStats[i].AuthIndex < summary.CredentialStats[j].AuthIndex
	})
	sort.Slice(summary.ClientAPIStats, func(i, j int) bool {
		a, b := summary.ClientAPIStats[i], summary.ClientAPIStats[j]
		if a.APIKeyHash != b.APIKeyHash {
			return a.APIKeyHash < b.APIKeyHash
		}
		return a.APIKey < b.APIKey
	})
	for i := range summary.ClientAPIStats {
		models := summary.ClientAPIStats[i].Models
		sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })
	}
	return summary
}

// Compare integer JSON exactly (including values above 2^53). Only floating
// results allow the rounding error introduced by adding complete blocks.
func assertRangeJSONEqual(t *testing.T, path string, got, want any) {
	t.Helper()
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok || len(actual) != len(expected) {
			t.Fatalf("%s object differs: got %v, want %v", path, got, want)
		}
		for key, value := range expected {
			assertRangeJSONEqual(t, path+"."+key, actual[key], value)
		}
	case []any:
		actual, ok := got.([]any)
		if !ok || len(actual) != len(expected) {
			t.Fatalf("%s array differs: got %v, want %v", path, got, want)
		}
		for i, value := range expected {
			assertRangeJSONEqual(t, fmt.Sprintf("%s[%d]", path, i), actual[i], value)
		}
	case json.Number:
		actual, ok := got.(json.Number)
		if ok && actual == expected {
			return
		}
		if !ok || (!strings.ContainsAny(string(actual), ".eE") && !strings.ContainsAny(string(expected), ".eE")) {
			t.Fatalf("%s integer differs: got %v, want %v", path, got, want)
		}
		a, _ := actual.Float64()
		b, _ := expected.Float64()
		if math.Abs(a-b) > math.Max(math.Abs(a), math.Abs(b))*2e-12+1e-18 {
			t.Fatalf("%s number differs: got %v, want %v", path, got, want)
		}
	default:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s differs: got %v, want %v", path, got, want)
		}
	}
}

func assertRangeCacheMatchesScan(t *testing.T, s *RequestStatistics, now time.Time, rangeKey, client string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff, window := dashboardRangeCutoff(rangeKey, now), summaryHealthWindow(now)
	got := canonicalRangeSummary(s.buildSummaryWithoutDetailsForRangeLocked(now, window, cutoff, client))
	want := canonicalRangeSummary(s.referenceRangeSummaryLocked(now, window, cutoff, client))
	decode := func(value any) any {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	assertRangeJSONEqual(t, rangeKey+"/"+client, decode(got), decode(want))
}

func TestRangeBlockSummaryMatchesIndependentScan(t *testing.T) {
	s, now := rangeCacheFixture(t)
	selector := clientAPISelectorForStat(ClientAPIStat{APIKeyHash: "hash-2", APIKey: "client-2"})
	for _, client := range []string{"", selector, "invalid selector"} {
		for _, rangeKey := range []string{"7h", "24h", "7d", "30d", "all"} {
			for pass := 0; pass < 2; pass++ {
				assertRangeCacheMatchesScan(t, s, now, rangeKey, client)
			}
		}
	}
	if s.rangeAggregates.hits == 0 || s.rangeAggregates.bytes == 0 || s.rangeAggregates.bytes > rangeAggregateCacheBytes {
		t.Fatalf("cache did not exercise bounded reuse: %+v", s.rangeAggregates)
	}
	before := s.rangeAggregates.scannedRows
	assertRangeCacheMatchesScan(t, s, now, "all", "")
	if scanned := s.rangeAggregates.scannedRows - before; scanned >= 32000/4 {
		t.Fatalf("warm complete blocks scanned %d records", scanned)
	}
}

func TestRangeBlockInvalidationAndFrozenAccounting(t *testing.T) {
	s, now := rangeCacheFixture(t)
	assertRangeCacheMatchesScan(t, s, now, "all", "")
	m := s.apis["openai · production"].Models["alias-0"]
	frozen := m.accounting.freeze()
	old := frozen[0][0]
	for _, index := range []int{2000, len(m.Details)} {
		detail := m.accountingDetailAt(index)
		detail.Tokens.InputTokens += 1900
		detail.LatencyMs += 100
		m.setAccountingDetailAt(index, detail)
	}
	assertRangeCacheMatchesScan(t, s, now, "all", "")
	if !reflect.DeepEqual(frozen[0][0], old) {
		t.Fatal("range invalidation changed a frozen snapshot")
	}
	m.removeAccountingDetailAt(1300)
	m.removeAccountingDetailAt(len(m.Details) + 1)
	assertRangeCacheMatchesScan(t, s, now, "7d", "")
	assertRangeCacheMatchesScan(t, s, now, "all", "")

	// Force rolling trimming, allocation rebasing and an out-of-order insert
	// after blocks have been admitted. Real duplicate rows remain independent.
	for i := 0; i < 6500; i++ {
		detail := m.Details[len(m.Details)-1]
		detail.Timestamp = now.Add(time.Duration(i) * time.Second)
		if i%17 == 0 {
			detail.Timestamp = now.Add(-4 * 24 * time.Hour)
		}
		m.appendDetail(detail, false)
		s.trimModelDetailsLocked(m)
		if i%1300 == 0 {
			assertRangeCacheMatchesScan(t, s, now, "7d", "")
		}
	}
	assertRangeCacheMatchesScan(t, s, now, "all", "")
	s.retention = 3 * 24 * time.Hour
	s.pruneLocked(now, true)
	assertRangeCacheMatchesScan(t, s, now, "all", "")
}

func TestRangeBlockRepricingAndResultOwnership(t *testing.T) {
	s, now := rangeCacheFixture(t)
	assertRangeCacheMatchesScan(t, s, now, "7d", "")
	got := s.SummaryWithoutDetailsForRangeAt("7d", now)
	got.Usage.RequestsByDay["2099-01-01"] = 900
	got.ClientAPIStats[0].Models[0].Providers[0].InputTokens = 900
	got.ModelStats[0].Providers[0].Provider = "caller mutation"
	assertRangeCacheMatchesScan(t, s, now, "7d", "")
	if _, err := s.UpsertModelPrice("model-0", ModelPrice{Prompt: 20, Completion: 30}); err != nil {
		t.Fatal(err)
	}
	assertRangeCacheMatchesScan(t, s, now, "7d", "")
	if _, err := s.DeleteModelPrice("claude/model-0"); err != nil {
		t.Fatal(err)
	}
	assertRangeCacheMatchesScan(t, s, now, "all", "")
}

func TestRangeBlockCacheBudgetAndInvalidEntryCollection(t *testing.T) {
	s, now := rangeCacheFixture(t)
	_ = s.rangeSummaryLocked(time.Time{}, "")
	cache := &s.rangeAggregates
	for i := 0; i < 5000; i++ {
		key := rangeAggregateKey{block: &rangeBlockIdentity{valid: true}, clientAPI: fmt.Sprint(i)}
		cache.retain(key, newRangeSummaryAccumulator())
	}
	if cache.bytes > rangeAggregateCacheBytes || len(cache.entries) > rangeAggregateCacheEntries {
		t.Fatalf("cache exceeded budget: bytes=%d entries=%d", cache.bytes, len(cache.entries))
	}
	for key := range cache.entries {
		key.block.valid = false
	}
	cache.sweepInvalid()
	if len(cache.entries) != 0 || cache.bytes != 0 {
		t.Fatalf("invalid blocks retained: bytes=%d entries=%d", cache.bytes, len(cache.entries))
	}
	s.invalidateRangeAggregatesLocked()
	assertRangeCacheMatchesScan(t, s, now, "7d", "")
}

func TestRangeBlockSortWithoutEvictionInvalidatesCachedPositions(t *testing.T) {
	s := NewRequestStatistics()
	s.retention, s.maxDetailsPerModel = 0, 5000
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	m := &modelStats{}
	s.apis["api"] = &apiStats{Models: map[string]*modelStats{"model": m}}
	// Each block is ordered, but the later block precedes the earlier one.
	for _, age := range []int{2048, 4096} {
		for i := 0; i < 2048; i++ {
			m.Details = append(m.Details, RequestDetail{Model: "model", Provider: "provider",
				Timestamp: now.Add(-time.Duration(age-i) * 7 * time.Second), Tokens: TokenStats{InputTokens: int64(age + i)}})
		}
	}
	s.rebuildAggregatesLocked()
	assertRangeCacheMatchesScan(t, s, now, "all", "")
	assertRangeAPIMatchesScan(t, s, "api", "all", "", now)
	if m.rangeDetailsAreOrdered() {
		t.Fatal("fixture must start out of order")
	}
	s.pruneLocked(now, true)
	if s.evictedTotal != 0 || len(m.Details) != 4096 {
		t.Fatal("sort-only fixture unexpectedly removed records")
	}
	for pass := 0; pass < 2; pass++ {
		assertRangeCacheMatchesScan(t, s, now, "7h", "")
		assertRangeAPIMatchesScan(t, s, "api", "7h", "", now)
	}
}

func TestRangeBlockCacheReadmitsAfterBudgetPressure(t *testing.T) {
	for _, apiDetail := range []bool{false, true} {
		t.Run(fmt.Sprintf("api_detail_%t", apiDetail), func(t *testing.T) {
			s := NewRequestStatistics()
			s.retention = 0
			now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
			m := &modelStats{}
			s.apis["api"] = &apiStats{Models: map[string]*modelStats{"model": m}}
			for i := 0; i < 2048; i++ {
				m.Details = append(m.Details, RequestDetail{Model: "model", Timestamp: now.Add(-time.Duration(i) * time.Second)})
			}
			s.rebuildAggregatesLocked()
			cache := &s.rangeAggregates
			s.prepareRangeAggregateCacheLocked()
			var pressure []*rangeBlockIdentity
			// Exercise the actual byte limit without retaining large test buffers.
			for i := 0; i < 4; i++ {
				identity := &rangeBlockIdentity{valid: true}
				pressure = append(pressure, identity)
				if !cache.retainEntry(rangeAggregateKey{block: identity}, rangeAggregateEntry{}, rangeAggregateCacheBytes/4-128) {
					t.Fatal("failed to fill the cache budget")
				}
			}
			query := func() {
				if apiDetail {
					assertRangeAPIMatchesScan(t, s, "api", "7h", "", now)
				} else {
					assertRangeCacheMatchesScan(t, s, now, "7h", "")
				}
			}
			query() // learn the rejected block's retained size
			key := rangeAggregateKey{block: m.rangeDetailBlocks[0].identity, apiDetail: apiDetail}
			if _, exists := cache.entries[key]; exists {
				t.Fatal("query block bypassed the byte budget")
			}
			query() // take the known-size rejection path
			pressure[0].valid = false
			query()
			if _, exists := cache.entries[key]; !exists {
				t.Fatal("rejected block was not readmitted after budget pressure was released")
			}
			before := cache.hits
			query()
			if cache.hits <= before || cache.bytes > rangeAggregateCacheBytes {
				t.Fatal("readmitted block did not provide bounded reuse")
			}
		})
	}
}

func TestRangeBlockCacheSweepsAtMostOncePerQuery(t *testing.T) {
	s := NewRequestStatistics()
	s.prepareRangeAggregateCacheLocked()
	cache := &s.rangeAggregates
	for i := 0; i < rangeAggregateCacheEntries; i++ {
		if !cache.retainEntry(rangeAggregateKey{block: &rangeBlockIdentity{valid: true}}, rangeAggregateEntry{}, 1) {
			t.Fatal("failed to fill the entry budget")
		}
	}
	for pass := 0; pass < 3; pass++ {
		s.prepareRangeAggregateCacheLocked()
		before := cache.sweeps
		for i := 0; i < 10000; i++ {
			if cache.canRetain(129) {
				t.Fatal("full cache must reject new entries")
			}
		}
		if cache.sweeps-before != 1 {
			t.Fatalf("full cache was swept %d times within one query", cache.sweeps-before)
		}
	}
	for key := range cache.entries {
		key.block.valid = false
		break
	}
	s.prepareRangeAggregateCacheLocked()
	if !cache.canRetain(129) {
		t.Fatal("the next query did not reclaim newly invalidated entries")
	}
}

func TestRangeBlockZeroTimeAndSaturatedCounters(t *testing.T) {
	s := NewRequestStatistics()
	s.retention = 0
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	m := &modelStats{}
	s.apis["api"] = &apiStats{Models: map[string]*modelStats{"model": m}}
	for i := 0; i < 5000; i++ {
		at := now.Add(-time.Duration(i) * time.Minute)
		if i%11 == 0 {
			at = time.Time{}
		}
		m.Details = append(m.Details, RequestDetail{Timestamp: at, Tokens: TokenStats{InputTokens: math.MaxInt64, OutputTokens: 1}, LatencyMs: math.MaxInt64})
	}
	s.rebuildAggregatesLocked()
	for _, rangeKey := range []string{"all", "24h", "7d"} {
		assertRangeCacheMatchesScan(t, s, now, rangeKey, "")
		assertRangeCacheMatchesScan(t, s, now, rangeKey, "")
	}
}
