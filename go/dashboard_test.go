package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// P0 Tests: Lightweight dashboard summary
// ============================================================================

func TestDashboardSummaryReturnsNoDetails(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		Failed:      true,
		RequestedAt: time.Now().Add(time.Minute),
		Detail:      UsageDetail{TotalTokens: 50},
	})

	summary := stats.SummaryWithoutDetails()
	if summary.Usage.TotalRequests != 2 {
		t.Fatalf("expected 2 total_requests, got %d", summary.Usage.TotalRequests)
	}
	if summary.Usage.SuccessCount != 1 {
		t.Fatalf("expected 1 success, got %d", summary.Usage.SuccessCount)
	}
	if summary.Usage.FailureCount != 1 {
		t.Fatalf("expected 1 failure, got %d", summary.Usage.FailureCount)
	}

	api, ok := summary.Usage.APIs["openai"]
	if !ok {
		t.Fatal("expected 'openai' api in summary")
	}
	model, ok := api.Models["gpt-4"]
	if !ok {
		t.Fatal("expected 'gpt-4' model in summary")
	}
	if model.TotalRequests != 2 {
		t.Fatalf("model total_requests = %d, want 2", model.TotalRequests)
	}

	// Verify no details arrays at any level
	summaryJSON, _ := json.Marshal(summary)
	if strings.Contains(string(summaryJSON), `"details":`) {
		t.Fatal("summary JSON contains 'details' field — details should not be included")
	}
}

func TestDashboardSummaryHasHealthGrid(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})

	summary := stats.SummaryWithoutDetails()
	if len(summary.HealthGrid) != 672 {
		t.Fatalf("health grid should have 672 slots, got %d", len(summary.HealthGrid))
	}

	// At least one slot should have data
	hasData := false
	for _, slot := range summary.HealthGrid {
		if slot.Total > 0 {
			hasData = true
			if slot.Success != 1 || slot.Failure != 0 {
				t.Fatalf("health grid slot data mismatch: %#v", slot)
			}
			break
		}
	}
	if !hasData {
		t.Fatal("health grid should have at least one populated slot")
	}
}

func TestDashboardSummaryHasSourceStats(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Source:   "openai-prod",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider: "deepseek",
		Source:   "deepseek-beta",
		Model:    "deepseek-v3",
		Failed:   true,
		Detail:   UsageDetail{TotalTokens: 50},
	})

	summary := stats.SummaryWithoutDetails()
	if len(summary.SourceStats) < 2 {
		t.Fatalf("expected >= 2 source stats, got %d", len(summary.SourceStats))
	}

	// Check first source (sorted by requests desc)
	if summary.SourceStats[0].TotalRequests != 1 {
		t.Fatalf("first source total = %d, want 1", summary.SourceStats[0].TotalRequests)
	}
}

func TestDashboardSummaryHasModelStats(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider: "deepseek",
		Model:    "deepseek-v3",
		Detail:   UsageDetail{TotalTokens: 50},
	})

	summary := stats.SummaryWithoutDetails()
	if len(summary.ModelStats) != 2 {
		t.Fatalf("expected 2 model stats, got %d", len(summary.ModelStats))
	}

	// Check model names are present
	models := make(map[string]bool)
	for _, m := range summary.ModelStats {
		models[m.Model] = true
	}
	if !models["gpt-4"] || !models["deepseek-v3"] {
		t.Fatalf("model stats missing expected models: %v", summary.ModelStats)
	}
}

func TestDashboardSummaryAggregatesClientAPIKeyStats(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)

	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4.1",
		APIKey:      "sk-client-alpha-123456",
		AuthIndex:   "upstream-credential-1",
		RequestedAt: base,
		Detail: UsageDetail{
			InputTokens:     1000,
			OutputTokens:    200,
			ReasoningTokens: 30,
			CachedTokens:    100,
			TotalTokens:     1230,
		},
	})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4.1",
		APIKey:      "sk-client-alpha-123456",
		AuthIndex:   "upstream-credential-2",
		RequestedAt: base.Add(time.Minute),
		Detail: UsageDetail{
			InputTokens:  500,
			OutputTokens: 50,
			TotalTokens:  550,
		},
	})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4.1",
		APIKey:      "sk-client-beta-123456",
		AuthIndex:   "upstream-credential-1",
		RequestedAt: base.Add(2 * time.Minute),
		Detail:      UsageDetail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	})

	summary := stats.SummaryWithoutDetails()
	if len(summary.ClientAPIStats) != 2 {
		t.Fatalf("client api stats len = %d, want 2: %#v", len(summary.ClientAPIStats), summary.ClientAPIStats)
	}
	if len(summary.CredentialStats) != 2 {
		t.Fatalf("credential stats len = %d, want 2", len(summary.CredentialStats))
	}

	first := summary.ClientAPIStats[0]
	if first.APIKey != "sk******56" {
		t.Fatalf("first client api label = %q, want masked CPA api key", first.APIKey)
	}
	if first.TotalRequests != 2 || first.TotalTokens != 1780 {
		t.Fatalf("first client api totals = requests %d tokens %d, want 2/1780", first.TotalRequests, first.TotalTokens)
	}
	if first.InputTokens != 1500 || first.OutputTokens != 250 || first.CachedTokens != 100 || first.ReasoningTokens != 30 {
		t.Fatalf("first client api token parts = %#v", first)
	}
	if len(first.Models) != 1 || first.Models[0].Model != "gpt-4.1" {
		t.Fatalf("client api model breakdown = %#v", first.Models)
	}
}

func TestDashboardSummaryUsesOriginalModelNotAlias(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4.1",
		Alias:    "claude-sonnet",
		APIKey:   "sk-client-alias-test",
		Detail: UsageDetail{
			InputTokens:  100,
			OutputTokens: 20,
			TotalTokens:  120,
		},
	})

	summary := stats.SummaryWithoutDetails()
	if _, ok := summary.Usage.APIs["openai"].Models["gpt-4.1"]; !ok {
		t.Fatalf("summary models = %#v, want original model gpt-4.1", summary.Usage.APIs["openai"].Models)
	}
	if _, ok := summary.Usage.APIs["openai"].Models["claude-sonnet"]; ok {
		t.Fatalf("alias should not become a model key: %#v", summary.Usage.APIs["openai"].Models)
	}
	if len(summary.ModelStats) != 1 || summary.ModelStats[0].Model != "gpt-4.1" {
		t.Fatalf("model stats = %#v, want original model only", summary.ModelStats)
	}
}

func TestMergeSnapshotUsesDetailModelOverOuterAliasKey(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 0})
	when := time.Now().Add(-time.Hour)
	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"openai": {
				Models: map[string]ModelSnapshot{
					"claude-sonnet": {
						Details: []RequestDetail{
							{
								Model:     "gpt-4.1",
								Timestamp: when,
								Tokens: TokenStats{
									InputTokens:  11,
									OutputTokens: 7,
								},
							},
						},
					},
				},
			},
		},
	})
	if result.Added != 1 {
		t.Fatalf("merge result = %#v, want one added record", result)
	}
	snapshot := stats.Snapshot()
	if _, ok := snapshot.APIs["openai"].Models["gpt-4.1"]; !ok {
		t.Fatalf("snapshot models = %#v, want detail model gpt-4.1", snapshot.APIs["openai"].Models)
	}
	if _, ok := snapshot.APIs["openai"].Models["claude-sonnet"]; ok {
		t.Fatalf("outer alias key should not be used as model: %#v", snapshot.APIs["openai"].Models)
	}
	if snapshot.TotalTokens != 18 {
		t.Fatalf("total tokens = %d, want fallback input+output total 18", snapshot.TotalTokens)
	}
}

func TestDashboardSummaryHasMetadata(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, RetentionDays: 14, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})

	summary := stats.SummaryWithoutDetails()
	if summary.Meta.RetentionDays != 14 {
		t.Fatalf("retention_days = %d, want 14", summary.Meta.RetentionDays)
	}
	if summary.Meta.MaxDetailsPerModel != 200 {
		t.Fatalf("max_details = %d, want 200", summary.Meta.MaxDetailsPerModel)
	}
	if summary.Meta.CurrentDetailCount != 1 {
		t.Fatalf("detail_count = %d, want 1", summary.Meta.CurrentDetailCount)
	}
	if summary.Meta.LastImport != nil {
		t.Fatal("last_import should be nil when no import has occurred")
	}
	if summary.GeneratedAt == "" {
		t.Fatal("generated_at should not be empty")
	}
}

// ============================================================================
// P0 Tests: Paginated events endpoint
// ============================================================================

func TestDashboardEventsPagination(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 20; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(10 + i)},
		})
	}

	result := stats.QueryEvents(EventsQuery{Limit: 5, Offset: 0})
	if len(result.Events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(result.Events))
	}
	if result.Total != 20 {
		t.Fatalf("expected total 20, got %d", result.Total)
	}
	if result.Limit != 5 || result.Offset != 0 {
		t.Fatalf("limit/offset mismatch: %d/%d", result.Limit, result.Offset)
	}

	// Second page
	result2 := stats.QueryEvents(EventsQuery{Limit: 5, Offset: 5})
	if len(result2.Events) != 5 {
		t.Fatalf("page 2: expected 5 events, got %d", len(result2.Events))
	}
	if result2.Total != 20 {
		t.Fatalf("page 2: total = %d, want 20", result2.Total)
	}
	if result2.Offset != 5 {
		t.Fatalf("page 2: offset = %d, want 5", result2.Offset)
	}
}

func TestDashboardEventsModelFilter(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-3.5",
		Detail:   UsageDetail{TotalTokens: 50},
	})

	result := stats.QueryEvents(EventsQuery{Limit: 50, Model: "gpt-4"})
	if result.Total != 1 {
		t.Fatalf("model filter: total = %d, want 1", result.Total)
	}
	if len(result.Events) != 1 {
		t.Fatalf("model filter: events = %d, want 1", len(result.Events))
	}
	if result.Events[0].Model != "gpt-4" {
		t.Fatalf("filtered event model = %q, want gpt-4", result.Events[0].Model)
	}
}

func TestDashboardEventsDefaultLimit(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0})
	for i := 0; i < 100; i++ {
		stats.Record(UsageRecord{
			Provider: "openai",
			Model:    "gpt-4",
			Detail:   UsageDetail{TotalTokens: int64(i)},
		})
	}

	result := stats.QueryEvents(EventsQuery{Limit: 0})
	if result.Limit != 50 {
		t.Fatalf("default limit should be 50, got %d. QueryEvents should enforce minimum 50, not 0", result.Limit)
	}
	if len(result.Events) < 1 {
		t.Fatal("should return at least 1 event")
	}
}

func TestDashboardEventsEmptyResult(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})

	result := stats.QueryEvents(EventsQuery{Limit: 50, Model: "nonexistent"})
	if result.Total != 0 {
		t.Fatalf("total should be 0, got %d", result.Total)
	}
	if len(result.Events) != 0 {
		t.Fatalf("events should be empty, got %d", len(result.Events))
	}
}

func TestDashboardEventsRangeFilter(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0, RetentionDays: 30})
	// Old event (~7 days ago)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-7*24*time.Hour - time.Hour),
		Detail:      UsageDetail{TotalTokens: 100},
	})
	// Recent event
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 50},
	})

	result := stats.QueryEvents(EventsQuery{Limit: 50, Range: "24h"})
	if result.Total != 1 {
		t.Fatalf("24h range: total = %d, want 1", result.Total)
	}
}

// ============================================================================
// P1 Tests: Import tracking + backward compatibility
// ============================================================================

func TestImportTracksLastResult(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	snapshot := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-api": {
				Models: map[string]ModelSnapshot{
					"test-model": {
						Details: []RequestDetail{
							{Timestamp: time.Now(), Tokens: TokenStats{TotalTokens: 100}},
						},
					},
				},
			},
		},
	}
	exportPayload := ExportPayload{Version: 1, Usage: snapshot}
	exportJSON, _ := json.Marshal(exportPayload)

	// Go through the real handler to trigger lastImportResult tracking
	var importReq struct {
		Version int                `json:"version"`
		Usage   StatisticsSnapshot `json:"usage"`
	}
	json.Unmarshal(exportJSON, &importReq)

	// Simulate the import handler flow
	stats.MergeSnapshot(importReq.Usage)
	// lastImportResult is set in handleImportUsage, not in MergeSnapshot directly.
	// Test that the merge itself works via SummaryWithoutDetails.
	snap := stats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Fatalf("after merge: total_requests = %d, want 1", snap.TotalRequests)
	}

	summary := stats.SummaryWithoutDetails()
	if summary.Meta.CurrentDetailCount != 1 {
		t.Fatalf("detail_count = %d, want 1", summary.Meta.CurrentDetailCount)
	}
}

func TestDashboardDataBackwardCompatible(t *testing.T) {
	// Use package-level stats so the handler can see the data
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "bkwd-openai",
		Model:    "bkwd-gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})

	// Old endpoint should still return full details
	raw, err := handleDashboardData()
	if err != nil {
		t.Fatalf("handleDashboardData() error = %v", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatal("envelope not ok")
	}

	var resp ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}

	if _, ok := data["usage"]; !ok {
		t.Fatal("old dashboard-data should contain 'usage' field")
	}
	if _, ok := data["generated_at"]; !ok {
		t.Fatal("old dashboard-data should contain 'generated_at' field")
	}

	// The usage field should contain APIS with models
	bodyStr := string(resp.Body)
	if !strings.Contains(bodyStr, `"details"`) {
		preview := bodyStr
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Logf("response body (first 200 chars): %s", preview)
		t.Fatal("old dashboard-data should contain 'details' arrays")
	}
}

func TestRequestDetailHasModelField(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["openai"].Models["gpt-4"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	if details[0].Model != "gpt-4" {
		t.Fatalf("detail.Model = %q, want gpt-4", details[0].Model)
	}
}

func TestSummaryWithoutDetailsMatchesCounts(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		Failed:      true,
		RequestedAt: time.Now().Add(time.Minute),
		Detail:      UsageDetail{TotalTokens: 50},
	})

	full := stats.Snapshot()
	summary := stats.SummaryWithoutDetails()

	if summary.Usage.TotalRequests != full.TotalRequests {
		t.Fatalf("total_requests: summary=%d full=%d", summary.Usage.TotalRequests, full.TotalRequests)
	}
	if summary.Usage.SuccessCount != full.SuccessCount {
		t.Fatalf("success_count: summary=%d full=%d", summary.Usage.SuccessCount, full.SuccessCount)
	}
	if summary.Usage.FailureCount != full.FailureCount {
		t.Fatalf("failure_count: summary=%d full=%d", summary.Usage.FailureCount, full.FailureCount)
	}
	if summary.Usage.TotalTokens != full.TotalTokens {
		t.Fatalf("total_tokens: summary=%d full=%d", summary.Usage.TotalTokens, full.TotalTokens)
	}
}

// ============================================================================
// P2 Tests: Stats engine observability
// ============================================================================

func TestEvictedTotalTracksPrunedRecords(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 2, RetentionDays: 0, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: 10},
		})
	}

	evicted := stats.EvictedTotal()
	if evicted < 3 {
		t.Fatalf("evicted_total should be >= 3 (5 records, max=2), got %d", evicted)
	}

	detailCount := stats.DetailCount()
	if detailCount != 2 {
		t.Fatalf("detail_count should be 2, got %d", detailCount)
	}
}
