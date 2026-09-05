package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
