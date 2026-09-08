package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func reviewRecord(at time.Time) UsageRecord {
	return UsageRecord{Provider: "openai", Model: "review-model", RequestedAt: at, Detail: UsageDetail{InputTokens: 10, TotalTokens: 10}}
}

func TestReviewRestartAfterTrimming(t *testing.T) {
	cfg := runtimeConfig{MaxDetailsPerModel: 1, StorageEnabled: true, StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), StorageFlushSeconds: 1}
	s := NewRequestStatistics()
	s.Configure(cfg)
	now := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		s.Record(reviewRecord(now.Add(time.Duration(i) * time.Second)))
	}
	before := s.Snapshot().TotalRequests
	s.Close()
	restored := NewRequestStatistics()
	restored.Configure(cfg)
	defer restored.Close()
	if got := restored.Snapshot().TotalRequests; got != before {
		t.Fatalf("before=%d after restart=%d", before, got)
	}
}

func TestReviewIdleStorageIntervals(t *testing.T) {
	s := NewRequestStatistics()
	s.Configure(runtimeConfig{MaxDetailsPerModel: 10, StorageEnabled: true, StoragePath: filepath.Join(t.TempDir(), "usage.jsonl"), StorageFlushSeconds: 1, StorageSnapshotSeconds: 1, StorageSyncSeconds: 1})
	defer s.Close()
	s.Record(reviewRecord(time.Now()))
	deadline := time.Now().Add(time.Second)
	for s.StorageStatus().LastFlushAt == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.Record(reviewRecord(time.Now()))
	time.Sleep(1500 * time.Millisecond)
	s.mu.RLock()
	buffered, unsynced, snapshot := s.storageBuffered, s.storageUnsyncedRecords, s.storageLastSnapshot
	s.mu.RUnlock()
	if buffered != 0 || unsynced != 0 || snapshot.IsZero() {
		t.Fatalf("after idle > intervals: buffered=%d unsynced=%d snapshot=%v", buffered, unsynced, snapshot)
	}
}

func TestReviewBearerRedaction(t *testing.T) {
	secret := "abcdefghijklmnopqrstuvwxyz0123456789"
	got := requestDetailFromUsageRecord(UsageRecord{Failure: UsageFailure{Body: "Authorization: Bearer " + secret}}, time.Now(), headerWhitelist{}).Failure
	if strings.Contains(got, secret) {
		t.Fatalf("raw credential persisted: %s", got)
	}
}

func TestReviewExportDelayedUsage(t *testing.T) {
	s := NewRequestStatistics()
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 4; i++ {
		s.Record(reviewRecord(base.Add(time.Duration(i) * time.Second)))
	}
	snapshotAt := time.Now()
	frozen := s.captureEventExport(EventsQuery{}, 0, snapshotAt)
	first := frozen.page(0, 2)
	s.Record(reviewRecord(base.Add(4 * time.Second)))
	second := frozen.page(2, 2)
	if first.Events[1].Timestamp.Equal(second.Events[0].Timestamp) {
		t.Fatalf("duplicate across export pages: %s", second.Events[0].Timestamp)
	}
}

func TestReviewTrimmedRetention(t *testing.T) {
	s := NewRequestStatistics()
	s.Configure(runtimeConfig{MaxDetailsPerModel: 1, RetentionDays: 1})
	now := time.Now()
	s.Record(reviewRecord(now.Add(-time.Hour)))
	s.Record(reviewRecord(now))
	s.mu.Lock()
	s.pruneLocked(now.Add(48*time.Hour), true)
	s.mu.Unlock()
	summary := s.SummaryWithoutDetailsAt(now.Add(48 * time.Hour))
	if summary.Usage.TotalRequests != 0 {
		t.Fatalf("after all records expire: requests=%d details=%d", summary.Usage.TotalRequests, s.DetailCount())
	}
}

func TestReviewRangeAfterTrimming(t *testing.T) {
	s := NewRequestStatistics()
	s.Configure(runtimeConfig{MaxDetailsPerModel: 1, RetentionDays: 30})
	now := time.Now()
	s.Record(reviewRecord(now.Add(-time.Minute)))
	s.Record(reviewRecord(now))
	all := s.SummaryWithoutDetailsAt(now).Usage.TotalRequests
	recent := s.SummaryWithoutDetailsForRangeAt("24h", now).Usage.TotalRequests
	if all != recent {
		t.Fatalf("all records within 24h: all=%d 24h=%d", all, recent)
	}
}

func TestReviewExportDeleteQueuedJob(t *testing.T) {
	m := newDashboardExportJobManager()
	defer m.close()
	id := "review-deleted"
	path := filepath.Join(t.TempDir(), "export")
	m.jobs[id] = &dashboardExportJob{ID: id, Status: dashboardExportJobQueued, FilePath: path}
	m.delete(id)
	m.run(id, EventsQuery{}, dashboardEventsExportOptions{}, path)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("deleted job still ran and left an untracked export file")
	}
}

func TestReviewPriceWriteFailureConsistency(t *testing.T) {
	s := NewRequestStatistics()
	path := filepath.Join(t.TempDir(), "prices.json")
	s.Configure(runtimeConfig{MaxDetailsPerModel: 10, PriceStoragePath: path})
	if _, err := s.UpsertModelPrice("review-model", ModelPrice{Prompt: 1}); err != nil {
		t.Fatal(err)
	}
	rec := reviewRecord(time.Now())
	rec.Detail.InputTokens = 1000000
	rec.Detail.TotalTokens = 1000000
	s.Record(rec)
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	override := 3.0
	_, err := s.UpsertModelPrice("review-model", ModelPrice{Prompt: 2, TimeRules: []ModelPriceRule{{Name: "review", Start: "00:00", End: "23:59", Prompt: &override}}})
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if s.ModelPrices().Prices["review-model"].Prompt != 2 {
		t.Fatalf("did not reach persistence failure: %v", err)
	}
	summary := s.SummaryWithoutDetails().Usage.TotalCost
	events := s.QueryEvents(EventsQuery{}).Events
	if summary != *events[0].CostUSD {
		t.Fatalf("failed price write leaves summary=%g event=%g", summary, *events[0].CostUSD)
	}
}

func TestReviewLateRequestEvictsRecent(t *testing.T) {
	s := NewRequestStatistics()
	s.Configure(runtimeConfig{MaxDetailsPerModel: 1})
	now := time.Now()
	s.Record(reviewRecord(now))
	s.Record(reviewRecord(now.Add(-time.Hour)))
	events := s.QueryEvents(EventsQuery{}).Events
	if !events[0].Timestamp.Equal(now) {
		t.Fatalf("newest retained timestamp=%s instead of %s", events[0].Timestamp, now)
	}
}

func TestReviewAccountingImportValidationAndRoundTrip(t *testing.T) {
	old := stats
	stats = NewRequestStatistics()
	t.Cleanup(func() { stats = old })
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 1})
	now := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		stats.Record(reviewRecord(now.Add(time.Duration(i) * time.Second)))
	}
	snapshot, count := stats.ReconciledSnapshot()
	if count != 3 {
		t.Fatalf("export count=%d", count)
	}
	stats = NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 1})
	body, err := json.Marshal(ExportPayload{Version: 1, Usage: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleImportUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("import=%s err=%v", raw, err)
	}
	var response ManagementResponse
	if err := json.Unmarshal(env.Result, &response); err != nil {
		t.Fatal(err)
	}
	var result ImportResponse
	if err := json.Unmarshal(response.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.InputRecords != 3 || result.AcceptedRecords != 3 || result.RejectedRecords != 0 || stats.DetailCount() != 1 {
		t.Fatalf("result=%+v details=%d", result, stats.DetailCount())
	}
	if repeat := stats.MergeSnapshot(snapshot); repeat.Added != 0 || repeat.Skipped != 3 {
		t.Fatalf("reimport=%+v", repeat)
	}
	for apiName, api := range snapshot.APIs {
		for modelName, model := range api.Models {
			model.Accounting[0].Tokens.InputTokens = -1
			api.Models[modelName] = model
		}
		snapshot.APIs[apiName] = api
	}
	body, err = json.Marshal(ExportPayload{Version: 1, Usage: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = handleImportUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.OK {
		t.Fatalf("invalid accounting accepted: %s err=%v", raw, err)
	}
}

func TestReviewArchivedHistoricalRepairs(t *testing.T) {
	t.Run("cache", func(t *testing.T) {
		s := NewRequestStatistics()
		s.ConfigurePatch(runtimeConfigPatch{MaxDetailsPerModel: intPtr(0), RetentionDays: intPtr(0)})
		d := pollutedClaudeCacheDetail()
		now := time.Now()
		s.mu.Lock()
		s.recordDetailLocked("claude", d.Model, d, requestDedupKey{}, now, false)
		s.pruneLocked(now, true)
		s.claudeCacheRepairEnabled = true
		n := s.repairClaudeCacheFallbackDetailsLocked(now)
		s.mu.Unlock()
		got := s.Snapshot()
		if n != 1 || got.TotalRequests != 1 || got.TotalTokens != d.Tokens.TotalTokens-d.Tokens.CacheWriteTokens || s.DetailCount() != 0 {
			t.Fatalf("repairs=%d stats=%+v details=%d", n, got, s.DetailCount())
		}
	})
	t.Run("attribution", func(t *testing.T) {
		s := NewRequestStatistics()
		s.ConfigurePatch(runtimeConfigPatch{MaxDetailsPerModel: intPtr(0), RetentionDays: intPtr(0)})
		now := time.Now()
		s.mu.Lock()
		for _, d := range []RequestDetail{repairTestOriginalClaudeDetail(), repairTestMigratedTwinDetail()} {
			s.recordDetailLocked(d.Source, d.Model, d, requestDedupKey{}, now, false)
		}
		s.pruneLocked(now, true)
		removed := s.repairMigratedAttributionDetailsLocked(now)
		s.mu.Unlock()
		if len(removed) != 1 || s.Snapshot().TotalRequests != 1 {
			t.Fatalf("removed=%d total=%d", len(removed), s.Snapshot().TotalRequests)
		}
	})
	t.Run("protocol", func(t *testing.T) {
		s := NewRequestStatistics()
		s.ConfigurePatch(runtimeConfigPatch{MaxDetailsPerModel: intPtr(0), RetentionDays: intPtr(0)})
		fallback, native := protocolFallbackTestRecords()
		s.Record(fallback)
		s.Record(native)
		snapshot := s.Snapshot()
		repaired, n := reconcileProtocolFallbackSnapshot(snapshot)
		if n != 1 || repaired.TotalRequests != 1 || snapshot.TotalRequests != 2 {
			t.Fatalf("snapshot repair=%d total=%d original=%d", n, repaired.TotalRequests, snapshot.TotalRequests)
		}
		if n := s.ReconcileProtocolFallbacks(); n != 1 || s.Snapshot().TotalRequests != 1 || s.DetailCount() != 0 {
			t.Fatalf("live repair=%d total=%d details=%d", n, s.Snapshot().TotalRequests, s.DetailCount())
		}
		if !s.RemoveRecordedUsage(native) || s.Snapshot().TotalRequests != 0 {
			t.Fatal("archived native could not be removed")
		}
	})
}

func TestReviewArchivedSplitRestoreAndAPIStats(t *testing.T) {
	now := time.Now().Add(-time.Minute)
	a := requestDetailFromUsageRecord(reviewRecord(now), now, headerWhitelist{})
	b := a
	b.Model = "second-model"
	b.Timestamp = now.Add(time.Second)
	b.Source = "second-source"
	snapshot := StatisticsSnapshot{TotalRequests: 2, SuccessCount: 2, TotalTokens: 20, APIs: map[string]APISnapshot{"legacy": {TotalRequests: 2, SuccessCount: 2, TotalTokens: 20, Models: map[string]ModelSnapshot{"legacy-model": {TotalRequests: 2, SuccessCount: 2, TotalTokens: 20, Details: []RequestDetail{b}, Accounting: []RequestDetail{a}}}}}}
	for _, splitAPI := range []bool{false, true} {
		if !splitAPI {
			b.Source = a.Source
		} else {
			b.Source = "second-source"
		}
		api := snapshot.APIs["legacy"]
		model := api.Models["legacy-model"]
		model.Details[0] = b
		api.Models["legacy-model"] = model
		snapshot.APIs["legacy"] = api
		s := NewRequestStatistics()
		s.mu.Lock()
		s.restoreStorageSnapshotLocked(snapshot, time.Now())
		s.mu.Unlock()
		got := s.Snapshot()
		if got.TotalRequests != 2 || s.DetailCount() != 1 {
			t.Fatalf("splitAPI=%v totals=%d details=%d", splitAPI, got.TotalRequests, s.DetailCount())
		}
		var total int64
		for name := range got.APIs {
			total += s.QueryAPIDetail(name, "24h", 10, 10).Summary.TotalRequests
		}
		if total != 2 {
			t.Fatalf("splitAPI=%v range API count=%d", splitAPI, total)
		}
	}
}

func TestReviewCredentialContexts(t *testing.T) {
	for _, input := range []string{"Authorization:Bearer short", "AUTHORIZATION:\tBeArEr short", `{"authorization":"Basic c2VjcmV0"}`, "X-API-Key:short\napi-key:other", "Bearer first Bearer second"} {
		got := redactSensitiveText(input)
		for _, secret := range []string{"short", "c2VjcmV0", "other", "first", "second"} {
			if strings.Contains(got, secret) {
				t.Fatalf("%q leaks %q in %q", input, secret, got)
			}
		}
	}
}

func TestReviewExportSnapshotSurvivesMutations(t *testing.T) {
	s := NewRequestStatistics()
	now := time.Now().Add(-time.Minute)
	for i := 0; i < 4; i++ {
		s.Record(reviewRecord(now.Add(time.Duration(i) * time.Second)))
	}
	frozen := s.captureEventExport(EventsQuery{}, 0, time.Now())
	want := cloneRequestDetails(frozen.result.Events)
	s.Configure(runtimeConfig{MaxDetailsPerModel: 1})
	if _, err := s.UpsertModelPrice("review-model", ModelPrice{Prompt: 100}); err != nil {
		t.Fatal(err)
	}
	s.Record(reviewRecord(now.Add(10 * time.Second)))
	var got []RequestDetail
	for offset := 0; offset < 4; offset += 2 {
		got = append(got, frozen.page(offset, 2).Events...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("export snapshot changed after retention, pricing and late usage")
	}
}

func TestReviewExportCancellationCleansFiles(t *testing.T) {
	old := stats
	stats = NewRequestStatistics()
	defer func() { stats = old }()
	m := newDashboardExportJobManager()
	// Hold the snapshot lock until the queued job has been deleted. Whichever
	// side wins startup, it must leave neither a result nor a temporary file.
	stats.mu.Lock()
	job, status, message := m.create(EventsQuery{}, dashboardEventsExportOptions{Format: dashboardExportJSON})
	if status != 202 {
		stats.mu.Unlock()
		m.close()
		t.Fatalf("create=%d %s", status, message)
	}
	if !m.delete(job.ID) {
		stats.mu.Unlock()
		m.close()
		t.Fatal("delete failed")
	}
	stats.mu.Unlock()
	m.close()
	for _, path := range []string{job.FilePath, job.FilePath + ".tmp"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted export file remains: %s err=%v", path, err)
		}
	}
	if m.workers != 0 {
		t.Fatalf("workers=%d after close", m.workers)
	}
}

func TestReviewExportCancellationWhileWriting(t *testing.T) {
	old := stats
	stats = NewRequestStatistics()
	defer func() { stats = old }()
	for i := 0; i < 100; i++ {
		stats.Record(reviewRecord(time.Now().Add(time.Duration(i-110) * time.Second)))
	}
	for _, format := range []dashboardExportFormat{dashboardExportJSON, dashboardExportJSONL, dashboardExportCSV} {
		ctx, cancel := context.WithCancel(context.Background())
		writer := reviewCancelWriter{cancel: cancel}
		_, err := encodeDashboardEventsExportPaged(&writer, EventsQuery{}, dashboardEventsExportOptions{Format: format, ctx: ctx}, time.Now())
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("format=%s cancellation=%v", format, err)
		}
	}
}

type reviewCancelWriter struct{ cancel context.CancelFunc }

func (w *reviewCancelWriter) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }
