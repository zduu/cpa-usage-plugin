package main

import (
	"strings"
	"testing"
	"time"
)

func quotaOnlyWindowForTest(t *testing.T, s *RequestStatistics) quotaWindow {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quota == nil || len(s.quota.Windows) != 1 {
		t.Fatal("expected exactly one quota window")
	}
	for _, w := range s.quota.Windows {
		return w
	}
	return quotaWindow{}
}

func TestQuotaOversizedFactDoesNotBreakSnapshot(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	s.Record(quotaTestRecord("long-model", strings.Repeat("m", 1100), now.Add(-time.Minute), 5))
	s.Record(quotaTestRecord("normal", "normal-model", now.Add(-time.Minute), 7))
	snapshot := s.Snapshot()
	if err := validateQuotaSnapshot(snapshot.QuotaCycles, nil); err != nil {
		t.Fatalf("snapshot with oversized request must still load: %v", err)
	}
	if got := len(snapshot.QuotaCycles.Facts); got != 1 {
		t.Fatalf("expected only the valid fact, got %d", got)
	}
}

func TestQuotaRelativeResetMovingEarlierKeepsPrevious(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	boundary := now.Add(-time.Hour)
	quotaTestObserve(s, boundary.Add(-time.Minute), boundary, .9, 18000)
	quotaTestObserve(s, now.Add(-30*time.Minute), boundary.Add(5*time.Hour), .2, 18000)
	// A relative reset measured after a long stream lands 3 minutes earlier.
	quotaTestObserve(s, now, boundary.Add(5*time.Hour-3*time.Minute), .3, 18000)
	w := quotaOnlyWindowForTest(t, s)
	if w.Previous == nil || w.Current == nil {
		t.Fatal("drifted relative reset discarded the previous period")
	}
	if !w.Previous.End.Equal(w.Current.Start) || w.Previous.End.Sub(w.Previous.Start) != 5*time.Hour {
		t.Fatal("previous period was not aligned to the new boundary")
	}
	if err := validateQuotaSnapshot(s.Snapshot().QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaLateDriftedSampleJoinsExpiredPeriod(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(-time.Minute)
	quotaTestObserve(s, end.Add(-2*time.Hour), end, .4, 18000)
	quotaTestObserve(s, end.Add(-time.Hour), end, .5, 18000)
	s.mu.Lock()
	s.advanceQuotaCyclesLocked(now)
	s.mu.Unlock()
	quotaTestObserve(s, end.Add(-30*time.Minute), end.Add(2*time.Second), .6, 18000)
	s.mu.Lock()
	s.advanceQuotaCyclesLocked(now)
	s.mu.Unlock()
	w := quotaOnlyWindowForTest(t, s)
	if w.Current != nil || w.Previous == nil || len(w.Previous.Samples) != 3 || !w.Previous.End.Equal(end) {
		t.Fatal("late drifted sample replaced the expired period's samples")
	}
}

func TestQuotaResubmittedObservationIsNotDuplicated(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	o := quotaObservation{Provider: "claude", AuthIndex: "account-index", AuthID: "account.json", Group: "shared", Slot: "5h",
		Seconds: 18000, Reset: now.Add(time.Hour), ObservedAt: now, Used: .3}
	zone := time.FixedZone("UTC+8", 8*3600)
	s.mu.Lock()
	first := s.applyQuotaObservationLocked(o)
	// A restart assigns a new baseline; times may arrive in another zone.
	o.CollectionStartedAt = now.Add(time.Minute)
	o.Reset, o.ObservedAt = o.Reset.In(zone), o.ObservedAt.In(zone)
	second := s.applyQuotaObservationLocked(o)
	s.mu.Unlock()
	if !first || second {
		t.Fatal("identical observation was accepted twice")
	}
	if w := quotaOnlyWindowForTest(t, s); len(w.Current.Samples) != 1 {
		t.Fatalf("expected one sample, got %d", len(w.Current.Samples))
	}
}

func TestQuotaObservationsReportRejectedIndexes(t *testing.T) {
	oldStats, oldResolver := stats, resolveQuotaHostAuth
	stats = NewRequestStatistics()
	defer func() { stats.Close(); stats = oldStats; resolveQuotaHostAuth = oldResolver }()
	resolveQuotaHostAuth = func(index string) (quotaHostAuth, error) {
		return quotaHostAuth{ID: "devin-account.json", AuthIndex: index, Provider: "devin", AccountType: "oauth"}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	body := `{"version":1,"observations":[` +
		`{"provider":"unknown","auth_index":"i","auth_id":"devin-account.json","observed_at":"` + now + `","signals":{}},` +
		`{"provider":"devin","auth_index":"i","auth_id":"devin-account.json","observed_at":"` + now + `","signals":{"weekly_quota_remaining_percent":"60%","weekly_quota_reset_at":"` + time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339) + `"}}]}`
	raw, err := handleQuotaObservations([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Accepted        int   `json:"accepted"`
		RejectedIndexes []int `json:"rejected_indexes"`
	}
	decodeManagementResponse(t, raw, &result)
	if result.Accepted != 1 || len(result.RejectedIndexes) != 1 || result.RejectedIndexes[0] != 0 {
		t.Fatalf("response did not list rejected entries: %+v", result)
	}
}

func TestQuotaInvalidTombstoneDoesNotBreakSnapshot(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	s.mu.Lock()
	s.ensureQuotaLocked()
	s.removeQuotaFactLocked(RequestDetail{RecordID: strings.Repeat("r", 300), Timestamp: now})
	s.removeQuotaFactLocked(RequestDetail{RecordID: "no-time"})
	s.removeQuotaFactLocked(RequestDetail{RecordID: "valid", Timestamp: now})
	s.mu.Unlock()
	snapshot := s.Snapshot()
	if err := validateQuotaSnapshot(snapshot.QuotaCycles, nil); err != nil {
		t.Fatalf("snapshot with invalid tombstone must still load: %v", err)
	}
	if len(snapshot.QuotaCycles.Deleted) != 1 {
		t.Fatal("valid tombstone was not kept")
	}
}
