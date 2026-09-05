package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestColdRestoreSnapshotAndSameDayShardNoDoubleCount 复现审查发现的 P1:
// 旧版本把污染明细同时写进 snapshot.json 与当日分片;修复不能让两份来源
// 因 dedup key 变化而双计,且重复冷启动不得累加。
func TestColdRestoreSnapshotAndSameDayShardNoDoubleCount(t *testing.T) {
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
	line := append(mustMarshal(persistedDetail{
		API: "claude · 上游 e462f816a955deb1", Model: "claude-fable-5", Detail: polluted,
	}), '\n')
	if err := os.WriteFile(filepath.Join(dir, storageFileName(storageDate(time.Now()))), line, 0o600); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	wantTotal := int64(52000 + 1400 + 26800)
	for boot := 1; boot <= 2; boot++ {
		stats := NewRequestStatistics()
		stats.Configure(runtimeConfig{
			MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
			ClaudeCacheRepairEnabled: true,
			StorageEnabled:           true, StoragePath: dir, StorageFlushSeconds: 1,
			PriceStoragePath: filepath.Join(dir, "prices.json"),
		})
		snapshot := stats.Snapshot()
		stats.Close()
		if snapshot.TotalRequests != 1 || snapshot.TotalTokens != wantTotal || snapshot.CachedTokens != 0 {
			t.Fatalf("boot %d: req %d total %d cached %d, want 1/%d/0 (snapshot+shard must dedup)",
				boot, snapshot.TotalRequests, snapshot.TotalTokens, snapshot.CachedTokens, wantTotal)
		}
	}
}

// TestGenuineEqualCacheReadWriteWithMarkerUntouched 覆盖审查提出的反例:
// 修复版写入的真实「命中 == 创建」合法记录形态与污染签名相同,依靠持久化的
// CacheReadTokens 权威命中字段免除误判,与时间无关。
func TestGenuineEqualCacheReadWriteWithMarkerUntouched(t *testing.T) {
	genuine := pollutedClaudeCacheDetail()
	genuine.Tokens.CacheReadTokens = genuine.Tokens.CachedTokens
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
	if snapshot.CachedTokens != genuine.Tokens.CachedTokens ||
		snapshot.TotalTokens != genuine.Tokens.TotalTokens {
		t.Fatalf("aggregates = cached %d total %d, want marked record untouched",
			snapshot.CachedTokens, snapshot.TotalTokens)
	}
}

func TestClaudeCacheRepairRejectsOverflowingSignature(t *testing.T) {
	max := int64(^uint64(0) >> 1)
	detail := RequestDetail{Provider: "claude", Tokens: TokenStats{
		InputTokens: max, OutputTokens: 1,
		CachedTokens: max / 2, CacheTokens: max - 1, CacheWriteTokens: max / 2,
		TotalTokens: max,
	}}
	if isPollutedClaudeCacheFallbackDetail(detail) {
		t.Fatal("overflowing cache signature was accepted as a repairable record")
	}
}

// TestFixedVersionEqualCacheRecordSurvivesRestart 端到端锁定审查 P1 场景:
// 修复版原生记录真实「命中 == 创建」的 Claude 请求,重启冷恢复后不得被
// 历史修复篡改。
func TestFixedVersionEqualCacheRecordSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	record := UsageRecord{
		Provider:     "claude",
		ExecutorType: "ClaudeExecutor",
		Model:        "claude-fable-5",
		APIKey:       "sk-client-alpha-0000zy",
		AuthID:       "claude:apikey:98632dcc86bd",
		AuthIndex:    "e462f816a955deb1",
		AuthType:     "apikey",
		RequestedAt:  time.Now().Add(-time.Hour),
		Latency:      5 * time.Second,
		Detail: UsageDetail{
			InputTokens:         52000,
			OutputTokens:        1400,
			CachedTokens:        26800,
			CacheReadTokens:     26800,
			CacheCreationTokens: 26800,
			TotalTokens:         52000 + 1400 + 26800 + 26800,
		},
	}

	first := NewRequestStatistics()
	first.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		ClaudeCacheRepairEnabled: true,
		StorageEnabled:           true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	first.Record(record)
	first.Close()

	second := NewRequestStatistics()
	second.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		ClaudeCacheRepairEnabled: true,
		StorageEnabled:           true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	defer second.Close()

	snapshot := second.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.CachedTokens != 26800 || snapshot.CacheWriteTokens != 26800 {
		t.Fatalf("restored aggregates = req %d cached %d write %d, want genuine equal read/write preserved",
			snapshot.TotalRequests, snapshot.CachedTokens, snapshot.CacheWriteTokens)
	}
}

// TestMergePersistsRepairedClaudeCacheDetail 覆盖审查发现的 P2:导入的污染
// 明细必须以修复后的形态写入 JSONL,避免下次重启重新构成双计隐患。
func TestMergePersistsRepairedClaudeCacheDetail(t *testing.T) {
	dir := t.TempDir()
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		ClaudeCacheRepairEnabled: true,
		StorageEnabled:           true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})

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
	if result := stats.MergeSnapshot(imported); result.Added != 1 {
		t.Fatalf("merge result = %#v", result)
	}
	stats.Close()

	raw, err := os.ReadFile(filepath.Join(dir, storageFileName(storageDate(time.Now()))))
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, `"cached_tokens":0`) || !strings.Contains(content, `"cache_tokens":26800`) ||
		!strings.Contains(content, `"total_tokens":80200`) {
		t.Fatalf("persisted shard keeps polluted shape: %s", content)
	}
}

// TestRepairDisabledPreservesPollutedDataWithoutDuplicates 锁定默认关闭语义:
// 未启用 claude_cache_repair_enabled 时,不可判定的历史明细保持原样,
// 快照与当日分片也不得因此双计。
func TestRepairDisabledPreservesPollutedDataWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	polluted := pollutedClaudeCacheDetail()
	usage := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   polluted.Tokens.TotalTokens,
		CachedTokens:  polluted.Tokens.CachedTokens,
		APIs: map[string]APISnapshot{
			"claude · 上游 e462f816a955deb1": {
				TotalRequests: 1, SuccessCount: 1,
				TotalTokens:  polluted.Tokens.TotalTokens,
				CachedTokens: polluted.Tokens.CachedTokens,
				Models: map[string]ModelSnapshot{
					"claude-fable-5": {
						TotalRequests: 1, SuccessCount: 1,
						TotalTokens:  polluted.Tokens.TotalTokens,
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
	line := append(mustMarshal(persistedDetail{
		API: "claude · 上游 e462f816a955deb1", Model: "claude-fable-5", Detail: polluted,
	}), '\n')
	if err := os.WriteFile(filepath.Join(dir, storageFileName(storageDate(time.Now()))), line, 0o600); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		StorageEnabled: true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	defer stats.Close()

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.CachedTokens != polluted.Tokens.CachedTokens ||
		snapshot.TotalTokens != polluted.Tokens.TotalTokens {
		t.Fatalf("aggregates = req %d cached %d total %d, want undecidable data untouched without duplicates",
			snapshot.TotalRequests, snapshot.CachedTokens, snapshot.TotalTokens)
	}
}

// TestRepairToggleOffAfterHealNoDoubleCount 锁定开关先开后关的边角:启用修复
// 治愈快照后再关闭开关,当日分片中的原始污染行必须经规范化去重键与治愈形态
// 对上,不得二次入账。
func TestRepairToggleOffAfterHealNoDoubleCount(t *testing.T) {
	dir := t.TempDir()
	polluted := pollutedClaudeCacheDetail()
	usage := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   polluted.Tokens.TotalTokens,
		CachedTokens:  polluted.Tokens.CachedTokens,
		APIs: map[string]APISnapshot{
			"claude · 上游 e462f816a955deb1": {
				TotalRequests: 1, SuccessCount: 1,
				TotalTokens:  polluted.Tokens.TotalTokens,
				CachedTokens: polluted.Tokens.CachedTokens,
				Models: map[string]ModelSnapshot{
					"claude-fable-5": {
						TotalRequests: 1, SuccessCount: 1,
						TotalTokens:  polluted.Tokens.TotalTokens,
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
	line := append(mustMarshal(persistedDetail{
		API: "claude · 上游 e462f816a955deb1", Model: "claude-fable-5", Detail: polluted,
	}), '\n')
	if err := os.WriteFile(filepath.Join(dir, storageFileName(storageDate(time.Now()))), line, 0o600); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	wantTotal := int64(52000 + 1400 + 26800)
	healer := NewRequestStatistics()
	healer.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		ClaudeCacheRepairEnabled: true,
		StorageEnabled:           true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	healed := healer.Snapshot()
	healer.Close()
	if healed.TotalRequests != 1 || healed.CachedTokens != 0 || healed.TotalTokens != wantTotal {
		t.Fatalf("healed aggregates = req %d cached %d total %d", healed.TotalRequests, healed.CachedTokens, healed.TotalTokens)
	}

	reopened := NewRequestStatistics()
	reopened.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		StorageEnabled: true, StoragePath: dir, StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	defer reopened.Close()
	snapshot := reopened.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("toggle-off restart total requests = %d, want canonical dedup to prevent duplicates", snapshot.TotalRequests)
	}
}

// TestHotReloadEnableRepairsExistingRecords 复现审查发现的热重载缺口:
// 通过 ConfigurePatch(/reconfigure 路径)把修复开关由关转开时,内存中已有的
// 污染明细必须立即修复,而不是等到完整重启的冷恢复。
func TestHotReloadEnableRepairsExistingRecords(t *testing.T) {
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
	defer stats.Close()
	stats.MergeSnapshot(imported)
	before := stats.Snapshot()
	if before.CachedTokens != polluted.Tokens.CachedTokens {
		t.Fatalf("precondition: cached = %d, want polluted state kept while disabled", before.CachedTokens)
	}

	stats.ConfigurePatch(runtimeConfigPatch{ClaudeCacheRepairEnabled: boolPtr(true)})

	after := stats.Snapshot()
	wantTotal := int64(52000 + 1400 + 26800)
	if after.TotalRequests != 1 || after.CachedTokens != 0 || after.TotalTokens != wantTotal {
		t.Fatalf("hot reload aggregates = req %d cached %d total %d, want repaired without restart",
			after.TotalRequests, after.CachedTokens, after.TotalTokens)
	}
}
