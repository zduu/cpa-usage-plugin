package main

import (
	"reflect"
	"testing"
	"time"
)

func TestAccountingBlocksPreserveOrderAcrossRepairs(t *testing.T) {
	m := &modelStats{}
	now := time.Now()
	want := make([]RequestDetail, 0, 1025)
	for i := 0; i < 1025; i++ {
		d := RequestDetail{Model: "model", Provider: "provider", Timestamp: now.Add(time.Duration(i) * time.Second), Tokens: TokenStats{InputTokens: int64(i)}}
		m.archiveDetail(d)
		want = append(want, d)
	}
	for _, index := range []int{1024, 512, 255, 0} {
		m.removeAccountingDetailAt(index)
		want = append(want[:index], want[index+1:]...)
	}
	want[256].Endpoint = "/v1/responses"
	want[256].Correlation = &ProtocolCorrelationMeta{InputMode: "separate"}
	m.setAccountingDetailAt(256, want[256])
	if got := m.accountingSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatal("cross-block repair changed order or values")
	}
	// A snapshot owns its mutable fields even when the backing history is
	// segmented and an updated record is exactly on a block boundary.
	got := m.accountingSnapshot()
	got[256].Correlation.InputMode = "mutated"
	if m.accountingDetailAt(256).Correlation.InputMode != "separate" {
		t.Fatal("snapshot shares mutable metadata with accounting")
	}
	for m.accounting.count > 0 {
		m.removeAccountingDetailAt(m.accounting.count - 1)
	}
	if m.accounting.blocks != nil {
		t.Fatal("empty accounting retains blocks")
	}
	m.archiveDetail(want[0])
	if m.accounting.count != 1 || m.accounting.capacity() != 1 {
		t.Fatal("small model reserves an entire historical block")
	}
}

func TestVisibleDetailStorageReuseKeepsIndexesAndExportSnapshots(t *testing.T) {
	s := NewRequestStatistics()
	s.maxDetailsPerModel = 16
	now := time.Now().Add(-time.Hour)
	for i := 0; i < 20; i++ {
		s.Record(reviewRecord(now.Add(time.Duration(i) * time.Second)))
	}
	frozen := s.captureEventExport(EventsQuery{}, 0, time.Now())
	original := cloneRequestDetails(frozen.result.Events)
	// Build shared references before many prefix-reuse cycles, including
	// late insertion and duplicate timestamps; queries must follow moved rows.
	query := EventsQuery{Limit: 16, Model: "review-model"}
	_ = s.QueryEvents(query)
	for i := 20; i < 1000; i++ {
		at := now.Add(time.Duration(i-i%3) * time.Second)
		s.Record(reviewRecord(at))
		got := s.QueryEvents(query)
		want := expectedVisibleEvents(s, query, time.Now())
		if len(got.Events) != len(want) {
			t.Fatalf("step %d: visible count %d != %d", i, len(got.Events), len(want))
		}
		for j := range want {
			got.Events[j].CostUSD = nil
			if !reflect.DeepEqual(got.Events[j], want[j]) {
				t.Fatalf("step %d: stale event reference %d", i, j)
			}
		}
	}
	if !reflect.DeepEqual(frozen.result.Events, original) {
		t.Fatal("storage reuse mutated a frozen export")
	}
	if snapshot := s.Snapshot(); snapshot.TotalRequests != 1000 || s.DetailCount() != 16 {
		t.Fatalf("retention lost accounting: requests=%d details=%d", snapshot.TotalRequests, s.DetailCount())
	}
}

func TestBulkDetailReductionReleasesCapacityAndRebindsEvents(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		s := buildBenchmarkStats(16000)
		now := time.Now()
		frozen := s.captureEventExport(EventsQuery{}, 0, now)
		original := cloneRequestDetails(frozen.result.Events)
		_ = s.QueryEvents(EventsQuery{Limit: 100})
		if expiry {
			// Leave just the most recent 16 seconds: four rows in each model.
			latest := now.Add(-100 * 24 * time.Hour)
			for _, api := range s.apis {
				for _, m := range api.Models {
					if at := m.Details[len(m.Details)-1].Timestamp; at.After(latest) {
						latest = at
					}
				}
			}
			s.retention = now.Sub(latest.Add(-15 * time.Second))
		} else {
			s.maxDetailsPerModel = 4
		}
		s.pruneLocked(now, true)
		for _, api := range s.apis {
			for _, m := range api.Models {
				if len(m.Details) != 4 || cap(m.Details) > 5 || len(m.detailStorage) > 5 {
					t.Fatalf("expiry=%t retained=%d capacity=%d storage=%d", expiry, len(m.Details), cap(m.Details), len(m.detailStorage))
				}
			}
		}
		got := s.QueryEventsAt(EventsQuery{Limit: 100}, now)
		want := expectedVisibleEvents(s, EventsQuery{Limit: 100}, now)
		for i := range got.Events {
			got.Events[i].CostUSD = nil
		}
		if !reflect.DeepEqual(got.Events, want) {
			t.Fatalf("expiry=%t compacted event references changed", expiry)
		}
		expected := int64(16000)
		if expiry {
			expected = 16
		}
		if s.totalRequests != expected || s.countAccountingLocked() != expected {
			t.Fatalf("expiry=%t lost accounting: %d/%d", expiry, s.totalRequests, s.countAccountingLocked())
		}
		if !reflect.DeepEqual(frozen.result.Events, original) {
			t.Fatal("compaction changed frozen export")
		}
	}
}
