package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func metadataRequest(id, endpoint string, stream bool) requestMetadataRequest {
	return requestMetadataRequest{RequestID: id, Model: "model", RequestedModel: "alias", Stream: stream,
		Metadata: map[string]any{"selected_auth_id": "auth", "selected_auth_index": "index", "request_path": endpoint}}
}

func metadataUsage(at time.Time) UsageRecord {
	return UsageRecord{Provider: "test", Model: "model", Alias: "alias", AuthID: "auth", AuthIndex: "index",
		RequestedAt: at, Detail: UsageDetail{InputTokens: 3, OutputTokens: 2}}
}

func TestRequestMetadataStockABIToDashboard(t *testing.T) {
	previousStats, previousCache := stats, requestMetadata
	stats, requestMetadata = NewRequestStatistics(), newRequestMetadataCache()
	t.Cleanup(func() { stats.Close(); stats, requestMetadata = previousStats, previousCache })
	cfg := runtimeConfig{MaxDetailsPerModel: 100, StorageEnabled: true,
		StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), StorageFlushSeconds: 1,
		PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")}
	stats.Configure(cfg)
	paths := []string{"/v1/responses", "/v1/chat/completions", "/v1/messages", "/v1beta/models/gemini:streamGenerateContent"}
	for i, path := range paths {
		id := fmt.Sprint(i)
		req := metadataRequest(id, path+"?key=secret", i%2 == 0)
		for _, method := range []string{"request.intercept_before", "request.intercept_after"} {
			raw, err := handleMethod(method, mustMarshal(req))
			if err != nil {
				t.Fatal(err)
			}
			var env envelope
			if err := json.Unmarshal(raw, &env); err != nil || !env.OK || string(env.Result) != "{}" {
				t.Fatalf("request callback modified traffic: %s (%v)", raw, err)
			}
		}
		at := time.Now()
		// This is the unmodified v7.2.152 wire schema: no Endpoint, Stream or
		// RequestID. Cancellation reports follow the same native usage ABI.
		native := map[string]any{"Provider": "test", "Model": "model", "Alias": "alias", "AuthID": "auth", "AuthIndex": "index",
			"RequestedAt": at, "Failed": i == 2, "Latency": int64(time.Second), "TTFT": int64(time.Millisecond),
			"Detail": map[string]int{"InputTokens": 3, "OutputTokens": 2}}
		complete := requestMetadataRequest{RequestID: id, CompletedAt: time.Now()}
		// Exercise asynchronous usage both before and after completion.
		if i%2 == 0 {
			if _, err := handleMethod("request.complete", mustMarshal(complete)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := handleMethod("usage.handle", mustMarshal(native)); err != nil {
			t.Fatal(err)
		}
		if i%2 != 0 {
			if _, err := handleMethod("request.complete", mustMarshal(complete)); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertEvents := func(s *RequestStatistics) {
		t.Helper()
		for _, rangeKey := range []string{"all", "24h"} {
			events := s.QueryAPIDetail(usageGroupKey(metadataUsage(time.Now())), rangeKey, 10, 10).RecentEvents
			if len(events) != len(paths) {
				t.Fatalf("%s events = %d", rangeKey, len(events))
			}
			seen := map[string]bool{}
			for _, event := range events {
				seen[event.Endpoint] = event.Stream
			}
			for i, path := range paths {
				if stream, ok := seen[path]; !ok || stream != (i%2 == 0) {
					t.Fatalf("%s: metadata missing for %s: %v", rangeKey, path, seen)
				}
			}
		}
		if s.totalRequests != 4 || s.totalTokens != 20 || s.failureCount != 1 {
			t.Fatalf("native accounting changed: %+v", s.Snapshot())
		}
	}
	assertEvents(stats)
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(mustMarshal(stats.Snapshot()), &snapshot); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	t.Cleanup(restored.Close)
	restored.mu.Lock()
	restored.restoreStorageSnapshotLocked(snapshot, time.Now())
	restored.mu.Unlock()
	assertEvents(restored)
	stats.Close()
	fromDisk := NewRequestStatistics()
	t.Cleanup(fromDisk.Close)
	fromDisk.Configure(cfg)
	assertEvents(fromDisk)
}

func TestRequestMetadataConcurrentAmbiguity(t *testing.T) {
	base := time.Now()
	for _, tc := range []struct {
		name, path string
		stream     bool
	}{
		{"different path", "/v1/messages", true}, {"different stream", "/v1/responses", false}, {"missing path", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newRequestMetadataCache()
			c.observe(metadataRequest("a", "/v1/responses", true), base)
			c.observe(metadataRequest("b", tc.path, tc.stream), base.Add(time.Millisecond))
			got := c.enrich(metadataUsage(base.Add(2*time.Millisecond)), base.Add(time.Second))
			if got.Endpoint != "" || got.Stream {
				t.Fatalf("ambiguous request was enriched: %+v", got)
			}
			// An observation beginning after this record cannot be its source.
			got = c.enrich(metadataUsage(base), base.Add(time.Second))
			if got.Endpoint != "/v1/responses" || !got.Stream {
				t.Fatal("earlier request lost metadata")
			}
		})
	}
}

func TestRequestMetadataRetriesAndIsolation(t *testing.T) {
	base := time.Now()
	c := newRequestMetadataCache()
	c.observe(metadataRequest("a", "/v1/responses", true), base)
	c.observe(metadataRequest("a", "/v1/messages", false), base.Add(time.Second))
	c.complete("a", base.Add(2*time.Second), base.Add(3*time.Second))
	for _, tc := range []struct {
		offset   time.Duration
		endpoint string
		stream   bool
	}{
		{time.Millisecond, "/v1/responses", true}, {time.Second, "/v1/messages", false},
		{2 * time.Second, "/v1/messages", false}, {2*time.Second + time.Millisecond, "", false},
	} {
		got := c.enrich(metadataUsage(base.Add(tc.offset)), base.Add(4*time.Second))
		if got.Endpoint != tc.endpoint || got.Stream != tc.stream {
			t.Fatalf("retry metadata: %+v", got)
		}
	}
	for _, change := range []func(*UsageRecord){
		func(r *UsageRecord) { r.AuthID = "other" }, func(r *UsageRecord) { r.AuthIndex = "other" },
		func(r *UsageRecord) { r.Alias = "other" }, func(r *UsageRecord) { r.Model = "other"; r.Alias = "" },
		func(r *UsageRecord) { r.RequestedAt = time.Time{} },
	} {
		r := metadataUsage(base.Add(time.Millisecond))
		change(&r)
		if got := c.enrich(r, base.Add(4*time.Second)); got.Endpoint != "" || got.Stream {
			t.Fatalf("unrelated usage enriched: %+v", got)
		}
	}
}

func TestRequestMetadataPreservesExplicitNativeFields(t *testing.T) {
	base := time.Now()
	c := newRequestMetadataCache()
	c.observe(metadataRequest("a", "/v1/responses", true), base)
	for _, field := range []string{"Stream", "stream", "Streaming", "streaming"} {
		raw := map[string]any{"AuthID": "auth", "Model": "model", "RequestedAt": base.Add(time.Millisecond), "Endpoint": "/native", field: false}
		var r UsageRecord
		if err := json.Unmarshal(mustMarshal(raw), &r); err != nil {
			t.Fatal(err)
		}
		got := c.enrich(r, base.Add(time.Second))
		if got.Stream || got.Endpoint != "/native" {
			t.Fatalf("explicit %s overwritten: %+v", field, got)
		}
	}
}

func TestRequestMetadataBoundsFailClosed(t *testing.T) {
	base := time.Now()
	c := newRequestMetadataCache()
	for i := 0; i <= maxRequestMetadata; i++ {
		c.observe(metadataRequest(fmt.Sprint(i), "/v1/responses", true), base)
	}
	if c.count != 0 || len(c.byAuth) != 0 || len(c.byID) != 0 {
		t.Fatal("overflow retained metadata")
	}
	c.observe(metadataRequest("new", "/v1/messages", true), base.Add(time.Second))
	if got := c.enrich(metadataUsage(base.Add(2*time.Second)), base.Add(3*time.Second)); got.Endpoint != "" || got.Stream {
		t.Fatal("overflow exposed partial matching state")
	}
	c.observe(metadataRequest("recovered", "/v1/messages", true), base.Add(requestMetadataTTL+time.Second))
	if c.count != 1 {
		t.Fatal("cache did not recover")
	}
	c.cleanupLocked(base.Add(2*requestMetadataTTL + 2*time.Second))
	if c.count != 0 || c.unsafeUntil.IsZero() {
		t.Fatal("stale active request was silently discarded")
	}
}

func TestRequestMetadataExpiredCompletionAndConcurrentAccess(t *testing.T) {
	base := time.Now()
	c := newRequestMetadataCache()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint(i)
			c.observe(metadataRequest(id, "/v1/responses", true), base)
			c.enrich(metadataUsage(base.Add(time.Millisecond)), base.Add(time.Second))
			c.complete(id, base.Add(time.Second), base.Add(2*time.Second))
		}(i)
	}
	wg.Wait()
	c.cleanupLocked(base.Add(requestMetadataTTL + 3*time.Second))
	if c.count != 0 || len(c.byID) != 0 || len(c.byAuth) != 0 || !c.unsafeUntil.IsZero() {
		t.Fatal("completed metadata not released")
	}
}
