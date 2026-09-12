package main

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestRangeTimeSeriesPreservesLocalSparseZeroAndSaturatedBuckets(t *testing.T) {
	s := NewRequestStatistics()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	s.maxDetailsPerModel = 1 // exercise both visible and archived records
	records := []struct {
		at     string
		tokens int64
	}{
		{"2026-09-12T00:00:00+08:00", 0},
		{"2026-09-11T16:00:00Z", math.MaxInt64}, // same instant, different local bucket
		{"2026-09-11T16:01:00Z", 5},
		{"2026-09-11T12:00:00Z", 3},   // inclusive cutoff
		{"2026-09-11T11:59:59Z", 100}, // excluded cutoff
	}
	for _, record := range records {
		at, err := time.Parse(time.RFC3339, record.at)
		if err != nil {
			t.Fatal(err)
		}
		s.recordDetailLocked("upstream", "model", RequestDetail{Model: "model", Timestamp: at, Tokens: TokenStats{InputTokens: record.tokens, TotalTokens: record.tokens}}, requestDedupKey{}, now, false)
	}
	got := s.SummaryWithoutDetailsForRangeAt("24h", now).Usage
	checks := []struct {
		name      string
		got, want any
	}{
		{"requests/day", got.RequestsByDay, map[string]int64{"2026-09-11": 3, "2026-09-12": 1}},
		{"tokens/day", got.TokensByDay, map[string]int64{"2026-09-11": math.MaxInt64, "2026-09-12": 0}},
		{"cost/day", got.CostByDay, map[string]float64{"2026-09-11": 0, "2026-09-12": 0}},
		{"requests/hour", got.RequestsByHour, map[string]int64{"00": 1, "12": 1, "16": 2}},
		{"tokens/hour", got.TokensByHour, map[string]int64{"00": 0, "12": 3, "16": math.MaxInt64}},
		{"cost/hour", got.CostByHour, map[string]float64{"00": 0, "12": 0, "16": 0}},
	}
	for _, check := range checks {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("%s: got %v want %v", check.name, check.got, check.want)
		}
	}
}
