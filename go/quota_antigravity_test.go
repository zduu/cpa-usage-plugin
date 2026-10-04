package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAntigravityBucketsUseExplicitWindowsAndPreserveExhaustion(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	zero, fraction, invalid := 0.0, 0.6, 1.1
	input := quotaSignalsInput{Provider: "antigravity", AuthIndex: "index", AuthID: "antigravity.json", ObservedAt: now,
		AntigravityBuckets: []quotaAntigravityBucket{
			{Group: "Claude", ID: "claude-5h", Window: "5h", RemainingFraction: &zero, ResetTime: now.Add(time.Hour).Format(time.RFC3339)},
			{Group: "Gemini", ID: "gemini-week", Window: "weekly", RemainingFraction: &fraction, ResetTime: now.Add(24 * time.Hour).Format(time.RFC3339)},
			{Group: "Unknown", Window: "monthly", RemainingFraction: &fraction, ResetTime: now.Add(time.Hour).Format(time.RFC3339)},
			{Group: "Invalid", Window: "5h", RemainingFraction: &invalid, ResetTime: now.Add(time.Hour).Format(time.RFC3339)},
			{Group: "Missing", Window: "5h", ResetTime: now.Add(time.Hour).Format(time.RFC3339)},
		}}
	got := parseQuotaSignals(input)
	if len(got) != 2 || got[0].Seconds != 18000 || got[0].Used != 1 || got[1].Seconds != 604800 {
		t.Fatalf("incorrect explicit buckets: %+v", got)
	}
	if got[0].Group == got[1].Group || !got[0].Unmapped || !got[1].Unmapped {
		t.Fatal("independent pools were merged or given an invented model scope")
	}
	input.AntigravityBuckets[0].Window = "five_hour"
	input.AntigravityBuckets[1].Window = "week"
	aliases := parseQuotaSignals(input)
	if len(aliases) != 2 || quotaWindowKey(aliases[0]) != quotaWindowKey(got[0]) || quotaWindowKey(aliases[1]) != quotaWindowKey(got[1]) {
		t.Fatal("equivalent window aliases created distinct quota histories")
	}
	input.Provider = "codex"
	if len(parseQuotaSignals(input)) != 0 {
		t.Fatal("Antigravity bucket data interpreted for another provider")
	}
}

func TestAntigravityCyclesPreserveRequestsAndPoolHistoryAcrossBackup(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	record := func(id string, at time.Time) UsageRecord {
		r := quotaTestRecord(id, "claude-model", at, 1000)
		r.Provider, r.AuthID = "antigravity", "antigravity.json"
		return r
	}
	s.Record(record("old", now.Add(-2*time.Hour)))
	s.Record(record("new", now.Add(-time.Minute)))
	api := usageGroupKey(record("", now))
	before := s.QueryAPIDetailAt(api, "all", 10, 10, now)
	if len(before.QuotaCredentials) != 1 || before.QuotaCredentials[0].AuthID != "antigravity.json" || len(before.QuotaCycles) != 0 {
		t.Fatal("credential is not discoverable before its first quota fetch")
	}
	apply := func(at, reset time.Time, remaining float64) {
		input := quotaSignalsInput{Provider: "antigravity", AuthIndex: "account-index", AuthID: "antigravity.json", ObservedAt: at,
			AntigravityBuckets: []quotaAntigravityBucket{{Group: "Claude", ID: "5h", Window: "5h", RemainingFraction: &remaining, ResetTime: reset.Format(time.RFC3339)}}}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, observation := range parseQuotaSignals(input) {
			if !s.applyQuotaObservationLocked(observation) {
				t.Fatal("valid pool observation rejected")
			}
		}
	}
	apply(now.Add(-90*time.Minute), now.Add(-time.Hour), 0.25)
	apply(now, now.Add(4*time.Hour), 0.8)
	detail := s.QueryAPIDetailAt(api, "all", 10, 10, now)
	if detail.Summary.TotalRequests != 2 || len(detail.QuotaCycles) != 1 || len(detail.QuotaCycles[0].Groups) != 1 {
		t.Fatalf("quota altered ordinary requests: %+v", detail)
	}
	group := detail.QuotaCycles[0].Groups[0]
	if group.Current == nil || group.Previous == nil || *group.Previous.UsedPercent != 75 {
		t.Fatal("previous pool watermark was lost")
	}
	if !group.Current.Unmapped || group.Current.Summary != nil || group.Current.ModelStats != nil || group.Current.EstimatedTotalUSD != nil {
		t.Fatal("credential-wide requests were incorrectly charged to a single pool")
	}
	snapshot := s.Snapshot()
	if err := validateQuotaSnapshot(snapshot.QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
	dest := NewRequestStatistics()
	defer dest.Close()
	if result := dest.MergeSnapshot(snapshot); result.Added != 2 {
		t.Fatalf("restore: %+v", result)
	}
	restored := dest.QueryAPIDetailAt(api, "all", 10, 10, now)
	if len(restored.QuotaCycles) != 1 || restored.QuotaCycles[0].Groups[0].Previous == nil {
		t.Fatal("backup lost pool history")
	}
}

func TestAntigravityObservationChecksHostIdentityAndDoesNotCountRequests(t *testing.T) {
	previousStats, previousResolver := stats, resolveQuotaHostAuth
	stats = NewRequestStatistics()
	defer func() { stats.Close(); stats, resolveQuotaHostAuth = previousStats, previousResolver }()
	resolveQuotaHostAuth = func(index string) (quotaHostAuth, error) {
		return quotaHostAuth{ID: "antigravity.json", AuthIndex: index, Provider: "antigravity"}, nil
	}
	zero := 0.0
	now := time.Now().UTC().Truncate(time.Second)
	input := quotaSignalsInput{Provider: "antigravity", AuthIndex: "index", AuthID: "antigravity.json", ObservedAt: now,
		AntigravityBuckets: []quotaAntigravityBucket{{Group: "Claude", ID: "5h", Window: "5h", RemainingFraction: &zero, ResetTime: now.Add(time.Hour).Format(time.RFC3339)}}}
	call := func(input quotaSignalsInput) (accepted, rejected int) {
		body, _ := json.Marshal(map[string]any{"version": 1, "observations": []quotaSignalsInput{input}})
		raw, err := handleQuotaObservations(body)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Accepted int `json:"accepted"`
			Rejected int `json:"rejected"`
		}
		decodeManagementResponse(t, raw, &response)
		return response.Accepted, response.Rejected
	}
	if accepted, rejected := call(input); accepted != 1 || rejected != 0 {
		t.Fatal("valid Antigravity observation rejected")
	}
	input.AuthID = "another.json"
	if accepted, rejected := call(input); accepted != 0 || rejected != 1 {
		t.Fatal("mismatched credential accepted")
	}
	input.AuthID = "antigravity.json"
	input.AntigravityBuckets[0].Group = strings.Repeat("x", 257)
	if accepted, rejected := call(input); accepted != 0 || rejected != 1 {
		t.Fatal("oversized group accepted")
	}
	if stats.totalRequests != 0 || len(stats.quota.Windows) != 1 {
		t.Fatal("quota observations changed request accounting")
	}
}
