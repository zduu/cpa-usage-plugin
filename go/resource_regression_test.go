package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDisabledInterceptorABIHasNoSideEffects(t *testing.T) {
	previous := usageFallbacks
	coordinator := newUsageFallbackCoordinator()
	usageFallbacks = coordinator
	t.Cleanup(func() { usageFallbacks = previous })
	for _, method := range []string{"response.intercept_after", "response.intercept_stream_chunk"} {
		// No payload parsing, even if a stale host calls the disabled capability.
		if _, err := handleMethod(method, []byte("not JSON")); err != nil {
			t.Fatal(err)
		}
	}
	if len(coordinator.pending) != 0 || len(coordinator.nativeRecent) != 0 {
		t.Fatal("disabled interceptors retained live correlation state")
	}
}

func TestDetailEvictionReleasesReferences(t *testing.T) {
	s := NewRequestStatistics()
	s.maxDetailsPerModel = 1
	backing := []RequestDetail{{Model: "old", Headers: map[string][]string{"large": {"payload"}}}, {Model: "new"}}
	model := &modelStats{Details: backing}
	s.trimModelDetailsLocked(model)
	if backing[0].Model != "" || backing[0].Headers != nil {
		t.Fatal("eviction retained references in the backing array")
	}
	if len(model.Details) != 1 || model.Details[0].Model != "new" || s.evictedTotal != 1 {
		t.Fatal("incorrect retained window")
	}
	s.maxDetailsPerModel = 0
	s.trimModelDetailsLocked(model)
	if model.Details != nil || backing[1].Model != "" {
		t.Fatal("zero detail limit retained its backing array")
	}
}

func TestRecordRetentionAndDetailLimit(t *testing.T) {
	s := NewRequestStatistics()
	s.maxDetailsPerModel = 2
	s.retention = time.Hour
	now := time.Now()
	for _, at := range []time.Time{now.Add(-2 * time.Hour), now, now.Add(-time.Minute), now} {
		s.Record(UsageRecord{Provider: "test", Model: "model", RequestedAt: at, Detail: UsageDetail{InputTokens: 1}})
	}
	if s.totalRequests != 3 || s.DetailCount() != 2 || s.evictedTotal != 2 {
		t.Fatalf("requests/details/evictions = %d/%d/%d", s.totalRequests, s.DetailCount(), s.evictedTotal)
	}
	// A late record already outside retention must be removed immediately,
	// even while the next previously scheduled expiry is still in the future.
	s.Record(UsageRecord{Provider: "test", Model: "other", RequestedAt: now.Add(-3 * time.Hour), Detail: UsageDetail{InputTokens: 1}})
	if s.totalRequests != 3 || s.DetailCount() != 2 {
		t.Fatal("late expired record escaped retention")
	}
	s.mu.Lock()
	s.pruneLocked(now.Add(2*time.Hour), false)
	s.mu.Unlock()
	if s.DetailCount() != 0 {
		t.Fatal("scheduled retention did not clear expired details")
	}
}

func TestRetentionCompactionClearsTail(t *testing.T) {
	s := NewRequestStatistics()
	s.retention = time.Hour
	now := time.Now()
	backing := []RequestDetail{{Model: "keep", Timestamp: now}, {Model: "expired", Timestamp: now.Add(-2 * time.Hour)}}
	s.apis["api"] = &apiStats{Models: map[string]*modelStats{"model": {Details: backing}}}
	s.pruneLocked(now, false)
	if backing[1].Model != "" {
		t.Fatal("retention compaction retained expired tail references")
	}
}

func TestRecordConcurrentHeaderConfiguration(t *testing.T) {
	s := NewRequestStatistics()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 0; i < 100; i++ {
			s.mu.Lock()
			s.logResponseHeaders = headerWhitelist{}
			s.mu.Unlock()
		}
	}()
	for i := 0; i < 100; i++ {
		s.Record(UsageRecord{Model: "test", ResponseHeaders: map[string][]string{"x-test": {"value"}}})
	}
	workers.Wait()
}

func TestSeenExpiryReschedulesAfterRebuild(t *testing.T) {
	s := NewRequestStatistics()
	s.dedupWindow = time.Minute
	now := time.Now()
	key := requestDedupKey{modelName: "test"}
	s.seen[key] = now
	s.pruneSeenLocked(now)
	if !s.nextSeenExpiry.Equal(now.Add(time.Minute)) {
		t.Fatal("incorrect dedup expiry")
	}
	s.pruneSeenLocked(now.Add(2 * time.Minute))
	if len(s.seen) != 0 {
		t.Fatal("expired dedup entry retained")
	}
}

func TestNativeOnlyReconciliationDoesNotAllocate(t *testing.T) {
	s := NewRequestStatistics()
	s.Record(UsageRecord{Provider: "codex", Model: "gpt-5", AuthID: "native", AuthIndex: "index", Latency: time.Second})
	now := time.Now()
	allocs := testing.AllocsPerRun(10, func() {
		if s.reconcileRecordedProtocolFallbacksLocked(now) != 0 {
			t.Fatal("native-only history was modified")
		}
	})
	if allocs != 0 {
		t.Fatalf("native-only reconciliation allocated %g objects", allocs)
	}
}

func BenchmarkRecordRetainedHistory(b *testing.B) {
	for _, models := range []int{1, 20} {
		b.Run(fmt.Sprintf("models_%d", models), func(b *testing.B) {
			s := NewRequestStatistics()
			now := time.Now()
			for model := 0; model < models; model++ {
				name := fmt.Sprintf("model-%d", model)
				for i := 0; i < 5000; i++ {
					s.recordDetailLocked("test", name, RequestDetail{Timestamp: now, Model: name, Tokens: TokenStats{InputTokens: 1}}, requestDedupKey{}, now, false)
				}
			}
			record := UsageRecord{Provider: "test", Model: "model-0", RequestedAt: now, Detail: UsageDetail{InputTokens: 1}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.Record(record)
			}
		})
	}
}
