package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

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
	api, ok := snapshot.APIs["openai-compatible-opencode"]
	if !ok {
		t.Fatalf("snapshot APIs = %#v, want clean upstream key", snapshot.APIs)
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
}

func TestStripCredentialSuffix(t *testing.T) {
	tests := map[string]string{
		"openai-compatible-opencode · apikey · 5312415661d8a481": "openai-compatible-opencode",
		"openai-compatibility:opencode:a4e4860e4fc0":             "openai-compatibility:opencode",
		"deepseek": "deepseek",
	}
	for input, want := range tests {
		if got := stripCredentialSuffix(input); got != want {
			t.Fatalf("stripCredentialSuffix(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRecordDeduplicatesRepeatedUsageRecords(t *testing.T) {
	stats := NewRequestStatistics()
	when := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
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
	base := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
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
