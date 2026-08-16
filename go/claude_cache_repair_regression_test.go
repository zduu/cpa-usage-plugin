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
			StorageEnabled: true, StoragePath: dir, StorageFlushSeconds: 1,
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

// TestGenuineEqualCacheReadWriteAfterCutoffUntouched 覆盖审查提出的反例:
// 修复版本上线后,真实「命中 == 创建」的合法 Claude 记录形态与污染签名相同,
// 必须依靠时间窗保持原样。
func TestGenuineEqualCacheReadWriteAfterCutoffUntouched(t *testing.T) {
	genuine := pollutedClaudeCacheDetail()
	genuine.Timestamp = claudeCacheRepairCutoff.Add(time.Hour)
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
	stats.MergeSnapshot(imported)
	snapshot := stats.Snapshot()
	if snapshot.CachedTokens != genuine.Tokens.CachedTokens ||
		snapshot.TotalTokens != genuine.Tokens.TotalTokens {
		t.Fatalf("aggregates = cached %d total %d, want post-cutoff record untouched",
			snapshot.CachedTokens, snapshot.TotalTokens)
	}
}

// TestMergePersistsRepairedClaudeCacheDetail 覆盖审查发现的 P2:导入的污染
// 明细必须以修复后的形态写入 JSONL,避免下次重启重新构成双计隐患。
func TestMergePersistsRepairedClaudeCacheDetail(t *testing.T) {
	dir := t.TempDir()
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100, RetentionDays: 0, DedupWindowMinutes: 0,
		StorageEnabled: true, StoragePath: dir, StorageFlushSeconds: 1,
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
