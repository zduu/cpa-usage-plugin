package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNativeUsageEndpointReachesDashboard(t *testing.T) {
	previous := stats
	stats = NewRequestStatistics()
	t.Cleanup(func() { stats.Close(); stats = previous })
	for _, body := range []string{
		`{"Provider":"test","Model":"model","Endpoint":"/v1/messages","Stream":true,"ServiceTier":"priority","Detail":{"InputTokens":3,"OutputTokens":2}}`,
		`{"provider":"test","model":"model","endpoint":"/v1/responses","stream":false,"service_tier":"default","detail":{"input_tokens":3,"output_tokens":2}}`,
	} {
		if _, err := handleMethod("usage.handle", []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	result := stats.QueryAPIDetail("test", "all", 10, 10)
	if len(result.RecentEvents) != 2 {
		t.Fatalf("events = %d", len(result.RecentEvents))
	}
	tiers := map[string]string{"/v1/messages": "priority", "/v1/responses": "default"}
	seen := map[string]bool{}
	for _, event := range result.RecentEvents {
		seen[event.Endpoint] = event.Stream
		if event.ServiceTier != tiers[event.Endpoint] {
			t.Fatalf("native service tier = %q for %s", event.ServiceTier, event.Endpoint)
		}
	}
	if stream, ok := seen["/v1/messages"]; !ok || !stream {
		t.Fatal("native streaming endpoint missing")
	}
	if stream, ok := seen["/v1/responses"]; !ok || stream {
		t.Fatal("native non-streaming endpoint missing")
	}
	// Exercise the same JSON boundary used by storage, then query the restored
	// data through the time-range path as well as the all-time path above.
	raw, err := json.Marshal(stats.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	t.Cleanup(restored.Close)
	restored.mu.Lock()
	restored.restoreStorageSnapshotLocked(snapshot, time.Now())
	restored.mu.Unlock()
	for _, rangeKey := range []string{"all", "24h"} {
		result := restored.QueryAPIDetail("test", rangeKey, 10, 10)
		if len(result.RecentEvents) != 2 {
			t.Fatalf("restored %s events = %d", rangeKey, len(result.RecentEvents))
		}
		for _, event := range result.RecentEvents {
			if stream, ok := seen[event.Endpoint]; !ok || stream != event.Stream || event.ServiceTier != tiers[event.Endpoint] {
				t.Fatalf("restored %s endpoint/stream = %q/%v", rangeKey, event.Endpoint, event.Stream)
			}
		}
	}
}

func TestServiceTierLateEnrichmentPreservesNativeValue(t *testing.T) {
	s := NewRequestStatistics()
	t.Cleanup(s.Close)
	record := metadataUsage(time.Now())
	s.Record(record)
	enriched := enrichUsageRecord(record, UsageRecord{ServiceTier: "priority"})
	if enriched.ServiceTier != "priority" || !s.EnrichRecordedUsage(record, enriched) {
		t.Fatal("late service tier did not reach the recorded request")
	}
	if s.EnrichRecordedUsage(record, UsageRecord{ServiceTier: "default"}) {
		t.Fatal("late metadata overwrote an existing service tier")
	}
	if got := enrichUsageRecord(enriched, UsageRecord{ServiceTier: "default"}); got.ServiceTier != "priority" {
		t.Fatal("native service tier was overwritten")
	}
	for _, rangeKey := range []string{"all", "24h"} {
		result := s.QueryAPIDetail(usageGroupKey(record), rangeKey, 10, 10)
		if len(result.RecentEvents) != 1 || result.RecentEvents[0].ServiceTier != "priority" || result.Summary.TotalRequests != 1 || result.Summary.TotalTokens != 5 {
			t.Fatalf("%s: enrichment lost metadata or changed usage: %+v", rangeKey, result)
		}
	}
}
