package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

func quotaCloakedSnapshotFixture(t *testing.T, now time.Time) (StatisticsSnapshot, UsageRecord) {
	t.Helper()
	s := NewRequestStatistics()
	defer s.Close()
	r := quotaTestRecord("old-cloaked", "gpt-4o", now.Add(-time.Minute), 1_000_000)
	s.Record(r)
	quotaTestObserve(s, now, now.Add(time.Hour), .5, 18000)
	snapshot := s.Snapshot()
	const cloaked = "claude-fable-5-dd-o4-tpg"
	// Recreate storage written before decoding was introduced; Record now
	// decodes the name, so recording a cloaked model alone is not sufficient.
	a := snapshot.APIs[usageGroupKey(r)]
	m := a.Models[r.Model]
	for i := range m.Details {
		m.Details[i].Model, m.Details[i].RequestedModel = cloaked, cloaked
	}
	delete(a.Models, r.Model)
	a.Models[cloaked] = m
	snapshot.APIs[usageGroupKey(r)] = a
	for id, f := range snapshot.QuotaCycles.Facts {
		f.Model = cloaked
		snapshot.QuotaCycles.Facts[id] = f
		// Quota facts can outlive the corresponding main-ledger detail.
		f.ID, f.Timestamp = "quota-only", now.Add(-2*time.Minute)
		f.CompletedAt = f.Timestamp.Add(time.Second)
		snapshot.QuotaCycles.Facts[f.ID] = f
		break
	}
	return snapshot, r
}

func TestQuotaCloakedSnapshotLoadsAndImports(t *testing.T) {
	for _, repairCache := range []bool{false, true} {
		for _, mode := range []string{"load", "load-overlap", "import", "import-overlap"} {
			t.Run(mode+"/repair-cache="+strconv.FormatBool(repairCache), func(t *testing.T) {
				now := time.Now().UTC().Truncate(time.Second)
				snapshot, r := quotaCloakedSnapshotFixture(t, now)
				before, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				dest := NewRequestStatistics()
				defer dest.Close()
				dest.claudeCacheRepairEnabled = repairCache
				dest.modelPrices = map[string]ModelPrice{r.Model: {Prompt: 1}}
				if mode == "load-overlap" || mode == "import-overlap" {
					dest.Record(r)
				}
				if mode == "load" || mode == "load-overlap" {
					dir := t.TempDir()
					raw, err := json.Marshal(persistedStorageSnapshot{Version: currentStorageSnapshotVersion, GeneratedAt: now.Format(time.RFC3339Nano), Usage: snapshot})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(storageSnapshotPath(dir), raw, 0o600); err != nil {
						t.Fatal(err)
					}
					dest.mu.Lock()
					at, err := dest.loadStorageSnapshotLocked(dir, now)
					dest.mu.Unlock()
					if err != nil || !at.Equal(now) {
						t.Fatalf("load old snapshot: cutoff=%v err=%v", at, err)
					}
				} else if _, err := dest.mergeSnapshotChecked(snapshot); err != nil {
					t.Fatalf("import old snapshot: %v", err)
				}
				if dest.totalRequests != 1 || len(dest.quota.Facts) != 2 {
					t.Fatalf("lost or duplicated records: requests=%d facts=%d", dest.totalRequests, len(dest.quota.Facts))
				}
				for _, f := range dest.quota.Facts {
					if f.Model != r.Model {
						t.Fatalf("quota fact still has cloaked model: %+v", f)
					}
				}
				detail := dest.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now)
				cycle := detail.QuotaCycles[0].Groups[0].Current
				if detail.Summary.EstimatedCost != 1 || cycle.Summary.EstimatedCost != 2 {
					t.Fatalf("decoded models were not priced: main=%v quota=%v", detail.Summary.EstimatedCost, cycle.Summary.EstimatedCost)
				}
				after, err := json.Marshal(snapshot)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("import mutated the caller's snapshot")
				}
			})
		}
	}
}

func TestQuotaCloakedSnapshotStillRejectsConflictingTokens(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	snapshot, r := quotaCloakedSnapshotFixture(t, now)
	id := quotaRequestID(r)
	f := snapshot.QuotaCycles.Facts[id]
	f.Tokens.InputTokens++
	f.Tokens.TotalTokens++
	snapshot.QuotaCycles.Facts[id] = f
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	dest := NewRequestStatistics()
	defer dest.Close()
	if _, err := dest.mergeSnapshotChecked(snapshot); err == nil {
		t.Fatal("accepted conflicting quota tokens")
	}
	if dest.totalRequests != 0 || dest.quota != nil {
		t.Fatal("rejected import changed live statistics")
	}
	after, err := json.Marshal(snapshot)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected import mutated the caller's snapshot")
	}
}
