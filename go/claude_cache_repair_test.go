package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pollutedClaudeCacheDetail 构造修复前版本入账的污染明细:创建 X 被同时计入
// 命中(CachedTokens=X)与缓存合计(2X),总量多算一份。
func pollutedClaudeCacheDetail() RequestDetail {
	return RequestDetail{
		Model:     "claude-fable-5",
		Timestamp: time.Date(2026, 8, 16, 10, 12, 33, 123456789, time.FixedZone("CST", 8*3600)),
		LatencyMs: 8421,
		TTFTMs:    902,
		APIKey:    "sk******zy",
		Source:    "claude",
		Provider:  "claude",
		AuthID:    "claude:apikey:98632dcc86bd",
		AuthIndex: "e462f816a955deb1",
		AuthType:  "apikey",
		Tokens: TokenStats{
			InputTokens:      52000,
			OutputTokens:     1400,
			CachedTokens:     26800,
			CacheTokens:      53600,
			CacheWriteTokens: 26800,
			TotalTokens:      52000 + 1400 + 53600,
		},
	}
}

func TestUsageDetailCacheTokenPartsDropsClaudeCreationFallback(t *testing.T) {
	// CPA parseClaudeUsageNode 回填形态:cache_read 为 0,CachedTokens 被填成创建量。
	fallback := UsageDetail{InputTokens: 100, OutputTokens: 10, CachedTokens: 500, CacheReadTokens: 0, CacheCreationTokens: 500}
	read, write, combined := usageDetailCacheTokenParts(fallback, "claude")
	if read != 0 || write != 500 || combined != 500 {
		t.Fatalf("fallback parts = %d/%d/%d, want creation not double counted as read", read, write, combined)
	}

	// 相同形态在非 Claude 家族不做回填推断,原样保留。
	read, write, combined = usageDetailCacheTokenParts(fallback, "openai-compatible-foo")
	if read != 500 || write != 500 || combined != 1000 {
		t.Fatalf("non-claude parts = %d/%d/%d, want no fallback inference", read, write, combined)
	}

	// 真实命中形态:CacheReadTokens 非零,即使数值恰好等于创建量也保留。
	genuine := UsageDetail{InputTokens: 100, OutputTokens: 10, CachedTokens: 500, CacheReadTokens: 500, CacheCreationTokens: 500}
	read, write, combined = usageDetailCacheTokenParts(genuine, "claude")
	if read != 500 || write != 500 || combined != 1000 {
		t.Fatalf("genuine parts = %d/%d/%d, want real read kept", read, write, combined)
	}

	// 常规形态:命中与创建不同值。
	regular := UsageDetail{CachedTokens: 700, CacheReadTokens: 700, CacheCreationTokens: 300}
	read, write, combined = usageDetailCacheTokenParts(regular, "claude")
	if read != 700 || write != 300 || combined != 1000 {
		t.Fatalf("regular parts = %d/%d/%d", read, write, combined)
	}
}

func TestHandleUsageRecordsClaudeCreationOnlyWithoutDoubleCount(t *testing.T) {
	previousStats := stats
	previousFallbacks := usageFallbacks
	stats = NewRequestStatistics()
	usageFallbacks = nil
	t.Cleanup(func() {
		stats = previousStats
		usageFallbacks = previousFallbacks
	})

	record := UsageRecord{
		Provider:     "claude",
		ExecutorType: "ClaudeExecutor",
		Model:        "claude-fable-5",
		APIKey:       "sk-client-alpha-0000zy",
		AuthID:       "claude:apikey:98632dcc86bd",
		AuthIndex:    "e462f816a955deb1",
		AuthType:     "apikey",
		RequestedAt:  time.Now(),
		Latency:      8 * time.Second,
		Detail: UsageDetail{
			InputTokens:         52000,
			OutputTokens:        1400,
			CachedTokens:        26800, // CPA 回填:等于创建量
			CacheReadTokens:     0,
			CacheCreationTokens: 26800,
			TotalTokens:         52000 + 1400 + 26800,
		},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal usage: %v", err)
	}
	if _, err = handleUsage(raw); err != nil {
		t.Fatalf("handleUsage: %v", err)
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total requests = %d", snapshot.TotalRequests)
	}
	wantTotal := int64(52000 + 1400 + 26800)
	if snapshot.TotalTokens != wantTotal || snapshot.CachedTokens != 0 || snapshot.CacheWriteTokens != 26800 {
		t.Fatalf("aggregates = total %d cached %d write %d, want %d/0/26800",
			snapshot.TotalTokens, snapshot.CachedTokens, snapshot.CacheWriteTokens, wantTotal)
	}
	for _, api := range snapshot.APIs {
		for _, model := range api.Models {
			for _, detail := range model.Details {
				if detail.Tokens.CachedTokens != 0 || detail.Tokens.CacheWriteTokens != 26800 ||
					detail.Tokens.TotalTokens != wantTotal {
					t.Fatalf("recorded tokens = %#v, want creation counted once", detail.Tokens)
				}
			}
		}
	}
}

func TestColdRestoreRepairsPollutedClaudeCacheDetails(t *testing.T) {
	dir := t.TempDir()
	polluted := pollutedClaudeCacheDetail()
	usage := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   polluted.Tokens.TotalTokens,
		InputTokens:   polluted.Tokens.InputTokens,
		OutputTokens:  polluted.Tokens.OutputTokens,
		CachedTokens:  polluted.Tokens.CachedTokens,
		APIs: map[string]APISnapshot{
			"claude · 上游 e462f816a955deb1": {
				TotalRequests: 1, SuccessCount: 1,
				TotalTokens:  polluted.Tokens.TotalTokens,
				InputTokens:  polluted.Tokens.InputTokens,
				OutputTokens: polluted.Tokens.OutputTokens,
				CachedTokens: polluted.Tokens.CachedTokens,
				Models: map[string]ModelSnapshot{
					"claude-fable-5": {
						TotalRequests: 1, SuccessCount: 1,
						TotalTokens:  polluted.Tokens.TotalTokens,
						InputTokens:  polluted.Tokens.InputTokens,
						OutputTokens: polluted.Tokens.OutputTokens,
						CachedTokens: polluted.Tokens.CachedTokens,
						Details:      []RequestDetail{polluted},
					},
				},
			},
		},
	}
	payload := persistedStorageSnapshot{
		Version:     currentStorageSnapshotVersion,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Usage:       usage,
	}
	if err := os.WriteFile(storageSnapshotPath(dir), mustMarshal(payload), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		ClaudeCacheRepairEnabled: true,
		StorageEnabled:           true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	defer stats.Close()

	snapshot := stats.Snapshot()
	wantTotal := int64(52000 + 1400 + 26800)
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != wantTotal || snapshot.CachedTokens != 0 {
		t.Fatalf("restored aggregates = req %d total %d cached %d, want 1/%d/0",
			snapshot.TotalRequests, snapshot.TotalTokens, snapshot.CachedTokens, wantTotal)
	}
	for _, api := range snapshot.APIs {
		for _, model := range api.Models {
			for _, detail := range model.Details {
				if detail.Tokens.CachedTokens != 0 || detail.Tokens.CacheTokens != 26800 ||
					detail.Tokens.TotalTokens != wantTotal {
					t.Fatalf("restored tokens = %#v, want repaired", detail.Tokens)
				}
			}
		}
	}
}

func TestMergeRepairsPollutedClaudeCacheDetails(t *testing.T) {
	polluted := pollutedClaudeCacheDetail()
	imported := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   polluted.Tokens.TotalTokens,
		APIs: map[string]APISnapshot{
			"claude · 上游 e462f816a955deb1": {
				TotalRequests: 1, SuccessCount: 1,
				TotalTokens: polluted.Tokens.TotalTokens,
				Models: map[string]ModelSnapshot{
					"claude-fable-5": {
						TotalRequests: 1, SuccessCount: 1,
						TotalTokens: polluted.Tokens.TotalTokens,
						Details:     []RequestDetail{polluted},
					},
				},
			},
		},
	}

	stats := NewRequestStatistics()
	stats.claudeCacheRepairEnabled = true
	if result := stats.MergeSnapshot(imported); result.Added != 1 {
		t.Fatalf("merge result = %#v", result)
	}
	snapshot := stats.Snapshot()
	wantTotal := int64(52000 + 1400 + 26800)
	if snapshot.TotalTokens != wantTotal || snapshot.CachedTokens != 0 || snapshot.CacheWriteTokens != 26800 {
		t.Fatalf("merged aggregates = total %d cached %d write %d, want %d/0/26800",
			snapshot.TotalTokens, snapshot.CachedTokens, snapshot.CacheWriteTokens, wantTotal)
	}
}

func TestRepairLeavesGenuineClaudeCacheDetailsUntouched(t *testing.T) {
	genuine := pollutedClaudeCacheDetail()
	// 真实形态:命中与创建不同值,缓存合计与总量按真实口径。
	genuine.Tokens.CachedTokens = 40000
	genuine.Tokens.CacheTokens = 40000 + 26800
	genuine.Tokens.TotalTokens = 52000 + 1400 + 40000 + 26800
	imported := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   genuine.Tokens.TotalTokens,
		APIs: map[string]APISnapshot{
			"claude · 上游 e462f816a955deb1": {
				TotalRequests: 1, SuccessCount: 1,
				TotalTokens: genuine.Tokens.TotalTokens,
				Models: map[string]ModelSnapshot{
					"claude-fable-5": {
						TotalRequests: 1, SuccessCount: 1,
						TotalTokens: genuine.Tokens.TotalTokens,
						Details:     []RequestDetail{genuine},
					},
				},
			},
		},
	}

	stats := NewRequestStatistics()
	stats.claudeCacheRepairEnabled = true
	stats.MergeSnapshot(imported)
	snapshot := stats.Snapshot()
	if snapshot.CachedTokens != 40000 || snapshot.CacheWriteTokens != 26800 ||
		snapshot.TotalTokens != genuine.Tokens.TotalTokens {
		t.Fatalf("aggregates = cached %d write %d total %d, want genuine detail untouched",
			snapshot.CachedTokens, snapshot.CacheWriteTokens, snapshot.TotalTokens)
	}
}
