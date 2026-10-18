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

func TestAntigravityHubSourceDoesNotReuseLegacyCycles(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("gemini-request", "gemini-test", now.Add(-time.Hour), 1000)
	r.Provider, r.AuthID = "antigravity", "antigravity.json"
	s.Record(r)
	observe := func(source string, at, weekEnd time.Time, remainingWeek, remainingFive float64) {
		t.Helper()
		input := quotaSignalsInput{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID, ObservedAt: at,
			AntigravityBuckets: []quotaAntigravityBucket{
				{Source: source, Group: "Gemini Models", ID: "gemini-weekly", Window: "weekly", RemainingFraction: &remainingWeek, ResetTime: weekEnd.Format(time.RFC3339)},
				{Source: source, Group: "Gemini Models", ID: "gemini-5h", Window: "5h", RemainingFraction: &remainingFive, ResetTime: at.Add(4*time.Hour + 6*time.Minute).Format(time.RFC3339)},
			}}
		observations := parseQuotaSignals(input)
		if len(observations) != 2 {
			t.Fatalf("source rejected: %q", source)
		}
		for _, o := range observations {
			if o.Source != source || o.Unmapped || !s.applyQuotaObservationLocked(o) {
				t.Fatalf("source or model attribution was lost: %+v", o)
			}
		}
	}
	legacyEnd := now.Add(3*24*time.Hour + 7*time.Hour)
	dailyEnd := now.Add(6*24*time.Hour + 2*time.Hour)
	observe("", now.Add(-10*time.Minute), legacyEnd, .9087964, 1)
	legacy := s.Snapshot()
	observe("hub-daily", now, dailyEnd, .93627316, .9554956)
	// A partial daily refresh updates the weekly reading without removing the
	// previously observed five-hour window from the same source.
	remainingWeek := .93627316
	partial := parseQuotaSignals(quotaSignalsInput{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID,
		ObservedAt: now.Add(30 * time.Second), AntigravityBuckets: []quotaAntigravityBucket{
			{Source: "hub-daily", Group: "Gemini Models", ID: "gemini-weekly", Window: "weekly",
				RemainingFraction: &remainingWeek, ResetTime: dailyEnd.Format(time.RFC3339)},
		}})
	if len(partial) != 1 || !s.applyQuotaObservationLocked(partial[0]) {
		t.Fatal("partial daily refresh was rejected")
	}
	// An old browser can still submit legacy observations after the new source
	// arrives. Its timestamp must not restore the wrong environment.
	observe("", now.Add(time.Minute), legacyEnd, .9087964, 1)
	check := func(target *RequestStatistics) {
		t.Helper()
		detail := target.QueryAPIDetailAt(usageGroupKey(r), "all", 10, 10, now.Add(2*time.Minute))
		if len(detail.QuotaCycles) != 1 || detail.QuotaCycles[0].Source != "hub-daily" || len(detail.QuotaCycles[0].Groups) != 1 {
			t.Fatalf("wrong quota source selected: %+v", detail.QuotaCycles)
		}
		windows := detail.QuotaCycles[0].Groups[0].Windows
		if len(windows) != 2 {
			t.Fatalf("legacy windows leaked into the current pool: %+v", windows)
		}
		for _, window := range windows {
			current := window.Current
			if current == nil || current.ResetTimeUnknown || current.ResetBaselineUsedPercent != nil || window.Previous != nil {
				t.Fatalf("source correction became a reset card: %+v", window)
			}
			if window.WindowSeconds == 604800 {
				if !current.EndAt.Equal(dailyEnd) || !current.StartAt.Equal(dailyEnd.Add(-7*24*time.Hour)) {
					t.Fatalf("weekly boundaries did not use daily: %+v", current)
				}
				quotaModelAssert(t, current.UsedPercent, (1-.93627316)*100)
			} else {
				if !current.EndAt.Equal(now.Add(4*time.Hour + 6*time.Minute)) {
					t.Fatalf("five-hour deadline did not use daily: %+v", current)
				}
				quotaModelAssert(t, current.UsedPercent, (1-.9554956)*100)
			}
		}
		if len(target.quota.Windows) != 4 || len(target.quota.Facts) != 1 || detail.Summary.TotalRequests != 1 {
			t.Fatal("source isolation discarded observations or changed request accounting")
		}
	}
	check(s)
	backup := s.Snapshot()
	if err := validateQuotaSnapshot(backup.QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
	for _, oldFirst := range []bool{false, true} {
		dest := NewRequestStatistics()
		if oldFirst {
			dest.MergeSnapshot(legacy)
		}
		dest.MergeSnapshot(backup)
		if !oldFirst {
			dest.MergeSnapshot(legacy)
		}
		check(dest)
		dest.Close()
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
	group := detail.QuotaCycles[0].Groups[0].Windows[0]
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
	if len(restored.QuotaCycles) != 1 || restored.QuotaCycles[0].Groups[0].Windows[0].Previous == nil {
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

func TestAntigravityLegacyRequestsDiscoverCredentialsWithoutQuotaFacts(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("legacy-ag", "claude-model", now.Add(-time.Hour), 1000)
	r.Provider, r.AuthID = "antigravity", "antigravity.json"
	s.Record(r)
	api := usageGroupKey(r)
	// Simulate a pre-quota ledger, first without quota state and then with
	// existing quota state that contains no facts for these older requests.
	s.quota = nil
	check := func() {
		t.Helper()
		refs := s.antigravityQuotaCredentialsForAPILocked(api)
		if len(refs) != 1 || refs[0].AuthID != r.AuthID {
			t.Fatalf("legacy credential missing: %+v", refs)
		}
		if len(s.antigravityQuotaCredentialsForAPILocked("unrelated-api")) != 0 {
			t.Fatal("cross-API credential disclosure")
		}
	}
	check()
	s.ensureQuotaLocked()
	check()
	remaining := .8
	input := quotaSignalsInput{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID, ObservedAt: now,
		AntigravityBuckets: []quotaAntigravityBucket{{Group: "Claude", ID: "week", Window: "weekly", RemainingFraction: &remaining, ResetTime: now.Add(time.Hour).Format(time.RFC3339)}}}
	for _, o := range parseQuotaSignals(input) {
		s.applyQuotaObservationLocked(o)
	}
	detail := s.QueryAPIDetailAt(api, "all", 10, 10, now)
	if len(detail.QuotaCycles) != 1 || len(detail.QuotaCredentials) != 1 {
		t.Fatal("newly queried pool hidden without quota facts")
	}
	if len(s.quota.Facts) != 0 || detail.Summary.TotalRequests != 1 {
		t.Fatal("discovery changed accounting")
	}
	if detail.QuotaCycles[0].Groups[0].Windows[0].Current.Summary != nil {
		t.Fatal("unmapped pool acquired invented usage")
	}
	// A new quota fact must not duplicate an identity discovered from history.
	r.RequestID, r.RequestedAt = "new-ag", now
	s.Record(r)
	check()
}

// The actual Google response has two pools, each containing both windows.
// Requests belong to one model pool and are independently counted in its
// overlapping windows, never in the other model pool.
func TestAntigravityTwoPoolsKeepWindowAccountingAndRestoreLegacyMapping(t *testing.T) {
	s := NewRequestStatistics()
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	s.startedAt = now.Add(-48 * time.Hour)
	models := []string{"gemini-3.8-flash-high", "claude-sonnet-4-6", "gpt-oss-120b-medium", "unknown-model"}
	s.modelPrices = make(map[string]ModelPrice)
	var api string
	for i, model := range append(models, models[0]) {
		s.modelPrices[model] = ModelPrice{Prompt: 1}
		at := now.Add(-time.Minute)
		if i == 4 {
			at = now.Add(-20 * time.Hour)
		}
		r := quotaTestRecord(model+at.String(), model, at, 175959)
		r.Provider, r.AuthID = "antigravity", "antigravity.json"
		s.Record(r)
		api = usageGroupKey(r)
	}
	remainingFive, remainingWeek := .944, .977
	input := quotaSignalsInput{Provider: "antigravity", AuthIndex: "account-index", AuthID: "antigravity.json", ObservedAt: now,
		AntigravityBuckets: []quotaAntigravityBucket{
			{Group: "Gemini Models", ID: "gemini-5h", Window: "5h", RemainingFraction: &remainingFive, ResetTime: now.Add(4 * time.Hour).Format(time.RFC3339)},
			{Group: "Gemini Models", ID: "gemini-weekly", Window: "weekly", RemainingFraction: &remainingWeek, ResetTime: now.Add(6 * 24 * time.Hour).Format(time.RFC3339)},
			{Group: "Claude and GPT models", ID: "3p-5h", Window: "5h", RemainingFraction: &remainingFive, ResetTime: now.Add(4 * time.Hour).Format(time.RFC3339)},
			{Group: "Claude and GPT models", ID: "3p-weekly", Window: "weekly", RemainingFraction: &remainingWeek, ResetTime: now.Add(6 * 24 * time.Hour).Format(time.RFC3339)},
		}}
	for _, o := range parseQuotaSignals(input) {
		if o.Unmapped || !s.applyQuotaObservationLocked(o) {
			t.Fatalf("known upstream bucket not mapped: %+v", o)
		}
	}
	check := func(target *RequestStatistics) {
		t.Helper()
		detail := target.QueryAPIDetailAt(api, "all", 10, 10, now)
		if detail.Summary.TotalRequests != 5 || len(detail.QuotaCycles) != 1 || len(detail.QuotaCycles[0].Groups) != 2 {
			t.Fatalf("expected two pools without changing requests: %+v", detail)
		}
		for _, pool := range detail.QuotaCycles[0].Groups {
			if len(pool.Windows) != 2 || pool.Current != nil || pool.Previous != nil {
				t.Fatalf("pool must contain independent windows: %+v", pool)
			}
			for i, window := range pool.Windows {
				current := window.Current
				wantRequests := int64(2)
				if pool.Name == "Gemini Models" && i == 0 {
					wantRequests = 1
				}
				if current == nil || current.Unmapped || current.Summary == nil || int64(current.Summary.TotalRequests) != wantRequests || current.Summary.TotalTokens != wantRequests*175959 {
					t.Fatalf("wrong model/window attribution: pool=%s window=%+v", pool.Name, window)
				}
				for _, row := range current.ModelStats {
					if pool.Name == "Gemini Models" && row.Model != models[0] || pool.Name != "Gemini Models" && row.Model != models[1] && row.Model != models[2] {
						t.Fatalf("model charged to wrong pool: %+v", row)
					}
				}
				remaining := remainingFive
				seconds := int64(18000)
				if i == 1 {
					remaining = remainingWeek
					seconds = 604800
				}
				if window.WindowSeconds != seconds || !current.StartAt.Equal(current.EndAt.Add(-time.Duration(seconds)*time.Second)) {
					t.Fatalf("window boundaries merged: %+v", window)
				}
				quotaModelAssert(t, current.UsedPercent, (1-remaining)*100)
				quotaModelAssert(t, current.Summary.CostUSD, float64(wantRequests)*.175959)
				quotaModelAssert(t, current.EstimatedTotalUSD, float64(wantRequests)*.175959/(1-remaining))
			}
		}
	}
	check(s)
	snapshot := s.Snapshot()
	if err := validateQuotaSnapshot(snapshot.QuotaCycles, nil); err != nil {
		t.Fatal(err)
	}
	// Existing snapshots marked these exact bucket identities as unmapped.
	for key, w := range snapshot.QuotaCycles.Windows {
		w.Unmapped = true
		snapshot.QuotaCycles.Windows[key] = w
	}
	dest := NewRequestStatistics()
	defer dest.Close()
	dest.modelPrices = s.modelPrices
	dest.MergeSnapshot(snapshot)
	check(dest)
}
