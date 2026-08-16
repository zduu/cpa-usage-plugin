package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var repairTestTimestamp = time.Date(2026, 8, 15, 15, 4, 23, 796913688, time.FixedZone("CST", 8*3600))

// repairTestOriginalClaudeRecord 复刻导出数据中真实 Claude 中转上游的原生记录:
// deepseek 模型由 claude:apikey 认证的上游实际执行。
func repairTestOriginalClaudeRecord() UsageRecord {
	return UsageRecord{
		Provider:     "claude",
		ExecutorType: "ClaudeExecutor",
		Model:        "deepseek-v4-flash",
		Alias:        "deepseek-v4-flash",
		APIKey:       "sk-client-alpha-0000zy",
		AuthID:       "claude:apikey:f4210cff4b55",
		AuthIndex:    "42cfbd4d51456110",
		AuthType:     "apikey",
		Endpoint:     "/v1/messages",
		RequestedAt:  repairTestTimestamp,
		Latency:      17291 * time.Millisecond,
		TTFT:         348 * time.Millisecond,
		Detail: UsageDetail{
			InputTokens:     434,
			OutputTokens:    1471,
			CacheReadTokens: 4480,
			TotalTokens:     6385,
		},
	}
}

func repairTestGenuineOpenAICompatRecord() UsageRecord {
	return UsageRecord{
		Provider:     "openai-compatible-opencode-go",
		ExecutorType: "OpenAICompatExecutor",
		Source:       "openai-compatible-opencode-go",
		Model:        "deepseek-v4-pro",
		Alias:        "deepseek-v4-pro",
		APIKey:       "sk-client-alpha-0000zy",
		AuthID:       "openai-compatibility:opencode-go:17d5e5df394b",
		AuthIndex:    "cde44024f2945d1e",
		AuthType:     "apikey",
		Endpoint:     "/v1/messages",
		RequestedAt:  repairTestTimestamp.Add(-30 * time.Minute),
		Latency:      9204 * time.Millisecond,
		TTFT:         512 * time.Millisecond,
		Detail: UsageDetail{
			InputTokens:  14738,
			OutputTokens: 162,
			TotalTokens:  14900,
		},
	}
}

// repairTestOriginalClaudeDetail 是原始 Claude 明细的持久化形态(JSONL/导出)。
func repairTestOriginalClaudeDetail() RequestDetail {
	return RequestDetail{
		Model:      "deepseek-v4-flash",
		Timestamp:  repairTestTimestamp,
		LatencyMs:  17291,
		TTFTMs:     348,
		APIKey:     "sk******zy",
		Source:     "claude",
		Provider:   "claude",
		AuthID:     "claude:apikey:f4210cff4b55",
		AuthIndex:  "42cfbd4d51456110",
		AuthType:   "apikey",
		Endpoint:   "/v1/messages",
		Tokens: TokenStats{
			InputTokens:  434,
			OutputTokens: 1471,
			CachedTokens: 4480,
			TotalTokens:  6385,
		},
	}
}

// repairTestMigratedTwinDetail 是旧版改写产物:身份换成 openai-compatible,
// 缓存 token 折进输入,其余字段与原始明细逐位相同。
func repairTestMigratedTwinDetail() RequestDetail {
	detail := repairTestOriginalClaudeDetail()
	detail.Source = "openai-compatible-opencode-go"
	detail.Provider = "openai-compatible-opencode-go"
	detail.AuthID = "openai-compatibility:opencode-go:17d5e5df394b"
	detail.AuthIndex = "cde44024f2945d1e"
	detail.Tokens.InputTokens += detail.Tokens.CachedTokens
	detail.Tokens.TotalTokens = detail.Tokens.InputTokens + detail.Tokens.OutputTokens
	return detail
}

func repairTestGenuineOpenAICompatDetail() RequestDetail {
	return RequestDetail{
		Model:      "deepseek-v4-pro",
		Timestamp:  repairTestTimestamp.Add(-30 * time.Minute),
		LatencyMs:  9204,
		TTFTMs:     512,
		APIKey:     "sk******zy",
		Source:     "openai-compatible-opencode-go",
		Provider:   "openai-compatible-opencode-go",
		AuthID:     "openai-compatibility:opencode-go:17d5e5df394b",
		AuthIndex:  "cde44024f2945d1e",
		AuthType:   "apikey",
		Endpoint:   "/v1/messages",
		Tokens: TokenStats{
			InputTokens:  14738,
			OutputTokens: 162,
			TotalTokens:  14900,
		},
	}
}

func findAPIByAuthID(t *testing.T, snapshot StatisticsSnapshot, authID string) (string, APISnapshot) {
	t.Helper()
	for name, api := range snapshot.APIs {
		for _, model := range api.Models {
			for _, detail := range model.Details {
				if detail.AuthID == authID {
					return name, api
				}
			}
		}
	}
	t.Fatalf("no API group holds auth %q: %#v", authID, snapshot.APIs)
	return "", APISnapshot{}
}

// TestHandleUsageKeepsGenuineClaudeDeepSeekIdentity 锁定改写机制的移除:
// 即使同一客户端在 openai-compatible 渠道跑过同族 DeepSeek 模型,
// claude:apikey 上游的原生记录也必须原样入账。
func TestHandleUsageKeepsGenuineClaudeDeepSeekIdentity(t *testing.T) {
	previousStats := stats
	previousFallbacks := usageFallbacks
	stats = NewRequestStatistics()
	usageFallbacks = nil
	t.Cleanup(func() {
		stats = previousStats
		usageFallbacks = previousFallbacks
	})

	for _, record := range []UsageRecord{repairTestGenuineOpenAICompatRecord(), repairTestOriginalClaudeRecord()} {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal usage: %v", err)
		}
		if _, err = handleUsage(raw); err != nil {
			t.Fatalf("handleUsage: %v", err)
		}
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 2 || len(snapshot.APIs) != 2 {
		t.Fatalf("snapshot = %d requests %d groups, want native identities kept apart", snapshot.TotalRequests, len(snapshot.APIs))
	}
	claudeAPI, api := findAPIByAuthID(t, snapshot, "claude:apikey:f4210cff4b55")
	flash := api.Models["deepseek-v4-flash"]
	if len(flash.Details) != 1 || flash.Details[0].Provider != "claude" ||
		flash.Details[0].AuthIndex != "42cfbd4d51456110" {
		t.Fatalf("claude group %q details = %#v, want untouched claude identity", claudeAPI, flash.Details)
	}
}

// TestStorageColdRestoreRepairsMigratedAttributionTwins 模拟被旧版本破坏的现场:
// snapshot.json 中的明细已被改写为 opencode-go,JSONL 分片仍保存原始 claude
// 记录。冷启动后孪生副本应被丢弃,而不是与原始记录双计。
func TestStorageColdRestoreRepairsMigratedAttributionTwins(t *testing.T) {
	dir := t.TempDir()
	migrated := repairTestMigratedTwinDetail()
	genuine := repairTestGenuineOpenAICompatDetail()

	usage := StatisticsSnapshot{
		TotalRequests: 2,
		SuccessCount:  2,
		TotalTokens:   migrated.Tokens.TotalTokens + genuine.Tokens.TotalTokens,
		InputTokens:   migrated.Tokens.InputTokens + genuine.Tokens.InputTokens,
		OutputTokens:  migrated.Tokens.OutputTokens + genuine.Tokens.OutputTokens,
		CachedTokens:  migrated.Tokens.CachedTokens,
		APIs: map[string]APISnapshot{
			"openai-compatible-opencode-go": {
				TotalRequests: 2,
				SuccessCount:  2,
				TotalTokens:   migrated.Tokens.TotalTokens + genuine.Tokens.TotalTokens,
				InputTokens:   migrated.Tokens.InputTokens + genuine.Tokens.InputTokens,
				OutputTokens:  migrated.Tokens.OutputTokens + genuine.Tokens.OutputTokens,
				CachedTokens:  migrated.Tokens.CachedTokens,
				Models: map[string]ModelSnapshot{
					"deepseek-v4-flash": {
						TotalRequests: 1, SuccessCount: 1,
						TotalTokens:  migrated.Tokens.TotalTokens,
						InputTokens:  migrated.Tokens.InputTokens,
						OutputTokens: migrated.Tokens.OutputTokens,
						CachedTokens: migrated.Tokens.CachedTokens,
						Details:      []RequestDetail{migrated},
					},
					"deepseek-v4-pro": {
						TotalRequests: 1, SuccessCount: 1,
						TotalTokens:  genuine.Tokens.TotalTokens,
						InputTokens:  genuine.Tokens.InputTokens,
						OutputTokens: genuine.Tokens.OutputTokens,
						Details:      []RequestDetail{genuine},
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

	lines := append(mustMarshal(persistedDetail{
		API: "claude · 上游 42cfbd4d51456110", Model: "deepseek-v4-flash", Detail: repairTestOriginalClaudeDetail(),
	}), '\n')
	lines = append(lines, mustMarshal(persistedDetail{
		API: "openai-compatible-opencode-go", Model: "deepseek-v4-pro", Detail: genuine,
	})...)
	lines = append(lines, '\n')
	if err := os.WriteFile(filepath.Join(dir, storageFileName(storageDate(time.Now()))), lines, 0o600); err != nil {
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
	if snapshot.TotalRequests != 2 {
		t.Fatalf("total requests = %d, want repaired twin dropped without double count", snapshot.TotalRequests)
	}
	_, claudeAPI := findAPIByAuthID(t, snapshot, "claude:apikey:f4210cff4b55")
	flash := claudeAPI.Models["deepseek-v4-flash"]
	if len(flash.Details) != 1 || flash.Details[0].Provider != "claude" ||
		flash.Details[0].Tokens.InputTokens != 434 {
		t.Fatalf("restored claude details = %#v, want original identity and tokens", flash.Details)
	}
	for name, api := range snapshot.APIs {
		for modelName, model := range api.Models {
			for _, detail := range model.Details {
				if modelName == "deepseek-v4-flash" && detail.Provider != "claude" {
					t.Fatalf("group %q still holds migrated twin %#v", name, detail)
				}
			}
		}
	}
	if _, ok := findOpencodeProModel(snapshot); !ok {
		t.Fatalf("genuine opencode record lost: %#v", snapshot.APIs)
	}
}

func findOpencodeProModel(snapshot StatisticsSnapshot) (ModelSnapshot, bool) {
	for _, api := range snapshot.APIs {
		if model, ok := api.Models["deepseek-v4-pro"]; ok && len(model.Details) == 1 {
			return model, true
		}
	}
	return ModelSnapshot{}, false
}

// TestMergeSnapshotRepairsCorruptedAttributionTwins 模拟用户把损坏前导出的备份
// 重新导入:导入的原始 claude 明细应替换内存中被改写的孪生副本。
func TestMergeSnapshotRepairsCorruptedAttributionTwins(t *testing.T) {
	stats := NewRequestStatistics()
	corrupted := repairTestOriginalClaudeRecord()
	corrupted.Provider = "openai-compatible-opencode-go"
	corrupted.Source = "openai-compatible-opencode-go"
	corrupted.AuthID = "openai-compatibility:opencode-go:17d5e5df394b"
	corrupted.AuthIndex = "cde44024f2945d1e"
	corrupted.Detail.InputTokens += corrupted.Detail.CacheReadTokens
	corrupted.Detail.CachedTokens = corrupted.Detail.CacheReadTokens
	corrupted.Detail.CacheReadTokens = 0
	stats.Record(corrupted)
	stats.Record(repairTestGenuineOpenAICompatRecord())

	original := repairTestOriginalClaudeDetail()
	imported := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   original.Tokens.TotalTokens,
		APIs: map[string]APISnapshot{
			"claude · 上游 42cfbd4d51456110": {
				TotalRequests: 1, SuccessCount: 1,
				TotalTokens: original.Tokens.TotalTokens,
				Models: map[string]ModelSnapshot{
					"deepseek-v4-flash": {
						TotalRequests: 1, SuccessCount: 1,
						TotalTokens: original.Tokens.TotalTokens,
						Details:     []RequestDetail{original},
					},
				},
			},
		},
	}
	result := stats.MergeSnapshot(imported)
	if result.Added != 1 || result.Skipped != 1 {
		t.Fatalf("merge result = %#v, want original added and migrated twin retired", result)
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 2 {
		t.Fatalf("total requests = %d, want twin collapsed", snapshot.TotalRequests)
	}
	_, claudeAPI := findAPIByAuthID(t, snapshot, "claude:apikey:f4210cff4b55")
	flash := claudeAPI.Models["deepseek-v4-flash"]
	if len(flash.Details) != 1 || flash.Details[0].Provider != "claude" ||
		flash.Details[0].Tokens.InputTokens != 434 || flash.Details[0].Tokens.CachedTokens != 4480 {
		t.Fatalf("merged claude details = %#v, want original tokens restored", flash.Details)
	}
	for name, api := range snapshot.APIs {
		for modelName, model := range api.Models {
			for _, detail := range model.Details {
				if modelName == "deepseek-v4-flash" && detail.Provider != "claude" {
					t.Fatalf("group %q still holds migrated twin %#v", name, detail)
				}
			}
		}
	}
}

// TestRepairPreservesGenuineDistinctRecords 验证指纹不同的真实记录不受修复影响:
// 两个上游各自的真实 DeepSeek 请求(时间戳/延迟不同)必须全部保留。
func TestRepairPreservesGenuineDistinctRecords(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(repairTestOriginalClaudeRecord())
	stats.Record(repairTestGenuineOpenAICompatRecord())

	if result := stats.MergeSnapshot(StatisticsSnapshot{}); result.Skipped != 0 {
		t.Fatalf("empty merge result = %#v, want no repair on genuine records", result)
	}
	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 2 || len(snapshot.APIs) != 2 {
		t.Fatalf("snapshot = %d requests %d groups, want both genuine upstreams kept", snapshot.TotalRequests, len(snapshot.APIs))
	}
}
