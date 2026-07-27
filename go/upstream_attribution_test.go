package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type usageRecordCollector struct {
	mu      sync.Mutex
	records []UsageRecord
}

func (c *usageRecordCollector) add(record UsageRecord) {
	c.mu.Lock()
	c.records = append(c.records, record)
	c.mu.Unlock()
}

func (c *usageRecordCollector) snapshot() []UsageRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]UsageRecord(nil), c.records...)
}

func testDeepSeekUsageRecord(provider, model, apiKey, authID, authIndex string) UsageRecord {
	return UsageRecord{
		Provider:     provider,
		ExecutorType: "ClaudeExecutor",
		Model:        model,
		Alias:        model,
		APIKey:       apiKey,
		AuthID:       authID,
		AuthIndex:    authIndex,
		AuthType:     "apikey",
		Endpoint:     "/v1/messages",
		RequestedAt:  time.Now(),
		Latency:      17291 * time.Millisecond,
		TTFT:         348 * time.Millisecond,
		Detail: UsageDetail{
			InputTokens:  434,
			OutputTokens: 1471,
			TotalTokens:  1905,
		},
	}
}

func testSuspiciousDeepSeekRecord() UsageRecord {
	return testDeepSeekUsageRecord(
		"claude",
		"deepseek-v4-flash",
		"sk-client-alpha-0000zy",
		"claude:apikey:f4210cff4b55",
		"42cfbd4d51456110",
	)
}

func testTrustedDeepSeekRecord() UsageRecord {
	record := testDeepSeekUsageRecord(
		"openai-compatible-opencode-go",
		"deepseek-v4-pro",
		"sk-client-alpha-0000zy",
		"openai-compatibility:opencode-go:17d5e5df394b",
		"cde44024f2945d1e",
	)
	record.ExecutorType = "OpenAICompatExecutor"
	record.Source = "openai-compatible-opencode-go"
	return record
}

func TestUpstreamAttributionReconcilesExportedClaudeDeepSeekShape(t *testing.T) {
	previousDelay := upstreamAttributionDelay
	upstreamAttributionDelay = time.Second
	t.Cleanup(func() { upstreamAttributionDelay = previousDelay })

	coordinator := newUpstreamAttributionCoordinator()
	collector := &usageRecordCollector{}
	suspicious := testSuspiciousDeepSeekRecord()
	coordinator.Submit(suspicious, collector.add)
	if got := collector.snapshot(); len(got) != 0 {
		t.Fatalf("records before corroboration = %#v, want pending", got)
	}

	trusted := testTrustedDeepSeekRecord()
	coordinator.Submit(trusted, collector.add)
	got := collector.snapshot()
	if len(got) != 2 {
		t.Fatalf("records after corroboration = %#v, want corrected plus trusted", got)
	}
	corrected := got[0]
	if corrected.Provider != trusted.Provider || corrected.Source != trusted.Source ||
		corrected.AuthID != trusted.AuthID || corrected.AuthIndex != trusted.AuthIndex {
		t.Fatalf("corrected identity = %#v, want route identity %#v", corrected, trusted)
	}
	if corrected.Model != suspicious.Model || corrected.Latency != suspicious.Latency ||
		corrected.TTFT != suspicious.TTFT || corrected.Detail != suspicious.Detail ||
		corrected.ExecutorType != suspicious.ExecutorType {
		t.Fatalf("corrected request fields = %#v, want original request fields preserved", corrected)
	}
	if key := usageGroupKey(corrected); key != "openai-compatible-opencode-go" {
		t.Fatalf("corrected group = %q", key)
	}
}

func TestHandleUsageReconcilesExportedClaudeFallbackShape(t *testing.T) {
	previousStats := stats
	previousFallbacks := usageFallbacks
	previousAttributions := upstreamAttributions
	previousDelay := upstreamAttributionDelay
	stats = NewRequestStatistics()
	usageFallbacks = nil
	upstreamAttributions = newUpstreamAttributionCoordinator()
	upstreamAttributionDelay = time.Second
	t.Cleanup(func() {
		upstreamAttributions.Flush()
		upstreamAttributions = previousAttributions
		upstreamAttributionDelay = previousDelay
		usageFallbacks = previousFallbacks
		stats = previousStats
	})

	suspicious := testSuspiciousDeepSeekRecord()
	trusted := testTrustedDeepSeekRecord()
	trusted.RequestedAt = suspicious.RequestedAt.Add(time.Minute)
	for _, record := range []UsageRecord{suspicious, trusted} {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal usage: %v", err)
		}
		if _, err = handleUsage(raw); err != nil {
			t.Fatalf("handleUsage: %v", err)
		}
	}

	snapshot := stats.Snapshot()
	api, ok := snapshot.APIs["openai-compatible-opencode-go"]
	if snapshot.TotalRequests != 2 || len(snapshot.APIs) != 1 || !ok || api.TotalRequests != 2 {
		t.Fatalf("snapshot = %#v, want two requests in one opencode group", snapshot)
	}
	flash := api.Models["deepseek-v4-flash"]
	if len(flash.Details) != 1 || flash.Details[0].AuthID != trusted.AuthID ||
		flash.Details[0].AuthIndex != trusted.AuthIndex || flash.Details[0].LatencyMs != 17291 ||
		flash.Details[0].TTFTMs != 348 {
		t.Fatalf("corrected flash details = %#v", flash.Details)
	}
}

func TestUpstreamAttributionPreservesUncorroboratedAndAmbiguousRecords(t *testing.T) {
	previousDelay := upstreamAttributionDelay
	upstreamAttributionDelay = time.Second
	t.Cleanup(func() { upstreamAttributionDelay = previousDelay })

	for _, test := range []struct {
		name    string
		prepare func(*upstreamAttributionCoordinator, *usageRecordCollector)
	}{
		{
			name: "different client",
			prepare: func(c *upstreamAttributionCoordinator, out *usageRecordCollector) {
				trusted := testTrustedDeepSeekRecord()
				trusted.APIKey = "sk-other-client-0000zz"
				c.Submit(trusted, out.add)
			},
		},
		{
			name: "different endpoint",
			prepare: func(c *upstreamAttributionCoordinator, out *usageRecordCollector) {
				trusted := testTrustedDeepSeekRecord()
				trusted.Endpoint = "/v1/responses"
				c.Submit(trusted, out.add)
			},
		},
		{
			name: "ambiguous providers",
			prepare: func(c *upstreamAttributionCoordinator, out *usageRecordCollector) {
				first := testTrustedDeepSeekRecord()
				second := testTrustedDeepSeekRecord()
				second.Provider = "openai-compatible-openrouter"
				second.Source = second.Provider
				second.AuthID = "openai-compatibility:openrouter:0123456789ab"
				second.AuthIndex = "0123456789abcdef"
				c.Submit(first, out.add)
				c.Submit(second, out.add)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := newUpstreamAttributionCoordinator()
			collector := &usageRecordCollector{}
			test.prepare(coordinator, collector)
			coordinator.Submit(testSuspiciousDeepSeekRecord(), collector.add)
			coordinator.Flush()
			got := collector.snapshot()
			last := got[len(got)-1]
			if last.Provider != "claude" || last.AuthID != "claude:apikey:f4210cff4b55" {
				t.Fatalf("records = %#v, want suspicious record unchanged", got)
			}
		})
	}
}

func TestUpstreamAttributionKeepsRealClaudeFailedAndAnonymousRecords(t *testing.T) {
	coordinator := newUpstreamAttributionCoordinator()
	collector := &usageRecordCollector{}

	realClaude := testSuspiciousDeepSeekRecord()
	realClaude.Model = "claude-opus-4-8"
	realClaude.Alias = realClaude.Model
	coordinator.Submit(realClaude, collector.add)

	failed := testSuspiciousDeepSeekRecord()
	failed.Failed = true
	coordinator.Submit(failed, collector.add)

	anonymous := testSuspiciousDeepSeekRecord()
	anonymous.APIKey = ""
	coordinator.Submit(anonymous, collector.add)

	got := collector.snapshot()
	if len(got) != 3 {
		t.Fatalf("records = %#v, want all three committed immediately", got)
	}
	for _, record := range got {
		if record.Provider != "claude" {
			t.Fatalf("record = %#v, want unchanged Claude provider", record)
		}
	}
}

func TestUpstreamAttributionTimeoutPreservesOriginal(t *testing.T) {
	previousDelay := upstreamAttributionDelay
	upstreamAttributionDelay = 10 * time.Millisecond
	t.Cleanup(func() { upstreamAttributionDelay = previousDelay })

	done := make(chan UsageRecord, 1)
	coordinator := newUpstreamAttributionCoordinator()
	coordinator.Submit(testSuspiciousDeepSeekRecord(), func(record UsageRecord) { done <- record })
	select {
	case got := <-done:
		if got.Provider != "claude" || got.AuthID != "claude:apikey:f4210cff4b55" {
			t.Fatalf("timed-out record = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out attribution was not committed")
	}
}

func TestLiveTrustedRouteMigratesPreviouslyTimedOutDetail(t *testing.T) {
	previousStats := stats
	previousAttributions := upstreamAttributions
	previousDelay := upstreamAttributionDelay
	stats = NewRequestStatistics()
	upstreamAttributions = newUpstreamAttributionCoordinator()
	upstreamAttributionDelay = 5 * time.Millisecond
	t.Cleanup(func() {
		upstreamAttributions.Flush()
		upstreamAttributions = previousAttributions
		upstreamAttributionDelay = previousDelay
		stats = previousStats
	})

	recordUsageWithAttribution(testSuspiciousDeepSeekRecord())
	deadline := time.Now().Add(time.Second)
	for stats.Snapshot().TotalRequests == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, ok := stats.Snapshot().APIs["claude · 上游 42cfbd4d51456110"]; !ok {
		t.Fatalf("timed-out snapshot = %#v, want original Claude group before corroboration", stats.Snapshot().APIs)
	}

	recordUsageWithAttribution(testTrustedDeepSeekRecord())
	assertCorrectedAttributionSnapshot(t, stats.Snapshot())
}

func TestUpstreamAttributionResetClearsLearnedRoutes(t *testing.T) {
	previousDelay := upstreamAttributionDelay
	upstreamAttributionDelay = time.Second
	t.Cleanup(func() { upstreamAttributionDelay = previousDelay })

	coordinator := newUpstreamAttributionCoordinator()
	collector := &usageRecordCollector{}
	coordinator.Submit(testTrustedDeepSeekRecord(), collector.add)
	coordinator.Reset()
	coordinator.Submit(testSuspiciousDeepSeekRecord(), collector.add)
	coordinator.Flush()
	got := collector.snapshot()
	last := got[len(got)-1]
	if last.Provider != "claude" || last.AuthID != "claude:apikey:f4210cff4b55" {
		t.Fatalf("records = %#v, want reset route forgotten", got)
	}
}

func TestUpstreamAttributionNormalizesClaudeCacheAccounting(t *testing.T) {
	coordinator := newUpstreamAttributionCoordinator()
	collector := &usageRecordCollector{}
	coordinator.Submit(testTrustedDeepSeekRecord(), collector.add)

	suspicious := testSuspiciousDeepSeekRecord()
	suspicious.Detail.InputTokens = 434
	suspicious.Detail.CacheReadTokens = 100
	suspicious.Detail.TotalTokens = 2005
	coordinator.Submit(suspicious, collector.add)
	got := collector.snapshot()
	corrected := got[len(got)-1]
	if corrected.Provider != "openai-compatible-opencode-go" || corrected.Detail.InputTokens != 534 ||
		corrected.Detail.CacheReadTokens != 100 || corrected.Detail.TotalTokens != 2005 {
		t.Fatalf("corrected cache accounting = %#v", corrected)
	}
}

func TestUpstreamAttributionPreservesLateResponseEnrichmentWhilePending(t *testing.T) {
	previousDelay := upstreamAttributionDelay
	upstreamAttributionDelay = time.Second
	t.Cleanup(func() { upstreamAttributionDelay = previousDelay })

	coordinator := newUpstreamAttributionCoordinator()
	collector := &usageRecordCollector{}
	suspicious := testSuspiciousDeepSeekRecord()
	suspicious.Endpoint = ""
	coordinator.Submit(suspicious, collector.add)
	enrichment := suspicious
	enrichment.Endpoint = "/v1/messages"
	enrichment.Stream = true
	if !coordinator.EnrichPending(suspicious, enrichment) {
		t.Fatal("EnrichPending() = false, want pending record enriched")
	}
	coordinator.Submit(testTrustedDeepSeekRecord(), collector.add)

	got := collector.snapshot()
	if len(got) != 2 || got[0].Provider != "openai-compatible-opencode-go" ||
		got[0].Endpoint != "/v1/messages" || !got[0].Stream {
		t.Fatalf("records = %#v, want corrected record with response enrichment", got)
	}
}

func testAttributionSnapshot() StatisticsSnapshot {
	when := time.Now().Add(-2 * time.Minute).UTC()
	hash := strings.Repeat("a", 56)
	suspicious := RequestDetail{
		Model:      "deepseek-v4-flash",
		Timestamp:  when,
		LatencyMs:  17291,
		TTFTMs:     348,
		APIKey:     "sk******zy",
		APIKeyHash: hash,
		Source:     "claude",
		Provider:   "claude",
		AuthID:     "claude:apikey:f4210cff4b55",
		AuthIndex:  "42cfbd4d51456110",
		AuthType:   "apikey",
		Endpoint:   "/v1/messages",
		Stream:     true,
		Tokens: TokenStats{
			InputTokens: 434, OutputTokens: 1471, TotalTokens: 1905,
		},
	}
	trusted := RequestDetail{
		Model:      "deepseek-v4-pro",
		Timestamp:  when.Add(time.Minute),
		LatencyMs:  3708,
		TTFTMs:     855,
		APIKey:     "sk******zy",
		APIKeyHash: hash,
		Source:     "openai-compatible-opencode-go",
		Provider:   "openai-compatible-opencode-go",
		AuthID:     "openai-compatibility:opencode-go:17d5e5df394b",
		AuthIndex:  "cde44024f2945d1e",
		AuthType:   "apikey",
		Endpoint:   "/v1/messages",
		Stream:     true,
		Tokens: TokenStats{
			InputTokens: 14738, OutputTokens: 162, CachedTokens: 4480, CacheTokens: 4480, TotalTokens: 14900,
		},
	}
	return StatisticsSnapshot{
		TotalRequests: 2,
		SuccessCount:  2,
		TotalTokens:   16805,
		InputTokens:   15172,
		OutputTokens:  1633,
		CachedTokens:  4480,
		APIs: map[string]APISnapshot{
			"claude · 上游 42cfbd4d51456110": {
				TotalRequests: 1, SuccessCount: 1, TotalTokens: 1905, InputTokens: 434, OutputTokens: 1471,
				Models: map[string]ModelSnapshot{
					"deepseek-v4-flash": {
						TotalRequests: 1, SuccessCount: 1, TotalTokens: 1905, InputTokens: 434, OutputTokens: 1471,
						Providers: []ModelProviderStat{{Provider: "claude", TotalRequests: 1, SuccessCount: 1, TotalTokens: 1905, InputTokens: 434, OutputTokens: 1471}},
						Details:   []RequestDetail{suspicious},
					},
				},
			},
			"openai-compatible-opencode-go": {
				TotalRequests: 1, SuccessCount: 1, TotalTokens: 14900, InputTokens: 14738, OutputTokens: 162, CachedTokens: 4480,
				Models: map[string]ModelSnapshot{
					"deepseek-v4-pro": {
						TotalRequests: 1, SuccessCount: 1, TotalTokens: 14900, InputTokens: 14738, OutputTokens: 162, CachedTokens: 4480,
						Providers: []ModelProviderStat{{Provider: "openai-compatible-opencode-go", TotalRequests: 1, SuccessCount: 1, TotalTokens: 14900, InputTokens: 14738, OutputTokens: 162, CachedTokens: 4480}},
						Details:   []RequestDetail{trusted},
					},
				},
			},
		},
	}
}

func assertCorrectedAttributionSnapshot(t *testing.T, snapshot StatisticsSnapshot) {
	t.Helper()
	if snapshot.TotalRequests != 2 || len(snapshot.APIs) != 1 {
		t.Fatalf("snapshot totals/APIs = %d/%#v", snapshot.TotalRequests, snapshot.APIs)
	}
	api, ok := snapshot.APIs["openai-compatible-opencode-go"]
	if !ok || api.TotalRequests != 2 {
		t.Fatalf("APIs = %#v, want merged opencode group", snapshot.APIs)
	}
	flash := api.Models["deepseek-v4-flash"]
	if len(flash.Details) != 1 {
		t.Fatalf("flash details = %#v", flash.Details)
	}
	detail := flash.Details[0]
	if detail.Provider != "openai-compatible-opencode-go" ||
		detail.AuthID != "openai-compatibility:opencode-go:17d5e5df394b" ||
		detail.AuthIndex != "cde44024f2945d1e" || detail.LatencyMs != 17291 || detail.TTFTMs != 348 {
		t.Fatalf("corrected flash detail = %#v", detail)
	}
	if _, exists := snapshot.APIs["claude · 上游 42cfbd4d51456110"]; exists {
		t.Fatalf("stale Claude group remains: %#v", snapshot.APIs)
	}
}

func TestImportReconcilesExistingExportedAttribution(t *testing.T) {
	previousStats := stats
	stats = NewRequestStatistics()
	t.Cleanup(func() { stats = previousStats })

	payload := struct {
		Version int                `json:"version"`
		Usage   StatisticsSnapshot `json:"usage"`
	}{Version: 1, Usage: testAttributionSnapshot()}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal import: %v", err)
	}
	if _, err = handleImportUsage(raw); err != nil {
		t.Fatalf("handleImportUsage: %v", err)
	}
	assertCorrectedAttributionSnapshot(t, stats.Snapshot())
}

func TestReconciledSnapshotImportRemainsIdempotent(t *testing.T) {
	stats := NewRequestStatistics()
	snapshot := testAttributionSnapshot()
	first := stats.MergeSnapshot(snapshot)
	second := stats.MergeSnapshot(snapshot)
	if first.Added != 2 || second.Added != 0 || second.Skipped != 2 {
		t.Fatalf("merge results = first %#v second %#v", first, second)
	}
	assertCorrectedAttributionSnapshot(t, stats.Snapshot())
}

func TestImportReconciliationConvertsClaudeExclusiveCacheInput(t *testing.T) {
	snapshot := testAttributionSnapshot()
	api := snapshot.APIs["claude · 上游 42cfbd4d51456110"]
	model := api.Models["deepseek-v4-flash"]
	model.Details[0].Tokens.InputTokens = 434
	model.Details[0].Tokens.CachedTokens = 100
	model.Details[0].Tokens.CacheTokens = 100
	model.Details[0].Tokens.TotalTokens = 2005
	api.Models["deepseek-v4-flash"] = model
	snapshot.APIs["claude · 上游 42cfbd4d51456110"] = api

	stats := NewRequestStatistics()
	stats.MergeSnapshot(snapshot)
	detail := stats.Snapshot().APIs["openai-compatible-opencode-go"].Models["deepseek-v4-flash"].Details[0]
	if detail.Tokens.InputTokens != 534 || detail.Tokens.CachedTokens != 100 || detail.Tokens.TotalTokens != 2005 {
		t.Fatalf("corrected cache tokens = %#v", detail.Tokens)
	}
}

func TestSnapshotReconciliationRequiresUniqueTrustedRoute(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*StatisticsSnapshot)
	}{
		{
			name: "no corroboration",
			mutate: func(snapshot *StatisticsSnapshot) {
				delete(snapshot.APIs, "openai-compatible-opencode-go")
			},
		},
		{
			name: "conflicting corroboration",
			mutate: func(snapshot *StatisticsSnapshot) {
				trusted := snapshot.APIs["openai-compatible-opencode-go"].Models["deepseek-v4-pro"].Details[0]
				trusted.Provider = "openai-compatible-openrouter"
				trusted.Source = trusted.Provider
				trusted.AuthID = "openai-compatibility:openrouter:0123456789ab"
				trusted.AuthIndex = "0123456789abcdef"
				snapshot.APIs[trusted.Provider] = APISnapshot{Models: map[string]ModelSnapshot{
					"deepseek-v4-pro": {Details: []RequestDetail{trusted}},
				}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := testAttributionSnapshot()
			test.mutate(&snapshot)
			stats := NewRequestStatistics()
			stats.MergeSnapshot(snapshot)
			got := stats.Snapshot()
			if _, ok := got.APIs["claude · 上游 42cfbd4d51456110"]; !ok {
				t.Fatalf("APIs = %#v, want unproven Claude record preserved", got.APIs)
			}
		})
	}
}

func TestStorageSnapshotColdRestoreReconcilesAttribution(t *testing.T) {
	dir := t.TempDir()
	usage := testAttributionSnapshot()
	flash := usage.APIs["claude · 上游 42cfbd4d51456110"].Models["deepseek-v4-flash"].Details[0]
	pro := usage.APIs["openai-compatible-opencode-go"].Models["deepseek-v4-pro"].Details[0]
	day := flash.Timestamp.Format("2006-01-02")
	hour := hourKeys[flash.Timestamp.Hour()]
	usage.CostByDay = map[string]float64{day: 123}
	usage.CostByHour = map[string]float64{hour: 123}
	usage.CostTokensByDay = map[string][]TimeSeriesTokenStat{day: {
		{Model: flash.Model, Provider: flash.Provider, TotalTokens: 1905, InputTokens: 434, OutputTokens: 1471},
		{Model: pro.Model, Provider: pro.Provider, TotalTokens: 14900, InputTokens: 14738, OutputTokens: 162, CachedTokens: 4480},
	}}
	usage.CostTokensByHour = map[string][]TimeSeriesTokenStat{hour: usage.CostTokensByDay[day]}
	payload := persistedStorageSnapshot{
		Version: currentStorageSnapshotVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Usage: usage,
	}
	if err := os.WriteFile(storageSnapshotPath(dir), mustMarshal(payload), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		StorageEnabled: true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	defer stats.Close()
	restored := stats.Snapshot()
	assertCorrectedAttributionSnapshot(t, restored)
	for _, values := range []map[string][]TimeSeriesTokenStat{restored.CostTokensByDay, restored.CostTokensByHour} {
		for _, series := range values {
			for _, stat := range series {
				if stat.Model == "deepseek-v4-flash" && stat.Provider != "openai-compatible-opencode-go" {
					t.Fatalf("restored cost token series = %#v, want corrected flash provider", values)
				}
			}
		}
	}
	if restored.CostByDay[day] == 123 || restored.CostByHour[hour] == 123 {
		t.Fatalf("restored stale monetary cost = day %#v hour %#v", restored.CostByDay, restored.CostByHour)
	}
}

func TestStorageJSONLColdRestoreReconcilesAcrossFilesAndMetadata(t *testing.T) {
	dir := t.TempDir()
	snapshot := testAttributionSnapshot()
	suspicious := snapshot.APIs["claude · 上游 42cfbd4d51456110"].Models["deepseek-v4-flash"].Details[0]
	trusted := snapshot.APIs["openai-compatible-opencode-go"].Models["deepseek-v4-pro"].Details[0]
	suspicious.Endpoint = ""
	metadata := suspicious
	metadata.Endpoint = "/v1/messages"

	first := append(mustMarshal(persistedDetail{
		API: "claude · 上游 42cfbd4d51456110", Model: suspicious.Model, Detail: suspicious,
	}), '\n')
	first = append(first, mustMarshal(persistedDetail{
		API: "claude · 上游 42cfbd4d51456110", Model: metadata.Model, Detail: metadata, MetadataOnly: true,
	})...)
	first = append(first, '\n')
	second := append(mustMarshal(persistedDetail{
		API: "openai-compatible-opencode-go", Model: trusted.Model, Detail: trusted,
	}), '\n')

	yesterday := storageDate(time.Now().Add(-24 * time.Hour))
	today := storageDate(time.Now())
	if err := os.WriteFile(filepath.Join(dir, storageFileName(yesterday)), first, 0o600); err != nil {
		t.Fatalf("write first shard: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, storageFileName(today)), second, 0o600); err != nil {
		t.Fatalf("write second shard: %v", err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		StorageEnabled: true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	defer stats.Close()
	got := stats.Snapshot()
	assertCorrectedAttributionSnapshot(t, got)
	flash := got.APIs["openai-compatible-opencode-go"].Models["deepseek-v4-flash"].Details[0]
	if flash.Endpoint != "/v1/messages" {
		t.Fatalf("metadata-only endpoint = %q, want preserved enrichment", flash.Endpoint)
	}
}
