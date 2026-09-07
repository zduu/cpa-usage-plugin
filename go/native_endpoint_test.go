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
		`{"Provider":"test","Model":"model","Endpoint":"/v1/messages","Stream":true,"Detail":{"InputTokens":3,"OutputTokens":2}}`,
		`{"provider":"test","model":"model","endpoint":"/v1/responses","stream":false,"detail":{"input_tokens":3,"output_tokens":2}}`,
	} {
		if _, err := handleMethod("usage.handle", []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	result := stats.QueryAPIDetail("test", "all", 10, 10)
	if len(result.RecentEvents) != 2 {
		t.Fatalf("events = %d", len(result.RecentEvents))
	}
	seen := map[string]bool{}
	for _, event := range result.RecentEvents {
		seen[event.Endpoint] = event.Stream
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
			if stream, ok := seen[event.Endpoint]; !ok || stream != event.Stream {
				t.Fatalf("restored %s endpoint/stream = %q/%v", rangeKey, event.Endpoint, event.Stream)
			}
		}
	}
}
