package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestImportResponseCountersIncludeArchivedHistoryAndResiduals(t *testing.T) {
	previous := stats
	stats = NewRequestStatistics()
	t.Cleanup(func() { stats = previous })
	stats.retention, stats.maxDetailsPerModel = 0, 1
	now := time.Now()
	for i := 0; i < 8; i++ {
		stats.Record(UsageRecord{Provider: "test", Model: "model", RequestedAt: now.Add(time.Duration(i) * time.Nanosecond), Failed: i%2 == 0})
	}
	// Old snapshots may retain aggregates without corresponding request rows.
	// Totals must not be reconstructed from the visible or archived records.
	const residual int64 = 9007199254740993
	stats.totalRequests += residual
	stats.failureCount += residual
	for pass := 0; pass < 2; pass++ {
		raw, err := handleImportUsage([]byte(`{"version":1,"usage":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		var result ImportResponse
		decodeManagementResponse(t, raw, &result)
		if result.TotalRequests != residual+8 || result.FailedRequests != residual+4 || result.InputRecords != 0 || result.Added != 0 {
			t.Fatalf("import response lost counters: %+v", result)
		}
		stats.mu.RLock()
		matches := stats.lastImportResult != nil && reflect.DeepEqual(*stats.lastImportResult, result)
		stats.mu.RUnlock()
		if !matches || stats.DetailCount() != 1 {
			t.Fatal("import response tracking or visible detail count changed")
		}
	}
}

func TestImportResponseCounterPairDuringConcurrentRecords(t *testing.T) {
	previous := stats
	s := NewRequestStatistics()
	s.retention, s.maxDetailsPerModel = 0, 1
	stats = s
	done := make(chan struct{})
	t.Cleanup(func() {
		<-done
		stats = previous
	})
	now := time.Now()
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			s.Record(UsageRecord{Provider: "test", Model: "model", RequestedAt: now.Add(time.Duration(i) * time.Nanosecond), Failed: true})
		}
	}()
	for i := 0; i < 20; i++ {
		raw, err := handleImportUsage([]byte(`{"version":1,"usage":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		var result ImportResponse
		decodeManagementResponse(t, raw, &result)
		if result.TotalRequests != result.FailedRequests || result.TotalRequests > 200 || result.Added != 0 {
			t.Fatalf("inconsistent concurrent counter pair: %+v", result)
		}
	}
	<-done
	if s.totalRequests != 200 || s.failureCount != 200 {
		t.Fatal("concurrent import changed native request counts")
	}
}

func BenchmarkImportResponseRetainedHistory(b *testing.B) {
	for _, count := range []int{10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			previous := stats
			stats = buildBenchmarkStats(count)
			b.Cleanup(func() { stats = previous })
			body := []byte(`{"version":1,"usage":{}}`)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := handleImportUsage(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
