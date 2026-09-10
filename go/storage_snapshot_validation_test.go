package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStorageSnapshotInvalidHeaderDoesNotMutateStatistics(t *testing.T) {
	for _, test := range []struct {
		name, generated string
		version         int
	}{
		{"invalid-time", "bad", currentStorageSnapshotVersion},
		{"future-version", "2026-09-10T00:00:00Z", currentStorageSnapshotVersion + 1},
		{"negative-version", "2026-09-10T00:00:00Z", -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := NewRequestStatistics()
			dir := t.TempDir()
			payload, err := json.Marshal(persistedStorageSnapshot{Version: test.version, GeneratedAt: test.generated, Usage: StatisticsSnapshot{TotalRequests: 99, TotalTokens: 100, SuccessCount: 99}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(storageSnapshotPath(dir), payload, 0o600); err != nil {
				t.Fatal(err)
			}
			before := s.Snapshot()
			s.mu.Lock()
			_, err = s.loadStorageSnapshotLocked(dir, time.Now())
			s.mu.Unlock()
			if err == nil {
				t.Fatal("invalid snapshot header was accepted")
			}
			if after := s.Snapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected snapshot changed live statistics: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestStorageSnapshotInvalidHeaderPreventsWriterAndCleanup(t *testing.T) {
	for _, generated := range []string{"bad", "2026-09-10T00:00:00Z"} {
		s := NewRequestStatistics()
		dir := t.TempDir()
		version := currentStorageSnapshotVersion
		if generated != "bad" {
			version++
		}
		payload, err := json.Marshal(persistedStorageSnapshot{Version: version, GeneratedAt: generated, Usage: StatisticsSnapshot{TotalRequests: 99}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(storageSnapshotPath(dir), payload, 0o600); err != nil {
			t.Fatal(err)
		}
		oldShard := filepath.Join(dir, "usage-2000-01-01.jsonl")
		if err := os.WriteFile(oldShard, []byte("preserve recovery source"), 0o600); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.storageEnabled, s.storagePath = true, dir
		s.configureStorageLocked()
		running, lastError, target := s.storageWorkerRunning, s.storageLastError, s.storageDir
		s.mu.Unlock()
		s.Close()
		if running || lastError == "" || target != "" {
			t.Fatalf("unsafe storage activation: running=%v error=%q target=%q", running, lastError, target)
		}
		if got, err := os.ReadFile(storageSnapshotPath(dir)); err != nil || string(got) != string(payload) {
			t.Fatalf("rejected snapshot overwritten on close: err=%v", err)
		}
		if _, err := os.Stat(oldShard); err != nil {
			t.Fatalf("retention deleted recovery source: %v", err)
		}
	}
}
