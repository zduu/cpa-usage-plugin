package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnreadableSnapshotPreservesRecoverySources(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		directory     bool
	}{
		{"truncated", `{"version":2,"generated_at":"2026-09-10T00:00:00Z","usage":{"total_requests":99}`, false},
		{"future-version-with-type-error", `{"version":3,"generated_at":"2026-09-10T00:00:00Z","usage":{"total_requests":"99"}}`, false},
		{"read-error", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := storageSnapshotPath(dir)
			if tc.directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(tc.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			shard := filepath.Join(dir, "usage-2000-01-01.jsonl")
			if err := os.WriteFile(shard, []byte("recovery source"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := NewRequestStatistics()
			cfg := runtimeConfig{StorageEnabled: true, StoragePath: dir, RetentionDays: 30}
			for i := 0; i < 2; i++ {
				s.Configure(cfg)
				s.mu.RLock()
				running, lastError, target := s.storageWorkerRunning, s.storageLastError, s.storageDir
				s.mu.RUnlock()
				if running || lastError == "" || target != "" {
					t.Fatalf("unsafe storage activation: running=%v error=%q target=%q", running, lastError, target)
				}
			}
			s.Record(reviewRecord(time.Now()))
			s.Close()
			if tc.directory {
				if info, err := os.Stat(path); err != nil || !info.IsDir() {
					t.Fatalf("recovery path replaced: %v", err)
				}
			} else if raw, err := os.ReadFile(path); err != nil || string(raw) != tc.payload {
				t.Fatalf("recovery snapshot overwritten: %v", err)
			}
			if raw, err := os.ReadFile(shard); err != nil || string(raw) != "recovery source" {
				t.Fatalf("recovery shard removed: %v", err)
			}
		})
	}
}

func TestArchivedImportSurvivesJSONLRecovery(t *testing.T) {
	for _, withSnapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "log-only", true: "snapshot-and-log"}[withSnapshot], func(t *testing.T) {
			now := time.Now().Add(-time.Minute)
			s := NewRequestStatistics()
			// The destination deliberately has a larger visible-detail budget
			// than the exporting instance: archived rows must stay archived.
			s.Configure(runtimeConfig{MaxDetailsPerModel: 100, RetentionDays: 30})
			dir := t.TempDir()
			if withSnapshot {
				s.Record(reviewRecord(now.Add(-time.Minute)))
				if err := writeStorageSnapshotFile(dir, s.Snapshot(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			archived := requestDetailFromUsageRecord(reviewRecord(now), now, headerWhitelist{})
			visible := archived
			visible.Timestamp = now.Add(time.Second)
			model := ModelSnapshot{Details: []RequestDetail{visible}, Accounting: []RequestDetail{archived}}
			snapshot := StatisticsSnapshot{APIs: map[string]APISnapshot{"openai": {Models: map[string]ModelSnapshot{"review-model": model}}}}
			s.mu.Lock()
			s.storageEnabled = true
			result, records := s.mergeSnapshotLocked(snapshot, true, time.Now())
			s.mu.Unlock()
			if result.Added != 2 || len(records) != 2 {
				t.Fatalf("import failed: %+v records=%d", result, len(records))
			}
			file, err := os.Create(filepath.Join(dir, storageFileName(storageDate(time.Now()))))
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				if err := json.NewEncoder(file).Encode(record); err != nil {
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			restored := NewRequestStatistics()
			restored.Configure(runtimeConfig{MaxDetailsPerModel: 100, RetentionDays: 30, StorageEnabled: true, StoragePath: dir})
			defer restored.Close()
			if before, after := s.QueryAllEvents(EventsQuery{}), restored.QueryAllEvents(EventsQuery{}); before.Total != after.Total {
				t.Fatalf("visible records changed after recovery: %d -> %d", before.Total, after.Total)
			}
			if before, after := s.Snapshot(), restored.Snapshot(); before.TotalRequests != after.TotalRequests || before.TotalTokens != after.TotalTokens {
				t.Fatalf("accounting changed after recovery: requests %d -> %d, tokens %d -> %d", before.TotalRequests, after.TotalRequests, before.TotalTokens, after.TotalTokens)
			}
			if got := restored.QueryAllEvents(EventsQuery{}); len(got.Events) == 0 || !got.Events[0].Timestamp.Equal(visible.Timestamp) {
				t.Fatalf("recent event did not survive: %+v", got.Events)
			}
		})
	}
}
