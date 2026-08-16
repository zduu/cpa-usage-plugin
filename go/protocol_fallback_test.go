package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResponseFallbackRecoversEndpointAndReasoningFromRequest(t *testing.T) {
	record, ok := usageRecordFromResponseIntercept(ResponseInterceptRequest{
		SourceFormat:   "openai",
		Model:          "gpt-5.6-luna",
		RequestedModel: "gpt-5.6-luna",
		RequestHeaders: map[string][]string{"Authorization": {"Bearer sk-client-protocol-fallback-test"}},
		RequestBody:    []byte(`{"model":"gpt-5.6-luna","reasoning_effort":"high"}`),
		Body:           []byte(`{"model":"gpt-5.6-luna","usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`),
		StatusCode:     http.StatusOK,
	})
	if !ok {
		t.Fatal("usageRecordFromResponseIntercept() ok = false")
	}
	if record.Endpoint != "/v1/chat/completions" || record.ReasoningEffort != "high" {
		t.Fatalf("endpoint/reasoning = %q/%q, want /v1/chat/completions/high", record.Endpoint, record.ReasoningEffort)
	}
}

func protocolFallbackTestRecords() (UsageRecord, UsageRecord) {
	location := time.FixedZone("CST", 8*60*60)
	completedAt := time.Date(2026, 7, 27, 15, 49, 23, 62_369_171, location)
	detail := UsageDetail{
		InputTokens: 1803, OutputTokens: 11, TotalTokens: 1814,
	}
	fallback := UsageRecord{
		Provider:        "openai-compatible",
		ExecutorType:    "ResponseInterceptorFallback",
		Model:           "gpt-5.6-luna",
		Alias:           "gpt-5.6-luna",
		APIKey:          "sk-client-protocol-fallback-test",
		Endpoint:        "/v1/chat/completions",
		ReasoningEffort: "high",
		Stream:          true,
		RequestedAt:     completedAt,
		Detail:          detail,
	}
	native := UsageRecord{
		Provider:        "codex",
		ExecutorType:    "CodexExecutor",
		Model:           "gpt-5.6-luna",
		Alias:           "gpt-5.6-luna",
		APIKey:          fallback.APIKey,
		AuthID:          "codex-xpspwc9mfb@privaterelay.appleid.com-plus.json",
		AuthIndex:       "a2f9cd186fd7dee9",
		AuthType:        "oauth",
		Source:          "xpspwc9mfb@privaterelay.appleid.com",
		RequestedAt:     time.Date(2026, 7, 27, 15, 49, 21, 72_754_693, location),
		Latency:         1981 * time.Millisecond,
		TTFT:            1326 * time.Millisecond,
		ReasoningEffort: "medium",
		Detail:          detail,
	}
	return fallback, native
}

func withProtocolFallbackTestGlobals(t *testing.T, delay time.Duration) *usageFallbackCoordinator {
	t.Helper()
	previousStats := stats
	previousFallbacks := usageFallbacks
	previousDelay := usageFallbackRecordDelay
	stats = NewRequestStatistics()
	usageFallbacks = newUsageFallbackCoordinator()
	usageFallbackRecordDelay = delay
	t.Cleanup(func() {
		usageFallbacks.Flush()
		stats = previousStats
		usageFallbacks = previousFallbacks
		usageFallbackRecordDelay = previousDelay
	})
	return usageFallbacks
}

func TestAnonymousOpenAIChatFallbackMatchesNativeCodexWhenFallbackArrivesFirst(t *testing.T) {
	coordinator := withProtocolFallbackTestGlobals(t, 50*time.Millisecond)
	fallback, native := protocolFallbackTestRecords()
	// Match the export shape: the response fallback lacks selected_auth_id and
	// therefore has a different exact provider fingerprint from native Codex.
	if usageRecordFingerprint(fallback) == usageRecordFingerprint(native) {
		t.Fatal("exact fingerprints unexpectedly match")
	}

	coordinator.Schedule(fallback)
	got, accepted := coordinator.HandleNative(native)
	if !accepted {
		t.Fatal("native record was not accepted")
	}
	stats.Record(got)
	time.Sleep(2 * usageFallbackRecordDelay)

	assertProtocolFallbackMergedSnapshot(t, stats.Snapshot(), native.ReasoningEffort)
}

func TestAnonymousOpenAIChatFallbackMatchesRecordedNativeCodex(t *testing.T) {
	coordinator := withProtocolFallbackTestGlobals(t, 50*time.Millisecond)
	fallback, native := protocolFallbackTestRecords()
	native.ReasoningEffort = ""

	got, accepted := coordinator.HandleNative(native)
	if !accepted {
		t.Fatal("native record was not accepted")
	}
	stats.Record(got)
	coordinator.Schedule(fallback)
	time.Sleep(2 * usageFallbackRecordDelay)

	assertProtocolFallbackMergedSnapshot(t, stats.Snapshot(), fallback.ReasoningEffort)
}

func TestAnonymousOpenAIChatFallbackLateNativeReplacesCommittedFallback(t *testing.T) {
	coordinator := withProtocolFallbackTestGlobals(t, 10*time.Millisecond)
	fallback, native := protocolFallbackTestRecords()
	coordinator.Schedule(fallback)
	time.Sleep(3 * usageFallbackRecordDelay)
	if got := stats.Snapshot().TotalRequests; got != 1 {
		t.Fatalf("committed fallback requests = %d, want 1", got)
	}

	got, accepted := coordinator.HandleNative(native)
	if !accepted {
		t.Fatal("late native record was not accepted after fallback removal")
	}
	stats.Record(got)
	assertProtocolFallbackMergedSnapshot(t, stats.Snapshot(), native.ReasoningEffort)
}

func TestAnonymousOpenAIChatFallbackConcurrentIdenticalTokensStayOneToOne(t *testing.T) {
	coordinator := withProtocolFallbackTestGlobals(t, 100*time.Millisecond)
	fallbackA, nativeA := protocolFallbackTestRecords()
	fallbackB, nativeB := protocolFallbackTestRecords()
	fallbackA.ReasoningEffort = "low"
	nativeA.ReasoningEffort = ""
	nativeA.AuthIndex = "auth-a"
	fallbackB.RequestedAt = fallbackB.RequestedAt.Add(300 * time.Millisecond)
	nativeB.RequestedAt = nativeB.RequestedAt.Add(300 * time.Millisecond)
	fallbackB.ReasoningEffort = "high"
	nativeB.ReasoningEffort = ""
	nativeB.AuthIndex = "auth-b"

	// Schedule in reverse order to prove matching is based on completion time,
	// not queue insertion order.
	coordinator.Schedule(fallbackB)
	coordinator.Schedule(fallbackA)
	for _, native := range []UsageRecord{nativeA, nativeB} {
		got, accepted := coordinator.HandleNative(native)
		if !accepted {
			t.Fatal("native record was not accepted")
		}
		stats.Record(got)
	}
	time.Sleep(2 * usageFallbackRecordDelay)

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 2 || len(snapshot.APIs) != 1 {
		t.Fatalf("snapshot requests/APIs = %d/%d, want 2/1", snapshot.TotalRequests, len(snapshot.APIs))
	}
	api := snapshot.APIs[usageGroupKey(nativeA)]
	details := api.Models[nativeA.Model].Details
	if len(details) != 2 {
		t.Fatalf("details = %d, want 2", len(details))
	}
	gotEffort := make(map[string]string)
	for _, detail := range details {
		gotEffort[detail.AuthIndex] = detail.Thinking.Level
	}
	if gotEffort["auth-a"] != "low" || gotEffort["auth-b"] != "high" {
		t.Fatalf("reasoning by auth = %#v, want auth-a=low auth-b=high", gotEffort)
	}
}

func TestAnonymousOpenAIChatFallbackOutsideCompletionWindowIsNotMerged(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	fallback.RequestedAt = fallback.RequestedAt.Add(2 * protocolFallbackCompletionTolerance)
	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)

	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 0 || reconciled.TotalRequests != 2 {
		t.Fatalf("reconciled count/requests = %d/%d, want 0/2", count, reconciled.TotalRequests)
	}
}

func TestAnonymousOpenAIProtocolFallbackOnlyCrossMatchesCodex(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	native.Provider = "gemini"
	native.AuthID = "gemini-user@example.com.json"
	native.AuthIndex = "gemini-auth"
	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)

	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 0 || reconciled.TotalRequests != 2 {
		t.Fatalf("reconciled count/requests = %d/%d, want 0/2", count, reconciled.TotalRequests)
	}
}

func TestAnonymousOpenAIResponsesFallbackMatchesNativeCodex(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	fallback.Endpoint = "/v1/responses"
	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)

	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 1 || reconciled.TotalRequests != 1 {
		t.Fatalf("reconciled count/requests = %d/%d, want 1/1", count, reconciled.TotalRequests)
	}
	api := reconciled.APIs[usageGroupKey(native)]
	detail := api.Models[native.Model].Details[0]
	if detail.Endpoint != "/v1/responses" {
		t.Fatalf("endpoint = %q, want /v1/responses", detail.Endpoint)
	}
}

func TestProtocolFallbackSnapshotReconciliationRepairsExportShape(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)
	original := oldStats.Snapshot()

	reconciled, count := reconcileProtocolFallbackSnapshot(original)
	if count != 1 {
		t.Fatalf("reconciled count = %d, want 1", count)
	}
	if original.TotalRequests != 2 {
		t.Fatalf("input snapshot was mutated: requests = %d, want 2", original.TotalRequests)
	}
	assertProtocolFallbackMergedSnapshot(t, reconciled, native.ReasoningEffort)

	imported := NewRequestStatistics()
	result := imported.MergeSnapshot(original)
	if result.Added != 1 || result.Skipped != 1 {
		t.Fatalf("merge result = %#v, want added=1 skipped=1", result)
	}
	assertProtocolFallbackMergedSnapshot(t, imported.Snapshot(), native.ReasoningEffort)
}

func TestPersistedProtocolFallbackReconciliationKeepsEnrichedNative(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	records := []persistedDetail{
		{API: usageGroupKey(fallback), Model: fallback.Model, Detail: requestDetailFromUsageRecord(fallback, fallback.RequestedAt, headerWhitelist{})},
		{API: usageGroupKey(native), Model: native.Model, Detail: requestDetailFromUsageRecord(native, native.RequestedAt, headerWhitelist{})},
	}

	got, removed := reconcilePersistedProtocolFallbacks(records)
	if removed != 1 || len(got) != 1 {
		t.Fatalf("removed/records = %d/%d, want 1/1", removed, len(got))
	}
	detail := got[0].Detail
	if detail.Provider != "codex" || detail.Endpoint != fallback.Endpoint || !detail.Stream || detail.Thinking.Level != native.ReasoningEffort {
		t.Fatalf("reconciled detail = %#v", detail)
	}
}

func TestStorageSnapshotRestoreReconcilesProtocolFallback(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)
	restored := NewRequestStatistics()
	restored.mu.Lock()
	restored.restoreStorageSnapshotLocked(oldStats.Snapshot(), time.Now())
	restored.mu.Unlock()
	assertProtocolFallbackMergedSnapshot(t, restored.Snapshot(), native.ReasoningEffort)
}

func TestJSONLReplayReconcilesProtocolFallback(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	records := []persistedDetail{
		{API: usageGroupKey(fallback), Model: fallback.Model, Detail: requestDetailFromUsageRecord(fallback, fallback.RequestedAt, headerWhitelist{})},
		{API: usageGroupKey(native), Model: native.Model, Detail: requestDetailFromUsageRecord(native, native.RequestedAt, headerWhitelist{})},
	}
	var payload []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		payload = append(payload, line...)
		payload = append(payload, '\n')
	}
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	restored := NewRequestStatistics()
	restored.mu.Lock()
	err := restored.replayStorageLocked(path)
	restored.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	assertProtocolFallbackMergedSnapshot(t, restored.Snapshot(), native.ReasoningEffort)
}

func assertProtocolFallbackMergedSnapshot(t *testing.T, snapshot StatisticsSnapshot, wantReasoning string) {
	t.Helper()
	_, native := protocolFallbackTestRecords()
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != native.Detail.TotalTokens {
		t.Fatalf("snapshot totals = requests %d tokens %d, want 1/%d", snapshot.TotalRequests, snapshot.TotalTokens, native.Detail.TotalTokens)
	}
	if _, exists := snapshot.APIs["openai-compatible"]; exists {
		t.Fatalf("snapshot still contains anonymous fallback group: %#v", snapshot.APIs)
	}
	api := snapshot.APIs[usageGroupKey(native)]
	details := api.Models[native.Model].Details
	if len(details) != 1 {
		t.Fatalf("native details = %d, want 1; APIs=%#v", len(details), snapshot.APIs)
	}
	detail := details[0]
	if detail.Provider != "codex" || detail.Endpoint != "/v1/chat/completions" || !detail.Stream {
		t.Fatalf("canonical detail provider/endpoint/stream = %q/%q/%v", detail.Provider, detail.Endpoint, detail.Stream)
	}
	if detail.LatencyMs != native.Latency.Milliseconds() || detail.TTFTMs != native.TTFT.Milliseconds() {
		t.Fatalf("canonical latency/ttft = %d/%d, want %d/%d", detail.LatencyMs, detail.TTFTMs, native.Latency.Milliseconds(), native.TTFT.Milliseconds())
	}
	if detail.Thinking.Level != wantReasoning || detail.Thinking.Intensity != wantReasoning {
		t.Fatalf("canonical reasoning = %#v, want %q", detail.Thinking, wantReasoning)
	}
}
