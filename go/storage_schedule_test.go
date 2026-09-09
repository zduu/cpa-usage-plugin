package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageDeadlineTracksPendingWorkAndRetries(t *testing.T) {
	now := time.Now()
	w := &storageWorkerState{cfg: storageWorkerConfig{flushInterval: 5 * time.Second, syncInterval: 10 * time.Second, snapshotInterval: time.Minute}}
	if !w.nextDeadline(now).IsZero() {
		t.Fatal("idle writer scheduled a wakeup")
	}
	w.writer = bufio.NewWriter(io.Discard)
	w.file = &os.File{} // deadline calculation does not perform I/O
	w.buffered, w.unsyncedRecords, w.snapshotRecords = 1, 1, 1
	w.lastFlush, w.firstUnsyncedRecord, w.firstSnapshotRecord = now, now, now
	if got := w.nextDeadline(now); !got.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("flush deadline = %v", got)
	}
	w.buffered = 0
	if got := w.nextDeadline(now); !got.Equal(now.Add(10 * time.Second)) {
		t.Fatalf("sync deadline = %v", got)
	}
	w.cfg.syncRecordInterval = 1
	if got := w.nextDeadline(now); !got.Equal(now) {
		t.Fatalf("record threshold was delayed: %v", got)
	}
	w.syncRetryAt = now.Add(storageRetryDelay)
	if got := w.nextDeadline(now); !got.Equal(w.syncRetryAt) {
		t.Fatalf("failed operation would busy-loop: %v", got)
	}
	w.unsyncedRecords = 0
	if got := w.nextDeadline(now); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("snapshot deadline = %v", got)
	}
	w.snapshotRecords = 0
	if !w.nextDeadline(now).IsZero() {
		t.Fatal("completed tasks kept a timer active")
	}
}

func TestStorageDeadlineFlushesLowTrafficAndDrainsOnClose(t *testing.T) {
	s := NewRequestStatistics()
	dir := t.TempDir()
	s.mu.Lock()
	s.storageEnabled, s.storageDir = true, dir
	s.storageFlush = 20 * time.Millisecond
	s.storageSnapshotInterval = time.Hour
	s.storageSnapshotRecordInterval = 0
	s.startStorageWorkerLocked()
	s.mu.Unlock()
	t.Cleanup(s.Close)
	s.Record(UsageRecord{Provider: "test", Model: "model", Detail: UsageDetail{InputTokens: 1}})
	s.Record(UsageRecord{Provider: "test", Model: "model", Detail: UsageDetail{InputTokens: 2}})
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, _, err := readPersistedStorageFile(filepath.Join(dir, storageFileName(storageDate(time.Now()))))
		if err == nil && len(entries) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("low traffic was not flushed: entries=%d error=%v", len(entries), err)
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 150; i++ {
		s.Record(UsageRecord{Provider: "test", Model: "model", Detail: UsageDetail{InputTokens: 3}})
	}
	s.Close()
	entries, invalid, err := readPersistedStorageFile(filepath.Join(dir, storageFileName(storageDate(time.Now()))))
	if err != nil || invalid != 0 || len(entries) != 152 {
		t.Fatalf("close failed to drain valid JSONL: entries=%d invalid=%d error=%v", len(entries), invalid, err)
	}
}
