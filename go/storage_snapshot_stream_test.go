package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestFrozenAccountingBlocksIsolateEveryMutation(t *testing.T) {
	var blocks accountingBlocks
	for i := 0; i < 800; i++ {
		blocks.append(accountingRecord{LatencyMs: int64(i)})
	}
	frozen := blocks.freeze()
	expected := make([][]accountingRecord, len(frozen))
	for i, block := range frozen {
		expected[i] = append([]accountingRecord(nil), block...)
	}
	first := blocks.at(0)
	if blocks.at(0) != first {
		t.Fatal("read copied shared block")
	}
	blocks.append(accountingRecord{LatencyMs: 900})
	blocks.mutableAt(0).LatencyMs = -1
	blocks.remove(255)
	blocks.truncate(17)
	for i := 0; i < 900; i++ {
		blocks.append(accountingRecord{LatencyMs: 1000 + int64(i)})
	}
	blocks.truncate(0)
	if !reflect.DeepEqual(frozen, expected) {
		t.Fatal("append/update/remove/truncate changed frozen blocks")
	}
}

func TestStreamedStorageSnapshotMatchesLegacyAndSurvivesMutation(t *testing.T) {
	for _, count := range []int{0, 100, 100000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			now := time.Now()
			s := buildPerformanceDataset(count, false, now)
			expected := s.Snapshot()
			view := s.captureStorageSnapshot()
			// Every frozen view includes exact counters and rows from the same lock.
			// Concurrent pruning exercises copy-on-write while the encoder reads.
			var workers sync.WaitGroup
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := 0; i < 50; i++ {
					s.Record(UsageRecord{Provider: "provider-0", Model: "model-000", RequestedAt: now, Detail: UsageDetail{InputTokens: 1}})
				}
				s.mu.Lock()
				for _, api := range s.apis {
					for _, model := range api.Models {
						if model.accounting.count > 0 {
							d := model.accountingDetailAt(len(model.Details))
							d.Tokens.InputTokens = 9876
							model.setAccountingDetailAt(len(model.Details), d)
							model.removeAccountingDetailAt(len(model.Details))
						}
					}
				}
				s.pruneLocked(now.Add(31*24*time.Hour), true)
				s.mu.Unlock()
			}()
			var buffer bytes.Buffer
			err := view.write(&buffer, now)
			workers.Wait()
			if err != nil {
				t.Fatal(err)
			}
			var got persistedStorageSnapshot
			if err := json.Unmarshal(buffer.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			// JSON normalizes time.Location internals, nil/empty omitted maps and
			// headers. Compare decoded contracts against the existing encoder.
			oldJSON, err := json.Marshal(persistedStorageSnapshot{Version: currentStorageSnapshotVersion, GeneratedAt: now.UTC().Format(time.RFC3339), Usage: expected})
			if err != nil {
				t.Fatal(err)
			}
			var want persistedStorageSnapshot
			if err := json.Unmarshal(oldJSON, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("streamed snapshot contract differs for %d records", count)
			}
		})
	}
}

func TestStreamedStorageSnapshotFailurePreservesPreviousFile(t *testing.T) {
	dir := t.TempDir()
	target := storageSnapshotPath(dir)
	if err := os.WriteFile(target, []byte("previous recovery source"), 0600); err != nil {
		t.Fatal(err)
	}
	s := buildBenchmarkStats(100)
	view := s.captureStorageSnapshot()
	view.metadata.CostByDay = map[string]float64{"invalid": math.NaN()}
	if err := writeStorageSnapshotViewFile(dir, view, time.Now()); err == nil {
		t.Fatal("encoding failure reported success")
	}
	if got, _ := os.ReadFile(target); string(got) != "previous recovery source" {
		t.Fatal("failed snapshot replaced recovery source")
	}
	temporary, err := filepath.Glob(filepath.Join(dir, ".snapshot-*.tmp"))
	if err != nil || len(temporary) != 0 {
		t.Fatal("failed snapshot left temporary files")
	}
	view = s.captureStorageSnapshot()
	failure := errors.New("injected write failure")
	if err := view.write(snapshotFailWriter{failure}, time.Now()); !errors.Is(err, failure) {
		t.Fatalf("lost I/O error: %v", err)
	}
	if err := writeStorageSnapshotViewFile(dir, view, time.Now()); err != nil {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	if _, err := restored.loadStorageSnapshotLocked(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if restored.totalRequests != 100 {
		t.Fatal("streamed snapshot did not restore")
	}
}

type snapshotFailWriter struct{ err error }

func (w snapshotFailWriter) Write([]byte) (int, error) { return 0, w.err }

// Controlled comparison within one executable: same dataset, file target,
// sync semantics and retained functionality, including the full ledger.
func BenchmarkStorageSnapshotPipeline(b *testing.B) {
	now := time.Now()
	s := buildPerformanceDataset(100000, false, now)
	for _, legacy := range []bool{true, false} {
		name := "streamed"
		if legacy {
			name = "legacy"
		}
		b.Run(name, func(b *testing.B) {
			dir := b.TempDir()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				if legacy {
					err = writeStorageSnapshotFile(dir, s.Snapshot(), now)
				} else {
					err = writeStorageSnapshotViewFile(dir, s.captureStorageSnapshot(), now)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStorageSnapshotCaptureMillion(b *testing.B) {
	s := buildPerformanceDataset(1000000, false, time.Now())
	for _, legacy := range []bool{true, false} {
		name := "blocks"
		if legacy {
			name = "full"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if legacy {
					_ = s.Snapshot()
				} else {
					_ = s.captureStorageSnapshot()
				}
			}
		})
	}
}
