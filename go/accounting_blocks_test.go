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
