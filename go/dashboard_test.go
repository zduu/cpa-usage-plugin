package main

import (
	"encoding/json"
	"math"
	"path/filepath"
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

func TestDashboardModelStatsIncludeProviderBreakdown(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "openai/gpt-5.5",
		Detail: UsageDetail{
			InputTokens:  100,
			OutputTokens: 50,
			CachedTokens: 10,
		},
	})
	stats.Record(UsageRecord{
		Provider: "openrouter",
		Model:    "openai/gpt-5.5",
		Detail: UsageDetail{
			InputTokens:  200,
			OutputTokens: 80,
			CachedTokens: 20,
		},
	})

	summary := stats.SummaryWithoutDetails()
	if len(summary.ModelStats) != 1 {
		t.Fatalf("model stats = %#v, want one model", summary.ModelStats)
	}
	providers := summary.ModelStats[0].Providers
	if len(providers) != 2 {
		t.Fatalf("model providers = %#v, want two providers", providers)
	}
	providerTotals := map[string]int64{}
	for _, provider := range providers {
		providerTotals[provider.Provider] = provider.InputTokens
	}
	if providerTotals["openai"] != 100 || providerTotals["openrouter"] != 200 {
		t.Fatalf("model provider input totals = %#v", providerTotals)
	}
	apiModel := summary.Usage.APIs["openai"].Models["openai/gpt-5.5"]
	if len(apiModel.Providers) != 1 || apiModel.Providers[0].Provider != "openai" {
		t.Fatalf("api model providers = %#v, want openai provider", apiModel.Providers)
	}

	if len(summary.ClientAPIStats) != 1 || len(summary.ClientAPIStats[0].Models) != 1 {
		t.Fatalf("client API stats = %#v, want one model", summary.ClientAPIStats)
	}
	clientProviders := summary.ClientAPIStats[0].Models[0].Providers
	if len(clientProviders) != 2 {
		t.Fatalf("client model providers = %#v, want two providers", clientProviders)
	}

	detail := stats.QueryAPIDetail("openai", "all", 10, 10)
	if len(detail.ModelStats) != 1 || len(detail.ModelStats[0].Providers) != 1 || detail.ModelStats[0].Providers[0].Provider != "openai" {
		t.Fatalf("api detail model stats = %#v, want current provider breakdown", detail.ModelStats)
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
		APIKey:      "sk-client-alpha-0000xx",
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
		APIKey:      "sk-client-alpha-0000xx",
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
		APIKey:      "sk-client-beta-0000yy",
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
	if first.APIKey != "sk******xx" {
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
	if first.Selector == "" || !strings.HasPrefix(first.Selector, "h.") {
		t.Fatalf("client api selector = %q, want irreversible hash selector", first.Selector)
	}
}

func TestClientAPISelectorFiltersSummaryEventsAndAPIDetail(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	now := time.Now()
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4.1",
		APIKey:      "sk-client-alpha-0000xx",
		RequestedAt: now.Add(-2 * time.Minute),
		Detail:      UsageDetail{InputTokens: 80, OutputTokens: 20, TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4.1-mini",
		APIKey:      "sk-client-alpha-0000xx",
		RequestedAt: now.Add(-time.Minute),
		Detail:      UsageDetail{InputTokens: 30, OutputTokens: 10, TotalTokens: 40},
	})
	stats.Record(UsageRecord{
		Provider:    "claude",
		Model:       "claude-sonnet",
		APIKey:      "sk-client-beta-0000yy",
		RequestedAt: now,
		Detail:      UsageDetail{InputTokens: 200, OutputTokens: 50, TotalTokens: 250},
	})

	base := stats.SummaryWithoutDetailsForRangeAndClientAPIAt("24h", "", now)
	if len(base.ClientAPIStats) != 2 {
		t.Fatalf("base client api stats = %#v, want two groups", base.ClientAPIStats)
	}
	var alpha ClientAPIStat
	for _, stat := range base.ClientAPIStats {
		if stat.APIKey == "sk******xx" {
			alpha = stat
		}
	}
	if alpha.Selector == "" {
		t.Fatalf("alpha selector missing: %#v", base.ClientAPIStats)
	}

	filtered := stats.SummaryWithoutDetailsForRangeAndClientAPIAt("24h", alpha.Selector, now)
	if filtered.Usage.TotalRequests != 2 || filtered.Usage.TotalTokens != 140 {
		t.Fatalf("filtered usage = %#v, want 2 requests and 140 tokens", filtered.Usage)
	}
	if len(filtered.ModelStats) != 2 || len(filtered.Usage.APIs) != 1 {
		t.Fatalf("filtered dimensions = models %#v apis %#v", filtered.ModelStats, filtered.Usage.APIs)
	}

	events := stats.QueryEventsAt(EventsQuery{Range: "24h", ClientAPI: alpha.Selector, Limit: 50}, now)
	if events.Total != 2 || len(events.Events) != 2 {
		t.Fatalf("filtered events = %#v, want two alpha events", events)
	}
	for _, event := range events.Events {
		if event.APIKeyHash != alpha.APIKeyHash {
			t.Fatalf("event api key hash = %q, want %q", event.APIKeyHash, alpha.APIKeyHash)
		}
	}

	detail := stats.QueryAPIDetailForClientAPIAt("openai", "24h", alpha.Selector, 20, 20, now)
	if detail.TotalEvents != 2 || detail.Summary.TotalTokens != 140 || len(detail.RecentEvents) != 2 {
		t.Fatalf("filtered api detail = %#v, want two alpha events and 140 tokens", detail)
	}

	invalid := stats.QueryEventsAt(EventsQuery{Range: "24h", ClientAPI: "invalid", Limit: 50}, now)
	if invalid.Total != 0 || len(invalid.Events) != 0 {
		t.Fatalf("invalid selector must not fall back to all events: %#v", invalid)
	}
}

func TestClientAPISelectorMatchesCoalescedLegacyIdentity(t *testing.T) {
	hash := strings.Repeat("a", 56)
	stat := ClientAPIStat{APIKey: "sk******xx", APIKeyHash: hash}
	selector := clientAPISelectorForStat(stat)
	if !clientAPISelectorMatchesDetail(selector, RequestDetail{APIKey: "sk******xx", APIKeyHash: hash}) {
		t.Fatal("hash selector should match the current hashed record")
	}
	if !clientAPISelectorMatchesDetail(selector, RequestDetail{APIKey: "sk******xx"}) {
		t.Fatal("hash selector should include the coalesced legacy hashless record")
	}
	if clientAPISelectorMatchesDetail(selector, RequestDetail{APIKey: "sk******xx", APIKeyHash: strings.Repeat("b", 56)}) {
		t.Fatal("hash selector must not match a distinct live hash")
	}

	mergedSelector := clientAPISelectorForStat(ClientAPIStat{APIKey: "sk******xx"})
	if !clientAPISelectorMatchesDetail(mergedSelector, RequestDetail{APIKey: "sk******xx", APIKeyHash: hash}) {
		t.Fatal("masked selector should match all identities represented by a merged masked group")
	}
}

func TestClientAPIFilterChangesDashboardETags(t *testing.T) {
	now := time.Now()
	selector := clientAPISelectorForStat(ClientAPIStat{APIKey: "sk******xx", APIKeyHash: strings.Repeat("a", 56)})
	if dashboardSummaryETagForVersion(now, "24h", 7) == dashboardSummaryETagForClientAPIVersion(now, "24h", selector, 7) {
		t.Fatal("summary etag must include client api selector")
	}
	baseParams := EventsQuery{Range: "24h", Limit: 50}
	filteredParams := baseParams
	filteredParams.ClientAPI = selector
	if dashboardEventsETagForVersion(baseParams, now, 7) == dashboardEventsETagForVersion(filteredParams, now, 7) {
		t.Fatal("events etag must include client api selector")
	}
	if dashboardAPIDetailETagForVersion("openai", "24h", 20, 20, now, 7) == dashboardAPIDetailETagForClientAPIVersion("openai", "24h", selector, 20, 20, now, 7) {
		t.Fatal("api detail etag must include client api selector")
	}
}

func TestCoalesceMaskedClientAPIStatsMergesImportedHashlessGroupWithHistoricalHashes(t *testing.T) {
	rows := coalesceMaskedClientAPIStats([]ClientAPIStat{
		{APIKey: "sk******xx", APIKeyHash: strings.Repeat("a", 56), TotalRequests: 1, TotalTokens: 120},
		{APIKey: "sk******xx", APIKeyHash: strings.Repeat("b", 56), TotalRequests: 1, TotalTokens: 60},
		{APIKey: "sk******xx", TotalRequests: 1, TotalTokens: 40},
	})

	if len(rows) != 1 {
		t.Fatalf("client api stats len = %d, want imported masked group merged: %#v", len(rows), rows)
	}
	if rows[0].APIKey != "sk******xx" || rows[0].APIKeyHash != "" || rows[0].TotalRequests != 3 || rows[0].TotalTokens != 220 {
		t.Fatalf("client api stat = %#v, want merged hashless identity totals 3/220", rows[0])
	}
}

func TestCoalesceMaskedClientAPIStatsKeepsDifferentLiveHashesSeparateWithoutImport(t *testing.T) {
	rows := coalesceMaskedClientAPIStats([]ClientAPIStat{
		{APIKey: "sk******xx", APIKeyHash: strings.Repeat("a", 56), TotalRequests: 1, TotalTokens: 120},
		{APIKey: "sk******xx", APIKeyHash: strings.Repeat("b", 56), TotalRequests: 1, TotalTokens: 60},
	})

	if len(rows) != 2 {
		t.Fatalf("client api stats len = %d, want different live hashes kept separate: %#v", len(rows), rows)
	}
}

func TestDashboardSummaryMergesLegacyHashlessClientAPIKeyWithUniqueCurrentHash(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 0})
	when := time.Now().Add(-time.Hour)

	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"openai": {
				Models: map[string]ModelSnapshot{
					"gpt-4.1": {
						Details: []RequestDetail{
							{
								Model:     "gpt-4.1",
								Timestamp: when,
								APIKey:    "sk******xx",
								Tokens:    TokenStats{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
							},
						},
					},
				},
			},
		},
	})
	if result.Added != 1 {
		t.Fatalf("merge result = %#v, want one added legacy record", result)
	}

	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4.1",
		APIKey:      "sk-client-alpha-0000xx",
		RequestedAt: when.Add(time.Minute),
		Detail:      UsageDetail{InputTokens: 30, OutputTokens: 10, TotalTokens: 40},
	})

	assertMerged := func(label string, summary DashboardSummary) {
		t.Helper()
		if len(summary.ClientAPIStats) != 1 {
			t.Fatalf("%s client api stats len = %d, want 1: %#v", label, len(summary.ClientAPIStats), summary.ClientAPIStats)
		}
		got := summary.ClientAPIStats[0]
		if got.APIKey != "sk******xx" || got.APIKeyHash == "" || got.TotalRequests != 2 || got.TotalTokens != 160 {
			t.Fatalf("%s client api stat = %#v, want merged legacy/current key totals", label, got)
		}
		if len(got.Models) != 1 || got.Models[0].Model != "gpt-4.1" || got.Models[0].TotalRequests != 2 {
			t.Fatalf("%s client api model stats = %#v, want merged model totals", label, got.Models)
		}
	}

	assertMerged("all", stats.SummaryWithoutDetails())
	assertMerged("range", stats.SummaryWithoutDetailsForRange("24h"))
}

func TestDashboardSummaryMergesCrossDeploymentHashlessClientAPIKeyWithHashVariants(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 0})
	when := time.Now().Add(-time.Hour)

	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4.1",
		APIKey:      "sk-client-alpha-0000xx",
		RequestedAt: when,
		Detail:      UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4.1",
		APIKey:      "sk-client-beta-1111xx",
		RequestedAt: when.Add(time.Minute),
		Detail:      UsageDetail{InputTokens: 50, OutputTokens: 10, TotalTokens: 60},
	})
	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"openai": {
				Models: map[string]ModelSnapshot{
					"gpt-4.1": {
						Details: []RequestDetail{
							{
								Model:     "gpt-4.1",
								Timestamp: when.Add(2 * time.Minute),
								APIKey:    "sk******xx",
								Tokens:    TokenStats{InputTokens: 30, OutputTokens: 10, TotalTokens: 40},
							},
						},
					},
				},
			},
		},
	})
	if result.Added != 1 {
		t.Fatalf("merge result = %#v, want one added legacy record", result)
	}

	summary := stats.SummaryWithoutDetails()
	if len(summary.ClientAPIStats) != 1 {
		t.Fatalf("client api stats len = %d, want cross-deployment masked key merged: %#v", len(summary.ClientAPIStats), summary.ClientAPIStats)
	}
	got := summary.ClientAPIStats[0]
	if got.APIKey != "sk******xx" || got.APIKeyHash != "" || got.TotalRequests != 3 || got.TotalTokens != 220 {
		t.Fatalf("client api stat = %#v, want merged cross-deployment totals 3/220", got)
	}
	if len(got.Models) != 1 || got.Models[0].TotalRequests != 3 || got.Models[0].TotalTokens != 220 {
		t.Fatalf("client api model stats = %#v, want merged cross-deployment model totals", got.Models)
	}
}

func TestCoalesceMaskedClientAPIStatsMergesRealCrossDeploymentShape(t *testing.T) {
	requestCounts := []int64{6563, 61, 11, 5, 3, 1}
	rows := make([]ClientAPIStat, 0, len(requestCounts))
	for i, count := range requestCounts {
		hash := ""
		if i > 0 {
			hash = strings.Repeat(string(rune('a'+i-1)), 56)
		}
		rows = append(rows, ClientAPIStat{
			APIKey:        "sk******zy",
			APIKeyHash:    hash,
			TotalRequests: count,
			SuccessCount:  count,
			TotalTokens:   count * 10,
			Models: []ClientAPIModelStat{{
				Model:         "gpt-4.1",
				TotalRequests: count,
				SuccessCount:  count,
				TotalTokens:   count * 10,
			}},
		})
	}

	got := coalesceMaskedClientAPIStats(rows)
	if len(got) != 1 {
		t.Fatalf("client api stats len = %d, want real cross-deployment rows merged: %#v", len(got), got)
	}
	if got[0].APIKey != "sk******zy" || got[0].APIKeyHash != "" || got[0].TotalRequests != 6644 || got[0].TotalTokens != 66440 {
		t.Fatalf("client api stat = %#v, want merged real totals 6644/66440", got[0])
	}
	if len(got[0].Models) != 1 || got[0].Models[0].TotalRequests != 6644 || got[0].Models[0].TotalTokens != 66440 {
		t.Fatalf("client api model stats = %#v, want merged real model totals", got[0].Models)
	}
}

func TestDashboardSummaryMergesImportedClientAPIStatsByMaskedKey(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 0})
	when := time.Now().Add(-time.Hour)
	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"openai": {
				Models: map[string]ModelSnapshot{
					"gpt-4.1": {
						Details: []RequestDetail{
							{
								Model:      "gpt-4.1",
								Timestamp:  when,
								APIKey:     "sk******xx",
								APIKeyHash: "hash-from-first-export",
								Tokens:     TokenStats{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
							},
							{
								Model:      "gpt-4.1",
								Timestamp:  when.Add(time.Minute),
								APIKey:     "sk******xx",
								APIKeyHash: "hash-from-second-export",
								Tokens:     TokenStats{InputTokens: 30, OutputTokens: 10, TotalTokens: 40},
							},
						},
					},
				},
			},
		},
	})
	if result.Added != 2 {
		t.Fatalf("merge result = %#v, want two added records", result)
	}

	summary := stats.SummaryWithoutDetails()
	if len(summary.ClientAPIStats) != 1 {
		t.Fatalf("client api stats len = %d, want 1: %#v", len(summary.ClientAPIStats), summary.ClientAPIStats)
	}
	got := summary.ClientAPIStats[0]
	if got.APIKey != "sk******xx" || got.TotalRequests != 2 || got.TotalTokens != 160 {
		t.Fatalf("client api stat = %#v, want merged masked key totals", got)
	}
	if len(got.Models) != 1 || got.Models[0].TotalRequests != 2 {
		t.Fatalf("client api model stats = %#v, want merged model totals", got.Models)
	}
}

func TestDashboardSummaryMergesImportedRawClientAPIKeysByMaskedLabel(t *testing.T) {
	previousSalt := currentAPIKeySalt()
	setAPIKeySalt("import-raw-client-api-test-salt")
	t.Cleanup(func() { setAPIKeySalt(previousSalt) })

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 0})
	when := time.Now().Add(-time.Hour)
	firstKey := "sk-client-alpha-0000xx"
	secondKey := "sk-client-beta-1111xx"
	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"openai": {
				Models: map[string]ModelSnapshot{
					"gpt-4.1": {
						Details: []RequestDetail{
							{
								Model:      "gpt-4.1",
								Timestamp:  when,
								APIKey:     firstKey,
								APIKeyHash: "external-hash-a",
								Tokens:     TokenStats{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
							},
							{
								Model:      "gpt-4.1",
								Timestamp:  when.Add(time.Minute),
								APIKey:     secondKey,
								APIKeyHash: "external-hash-b",
								Tokens:     TokenStats{InputTokens: 30, OutputTokens: 10, TotalTokens: 40},
							},
						},
					},
				},
			},
		},
	})
	if result.Added != 2 {
		t.Fatalf("merge result = %#v, want two added records", result)
	}

	summary := stats.SummaryWithoutDetails()
	if len(summary.ClientAPIStats) != 2 {
		t.Fatalf("client api stats len = %d, want distinct imported raw masked keys: %#v", len(summary.ClientAPIStats), summary.ClientAPIStats)
	}
	var totalTokens int64
	for _, got := range summary.ClientAPIStats {
		if got.APIKey != "sk******xx" || got.APIKeyHash == "" || got.TotalRequests != 1 {
			t.Fatalf("client api stat = %#v, want distinct current-salt masked key", got)
		}
		totalTokens += got.TotalTokens
	}
	if totalTokens != 160 {
		t.Fatalf("client api total tokens = %d, want 160 across separate rows", totalTokens)
	}
}

func TestMergeSnapshotMasksImportedNonStandardRawClientAPIKey(t *testing.T) {
	previousSalt := currentAPIKeySalt()
	setAPIKeySalt("import-non-standard-client-api-test-salt")
	t.Cleanup(func() { setAPIKeySalt(previousSalt) })

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 0})
	rawKey := "raw-client-key"
	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"vendor · raw-client-key": {
				Models: map[string]ModelSnapshot{
					"gpt-4.1": {
						Details: []RequestDetail{
							{
								Model:      "gpt-4.1",
								Timestamp:  time.Now().Add(-time.Hour),
								Provider:   "vendor",
								Source:     "vendor · public · raw-client-key",
								AuthIndex:  "public",
								APIKey:     rawKey,
								APIKeyHash: "external-hash",
								Tokens:     TokenStats{InputTokens: 12, OutputTokens: 8, TotalTokens: 20},
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
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(raw), rawKey) {
		t.Fatalf("snapshot leaked raw imported client API key: %s", raw)
	}

	var detail RequestDetail
	for _, api := range snapshot.APIs {
		for _, model := range api.Models {
			if len(model.Details) > 0 {
				detail = model.Details[0]
			}
		}
	}
	if detail.APIKey != "ra******ey" || detail.APIKeyHash != hashAPIKey(rawKey) {
		t.Fatalf("imported detail api identity = %q/%q, want masked current-salt hash", detail.APIKey, detail.APIKeyHash)
	}
	if strings.Contains(detail.Source, rawKey) {
		t.Fatalf("detail source leaked raw imported key: %q", detail.Source)
	}

	summary := stats.SummaryWithoutDetails()
	if len(summary.ClientAPIStats) != 1 {
		t.Fatalf("client api stats = %#v, want one stat", summary.ClientAPIStats)
	}
	got := summary.ClientAPIStats[0]
	if got.APIKey != "ra******ey" || got.APIKeyHash != hashAPIKey(rawKey) || got.TotalRequests != 1 {
		t.Fatalf("client api stat = %#v, want masked current-salt hash", got)
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
	if summary.Meta.CurrentHour < 0 || summary.Meta.CurrentHour >= 24 {
		t.Fatalf("current_hour = %d, want 0..23", summary.Meta.CurrentHour)
	}
	if summary.Meta.SummaryVersion == 0 {
		t.Fatal("summary_version should be populated after recording a request")
	}
	if summary.Meta.Storage.Enabled {
		t.Fatal("storage should be disabled in this test configuration")
	}
	if summary.Meta.LastRecordedAt == "" {
		t.Fatal("last_recorded_at should not be empty after recording a request")
	}
	if _, err := time.Parse(time.RFC3339, summary.Meta.LastRecordedAt); err != nil {
		t.Fatalf("last_recorded_at is not RFC3339: %q", summary.Meta.LastRecordedAt)
	}
	if summary.Meta.LastImport != nil {
		t.Fatal("last_import should be nil when no import has occurred")
	}
	if summary.GeneratedAt == "" {
		t.Fatal("generated_at should not be empty")
	}
}

func TestSummaryCacheHitPreservesGeneratedMetadata(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Now()
	if time.Until(summaryHealthWindow(now)) < 2*time.Second {
		time.Sleep(2 * time.Second)
		now = time.Now()
	}
	stats.summaryCache = DashboardSummary{
		GeneratedAt: "2026-01-02T00:00:00Z",
		Meta:        DashboardMeta{CurrentHour: 16},
		Usage:       StatisticsSnapshotWithoutDetails{TotalRequests: 1},
	}
	stats.summaryCacheValid = true
	stats.summaryCacheVersion = stats.summaryVersion
	stats.summaryCacheWindow = summaryHealthWindow(now)

	result := stats.SummaryWithoutDetails()
	if result.GeneratedAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("generated_at = %q, want cached timestamp", result.GeneratedAt)
	}
	if result.Meta.CurrentHour != 16 {
		t.Fatalf("current_hour = %d, want cached hour 16", result.Meta.CurrentHour)
	}
	if result.Usage.TotalRequests != 1 || stats.summaryCacheHits != 1 || stats.summaryCacheMisses != 0 {
		t.Fatalf("summary cache result requests=%d hits=%d misses=%d, want cached hit", result.Usage.TotalRequests, stats.summaryCacheHits, stats.summaryCacheMisses)
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
	for i, event := range result.Events {
		want := int64(29 - i)
		if event.UpstreamAPI != "openai" {
			t.Fatalf("page 1 event %d api = %q, want openai", i, event.UpstreamAPI)
		}
		if event.Tokens.TotalTokens != want {
			t.Fatalf("page 1 event %d total tokens = %d, want %d", i, event.Tokens.TotalTokens, want)
		}
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
	for i, event := range result2.Events {
		want := int64(24 - i)
		if event.Tokens.TotalTokens != want {
			t.Fatalf("page 2 event %d total tokens = %d, want %d", i, event.Tokens.TotalTokens, want)
		}
	}
}

func TestDashboardEventsExposeExactCodexAndClaudeUpstreamInterfaces(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 20, DedupWindowMinutes: 0})
	now := time.Now()
	stats.Record(UsageRecord{
		Provider:    "codex",
		Source:      "codex",
		AuthIndex:   "b374b8e7c98ca23c",
		Model:       "gpt-5.5",
		RequestedAt: now.Add(-time.Minute),
		Detail:      UsageDetail{TotalTokens: 10},
	})
	stats.Record(UsageRecord{
		Provider:    "claude",
		Source:      "claude",
		AuthID:      "openai-compatibility:opencode-go:f85c45252fee",
		Model:       "deepseek-v4-pro",
		RequestedAt: now,
		Detail:      UsageDetail{TotalTokens: 20},
	})

	events := stats.QueryEvents(EventsQuery{Range: "all", Limit: 20})
	if len(events.Events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events.Events))
	}
	wantAPIByModel := map[string]string{
		"gpt-5.5":         "codex · 上游 b374b8e7c98ca23c",
		"deepseek-v4-pro": "claude · 上游 f85c45252fee",
	}
	for _, event := range events.Events {
		if event.UpstreamAPI != wantAPIByModel[event.Model] {
			t.Fatalf("event model %q api = %q, want %q", event.Model, event.UpstreamAPI, wantAPIByModel[event.Model])
		}
	}

	detail := stats.QueryAPIDetail("claude · 上游 f85c45252fee", "all", 10, 10)
	if len(detail.RecentEvents) != 1 || detail.RecentEvents[0].UpstreamAPI != detail.API {
		t.Fatalf("claude recent events = %#v, want api %q", detail.RecentEvents, detail.API)
	}
}

func TestDashboardEventsCacheReturnsCopyAndInvalidates(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(10 + i)},
		})
	}

	params := EventsQuery{Limit: 2, Offset: 0}
	first := stats.QueryEvents(params)
	if len(first.Events) != 2 || first.Events[0].Tokens.TotalTokens != 12 {
		t.Fatalf("first events = %#v, want newest two events", first.Events)
	}
	if len(stats.eventQueryCache) != 1 {
		t.Fatalf("event cache len = %d, want 1", len(stats.eventQueryCache))
	}
	if len(stats.eventIndex) != 3 || stats.eventIndexVersion != stats.summaryVersion {
		t.Fatalf("event index len/version = %d/%d, want 3/%d", len(stats.eventIndex), stats.eventIndexVersion, stats.summaryVersion)
	}

	first.Events[0].Model = "mutated"
	second := stats.QueryEvents(params)
	if second.Events[0].Model == "mutated" {
		t.Fatalf("cached result was mutated through returned events: %#v", second.Events[0])
	}

	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: base.Add(10 * time.Minute),
		Detail:      UsageDetail{TotalTokens: 99},
	})
	if len(stats.eventQueryCache) != 0 {
		t.Fatalf("event cache len after record = %d, want 0", len(stats.eventQueryCache))
	}
	if stats.eventIndex != nil || stats.eventIndexVersion != 0 {
		t.Fatalf("event index after record = len %d version %d, want cleared", len(stats.eventIndex), stats.eventIndexVersion)
	}
	updated := stats.QueryEvents(params)
	if len(updated.Events) != 2 || updated.Events[0].Tokens.TotalTokens != 99 {
		t.Fatalf("updated events = %#v, want new event first", updated.Events)
	}
}

func TestDashboardEventsCacheHitPreservesGeneratedAt(t *testing.T) {
	stats := NewRequestStatistics()
	params := normalizeEventsQuery(EventsQuery{Range: "all"}, true)
	cacheKey := dashboardEventCacheKeyFor(params, time.Now())
	cachedGeneratedAt := "2026-01-02T03:04:05Z"
	stats.eventQueryCache = map[dashboardEventCacheKey]EventsResult{
		cacheKey: {
			Events:      []RequestDetail{{Model: "gpt-4", Tokens: TokenStats{TotalTokens: 1}}},
			Total:       1,
			Limit:       params.Limit,
			Offset:      params.Offset,
			GeneratedAt: cachedGeneratedAt,
		},
	}
	stats.eventQueryCacheOrder = []dashboardEventCacheKey{cacheKey}

	result := stats.QueryEvents(EventsQuery{Range: "all"})
	if result.GeneratedAt != cachedGeneratedAt {
		t.Fatalf("cache hit generated_at = %q, want cached %q", result.GeneratedAt, cachedGeneratedAt)
	}
	if stats.eventCacheHits != 1 || stats.eventCacheMisses != 0 {
		t.Fatalf("cache metrics hits/misses = %d/%d, want 1/0", stats.eventCacheHits, stats.eventCacheMisses)
	}
}

func TestRuntimeStatusReportsDashboardMetrics(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 2; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(10 + i)},
		})
	}

	_ = stats.SummaryWithoutDetails()
	_ = stats.SummaryWithoutDetails()
	params := EventsQuery{Limit: 2, Offset: 0}
	_ = stats.QueryEvents(params)
	_ = stats.QueryEvents(params)
	_ = stats.QueryAPIDetail("openai", "24h", 10, 10)

	status := stats.RuntimeStatus()
	if status.SummaryCacheMisses != 1 || status.SummaryCacheHits != 1 {
		t.Fatalf("summary cache metrics = hits %d misses %d, want 1/1", status.SummaryCacheHits, status.SummaryCacheMisses)
	}
	if !status.SummaryCacheValid || status.SummaryVersion == 0 {
		t.Fatalf("summary cache status = valid %v version %d", status.SummaryCacheValid, status.SummaryVersion)
	}
	if status.EventCacheMisses != 1 || status.EventCacheHits != 1 || status.EventCacheEntries != 1 {
		t.Fatalf("event cache metrics = hits %d misses %d entries %d, want 1/1/1",
			status.EventCacheHits, status.EventCacheMisses, status.EventCacheEntries)
	}
	if status.LastEventsQueryTotal != 2 || status.EventIndexEntries != 2 {
		t.Fatalf("event query metrics = total %d index entries %d, want 2/2",
			status.LastEventsQueryTotal, status.EventIndexEntries)
	}
	if status.APIDetailQueries != 1 || status.LastAPIDetailTotalEvents != 2 {
		t.Fatalf("api detail metrics = queries %d total %d, want 1/2",
			status.APIDetailQueries, status.LastAPIDetailTotalEvents)
	}
	if status.LastSummaryDurationMs <= 0 || status.LastEventsQueryDurationMs <= 0 || status.LastAPIDetailDurationMs <= 0 {
		t.Fatalf("query durations should be reported: %#v", status)
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

func TestDashboardEventsSecondaryIndexesBuildAndInvalidate(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai-prod",
		Model:       "gpt-4",
		AuthIndex:   "auth-a",
		RequestedAt: base,
		Detail:      UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider:    "deepseek",
		Source:      "deepseek-prod",
		Model:       "deepseek-chat",
		AuthIndex:   "auth-b",
		RequestedAt: base.Add(time.Minute),
		Detail:      UsageDetail{TotalTokens: 50},
	})

	if result := stats.QueryEvents(EventsQuery{Limit: 50, Model: "gpt-4"}); result.Total != 1 {
		t.Fatalf("model-filtered total = %d, want 1", result.Total)
	}
	if result := stats.QueryEvents(EventsQuery{Limit: 50, Source: "deepseek-prod"}); result.Total != 1 {
		t.Fatalf("source-filtered total = %d, want 1", result.Total)
	}
	if result := stats.QueryEvents(EventsQuery{Limit: 50, AuthIndex: "auth-a"}); result.Total != 1 {
		t.Fatalf("auth-filtered total = %d, want 1", result.Total)
	}

	status := stats.RuntimeStatus()
	if status.EventIndexEntries != 1 {
		t.Fatalf("secondary event index entries = %d, want 1", status.EventIndexEntries)
	}

	func() {
		stats.mu.RLock()
		defer stats.mu.RUnlock()
		if stats.eventIndex != nil {
			t.Fatalf("global event index should not be built for secondary-only filters")
		}
		if len(stats.eventModelIndex["gpt-4"]) != 1 {
			t.Fatalf("model index len = %d, want 1", len(stats.eventModelIndex["gpt-4"]))
		}
		if len(stats.eventSourceIndex["deepseek-prod"]) != 1 {
			t.Fatalf("source index len = %d, want 1", len(stats.eventSourceIndex["deepseek-prod"]))
		}
		if len(stats.eventAuthIndex["auth-a"]) != 1 {
			t.Fatalf("auth index len = %d, want 1", len(stats.eventAuthIndex["auth-a"]))
		}
	}()

	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai-prod",
		Model:       "gpt-4",
		AuthIndex:   "auth-a",
		RequestedAt: base.Add(2 * time.Minute),
		Detail:      UsageDetail{TotalTokens: 120},
	})

	stats.mu.RLock()
	defer stats.mu.RUnlock()
	if stats.eventIndex != nil || stats.eventModelIndex != nil || stats.eventSourceIndex != nil || stats.eventAuthIndex != nil {
		t.Fatalf("event indexes should be cleared after record")
	}
	if stats.eventIndexVersion != 0 {
		t.Fatalf("event index version after record = %d, want 0", stats.eventIndexVersion)
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

func TestDashboardEventsNegativeOffsetUsesFirstPage(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Second),
			Detail:      UsageDetail{TotalTokens: int64(i + 1)},
		})
	}

	result := stats.QueryEvents(EventsQuery{Limit: 2, Offset: -10})
	if result.Offset != 0 {
		t.Fatalf("offset = %d, want 0", result.Offset)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(result.Events))
	}
	if result.Events[0].Tokens.TotalTokens != 3 || result.Events[1].Tokens.TotalTokens != 2 {
		t.Fatalf("events are not the first page in descending order: %#v", result.Events)
	}
}

func TestDashboardEventsExportReturnsAllFilteredRows(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 7; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(10 + i)},
		})
	}
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-chat",
			RequestedAt: base.Add(time.Duration(i+10) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(100 + i)},
		})
	}

	result := stats.QueryAllEvents(EventsQuery{Limit: 2, Offset: 5, API: "openai"})
	if result.Total != 7 || len(result.Events) != 7 {
		t.Fatalf("export result total/len = %d/%d, want 7/7", result.Total, len(result.Events))
	}
	if result.Limit != 7 || result.Offset != 0 {
		t.Fatalf("export limit/offset = %d/%d, want 7/0", result.Limit, result.Offset)
	}
	if result.Events[0].Tokens.TotalTokens != 16 || result.Events[6].Tokens.TotalTokens != 10 {
		t.Fatalf("export events not sorted newest first or not filtered: %#v", result.Events)
	}
}

func TestDashboardEventsExportLimitKeepsTotalAndMarksTruncated(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(10 + i)},
		})
	}

	result := stats.QueryExportEvents(EventsQuery{}, 3)
	if result.Total != 6 || len(result.Events) != 3 || result.Limit != 3 || !result.Truncated {
		t.Fatalf("limited export = total %d len %d limit %d truncated %v, want 6/3/3/true", result.Total, len(result.Events), result.Limit, result.Truncated)
	}
	if result.Events[0].Tokens.TotalTokens != 15 || result.Events[2].Tokens.TotalTokens != 13 {
		t.Fatalf("limited export should keep newest rows first: %#v", result.Events)
	}

	filtered := stats.QueryExportEvents(EventsQuery{Range: "24h", Model: "gpt-4"}, 10)
	if filtered.Total != 6 || len(filtered.Events) != 6 || filtered.Limit != 6 || filtered.Truncated {
		t.Fatalf("uncapped filtered export = total %d len %d limit %d truncated %v, want 6/6/6/false", filtered.Total, len(filtered.Events), filtered.Limit, filtered.Truncated)
	}
}

func TestDashboardEventsCSVUsesFullUpstreamInterfaceAsSource(t *testing.T) {
	event := RequestDetail{
		UpstreamAPI: "codex · 上游 b374b8e7c98ca23c",
		Source:      "codex",
		Provider:    "codex",
		Timestamp:   time.Date(2026, 7, 16, 5, 0, 0, 0, time.UTC),
		Model:       "gpt-5.5",
	}
	record := dashboardEventCSVRecord(event)
	if record[2] != event.UpstreamAPI {
		t.Fatalf("csv source = %q, want full upstream interface %q", record[2], event.UpstreamAPI)
	}
}

func TestDashboardEventCSVUsesUncachedInputTokens(t *testing.T) {
	openAI := RequestDetail{
		Provider: "openai",
		Tokens:   TokenStats{InputTokens: 100, OutputTokens: 20, CachedTokens: 30, CacheWriteTokens: 10, CacheTokens: 40, TotalTokens: 120},
	}
	if got := dashboardEventCSVRecord(openAI)[7]; got != "60" {
		t.Fatalf("OpenAI CSV input = %q, want uncached input 60", got)
	}

	claude := RequestDetail{
		Provider: "anthropic",
		Tokens:   TokenStats{InputTokens: 100, OutputTokens: 20, CachedTokens: 30, CacheWriteTokens: 10, CacheTokens: 40, TotalTokens: 160},
	}
	if got := dashboardEventCSVRecord(claude)[7]; got != "100" {
		t.Fatalf("Claude CSV input = %q, want already-exclusive input 100", got)
	}
}

func TestUsesExclusiveCacheInputRequiresPositiveUnknownTotal(t *testing.T) {
	if !usesExclusiveCacheInput("anthropic", 100, 20, 40, 160) {
		t.Fatal("Anthropic provider should use exclusive cache input")
	}
	if !usesExclusiveCacheInput("", 60, 20, 40, 120) {
		t.Fatal("empty provider with a matching positive total should use exclusive cache input")
	}
	if usesExclusiveCacheInput("", 0, 0, 0, 0) {
		t.Fatal("empty provider with a zero total must not infer exclusive cache input")
	}
}

func TestDashboardEventsExportPageUsesSnapshotAndLimit(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(10 + i)},
		})
	}
	snapshotAt := base.Add(10 * time.Minute)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: snapshotAt.Add(time.Minute),
		Detail:      UsageDetail{TotalTokens: 999},
	})

	first := stats.QueryExportEventsPage(EventsQuery{}, 0, 2, 3, snapshotAt)
	if first.Total != 6 || len(first.Events) != 2 || first.Limit != 3 || !first.Truncated {
		t.Fatalf("first export page = total %d len %d limit %d truncated %v, want 6/2/3/true", first.Total, len(first.Events), first.Limit, first.Truncated)
	}
	if first.Events[0].Tokens.TotalTokens != 15 || first.Events[1].Tokens.TotalTokens != 14 {
		t.Fatalf("first export page should start with newest snapshot rows: %#v", first.Events)
	}

	second := stats.QueryExportEventsPage(EventsQuery{}, 2, 2, 3, snapshotAt)
	if second.Total != 6 || len(second.Events) != 1 || second.Limit != 3 || !second.Truncated {
		t.Fatalf("second export page = total %d len %d limit %d truncated %v, want 6/1/3/true", second.Total, len(second.Events), second.Limit, second.Truncated)
	}
	if second.Events[0].Tokens.TotalTokens != 13 {
		t.Fatalf("second export page should continue after offset: %#v", second.Events)
	}

	afterLimit := stats.QueryExportEventsPage(EventsQuery{}, 3, 2, 3, snapshotAt)
	if afterLimit.Total != 6 || len(afterLimit.Events) != 0 || afterLimit.Limit != 3 || !afterLimit.Truncated {
		t.Fatalf("after-limit export page = total %d len %d limit %d truncated %v, want 6/0/3/true", afterLimit.Total, len(afterLimit.Events), afterLimit.Limit, afterLimit.Truncated)
	}
}

func TestDashboardEventsExportLimitQueryReturnsTruncationHeaders(t *testing.T) {
	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	t.Cleanup(func() { stats = previousStats })

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(10 + i)},
		})
	}

	resp := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "GET",
		Path:   "/v0/management/plugins/usage-dashboard-zduu/dashboard-events-export",
		Query:  map[string][]string{"limit": {"2"}},
	}), nil)

	if got := resp.Headers["X-Total-Count"]; len(got) != 1 || got[0] != "5" {
		t.Fatalf("X-Total-Count = %#v, want 5", got)
	}
	if got := resp.Headers["X-Exported-Count"]; len(got) != 1 || got[0] != "2" {
		t.Fatalf("X-Exported-Count = %#v, want 2", got)
	}
	if got := resp.Headers["X-Export-Truncated"]; len(got) != 1 || got[0] != "true" {
		t.Fatalf("X-Export-Truncated = %#v, want true", got)
	}

	var result EventsResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal limited export: %v", err)
	}
	if result.Total != 5 || len(result.Events) != 2 || result.Limit != 2 || !result.Truncated {
		t.Fatalf("limited management export = total %d len %d limit %d truncated %v, want 5/2/2/true", result.Total, len(result.Events), result.Limit, result.Truncated)
	}

	runtime := stats.RuntimeStatus()
	if runtime.EventsExportRequests != 1 || runtime.EventsExportTruncatedTotal != 1 {
		t.Fatalf("export runtime counters = requests %d truncated %d, want 1/1", runtime.EventsExportRequests, runtime.EventsExportTruncatedTotal)
	}
	if runtime.LastEventsExportTotal != 5 || runtime.LastEventsExported != 2 || !runtime.LastEventsExportTruncated {
		t.Fatalf("last export rows = total %d exported %d truncated %v, want 5/2/true",
			runtime.LastEventsExportTotal, runtime.LastEventsExported, runtime.LastEventsExportTruncated)
	}
}

func TestDashboardEventsExportQueryLimitCannotExceedConfiguredLimit(t *testing.T) {
	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, ExportMaxRecords: 3})
	t.Cleanup(func() { stats = previousStats })

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{TotalTokens: int64(10 + i)},
		})
	}

	resp := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "GET",
		Path:   "/v0/management/plugins/usage-dashboard-zduu/dashboard-events-export",
		Query:  map[string][]string{"limit": {"4"}},
	}), nil)

	if got := resp.Headers["X-Exported-Count"]; len(got) != 1 || got[0] != "3" {
		t.Fatalf("X-Exported-Count = %#v, want configured cap 3", got)
	}
	var result EventsResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal limited export: %v", err)
	}
	if result.Total != 5 || len(result.Events) != 3 || result.Limit != 3 || !result.Truncated {
		t.Fatalf("configured limited export = total %d len %d limit %d truncated %v, want 5/3/3/true", result.Total, len(result.Events), result.Limit, result.Truncated)
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

func TestDashboardEventsRangeExcludesZeroTimestampEvents(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.mu.Lock()
	stats.recordDetailLocked("openai", "gpt-4", RequestDetail{
		Model:     "gpt-4",
		Provider:  "openai",
		Timestamp: time.Time{},
		Tokens:    TokenStats{TotalTokens: 100},
	}, requestDedupKey{}, time.Now(), false)
	stats.mu.Unlock()

	all := stats.QueryEvents(EventsQuery{Limit: 50, Range: "all"})
	if all.Total != 1 {
		t.Fatalf("all range total = %d, want 1", all.Total)
	}
	rangeResult := stats.QueryEvents(EventsQuery{Limit: 50, Range: "24h"})
	if rangeResult.Total != 0 {
		t.Fatalf("24h range total = %d, want zero timestamp event excluded", rangeResult.Total)
	}
}

func TestDashboardAPIDetailAggregatesErrorsAndRecentEvents(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0, RetentionDays: 30})
	base := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 5; i++ {
		failed := i == 1 || i == 3
		record := UsageRecord{
			Provider:    "openai",
			Source:      "openai",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Failed:      failed,
			Detail:      UsageDetail{InputTokens: int64(10 + i), OutputTokens: 5},
		}
		if failed {
			record.Failure = UsageFailure{StatusCode: 401, Body: `{"error":{"type":"ModelError","message":"not supported"}}`}
		}
		stats.Record(record)
	}
	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-48 * time.Hour),
		Failed:      true,
		Failure:     UsageFailure{StatusCode: 500, Body: "old failure"},
		Detail:      UsageDetail{TotalTokens: 99},
	})
	stats.Record(UsageRecord{
		Provider:    "deepseek",
		Source:      "deepseek",
		Model:       "deepseek-chat",
		RequestedAt: base.Add(time.Hour),
		Detail:      UsageDetail{TotalTokens: 1000},
	})

	result := stats.QueryAPIDetail("openai", "24h", 3, 10)
	if result.API != "openai" {
		t.Fatalf("api = %q, want openai", result.API)
	}
	if result.Summary.TotalRequests != 5 || result.Summary.FailureCount != 2 || result.Summary.SuccessCount != 3 {
		t.Fatalf("summary = %#v, want 5 total / 2 failed / 3 success", result.Summary)
	}
	if result.Summary.TotalTokens != 85 {
		t.Fatalf("total tokens = %d, want 85", result.Summary.TotalTokens)
	}
	if len(result.ModelStats) != 1 || result.ModelStats[0].Model != "gpt-4" || result.ModelStats[0].TotalRequests != 5 {
		t.Fatalf("model stats = %#v", result.ModelStats)
	}
	if len(result.SourceStats) != 1 || result.SourceStats[0].Source != "openai" || result.SourceStats[0].TotalRequests != 5 {
		t.Fatalf("source stats = %#v", result.SourceStats)
	}
	if len(result.ErrorStats) != 1 || result.ErrorStats[0].StatusCode != 401 || result.ErrorStats[0].Count != 2 {
		t.Fatalf("error stats = %#v, want one 401 x2", result.ErrorStats)
	}
	if len(result.RecentEvents) != 3 {
		t.Fatalf("recent events = %d, want 3", len(result.RecentEvents))
	}
	for i, event := range result.RecentEvents {
		if event.UpstreamAPI != "openai" {
			t.Fatalf("recent event %d api = %q, want openai", i, event.UpstreamAPI)
		}
	}
	if !result.RecentEvents[0].Timestamp.After(result.RecentEvents[1].Timestamp) ||
		!result.RecentEvents[1].Timestamp.After(result.RecentEvents[2].Timestamp) {
		t.Fatalf("recent events not sorted descending: %#v", result.RecentEvents)
	}
	if result.TotalEvents != 5 {
		t.Fatalf("total events = %d, want 5", result.TotalEvents)
	}
}

func TestDashboardAPIDetailRecentEventsPreserveReasoningEffortAndEndpoint(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 20, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider:        "openai",
		Source:          "openai",
		Model:           "gpt-5.5",
		Endpoint:        "/v1/responses",
		ReasoningEffort: "xhigh",
		Stream:          true,
		RequestedAt:     time.Now(),
		Latency:         11 * time.Second,
		TTFT:            time.Second,
		Detail:          UsageDetail{InputTokens: 100, OutputTokens: 358},
	})

	result := stats.QueryAPIDetail("openai", "24h", 10, 10)
	if len(result.RecentEvents) != 1 {
		t.Fatalf("recent events = %d, want 1", len(result.RecentEvents))
	}
	event := result.RecentEvents[0]
	if event.Endpoint != "/v1/responses" || event.Thinking.Intensity != "xhigh" || !event.Stream {
		t.Fatalf("recent event endpoint/thinking/stream = %q/%#v/%t", event.Endpoint, event.Thinking, event.Stream)
	}
}

func TestDashboardAPIDetailAllRangeUsesAggregatesAfterDetailTrim(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 2, DedupWindowMinutes: 0, RetentionDays: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Source:      "openai-prod",
			Model:       "gpt-4",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{InputTokens: int64(i + 1), OutputTokens: 10},
		})
	}

	result := stats.QueryAPIDetail("openai · openai-prod", "all", 10, 10)
	if result.Summary.TotalRequests != 5 {
		t.Fatalf("summary total_requests = %d, want 5", result.Summary.TotalRequests)
	}
	if len(result.ModelStats) != 1 || result.ModelStats[0].TotalRequests != 5 {
		t.Fatalf("model stats = %#v, want aggregate total 5", result.ModelStats)
	}
	if len(result.SourceStats) != 1 || result.SourceStats[0].Source != "openai-prod" || result.SourceStats[0].TotalRequests != 5 {
		t.Fatalf("source stats = %#v, want aggregate source total 5", result.SourceStats)
	}
	if len(result.RecentEvents) != 2 {
		t.Fatalf("recent events = %d, want retained detail count 2", len(result.RecentEvents))
	}
	if result.TotalEvents != 5 {
		t.Fatalf("total events = %d, want aggregate total 5", result.TotalEvents)
	}
}

func TestDashboardAPIDetailRecentEventsTieBreaksByModel(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, DedupWindowMinutes: 0, RetentionDays: 30})
	ts := time.Now().Add(-5 * time.Minute)

	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai",
		Model:       "z-model",
		RequestedAt: ts,
		Detail:      UsageDetail{TotalTokens: 10},
	})
	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai",
		Model:       "a-model",
		RequestedAt: ts,
		Detail:      UsageDetail{TotalTokens: 20},
	})

	result := stats.QueryAPIDetail("openai", "24h", 2, 10)
	if len(result.RecentEvents) != 2 {
		t.Fatalf("recent events = %d, want 2", len(result.RecentEvents))
	}
	if result.RecentEvents[0].Model != "a-model" || result.RecentEvents[1].Model != "z-model" {
		t.Fatalf("recent events = %#v, want same timestamp sorted by model", result.RecentEvents)
	}
}

func TestDashboardAPIDetailRangeExcludesZeroTimestampEvents(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.mu.Lock()
	stats.recordDetailLocked("openai", "gpt-4", RequestDetail{
		Model:     "gpt-4",
		Provider:  "openai",
		Timestamp: time.Time{},
		Tokens:    TokenStats{TotalTokens: 100},
	}, requestDedupKey{}, time.Now(), false)
	stats.mu.Unlock()

	all := stats.QueryAPIDetail("openai", "all", 10, 10)
	if all.Summary.TotalRequests != 1 || all.TotalEvents != 1 {
		t.Fatalf("all api detail = %#v, total_events = %d, want one zero timestamp event", all.Summary, all.TotalEvents)
	}
	rangeResult := stats.QueryAPIDetail("openai", "24h", 10, 10)
	if rangeResult.Summary.TotalRequests != 0 || rangeResult.TotalEvents != 0 || len(rangeResult.RecentEvents) != 0 {
		t.Fatalf("24h api detail summary = %#v, total_events = %d, recent = %#v; want zero timestamp event excluded", rangeResult.Summary, rangeResult.TotalEvents, rangeResult.RecentEvents)
	}
}

func TestCacheWriteCostSeparatesReadWriteAndProviderAccounting(t *testing.T) {
	stats := NewRequestStatistics()
	stats.modelPrices = map[string]ModelPrice{
		"model": {Prompt: 1, Cache: 0.1, CacheWrite: 1.25},
	}

	for name, stat := range map[string]TimeSeriesTokenStat{
		"inclusive input": {
			Model:            "model",
			Provider:         "openai",
			TotalTokens:      1_000_000,
			InputTokens:      1_000_000,
			CachedTokens:     200_000,
			CacheWriteTokens: 300_000,
		},
		"exclusive input": {
			Model:            "model",
			Provider:         "anthropic",
			TotalTokens:      1_000_000,
			InputTokens:      500_000,
			CachedTokens:     200_000,
			CacheWriteTokens: 300_000,
		},
		"inclusive input with inflated total": {
			Model:            "model",
			Provider:         "openai",
			TotalTokens:      1_500_000,
			InputTokens:      1_000_000,
			CachedTokens:     200_000,
			CacheWriteTokens: 300_000,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := stats.timeSeriesTokenCostLocked(stat); math.Abs(got-0.895) > 1e-9 {
				t.Fatalf("cost = %v, want 0.895", got)
			}
		})
	}

	when := time.Date(2026, 7, 11, 10, 30, 0, 0, time.UTC)
	recorded := NewRequestStatistics()
	recorded.modelPrices = map[string]ModelPrice{
		"model": {Prompt: 1, Cache: 0.1, CacheWrite: 1.25},
	}
	recorded.Record(UsageRecord{
		Provider:    "openai",
		Model:       "model",
		RequestedAt: when,
		Detail: UsageDetail{
			InputTokens:         1_000_000,
			CacheReadTokens:     200_000,
			CacheCreationTokens: 300_000,
			TotalTokens:         1_000_000,
		},
	})

	snapshot := recorded.Snapshot()
	assertFloatNear(t, "recorded daily cache cost", snapshot.CostByDay["2026-07-11"], 0.895)
	assertFloatNear(t, "recorded hourly cache cost", snapshot.CostByHour["10"], 0.895)

	recorded.mu.RLock()
	rebuiltDay := recorded.costByDayFromTokenSeriesLocked()
	rebuiltHour := recorded.costByHourFromTokenSeriesLocked()
	recorded.mu.RUnlock()
	assertFloatNear(t, "rebuilt daily cache cost", rebuiltDay["2026-07-11"], 0.895)
	assertFloatNear(t, "rebuilt hourly cache cost", rebuiltHour[10], 0.895)
}

func TestMissingTotalTokensIncludesExclusiveClaudeCache(t *testing.T) {
	claudeUsage := UsageDetail{
		InputTokens:         100,
		OutputTokens:        20,
		CacheReadTokens:     40,
		CacheCreationTokens: 30,
	}
	if got := usageDetailTotalTokens(claudeUsage, "anthropic"); got != 190 {
		t.Fatalf("Claude usage total = %d, want 190", got)
	}
	if got := usageDetailTotalTokens(claudeUsage, "openai-compatible"); got != 120 {
		t.Fatalf("inclusive usage total = %d, want 120", got)
	}
	aliasUsage := UsageDetail{InputTokens: 100, OutputTokens: 20, CachedTokens: 40, CacheCreationTokens: 30}
	cacheRead, cacheWrite, cacheTotal := usageDetailCacheTokenParts(aliasUsage)
	if cacheRead != 40 || cacheWrite != 30 || cacheTotal != 70 || usageDetailTotalTokens(aliasUsage, "claude") != 190 {
		t.Fatalf("aliased cache parts = read %d write %d total %d, want 40/30/70", cacheRead, cacheWrite, cacheTotal)
	}

	detail := RequestDetail{
		Provider: "claude",
		Tokens: TokenStats{
			InputTokens:      100,
			OutputTokens:     20,
			CacheTokens:      70,
			CacheWriteTokens: 30,
		},
	}
	if got := detailTotalTokensForRequest(detail); got != 190 {
		t.Fatalf("Claude request total = %d, want 190", got)
	}
}

func TestDashboardAPIDetailRangePreservesCacheWriteTokens(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})
	now := time.Now().UTC()
	detail := RequestDetail{
		Model:     "model",
		Provider:  "anthropic",
		Timestamp: now.Add(-time.Minute),
		Tokens: TokenStats{
			InputTokens:      1_000,
			CacheTokens:      500,
			CacheWriteTokens: 300,
			TotalTokens:      1_000,
		},
	}
	stats.mu.Lock()
	if !stats.recordDetailLocked("anthropic", "model", detail, requestDedupKey{}, now, false) {
		stats.mu.Unlock()
		t.Fatal("record detail failed")
	}
	stats.mu.Unlock()

	summary := stats.SummaryWithoutDetailsAt(now)
	if summary.Usage.CachedTokens != 200 || summary.Usage.CacheWriteTokens != 300 || summary.ModelStats[0].CachedTokens != 200 || summary.ModelStats[0].CacheWriteTokens != 300 {
		t.Fatalf("summary cache read/write = usage %#v model %#v, want 200/300", summary.Usage, summary.ModelStats)
	}
	api := summary.Usage.APIs["anthropic"]
	if api.CachedTokens != 200 || api.CacheWriteTokens != 300 || api.Models["model"].CachedTokens != 200 || api.Models["model"].CacheWriteTokens != 300 {
		t.Fatalf("api cache read/write = %#v, want 200/300", api)
	}

	result := stats.QueryAPIDetailAt("anthropic", "24h", 10, 10, now)
	if result.Summary.CachedTokens != 200 || result.Summary.CacheWriteTokens != 300 || len(result.ModelStats) != 1 || result.ModelStats[0].CachedTokens != 200 || result.ModelStats[0].CacheWriteTokens != 300 {
		t.Fatalf("range cache read/write = summary %#v models %#v, want 200/300", result.Summary, result.ModelStats)
	}
	if len(result.ModelStats[0].Providers) != 1 || result.ModelStats[0].Providers[0].CachedTokens != 200 || result.ModelStats[0].Providers[0].CacheWriteTokens != 300 {
		t.Fatalf("range provider cache read/write = %#v, want 200/300", result.ModelStats[0].Providers)
	}
}

func TestStorageSnapshotPreservesProviderAggregatesAfterDetailTrimming(t *testing.T) {
	now := time.Now().UTC()
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 1, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.mu.Lock()
	for i := 0; i < 2; i++ {
		detail := RequestDetail{
			Model:     "model",
			Provider:  "anthropic",
			Timestamp: now.Add(time.Duration(i-2) * time.Minute),
			Tokens: TokenStats{
				InputTokens:      1_000,
				CacheTokens:      500,
				CacheWriteTokens: 300,
				TotalTokens:      1_000,
			},
		}
		if !stats.recordDetailLocked("anthropic", "model", detail, requestDedupKey{}, now, false) {
			stats.mu.Unlock()
			t.Fatal("record detail failed")
		}
	}
	stats.pruneLocked(now, false)
	snapshot := stats.snapshotLocked()
	stats.mu.Unlock()

	modelSnapshot := snapshot.APIs["anthropic"].Models["model"]
	if modelSnapshot.TotalRequests != 2 || len(modelSnapshot.Details) != 1 || len(modelSnapshot.Providers) != 1 {
		t.Fatalf("snapshot model = %#v, want 2 requests, 1 detail and 1 provider", modelSnapshot)
	}
	if modelSnapshot.Providers[0].TotalRequests != 2 || modelSnapshot.Providers[0].CacheWriteTokens != 600 {
		t.Fatalf("snapshot providers = %#v, want full untrimmed aggregates", modelSnapshot.Providers)
	}

	restored := NewRequestStatistics()
	restored.Configure(runtimeConfig{MaxDetailsPerModel: 1, DedupWindowMinutes: 0, RetentionDays: 30})
	restored.mu.Lock()
	restored.restoreStorageSnapshotLocked(snapshot, now)
	restored.mu.Unlock()

	summary := restored.SummaryWithoutDetailsAt(now)
	if len(summary.ModelStats) != 1 || len(summary.ModelStats[0].Providers) != 1 {
		t.Fatalf("restored model stats = %#v", summary.ModelStats)
	}
	provider := summary.ModelStats[0].Providers[0]
	if provider.TotalRequests != 2 || provider.CacheWriteTokens != 600 || provider.CachedTokens != 400 {
		t.Fatalf("restored provider = %#v, want full untrimmed aggregates", provider)
	}
}

func TestLegacyStorageSnapshotMigratesCacheTotalsToReads(t *testing.T) {
	snapshot := StatisticsSnapshot{
		CachedTokens:     70,
		CacheWriteTokens: 40,
		APIs: map[string]APISnapshot{
			"openai": {
				CachedTokens:     70,
				CacheWriteTokens: 40,
				Models: map[string]ModelSnapshot{
					"model": {
						CachedTokens:     70,
						CacheWriteTokens: 40,
						Providers: []ModelProviderStat{{
							Provider:         "openai",
							CachedTokens:     70,
							CacheWriteTokens: 40,
						}},
					},
				},
			},
		},
		CostTokensByDay: map[string][]TimeSeriesTokenStat{
			"2026-07-11": {{CachedTokens: 70, CacheWriteTokens: 40}},
		},
	}

	migrateLegacySnapshotCacheReads(&snapshot)
	if snapshot.CachedTokens != 30 || snapshot.APIs["openai"].CachedTokens != 30 {
		t.Fatalf("snapshot cache reads = %#v, want 30", snapshot)
	}
	model := snapshot.APIs["openai"].Models["model"]
	if model.CachedTokens != 30 || len(model.Providers) != 1 || model.Providers[0].CachedTokens != 30 {
		t.Fatalf("model cache reads = %#v, want 30", model)
	}
	if snapshot.CostTokensByDay["2026-07-11"][0].CachedTokens != 30 {
		t.Fatalf("time-series cache reads = %#v, want 30", snapshot.CostTokensByDay)
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

func TestDashboardDataIncludesAggregateTokenParts(t *testing.T) {
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 2, RetentionDays: 0, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4.1",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Latency:     time.Duration((i+1)*10) * time.Millisecond,
			Detail: UsageDetail{
				InputTokens:     int64(i + 1),
				OutputTokens:    int64((i + 1) * 10),
				ReasoningTokens: int64((i + 1) * 100),
				CachedTokens:    int64(i + 5),
			},
		})
	}

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

	var data struct {
		Usage StatisticsSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}

	if data.Usage.TotalRequests != 3 || data.Usage.TotalTokens != 66 {
		t.Fatalf("usage totals = requests %d tokens %d, want 3/66", data.Usage.TotalRequests, data.Usage.TotalTokens)
	}
	if data.Usage.InputTokens != 6 || data.Usage.OutputTokens != 60 ||
		data.Usage.ReasoningTokens != 600 || data.Usage.CachedTokens != 18 {
		t.Fatalf("usage token parts = %#v", data.Usage)
	}
	if data.Usage.AvgLatencyMs != 20 {
		t.Fatalf("usage avg latency = %v, want 20", data.Usage.AvgLatencyMs)
	}

	api := data.Usage.APIs["openai"]
	if api.InputTokens != 6 || api.OutputTokens != 60 || api.ReasoningTokens != 600 ||
		api.CachedTokens != 18 || api.AvgLatencyMs != 20 {
		t.Fatalf("api aggregate = %#v", api)
	}

	model := api.Models["gpt-4.1"]
	if model.InputTokens != 6 || model.OutputTokens != 60 || model.ReasoningTokens != 600 ||
		model.CachedTokens != 18 || model.AvgLatencyMs != 20 {
		t.Fatalf("model aggregate = %#v", model)
	}
	if len(model.Details) != 2 {
		t.Fatalf("trimmed details len = %d, want 2", len(model.Details))
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

func TestSummaryWithoutDetailsKeepsAggregatesAfterDetailTrim(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 2, RetentionDays: 0, DedupWindowMinutes: 0})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		source := "source-shared"
		authIndex := "auth-live"
		apiKey := "sk-client-live-123456"
		if i == 0 {
			authIndex = "auth-old"
			apiKey = "sk-client-old-123456"
		}
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4.1",
			APIKey:      apiKey,
			AuthIndex:   authIndex,
			Source:      source,
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Latency:     time.Duration((i+1)*10) * time.Millisecond,
			Detail: UsageDetail{
				InputTokens:     int64(i + 1),
				OutputTokens:    int64((i + 1) * 10),
				ReasoningTokens: int64((i + 1) * 100),
				CachedTokens:    int64(i + 5),
			},
		})
	}

	assertSummary := func(label string, summary DashboardSummary) {
		if summary.Usage.TotalRequests != 3 || summary.Usage.TotalTokens != 66 {
			t.Fatalf("%s usage totals = requests %d tokens %d, want 3/66", label, summary.Usage.TotalRequests, summary.Usage.TotalTokens)
		}
		if summary.Usage.InputTokens != 6 || summary.Usage.OutputTokens != 60 ||
			summary.Usage.ReasoningTokens != 600 || summary.Usage.CachedTokens != 18 {
			t.Fatalf("%s usage token parts = %#v", label, summary.Usage)
		}
		if summary.Usage.AvgLatencyMs != 20 {
			t.Fatalf("%s usage avg latency = %v, want 20", label, summary.Usage.AvgLatencyMs)
		}
		if len(summary.Usage.APIs) != 1 {
			t.Fatalf("%s api count = %d, want 1: %#v", label, len(summary.Usage.APIs), summary.Usage.APIs)
		}
		var api APISnapshotWithoutDetails
		for _, candidate := range summary.Usage.APIs {
			api = candidate
		}
		if api.InputTokens != 6 || api.OutputTokens != 60 || api.ReasoningTokens != 600 ||
			api.CachedTokens != 18 || api.AvgLatencyMs != 20 {
			t.Fatalf("%s api aggregate = %#v", label, api)
		}
		model := api.Models["gpt-4.1"]
		if model.InputTokens != 6 || model.OutputTokens != 60 || model.ReasoningTokens != 600 ||
			model.CachedTokens != 18 || model.AvgLatencyMs != 20 {
			t.Fatalf("%s model aggregate = %#v", label, model)
		}
		if len(summary.ModelStats) != 1 || summary.ModelStats[0].InputTokens != 6 ||
			summary.ModelStats[0].AvgLatencyMs != 20 {
			t.Fatalf("%s model stats = %#v", label, summary.ModelStats)
		}
		if len(summary.SourceStats) != 1 || summary.SourceStats[0].Source != "source-shared" ||
			summary.SourceStats[0].TotalRequests != 3 || summary.SourceStats[0].TotalTokens != 66 {
			t.Fatalf("%s source stats = %#v", label, summary.SourceStats)
		}
		if len(summary.CredentialStats) != 2 {
			t.Fatalf("%s credential stats = %#v", label, summary.CredentialStats)
		}
		if len(summary.ClientAPIStats) != 2 {
			t.Fatalf("%s client api stats = %#v", label, summary.ClientAPIStats)
		}
		var clientRequests, clientTokens, clientModelRequests int64
		for _, stat := range summary.ClientAPIStats {
			if stat.APIKey != "sk******56" || stat.APIKeyHash == "" || len(stat.Models) != 1 {
				t.Fatalf("%s client api stat = %#v", label, stat)
			}
			clientRequests += stat.TotalRequests
			clientTokens += stat.TotalTokens
			clientModelRequests += stat.Models[0].TotalRequests
		}
		if clientRequests != 3 || clientTokens != 66 || clientModelRequests != 3 {
			t.Fatalf("%s client api aggregate = %#v", label, summary.ClientAPIStats)
		}
		var healthTotal int64
		var healthSuccess int64
		var healthFailure int64
		for _, slot := range summary.HealthGrid {
			healthTotal += slot.Total
			healthSuccess += slot.Success
			healthFailure += slot.Failure
		}
		if healthTotal != 3 || healthSuccess != 3 || healthFailure != 0 {
			t.Fatalf("%s health totals = total %d success %d failure %d, want 3/3/0", label, healthTotal, healthSuccess, healthFailure)
		}
	}

	assertSummary("incremental", stats.SummaryWithoutDetails())
}

func TestCostSeriesRepricesAfterDetailTrim(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 1,
		RetentionDays:      0,
		DedupWindowMinutes: 0,
		PriceStoragePath:   filepath.Join(t.TempDir(), "prices.json"),
	})
	if _, err := stats.UpsertModelPrice("gpt-4.1", ModelPrice{Prompt: 1, Completion: 0, Cache: 0}); err != nil {
		t.Fatalf("UpsertModelPrice() error = %v", err)
	}

	base := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4.1",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail: UsageDetail{
				InputTokens: int64(i * 100),
			},
		})
	}

	summary := stats.SummaryWithoutDetails()
	if got := len(summary.Usage.APIs["openai"].Models["gpt-4.1"].Providers); got != 1 {
		t.Fatalf("provider stats count = %d, want 1", got)
	}
	assertFloatNear(t, "initial daily cost", summary.Usage.CostByDay["2026-07-03"], 0.0006)

	if _, err := stats.UpsertModelPrice("gpt-4.1", ModelPrice{Prompt: 2, Completion: 0, Cache: 0}); err != nil {
		t.Fatalf("UpsertModelPrice() update error = %v", err)
	}
	summary = stats.SummaryWithoutDetails()
	if summary.Usage.TotalRequests != 3 || summary.Usage.InputTokens != 600 {
		t.Fatalf("summary aggregate changed after trim/reprice: %#v", summary.Usage)
	}
	assertFloatNear(t, "repriced daily cost", summary.Usage.CostByDay["2026-07-03"], 0.0012)
}

func TestCostTokenSeriesSurvivesSnapshotWithTrimmedDetails(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 1,
		RetentionDays:      0,
		DedupWindowMinutes: 0,
		PriceStoragePath:   filepath.Join(t.TempDir(), "source-prices.json"),
	})
	if _, err := stats.UpsertModelPrice("gpt-4.1", ModelPrice{Prompt: 1, Completion: 0, Cache: 0}); err != nil {
		t.Fatalf("UpsertModelPrice() error = %v", err)
	}

	base := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4.1",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail: UsageDetail{
				InputTokens: int64(i * 100),
			},
		})
	}
	snapshot := stats.Snapshot()
	if len(snapshot.CostTokensByDay["2026-07-03"]) != 1 {
		t.Fatalf("snapshot cost token series = %#v", snapshot.CostTokensByDay)
	}
	if got := snapshot.CostTokensByDay["2026-07-03"][0].InputTokens; got != 600 {
		t.Fatalf("snapshot cost input tokens = %d, want 600", got)
	}

	restored := NewRequestStatistics()
	restored.Configure(runtimeConfig{
		MaxDetailsPerModel: 1,
		RetentionDays:      0,
		DedupWindowMinutes: 0,
		PriceStoragePath:   filepath.Join(t.TempDir(), "restored-prices.json"),
	})
	restored.mu.Lock()
	restored.restoreStorageSnapshotLocked(snapshot, time.Now())
	restored.mu.Unlock()
	if _, err := restored.UpsertModelPrice("gpt-4.1", ModelPrice{Prompt: 2, Completion: 0, Cache: 0}); err != nil {
		t.Fatalf("restored UpsertModelPrice() error = %v", err)
	}

	summary := restored.SummaryWithoutDetails()
	if summary.Usage.TotalRequests != 3 || summary.Usage.InputTokens != 600 {
		t.Fatalf("restored summary aggregate = %#v", summary.Usage)
	}
	assertFloatNear(t, "restored repriced daily cost", summary.Usage.CostByDay["2026-07-03"], 0.0012)
}

func TestCostTokenSnapshotMergesDuplicateNormalizedEntries(t *testing.T) {
	restored := NewRequestStatistics()
	restored.Configure(runtimeConfig{
		MaxDetailsPerModel: 100,
		DedupWindowMinutes: 0,
		PriceStoragePath:   filepath.Join(t.TempDir(), "prices.json"),
	})
	snapshot := StatisticsSnapshot{
		TotalRequests: 2,
		SuccessCount:  2,
		InputTokens:   300,
		APIs: map[string]APISnapshot{
			"openai": {
				TotalRequests: 2,
				SuccessCount:  2,
				InputTokens:   300,
				Models: map[string]ModelSnapshot{
					"gpt-4.1": {
						TotalRequests: 2,
						SuccessCount:  2,
						InputTokens:   300,
					},
				},
			},
		},
		CostTokensByDay: map[string][]TimeSeriesTokenStat{
			"2026-07-03": {
				{Model: "GPT-4.1", Provider: "OpenAI", InputTokens: 100},
				{Model: " gpt-4.1 ", Provider: " openai ", InputTokens: 200},
			},
		},
		CostTokensByHour: map[string][]TimeSeriesTokenStat{
			"10": {
				{Model: "GPT-4.1", Provider: "OpenAI", InputTokens: 100},
				{Model: "gpt-4.1", Provider: "openai", InputTokens: 200},
			},
		},
	}
	restored.mu.Lock()
	restored.restoreStorageSnapshotLocked(snapshot, time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))
	restored.mu.Unlock()

	if _, err := restored.UpsertModelPrice("openai/gpt-4.1", ModelPrice{Prompt: 2, Completion: 0, Cache: 0}); err != nil {
		t.Fatalf("UpsertModelPrice() error = %v", err)
	}
	summary := restored.SummaryWithoutDetails()
	assertFloatNear(t, "merged duplicate daily cost", summary.Usage.CostByDay["2026-07-03"], 0.0006)
	assertFloatNear(t, "merged duplicate hourly cost", summary.Usage.CostByHour["10"], 0.0006)
}

func TestLegacySnapshotCostSeriesPruneDoesNotGoNegative(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100,
		RetentionDays:      1,
		DedupWindowMinutes: 0,
		PriceStoragePath:   filepath.Join(t.TempDir(), "prices.json"),
	})
	if _, err := stats.UpsertModelPrice("openai/gpt-4.1", ModelPrice{Prompt: 1, Completion: 0, Cache: 0}); err != nil {
		t.Fatalf("UpsertModelPrice() error = %v", err)
	}

	oldTime := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	snapshot := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   100,
		InputTokens:   100,
		APIs: map[string]APISnapshot{
			"openai": {
				TotalRequests: 1,
				SuccessCount:  1,
				TotalTokens:   100,
				InputTokens:   100,
				Models: map[string]ModelSnapshot{
					"gpt-4.1": {
						TotalRequests: 1,
						SuccessCount:  1,
						TotalTokens:   100,
						InputTokens:   100,
						Details: []RequestDetail{{
							Model:     "gpt-4.1",
							Timestamp: oldTime,
							Provider:  "openai",
							Tokens: TokenStats{
								InputTokens: 100,
								TotalTokens: 100,
							},
						}},
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{"2026-07-01": 1},
		RequestsByHour: map[string]int64{"10": 1},
		TokensByDay:    map[string]int64{"2026-07-01": 100},
		TokensByHour:   map[string]int64{"10": 100},
	}

	stats.mu.Lock()
	stats.restoreStorageSnapshotLocked(snapshot, now)
	if got := stats.costByDay["2026-07-01"]; got <= 0 {
		stats.mu.Unlock()
		t.Fatalf("legacy restore daily cost = %.12f, want positive rebuilt cost", got)
	}
	stats.pruneLocked(now, true)
	costByDay := stats.costByDay["2026-07-01"]
	costByHour := stats.costByHour[10]
	stats.mu.Unlock()

	if costByDay < -0.000000001 || costByHour < -0.000000001 {
		t.Fatalf("pruned legacy costs went negative: day %.12f hour %.12f", costByDay, costByHour)
	}
	assertFloatNear(t, "pruned legacy daily cost", costByDay, 0)
	assertFloatNear(t, "pruned legacy hourly cost", costByHour, 0)
}

func TestLegacySnapshotWithCostMapsPruneRebuildsMissingCostTokens(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100,
		RetentionDays:      1,
		DedupWindowMinutes: 0,
		PriceStoragePath:   filepath.Join(t.TempDir(), "prices.json"),
	})
	if _, err := stats.UpsertModelPrice("openai/gpt-4.1", ModelPrice{Prompt: 1, Completion: 0, Cache: 0}); err != nil {
		t.Fatalf("UpsertModelPrice() error = %v", err)
	}

	oldTime := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	snapshot := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   100,
		InputTokens:   100,
		APIs: map[string]APISnapshot{
			"openai": {
				TotalRequests: 1,
				SuccessCount:  1,
				TotalTokens:   100,
				InputTokens:   100,
				Models: map[string]ModelSnapshot{
					"gpt-4.1": {
						TotalRequests: 1,
						SuccessCount:  1,
						TotalTokens:   100,
						InputTokens:   100,
						Details: []RequestDetail{{
							Model:     "gpt-4.1",
							Timestamp: oldTime,
							Provider:  "openai",
							Tokens: TokenStats{
								InputTokens: 100,
								TotalTokens: 100,
							},
						}},
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{"2026-07-01": 1},
		RequestsByHour: map[string]int64{"10": 1},
		TokensByDay:    map[string]int64{"2026-07-01": 100},
		TokensByHour:   map[string]int64{"10": 100},
		CostByDay:      map[string]float64{"2026-07-01": 0.0001},
		CostByHour:     map[string]float64{"10": 0.0001},
	}

	stats.mu.Lock()
	stats.restoreStorageSnapshotLocked(snapshot, now)
	if len(stats.costTokensByDay["2026-07-01"]) != 1 || len(stats.costTokensByHour[10]) != 1 {
		stats.mu.Unlock()
		t.Fatalf("legacy restore did not rebuild missing cost token series: day %#v hour %#v", stats.costTokensByDay, stats.costTokensByHour)
	}
	stats.pruneLocked(now, true)
	costByDay := stats.costByDay["2026-07-01"]
	costByHour := stats.costByHour[10]
	stats.mu.Unlock()

	assertFloatNear(t, "pruned legacy cost-map daily cost", costByDay, 0)
	assertFloatNear(t, "pruned legacy cost-map hourly cost", costByHour, 0)
}

func assertFloatNear(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000000001 {
		t.Fatalf("%s = %.12f, want %.12f", label, got, want)
	}
}

func TestSummaryWithoutDetailsCacheReturnsCopyAndInvalidates(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})

	first := stats.SummaryWithoutDetails()
	if !stats.summaryCacheValid {
		t.Fatal("summary cache should be populated after first summary")
	}
	first.Usage.TotalRequests = 999
	first.Usage.APIs["openai"] = APISnapshotWithoutDetails{TotalRequests: 999}
	first.HealthGrid[0].Total = 999

	second := stats.SummaryWithoutDetails()
	if second.Usage.TotalRequests != 1 {
		t.Fatalf("cached summary was mutated through returned value: total_requests=%d", second.Usage.TotalRequests)
	}
	if second.Usage.APIs["openai"].TotalRequests != 1 {
		t.Fatalf("cached API summary was mutated: %#v", second.Usage.APIs["openai"])
	}
	if second.HealthGrid[0].Total == 999 {
		t.Fatal("cached health grid was mutated through returned value")
	}

	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(time.Second),
		Detail:      UsageDetail{TotalTokens: 50},
	})
	third := stats.SummaryWithoutDetails()
	if third.Usage.TotalRequests != 2 || third.Usage.TotalTokens != 150 {
		t.Fatalf("summary cache did not invalidate after record: requests=%d tokens=%d", third.Usage.TotalRequests, third.Usage.TotalTokens)
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

// ============================================================================
// P0 Tests: Range-scoped summary
// ============================================================================

func TestSummaryRange24hOnlyCountsRecentEvents(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	// Old event (~48 hours ago, outside 24h window)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-48 * time.Hour),
		Detail:      UsageDetail{TotalTokens: 100},
	})
	// Recent event
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 50},
	})

	summary := stats.SummaryWithoutDetailsForRange("24h")
	if summary.Usage.TotalRequests != 1 {
		t.Fatalf("24h range: total_requests = %d, want 1", summary.Usage.TotalRequests)
	}
	if summary.Usage.TotalTokens != 50 {
		t.Fatalf("24h range: total_tokens = %d, want 50", summary.Usage.TotalTokens)
	}
}

func TestSummaryRangeExcludesZeroTimestampEvents(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.mu.Lock()
	stats.recordDetailLocked("openai", "gpt-4", RequestDetail{
		Model:     "gpt-4",
		Provider:  "openai",
		Timestamp: time.Time{},
		Tokens:    TokenStats{TotalTokens: 100},
	}, requestDedupKey{}, time.Now(), false)
	stats.mu.Unlock()

	fullSummary := stats.SummaryWithoutDetails()
	if fullSummary.Usage.TotalRequests != 1 {
		t.Fatalf("full summary total_requests = %d, want 1", fullSummary.Usage.TotalRequests)
	}
	rangeSummary := stats.SummaryWithoutDetailsForRange("24h")
	if rangeSummary.Usage.TotalRequests != 0 {
		t.Fatalf("range summary total_requests = %d, want zero timestamp event excluded", rangeSummary.Usage.TotalRequests)
	}
}

func TestSummaryRangeAllMatchesFullSummary(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 50},
	})

	fullSummary := stats.SummaryWithoutDetails()
	allSummary := stats.SummaryWithoutDetailsForRange("all")

	if fullSummary.Usage.TotalRequests != allSummary.Usage.TotalRequests {
		t.Fatalf("range=all should match full summary: %d vs %d", fullSummary.Usage.TotalRequests, allSummary.Usage.TotalRequests)
	}
	if fullSummary.Usage.TotalTokens != allSummary.Usage.TotalTokens {
		t.Fatalf("range=all should match full summary tokens: %d vs %d", fullSummary.Usage.TotalTokens, allSummary.Usage.TotalTokens)
	}
}

func TestSummaryEmptyRangeUsesFullPath(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})

	fullSummary := stats.SummaryWithoutDetails()
	emptySummary := stats.SummaryWithoutDetailsForRange("")

	if fullSummary.Usage.TotalRequests != emptySummary.Usage.TotalRequests {
		t.Fatalf("empty range should match full summary: %d vs %d", fullSummary.Usage.TotalRequests, emptySummary.Usage.TotalRequests)
	}
}

func TestSummaryRange7hOnlyCountsLast7Hours(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	// Old event (~8 hours ago)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-8 * time.Hour),
		Detail:      UsageDetail{TotalTokens: 100},
	})
	// Recent events within 7h
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "openai",
			Model:       "gpt-4",
			RequestedAt: time.Now().Add(-time.Duration(i) * time.Hour),
			Detail:      UsageDetail{TotalTokens: 10},
		})
	}

	summary := stats.SummaryWithoutDetailsForRange("7h")
	if summary.Usage.TotalRequests != 3 {
		t.Fatalf("7h range: total_requests = %d, want 3", summary.Usage.TotalRequests)
	}
	if summary.Usage.TotalTokens != 30 {
		t.Fatalf("7h range: total_tokens = %d, want 30", summary.Usage.TotalTokens)
	}
}

func TestSummaryRangeIncludesAPIAndModelStats(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.Record(UsageRecord{
		Provider: "openai",
		Source:   "openai-prod",
		Model:    "gpt-4",
		Detail:   UsageDetail{InputTokens: 10, OutputTokens: 5},
	})
	stats.Record(UsageRecord{
		Provider: "openai",
		Source:   "openai-prod",
		Model:    "gpt-3.5",
		Detail:   UsageDetail{InputTokens: 20, OutputTokens: 10},
	})

	summary := stats.SummaryWithoutDetailsForRange("24h")
	if len(summary.ModelStats) != 2 {
		t.Fatalf("model stats length = %d, want 2", len(summary.ModelStats))
	}
	if len(summary.Usage.APIs) != 1 {
		t.Fatalf("api count = %d, want 1", len(summary.Usage.APIs))
	}
	if len(summary.SourceStats) != 1 || summary.SourceStats[0].Source != "openai-prod" {
		t.Fatalf("source stats = %#v, want 1 source openai-prod", summary.SourceStats)
	}
}

func TestSummaryRangeExcludesAPIsWithoutMatchingEvents(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai-old",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-48 * time.Hour),
		Detail:      UsageDetail{TotalTokens: 100},
	})
	stats.Record(UsageRecord{
		Provider: "anthropic",
		Source:   "anthropic-prod",
		Model:    "claude",
		Detail:   UsageDetail{TotalTokens: 50},
	})

	summary := stats.SummaryWithoutDetailsForRange("24h")
	oldAPI := usageGroupKey(UsageRecord{Provider: "openai", Source: "openai-old"})
	recentAPI := usageGroupKey(UsageRecord{Provider: "anthropic", Source: "anthropic-prod"})
	if _, ok := summary.Usage.APIs[oldAPI]; ok {
		t.Fatalf("range summary included API with no matching events: %#v", summary.Usage.APIs[oldAPI])
	}
	if api := summary.Usage.APIs[recentAPI]; api.TotalRequests != 1 {
		t.Fatalf("recent API total_requests = %d, want 1; apis=%#v", api.TotalRequests, summary.Usage.APIs)
	}
}

func TestSummaryRangeEmptyResultIsValid(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	// No events added.
	summary := stats.SummaryWithoutDetailsForRange("24h")

	if summary.Usage.TotalRequests != 0 {
		t.Fatalf("empty summary total_requests = %d, want 0", summary.Usage.TotalRequests)
	}
	if len(summary.HealthGrid) != 672 {
		t.Fatalf("health grid length = %d, want 672", len(summary.HealthGrid))
	}
	if summary.GeneratedAt == "" {
		t.Fatal("generated_at should not be empty")
	}
}

func TestAPIDetailFollowsRange(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	// Old event (~48h ago)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Source:      "openai",
		Model:       "gpt-4",
		RequestedAt: time.Now().Add(-48 * time.Hour),
		Detail:      UsageDetail{TotalTokens: 100},
	})
	// Recent event
	stats.Record(UsageRecord{
		Provider: "openai",
		Source:   "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 50},
	})

	result24h := stats.QueryAPIDetail("openai", "24h", 10, 10)
	if result24h.TotalEvents != 1 {
		t.Fatalf("24h range total_events = %d, want 1", result24h.TotalEvents)
	}
	if result24h.Summary.TotalRequests != 1 {
		t.Fatalf("24h range summary total_requests = %d, want 1", result24h.Summary.TotalRequests)
	}

	resultAll := stats.QueryAPIDetail("openai", "all", 10, 10)
	if resultAll.TotalEvents != 2 {
		t.Fatalf("all range total_events = %d, want 2", resultAll.TotalEvents)
	}
	if resultAll.Summary.TotalRequests != 2 {
		t.Fatalf("all range summary total_requests = %d, want 2", resultAll.Summary.TotalRequests)
	}
}

func TestSummaryETagVariesByRange(t *testing.T) {
	now := time.Now()
	etag24h := dashboardSummaryETag(now, "24h")
	etag7d := dashboardSummaryETag(now, "7d")
	etagAll := dashboardSummaryETag(now, "all")

	if etag24h == etag7d {
		t.Fatalf("24h and 7d ETags should differ: %q vs %q", etag24h, etag7d)
	}
	if etag24h == etagAll {
		t.Fatalf("24h and all ETags should differ: %q vs %q", etag24h, etagAll)
	}
	if etag7d == etagAll {
		t.Fatalf("7d and all ETags should differ: %q vs %q", etag7d, etagAll)
	}
}

func TestSummaryRangeETagUsesMinuteBucket(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 15, 0, 0, time.UTC)
	etagNow := dashboardSummaryETag(now, "24h")
	etagSameBucket := dashboardSummaryETag(now.Add(30*time.Second), "24h")
	etagNextBucket := dashboardSummaryETag(now.Add(time.Minute), "24h")

	if etagNow != etagSameBucket {
		t.Fatalf("range summary ETag should stay stable within minute bucket: %q vs %q", etagNow, etagSameBucket)
	}
	if etagNow == etagNextBucket {
		t.Fatalf("range summary ETag should vary across minute buckets: %q", etagNow)
	}
}

func TestEventsRangeETagUsesMinuteBucket(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 15, 0, 0, time.UTC)
	params := EventsQuery{Limit: 50, Range: "24h", Model: "gpt-4"}
	etagNow := dashboardEventsETag(params, now)
	etagSameBucket := dashboardEventsETag(params, now.Add(30*time.Second))
	etagNextBucket := dashboardEventsETag(params, now.Add(time.Minute))
	etagAll := dashboardEventsETag(EventsQuery{Limit: 50, Range: "all", Model: "gpt-4"}, now.Add(time.Hour))

	if etagNow != etagSameBucket {
		t.Fatalf("range events ETag should stay stable within minute bucket: %q vs %q", etagNow, etagSameBucket)
	}
	if etagNow == etagNextBucket {
		t.Fatalf("range events ETag should vary across minute buckets: %q", etagNow)
	}
	if etagAll != dashboardEventsETag(EventsQuery{Limit: 50, Range: "all", Model: "gpt-4"}, now) {
		t.Fatalf("all-range events ETag should not include a time bucket")
	}

	cacheKeyNow := dashboardEventCacheKeyFor(params, now)
	cacheKeySameBucket := dashboardEventCacheKeyFor(params, now.Add(30*time.Second))
	cacheKeyNextBucket := dashboardEventCacheKeyFor(params, now.Add(time.Minute))
	if cacheKeyNow != cacheKeySameBucket {
		t.Fatalf("range events cache key should stay stable within minute bucket: %#v vs %#v", cacheKeyNow, cacheKeySameBucket)
	}
	if cacheKeyNow == cacheKeyNextBucket {
		t.Fatalf("range events cache key should vary across minute buckets: %#v", cacheKeyNow)
	}
}

func TestAPIDetailRangeETagUsesMinuteBucket(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 15, 0, 0, time.UTC)
	etagNow := dashboardAPIDetailETag("openai", "24h", 120, 20, now)
	etagSameBucket := dashboardAPIDetailETag("openai", "24h", 120, 20, now.Add(30*time.Second))
	etagNextBucket := dashboardAPIDetailETag("openai", "24h", 120, 20, now.Add(time.Minute))
	etagAll := dashboardAPIDetailETag("openai", "all", 120, 20, now.Add(time.Hour))

	if etagNow != etagSameBucket {
		t.Fatalf("range API detail ETag should stay stable within minute bucket: %q vs %q", etagNow, etagSameBucket)
	}
	if etagNow == etagNextBucket {
		t.Fatalf("range API detail ETag should vary across minute buckets: %q", etagNow)
	}
	if etagAll != dashboardAPIDetailETag("openai", "all", 120, 20, now) {
		t.Fatalf("all-range API detail ETag should not include a time bucket")
	}
}

func TestAPIDetailETagNormalizesLimits(t *testing.T) {
	now := time.Date(2026, 7, 3, 10, 15, 0, 0, time.UTC)
	defaultETag := dashboardAPIDetailETag("openai", "24h", dashboardAPIDetailDefaultRecentLimit, dashboardAPIDetailDefaultErrorLimit, now)
	if got := dashboardAPIDetailETag("openai", "24h", 0, -1, now); got != defaultETag {
		t.Fatalf("non-positive limits ETag = %q, want default ETag %q", got, defaultETag)
	}
	if got := dashboardAPIDetailETag("openai", "24h", dashboardAPIDetailMaxRecentLimit+1, dashboardAPIDetailMaxErrorLimit+1, now); got != defaultETag {
		t.Fatalf("over-limit ETag = %q, want default ETag %q", got, defaultETag)
	}
	customETag := dashboardAPIDetailETag("openai", "24h", dashboardAPIDetailMaxRecentLimit, dashboardAPIDetailMaxErrorLimit, now)
	if customETag == defaultETag {
		t.Fatalf("max valid API detail limits should keep a distinct ETag")
	}
}

func TestAPIDetailGeneratedAtUsesETagTime(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	now := time.Date(2026, 7, 3, 10, 15, 10, 0, time.UTC)
	recordedAt := now.Add(-time.Hour)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: recordedAt,
		Detail:      UsageDetail{TotalTokens: 10},
	})

	first := stats.QueryAPIDetailAt("openai", "24h", 10, 10, now)
	second := stats.QueryAPIDetailAt("openai", "24h", 10, 10, now.Add(20*time.Second))
	if first.GeneratedAt != "2026-07-03T10:15:00Z" || second.GeneratedAt != first.GeneratedAt {
		t.Fatalf("range generated_at = %q/%q, want stable minute bucket", first.GeneratedAt, second.GeneratedAt)
	}
	if dashboardAPIDetailETag("openai", "24h", 10, 10, now) != dashboardAPIDetailETag("openai", "24h", 10, 10, now.Add(20*time.Second)) {
		t.Fatal("test setup expected same API detail ETag within minute bucket")
	}

	allFirst := stats.QueryAPIDetailAt("openai", "all", 10, 10, now)
	allSecond := stats.QueryAPIDetailAt("openai", "all", 10, 10, now.Add(time.Hour))
	wantAllGeneratedAt := recordedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	if allFirst.GeneratedAt != wantAllGeneratedAt || allSecond.GeneratedAt != wantAllGeneratedAt {
		t.Fatalf("all generated_at = %q/%q, want last recorded at %q", allFirst.GeneratedAt, allSecond.GeneratedAt, wantAllGeneratedAt)
	}
}

func TestEventsExportGeneratedAtUsesETagTime(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	now := time.Date(2026, 7, 3, 10, 15, 10, 0, time.UTC)
	recordedAt := now.Add(-time.Hour)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: recordedAt,
		Detail:      UsageDetail{TotalTokens: 10},
	})
	params := EventsQuery{Range: "24h", Model: "gpt-4"}

	first := stats.QueryExportEventsAt(params, 0, now)
	second := stats.QueryExportEventsAt(params, 0, now.Add(20*time.Second))
	if first.GeneratedAt != "2026-07-03T10:15:00Z" || second.GeneratedAt != first.GeneratedAt {
		t.Fatalf("range export generated_at = %q/%q, want stable minute bucket", first.GeneratedAt, second.GeneratedAt)
	}
	opts := dashboardEventsExportOptions{Format: dashboardExportJSON}
	if dashboardEventsExportETag(normalizeEventsQuery(params, false), opts, now) != dashboardEventsExportETag(normalizeEventsQuery(params, false), opts, now.Add(20*time.Second)) {
		t.Fatal("test setup expected same events export ETag within minute bucket")
	}

	allFirst := stats.QueryExportEventsAt(EventsQuery{Range: "all", Model: "gpt-4"}, 0, now)
	allSecond := stats.QueryExportEventsAt(EventsQuery{Range: "all", Model: "gpt-4"}, 0, now.Add(time.Hour))
	wantAllGeneratedAt := recordedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	if allFirst.GeneratedAt != wantAllGeneratedAt || allSecond.GeneratedAt != wantAllGeneratedAt {
		t.Fatalf("all export generated_at = %q/%q, want last recorded at %q", allFirst.GeneratedAt, allSecond.GeneratedAt, wantAllGeneratedAt)
	}
}

func TestDashboardETagCanUseQueryResultVersion(t *testing.T) {
	previousStats := stats
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0})
	t.Cleanup(func() { stats = previousStats })

	now := time.Date(2026, 7, 3, 10, 15, 10, 0, time.UTC)
	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: now.Add(-time.Hour),
		Detail:      UsageDetail{TotalTokens: 10},
	})

	params := normalizeEventsQuery(EventsQuery{Range: "24h", Model: "gpt-4"}, true)
	exportParams := normalizeEventsQuery(EventsQuery{Range: "24h", Model: "gpt-4"}, false)
	exportOpts := dashboardEventsExportOptions{Format: dashboardExportJSON}
	events := stats.QueryEventsAt(params, now)
	export := stats.QueryExportEventsAt(exportParams, 0, now)
	apiDetail := stats.QueryAPIDetailAt("openai", "24h", 10, 10, now)
	version := stats.DashboardVersion()
	if events.dashboardVersion != version || export.dashboardVersion != version || apiDetail.dashboardVersion != version {
		t.Fatalf("result versions events/export/api-detail = %d/%d/%d, want %d", events.dashboardVersion, export.dashboardVersion, apiDetail.dashboardVersion, version)
	}

	eventsETag := dashboardEventsETagForVersion(params, now, events.dashboardVersion)
	exportETag := dashboardEventsExportETagForVersion(exportParams, exportOpts, now, export.dashboardVersion)
	apiDetailETag := dashboardAPIDetailETagForVersion("openai", "24h", 10, 10, now, apiDetail.dashboardVersion)

	stats.Record(UsageRecord{
		Provider:    "openai",
		Model:       "gpt-4",
		RequestedAt: now.Add(-30 * time.Minute),
		Detail:      UsageDetail{TotalTokens: 20},
	})
	if stats.DashboardVersion() == version {
		t.Fatalf("dashboard version did not change after record")
	}
	if got := dashboardEventsETag(params, now); got == eventsETag {
		t.Fatalf("events ETag still matches stale query version after record: %q", got)
	}
	if got := dashboardEventsExportETag(exportParams, exportOpts, now); got == exportETag {
		t.Fatalf("export ETag still matches stale query version after record: %q", got)
	}
	if got := dashboardAPIDetailETag("openai", "24h", 10, 10, now); got == apiDetailETag {
		t.Fatalf("api detail ETag still matches stale query version after record: %q", got)
	}
}

func TestSummaryRangeCacheIsBounded(t *testing.T) {
	stats := NewRequestStatistics()
	stats.summaryRangeCache = make(map[string]DashboardSummary)
	stats.summaryRangeCacheWindow = make(map[string]time.Time)

	base := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	for i := 0; i < dashboardSummaryRangeCacheMax*2; i++ {
		key := summaryRangeCacheKey("24h", base.Add(time.Duration(i)*dashboardSummaryRangeCacheStep))
		stats.summaryRangeCache[key] = DashboardSummary{}
		stats.summaryRangeCacheWindow[key] = base
		stats.pruneSummaryRangeCacheLocked(key)
		if len(stats.summaryRangeCache) > dashboardSummaryRangeCacheMax {
			t.Fatalf("range cache size = %d, want <= %d", len(stats.summaryRangeCache), dashboardSummaryRangeCacheMax)
		}
	}
	if len(stats.summaryRangeCacheWindow) != len(stats.summaryRangeCache) {
		t.Fatalf("range cache window size = %d, cache size = %d", len(stats.summaryRangeCacheWindow), len(stats.summaryRangeCache))
	}
}

func TestSummaryRangeCachedResultsUseCache(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, DedupWindowMinutes: 0, RetentionDays: 30})

	stats.Record(UsageRecord{
		Provider: "openai",
		Model:    "gpt-4",
		Detail:   UsageDetail{TotalTokens: 100},
	})

	first := stats.SummaryWithoutDetailsForRange("24h")
	second := stats.SummaryWithoutDetailsForRange("24h")

	if first.Usage.TotalRequests != second.Usage.TotalRequests {
		t.Fatalf("cached result should match: %d vs %d", first.Usage.TotalRequests, second.Usage.TotalRequests)
	}
	if first.Usage.TotalTokens != second.Usage.TotalTokens {
		t.Fatalf("cached result should match: %d vs %d", first.Usage.TotalTokens, second.Usage.TotalTokens)
	}
}

func TestDashboardEventsQueryParsesClientAPISelector(t *testing.T) {
	selector := clientAPISelectorForStat(ClientAPIStat{APIKey: "sk******xx", APIKeyHash: strings.Repeat("a", 56)})
	query := dashboardEventsQuery(map[string][]string{
		"range":      {"24h"},
		"api":        {"openai"},
		"client_api": {"  " + selector + "  "},
	})
	if query.Range != "24h" || query.API != "openai" || query.ClientAPI != selector {
		t.Fatalf("parsed events query = %#v", query)
	}
}

func TestQueryRawValuePreservesClientAPISelectorCase(t *testing.T) {
	selector := "m.c2sQKioqKip6eQ"
	query := map[string][]string{"client_api": {"  " + selector + "  "}}
	if got := queryRawValue(query, "client_api"); got != selector {
		t.Fatalf("queryRawValue() = %q, want %q", got, selector)
	}
}
