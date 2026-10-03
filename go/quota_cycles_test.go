package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func quotaTestRecord(id, model string, at time.Time, tokens int64) UsageRecord {
	return UsageRecord{RequestID: id, Provider: "claude", AuthType: "oauth", AuthID: "account.json", AuthIndex: "account-index", Model: model, RequestedAt: at, Latency: time.Second, Detail: UsageDetail{InputTokens: tokens, TotalTokens: tokens}}
}

func quotaTestObserve(s *RequestStatistics, at, reset time.Time, used float64, seconds int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyQuotaObservationLocked(quotaObservation{Provider: "claude", AuthIndex: "account-index", AuthID: "account.json", Group: "shared", Slot: "5h", Seconds: seconds, Reset: reset, ObservedAt: at, Used: used})
}

func TestQuotaSignalWindowsAreUpstreamDefined(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cases := []struct {
		provider string
		signals  map[string]string
		seconds  int64
		used     float64
	}{
		{"claude", map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.4", "Anthropic-Ratelimit-Unified-7d-Reset": now.Add(time.Hour).Format(time.RFC3339)}, 604800, .4},
		{"codex", map[string]string{"X-Codex-Primary-Used-Percent": "40", "X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "3600"}, 604800, .4},
		{"devin", map[string]string{"weekly_quota_remaining_percent": "60%", "weekly_quota_reset_at": now.Add(time.Hour).Format(time.RFC3339)}, 604800, .4},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			got := parseQuotaSignals(quotaSignalsInput{Provider: tc.provider, AuthIndex: "index", AuthID: "auth", ObservedAt: now, Signals: tc.signals})
			if len(got) != 1 || got[0].Seconds != tc.seconds || math.Abs(got[0].Used-tc.used) > 1e-12 {
				t.Fatalf("got %#v", got)
			}
			if got[0].Seconds == 18000 {
				t.Fatal("invented a 5h window")
			}
		})
	}
	invalid := parseQuotaSignals(quotaSignalsInput{Provider: "codex", AuthIndex: "index", ObservedAt: now, Signals: map[string]string{"x-codex-primary-used-percent": "NaN", "x-codex-primary-window-minutes": "300", "x-codex-primary-reset-at": fmt.Sprint(now.Add(time.Hour).Unix())}})
	if len(invalid) != 0 {
		t.Fatal("accepted NaN")
	}
}

func TestQuotaRealPreviousAndCurrentModelDistribution(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	s.startedAt = start.Add(-6 * time.Hour)
	s.modelPrices = map[string]ModelPrice{"old-model": {Prompt: 30}, "new-model": {Prompt: 20}}
	s.Record(quotaTestRecord("old", "old-model", start.Add(-time.Hour), 1_000_000))
	quotaTestObserve(s, start.Add(-time.Minute), start, .6, 18000)
	s.Record(quotaTestRecord("new", "new-model", now.Add(-time.Minute), 1_000_000))
	quotaTestObserve(s, now, start.Add(5*time.Hour), .4, 18000)
	api := usageGroupKey(quotaTestRecord("", "new-model", now, 0))
	result := s.QueryAPIDetailAt(api, "24h", 50, 20, now)
	if len(result.QuotaCycles) != 1 || len(result.QuotaCycles[0].Groups) != 1 {
		t.Fatalf("missing cycles: %+v", result.QuotaCycles)
	}
	group := result.QuotaCycles[0].Groups[0]
	if group.Current == nil || group.Previous == nil {
		t.Fatal("missing current/previous")
	}
	if group.Current.Summary.EstimatedCost != 20 || group.Previous.Summary.EstimatedCost != 30 {
		t.Fatalf("costs %+v", group)
	}
	if group.Current.EstimatedTotalUSD == nil || *group.Current.EstimatedTotalUSD != 50 {
		t.Fatalf("estimate %+v", group.Current)
	}
	if *group.Previous.UsedPercent != 60 || group.Previous.ModelStats[0].Model != "old-model" || group.Current.ModelStats[0].Model != "new-model" {
		t.Fatal("mixed period model usage")
	}
	raw, _ := json.Marshal(group.Previous)
	if bytes.Contains(raw, []byte("estimated_total_usd")) {
		t.Fatal("previous has an estimated quota")
	}
	// Window accounting deliberately ignores the page's client/range filters.
	filtered := s.QueryAPIDetailForClientAPIAt(api, "7h", "missing-client", 10, 10, now)
	if filtered.QuotaCycles[0].Groups[0].Current.Summary.TotalRequests != 1 {
		t.Fatal("client filter cut the credential quota")
	}
}

func TestQuotaPartialCycleCalibrationAndUnknownValues(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	s.startedAt = now.Add(-30 * time.Minute)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 5}}
	s.Record(quotaTestRecord("first", "m", now.Add(-20*time.Minute), 1_000_000))
	quotaTestObserve(s, now.Add(-10*time.Minute), start.Add(5*time.Hour), .3, 18000)
	s.Record(quotaTestRecord("second", "m", now.Add(-5*time.Minute), 1_000_000))
	quotaTestObserve(s, now, start.Add(5*time.Hour), .4, 18000)
	result := s.QueryAPIDetailAt(usageGroupKey(quotaTestRecord("", "m", now, 0)), "all", 10, 10, now)
	c := result.QuotaCycles[0].Groups[0].Current
	if c.EstimatedTotalUSD == nil || math.Abs(*c.EstimatedTotalUSD-50) > 1e-9 {
		t.Fatalf("bad delta estimate %+v", c)
	}
	if c.Summary.EstimatedCost != 10 {
		t.Fatal("invented missing costs")
	}
	w := quotaWindow{Seconds: 18000}
	p := &quotaPeriod{Start: start, End: now.Add(time.Hour), Samples: []quotaObservation{{Used: 0, ObservedAt: now}}}
	if total, _ := quotaEstimate(w, p, nil, s.PricingSnapshot(), start, now); total != nil {
		t.Fatal("zero denominator was estimated")
	}
}

func TestQuotaSnapshotRetentionRepricingAndReplay(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	s.startedAt = start.Add(-6 * time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	s.maxDetailsPerModel = 1
	for i := 0; i < 3; i++ {
		s.Record(quotaTestRecord(fmt.Sprint(i), "m", start.Add(-time.Duration(i+1)*time.Minute), 1_000_000))
	}
	quotaTestObserve(s, start.Add(-time.Second), start, .6, 18000)
	s.Record(quotaTestRecord("current", "m", now.Add(-time.Minute), 1_000_000))
	quotaTestObserve(s, now, start.Add(5*time.Hour), .4, 18000)
	api := usageGroupKey(quotaTestRecord("", "m", now, 0))
	s.mu.Lock()
	s.retention = 30 * time.Minute
	s.pruneExpiredLocked(now)
	s.mu.Unlock()
	result := s.QueryAPIDetailAt(api, "all", 10, 10, now)
	if result.QuotaCycles[0].Groups[0].Previous.Summary.TotalRequests != 3 {
		t.Fatal("main retention erased the previous cycle")
	}
	view := s.captureStorageSnapshot()
	var buffer bytes.Buffer
	if err := view.write(&buffer, now); err != nil {
		t.Fatal(err)
	}
	var disk persistedStorageSnapshot
	if err := json.Unmarshal(buffer.Bytes(), &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Version != 3 || len(disk.Usage.QuotaCycles.Facts) != 4 {
		t.Fatal("snapshot omitted facts")
	}
	if err := validateQuotaSnapshot(disk.Usage.QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
	copy := NewRequestStatistics()
	defer copy.Close()
	copy.modelPrices = map[string]ModelPrice{"m": {Prompt: 2}}
	copy.mu.Lock()
	copy.restoreStorageSnapshotLocked(disk.Usage, now)
	copy.restoreQuotaSnapshotLocked(disk.Usage)
	copy.mu.Unlock()
	result = copy.QueryAPIDetailAt(api, "all", 10, 10, now)
	if result.QuotaCycles[0].Groups[0].Previous.Summary.EstimatedCost != 6 {
		t.Fatal("archived cycle did not reprice")
	}
	copy.MergeSnapshot(disk.Usage)
	copy.MergeSnapshot(disk.Usage)
	result = copy.QueryAPIDetailAt(api, "all", 10, 10, now)
	if result.QuotaCycles[0].Groups[0].Previous.Summary.TotalRequests != 3 {
		t.Fatal("duplicate backup double counted")
	}
	// The frozen view must not change when newer facts arrive.
	s.Record(quotaTestRecord("late", "m", now.Add(-time.Second), 1_000_000))
	buffer.Reset()
	if err := view.write(&buffer, now); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buffer.Bytes(), &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Usage.QuotaCycles.Facts) != 4 {
		t.Fatal("mutable frozen snapshot")
	}
}

func TestQuotaJournalPreservesIdenticalIndependentRequests(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	var records []persistedDetail
	for _, id := range []string{"one", "two"} {
		r := quotaTestRecord(id, "m", now, 7)
		d := requestDetailFromUsageRecord(r, now, headerWhitelist{})
		records = append(records, persistedDetail{API: usageGroupKey(r), Model: "m", Detail: d})
	}
	o := quotaObservation{Provider: "claude", AuthIndex: "account-index", AuthID: "account.json", Group: "shared", Slot: "5h", Seconds: 18000, Reset: now.Add(time.Hour), ObservedAt: now, Used: .4}
	records = append(records, persistedDetail{Kind: "quota", QuotaObservations: []quotaObservation{o}})
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range records {
		if err := json.NewEncoder(file).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	file.Close()
	if err := s.replayStorageLocked(path); err != nil {
		t.Fatal(err)
	}
	if s.totalRequests != 2 {
		t.Fatalf("identical requests lost: %d", s.totalRequests)
	}
	if err := s.replayStorageLocked(path); err != nil {
		t.Fatal(err)
	}
	if s.totalRequests != 2 || len(s.quota.Facts) != 2 {
		t.Fatal("journal replay was not idempotent")
	}
}

func TestQuotaManagementIdentityAndAnonymousResourceBoundary(t *testing.T) {
	oldStats, oldResolver := stats, resolveQuotaHostAuth
	stats = NewRequestStatistics()
	defer func() { stats.Close(); stats = oldStats; resolveQuotaHostAuth = oldResolver }()
	resolveQuotaHostAuth = func(index string) (quotaHostAuth, error) {
		return quotaHostAuth{ID: "a.json", AuthIndex: index, Provider: "devin"}, nil
	}
	now := time.Now().UTC().Truncate(time.Second)
	input := quotaSignalsInput{Provider: "devin", AuthIndex: "index", AuthID: "a.json", ObservedAt: now, Signals: map[string]string{"weekly_quota_remaining_percent": "60%", "weekly_quota_reset_at": now.Add(time.Hour).Format(time.RFC3339)}}
	body, _ := json.Marshal(map[string]any{"version": 1, "observations": []quotaSignalsInput{input}})
	for i := 0; i < 2; i++ {
		raw, err := handleQuotaObservations(body)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Version uint64 `json:"quota_version"`
		}
		decodeManagementResponse(t, raw, &result)
	}
	if len(stats.quota.Windows) != 1 || stats.totalRequests != 0 {
		t.Fatal("observation counted as a request")
	}
	raw, err := handleManagement(mustMarshal(ManagementRequest{Method: "POST", Path: "/v0/resource/plugins/usage-dashboard-zduu/dashboard-quota-observations", Body: body}))
	if err != nil || decodeManagementResponse(t, raw, nil).StatusCode != 404 {
		t.Fatal("resource alias accepted quota data")
	}
}

func TestQuotaImportPreservesIdenticalIndependentRequests(t *testing.T) {
	source := NewRequestStatistics()
	defer source.Close()
	at := time.Now().UTC().Add(-time.Minute)
	for _, id := range []string{"first", "second"} {
		source.Record(quotaTestRecord(id, "m", at, 7))
	}
	backup := source.Snapshot()
	destination := NewRequestStatistics()
	defer destination.Close()
	if result := destination.MergeSnapshot(backup); result.Added != 2 {
		t.Fatalf("independent requests collapsed: %+v", result)
	}
	if result := destination.MergeSnapshot(backup); result.Added != 0 || destination.totalRequests != 2 {
		t.Fatalf("backup not idempotent: %+v", result)
	}
	if len(destination.quota.Facts) != 2 {
		t.Fatal("quota and main accounting disagree")
	}
}

func TestQuotaEstimateRecalibratesAfterRestartOrDecrease(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	s := NewRequestStatistics()
	defer s.Close()
	s.startedAt = start.Add(-time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 5}}
	s.Record(quotaTestRecord("old", "m", now.Add(-30*time.Minute), 1_000_000))
	quotaTestObserve(s, now.Add(-20*time.Minute), start.Add(5*time.Hour), .3, 18000)
	s.Record(quotaTestRecord("new", "m", now.Add(-15*time.Minute), 1_000_000))
	quotaTestObserve(s, now.Add(-10*time.Minute), start.Add(5*time.Hour), .4, 18000)
	api := usageGroupKey(quotaTestRecord("", "m", now, 0))
	s.quota.StartedAt = now.Add(-5 * time.Minute)
	if got := s.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current.EstimatedTotalUSD; got != nil {
		t.Fatalf("reused pre-restart calibration: %v", *got)
	}
	quotaTestObserve(s, now.Add(-4*time.Minute), start.Add(5*time.Hour), .5, 18000)
	if got := s.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current.EstimatedTotalUSD; got != nil {
		t.Fatal("one post-restart sample is not a pair")
	}
	s.Record(quotaTestRecord("after", "m", now.Add(-3*time.Minute), 1_000_000))
	quotaTestObserve(s, now.Add(-2*time.Minute), start.Add(5*time.Hour), .6, 18000)
	got := s.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current.EstimatedTotalUSD
	if got == nil || math.Abs(*got-50) > 1e-9 {
		t.Fatalf("post-restart pair not calibrated: %v", got)
	}
	s.quota.StartedAt = start.Add(-time.Hour)
	quotaTestObserve(s, now, start.Add(5*time.Hour), .2, 18000)
	if got := s.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current.EstimatedTotalUSD; got != nil {
		t.Fatal("usage decrease reused the cycle-start estimate")
	}
}

func TestQuotaEstimateExcludesUnfinishedRequests(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := NewRequestStatistics()
	defer s.Close()
	s.startedAt = now.Add(-6 * time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 20}}
	r := quotaTestRecord("short", "m", now.Add(-2*time.Minute), 1_000_000)
	s.Record(r)
	r = quotaTestRecord("long", "m", now.Add(-time.Minute), 1_000_000)
	r.Latency = 2 * time.Minute
	s.Record(r)
	quotaTestObserve(s, now, now.Add(time.Hour), .4, 18000)
	c := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	if c.Summary.EstimatedCost != 40 || c.EstimatedTotalUSD == nil || *c.EstimatedTotalUSD != 50 {
		t.Fatalf("observation included later completion: %+v", c)
	}
}

func TestQuotaMetadataReplayDoesNotCreateFacts(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC()
	r := quotaTestRecord("metadata", "m", now, 7)
	d := requestDetailFromUsageRecord(r, now, headerWhitelist{})
	row := persistedDetail{API: usageGroupKey(r), Model: "m", Detail: d, MetadataOnly: true}
	if err := s.replayPersistedDetailsLocked([]persistedDetail{row}, 0, false, now); err != nil {
		t.Fatal(err)
	}
	if s.quota != nil && len(s.quota.Facts) != 0 {
		t.Fatal("metadata created an economic fact")
	}
}

func TestQuotaCycleBoundaryLateUsageAndGap(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(-time.Hour)
	r := quotaTestRecord("at-boundary", "m", end, 7)
	s.Record(r)
	quotaTestObserve(s, end.Add(-time.Minute), end, .6, 18000)
	quotaTestObserve(s, now, end.Add(5*time.Hour), .1, 18000)
	s.Record(quotaTestRecord("late", "old", end.Add(-time.Minute), 11))
	group := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0]
	if group.Current.Summary.TotalTokens != 7 || group.Previous.Summary.TotalTokens != 11 {
		t.Fatal("boundary or late usage assigned to wrong period")
	}
	s.mu.Lock()
	s.advanceQuotaCyclesLocked(end.Add(5 * time.Hour))
	s.mu.Unlock()
	group = s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, end.Add(5*time.Hour)).QuotaCycles[0].Groups[0]
	if group.Current != nil || group.Previous == nil || *group.Previous.UsedPercent != 10 {
		t.Fatal("idle rollover invented usage")
	}
	quotaTestObserve(s, end.Add(11*time.Hour), end.Add(15*time.Hour), .2, 18000)
	group = s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, end.Add(11*time.Hour)).QuotaCycles[0].Groups[0]
	if group.Previous != nil {
		t.Fatal("unobserved gap was treated as an adjacent previous period")
	}
}

func TestQuotaExplicitRevocationKeepsHistoryAndPartialSignalsDoNotRevoke(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("one", "m", now.Add(-time.Minute), 7)
	s.Record(r)
	quotaTestObserve(s, now.Add(-time.Second), now.Add(time.Hour), .4, 18000)
	input := quotaSignalsInput{Provider: "claude", AuthIndex: r.AuthIndex, AuthID: r.AuthID, ObservedAt: now,
		Signals: map[string]string{"anthropic-ratelimit-unified-7d-utilization": ".5", "anthropic-ratelimit-unified-7d-reset": now.Add(time.Hour).Format(time.RFC3339)}}
	for _, observation := range parseQuotaSignals(input) {
		s.applyQuotaObservationLocked(observation)
	}
	if got := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now); len(got.QuotaCycles[0].Groups) != 2 {
		t.Fatal("partial signal revoked the 5h window")
	}
	input.Signals = map[string]string{"anthropic-ratelimit-unified-5h-utilization": "null"}
	for _, observation := range parseQuotaSignals(input) {
		s.applyQuotaObservationLocked(observation)
	}
	got := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now)
	if len(got.QuotaCycles[0].Groups) != 1 || got.QuotaCycles[0].Groups[0].WindowSeconds != 604800 {
		t.Fatal("explicitly revoked window remains visible")
	}
	if len(s.quota.Windows) != 2 || len(s.quota.Facts) != 1 {
		t.Fatal("revocation deleted retained history")
	}
}

func TestQuotaUnknownPricesRemainNullAndZeroPricesRemainZero(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			s.startedAt = now.Add(-6 * time.Hour)
			if known {
				s.modelPrices = map[string]ModelPrice{"m": {Prompt: 0}}
			}
			r := quotaTestRecord("one", "m", now.Add(-time.Minute), 7)
			s.Record(r)
			quotaTestObserve(s, now, now.Add(time.Hour), .4, 18000)
			cycle := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
			if (cycle.Summary.CostUSD != nil) != known || (cycle.EstimatedTotalUSD != nil) != known || (cycle.ModelStats[0].CostUSD != nil) != known {
				t.Fatalf("unknown and zero price confused: %+v", cycle)
			}
			raw, err := json.Marshal(cycle)
			if err != nil {
				t.Fatal(err)
			}
			if !known && !bytes.Contains(raw, []byte(`"estimated_cost":null`)) {
				t.Fatalf("unknown cost not null on the wire: %s", raw)
			}
		})
	}
}

func TestQuotaConflictingImportDoesNotChangeEitherLedger(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	r := quotaTestRecord("one", "m", time.Now().UTC().Add(-time.Minute), 7)
	s.Record(r)
	backup := s.Snapshot()
	for api, a := range backup.APIs {
		m := a.Models["m"]
		m.Details[0].Tokens.InputTokens = 100
		a.Models["m"] = m
		backup.APIs[api] = a
	}
	if _, err := s.mergeSnapshotChecked(backup); err == nil {
		t.Fatal("accepted conflicting main/quota record")
	}
	if s.totalRequests != 1 || s.totalTokens != 7 || len(s.quota.Facts) != 1 {
		t.Fatal("invalid import mutated statistics")
	}
}

func TestQuotaExpiredEstimateInvalidatesAllTimeDashboard(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	quotaTestObserve(s, now, now.Add(5*time.Hour), .4, 18000)
	version := s.dashboardVersionAt(now)
	if s.dashboardVersionAt(now.Add(76*time.Minute)) == version {
		t.Fatal("expired estimate can be reused through a stale ETag")
	}
}

func TestQuotaDeletionSurvivesSnapshotAndJournalOverlap(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("deleted", "m", now, 7)
	s.Record(r)
	d := requestDetailFromUsageRecord(r, now, headerWhitelist{})
	if !s.RemoveRecordedUsage(r) {
		t.Fatal("record not removed")
	}
	backup := s.Snapshot()
	restored := NewRequestStatistics()
	defer restored.Close()
	restored.restoreStorageSnapshotLocked(backup, now)
	restored.restoreQuotaSnapshotLocked(backup)
	if err := restored.replayPersistedDetailsLocked([]persistedDetail{{API: usageGroupKey(r), Model: "m", Detail: d}}, 0, false, now); err != nil {
		t.Fatal(err)
	}
	if restored.totalRequests != 0 || len(restored.quota.Facts) != 0 {
		t.Fatal("same-day journal resurrected a deleted record")
	}
}

func BenchmarkQuotaAPIDetail10k(b *testing.B) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 1}}
	r := quotaTestRecord("", "m", now.Add(-time.Minute), 1000)
	for i := 0; i < 10000; i++ {
		r.RequestID = fmt.Sprint(i)
		s.Record(r)
	}
	s.applyQuotaObservationLocked(quotaObservation{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID,
		Group: "shared", Slot: "5h", Seconds: 18000, Reset: now.Add(time.Hour), ObservedAt: now, Used: .4})
	api := usageGroupKey(r)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.QueryAPIDetailAt(api, "all", 10, 10, now)
	}
}

func TestQuotaLegacyJournalBackupHasStableFactReferences(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("old", "m", now, 7)
	d := requestDetailFromUsageRecord(r, now, headerWhitelist{})
	d.RecordID = ""
	if err := s.replayPersistedDetailsLocked([]persistedDetail{{API: usageGroupKey(r), Model: "m", Detail: d}}, 0, false, now); err != nil {
		t.Fatal(err)
	}
	backup := s.Snapshot()
	restored := NewRequestStatistics()
	defer restored.Close()
	if result, err := restored.mergeSnapshotChecked(backup); err != nil || result.Added != 1 {
		t.Fatalf("legacy backup cannot be imported: %+v, %v", result, err)
	}
	if len(restored.quota.Facts) != 1 {
		t.Fatal("import created a second quota fact for the legacy record")
	}
}

func TestQuotaLegacyJournalReplayAfterDetailRetention(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dir := t.TempDir()
	r := quotaTestRecord("legacy", "m", now.Add(-time.Hour), 1_000_000)
	d := requestDetailFromUsageRecord(r, r.RequestedAt, headerWhitelist{})
	d.RecordID = ""
	row := persistedDetail{API: usageGroupKey(r), Model: "m", Detail: d}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, storageFileName(storageDate(now))), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewRequestStatistics()
	defer func() { s.Close() }()
	s.retention = 30 * time.Minute
	if err := s.replayPersistedDetailsLocked([]persistedDetail{row}, 0, false, now); err != nil {
		s.Close()
		t.Fatal(err)
	}
	quotaTestObserve(s, now, now.Add(time.Hour), .4, 18000)
	for restart := range 3 {
		if err := writeStorageSnapshotViewFile(dir, s.captureStorageSnapshot(), now); err != nil {
			s.Close()
			t.Fatal(err)
		}
		s.Close()
		s = NewRequestStatistics()
		s.retention = 30 * time.Minute
		s.modelPrices = map[string]ModelPrice{"m": {Prompt: 5}}
		snapshotAt, err := s.loadStorageSnapshotLocked(dir, now)
		if err != nil {
			s.Close()
			t.Fatal(err)
		}
		if err := s.replayStorageFilesLocked(dir, "", now, snapshotAt); err != nil {
			s.Close()
			t.Fatal(err)
		}
		cycle := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
		if s.totalRequests != 0 || len(s.quota.Facts) != 1 || cycle.Summary.TotalRequests != 1 || cycle.Summary.EstimatedCost != 5 {
			s.Close()
			t.Fatalf("restart %d duplicated expired legacy usage: requests=%d facts=%d cycle=%+v", restart, s.totalRequests, len(s.quota.Facts), cycle.Summary)
		}
	}
	s.Close()
}

func TestQuotaLegacySnapshotAndJournalShareFacts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("legacy-snapshot", "m", now.Add(-time.Hour), 7)
	d := requestDetailFromUsageRecord(r, r.RequestedAt, headerWhitelist{})
	d.RecordID = ""
	api := usageGroupKey(r)
	legacy := StatisticsSnapshot{TotalRequests: 1, SuccessCount: 1, TotalTokens: 7,
		APIs: map[string]APISnapshot{api: {TotalRequests: 1, SuccessCount: 1, TotalTokens: 7,
			Models: map[string]ModelSnapshot{"m": {TotalRequests: 1, SuccessCount: 1, TotalTokens: 7, Details: []RequestDetail{d}}}}}}
	s := NewRequestStatistics()
	defer s.Close()
	s.retention = 30 * time.Minute
	s.restoreStorageSnapshotLocked(legacy, now)
	s.restoreQuotaSnapshotLocked(legacy)
	quotaTestObserve(s, now, now.Add(time.Hour), .4, 18000)
	s.pruneLocked(now, false)
	if err := s.replayPersistedDetailsLocked([]persistedDetail{{API: api, Model: "m", Detail: d}}, 0, false, now); err != nil {
		t.Fatal(err)
	}
	if len(s.quota.Facts) != 1 {
		t.Fatalf("snapshot and journal assigned different legacy identities: %d facts", len(s.quota.Facts))
	}
}

func TestQuotaLegacyDeletionSurvivesJournalReplay(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("legacy-deleted", "m", now, 7)
	d := requestDetailFromUsageRecord(r, now, headerWhitelist{})
	d.RecordID = ""
	row := persistedDetail{API: usageGroupKey(r), Model: "m", Detail: d}
	s := NewRequestStatistics()
	defer s.Close()
	if err := s.replayPersistedDetailsLocked([]persistedDetail{row}, 0, false, now); err != nil {
		t.Fatal(err)
	}
	if !s.RemoveRecordedUsage(r) {
		t.Fatal("legacy record not removed")
	}
	backup := s.Snapshot()
	restored := NewRequestStatistics()
	defer restored.Close()
	restored.restoreStorageSnapshotLocked(backup, now)
	restored.restoreQuotaSnapshotLocked(backup)
	if err := restored.replayPersistedDetailsLocked([]persistedDetail{row}, 0, false, now); err != nil {
		t.Fatal(err)
	}
	if restored.totalRequests != 0 || len(restored.quota.Facts) != 0 {
		t.Fatal("legacy journal resurrected a deleted accounting record")
	}
}

func TestQuotaIdenticalLiveRequestsWithoutHostIDsRemainIndependent(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	at := time.Now().UTC().Add(-time.Minute)
	r := quotaTestRecord("", "m", at, 7)
	s.Record(r)
	s.Record(r)
	if s.totalRequests != 2 || len(s.quota.Facts) != 2 {
		t.Fatal("identical live executions were collapsed")
	}
	backup := s.Snapshot()
	restored := NewRequestStatistics()
	defer restored.Close()
	for range 2 {
		if _, err := restored.mergeSnapshotChecked(backup); err != nil {
			t.Fatal(err)
		}
	}
	if restored.totalRequests != 2 || len(restored.quota.Facts) != 2 {
		t.Fatal("backup lost independent live executions or duplicated an import")
	}
}

func TestQuotaRelativeResetDriftRecalibratesAfterDecrease(t *testing.T) {
	for _, drift := range []time.Duration{-time.Second, time.Second} {
		t.Run(drift.String(), func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			s.startedAt = now.Add(-6 * time.Hour)
			s.modelPrices = map[string]ModelPrice{"m": {Prompt: 5}}
			r := quotaTestRecord("before", "m", now.Add(-10*time.Minute), 1_000_000)
			r.Provider = "codex"
			s.Record(r)
			o := quotaObservation{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID,
				Group: "shared", Slot: "primary", Seconds: 18000, ObservedAt: now.Add(-5 * time.Minute), Reset: now.Add(time.Hour), Used: .5}
			if !s.applyQuotaObservationLocked(o) {
				t.Fatal("initial observation rejected")
			}
			o.ObservedAt, o.Reset, o.Used = now.Add(-3*time.Minute), o.Reset.Add(drift), .2
			if !s.applyQuotaObservationLocked(o) {
				t.Fatal("decrease with relative-reset drift rejected")
			}
			cycle := s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
			if *cycle.UsedPercent != 20 || cycle.EstimatedTotalUSD != nil {
				t.Fatalf("decrease retained old watermark or estimate: %+v", cycle)
			}
			r.RequestID, r.RequestedAt = "after", now.Add(-2*time.Minute)
			s.Record(r)
			o.ObservedAt, o.Reset, o.Used = now, o.Reset.Add(drift), .3
			if !s.applyQuotaObservationLocked(o) {
				t.Fatal("post-decrease observation rejected")
			}
			cycle = s.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
			if cycle.EstimatedTotalUSD == nil || math.Abs(*cycle.EstimatedTotalUSD-50) > 1e-9 || math.Abs(*cycle.EstimatedRemainingUSD-35) > 1e-9 {
				t.Fatalf("did not recalibrate from post-decrease cost: %+v", cycle)
			}
			if err := validateQuotaSnapshot(s.Snapshot().QuotaCycles, nil); err != nil {
				t.Fatal(err)
			}
			// A different overlapping window with a large reset shift must not
			// be mistaken for rounding drift merely because utilization fell.
			o.ObservedAt, o.Reset, o.Used = now.Add(time.Minute), o.Reset.Add(time.Hour), .1
			if s.applyQuotaObservationLocked(o) {
				t.Fatal("accepted a different overlapping window as small reset drift")
			}
		})
	}
}

func TestQuotaLegacyIdentitySurvivesClientNormalization(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	r := quotaTestRecord("legacy", "m", now, 7)
	r.APIKey = "sk-legacy-client-0123456789"
	d := requestDetailFromUsageRecord(r, now, headerWhitelist{})
	d.RecordID = ""
	api := usageGroupKey(r)
	legacy := StatisticsSnapshot{TotalRequests: 1, SuccessCount: 1, TotalTokens: 7,
		APIs: map[string]APISnapshot{api: {TotalRequests: 1, SuccessCount: 1, TotalTokens: 7,
			Models: map[string]ModelSnapshot{"m": {TotalRequests: 1, SuccessCount: 1, TotalTokens: 7, Details: []RequestDetail{d}}}}}}
	migrated := NewRequestStatistics()
	defer migrated.Close()
	migrated.restoreStorageSnapshotLocked(legacy, now)
	migrated.restoreQuotaSnapshotLocked(legacy)
	backup := migrated.Snapshot()
	for _, restoreFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore-first=%t", restoreFirst), func(t *testing.T) {
			destination := NewRequestStatistics()
			defer destination.Close()
			if restoreFirst {
				destination.restoreStorageSnapshotLocked(legacy, now)
				destination.restoreQuotaSnapshotLocked(legacy)
			} else if result, err := destination.mergeSnapshotChecked(legacy); err != nil || result.Added != 1 {
				t.Fatalf("initial import %+v %v", result, err)
			}
			for range 2 {
				if result, err := destination.mergeSnapshotChecked(backup); err != nil || result.Added != 0 {
					t.Fatalf("same legacy request added after client normalization: %+v err=%v", result, err)
				}
				if result, err := destination.mergeSnapshotChecked(legacy); err != nil || result.Added != 0 {
					t.Fatalf("old backup duplicated: %+v err=%v", result, err)
				}
			}
			if destination.totalRequests != 1 || len(destination.quota.Facts) != 1 {
				t.Fatalf("requests=%d facts=%d, want 1 each", destination.totalRequests, len(destination.quota.Facts))
			}
			if err := destination.replayPersistedDetailsLocked([]persistedDetail{{API: api, Model: "m", Detail: d}}, 0, false, now); err != nil {
				t.Fatal(err)
			}
			if destination.totalRequests != 1 || len(destination.quota.Facts) != 1 {
				t.Fatal("journal disagrees with imported legacy identity")
			}
		})
	}
}

func TestQuotaCalibrationSurvivesSampleLimit(t *testing.T) {
	for _, restarted := range []bool{false, true} {
		t.Run(fmt.Sprintf("restarted=%t", restarted), func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			start := now.Add(-70 * time.Minute)
			s.startedAt = start.Add(-time.Hour)
			s.modelPrices = map[string]ModelPrice{"m": {Prompt: 5}}
			s.Record(quotaTestRecord("before-drop", "m", start.Add(time.Minute), 5_000_000))
			quotaTestObserve(s, start.Add(5*time.Minute), start.Add(5*time.Hour), .5, 18000)
			if restarted {
				s.quota.StartedAt = start.Add(5*time.Minute + time.Second)
			}
			quotaTestObserve(s, start.Add(6*time.Minute), start.Add(5*time.Hour), .1, 18000)
			s.Record(quotaTestRecord("after-drop", "m", start.Add(7*time.Minute), 6_000_000))
			for i := 1; i <= 200; i++ {
				quotaTestObserve(s, start.Add(6*time.Minute+time.Duration(i)*15*time.Second), start.Add(5*time.Hour), .1+.003*float64(i), 18000)
			}
			cycle := s.QueryAPIDetailAt(usageGroupKey(quotaTestRecord("", "m", now, 0)), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
			if cycle.EstimatedTotalUSD == nil || math.Abs(*cycle.EstimatedTotalUSD-50) > 1e-9 || math.Abs(*cycle.EstimatedRemainingUSD-15) > 1e-9 {
				t.Fatalf("calibration lost after 200 observations: %+v", cycle)
			}
			raw, err := json.Marshal(s.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			var backup StatisticsSnapshot
			if err := json.Unmarshal(raw, &backup); err != nil {
				t.Fatal(err)
			}
			if err := validateQuotaSnapshot(backup.QuotaCycles, nil); err != nil {
				t.Fatal(err)
			}
			for _, w := range backup.QuotaCycles.Windows {
				if len(w.Current.Samples) > 64 {
					t.Fatal("sample limit exceeded")
				}
				total, _ := quotaEstimate(w, w.Current, quotaFactsForTest(backup.QuotaCycles), s.PricingSnapshot(), s.quota.StartedAt, now)
				if total == nil || math.Abs(*total-50) > 1e-9 {
					t.Fatal("backup lost calibration anchors")
				}
			}
		})
	}
}

func quotaFactsForTest(snapshot *quotaSnapshot) []quotaFact {
	var facts []quotaFact
	for _, f := range snapshot.Facts {
		facts = append(facts, f)
	}
	return facts
}

func TestQuotaSampleLimitKeepsDecreaseAboveInitialUtilization(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-70 * time.Minute)
	s.startedAt = start.Add(-time.Hour)
	s.modelPrices = map[string]ModelPrice{"m": {Prompt: 5}}
	s.Record(quotaTestRecord("before-drop", "m", start.Add(time.Minute), 8_000_000))
	quotaTestObserve(s, start.Add(2*time.Minute), start.Add(5*time.Hour), .1, 18000)
	quotaTestObserve(s, start.Add(5*time.Minute), start.Add(5*time.Hour), .8, 18000)
	quotaTestObserve(s, start.Add(6*time.Minute), start.Add(5*time.Hour), .5, 18000)
	s.Record(quotaTestRecord("after-drop", "m", start.Add(7*time.Minute), 2_000_000))
	for i := 1; i <= 200; i++ {
		quotaTestObserve(s, start.Add(6*time.Minute+time.Duration(i)*15*time.Second), start.Add(5*time.Hour), .5+.001*float64(i), 18000)
	}
	cycle := s.QueryAPIDetailAt(usageGroupKey(quotaTestRecord("", "m", now, 0)), "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
	if cycle.EstimatedTotalUSD == nil || math.Abs(*cycle.EstimatedTotalUSD-50) > 1e-9 {
		t.Fatalf("a drop above the initial utilization lost its baseline: %+v", cycle)
	}
}

func TestQuotaShortWindowDriftDoesNotRollover(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	o := quotaObservation{Provider: "codex", AuthIndex: "index", AuthID: "account", Group: "shared", Slot: "primary",
		Seconds: 60, Reset: now.Add(30 * time.Second), ObservedAt: now.Add(-20 * time.Second), Used: .5}
	if !s.applyQuotaObservationLocked(o) {
		t.Fatal("initial short window rejected")
	}
	o.Reset, o.ObservedAt, o.Used = o.Reset.Add(time.Second), now.Add(-10*time.Second), .2
	if !s.applyQuotaObservationLocked(o) {
		t.Fatal("short window drift rejected")
	}
	w := s.quota.Windows[quotaWindowKey(o)]
	if w.Previous != nil || w.Current == nil || len(w.Current.Samples) != 2 {
		t.Fatal("one-minute window drift was mistaken for a rollover")
	}
}

func TestQuotaRolloverToleratesResetDrift(t *testing.T) {
	for _, drift := range []time.Duration{-time.Second, time.Second} {
		for _, pollBeforeObservation := range []bool{false, true} {
			t.Run(fmt.Sprintf("drift=%s-poll=%t", drift, pollBeforeObservation), func(t *testing.T) {
				s := NewRequestStatistics()
				defer s.Close()
				now := time.Now().UTC().Truncate(time.Second)
				boundary := now.Add(-time.Minute)
				s.modelPrices = map[string]ModelPrice{"old": {Prompt: 30}, "new": {Prompt: 5}}
				s.Record(quotaTestRecord("old", "old", boundary.Add(-time.Minute), 1_000_000))
				s.Record(quotaTestRecord("boundary", "new", boundary, 1_000_000))
				quotaTestObserve(s, boundary.Add(-time.Minute), boundary.Add(drift), .6, 18000)
				if pollBeforeObservation {
					s.advanceQuotaCyclesLocked(now)
				}
				quotaTestObserve(s, now, boundary.Add(5*time.Hour), .1, 18000)
				group := s.QueryAPIDetailAt(usageGroupKey(quotaTestRecord("", "m", now, 0)), "all", 10, 10, now).QuotaCycles[0].Groups[0]
				if group.Current == nil || group.Previous == nil {
					t.Fatal("adjacent period disappeared")
				}
				if !group.Previous.EndAt.Equal(group.Current.StartAt) || group.Current.Summary.TotalRequests != 1 || group.Previous.Summary.TotalRequests != 1 || group.Current.Summary.EstimatedCost != 5 || group.Previous.Summary.EstimatedCost != 30 {
					t.Fatalf("drift mixed period accounting: %+v", group)
				}
				quotaTestObserve(s, boundary.Add(-10*time.Second), boundary.Add(drift), .9, 18000)
				group = s.QueryAPIDetailAt(usageGroupKey(quotaTestRecord("", "m", now, 0)), "all", 10, 10, now).QuotaCycles[0].Groups[0]
				if *group.Previous.UsedPercent != 90 || *group.Current.UsedPercent != 10 {
					t.Fatal("aligned boundary rejected a late observation from the previous period")
				}
				if err := validateQuotaSnapshot(s.Snapshot().QuotaCycles, nil); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestQuotaReplayPreservesRepairedFacts(t *testing.T) {
	for _, archived := range []bool{false, true} {
		for _, factRow := range []bool{false, true} {
			t.Run(fmt.Sprintf("archived=%t-fact-row=%t", archived, factRow), func(t *testing.T) {
				s := NewRequestStatistics()
				defer s.Close()
				now := time.Now().UTC().Truncate(time.Second)
				d := pollutedClaudeCacheDetail()
				d.AuthType, d.AuthID = "oauth", "account.json"
				d.Timestamp = now.Add(-time.Minute)
				api := usageGroupKeyFromDetail("claude", d)
				d.RecordID = quotaLegacyRecordID(api, d.Model, d)
				if !s.recordDetailWithAccountingLocked(api, d.Model, d, requestDedupKey{}, now, false, archived) {
					t.Fatal("initial detail rejected")
				}
				s.claudeCacheRepairEnabled = true
				if s.repairClaudeCacheFallbackDetailsLocked(now) != 1 {
					t.Fatal("detail was not repaired")
				}
				dir := t.TempDir()
				if err := writeStorageSnapshotViewFile(dir, s.captureStorageSnapshot(), now); err != nil {
					t.Fatal(err)
				}
				restored := NewRequestStatistics()
				defer restored.Close()
				if _, err := restored.loadStorageSnapshotLocked(dir, now); err != nil {
					t.Fatal(err)
				}
				// Repair is off after restart. Neither an old import's fact row
				// nor its detail row may replace the snapshot's repaired tokens.
				rows := []persistedDetail{{API: api, Model: d.Model, Detail: d, Archived: archived}}
				if factRow {
					rows = append([]persistedDetail{{Kind: "quota", QuotaFacts: []quotaFact{quotaFactFromDetail(api, d)}}}, rows...)
				}
				for range 2 {
					if err := restored.replayPersistedDetailsLocked(rows, 0, false, now); err != nil {
						t.Fatal(err)
					}
				}
				want := repairClaudeCacheFallbackTokens(d).Tokens
				if restored.totalRequests != 1 || restored.totalTokens != want.TotalTokens || restored.quota.Facts[d.RecordID].Tokens != want {
					t.Fatal("duplicate replay replaced repaired accounting")
				}
				backup := restored.Snapshot()
				if err := validateQuotaImport(backup, nil, false); err != nil {
					t.Fatalf("replayed backup is inconsistent: %v", err)
				}
				destination := NewRequestStatistics()
				defer destination.Close()
				if result, err := destination.mergeSnapshotChecked(backup); err != nil || result.Added != 1 {
					t.Fatalf("replayed backup cannot be imported: %+v, %v", result, err)
				}
			})
		}
	}
}

func TestQuotaSyntheticTimestampKeepsBasePrice(t *testing.T) {
	for _, synthetic := range []bool{false, true} {
		t.Run(fmt.Sprintf("synthetic=%t", synthetic), func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
			s.startedAt = now.Add(-6 * time.Hour)
			s.pricingLocation = time.UTC
			// Together the two rules cover every minute of the day.
			s.modelPrices = map[string]ModelPrice{"m": {Prompt: 5, TimeRules: []ModelPriceRule{
				{Start: "00:00", End: "12:00", Prompt: float64Ptr(10)},
				{Start: "12:00", End: "00:00", Prompt: float64Ptr(10)},
			}}}
			r := quotaTestRecord("", "m", now, 1_000_000)
			d := requestDetailFromUsageRecord(r, now, headerWhitelist{})
			d.RecordID = ""
			if synthetic {
				d.Timestamp = time.Time{}
			}
			api := usageGroupKey(r)
			legacy := StatisticsSnapshot{APIs: map[string]APISnapshot{api: {Models: map[string]ModelSnapshot{"m": {Details: []RequestDetail{d}}}}}}
			if result, _ := s.mergeSnapshotLocked(legacy, false, now); result.Added != 1 {
				t.Fatal("historical detail not imported")
			}
			observed := now.Add(2 * time.Second)
			quotaTestObserve(s, observed, now.Add(time.Hour), .5, 18000)
			want := 10.0
			if synthetic {
				want = 5
			}
			check := func(stats *RequestStatistics, started time.Time) {
				t.Helper()
				stats.quota.StartedAt = started
				result := stats.QueryAPIDetailAt(api, "all", 10, 10, observed)
				cycle := result.QuotaCycles[0].Groups[0].Current
				if result.Summary.EstimatedCost != want || cycle.Summary.EstimatedCost != want || cycle.ModelStats[0].CostUSD == nil || *cycle.ModelStats[0].CostUSD != want {
					t.Fatalf("main and quota costs disagree: main=%v quota=%v want=%v", result.Summary.EstimatedCost, cycle.Summary.EstimatedCost, want)
				}
				if cycle.EstimatedTotalUSD == nil || *cycle.EstimatedTotalUSD != want*2 || *cycle.EstimatedRemainingUSD != want {
					t.Fatal("estimate did not use the same price as the main ledger")
				}
			}
			check(s, s.startedAt)
			raw, err := json.Marshal(s.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			var backup StatisticsSnapshot
			if err := json.Unmarshal(raw, &backup); err != nil {
				t.Fatal(err)
			}
			for _, f := range quotaFactsForTest(backup.QuotaCycles) {
				if f.detail().TimestampSynthetic != synthetic {
					t.Fatal("backup lost the timestamp origin")
				}
			}
			destination := NewRequestStatistics()
			defer destination.Close()
			destination.modelPrices, destination.pricingLocation = s.modelPrices, time.UTC
			if result, err := destination.mergeSnapshotChecked(backup); err != nil || result.Added != 1 {
				t.Fatalf("backup cannot be imported: %+v, %v", result, err)
			}
			check(destination, s.startedAt)
			dir := t.TempDir()
			if err := writeStorageSnapshotViewFile(dir, s.captureStorageSnapshot(), now); err != nil {
				t.Fatal(err)
			}
			restored := NewRequestStatistics()
			defer restored.Close()
			restored.modelPrices, restored.pricingLocation = s.modelPrices, time.UTC
			if _, err := restored.loadStorageSnapshotLocked(dir, now); err != nil {
				t.Fatal(err)
			}
			// Configuration rebuilds cost series after loading a snapshot.
			restored.rebuildCostSeriesLocked()
			check(restored, s.startedAt)
			if synthetic {
				legacyBackup := backup
				legacyBackup.QuotaCycles = cloneQuotaSnapshot(backup.QuotaCycles)
				for id, f := range legacyBackup.QuotaCycles.Facts {
					f.TimestampSynthetic = false // older quota facts lacked this field
					legacyBackup.QuotaCycles.Facts[id] = f
				}
				legacyDestination := NewRequestStatistics()
				defer legacyDestination.Close()
				legacyDestination.modelPrices, legacyDestination.pricingLocation = s.modelPrices, time.UTC
				if result, err := legacyDestination.mergeSnapshotChecked(legacyBackup); err != nil || result.Added != 1 {
					t.Fatalf("old quota backup cannot be imported: %+v, %v", result, err)
				}
				check(legacyDestination, s.startedAt)
				legacyRestored := NewRequestStatistics()
				defer legacyRestored.Close()
				legacyRestored.modelPrices, legacyRestored.pricingLocation = s.modelPrices, time.UTC
				legacyRestored.restoreStorageSnapshotLocked(legacyBackup, now)
				legacyRestored.restoreQuotaSnapshotLocked(legacyBackup)
				legacyRestored.rebuildCostSeriesLocked()
				check(legacyRestored, s.startedAt)
				for _, f := range legacyBackup.QuotaCycles.Facts {
					if f.TimestampSynthetic {
						t.Fatal("import or restore modified the caller's backup")
					}
				}
			}
		})
	}
}

func TestQuotaReplayPreservesRepairedFactsAfterDetailRetention(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	d := pollutedClaudeCacheDetail()
	d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
	d.Timestamp = now.Add(-time.Minute)
	api := usageGroupKeyFromDetail("claude", d)
	d.RecordID = quotaLegacyRecordID(api, d.Model, d)
	s.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
	s.claudeCacheRepairEnabled = true
	s.repairClaudeCacheFallbackDetailsLocked(now)
	quotaTestObserve(s, now, now.Add(time.Hour), .5, 18000)
	s.retention = 30 * time.Second
	s.pruneLocked(now, true)
	backup := s.Snapshot()
	if backup.TotalRequests != 0 || len(backup.QuotaCycles.Facts) != 1 {
		t.Fatal("quota fact should survive detail retention")
	}
	for _, retention := range []time.Duration{30 * time.Second, time.Hour} {
		t.Run(retention.String(), func(t *testing.T) {
			restored := NewRequestStatistics()
			defer restored.Close()
			restored.retention = retention
			restored.restoreStorageSnapshotLocked(backup, now)
			restored.restoreQuotaSnapshotLocked(backup)
			rows := []persistedDetail{
				{Kind: "quota", QuotaFacts: []quotaFact{quotaFactFromDetail(api, d)}},
				{API: api, Model: d.Model, Detail: d},
			}
			if err := restored.replayPersistedDetailsLocked(rows, 0, false, now); err != nil {
				t.Fatal(err)
			}
			want := repairClaudeCacheFallbackTokens(d).Tokens
			if restored.quota.Facts[d.RecordID].Tokens != want {
				t.Fatal("old journal replaced a repaired fact retained independently")
			}
			if err := validateQuotaImport(restored.Snapshot(), nil, false); err != nil {
				t.Fatalf("restored detail disagrees with retained quota fact: %v", err)
			}
		})
	}
}

func TestQuotaEnableCacheRepairAfterDetailRetention(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	d := pollutedClaudeCacheDetail()
	d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
	d.Timestamp = now.Add(-2 * time.Hour)
	api := usageGroupKeyFromDetail("claude", d)
	d.RecordID = quotaLegacyRecordID(api, d.Model, d)
	s := NewRequestStatistics()
	defer s.Close()
	s.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
	quotaTestObserve(s, now, now.Add(time.Hour), .5, 18000)
	s.retention = time.Hour
	s.pruneLocked(now, true)
	if s.totalRequests != 0 || len(s.quota.Facts) != 1 {
		t.Fatal("quota fact should survive detail retention")
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			dir := t.TempDir()
			if err := writeStorageSnapshotViewFile(dir, s.captureStorageSnapshot(), now); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(persistedDetail{API: api, Model: d.Model, Detail: d})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, storageFileName(storageDate(now))), append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			restored := NewRequestStatistics()
			defer restored.Close()
			restored.retention, restored.claudeCacheRepairEnabled = time.Hour, enabled
			at, err := restored.loadStorageSnapshotLocked(dir, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := restored.replayStorageFilesLocked(dir, "", now, at); err != nil {
				t.Fatal(err)
			}
			want := d.Tokens
			if enabled {
				want = repairClaudeCacheFallbackTokens(d).Tokens
			}
			if restored.totalRequests != 0 || restored.quota.Facts[d.RecordID].Tokens != want {
				t.Fatalf("repair setting not respected after replay: %+v, want %+v", restored.quota.Facts[d.RecordID].Tokens, want)
			}
			restored.repairClaudeCacheFallbackDetailsLocked(now)
			// Once repaired, turning the option off must not undo the change
			// during another overlap with the original journal.
			restored.claudeCacheRepairEnabled = false
			if err := restored.replayStorageFilesLocked(dir, "", now, at); err != nil {
				t.Fatal(err)
			}
			if restored.quota.Facts[d.RecordID].Tokens != want {
				t.Fatal("subsequent replay changed the retained quota fact")
			}
		})
	}
}

func TestQuotaCacheRepairUpdatesFactsWithoutRetainedDetails(t *testing.T) {
	for _, genuine := range []bool{false, true} {
		t.Run(fmt.Sprintf("genuine-cache-read=%t", genuine), func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			d := pollutedClaudeCacheDetail()
			d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
			d.Timestamp = now.Add(-2 * time.Hour)
			if genuine {
				d.Tokens.CacheReadTokens = d.Tokens.CachedTokens
			}
			api := usageGroupKeyFromDetail("claude", d)
			d.RecordID = quotaLegacyRecordID(api, d.Model, d)
			s.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
			quotaTestObserve(s, now, now.Add(time.Hour), .5, 18000)
			s.retention = time.Hour
			s.pruneLocked(now, true)
			s.modelPrices = map[string]ModelPrice{d.Model: {Prompt: 5, Cache: 1, CacheWrite: 2}}
			// This is also the path used when the option is enabled at runtime,
			// or when restart has no remaining journal row for the old request.
			s.claudeCacheRepairEnabled = true
			want, count := repairClaudeCacheFallbackTokens(d).Tokens, 1
			if genuine {
				want, count = d.Tokens, 0
			}
			if repaired := s.repairClaudeCacheFallbackDetailsLocked(now); repaired != count {
				t.Fatalf("repaired %d facts, want %d", repaired, count)
			}
			if s.totalRequests != 0 || s.quota.Facts[d.RecordID].Tokens != want {
				t.Fatal("retained quota fact did not follow the repair setting")
			}
			cycle := s.QueryAPIDetailAt(api, "all", 10, 10, now).QuotaCycles[0].Groups[0].Current
			expected := d
			expected.Tokens = want
			cost := s.PricingSnapshot().detailCost(d.Model, expected, detailTotalsFromRequest(expected))
			if cycle.Summary.CachedTokens != want.CachedTokens || cycle.Summary.TotalTokens != want.TotalTokens || math.Abs(cycle.Summary.EstimatedCost-cost) > 1e-9 {
				t.Fatal("displayed quota usage did not use repaired tokens and costs")
			}
			backup := s.Snapshot()
			if err := validateQuotaImport(backup, nil, false); err != nil {
				t.Fatal(err)
			}
			if repaired := s.repairClaudeCacheFallbackDetailsLocked(now); repaired != 0 {
				t.Fatal("cache repair is not idempotent")
			}
		})
	}
}

func TestQuotaImportUsesCacheRepairSetting(t *testing.T) {
	for _, mode := range []string{"visible", "archived", "expired"} {
		for _, enabled := range []bool{false, true} {
			for _, genuine := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/enabled=%t/genuine=%t", mode, enabled, genuine), func(t *testing.T) {
					now := time.Now().UTC().Truncate(time.Second)
					d := pollutedClaudeCacheDetail()
					d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
					d.Timestamp = now.Add(-2 * time.Hour)
					if genuine {
						d.Tokens.CacheReadTokens = d.Tokens.CachedTokens
					}
					api := usageGroupKeyFromDetail("claude", d)
					d.RecordID = quotaLegacyRecordID(api, d.Model, d)
					source := NewRequestStatistics()
					defer source.Close()
					source.recordDetailWithAccountingLocked(api, d.Model, d, requestDedupKey{}, now, false, mode == "archived")
					quotaTestObserve(source, now, now.Add(time.Hour), .5, 18000)
					if mode == "expired" {
						source.retention = time.Hour
						source.pruneLocked(now, true)
					}
					backup := source.Snapshot()
					before, err := json.Marshal(backup)
					if err != nil {
						t.Fatal(err)
					}
					dest := NewRequestStatistics()
					defer dest.Close()
					dest.claudeCacheRepairEnabled = enabled
					if mode == "expired" {
						dest.retention = time.Hour
					}
					want := d.Tokens
					if enabled && !genuine {
						want = repairClaudeCacheFallbackTokens(d).Tokens
					}
					for i := 0; i < 2; i++ {
						if _, err := dest.mergeSnapshotChecked(backup); err != nil {
							t.Fatalf("import %d: %v", i, err)
						}
						if got := dest.quota.Facts[d.RecordID].Tokens; got != want {
							t.Fatalf("import %d quota tokens: %+v, want %+v", i, got, want)
						}
						result := dest.Snapshot()
						if err := validateQuotaImport(result, nil, false); err != nil {
							t.Fatalf("main detail differs from quota fact: %v", err)
						}
						if result.TotalRequests != backup.TotalRequests {
							t.Fatal("import duplicated or lost main accounting")
						}
						if result.TotalRequests > 0 && result.TotalTokens != want.TotalTokens {
							t.Fatal("main ledger did not follow the repair setting")
						}
					}
					// Enabling the option after the original import must also allow
					// importing that same pre-repair backup again.
					dest.claudeCacheRepairEnabled = true
					dest.repairClaudeCacheFallbackDetailsLocked(now)
					if _, err := dest.mergeSnapshotChecked(backup); err != nil {
						t.Fatalf("pre-repair backup rejected after repair: %v", err)
					}
					// Real conflicts must still reject the whole import.
					conflict := backup
					conflict.QuotaCycles = cloneQuotaSnapshot(backup.QuotaCycles)
					f := conflict.QuotaCycles.Facts[d.RecordID]
					f.Model = "different-model"
					conflict.QuotaCycles.Facts[d.RecordID] = f
					current := dest.quota.Facts[d.RecordID]
					if _, err := dest.mergeSnapshotChecked(conflict); err == nil {
						t.Fatal("conflicting model was accepted")
					}
					if !quotaFactsEqual(current, dest.quota.Facts[d.RecordID]) {
						t.Fatal("rejected import modified existing facts")
					}
					after, err := json.Marshal(backup)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("import mutated the caller's backup")
					}
				})
			}
		}
	}
}

func TestQuotaImportPersistsCacheRepairWithoutMainDetails(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	d := pollutedClaudeCacheDetail()
	d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
	d.Timestamp = now.Add(-2 * time.Hour)
	api := usageGroupKeyFromDetail("claude", d)
	d.RecordID = quotaLegacyRecordID(api, d.Model, d)
	source := NewRequestStatistics()
	defer source.Close()
	source.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
	quotaTestObserve(source, now, now.Add(time.Hour), .5, 18000)
	source.retention = time.Hour
	source.pruneLocked(now, true)
	backup := source.Snapshot()
	dir := t.TempDir()
	dest := NewRequestStatistics()
	dest.Configure(runtimeConfig{
		ClaudeCacheRepairEnabled: true, StorageEnabled: true, StoragePath: dir,
		StorageFlushSeconds: 1, PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	if _, err := dest.mergeSnapshotChecked(backup); err != nil {
		dest.Close()
		t.Fatal(err)
	}
	dest.Close()
	// Force replay from the journal alone, with repair now disabled.
	if err := os.Remove(storageSnapshotPath(dir)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	restored := NewRequestStatistics()
	defer restored.Close()
	restored.Configure(runtimeConfig{
		ClaudeCacheRepairEnabled: false, StorageEnabled: true, StoragePath: dir,
		StorageFlushSeconds: 1, PriceStoragePath: filepath.Join(dir, "prices.json"),
	})
	want := repairClaudeCacheFallbackTokens(d).Tokens
	if restored.quota == nil || restored.quota.Facts[d.RecordID].Tokens != want {
		t.Fatal("import journal did not preserve repaired quota-only facts")
	}
}

func TestQuotaSummaryKeepsDetailDiscoverableAfterRetention(t *testing.T) {
	for _, hide := range []bool{false, true} {
		t.Run(fmt.Sprintf("hide=%t", hide), func(t *testing.T) {
			s := NewRequestStatistics()
			defer s.Close()
			now := time.Now().UTC().Truncate(time.Second)
			r := quotaTestRecord("retained", "m", now.Add(-2*time.Hour), 100)
			s.Record(r)
			api := usageGroupKey(r)
			quotaTestObserve(s, now, now.Add(time.Hour), .4, 18000)
			s.retention = time.Hour
			s.pruneLocked(now, true)
			for _, rangeKey := range []string{"all", "7h", "24h"} {
				for _, client := range []string{"", "another-client"} {
					summary := s.SummaryWithoutDetailsForRangeAndClientAPIAt(rangeKey, client, now)
					if summary.Usage.TotalRequests != 0 || len(summary.Usage.APIs) != 0 {
						t.Fatal("quota-only API changed the main usage summary")
					}
					if len(summary.Meta.QuotaAPIs) != 1 || summary.Meta.QuotaAPIs[0] != api {
						t.Fatal("retained quota details are not discoverable")
					}
					// Returned metadata must not share a slice with the cache.
					summary.Meta.QuotaAPIs[0] = "changed-by-caller"
					cached := s.SummaryWithoutDetailsForRangeAndClientAPIAt(rangeKey, client, now)
					if len(cached.Meta.QuotaAPIs) != 1 || cached.Meta.QuotaAPIs[0] != api {
						t.Fatal("caller modified cached quota API names")
					}
					detail := s.QueryAPIDetailForClientAPIAt(api, rangeKey, client, 10, 10, now)
					if detail.Summary.TotalRequests != 0 || len(detail.QuotaCycles) != 1 || detail.QuotaCycles[0].Groups[0].Current.Summary.TotalRequests != 1 {
						t.Fatal("quota-only detail did not preserve independent accounting")
					}
				}
			}
			later := now.Add(7 * time.Hour)
			if hide {
				later = now.Add(time.Second)
				if !s.applyQuotaObservationLocked(quotaObservation{Provider: r.Provider, AuthID: r.AuthID, AuthIndex: r.AuthIndex, Group: "shared", Slot: "5h", ObservedAt: later, Revoked: true}) {
					t.Fatal("revocation was not accepted")
				}
			}
			for _, rangeKey := range []string{"all", "7h"} {
				if names := s.SummaryWithoutDetailsForRangeAt(rangeKey, later).Meta.QuotaAPIs; len(names) != 0 {
					t.Fatalf("expired or revoked quota remained selectable: %v", names)
				}
			}
		})
	}
}
