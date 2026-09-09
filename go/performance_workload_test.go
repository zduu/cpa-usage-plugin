package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

// Keep the seed and distributions identical when comparing revisions. Unlike
// the older 100k fixture this also exercises archived accounting, late reports,
// equal timestamps, response headers and independently varying identities.
const performanceDatasetSeed uint64 = 20260909

func buildPerformanceDataset(count int, highCardinality bool, now time.Time) *RequestStatistics {
	s := NewRequestStatistics()
	s.retention = 30 * 24 * time.Hour
	s.dedupWindow = 0
	s.maxDetailsPerModel = 5000
	models, sources, credentials, clients := 4, 8, 16, 32
	if highCardinality {
		models, sources, credentials, clients = 128, 1024, 4096, 8192
	}
	rng := rand.New(rand.NewPCG(performanceDatasetSeed, performanceDatasetSeed+1))
	base := now.Add(-14 * 24 * time.Hour)
	for i := 0; i < count; i++ {
		model := fmt.Sprintf("model-%03d", rng.IntN(models))
		provider := fmt.Sprintf("provider-%d", rng.IntN(4))
		api := provider
		client := rng.IntN(clients)
		d := RequestDetail{
			Model: model, Provider: provider,
			Source: fmt.Sprintf("source-%04d", rng.IntN(sources)), AuthIndex: fmt.Sprintf("auth-%04d", rng.IntN(credentials)),
			APIKey: fmt.Sprintf("client-%04d", client), APIKeyHash: fmt.Sprintf("hash-%04d", client),
			Timestamp: base.Add(time.Duration(rng.IntN(14*24*3600)) * time.Second),
			LatencyMs: int64(100 + rng.IntN(3000)), TTFTMs: int64(1 + rng.IntN(100)),
			Endpoint: "/v1/chat/completions", Stream: i%2 == 0,
			Tokens: TokenStats{InputTokens: int64(100 + rng.IntN(1000)), OutputTokens: int64(10 + rng.IntN(200)), CachedTokens: int64(rng.IntN(100)), CacheWriteTokens: int64(rng.IntN(20))},
			Failed: i%17 == 0,
		}
		if i%29 == 0 {
			d.Timestamp = now.Add(-time.Hour) // real requests with identical timestamps
		}
		if d.Failed {
			d.StatusCode, d.Failure = 503, "synthetic upstream unavailable"
		}
		if i%13 == 0 {
			d.Headers = map[string][]string{"x-request-id": {fmt.Sprintf("synthetic-%d", i)}}
		}
		d.Tokens.TotalTokens = detailTotalTokens(d.Tokens)
		if s.apis[api] == nil {
			s.apis[api] = &apiStats{Models: make(map[string]*modelStats)}
		}
		m := s.apis[api].Models[model]
		if m == nil {
			m = &modelStats{}
			s.apis[api].Models[model] = m
		}
		m.Details = append(m.Details, d)
	}
	s.rebuildAggregatesLocked()
	s.pruneLocked(now, true)
	return s
}

func BenchmarkPerformanceMixed(b *testing.B) {
	counts := []int{100000}
	if os.Getenv("CPA_PERF_MILLION") == "1" {
		counts = append(counts, 1000000)
	}
	for _, count := range counts {
		for _, high := range []bool{false, true} {
			b.Run(fmt.Sprintf("records=%d/high=%t", count, high), func(b *testing.B) {
				now := time.Now()
				s := buildPerformanceDataset(count, high, now)
				b.Run("summary_range", func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						clearBenchmarkSummaryRangeCache(s)
						_ = s.SummaryWithoutDetailsForRangeAt("7d", now)
					}
				})
				b.Run("events_after_record", func(b *testing.B) {
					query := EventsQuery{Limit: 100, Model: "model-000", Range: "7d"}
					_ = s.QueryEventsAt(query, now)
					b.ReportAllocs()
					for b.Loop() {
						s.Record(UsageRecord{Provider: "provider-0", Model: "model-000", RequestedAt: now, Detail: UsageDetail{InputTokens: 10}})
						_ = s.QueryEventsAt(query, now)
					}
				})
			})
		}
	}
}

// An opt-in resource probe, kept out of ordinary regression runs. Invoke the
// same test binary with the same environment before and after an optimization.
func TestPerformanceResourceProbe(t *testing.T) {
	if os.Getenv("CPA_PERF_PROBE") != "1" {
		t.Skip("set CPA_PERF_PROBE=1 for the repeatable resource probe")
	}
	count := 100000
	if os.Getenv("CPA_PERF_MILLION") == "1" {
		count = 1000000
	}
	runtime.GC()
	var before, loaded, after runtime.MemStats
	runtime.ReadMemStats(&before)
	now := time.Now()
	s := buildPerformanceDataset(count, os.Getenv("CPA_PERF_HIGH") == "1", now)
	runtime.GC()
	runtime.ReadMemStats(&loaded)
	query := EventsQuery{Limit: 100, Model: "model-000", Range: "7d"}
	latencies := make([]time.Duration, 100)
	for i := range latencies {
		started := time.Now()
		s.Record(UsageRecord{Provider: "provider-0", Model: "model-000", RequestedAt: now, Detail: UsageDetail{InputTokens: 10}})
		_ = s.QueryEventsAt(query, now)
		latencies[i] = time.Since(started)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("seed=%d records=%d high=%s retained=%d heap_before=%d heap_loaded=%d heap_after=%d allocated=%d gc=%d pause_ns=%d mixed_p50=%s mixed_p95=%s mixed_p99=%s",
		performanceDatasetSeed, count, os.Getenv("CPA_PERF_HIGH"), s.DetailCount(), before.HeapAlloc, loaded.HeapAlloc, after.HeapAlloc,
		after.TotalAlloc-before.TotalAlloc, after.NumGC-before.NumGC, after.PauseTotalNs-before.PauseTotalNs, latencies[49], latencies[94], latencies[98])
	runtime.KeepAlive(s)
}
