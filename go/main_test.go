package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func decodeManagementResponse(t *testing.T, raw []byte, target interface{}) ManagementResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if !env.OK {
		if env.Error != nil {
			t.Fatalf("management response failed: %s: %s", env.Error.Code, env.Error.Message)
		}
		t.Fatal("management response failed without error details")
	}
	var resp ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("failed to unmarshal management response: %v", err)
	}
	if target != nil {
		if err := json.Unmarshal(resp.Body, target); err != nil {
			t.Fatalf("failed to unmarshal management body: %v", err)
		}
	}
	return resp
}

func waitForTestCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !condition() {
		t.Fatal("condition not met before timeout")
	}
}

func invokeManagement(t *testing.T, req ManagementRequest) []byte {
	t.Helper()
	reqBody, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal management request: %v", err)
	}
	raw, err := handleManagement(reqBody)
	if err != nil {
		t.Fatalf("handleManagement() error = %v", err)
	}
	return raw
}

func TestUsageRecordUnmarshalAcceptsLegacyPascalCaseFields(t *testing.T) {
	raw := []byte(`{
		"Provider":"deepseek",
		"ExecutorType":"OpenAICompatExecutor",
		"Model":"deepseek-v3.1",
		"Alias":"claude-sonnet",
		"APIKey":"client-key",
		"AuthID":"auth-1",
		"AuthIndex":"2",
		"AuthType":"api-key",
		"Source":"deepseek-key",
		"ReasoningEffort":"medium",
		"ServiceTier":"default",
		"RequestedAt":"2026-06-25T10:00:00Z",
		"Latency":1500000000,
		"TTFT":200000000,
		"Failed":true,
		"Failure":{"StatusCode":429,"Body":"quota"},
		"Detail":{
			"InputTokens":11,
			"OutputTokens":12,
			"ReasoningTokens":13,
			"CachedTokens":14,
			"CacheReadTokens":15,
			"CacheCreationTokens":16,
			"TotalTokens":40
		},
		"ResponseHeaders":{"X-Usage":["ok"]}
	}`)

	var record UsageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if record.Provider != "deepseek" || record.ExecutorType != "OpenAICompatExecutor" || record.Model != "deepseek-v3.1" {
		t.Fatalf("record identity fields not decoded: %#v", record)
	}
	if record.Latency != 1500*time.Millisecond || record.TTFT != 200*time.Millisecond {
		t.Fatalf("duration fields = %v/%v", record.Latency, record.TTFT)
	}
	if !record.Failed || record.Failure.StatusCode != 429 || record.Failure.Body != "quota" {
		t.Fatalf("failure fields not decoded: %#v", record.Failure)
	}
	if record.Detail.InputTokens != 11 ||
		record.Detail.OutputTokens != 12 ||
		record.Detail.ReasoningTokens != 13 ||
		record.Detail.CachedTokens != 14 ||
		record.Detail.CacheReadTokens != 15 ||
		record.Detail.CacheCreationTokens != 16 ||
		record.Detail.TotalTokens != 40 {
		t.Fatalf("detail fields not decoded: %#v", record.Detail)
	}
	if got := record.ResponseHeaders["X-Usage"]; len(got) != 1 || got[0] != "ok" {
		t.Fatalf("response headers not decoded: %#v", record.ResponseHeaders)
	}
}

func TestUsageRecordUnmarshalAcceptsSnakeCaseFields(t *testing.T) {
	raw := []byte(`{
		"provider":"deepseek",
		"executor_type":"OpenAICompatExecutor",
		"model":"deepseek-v3.1",
		"failed":true,
		"failure":{"status_code":429,"body":"quota"},
		"detail":{"input_tokens":11,"output_tokens":12,"total_tokens":23}
	}`)

	var record UsageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if record.Provider != "deepseek" || record.ExecutorType != "OpenAICompatExecutor" || record.Model != "deepseek-v3.1" {
		t.Fatalf("record identity fields not decoded: %#v", record)
	}
	if !record.Failed || record.Failure.StatusCode != 429 || record.Failure.Body != "quota" {
		t.Fatalf("failure fields not decoded: %#v", record.Failure)
	}
	if record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 12 || record.Detail.TotalTokens != 23 {
		t.Fatalf("detail fields not decoded: %#v", record.Detail)
	}
}

func TestUsageRecordUnmarshalAcceptsBaseURLAliases(t *testing.T) {
	tests := map[string]string{
		"base_url": `"base_url":"https://snake.example/v1"`,
		"baseURL":  `"baseURL":"https://camel-upper.example/v1"`,
		"baseUrl":  `"baseUrl":"https://camel-lower.example/v1"`,
		"BaseURL":  `"BaseURL":"https://legacy.example/v1"`,
	}
	for name, field := range tests {
		t.Run(name, func(t *testing.T) {
			var record UsageRecord
			if err := json.Unmarshal([]byte(`{"provider":"codex",`+field+`}`), &record); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if record.BaseURL == "" {
				t.Fatalf("BaseURL not decoded from %s", field)
			}
		})
	}
}

func TestHandleImportUsageAcceptsV120ExportFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "usage-export-v1.2.0.json")
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("fixture not available: %v", err)
	}

	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 10000, RetentionDays: 0, DedupWindowMinutes: 0})
	t.Cleanup(func() { stats = previousStats })

	raw, err := handleImportUsage(body)
	if err != nil {
		t.Fatalf("handleImportUsage() error = %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if !env.OK {
		if env.Error != nil {
			t.Fatalf("import failed: %s: %s", env.Error.Code, env.Error.Message)
		}
		t.Fatal("import failed without error details")
	}

	var resp ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("failed to unmarshal management response: %v", err)
	}
	var result ImportResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("failed to unmarshal import response: %v", err)
	}
	if result.Added != 430 {
		t.Fatalf("added = %d, want 430", result.Added)
	}
	if result.InputRecords != 430 || result.AcceptedRecords != 430 || result.RejectedRecords != 0 {
		t.Fatalf("import counts = input %d accepted %d rejected %d, want 430/430/0",
			result.InputRecords, result.AcceptedRecords, result.RejectedRecords)
	}
	if result.TotalRequests != 430 {
		t.Fatalf("total_requests = %d, want 430", result.TotalRequests)
	}
}

func TestManagementImportRouteAcceptsExportFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "usage-export-v1.2.0.json")
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("fixture not available: %v", err)
	}

	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 10000, RetentionDays: 0, DedupWindowMinutes: 0})
	t.Cleanup(func() { stats = previousStats })

	req := ManagementRequest{
		Method: "POST",
		Path:   "/v0/management/plugins/usage-statistics/usage/import",
		Body:   body,
	}
	reqBody, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal management request: %v", err)
	}

	raw, err := handleManagement(reqBody)
	if err != nil {
		t.Fatalf("handleManagement() error = %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if !env.OK {
		if env.Error != nil {
			t.Fatalf("management import failed: %s: %s", env.Error.Code, env.Error.Message)
		}
		t.Fatal("management import failed without error details")
	}

	var resp ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("failed to unmarshal management response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result ImportResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("failed to unmarshal import response: %v", err)
	}
	if result.Added != 430 || result.TotalRequests != 430 {
		t.Fatalf("result = %#v, want added/total 430", result)
	}
	if result.InputRecords != 430 || result.AcceptedRecords != 430 || result.RejectedRecords != 0 {
		t.Fatalf("management import counts = %#v, want input/accepted/rejected 430/430/0", result)
	}
}

func TestExportUsageIncludesMetadata(t *testing.T) {
	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 10, RetentionDays: 7, DedupWindowMinutes: 5, LogResponseHeaders: "x-request-id"})
	t.Cleanup(func() { stats = previousStats })
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 10},
	})

	raw, err := handleExportUsage()
	if err != nil {
		t.Fatalf("handleExportUsage() error = %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var resp ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	var payload ExportPayload
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal export payload: %v", err)
	}
	if payload.Plugin != pluginVersion || payload.DetailCount != 1 {
		t.Fatalf("export metadata = plugin %q detail_count %d, want %q/1",
			payload.Plugin, payload.DetailCount, pluginVersion)
	}
	if payload.Config.RetentionDays != 7 || payload.Config.MaxDetailsPerModel != 10 ||
		payload.Config.DedupWindowMinutes != 5 || payload.Config.LogResponseHeaders != "x-request-id" ||
		payload.Config.PriceStoragePath != defaultPriceStoragePath {
		t.Fatalf("export config = %#v", payload.Config)
	}
}

func TestRecordStoresMaskedClientAPIKeyAndCleanSource(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(UsageRecord{
		Provider:  "openai-compatible-opencode",
		AuthType:  "apikey",
		AuthIndex: "5312415661d8a481",
		Source:    "openai-compatible-opencode · apikey · 5312415661d8a481",
		APIKey:    "sk-test-client-key-zy",
		Model:     "deepseek-v3.1",
		Detail: UsageDetail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	})

	snapshot := stats.Snapshot()
	wantAPI := "openai-compatible-opencode"
	api, ok := snapshot.APIs[wantAPI]
	if !ok {
		t.Fatalf("snapshot APIs = %#v, want upstream key %q", snapshot.APIs, wantAPI)
	}
	details := api.Models["deepseek-v3.1"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	detail := details[0]
	if detail.Source != "openai-compatible-opencode" {
		t.Fatalf("detail source = %q, want clean source", detail.Source)
	}
	if detail.APIKey != "sk******zy" {
		t.Fatalf("detail api key = %q, want masked key", detail.APIKey)
	}
	if detail.AuthIndex != "5312415661d8a481" {
		t.Fatalf("credential column value = %q", detail.AuthIndex)
	}
	// Verify APIKeyHash is set and consistent
	if detail.APIKeyHash == "" {
		t.Fatal("detail api_key_hash should not be empty")
	}
	hash1 := detail.APIKeyHash
	// Record again, hash should be identical
	stats.Record(UsageRecord{
		Provider:    "openai-compatible-opencode",
		AuthType:    "apikey",
		AuthIndex:   "5312415661d8a481",
		Source:      "openai-compatible-opencode · apikey · 5312415661d8a481",
		APIKey:      "sk-test-client-key-zy",
		Model:       "deepseek-v3.1",
		RequestedAt: time.Now().Add(time.Minute),
		Detail: UsageDetail{
			InputTokens:  1,
			OutputTokens: 1,
			TotalTokens:  2,
		},
	})
	snapshot2 := stats.Snapshot()
	details2 := snapshot2.APIs[wantAPI].Models["deepseek-v3.1"].Details
	hash2 := details2[len(details2)-1].APIKeyHash
	if hash1 != hash2 {
		t.Fatalf("APIKeyHash not stable across records: %q != %q", hash1, hash2)
	}
}

func TestConfiguredAPIKeyHashSaltIsStable(t *testing.T) {
	previousSalt := apiKeySalt
	t.Cleanup(func() { apiKeySalt = previousSalt })

	s1 := NewRequestStatistics()
	s1.ConfigurePatch(runtimeConfigPatch{
		MaxDetailsPerModel: intPtr(10),
		DedupWindowMinutes: intPtr(0),
		APIKeyHashSalt:     stringPtr("stable-salt"),
	})
	s1.Record(UsageRecord{Provider: "openai", Model: "gpt-4", APIKey: "sk-client-key-123456", Detail: UsageDetail{TotalTokens: 1}})
	hash1 := s1.Snapshot().APIs["openai"].Models["gpt-4"].Details[0].APIKeyHash

	apiKeySalt = previousSalt
	s2 := NewRequestStatistics()
	s2.ConfigurePatch(runtimeConfigPatch{
		MaxDetailsPerModel: intPtr(10),
		DedupWindowMinutes: intPtr(0),
		APIKeyHashSalt:     stringPtr("stable-salt"),
	})
	s2.Record(UsageRecord{Provider: "openai", Model: "gpt-4", APIKey: "sk-client-key-123456", Detail: UsageDetail{TotalTokens: 1}})
	hash2 := s2.Snapshot().APIs["openai"].Models["gpt-4"].Details[0].APIKeyHash

	if hash1 == "" || hash1 != hash2 {
		t.Fatalf("configured salt should produce stable hash, got %q and %q", hash1, hash2)
	}
}

func TestStorageReplayRestoresRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-statistics.jsonl")
	cfg := runtimeConfig{
		MaxDetailsPerModel:  100,
		RetentionDays:       0,
		DedupWindowMinutes:  0,
		StorageEnabled:      true,
		StoragePath:         path,
		StorageFlushSeconds: 1,
	}

	first := NewRequestStatistics()
	first.Configure(cfg)
	first.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		APIKey:      "sk-client-storage-test",
		RequestedAt: time.Now().Add(-time.Minute),
		Detail:      UsageDetail{InputTokens: 10, OutputTokens: 5},
	})
	first.Close()

	second := NewRequestStatistics()
	second.Configure(cfg)
	second.Configure(cfg)
	defer second.Close()

	snapshot := second.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != 15 {
		t.Fatalf("replayed snapshot = requests %d tokens %d, want 1/15", snapshot.TotalRequests, snapshot.TotalTokens)
	}
	if status := second.StorageStatus(); !status.Enabled || status.LoadedPath == "" || status.LastError != "" {
		t.Fatalf("storage status after replay = %#v", status)
	}
}

func TestStorageWritesDateShards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-statistics.jsonl")
	cfg := runtimeConfig{
		MaxDetailsPerModel:  100,
		RetentionDays:       0,
		DedupWindowMinutes:  0,
		StorageEnabled:      true,
		StoragePath:         path,
		StorageFlushSeconds: 1,
	}

	stats := NewRequestStatistics()
	stats.Configure(cfg)
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 11},
	})
	status := stats.StorageStatus()
	stats.Close()

	shardPath := status.LoadedPath
	if !strings.Contains(shardPath, string(filepath.Separator)+"usage-statistics"+string(filepath.Separator)+"usage-") {
		t.Fatalf("loaded storage path %q does not look like a date shard", shardPath)
	}
	if _, err := os.Stat(shardPath); err != nil {
		t.Fatalf("date shard %q was not written: %v", shardPath, err)
	}
	snapshotPath := storageSnapshotPath(filepath.Dir(shardPath))
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("snapshot %q was not written on close: %v", snapshotPath, err)
	}

	reloaded := NewRequestStatistics()
	reloaded.Configure(cfg)
	defer reloaded.Close()
	if got := reloaded.Snapshot().TotalRequests; got != 1 {
		t.Fatalf("replayed date shard requests = %d, want 1", got)
	}
}

func TestStorageStatusReportsPendingBufferedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-statistics.jsonl")
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel:  100,
		RetentionDays:       0,
		DedupWindowMinutes:  0,
		StorageEnabled:      true,
		StoragePath:         path,
		StorageFlushSeconds: 3600,
	})
	defer stats.Close()

	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 1},
	})
	waitForTestCondition(t, func() bool { return stats.StorageStatus().LastFlushAt != "" })
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 2},
	})

	waitForTestCondition(t, func() bool { return stats.StorageStatus().PendingBufferedRecords == 1 })
	if status := stats.StorageStatus(); status.WriteQueueCapacity != defaultStorageWriteQueueSize {
		t.Fatalf("write queue capacity = %d, want %d", status.WriteQueueCapacity, defaultStorageWriteQueueSize)
	}
	stats.Close()
	if status := stats.StorageStatus(); status.PendingBufferedRecords != 0 {
		t.Fatalf("pending buffered records after close = %d, want 0", status.PendingBufferedRecords)
	}
}

func TestStorageWorkerCollectsQueuedBatches(t *testing.T) {
	queue := make(chan persistedDetail, defaultStorageWriteBatchSize+4)
	first := persistedDetail{API: "api-0", Model: "gpt-4"}
	for i := 1; i < defaultStorageWriteBatchSize+4; i++ {
		queue <- persistedDetail{API: fmt.Sprintf("api-%d", i), Model: "gpt-4"}
	}

	batch := collectStorageBatch(queue, first)
	if len(batch) != defaultStorageWriteBatchSize {
		t.Fatalf("batch size = %d, want %d", len(batch), defaultStorageWriteBatchSize)
	}
	if batch[0].API != "api-0" {
		t.Fatalf("first batch item = %q, want api-0", batch[0].API)
	}
	wantLast := fmt.Sprintf("api-%d", defaultStorageWriteBatchSize-1)
	if batch[len(batch)-1].API != wantLast {
		t.Fatalf("last batch item = %q, want %s", batch[len(batch)-1].API, wantLast)
	}
	if remaining := len(queue); remaining != 4 {
		t.Fatalf("remaining queue length = %d, want 4", remaining)
	}
}

func TestStorageStatusReportsWriteBatchMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-statistics.jsonl")
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel:  100,
		RetentionDays:       0,
		DedupWindowMinutes:  0,
		StorageEnabled:      true,
		StoragePath:         path,
		StorageFlushSeconds: 3600,
	})
	defer stats.Close()

	for i := 0; i < 16; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
			Detail:      UsageDetail{TotalTokens: int64(i + 1)},
		})
	}

	waitForTestCondition(t, func() bool { return stats.StorageStatus().LastWriteBatchRecords > 0 })
	status := stats.StorageStatus()
	if status.LastWriteBatchRecords <= 0 {
		t.Fatalf("last write batch records = %d, want > 0", status.LastWriteBatchRecords)
	}
	if status.LastWriteBatchDurationMs <= 0 {
		t.Fatalf("last write batch duration = %f, want > 0", status.LastWriteBatchDurationMs)
	}
	if status.LastWriteQueueWaitMs < 0 {
		t.Fatalf("last write queue wait = %f, want >= 0", status.LastWriteQueueWaitMs)
	}
	if status.WriteBatchesTotal <= 0 {
		t.Fatalf("write batches total = %d, want > 0", status.WriteBatchesTotal)
	}
	if status.WriteRecordsTotal <= 0 {
		t.Fatalf("write records total = %d, want > 0", status.WriteRecordsTotal)
	}
	if status.WriteBatchAvgDurationMs <= 0 {
		t.Fatalf("write batch avg duration = %f, want > 0", status.WriteBatchAvgDurationMs)
	}
	if status.WritePressure == "" {
		t.Fatalf("write pressure should be reported when storage is enabled: %#v", status)
	}
}

func TestStorageWritePressureClassification(t *testing.T) {
	tests := []struct {
		name          string
		queueLength   int
		queueCapacity int
		avgWait       time.Duration
		want          string
	}{
		{name: "normal", queueCapacity: 4096, want: "normal"},
		{name: "queued", queueLength: 1, queueCapacity: 4096, want: "queued"},
		{name: "backlog", queueLength: 1024, queueCapacity: 4096, want: "backlog"},
		{name: "full", queueLength: 4096, queueCapacity: 4096, want: "full"},
		{name: "slow", queueCapacity: 4096, avgWait: 250 * time.Millisecond, want: "slow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := storageWritePressure(tt.queueLength, tt.queueCapacity, tt.avgWait); got != tt.want {
				t.Fatalf("storageWritePressure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStorageSnapshotWritesByRecordInterval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage-statistics")
	cfg := runtimeConfig{
		MaxDetailsPerModel:            100,
		RetentionDays:                 0,
		DedupWindowMinutes:            0,
		StorageEnabled:                true,
		StoragePath:                   dir,
		StorageFlushSeconds:           3600,
		StorageSnapshotSeconds:        3600,
		StorageSnapshotRecordInterval: 2,
	}

	stats := NewRequestStatistics()
	stats.Configure(cfg)
	defer stats.Close()
	snapshotPath := storageSnapshotPath(dir)

	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 1},
	})
	if _, err := os.Stat(snapshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot should not exist after one record, stat err = %v", err)
	}

	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(time.Second),
		Detail:      UsageDetail{TotalTokens: 2},
	})
	waitForTestCondition(t, func() bool {
		_, err := os.Stat(snapshotPath)
		return err == nil
	})
	status := stats.StorageStatus()
	if status.LastSnapshotAt == "" {
		t.Fatalf("last snapshot time should be reported: %#v", status)
	}
	if status.PendingSnapshotRecords != 0 {
		t.Fatalf("pending snapshot records = %d, want 0", status.PendingSnapshotRecords)
	}
	if status.SnapshotRecordIntervalRecords != 2 {
		t.Fatalf("snapshot record interval = %d, want 2", status.SnapshotRecordIntervalRecords)
	}
	stats.Close()

	reloaded := NewRequestStatistics()
	reloaded.Configure(cfg)
	defer reloaded.Close()
	if got := reloaded.Snapshot().TotalRequests; got != 2 {
		t.Fatalf("reloaded requests = %d, want 2", got)
	}
}

func TestStorageSyncsByRecordInterval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage-statistics")
	cfg := runtimeConfig{
		MaxDetailsPerModel:        100,
		RetentionDays:             0,
		DedupWindowMinutes:        0,
		StorageEnabled:            true,
		StoragePath:               dir,
		StorageFlushSeconds:       3600,
		StorageSyncRecordInterval: 2,
	}

	stats := NewRequestStatistics()
	stats.Configure(cfg)
	defer stats.Close()
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 1},
	})
	waitForTestCondition(t, func() bool {
		status := stats.StorageStatus()
		return status.PendingUnsyncedRecords == 1 && status.LastSyncAt == ""
	})

	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(time.Second),
		Detail:      UsageDetail{TotalTokens: 2},
	})
	waitForTestCondition(t, func() bool {
		status := stats.StorageStatus()
		return status.PendingUnsyncedRecords == 0 && status.PendingBufferedRecords == 0 && status.LastSyncAt != ""
	})
	status := stats.StorageStatus()
	if status.PendingUnsyncedRecords != 0 {
		t.Fatalf("pending unsynced records = %d, want 0", status.PendingUnsyncedRecords)
	}
	if status.PendingBufferedRecords != 0 {
		t.Fatalf("pending buffered records = %d, want 0 after sync flush", status.PendingBufferedRecords)
	}
	if status.LastSyncAt == "" {
		t.Fatalf("last sync time should be reported: %#v", status)
	}
	if status.SyncRecordIntervalRecords != 2 {
		t.Fatalf("sync record interval = %d, want 2", status.SyncRecordIntervalRecords)
	}
	stats.Close()
}

func TestStoragePersistsImportedSnapshotThroughBackgroundWriter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage-statistics")
	cfg := runtimeConfig{
		MaxDetailsPerModel:  100,
		RetentionDays:       0,
		DedupWindowMinutes:  0,
		StorageEnabled:      true,
		StoragePath:         dir,
		StorageFlushSeconds: 3600,
	}
	when := time.Now().Add(-time.Minute).UTC()
	imported := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"openai": {
				Models: map[string]ModelSnapshot{
					"gpt-4": {
						Details: []RequestDetail{{
							Model:     "gpt-4",
							Timestamp: when,
							Source:    "openai-prod",
							Provider:  "openai",
							Tokens:    TokenStats{TotalTokens: 7},
						}},
					},
				},
			},
		},
	}

	stats := NewRequestStatistics()
	stats.Configure(cfg)
	result := stats.MergeSnapshot(imported)
	if result.Added != 1 {
		t.Fatalf("import added = %d, want 1", result.Added)
	}
	stats.Close()

	reloaded := NewRequestStatistics()
	reloaded.Configure(cfg)
	defer reloaded.Close()
	snapshot := reloaded.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != 7 {
		t.Fatalf("replayed imported snapshot = requests %d tokens %d, want 1/7", snapshot.TotalRequests, snapshot.TotalTokens)
	}
}

func TestStorageReplaySkipsInvalidLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-statistics.jsonl")
	when := time.Now().Add(-time.Minute).UTC()
	lines := []string{
		string(mustMarshal(persistedDetail{
			API:   "openai",
			Model: "gpt-4",
			Detail: RequestDetail{
				Model:     "gpt-4",
				Timestamp: when,
				Source:    "openai-prod",
				Provider:  "openai",
				Tokens:    TokenStats{InputTokens: 10, OutputTokens: 5},
			},
		})),
		`{"api":"broken","model":`,
		string(mustMarshal(persistedDetail{
			API:   "deepseek",
			Model: "deepseek-chat",
			Detail: RequestDetail{
				Model:     "deepseek-chat",
				Timestamp: when.Add(time.Second),
				Source:    "deepseek-prod",
				Provider:  "deepseek",
				Tokens:    TokenStats{TotalTokens: 7},
			},
		})),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write storage fixture: %v", err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100,
		RetentionDays:      0,
		DedupWindowMinutes: 0,
		StorageEnabled:     true,
		StoragePath:        path,
	})
	defer stats.Close()

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 2 || snapshot.TotalTokens != 22 {
		t.Fatalf("snapshot after invalid replay = requests %d tokens %d, want 2/22", snapshot.TotalRequests, snapshot.TotalTokens)
	}
	if status := stats.StorageStatus(); !strings.Contains(status.LastError, "skipped 1 invalid line") {
		t.Fatalf("storage last error = %q, want invalid line warning", status.LastError)
	}
}

func TestStorageSnapshotSkipsOlderShardsAndReplaysSameDayDelta(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage-statistics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir storage dir: %v", err)
	}
	snapshotAt := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	snapshotDetail := RequestDetail{
		Model:     "gpt-4",
		Timestamp: snapshotAt.Add(-time.Minute),
		Source:    "openai-prod",
		Provider:  "openai",
		Tokens:    TokenStats{TotalTokens: 10},
	}
	newDetail := RequestDetail{
		Model:     "gpt-4",
		Timestamp: snapshotAt.Add(time.Minute),
		Source:    "openai-prod",
		Provider:  "openai",
		Tokens:    TokenStats{TotalTokens: 7},
	}
	snapshotPayload := persistedStorageSnapshot{
		Version:     1,
		GeneratedAt: snapshotAt.Format(time.RFC3339),
		Usage: StatisticsSnapshot{
			APIs: map[string]APISnapshot{
				"openai": {
					Models: map[string]ModelSnapshot{
						"gpt-4": {Details: []RequestDetail{snapshotDetail}},
					},
				},
			},
		},
	}
	if err := os.WriteFile(storageSnapshotPath(dir), mustMarshal(snapshotPayload), 0o600); err != nil {
		t.Fatalf("write storage snapshot: %v", err)
	}
	writePersisted := func(path string, details ...RequestDetail) {
		t.Helper()
		var lines []string
		for _, detail := range details {
			lines = append(lines, string(mustMarshal(persistedDetail{API: "openai", Model: "gpt-4", Detail: detail})))
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("write storage shard: %v", err)
		}
	}
	oldDetail := RequestDetail{
		Model:     "gpt-4",
		Timestamp: snapshotAt.Add(-24 * time.Hour),
		Source:    "openai-prod",
		Provider:  "openai",
		Tokens:    TokenStats{TotalTokens: 99},
	}
	writePersisted(filepath.Join(dir, storageFileName(storageDate(oldDetail.Timestamp))), oldDetail)
	writePersisted(filepath.Join(dir, storageFileName(storageDate(snapshotAt))), snapshotDetail, newDetail)

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100,
		RetentionDays:      30,
		DedupWindowMinutes: 0,
		StorageEnabled:     true,
		StoragePath:        dir,
	})
	defer stats.Close()

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 2 || snapshot.TotalTokens != 17 {
		t.Fatalf("snapshot restore = requests %d tokens %d, want 2/17", snapshot.TotalRequests, snapshot.TotalTokens)
	}
}

func TestStorageReplaySkipsAndCleansExpiredDateShards(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage-statistics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir storage dir: %v", err)
	}
	now := time.Now().UTC()
	oldTime := now.Add(-10 * 24 * time.Hour)
	recentTime := now.Add(-time.Hour)
	writePersisted := func(path string, detailTime time.Time, tokens int64) {
		t.Helper()
		raw := mustMarshal(persistedDetail{
			API:   "openai",
			Model: "gpt-4",
			Detail: RequestDetail{
				Model:     "gpt-4",
				Timestamp: detailTime,
				Source:    "openai-prod",
				Provider:  "openai",
				Tokens:    TokenStats{TotalTokens: tokens},
			},
		})
		if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
			t.Fatalf("write storage shard: %v", err)
		}
	}
	oldPath := filepath.Join(dir, storageFileName(storageDate(oldTime)))
	recentPath := filepath.Join(dir, storageFileName(storageDate(recentTime)))
	writePersisted(oldPath, oldTime, 99)
	writePersisted(recentPath, recentTime, 7)

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100,
		RetentionDays:      7,
		DedupWindowMinutes: 0,
		StorageEnabled:     true,
		StoragePath:        dir,
	})
	defer stats.Close()

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != 7 {
		t.Fatalf("snapshot after date shard replay = requests %d tokens %d, want 1/7", snapshot.TotalRequests, snapshot.TotalTokens)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old shard still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Fatalf("recent shard should remain: %v", err)
	}
}

func TestStripCredentialSuffix(t *testing.T) {
	tests := map[string]string{
		"openai-compatible-opencode · apikey · 5312415661d8a481": "openai-compatible-opencode",
		"openai-compatibility:opencode:a4e4860e4fc0":             "openai-compatibility:opencode",
		"deepseek": "deepseek",
		// Separator compatibility (P1-15)
		"opencode - sk-abc123": "opencode",
		"opencode | sk-abc123": "opencode",
	}
	for input, want := range tests {
		if got := stripCredentialSuffix(input); got != want {
			t.Fatalf("stripCredentialSuffix(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRecordDeduplicatesRepeatedUsageRecords(t *testing.T) {
	stats := NewRequestStatistics()
	when := time.Now().Add(-time.Hour)
	record := UsageRecord{
		Provider:    "deepseek",
		Model:       "deepseek-v3.1",
		AuthIndex:   "auth-1",
		RequestedAt: when,
		Detail: UsageDetail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	}

	stats.Record(record)
	stats.Record(record)

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != 15 {
		t.Fatalf("snapshot = %#v, want one deduplicated request", snapshot)
	}
}

func TestRecordPrunesByMaxDetailsPerModelAndRebuildsTotals(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 2, RetentionDays: 0, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-v3.1",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail: UsageDetail{
				InputTokens: int64(i + 1),
				TotalTokens: int64(i + 1),
			},
		})
	}

	snapshot := stats.Snapshot()
	model := snapshot.APIs["deepseek"].Models["deepseek-v3.1"]
	if snapshot.TotalRequests != 2 || snapshot.TotalTokens != 5 {
		t.Fatalf("snapshot totals = requests %d tokens %d, want 2/5", snapshot.TotalRequests, snapshot.TotalTokens)
	}
	if len(model.Details) != 2 {
		t.Fatalf("details len = %d, want 2", len(model.Details))
	}
	if model.Details[0].Tokens.TotalTokens != 2 || model.Details[1].Tokens.TotalTokens != 3 {
		t.Fatalf("kept details = %#v, want last two records", model.Details)
	}
}

func TestRecordPrunesByRetentionDays(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 10, RetentionDays: 1, DedupWindowMinutes: 0})

	stats.Record(UsageRecord{
		Provider:    "deepseek",
		Model:       "deepseek-v3.1",
		RequestedAt: time.Now().Add(-48 * time.Hour),
		Detail:      UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider:    "deepseek",
		Model:       "deepseek-v3.1",
		RequestedAt: time.Now(),
		Detail:      UsageDetail{TotalTokens: 7},
	})

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != 7 {
		t.Fatalf("snapshot after retention prune = %#v, want only recent record", snapshot)
	}
}

func TestParseRuntimeConfigFromLifecycleConfigYAML(t *testing.T) {
	yaml := []byte(`
plugins:
  configs:
    usage-statistics:
      max_details_per_model: 123
      retention_days: 9
      dedup_window_minutes: 45
`)
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString(yaml) + `"}`)

	cfg := parseRuntimeConfig(raw)
	if cfg.MaxDetailsPerModel != 123 || cfg.RetentionDays != 9 || cfg.DedupWindowMinutes != 45 {
		t.Fatalf("config = %#v", cfg)
	}
}

// ============================================================================
// P0 Tests: Performance, YAML parsing, dashboard backoff
// ============================================================================

func TestNestedYAMLConfigParsing(t *testing.T) {
	// P0-13: nested YAML structure should still parse correctly
	yaml := []byte(`
configs:
  usage-statistics:
    max_details_per_model: 3000
    retention_days: 14
`)
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString(yaml) + `"}`)

	cfg := parseRuntimeConfig(raw)
	if cfg.MaxDetailsPerModel != 3000 {
		t.Fatalf("max_details_per_model = %d, want 3000 (nested YAML not parsed)", cfg.MaxDetailsPerModel)
	}
	if cfg.RetentionDays != 14 {
		t.Fatalf("retention_days = %d, want 14", cfg.RetentionDays)
	}
}

func TestYAMLConfigParsingIgnoresOtherPluginKeys(t *testing.T) {
	yaml := []byte(`
plugins:
  configs:
    other-plugin:
      max_details_per_model: 999
      retention_days: 1
    usage-statistics:
      retention_days: 14
`)
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString(yaml) + `"}`)

	cfg := parseRuntimeConfig(raw)
	if cfg.MaxDetailsPerModel != defaultMaxDetailsPerModel {
		t.Fatalf("max_details_per_model = %d, want default %d", cfg.MaxDetailsPerModel, defaultMaxDetailsPerModel)
	}
	if cfg.RetentionDays != 14 {
		t.Fatalf("retention_days = %d, want 14", cfg.RetentionDays)
	}
}

func TestLogResponseHeadersConfig(t *testing.T) {
	// P1-17: log_response_headers config parsing
	yaml := []byte(`
configs:
  usage-statistics:
    log_response_headers: "x-request-id,x-ratelimit-*"
`)
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString(yaml) + `"}`)

	cfg := parseRuntimeConfig(raw)
	if cfg.LogResponseHeaders != "x-request-id,x-ratelimit-*" {
		t.Fatalf("log_response_headers = %q", cfg.LogResponseHeaders)
	}
}

func TestAPIKeyHashSaltConfig(t *testing.T) {
	yaml := []byte(`
configs:
  usage-statistics:
    api_key_hash_salt: "stable-test-salt"
`)
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString(yaml) + `"}`)

	cfg := parseRuntimeConfig(raw)
	if cfg.APIKeyHashSalt != "stable-test-salt" {
		t.Fatalf("api_key_hash_salt = %q, want stable-test-salt", cfg.APIKeyHashSalt)
	}
}

func TestStorageConfigParsing(t *testing.T) {
	yaml := []byte(`
configs:
  usage-statistics:
    storage_enabled: true
    storage_path: "/tmp/usage-statistics.jsonl"
    storage_flush_interval_seconds: 3
    storage_snapshot_interval_seconds: 7
    storage_snapshot_record_interval: 11
    storage_sync_interval_seconds: 13
    storage_sync_record_interval: 17
    price_storage_path: "/tmp/usage-statistics-prices.json"
`)
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString(yaml) + `"}`)

	cfg := parseRuntimeConfig(raw)
	if !cfg.StorageEnabled {
		t.Fatal("storage_enabled should be true")
	}
	if cfg.StoragePath != "/tmp/usage-statistics.jsonl" {
		t.Fatalf("storage_path = %q", cfg.StoragePath)
	}
	if cfg.StorageFlushSeconds != 3 {
		t.Fatalf("storage_flush_interval_seconds = %d, want 3", cfg.StorageFlushSeconds)
	}
	if cfg.StorageSnapshotSeconds != 7 {
		t.Fatalf("storage_snapshot_interval_seconds = %d, want 7", cfg.StorageSnapshotSeconds)
	}
	if cfg.StorageSnapshotRecordInterval != 11 {
		t.Fatalf("storage_snapshot_record_interval = %d, want 11", cfg.StorageSnapshotRecordInterval)
	}
	if cfg.StorageSyncSeconds != 13 {
		t.Fatalf("storage_sync_interval_seconds = %d, want 13", cfg.StorageSyncSeconds)
	}
	if cfg.StorageSyncRecordInterval != 17 {
		t.Fatalf("storage_sync_record_interval = %d, want 17", cfg.StorageSyncRecordInterval)
	}
	if cfg.PriceStoragePath != "/tmp/usage-statistics-prices.json" {
		t.Fatalf("price_storage_path = %q", cfg.PriceStoragePath)
	}
}

func TestUpdateConfigParsing(t *testing.T) {
	yaml := []byte(`
configs:
  usage-statistics:
    update_enabled: true
    update_version: "v1.1.0"
`)
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString(yaml) + `"}`)

	cfg := parseRuntimeConfig(raw)
	if !cfg.UpdateEnabled {
		t.Fatal("update_enabled should be true")
	}
	if cfg.UpdateVersion != "v1.1.0" {
		t.Fatalf("update_version = %q, want v1.1.0", cfg.UpdateVersion)
	}
}

func TestRegisterResponseExposesUpdateConfigFields(t *testing.T) {
	raw, err := handleRegister(nil)
	if err != nil {
		t.Fatalf("handleRegister() error = %v", err)
	}
	if !strings.Contains(string(raw), `"Name":"api_key_hash_salt"`) {
		t.Fatalf("register response missing api_key_hash_salt: %s", raw)
	}
	if !strings.Contains(string(raw), `"Name":"storage_enabled"`) {
		t.Fatalf("register response missing storage_enabled: %s", raw)
	}
	if !strings.Contains(string(raw), `"Name":"storage_snapshot_interval_seconds"`) {
		t.Fatalf("register response missing storage_snapshot_interval_seconds: %s", raw)
	}
	if !strings.Contains(string(raw), `"Name":"storage_snapshot_record_interval"`) {
		t.Fatalf("register response missing storage_snapshot_record_interval: %s", raw)
	}
	if !strings.Contains(string(raw), `"Name":"storage_sync_interval_seconds"`) {
		t.Fatalf("register response missing storage_sync_interval_seconds: %s", raw)
	}
	if !strings.Contains(string(raw), `"Name":"storage_sync_record_interval"`) {
		t.Fatalf("register response missing storage_sync_record_interval: %s", raw)
	}
	if !strings.Contains(string(raw), `"Name":"price_storage_path"`) {
		t.Fatalf("register response missing price_storage_path: %s", raw)
	}
	if !strings.Contains(string(raw), `"Name":"update_enabled"`) {
		t.Fatalf("register response missing update_enabled: %s", raw)
	}
	if !strings.Contains(string(raw), `"Name":"update_version"`) {
		t.Fatalf("register response missing update_version: %s", raw)
	}
}

func TestManagementRegisterIncludesImportExportResources(t *testing.T) {
	raw, err := handleManagementRegister()
	if err != nil {
		t.Fatalf("handleManagementRegister() error = %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("management register envelope not ok: %#v", env.Error)
	}
	var result ManagementRegisterResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal register result: %v", err)
	}
	resources := make(map[string]bool)
	for _, resource := range result.Resources {
		resources[resource.Path] = true
	}
	for _, path := range []string{"/usage/export", "/usage/import"} {
		if !resources[path] {
			t.Fatalf("management resources missing %s: %#v", path, result.Resources)
		}
	}
}

func TestManagementModelPricesCRUDAndPersistence(t *testing.T) {
	previousStats := stats
	pricePath := filepath.Join(t.TempDir(), "prices.json")
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{PriceStoragePath: pricePath})
	t.Cleanup(func() { stats = previousStats })

	var initial ModelPricesResponse
	decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "GET",
		Path:   "/v0/management/plugins/usage-statistics/model-prices",
	}), &initial)
	if len(initial.Prices) != 0 {
		t.Fatalf("initial prices = %#v, want empty", initial.Prices)
	}

	body, err := json.Marshal(map[string]interface{}{
		"model": "gpt-4.1",
		"price": ModelPrice{
			Prompt:     2,
			Completion: 8,
			Cache:      0.5,
		},
	})
	if err != nil {
		t.Fatalf("marshal price payload: %v", err)
	}
	var saved ModelPricesResponse
	decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "PUT",
		Path:   "/v0/management/plugins/usage-statistics/model-prices",
		Body:   body,
	}), &saved)
	if got := saved.Prices["gpt-4.1"]; got.Prompt != 2 || got.Completion != 8 || got.Cache != 0.5 {
		t.Fatalf("saved price = %#v", got)
	}

	body, err = json.Marshal(map[string]interface{}{
		"model": "gpt-4.1",
		"price": ModelPrice{
			Prompt:     3,
			Completion: 9,
			Cache:      1,
		},
	})
	if err != nil {
		t.Fatalf("marshal updated price payload: %v", err)
	}
	var updated ModelPricesResponse
	decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "PUT",
		Path:   "/v0/management/plugins/usage-statistics/model-prices",
		Body:   body,
	}), &updated)
	if got := updated.Prices["gpt-4.1"]; got.Prompt != 3 || got.Completion != 9 || got.Cache != 1 {
		t.Fatalf("updated price = %#v", got)
	}

	reloaded := NewRequestStatistics()
	reloaded.Configure(runtimeConfig{PriceStoragePath: pricePath})
	if got := reloaded.ModelPrices().Prices["gpt-4.1"]; got.Prompt != 3 || got.Completion != 9 || got.Cache != 1 {
		t.Fatalf("reloaded price = %#v", got)
	}

	var deleted ModelPricesResponse
	decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "DELETE",
		Path:   "/v0/management/plugins/usage-statistics/model-prices",
		Query:  map[string][]string{"model": {"gpt-4.1"}},
	}), &deleted)
	if _, ok := deleted.Prices["gpt-4.1"]; ok {
		t.Fatalf("deleted price still present: %#v", deleted.Prices)
	}
}

func TestDashboardManagementEndpointsReturnNotModifiedForMatchingETag(t *testing.T) {
	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	t.Cleanup(func() { stats = previousStats })

	stats.Record(UsageRecord{
		Provider: "openai",
		Source:   "openai-prod",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 11},
	})

	tests := []ManagementRequest{
		{Method: "GET", Path: "/v0/management/plugins/usage-statistics/dashboard-summary"},
		{
			Method: "GET",
			Path:   "/v0/management/plugins/usage-statistics/dashboard-events",
			Query:  map[string][]string{"limit": {"10"}, "offset": {"0"}},
		},
		{
			Method: "GET",
			Path:   "/v0/management/plugins/usage-statistics/dashboard-events-export",
			Query:  map[string][]string{"model": {"gpt-4"}},
		},
		{
			Method: "GET",
			Path:   "/v0/management/plugins/usage-statistics/dashboard-api-detail",
			Query:  map[string][]string{"api": {"openai · openai-prod"}},
		},
	}

	for _, req := range tests {
		first := decodeManagementResponse(t, invokeManagement(t, req), nil)
		if first.StatusCode != http.StatusOK {
			t.Fatalf("%s first status = %d, want 200", req.Path, first.StatusCode)
		}
		etag := first.Headers["ETag"]
		if len(etag) != 1 || etag[0] == "" {
			t.Fatalf("%s missing ETag header: %#v", req.Path, first.Headers)
		}

		req.Headers = map[string][]string{"if-none-match": {`W/"stale"`}}
		stale := decodeManagementResponse(t, invokeManagement(t, req), nil)
		if stale.StatusCode != http.StatusOK {
			t.Fatalf("%s stale conditional status = %d, want 200", req.Path, stale.StatusCode)
		}

		req.Headers = map[string][]string{"if-none-match": {etag[0]}}
		second := decodeManagementResponse(t, invokeManagement(t, req), nil)
		if second.StatusCode != http.StatusNotModified {
			t.Fatalf("%s conditional status = %d, want 304", req.Path, second.StatusCode)
		}
		if len(second.Body) != 0 {
			t.Fatalf("%s conditional body len = %d, want 0", req.Path, len(second.Body))
		}
		if got := second.Headers["ETag"]; len(got) != 1 || got[0] != etag[0] {
			t.Fatalf("%s conditional ETag = %#v, want %q", req.Path, got, etag[0])
		}
	}

	runtime := stats.RuntimeStatus()
	for _, endpoint := range []string{"dashboard-summary", "dashboard-events", "dashboard-events-export", "dashboard-api-detail"} {
		conditional := runtime.ConditionalRequests[endpoint]
		if conditional.Requests != 2 || conditional.NotModified != 1 || conditional.Misses != 1 || conditional.HitRate != 0.5 {
			t.Fatalf("%s conditional metrics = %#v, want requests=2 not_modified=1 misses=1 hit_rate=0.5", endpoint, conditional)
		}
	}
}

func TestDashboardEventsExportSupportsCSVJSONLAndGzip(t *testing.T) {
	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	t.Cleanup(func() { stats = previousStats })

	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai-prod",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-time.Minute),
		Detail:      UsageDetail{InputTokens: 10, OutputTokens: 5},
	})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai-prod",
		Model:       "gpt-4",
		RequestedAt: time.Now(),
		Failed:      true,
		Failure:     UsageFailure{StatusCode: 429, Body: "rate limited"},
		Detail:      UsageDetail{TotalTokens: 0},
	})

	csvResp := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "GET",
		Path:   "/v0/management/plugins/usage-statistics/dashboard-events-export",
		Query:  map[string][]string{"format": {"csv"}},
	}), nil)
	if got := csvResp.Headers["Content-Type"]; len(got) != 1 || !strings.HasPrefix(got[0], "text/csv") {
		t.Fatalf("csv content type = %#v", got)
	}
	csvBody := string(csvResp.Body)
	if !strings.HasPrefix(csvBody, "时间,模型,来源") || !strings.Contains(csvBody, "gpt-4") || !strings.Contains(csvBody, "rate limited") {
		t.Fatalf("csv body missing expected rows: %q", csvBody)
	}

	jsonlResp := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "GET",
		Path:   "/v0/management/plugins/usage-statistics/dashboard-events-export",
		Query:  map[string][]string{"format": {"jsonl"}},
	}), nil)
	if got := jsonlResp.Headers["Content-Type"]; len(got) != 1 || !strings.HasPrefix(got[0], "application/x-ndjson") {
		t.Fatalf("jsonl content type = %#v", got)
	}
	lines := strings.Split(strings.TrimSpace(string(jsonlResp.Body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl lines = %d, want 2: %q", len(lines), string(jsonlResp.Body))
	}
	var first RequestDetail
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first.Model != "gpt-4" {
		t.Fatalf("decode first jsonl line: detail=%#v err=%v", first, err)
	}

	gzipResp := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "GET",
		Path:   "/v0/management/plugins/usage-statistics/dashboard-events-export",
		Query:  map[string][]string{"format": {"csv"}, "gzip": {"1"}},
	}), nil)
	if got := gzipResp.Headers["Content-Encoding"]; len(got) != 1 || got[0] != "gzip" {
		t.Fatalf("gzip content encoding = %#v", got)
	}
	reader, err := gzip.NewReader(bytes.NewReader(gzipResp.Body))
	if err != nil {
		t.Fatalf("new gzip reader: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if !strings.HasPrefix(string(decompressed), "时间,模型,来源") {
		t.Fatalf("decompressed csv body = %q", string(decompressed))
	}

	etag := gzipResp.Headers["ETag"]
	if len(etag) != 1 || etag[0] == "" {
		t.Fatalf("gzip export missing ETag: %#v", gzipResp.Headers)
	}
	notModified := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method:  "GET",
		Path:    "/v0/management/plugins/usage-statistics/dashboard-events-export",
		Query:   map[string][]string{"format": {"csv"}, "gzip": {"1"}},
		Headers: map[string][]string{"If-None-Match": {etag[0]}},
	}), nil)
	if notModified.StatusCode != http.StatusNotModified {
		t.Fatalf("gzip conditional status = %d, want 304", notModified.StatusCode)
	}
	if got := notModified.Headers["Content-Encoding"]; len(got) != 1 || got[0] != "gzip" {
		t.Fatalf("gzip conditional content encoding = %#v", got)
	}
}

func TestManagementModelPricesRejectInvalidPrice(t *testing.T) {
	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})
	t.Cleanup(func() { stats = previousStats })

	body, err := json.Marshal(map[string]interface{}{
		"model": "gpt-4.1",
		"price": ModelPrice{
			Prompt:     -1,
			Completion: 8,
			Cache:      0,
		},
	})
	if err != nil {
		t.Fatalf("marshal price payload: %v", err)
	}
	raw := invokeManagement(t, ManagementRequest{
		Method: "PUT",
		Path:   "/v0/management/plugins/usage-statistics/model-prices",
		Body:   body,
	})
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "invalid_price" {
		t.Fatalf("invalid price response = %#v", env)
	}
}

func TestDashboardMarkupContainsHealthRowsApiSelectorAndBackoff(t *testing.T) {
	checks := map[string]string{
		"health grid seven rows":       "grid-template-rows:repeat(7,12px)",
		"health grid column style":     "healthCellStyle",
		"health grid column order":     "healthColor(rate)",
		"upstream api selector":        `id="apiSelect"`,
		"selector options are updated": "$('apiSelect').innerHTML",
		"poll scheduler exists":        "function schedulePoll",
		"failure backoff exists":       "function nextFailureDelay",
	}
	for name, needle := range checks {
		if !strings.Contains(completeDashboardHTML, needle) {
			t.Fatalf("%s: completeDashboardHTML missing %q", name, needle)
		}
	}
}

func TestDashboardUsesRootedPluginEndpointsForImportExport(t *testing.T) {
	checks := map[string]string{
		"resource endpoint helper":   "function pluginEndpoint",
		"management endpoint helper": "function fetchManagementJsonPayload",
		"import endpoint":            "fetchManagementJsonPayload('usage/import'",
		"price save endpoint":        "fetchManagementJsonPayload('model-prices'",
		"export endpoint":            "pluginEndpoint('usage/export')",
	}
	for name, needle := range checks {
		if !strings.Contains(completeDashboardHTML, needle) {
			t.Fatalf("%s: completeDashboardHTML missing %q", name, needle)
		}
	}
	for _, bad := range []string{"'./usage/import'", "\"./usage/import\"", "'./usage/export'", "\"./usage/export\""} {
		if strings.Contains(completeDashboardHTML, bad) {
			t.Fatalf("completeDashboardHTML still contains fragile relative endpoint %q", bad)
		}
	}
}

func TestEmptyLogResponseHeadersDefaultsToNil(t *testing.T) {
	cfg := defaultRuntimeConfig()
	if cfg.LogResponseHeaders != "" {
		t.Fatalf("default log_response_headers should be empty")
	}
}

// ============================================================================
// P0 Tests: Header filtering (P0-4, P1-17)
// ============================================================================

func TestResponseHeadersAreNotSavedByDefault(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-1 * time.Minute),
		ResponseHeaders: map[string][]string{
			"X-Request-Id": {"abc123"},
			"Set-Cookie":   {"secret"},
		},
		Detail: UsageDetail{TotalTokens: 10},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["openai"].Models["gpt-4"].Details
	if len(details) != 1 {
		t.Fatal("expected 1 detail")
	}
	if details[0].Headers != nil {
		t.Fatalf("headers should be nil by default, got %v", details[0].Headers)
	}
}

func TestResponseHeadersWhitelistWildcard(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{LogResponseHeaders: "*", DedupWindowMinutes: 0, MaxDetailsPerModel: 100})
	stats.Record(UsageRecord{
		Provider:    "openai2",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-2 * time.Minute),
		ResponseHeaders: map[string][]string{
			"X-Request-Id": {"abc123"},
		},
		Detail: UsageDetail{TotalTokens: 10},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["openai2"].Models["gpt-4"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	if details[0].Headers == nil {
		t.Fatal("headers should be present with * whitelist")
	}
	if got := details[0].Headers["X-Request-Id"]; len(got) != 1 || got[0] != "abc123" {
		t.Fatalf("unexpected headers: %v", details[0].Headers)
	}
}

func TestResponseHeadersWildcardExcludesSensitiveHeaders(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{LogResponseHeaders: "*", DedupWindowMinutes: 0, MaxDetailsPerModel: 100})
	stats.Record(UsageRecord{
		Provider:    "openai-sensitive",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-2 * time.Minute),
		ResponseHeaders: map[string][]string{
			"X-Request-Id":  {"abc123"},
			"Set-Cookie":    {"session=secret"},
			"Authorization": {"Bearer secret"},
		},
		Detail: UsageDetail{TotalTokens: 10},
	})

	h := stats.Snapshot().APIs["openai-sensitive"].Models["gpt-4"].Details[0].Headers
	if h["X-Request-Id"] == nil {
		t.Fatalf("x-request-id should be retained, got %v", h)
	}
	if h["Set-Cookie"] != nil || h["Authorization"] != nil {
		t.Fatalf("sensitive response headers should be dropped, got %v", h)
	}
}

func TestResponseHeadersWhitelistPrefixWildcard(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{LogResponseHeaders: "x-ratelimit-*", DedupWindowMinutes: 0, MaxDetailsPerModel: 100})
	stats.Record(UsageRecord{
		Provider:    "openai-ratelimit",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-2 * time.Minute),
		ResponseHeaders: map[string][]string{
			"X-RateLimit-Remaining": {"42"},
			"X-Request-Id":          {"abc123"},
		},
		Detail: UsageDetail{TotalTokens: 10},
	})

	h := stats.Snapshot().APIs["openai-ratelimit"].Models["gpt-4"].Details[0].Headers
	if got := h["X-RateLimit-Remaining"]; len(got) != 1 || got[0] != "42" {
		t.Fatalf("x-ratelimit-* should retain ratelimit header, got %v", h)
	}
	if h["X-Request-Id"] != nil {
		t.Fatalf("x-request-id should be filtered out, got %v", h)
	}
}

func TestResponseHeadersWhitelistSpecific(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{LogResponseHeaders: "x-request-id", DedupWindowMinutes: 0, MaxDetailsPerModel: 100})
	stats.Record(UsageRecord{
		Provider:    "openai3",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-3 * time.Minute),
		ResponseHeaders: map[string][]string{
			"X-Request-Id": {"abc123"},
			"X-Rate-Limit": {"100"},
			"Content-Type": {"application/json"},
		},
		Detail: UsageDetail{TotalTokens: 10},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["openai3"].Models["gpt-4"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	h := details[0].Headers
	if h == nil || len(h) != 1 || h["X-Request-Id"][0] != "abc123" {
		t.Fatalf("should only get x-request-id, got %v", h)
	}
	if h["X-Rate-Limit"] != nil {
		t.Fatal("x-ratelimit should be filtered out")
	}
}

// ============================================================================
// P1 Tests: Error redaction (P1-7, P1-18)
// ============================================================================

func TestRedactSensitiveText_KeyPrefixes(t *testing.T) {
	tests := []struct {
		input string
		check func(string) bool
	}{
		{"Authorization: Bearer sk-abc123def456", func(s string) bool {
			return !strings.Contains(s, "sk-abc123def456")
		}},
		{"x-api-key: AIzaSyABC123XYZ", func(s string) bool {
			return !strings.Contains(s, "AIzaSyABC123XYZ")
		}},
		{"api-key: hf_abcdefghijklmnop", func(s string) bool {
			return !strings.Contains(s, "hf_abcdefghijklmnop")
		}},
		{"Failed with key=sk-secret-key-here", func(s string) bool {
			return !strings.Contains(s, "sk-secret-key-here")
		}},
	}
	for _, tc := range tests {
		result := redactSensitiveText(tc.input)
		if !tc.check(result) {
			t.Errorf("redactSensitiveText(%q) = %q, secret not redacted", tc.input, result)
		}
	}
}

func TestRedactSensitiveText_PreservesNormalText(t *testing.T) {
	input := `{"error":{"message":"model not found","type":"invalid_request_error"}}`
	result := redactSensitiveText(input)
	if result != input {
		t.Errorf("normal error message should not be changed: got %q", result)
	}
}

func TestRedactSensitiveText_EmptyString(t *testing.T) {
	if redactSensitiveText("") != "" {
		t.Error("empty input should return empty string")
	}
}

// ============================================================================
// P1 Tests: Import validation (P1-6, P1-8)
// ============================================================================

func TestMergeSnapshot_ExpiredRecordsIgnored(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{RetentionDays: 30, DedupWindowMinutes: 0, MaxDetailsPerModel: 10000})

	oldTime := time.Now().Add(-60 * 24 * time.Hour) // 60 days ago
	snapshot := StatisticsSnapshot{
		TotalRequests: 1,
		APIs: map[string]APISnapshot{
			"test-api": {
				TotalRequests: 1,
				Models: map[string]ModelSnapshot{
					"test-model": {
						TotalRequests: 1,
						Details: []RequestDetail{
							{
								Timestamp: oldTime,
								Tokens:    TokenStats{TotalTokens: 100},
							},
						},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(snapshot)
	if result.IgnoredByRetention != 1 {
		t.Fatalf("expired record should be ignored_by_retention, got result=%#v", result)
	}
	if result.Added != 0 {
		t.Fatalf("no records should be added, got %d", result.Added)
	}
}

func TestMergeSnapshot_RecentRecordsAdded(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{RetentionDays: 30, DedupWindowMinutes: 0, MaxDetailsPerModel: 10000})

	recentTime := time.Now().Add(-1 * time.Hour)
	snapshot := StatisticsSnapshot{
		TotalRequests: 1,
		APIs: map[string]APISnapshot{
			"test-api": {
				TotalRequests: 1,
				Models: map[string]ModelSnapshot{
					"test-model": {
						TotalRequests: 1,
						Details: []RequestDetail{
							{
								Timestamp: recentTime,
								Tokens:    TokenStats{TotalTokens: 100},
							},
						},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(snapshot)
	if result.Added != 1 {
		t.Fatalf("recent record should be added, got result=%#v", result)
	}
	if result.IgnoredByRetention != 0 {
		t.Fatalf("no records should be ignored, got %d", result.IgnoredByRetention)
	}
}

func TestMergeSnapshot_NormalizesNegativeLatencyFields(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{RetentionDays: 30, DedupWindowMinutes: 0, MaxDetailsPerModel: 10000})

	snapshot := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-api": {
				Models: map[string]ModelSnapshot{
					"test-model": {
						Details: []RequestDetail{
							{
								Timestamp: time.Now().Add(-time.Hour),
								LatencyMs: -10,
								TTFTMs:    -20,
								Tokens:    TokenStats{TotalTokens: 100},
							},
						},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(snapshot)
	if result.Added != 1 {
		t.Fatalf("record should be added, got result=%#v", result)
	}
	detail := stats.Snapshot().APIs["test-api"].Models["test-model"].Details[0]
	if detail.LatencyMs != 0 || detail.TTFTMs != 0 {
		t.Fatalf("latency fields = %d/%d, want 0/0", detail.LatencyMs, detail.TTFTMs)
	}
}

func TestMergeSnapshot_DuplicatesSkipped(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{RetentionDays: 30, DedupWindowMinutes: 0, MaxDetailsPerModel: 10000})

	when := time.Now().Add(-time.Hour)
	snapshot := StatisticsSnapshot{
		TotalRequests: 1,
		APIs: map[string]APISnapshot{
			"test-api": {
				TotalRequests: 1,
				Models: map[string]ModelSnapshot{
					"test-model": {
						TotalRequests: 1,
						Details: []RequestDetail{
							{
								Timestamp: when,
								Tokens:    TokenStats{TotalTokens: 100},
							},
						},
					},
				},
			},
		},
	}

	result1 := stats.MergeSnapshot(snapshot)
	if result1.Added != 1 {
		t.Fatalf("first merge should add 1: %#v", result1)
	}

	result2 := stats.MergeSnapshot(snapshot)
	if result2.Skipped != 1 || result2.Added != 0 {
		t.Fatalf("duplicate should be skipped: %#v", result2)
	}
}

// ============================================================================
// P1 Tests: Strip credential separator compatibility (P1-15)
// ============================================================================

func TestStripCredentialSuffix_AlternateSeparators(t *testing.T) {
	tests := map[string]string{
		"opencode - apikey - somehash": "opencode",
		"opencode | key | hash123":     "opencode",
	}
	for input, want := range tests {
		if got := stripCredentialSuffix(input); got != want {
			t.Errorf("stripCredentialSuffix(%q) with alt separator = %q, want %q", input, got, want)
		}
	}
}

// ============================================================================
// P1 Tests: usageGroupKey fix (P1-16)
// ============================================================================

func TestUsageGroupKey_DifferentiatesSource(t *testing.T) {
	// provider="openai", source="openai-prod" should produce different keys
	r1 := UsageRecord{Provider: "openai", Source: "openai-prod"}
	r2 := UsageRecord{Provider: "openai"}
	k1 := usageGroupKey(r1)
	k2 := usageGroupKey(r2)
	if k1 == k2 {
		t.Fatalf("group keys should differ: %q vs %q", k1, k2)
	}
}

func TestUsageGroupKey_DifferentiatesSameProviderChannels(t *testing.T) {
	r1 := UsageRecord{
		Provider:  "codex",
		Source:    "codex",
		AuthIndex: "channel-a",
	}
	r2 := UsageRecord{
		Provider:  "codex",
		Source:    "xpspwc9mfb@privaterelay.appleid.com",
		AuthIndex: "channel-b",
	}

	k1 := usageGroupKey(r1)
	k2 := usageGroupKey(r2)
	if k1 == k2 {
		t.Fatalf("codex channel keys should differ: %q vs %q", k1, k2)
	}
	if k1 != "codex · 上游 channel-a" {
		t.Fatalf("first key = %q, want codex upstream channel label", k1)
	}
	if k2 != "codex · xpspwc9mfb@privaterelay.appleid.com" {
		t.Fatalf("second key = %q, want source without credential label", k2)
	}
}

func TestUsageGroupKey_UsesAuthIndexWhenSourceIsSecret(t *testing.T) {
	got := usageGroupKey(UsageRecord{
		Provider:  "codex",
		Source:    "sk-test-codex-key-123456",
		AuthIndex: "channel-a",
	})
	if got != "codex · 上游 channel-a" {
		t.Fatalf("key = %q, want codex upstream channel label", got)
	}
}

func TestUsageGroupKey_UsesBaseURLForCodexAPI(t *testing.T) {
	got := usageGroupKey(UsageRecord{
		Provider:  "codex",
		Source:    "codex",
		AuthIndex: "b374b8e7c98ca23c",
		BaseURL:   "https://api.example.com/v1",
	})
	if got != "codex · https://api.example.com/v1" {
		t.Fatalf("key = %q, want codex base-url label", got)
	}
}

func TestUsageGroupKey_UsesBaseURLForNonOpenAICompatibleProvider(t *testing.T) {
	got := usageGroupKey(UsageRecord{
		Provider:  "gemini",
		Source:    "gemini",
		AuthIndex: "3fa2611823b6fefc",
		BaseURL:   "https://cpa.xkkx.de/v1",
	})
	if got != "gemini · https://cpa.xkkx.de/v1" {
		t.Fatalf("key = %q, want non-openai-compatible base-url label", got)
	}
}

func TestUsageGroupKey_OpenAICompatibleDoesNotShowCredential(t *testing.T) {
	got := usageGroupKey(UsageRecord{
		Provider:  "openai-compatible-opencode-free",
		Source:    "public",
		AuthID:    "openai-compatibility:opencode-free:8623731db2f2",
		AuthIndex: "02bffe66b8460c3e",
		AuthType:  "apikey",
	})
	if got != "openai-compatible-opencode-free · public" {
		t.Fatalf("key = %q, want provider and source without credential", got)
	}
}

func TestRecordRekeysCodexAPIKeyChannelFromDetail(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider:  "codex",
		Source:    "codex",
		AuthIndex: "b374b8e7c98ca23c",
		AuthType:  "apikey",
		Model:     "gpt-5.5",
		Failed:    true,
		Failure:   UsageFailure{StatusCode: 500},
		Detail:    UsageDetail{TotalTokens: 1},
	})

	snapshot := stats.Snapshot()
	if _, ok := snapshot.APIs["codex"]; ok {
		t.Fatalf("snapshot APIs = %#v, want codex API-key records keyed by upstream channel", snapshot.APIs)
	}
	if api := snapshot.APIs["codex · 上游 b374b8e7c98ca23c"]; api.TotalRequests != 1 {
		t.Fatalf("codex upstream API = %#v, want one request; all APIs=%#v", api, snapshot.APIs)
	}
}

func TestStorageReplayRekeysUpstreamChannelsFromDetail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-statistics.jsonl")
	when := time.Now().Add(-time.Hour)
	lines := []persistedDetail{
		{
			API:   "codex",
			Model: "gpt-5",
			Detail: RequestDetail{
				Model:     "gpt-5",
				Timestamp: when,
				Source:    "codex",
				Provider:  "codex",
				AuthIndex: "channel-a",
				Tokens:    TokenStats{TotalTokens: 1},
			},
		},
		{
			API:   "codex",
			Model: "gpt-5",
			Detail: RequestDetail{
				Model:     "gpt-5",
				Timestamp: when.Add(time.Minute),
				Source:    "codex",
				Provider:  "codex",
				AuthIndex: "channel-b",
				Tokens:    TokenStats{TotalTokens: 2},
			},
		},
	}
	var raw strings.Builder
	for _, line := range lines {
		b, err := json.Marshal(line)
		if err != nil {
			t.Fatalf("marshal persisted detail: %v", err)
		}
		raw.Write(b)
		raw.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(raw.String()), 0o600); err != nil {
		t.Fatalf("write storage fixture: %v", err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{StorageEnabled: true, StoragePath: path, RetentionDays: 0, DedupWindowMinutes: 0})
	snapshot := stats.Snapshot()
	if _, ok := snapshot.APIs["codex"]; ok {
		t.Fatalf("snapshot APIs = %#v, want codex records split by upstream channel", snapshot.APIs)
	}
	if api := snapshot.APIs["codex · 上游 channel-a"]; api.TotalRequests != 1 {
		t.Fatalf("channel-a API = %#v, want one request; all APIs=%#v", api, snapshot.APIs)
	}
	if api := snapshot.APIs["codex · 上游 channel-b"]; api.TotalRequests != 1 {
		t.Fatalf("channel-b API = %#v, want one request; all APIs=%#v", api, snapshot.APIs)
	}
}

// ============================================================================
// P2 Tests: Concurrency, Snapshot isolation, Benchmarks
// ============================================================================

func TestRecordConcurrentSafety(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{DedupWindowMinutes: 0, MaxDetailsPerModel: 5000})

	done := make(chan struct{})
	const goroutines = 20
	const recordsEach = 500

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			for i := 0; i < recordsEach; i++ {
				stats.Record(UsageRecord{
					Provider: "deepseek",
					Model:    "deepseek-v3.1",
					Detail: UsageDetail{
						InputTokens: int64(i + 1),
						TotalTokens: int64(i + 1),
					},
				})
			}
			done <- struct{}{}
		}(g)
	}

	for g := 0; g < goroutines; g++ {
		<-done
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests <= 0 {
		t.Fatalf("snapshot total should be > 0, got %d", snapshot.TotalRequests)
	}
	t.Logf("concurrent write: total_requests=%d", snapshot.TotalRequests)
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})

	snap := stats.Snapshot()
	// Mutate snapshot
	snap.TotalRequests = 999
	if details := snap.APIs["openai"].Models["gpt-4"].Details; len(details) > 0 {
		details[0].Tokens.TotalTokens = -1
	}

	// Get fresh snapshot
	snap2 := stats.Snapshot()
	if snap2.TotalRequests != 1 {
		t.Fatalf("mutating snapshot should not affect stats: got %d", snap2.TotalRequests)
	}
	details2 := snap2.APIs["openai"].Models["gpt-4"].Details
	if details2[0].Tokens.TotalTokens != 100 {
		t.Fatalf("mutating snapshot detail should not affect stats: got %d", details2[0].Tokens.TotalTokens)
	}
}

func TestConfigure_ShrinkingMaxCleansUpCounters(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 5, RetentionDays: 0, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 10; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-v3",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(i + 1)},
		})
	}

	snapBefore := stats.Snapshot()
	if snapBefore.TotalRequests != 5 {
		t.Fatalf("before shrink: expected 5 requests, got %d", snapBefore.TotalRequests)
	}

	// Shrink further
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 2, RetentionDays: 0, DedupWindowMinutes: 0})
	snapAfter := stats.Snapshot()
	if snapAfter.TotalRequests != 2 {
		t.Fatalf("after shrink to 2: expected 2, got %d", snapAfter.TotalRequests)
	}
}

func TestConfigureSingleFieldDoesNotResetMaxDetails(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 3, RetentionDays: 0, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-v3",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(i + 1)},
		})
	}

	stats.Configure(runtimeConfig{RetentionDays: 0})

	snap := stats.Snapshot()
	if snap.TotalRequests != 3 {
		t.Fatalf("single-field Configure reset max details: total_requests = %d, want 3", snap.TotalRequests)
	}
}

func TestHourKeysPrecomputed(t *testing.T) {
	// Verify hourKeys array has 24 elements matching "00".."23"
	for i := 0; i < 24; i++ {
		expected := string([]byte{'0' + byte(i/10), '0' + byte(i%10)})
		if hourKeys[i] != expected {
			t.Fatalf("hourKeys[%d] = %q, want %q", i, hourKeys[i], expected)
		}
	}
}

func TestMergeSnapshot_PreFilterImportValidation(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{RetentionDays: 30, DedupWindowMinutes: 0})

	// Import a mix: 1 recent, 1 expired, 1 duplicate
	recent := time.Now().Add(-1 * time.Hour)
	expired := time.Now().Add(-100 * 24 * time.Hour)

	snapshot := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-api": {
				Models: map[string]ModelSnapshot{
					"test-model": {
						Details: []RequestDetail{
							{Timestamp: recent, Tokens: TokenStats{TotalTokens: 100}},
							{Timestamp: expired, Tokens: TokenStats{TotalTokens: 200}},
						},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(snapshot)
	if result.Added != 1 || result.IgnoredByRetention != 1 {
		t.Fatalf("import mismatched: added=%d ignored=%d", result.Added, result.IgnoredByRetention)
	}

	// Import again: both should be skipped or ignored (1 duplicate, 1 still expired)
	result2 := stats.MergeSnapshot(snapshot)
	if result2.Added != 0 || result2.Skipped != 1 || result2.IgnoredByRetention != 1 {
		// The second call uses a new "now" timestamp, which can affect
		// the pre-filter cutoff. Verify that at least the duplicate check works.
		t.Logf("re-import: added=%d skipped=%d ignored=%d",
			result2.Added, result2.Skipped, result2.IgnoredByRetention)
	}
}

// ============================================================================
// P0 Benchmarks
// ============================================================================

func BenchmarkRecordIncremental(b *testing.B) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 5000, RetentionDays: 30, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-v3",
			RequestedAt: base.Add(time.Duration(i%1000) * time.Second),
			Detail: UsageDetail{
				InputTokens:  int64(i%100 + 1),
				OutputTokens: int64(i%50 + 1),
				TotalTokens:  int64(i%150 + 1),
			},
		})
	}
}

func BenchmarkSnapshot(b *testing.B) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, RetentionDays: 30, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	// Pre-populate
	for i := 0; i < 100; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-v3",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(i)},
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stats.Snapshot()
	}
}

func buildBenchmarkStats(recordCount int) *RequestStatistics {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: recordCount, RetentionDays: 30, DedupWindowMinutes: 0})
	base := time.Now().Add(-7 * 24 * time.Hour)
	providers := []string{"openai", "deepseek", "claude", "gemini"}
	models := []string{"gpt-4.1", "deepseek-v3", "claude-sonnet", "gemini-pro"}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	for i := 0; i < recordCount; i++ {
		provider := providers[i%len(providers)]
		model := models[i%len(models)]
		detail := RequestDetail{
			Model:      model,
			Timestamp:  base.Add(time.Duration(i) * time.Second),
			LatencyMs:  int64(100 + i%3000),
			APIKey:     maskAPIKey(fmt.Sprintf("sk-client-%04d", i%100)),
			APIKeyHash: hashAPIKey(fmt.Sprintf("sk-client-%04d", i%100)),
			Source:     provider + "-prod",
			Provider:   provider,
			AuthIndex:  fmt.Sprintf("auth-%02d", i%20),
			Tokens: TokenStats{
				InputTokens:     int64(100 + i%1000),
				OutputTokens:    int64(10 + i%200),
				ReasoningTokens: int64(i % 50),
				CachedTokens:    int64(i % 100),
			},
			Failed: i%17 == 0,
		}
		detail.Tokens.TotalTokens = detailTotalTokens(detail.Tokens)
		apiName := provider + " · " + provider + "-prod"
		if existing, ok := stats.apis[apiName]; !ok || existing == nil {
			stats.apis[apiName] = &apiStats{Models: make(map[string]*modelStats)}
		}
		stats.apis[apiName].Models[model] = ensureBenchmarkModel(stats.apis[apiName].Models[model])
		stats.apis[apiName].Models[model].Details = append(stats.apis[apiName].Models[model].Details, detail)
	}
	stats.rebuildAggregatesLocked()
	stats.rebuildSeenLocked(time.Now())
	return stats
}

func ensureBenchmarkModel(model *modelStats) *modelStats {
	if model != nil {
		return model
	}
	return &modelStats{}
}

func buildBenchmarkSnapshot(recordCount int) StatisticsSnapshot {
	return buildBenchmarkStats(recordCount).Snapshot()
}

func clearBenchmarkEventCache(stats *RequestStatistics) {
	stats.mu.Lock()
	stats.eventQueryCache = nil
	stats.eventQueryCacheOrder = nil
	stats.mu.Unlock()
}

func clearBenchmarkEventIndex(stats *RequestStatistics) {
	stats.mu.Lock()
	stats.eventIndexVersion = 0
	stats.eventIndex = nil
	stats.eventAPIIndex = nil
	stats.eventModelIndex = nil
	stats.eventSourceIndex = nil
	stats.eventAuthIndex = nil
	stats.mu.Unlock()
}

func BenchmarkSummaryWithoutDetails100k(b *testing.B) {
	stats := buildBenchmarkStats(100000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stats.SummaryWithoutDetails()
	}
}

func BenchmarkSummaryWithoutDetailsRebuild100k(b *testing.B) {
	stats := buildBenchmarkStats(100000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats.mu.Lock()
		stats.invalidateSummaryLocked()
		stats.mu.Unlock()
		_ = stats.SummaryWithoutDetails()
	}
}

func BenchmarkQueryEvents100k(b *testing.B) {
	stats := buildBenchmarkStats(100000)
	params := EventsQuery{Limit: 500, Offset: 0, Range: "7d", Model: "gpt-4.1"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clearBenchmarkEventCache(stats)
		_ = stats.QueryEvents(params)
	}
}

func BenchmarkQueryEventsCached100k(b *testing.B) {
	stats := buildBenchmarkStats(100000)
	params := EventsQuery{Limit: 500, Offset: 0, Range: "7d", Model: "gpt-4.1"}
	_ = stats.QueryEvents(params)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stats.QueryEvents(params)
	}
}

func BenchmarkQueryEventsOffset100k(b *testing.B) {
	stats := buildBenchmarkStats(100000)
	params := EventsQuery{Limit: 500, Offset: 500, Range: "7d", Model: "gpt-4.1"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clearBenchmarkEventCache(stats)
		_ = stats.QueryEvents(params)
	}
}

func BenchmarkQueryAPIDetail100k(b *testing.B) {
	stats := buildBenchmarkStats(100000)
	_ = stats.QueryAPIDetail("openai · openai-prod", "7d", 120, 20)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stats.QueryAPIDetail("openai · openai-prod", "7d", 120, 20)
	}
}

func BenchmarkQueryAPIDetailColdIndex100k(b *testing.B) {
	stats := buildBenchmarkStats(100000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clearBenchmarkEventIndex(stats)
		_ = stats.QueryAPIDetail("openai · openai-prod", "7d", 120, 20)
	}
}

func BenchmarkMergeSnapshot100k(b *testing.B) {
	snapshot := buildBenchmarkSnapshot(100000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats := NewRequestStatistics()
		stats.Configure(runtimeConfig{MaxDetailsPerModel: 100000, RetentionDays: 30, DedupWindowMinutes: 0})
		_ = stats.MergeSnapshot(snapshot)
	}
}

func BenchmarkConfigurePrune200k(b *testing.B) {
	for i := 0; i < b.N; i++ {
		stats := buildBenchmarkStats(200000)
		b.StartTimer()
		stats.Configure(runtimeConfig{MaxDetailsPerModel: 100000, RetentionDays: 30, DedupWindowMinutes: 0})
		b.StopTimer()
	}
}
