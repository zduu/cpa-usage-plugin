package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestRestoredEventOrderSurvivesPartialIndexesAndTrim(t *testing.T) {
	for _, warmIndex := range []bool{false, true} {
		t.Run(fmt.Sprintf("warm=%t", warmIndex), func(t *testing.T) {
			now := time.Now()
			original := NewRequestStatistics()
			original.maxDetailsPerModel = 4
			record := func(s *RequestStatistics, source string, at time.Time) {
				s.Record(UsageRecord{Provider: "test", BaseURL: "https://example.com", Model: "model", Source: source, RequestedAt: at})
			}
			for _, source := range []string{"A", "B", "C", "D"} {
				record(original, source, now)
			}
			s := NewRequestStatistics()
			s.maxDetailsPerModel = 4
			s.restoreStorageSnapshotLocked(original.Snapshot(), now)
			if warmIndex {
				if got := s.QueryEventsAt(EventsQuery{Source: "C", Limit: 10}, now); got.Total != 1 {
					t.Fatalf("source index total = %d, want 1", got.Total)
				}
			}
			// A late arrival must not change identities before the first full
			// index build, even if it is immediately archived.
			record(s, "late", now.Add(-time.Minute))
			for _, source := range []string{"E", "F"} {
				record(s, source, now)
			}
			for _, query := range []EventsQuery{{Limit: 4}, {Model: "model", Limit: 4}, {Limit: 2, Offset: 1}} {
				got := s.QueryEventsAt(query, now)
				var sources []string
				for _, event := range got.Events {
					sources = append(sources, event.Source)
				}
				want := []string{"C", "D", "E", "F"}[query.Offset : query.Offset+query.Limit]
				if got.Total != 4 || !reflect.DeepEqual(sources, want) {
					t.Fatalf("query=%+v: total=%d sources=%v, want total=4 sources=%v", query, got.Total, sources, want)
				}
			}
		})
	}
}

func TestEventPendingUpdatesRespectIndexByteBudget(t *testing.T) {
	s := NewRequestStatistics()
	// Retained capacity counts even after most indexed records were trimmed.
	s.eventIndex = make([]dashboardEventDetail, 0, dashboardEventIndexByteBudget/int(unsafe.Sizeof(dashboardEventDetail{})))
	s.eventIndexVersion = s.summaryVersion
	s.Record(UsageRecord{Provider: "test", Model: "model", RequestedAt: time.Now()})
	if got := s.RuntimeStatus().EventIndexBytes; got > dashboardEventIndexByteBudget {
		t.Fatalf("pending update exceeded index budget: %d > %d", got, dashboardEventIndexByteBudget)
	}
	if got := s.QueryEvents(EventsQuery{Limit: 10}); got.Total != 1 || len(got.Events) != 1 {
		t.Fatalf("rebuilding after budget eviction lost the new record: %+v", got)
	}
}

func TestEventCacheBudgetIncludesQueryKeysAndMetadata(t *testing.T) {
	large := strings.Repeat("x", dashboardEventCacheByteBudget)
	for _, tc := range []struct {
		name   string
		key    dashboardEventCacheKey
		result EventsResult
	}{
		{name: "query key", key: dashboardEventCacheKey{source: large}},
		{name: "thinking", result: EventsResult{Events: []RequestDetail{{Thinking: UsageThinking{Intensity: large}}}}},
		{name: "correlation", result: EventsResult{Events: []RequestDetail{{Correlation: &ProtocolCorrelationMeta{InputMode: large}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewRequestStatistics()
			s.cacheDashboardEventsLocked(tc.key, tc.result)
			if len(s.eventQueryCache) != 0 {
				t.Fatalf("oversized %s was cached; reported only %d bytes", tc.name, s.eventQueryCacheBytes)
			}
		})
	}
}

func TestEventIndexRebuildAfterPendingOverflow(t *testing.T) {
	s := NewRequestStatistics()
	s.maxDetailsPerModel = 8
	now := time.Now()
	s.QueryEventsAt(EventsQuery{Model: "model", Limit: 10}, now)
	for i := 0; i < dashboardEventPendingMax+2; i++ {
		s.Record(UsageRecord{Provider: "test", Model: "model", RequestedAt: now, Detail: UsageDetail{InputTokens: int64(i + 1)}})
	}
	got := s.QueryEventsAt(EventsQuery{Model: "model", Limit: 10}, now)
	if got.Total != 8 || len(got.Events) != 8 {
		t.Fatalf("rebuild lost retained records: total=%d events=%d", got.Total, len(got.Events))
	}
	for i, event := range got.Events {
		if want := int64(dashboardEventPendingMax + 2 - 8 + i + 1); event.Tokens.InputTokens != want {
			t.Fatalf("event %d tokens=%d, want %d", i, event.Tokens.InputTokens, want)
		}
	}
}

func TestMergedRestoredModelsKeepTieOrder(t *testing.T) {
	now := time.Now()
	a, b := &modelStats{}, &modelStats{}
	for i := 0; i < 20; i++ {
		a.appendDetail(RequestDetail{Timestamp: now, Source: fmt.Sprintf("a-%d", i)}, false)
		b.appendDetail(RequestDetail{Timestamp: now, Source: fmt.Sprintf("b-%d", i)}, false)
	}
	mergeModelStats(a, b)
	api := &apiStats{Models: map[string]*modelStats{"model": a}}
	got := buildDashboardEventIndexForAPI("api", api)
	for i, event := range got {
		if want := a.Details[i].Source; event.requestDetail().Source != want {
			t.Fatalf("merged event %d source=%q, want %q", i, event.requestDetail().Source, want)
		}
	}
}

func TestEventQueriesDoNotShareMutableRecords(t *testing.T) {
	s := NewRequestStatistics()
	s.logResponseHeaders = parseHeaderWhitelist("x-test")
	now := time.Now()
	s.Record(UsageRecord{Provider: "test", Model: "model", RequestedAt: now, ResponseHeaders: map[string][]string{"x-test": {"original"}}})
	queries := []EventsQuery{{Limit: 10}, {Limit: 10, Model: "model"}, {Limit: 10, Range: "24h"}}
	for _, query := range queries {
		result := s.QueryEventsAt(query, now)
		if len(result.Events) != 1 {
			t.Fatalf("missing event: %+v", result)
		}
		for key, values := range result.Events[0].Headers {
			values[0] = "mutated"
			delete(result.Events[0].Headers, key)
		}
		if result.Events[0].CostUSD != nil {
			*result.Events[0].CostUSD = 123
		}
		clearBenchmarkEventCache(s)
		next := s.QueryEventsAt(query, now)
		if got := next.Events[0].Headers["x-test"]; !reflect.DeepEqual(got, []string{"original"}) {
			t.Fatalf("caller mutation changed stored headers: %v", next.Events[0].Headers)
		}
	}
}

// The oracle reads the authoritative visible arrays, independently of the
// query cache and index. Ties within a model retain insertion order; cross-model
// ties must be deterministic so rebuilding an index cannot shift pagination.
func expectedVisibleEvents(s *RequestStatistics, query EventsQuery, now time.Time) []RequestDetail {
	snapshot := s.Snapshot()
	var events []RequestDetail
	for api, data := range snapshot.APIs {
		if query.API != "" && query.API != api {
			continue
		}
		for _, model := range data.Models {
			for _, detail := range model.Details {
				if query.Model != "" && detail.Model != query.Model || query.Source != "" && detail.Source != query.Source || query.AuthIndex != "" && detail.AuthIndex != query.AuthIndex {
					continue
				}
				if query.Range == "24h" && detail.Timestamp.Before(now.Add(-24*time.Hour)) || query.Range == "7d" && detail.Timestamp.Before(now.Add(-7*24*time.Hour)) {
					continue
				}
				detail.UpstreamAPI = api
				events = append(events, detail)
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if !a.Timestamp.Equal(b.Timestamp) {
			return a.Timestamp.After(b.Timestamp)
		}
		if a.UpstreamAPI != b.UpstreamAPI {
			return a.UpstreamAPI < b.UpstreamAPI
		}
		return a.Model < b.Model
	})
	return events
}

func TestEventIndexesMatchVisibleHistoryAcrossMutations(t *testing.T) {
	s := NewRequestStatistics()
	s.maxDetailsPerModel, s.retention = 8, 48*time.Hour
	s.priceStoragePath = filepath.Join(t.TempDir(), "prices.json")
	now := time.Now().Truncate(time.Second)
	rng := rand.New(rand.NewPCG(20260909, 17))
	queries := []EventsQuery{{Limit: 5}, {Limit: 5, Offset: 5}, {Limit: 5, Model: "model-0"}, {Limit: 5, Source: "source-0"}, {Limit: 5, AuthIndex: "auth-0"}, {Limit: 5, Range: "24h"}, {Limit: 5, API: "test · https://example.com", Source: "source-0", AuthIndex: "auth-0"}}
	for step := 0; step < 250; step++ {
		record := UsageRecord{Provider: "test", BaseURL: "https://example.com", Model: fmt.Sprintf("model-%d", rng.IntN(3)), Source: fmt.Sprintf("source-%d", rng.IntN(2)), AuthIndex: fmt.Sprintf("auth-%d", rng.IntN(2)),
			RequestedAt: now.Add(-time.Duration(rng.IntN(72)) * time.Hour), Detail: UsageDetail{InputTokens: int64(step + 1)}, Latency: time.Duration(step+1) * time.Millisecond}
		s.Record(record)
		if step%13 == 0 {
			s.Record(record) // identical real requests are not deduplicated
		}
		if step%19 == 0 {
			s.EnrichRecordedUsage(record, UsageRecord{Endpoint: "/v1/responses", Stream: true})
		}
		if step%31 == 0 {
			if _, err := s.UpsertModelPrice("model-0", ModelPrice{Prompt: float64(step + 1)}); err != nil {
				t.Fatal(err)
			}
		}
		for _, query := range queries {
			want := expectedVisibleEvents(s, query, now)
			got := s.QueryEventsAt(query, now)
			if got.Total != len(want) {
				t.Fatalf("step=%d query=%+v: total %d != %d", step, query, got.Total, len(want))
			}
			start := min(query.Offset, len(want))
			end := min(start+query.Limit, len(want))
			want = want[start:end]
			for i := range got.Events {
				got.Events[i].CostUSD = nil
			}
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(got.Events)
			if len(want) != len(got.Events) || len(want) > 0 && string(wantJSON) != string(gotJSON) {
				t.Fatalf("step=%d query=%+v:\nwant %s\ngot  %s", step, query, wantJSON, gotJSON)
			}
		}
	}
}
