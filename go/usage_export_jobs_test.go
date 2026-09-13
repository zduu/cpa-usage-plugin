package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func usageExportTestStats(count int) *RequestStatistics {
	now := time.Now()
	s := buildPerformanceDataset(count, false, now)
	s.retention = 0
	s.maxDetailsPerModel = 32
	s.exportMaxRecords = 1 // An event limit must not truncate a usage backup.
	s.pruneLocked(now, true)
	return s
}

func decodeUsageExport(t *testing.T, raw []byte) ExportPayload {
	t.Helper()
	var payload ExportPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestUsageExportViewMatchesLegacyAndIsolatesConcurrentMutations(t *testing.T) {
	for _, count := range []int{0, 1, 1200} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			s := usageExportTestStats(count)
			s.priceStoragePath = filepath.Join(t.TempDir(), "prices.json")
			// Retain aggregate-only history as well as exact int64 record values.
			if count > 0 {
				for _, api := range s.apis {
					for _, model := range api.Models {
						detail := model.accountingDetailAt(0)
						detail.Headers = map[string][]string{"x-test": {"中文🙂, } ] \" quoted"}}
						detail.Tokens.InputTokens = 9007199254740993
						detail.Tokens.TotalTokens = detailTotalTokens(detail.Tokens)
						cost := 1.2345
						detail.CostUSD = &cost
						model.setAccountingDetailAt(0, detail)
					}
				}
				s.rebuildAggregatesLocked()
			}
			const residual int64 = 9007199254740993
			s.totalRequests += residual
			s.failureCount += residual
			view, err := s.captureUsageExport(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			wantSnapshot, detailCount := s.ReconciledSnapshot()
			want := ExportPayload{Version: 1, ExportedAt: view.header.ExportedAt, Plugin: pluginVersion,
				DetailCount: detailCount, Config: s.ConfigSnapshot(), Usage: wantSnapshot}
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if detailCount != int64(count) || view.header.DetailCount != int64(count) || view.usage.metadata.TotalRequests != int64(count)+residual {
				t.Fatal("backup confused record count with aggregate residuals")
			}
			var mutation sync.WaitGroup
			mutation.Add(1)
			go func() {
				defer mutation.Done()
				s.Record(UsageRecord{Provider: "provider-0", Model: "model-000", RequestedAt: time.Now(), Detail: UsageDetail{InputTokens: 1}})
				s.mu.Lock()
				for _, api := range s.apis {
					for _, model := range api.Models {
						for i := range model.Details {
							if model.Details[i].CostUSD != nil {
								*model.Details[i].CostUSD = 99
							}
							for _, values := range model.Details[i].Headers {
								if len(values) > 0 {
									values[0] = "changed"
								}
							}
						}
						if model.accounting.count > 0 {
							i := len(model.Details)
							detail := model.accountingDetailAt(i)
							detail.Tokens.InputTokens = 123
							model.setAccountingDetailAt(i, detail)
							model.removeAccountingDetailAt(i)
						}
					}
				}
				s.maxDetailsPerModel, s.exportMaxRecords = 1, 2
				s.retention = 24 * time.Hour
				s.pruneLocked(time.Now().Add(31*24*time.Hour), true)
				s.mu.Unlock()
				if _, err := s.UpsertModelPrice("model-000", ModelPrice{Prompt: 100}); err != nil {
					t.Error(err)
				}
			}()
			var output bytes.Buffer
			err = view.write(&output)
			mutation.Wait()
			if err != nil {
				t.Fatal(err)
			}
			got := decodeUsageExport(t, output.Bytes())
			if !reflect.DeepEqual(got, decodeUsageExport(t, wantJSON)) {
				t.Fatal("streamed v1 backup changed the legacy fields, ledger, config, counts or frozen values")
			}
			if count > 0 && !bytes.Contains(output.Bytes(), []byte("9007199254740993")) {
				t.Fatal("backup rounded an int64")
			}
		})
	}
}

func TestUsageExportViewReconcilesBeforeFreezing(t *testing.T) {
	s := NewRequestStatistics()
	fallback, native := protocolFallbackTestRecords()
	s.Record(fallback)
	s.Record(native)
	for pass := 0; pass < 2; pass++ {
		view, err := s.captureUsageExport(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var raw bytes.Buffer
		if err := view.write(&raw); err != nil {
			t.Fatal(err)
		}
		payload := decodeUsageExport(t, raw.Bytes())
		if payload.DetailCount != 1 {
			t.Fatalf("unreconciled backup detail_count=%d", payload.DetailCount)
		}
		assertProtocolFallbackMergedSnapshot(t, payload.Usage, native.ReasoningEffort)
	}
}

func TestUsageBackupReimportIntoOriginalInstanceDoesNotDuplicateClientHashes(t *testing.T) {
	s := NewRequestStatistics()
	s.retention, s.maxDetailsPerModel, s.dedupWindow = 0, 1, 0
	now := time.Now().Add(-time.Minute)
	for i := 0; i < 6; i++ {
		s.Record(UsageRecord{Provider: "openai", Model: "model", Source: "validation", APIKey: fmt.Sprintf("synthetic-client-%d", i%3),
			RequestedAt: now.Add(time.Duration(i) * time.Second), Detail: UsageDetail{InputTokens: int64(i + 1)}})
	}
	view, err := s.captureUsageExport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if err := view.write(&body); err != nil {
		t.Fatal(err)
	}
	backup := decodeUsageExport(t, body.Bytes())
	// The live instance still has hashed client identities. An empty-instance
	// round trip cannot expose a hash being discarded before duplicate lookup.
	s.Record(UsageRecord{Provider: "openai", Model: "model", Source: "validation", APIKey: "synthetic-client-0", RequestedAt: time.Now(), Detail: UsageDetail{InputTokens: 99}})
	for pass := 0; pass < 2; pass++ {
		result := s.MergeSnapshot(backup.Usage)
		if result.Added != 0 || result.Skipped != 6 || s.totalRequests != 7 || s.countAccountingLocked() != 7 {
			t.Fatalf("backup duplicated original hashed clients: result=%+v total=%d", result, s.totalRequests)
		}
	}
}

func TestUsageBackupImportedHashMatchesOnlyExistingFingerprint(t *testing.T) {
	s := NewRequestStatistics()
	s.retention, s.dedupWindow = 0, 0
	now := time.Now().Add(-time.Minute)
	// Different real clients can have the same masked display and timestamp.
	for _, key := range []string{"sk-first-test-client-xx", "sk-second-test-client-xx"} {
		s.Record(UsageRecord{Provider: "openai", Model: "model", Source: "validation", APIKey: key,
			RequestedAt: now, Detail: UsageDetail{InputTokens: 12, OutputTokens: 8}})
	}
	backup := s.Snapshot()
	result := s.MergeSnapshot(backup)
	if result.Added != 0 || result.Skipped != 2 || s.totalRequests != 2 {
		t.Fatal("same-display clients were duplicated or confused")
	}
	for apiName, api := range backup.APIs {
		for modelName, model := range api.Models {
			original := model.Details[0]
			for _, mutate := range []func(*RequestDetail){
				func(d *RequestDetail) { d.Timestamp = d.Timestamp.Add(time.Second) },
				func(d *RequestDetail) { d.Tokens.OutputTokens++; d.Tokens.TotalTokens++ },
				func(d *RequestDetail) { d.AuthIndex = "different-credential" },
				func(d *RequestDetail) { d.APIKeyHash = "external-instance-hash" },
			} {
				detail := original
				mutate(&detail)
				incoming := StatisticsSnapshot{APIs: map[string]APISnapshot{apiName: {Models: map[string]ModelSnapshot{modelName: {Details: []RequestDetail{detail}}}}}}
				if result := s.MergeSnapshot(incoming); result.Added != 1 || result.Skipped != 0 {
					t.Fatalf("a different request/identity was suppressed: %+v", result)
				}
				if result := s.MergeSnapshot(incoming); result.Added != 0 || result.Skipped != 1 {
					t.Fatalf("legacy normalized identity no longer deduplicates: %+v", result)
				}
			}
		}
	}
	if s.totalRequests != 6 {
		t.Fatalf("identity checks changed counts: %d", s.totalRequests)
	}
}

func TestUsageBackupReimportRetainsExpiryCountSemantics(t *testing.T) {
	s := NewRequestStatistics()
	s.retention = time.Hour
	s.Record(UsageRecord{Provider: "openai", Model: "model", APIKey: "synthetic-client", RequestedAt: time.Now().Add(-time.Minute)})
	backup := s.Snapshot()
	s.retention = time.Nanosecond
	result := s.MergeSnapshot(backup)
	if result.Added != 0 || result.Skipped != 0 || result.IgnoredByRetention != 1 || s.totalRequests != 0 {
		t.Fatalf("hash lookup bypassed retention accounting: %+v, total=%d", result, s.totalRequests)
	}
}

func TestUsageExportJobLifecycleAndImportRoundTrip(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("gzip=%t", compressed), func(t *testing.T) {
			oldStats, oldJobs := stats, dashboardExportJobs
			stats, dashboardExportJobs = usageExportTestStats(1200), newDashboardExportJobManager()
			t.Cleanup(func() {
				dashboardExportJobs.close()
				stats, dashboardExportJobs = oldStats, oldJobs
			})
			var created, completed dashboardExportJobResponse
			response := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{Method: "POST", Path: "/usage/export-jobs",
				Query: map[string][]string{"gzip": {strconv.FormatBool(compressed)}, "range": {"7h"}, "model": {"does-not-exist"}, "limit": {"1"}, "json_rows": {"true"}}}), &created)
			if response.StatusCode != http.StatusAccepted || created.ID == "" || created.Kind != dashboardExportKindUsage {
				t.Fatalf("usage backup negotiation failed: %+v", created)
			}
			query := map[string][]string{"id": {created.ID}}
			waitForTestCondition(t, func() bool {
				decodeManagementResponse(t, invokeManagement(t, ManagementRequest{Method: "GET", Path: "/usage/export-jobs", Query: query}), &completed)
				return completed.Status == dashboardExportJobSucceeded || completed.Status == dashboardExportJobFailed
			})
			if completed.Status != dashboardExportJobSucceeded || completed.Kind != dashboardExportKindUsage || completed.Total != 1200 || completed.Exported != 1200 || completed.Truncated || completed.Limit != 0 || completed.JSONRows || completed.Version == "" {
				t.Fatalf("incomplete backup: %+v", completed)
			}
			if completed.DownloadPath != "/usage/export-download?id="+created.ID || completed.Format != "json" || completed.Gzip != compressed {
				t.Fatalf("wrong file negotiation: %+v", completed)
			}
			// Event routes must neither expose nor delete a full backup.
			for _, req := range []ManagementRequest{
				{Method: "GET", Path: "/dashboard-events-export-jobs", Query: query},
				{Method: "DELETE", Path: "/dashboard-events-export-jobs", Query: query},
				{Method: "GET", Path: "/dashboard-events-export-download", Query: query},
			} {
				if response := decodeManagementResponse(t, invokeManagement(t, req), nil); response.StatusCode != http.StatusNotFound {
					t.Fatal("job kind isolation failed")
				}
			}
			var jobs dashboardExportJobListResponse
			decodeManagementResponse(t, invokeManagement(t, ManagementRequest{Method: "GET", Path: "/usage/export-jobs"}), &jobs)
			if len(jobs.Jobs) != 1 || jobs.Jobs[0].ID != created.ID {
				t.Fatal("usage backup omitted from its list")
			}
			decodeManagementResponse(t, invokeManagement(t, ManagementRequest{Method: "GET", Path: "/dashboard-events-export-jobs"}), &jobs)
			if len(jobs.Jobs) != 0 {
				t.Fatal("usage backup appeared in event job list")
			}
			stats.Record(UsageRecord{Provider: "later", Model: "later", RequestedAt: time.Now()})
			download := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{Method: "GET", Path: "/usage/export-download", Query: query}), nil)
			if download.StatusCode != http.StatusOK || len(download.Body) != completed.BodyBytes || !strings.Contains(download.Headers["Content-Disposition"][0], "usage-export-") {
				t.Fatal("whole backup download failed")
			}
			var assembled bytes.Buffer
			for offset := 0; offset < completed.BodyBytes; {
				chunkQuery := map[string][]string{"id": {created.ID}, "chunk": {"1"}, "offset": {strconv.Itoa(offset)}, "length": {"9971"}, "version": {completed.Version}}
				raw := invokeManagement(t, ManagementRequest{Method: "GET", Path: "/usage/export-download", Query: chunkQuery})
				var chunk dashboardExportChunk
				response := decodeManagementResponse(t, raw, &chunk)
				if response.StatusCode != 200 || chunk.Offset != int64(offset) || chunk.Total != int64(completed.BodyBytes) || chunk.Version != completed.Version || chunk.Checksum != fmt.Sprintf("%08x", crc32.ChecksumIEEE(chunk.Data)) {
					t.Fatal("usage backup chunk failed validation")
				}
				if retry := invokeManagement(t, ManagementRequest{Method: "GET", Path: "/usage/export-download", Query: chunkQuery}); !bytes.Equal(raw, retry) {
					t.Fatal("retry changed frozen backup bytes")
				}
				assembled.Write(chunk.Data)
				offset += len(chunk.Data)
			}
			if !bytes.Equal(download.Body, assembled.Bytes()) {
				t.Fatal("chunked backup differs from complete file")
			}
			body := download.Body
			if compressed {
				reader, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				body, err = io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
			}
			payload := decodeUsageExport(t, body)
			if payload.Version != 1 || payload.DetailCount != 1200 || payload.Usage.TotalRequests != 1200 || len(body) != completed.RawBytes || payload.Config.ExportMaxRecords != 1 {
				t.Fatal("backup lost full history or changed after a later record")
			}
			var visible, archived int
			for _, api := range payload.Usage.APIs {
				for _, model := range api.Models {
					visible += len(model.Details)
					archived += len(model.Accounting)
				}
			}
			if visible+archived != 1200 || archived == 0 || stats.RuntimeStatus().EventsExportRequests != 0 {
				t.Fatal("backup omitted the ledger or polluted event-export metrics")
			}
			dashboardExportJobs.wg.Wait()
			stats = NewRequestStatistics()
			stats.retention, stats.maxDetailsPerModel = 0, 32
			for pass := 0; pass < 2; pass++ {
				raw, err := handleImportUsage(body)
				if err != nil {
					t.Fatal(err)
				}
				var imported ImportResponse
				response := decodeManagementResponse(t, raw, &imported)
				wantAdded := int64(1200)
				if pass == 1 {
					wantAdded = 0
				}
				if response.StatusCode != 200 || imported.Added != wantAdded || imported.TotalRequests != 1200 || stats.countAccountingLocked() != 1200 {
					t.Fatalf("backup import changed counts: %+v", imported)
				}
			}
			job, _ := dashboardExportJobs.get(created.ID)
			response = decodeManagementResponse(t, invokeManagement(t, ManagementRequest{Method: "DELETE", Path: "/usage/export-jobs", Query: query}), nil)
			if response.StatusCode != http.StatusOK {
				t.Fatal("backup cleanup failed")
			}
			if _, err := os.Stat(job.FilePath); !os.IsNotExist(err) {
				t.Fatal("backup file was not removed")
			}
			if response := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{Method: "GET", Path: "/usage/export-download", Query: query}), nil); response.StatusCode != http.StatusNotFound {
				t.Fatal("deleted backup is still downloadable")
			}
		})
	}
}

func TestUsageExportJobsShareCapacityAndCancelQueuedWork(t *testing.T) {
	old := stats
	stats = NewRequestStatistics()
	t.Cleanup(func() { stats = old })
	m := newDashboardExportJobManager()
	defer m.close()
	stats.mu.Lock()
	var created []dashboardExportJob
	for _, kind := range []string{dashboardExportKindUsage, dashboardExportKindEvents} {
		job, status, message := m.createKind(kind, EventsQuery{}, dashboardEventsExportOptions{Format: dashboardExportJSON})
		if status != 202 {
			stats.mu.Unlock()
			t.Fatalf("create: %d %s", status, message)
		}
		created = append(created, job)
	}
	for _, kind := range []string{dashboardExportKindUsage, dashboardExportKindEvents} {
		if _, status, _ := m.createKind(kind, EventsQuery{}, dashboardEventsExportOptions{}); status != 429 {
			stats.mu.Unlock()
			t.Fatal("event and usage jobs did not share the active capacity")
		}
	}
	for _, job := range created {
		if !m.deleteKind(job.ID, job.Kind) {
			stats.mu.Unlock()
			t.Fatal("cancellation failed")
		}
	}
	stats.mu.Unlock()
	m.wg.Wait()
	for _, job := range created {
		for _, file := range []string{job.FilePath, job.FilePath + ".tmp"} {
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatal("canceled backup left an untracked file")
			}
		}
	}
	m.mu.Lock()
	if m.workers != 0 {
		m.mu.Unlock()
		t.Fatal("canceled workers retained capacity")
	}
	for i := 0; i < dashboardExportJobMaxStored; i++ {
		id := strconv.Itoa(i)
		kind := dashboardExportKindEvents
		if i%2 == 0 {
			kind = dashboardExportKindUsage
		}
		m.jobs[id] = &dashboardExportJob{ID: id, Kind: kind, Status: dashboardExportJobSucceeded, ExpiresAt: time.Now().Add(time.Hour), FilePath: filepath.Join(t.TempDir(), id)}
	}
	m.mu.Unlock()
	for _, kind := range []string{dashboardExportKindUsage, dashboardExportKindEvents} {
		if _, status, _ := m.createKind(kind, EventsQuery{}, dashboardEventsExportOptions{}); status != 429 {
			t.Fatal("event and usage jobs did not share retained capacity")
		}
	}
}

func TestUsageBackupCancellationDoesNotWaitForStatisticsLock(t *testing.T) {
	previous := stats
	stats = NewRequestStatistics()
	m := newDashboardExportJobManager()
	stats.mu.Lock()
	locked := true
	defer func() {
		if locked {
			stats.mu.Unlock()
		}
		m.close()
		stats = previous
	}()
	job, status, message := m.createKind(dashboardExportKindUsage, EventsQuery{}, dashboardEventsExportOptions{Format: dashboardExportJSON})
	if status != 202 {
		t.Fatalf("create: %d %s", status, message)
	}
	waitForTestCondition(t, func() bool {
		current, _ := m.get(job.ID)
		return current.Status == dashboardExportJobRunning
	})
	if !m.deleteKind(job.ID, dashboardExportKindUsage) {
		t.Fatal("delete failed")
	}
	finished := make(chan struct{})
	go func() { m.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		stats.mu.Unlock()
		locked = false
		<-finished
		t.Fatal("canceled backup worker remained blocked on the statistics lock")
	}
	for _, file := range []string{job.FilePath, job.FilePath + ".tmp"} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatal("canceled backup retained a file")
		}
	}
}

func TestUsageExportFailuresAndCancellation(t *testing.T) {
	oldStats, oldJobs := stats, dashboardExportJobs
	stats, dashboardExportJobs = usageExportTestStats(1200), newDashboardExportJobManager()
	t.Cleanup(func() {
		dashboardExportJobs.close()
		stats, dashboardExportJobs = oldStats, oldJobs
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	file := filepath.Join(t.TempDir(), "canceled")
	if _, err := encodeUsageExportFile(dashboardEventsExportOptions{ctx: ctx}, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled backup: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("pre-canceled backup created a file")
	}
	if _, err := fileSHA256Context(ctx, file); !errors.Is(err, context.Canceled) {
		t.Fatal("checksum ignored cancellation")
	}
	view, err := stats.captureUsageExport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected backup write failure")
	if err := view.write(snapshotFailWriter{failure}); !errors.Is(err, failure) {
		t.Fatal("backup hid a write error")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	w := &reviewCancelWriter{cancel: cancel}
	if err := view.write(&exportContextWriter{ctx: ctx, writer: w}); !errors.Is(err, context.Canceled) {
		t.Fatal("backup ignored mid-write cancellation")
	}
	if _, err := encodeDashboardExportFile(dashboardEventsExportOptions{}, file, func(io.Writer) (dashboardExportFileResult, error) {
		return dashboardExportFileResult{}, failure
	}); !errors.Is(err, failure) {
		t.Fatal("file encoder hid a failure")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("failed encoder retained a partial file")
	}
	stats.costByDay["invalid"] = math.NaN()
	job, status, message := dashboardExportJobs.createKind(dashboardExportKindUsage, EventsQuery{}, dashboardEventsExportOptions{Format: dashboardExportJSON})
	if status != 202 {
		t.Fatalf("create: %d %s", status, message)
	}
	waitForTestCondition(t, func() bool {
		current, _ := dashboardExportJobs.get(job.ID)
		return current.Status == dashboardExportJobFailed
	})
	response := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{Method: "GET", Path: "/usage/export-download", Query: map[string][]string{"id": {job.ID}}}), nil)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatal("failed backup exposed a partial download")
	}
	dashboardExportJobs.wg.Wait()
	for _, file := range []string{job.FilePath, job.FilePath + ".tmp"} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatal("failed backup retained a temporary file")
		}
	}
}

func BenchmarkUsageExportPipeline(b *testing.B) {
	old := stats
	stats = buildPerformanceDataset(100000, false, time.Now())
	defer func() { stats = old }()
	for _, variant := range []struct {
		name   string
		legacy bool
		gzip   bool
	}{
		{name: "legacy", legacy: true},
		{name: "streamed"},
		{name: "streamed_gzip", gzip: true},
	} {
		b.Run(variant.name, func(b *testing.B) {
			file := filepath.Join(b.TempDir(), "backup.json")
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if variant.legacy {
					usage, count := stats.ReconciledSnapshot()
					payload := ExportPayload{Version: 1, ExportedAt: time.Now().UTC().Format(time.RFC3339), Plugin: pluginVersion, DetailCount: count, Config: stats.ConfigSnapshot(), Usage: usage}
					var raw []byte
					raw, err = json.Marshal(payload)
					if err == nil {
						err = os.WriteFile(file, raw, 0600)
					}
				} else {
					_, err = encodeUsageExportFile(dashboardEventsExportOptions{Format: dashboardExportJSON, Gzip: variant.gzip}, file)
				}
				if err != nil {
					b.Fatal(err)
				}
				if _, err := fileSHA256Context(context.Background(), file); err != nil {
					b.Fatal(err)
				}
			}
			info, err := os.Stat(file)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(info.Size()), "file_bytes")
		})
	}
}
