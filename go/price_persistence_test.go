package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveredPriceFileRepricesCachedUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{"prices":`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewRequestStatistics()
	s.Configure(runtimeConfig{PriceStoragePath: path})
	now := time.Now()
	s.Record(UsageRecord{Provider: "openai", Model: "model", RequestedAt: now, Detail: UsageDetail{InputTokens: 1000000}})
	if got := s.SummaryWithoutDetailsAt(now).Usage.TotalCost; got != 0 {
		t.Fatalf("cost before price recovery = %g", got)
	}
	s.SummaryWithoutDetailsForRangeAt("24h", now)
	version := s.DashboardVersion()
	if err := os.WriteFile(path, []byte(`{"prices":{"model":{"prompt":3}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.ModelPrices().Prices["model"].Prompt; got != 3 {
		t.Fatalf("recovered price = %g, want 3", got)
	}
	if got := s.SummaryWithoutDetailsAt(now).Usage.TotalCost; got != 3 {
		t.Errorf("cached total after price recovery = %g, want 3", got)
	}
	if got := s.SummaryWithoutDetailsForRangeAt("24h", now).Usage.TotalCost; got != 3 {
		t.Errorf("cached range cost after price recovery = %g, want 3", got)
	}
	if s.DashboardVersion() == version {
		t.Error("price recovery did not invalidate conditional responses")
	}
}

func TestCoalescedLegacyClientPreservesTimeBasedCosts(t *testing.T) {
	s := NewRequestStatistics()
	s.Configure(runtimeConfig{MaxDetailsPerModel: 1, RetentionDays: 30, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})
	defer s.Close()
	prompt := 5.0
	if _, err := s.UpsertModelPrice("model", ModelPrice{Prompt: 2, TimeRules: []ModelPriceRule{{Name: "early", Start: "00:00", End: "01:00", Prompt: &prompt}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	legacy := RequestDetail{Model: "model", Provider: "openai", APIKey: "sk******xx", Timestamp: now.Add(-2 * time.Minute), Tokens: TokenStats{InputTokens: 1000000, TotalTokens: 1000000}}
	result := s.MergeSnapshot(StatisticsSnapshot{APIs: map[string]APISnapshot{
		"openai": {Models: map[string]ModelSnapshot{"model": {Details: []RequestDetail{legacy}}}},
	}})
	if result.Added != 1 {
		t.Fatalf("imported legacy row: %+v", result)
	}
	s.Record(UsageRecord{Provider: "openai", Model: "model", APIKey: "sk-client-0000xx", RequestedAt: now.Add(-time.Minute), Detail: UsageDetail{InputTokens: 1000000}})
	for _, rangeKey := range []string{"all", "24h"} {
		summary := s.SummaryWithoutDetailsForRangeAt(rangeKey, now)
		if len(summary.ClientAPIStats) != 1 || summary.Usage.TotalRequests != 2 || summary.Usage.TotalCost <= 0 {
			t.Fatalf("%s summary: %+v", rangeKey, summary)
		}
		client := summary.ClientAPIStats[0]
		if client.EstimatedCost != summary.Usage.TotalCost || len(client.Models) != 1 || client.Models[0].EstimatedCost != summary.Usage.TotalCost {
			t.Errorf("%s coalesced client lost part of the %g cost: %+v", rangeKey, summary.Usage.TotalCost, client)
		}
		filtered := s.SummaryWithoutDetailsForRangeAndClientAPIAt(rangeKey, client.Selector, now)
		if filtered.Usage.TotalCost != summary.Usage.TotalCost {
			t.Errorf("%s selected cost = %g, want %g", rangeKey, filtered.Usage.TotalCost, summary.Usage.TotalCost)
		}
	}
}

func TestModelPricesUseStableDataPathAcrossReload(t *testing.T) {
	t.Chdir(t.TempDir())

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{})
	if _, err := stats.UpsertModelPrice("gpt-4.1", ModelPrice{Prompt: 2, Completion: 8, Cache: 0.5}); err != nil {
		t.Fatalf("save model price: %v", err)
	}
	stats.Close()

	stablePath := filepath.Join("data", "usage-statistics-prices.json")
	if _, err := os.Stat(stablePath); err != nil {
		t.Fatalf("stable price file was not created: %v", err)
	}

	reloaded := NewRequestStatistics()
	reloaded.Configure(runtimeConfig{})
	t.Cleanup(func() { reloaded.Close() })
	got := reloaded.ModelPrices().Prices["gpt-4.1"]
	if got.Prompt != 2 || got.Completion != 8 || got.Cache != 0.5 {
		t.Fatalf("reloaded model price = %#v", got)
	}
}

func TestModelPricesMigrateLegacyDefaultPath(t *testing.T) {
	t.Chdir(t.TempDir())
	legacy := map[string]interface{}{
		"updated_at": "2026-09-05T00:00:00Z",
		"prices": map[string]ModelPrice{
			"claude-sonnet": {Prompt: 3, Completion: 15, Cache: 1.5},
		},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy price file: %v", err)
	}
	if err := os.WriteFile(legacyPriceStoragePath, raw, 0o600); err != nil {
		t.Fatalf("write legacy price file: %v", err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{})
	t.Cleanup(func() { stats.Close() })
	got := stats.ModelPrices().Prices["claude-sonnet"]
	if got.Prompt != 3 || got.Completion != 15 || got.Cache != 1.5 {
		t.Fatalf("migrated model price = %#v", got)
	}
	if _, err := os.Stat(defaultPriceStoragePath); err != nil {
		t.Fatalf("stable migrated price file was not created: %v", err)
	}
}

func TestModelPricesPreserveExplicitLegacyPath(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(map[bool]string{false: "relative", true: "absolute"}[absolute], func(t *testing.T) {
			t.Chdir(t.TempDir())
			path := legacyPriceStoragePath
			if absolute {
				var err error
				path, err = filepath.Abs(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte(`{"prices":{"model":{"prompt":1}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			s := NewRequestStatistics()
			s.Configure(runtimeConfig{PriceStoragePath: path})
			t.Cleanup(func() { s.Close() })
			if got := s.ModelPrices().Prices["model"].Prompt; got != 1 {
				t.Fatalf("loaded price = %v, want 1", got)
			}
			if _, err := s.UpsertModelPrice("model", ModelPrice{Prompt: 2}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var persisted struct {
				Prices map[string]ModelPrice `json:"prices"`
			}
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			if got := persisted.Prices["model"].Prompt; got != 2 {
				t.Fatalf("price in configured file = %v, want 2", got)
			}
			if _, err := os.Stat(defaultPriceStoragePath); !os.IsNotExist(err) {
				t.Fatalf("explicit legacy path unexpectedly created default price file: %v", err)
			}
		})
	}
}
