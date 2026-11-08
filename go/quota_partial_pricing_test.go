package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestQuotaMissingImagePriceKeepsAmountsAndRepricesBothWindows(t *testing.T) {
	for _, onlyUnpriced := range []bool{false, true} {
		for _, used := range []float64{.4, 1} {
			t.Run(fmt.Sprintf("only-unpriced=%t/used=%g", onlyUnpriced, used), func(t *testing.T) {
				s := NewRequestStatistics()
				defer s.Close()
				s.Configure(runtimeConfig{PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})
				now := time.Now().UTC().Truncate(time.Second)
				end := now.Add(time.Hour)
				r := quotaTestRecord("image", "gpt-image-2.5", now.Add(-time.Minute), 1_000_000)
				s.Record(r)
				want := 0.0
				if !onlyUnpriced {
					if _, err := s.UpsertModelPrice("priced", ModelPrice{Prompt: 10}); err != nil {
						t.Fatal(err)
					}
					s.Record(quotaTestRecord("priced", "priced", now.Add(-time.Minute), 1_000_000))
					want = 10
				}
				for _, seconds := range []int64{18000, 604800} {
					slot := "5h"
					if seconds == 604800 {
						slot = "7d"
					}
					s.mu.Lock()
					s.applyQuotaObservationLocked(quotaObservation{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID,
						Group: "shared", Slot: slot, Seconds: seconds, Reset: end, ObservedAt: now, Used: used})
					s.mu.Unlock()
				}
				check := func(priced bool, cost float64) {
					t.Helper()
					response := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now)
					if len(response.QuotaCycles) != 1 || len(response.QuotaCycles[0].Groups) != 2 {
						t.Fatalf("missing 5h or weekly window: %+v", response.QuotaCycles)
					}
					for _, group := range response.QuotaCycles[0].Groups {
						cycle := group.Current
						quotaModelAssert(t, cycle.Summary.CostUSD, cost)
						if used == 1 {
							quotaModelAssert(t, cycle.ActualTotalUSD, cost)
						} else {
							quotaModelAssert(t, cycle.EstimatedTotalUSD, cost/used)
						}
						quotaModelAssert(t, cycle.EstimatedRemainingUSD, cost/used-cost)
						if (len(cycle.UnpricedModels) == 0) != priced {
							t.Fatalf("wrong price completeness: %v", cycle.UnpricedModels)
						}
						for _, row := range cycle.ModelStats {
							if row.Model == r.Model && (row.CostUSD != nil) != priced {
								t.Fatal("unpriced model was mistaken for a free model")
							}
						}
						// Ensure the subtotal survives the nullable JSON DTO fields.
						raw, err := json.Marshal(cycle)
						if err != nil {
							t.Fatal(err)
						}
						var wire quotaCurrentDTO
						if err := json.Unmarshal(raw, &wire); err != nil {
							t.Fatal(err)
						}
						quotaModelAssert(t, wire.Summary.CostUSD, cost)
					}
				}
				check(false, want)
				if _, err := s.UpsertModelPrice(r.Model, ModelPrice{Prompt: 5}); err != nil {
					t.Fatal(err)
				}
				check(true, want+5)
				if _, err := s.DeleteModelPrice(r.Model); err != nil {
					t.Fatal(err)
				}
				check(false, want)
			})
		}
	}
}
