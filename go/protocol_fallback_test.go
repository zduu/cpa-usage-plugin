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
	// Keep the shared fixture inside the default retention window. A fixed
	// production timestamp makes every test using stats.Record start returning
	// an empty snapshot once that date becomes older than retention_days.
	completedAt := time.Now().In(location).Add(-time.Minute)
	nativeLatency := 1981 * time.Millisecond
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
		RequestedAt:     completedAt.Add(-nativeLatency - 9*time.Millisecond),
		Latency:         nativeLatency,
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

// 兜底记录的 provider 是按客户端协议猜的,与实际上游无关:CPA 的 conductor 把
// selected_auth_id 写进了 metadata 的副本,响应拦截器永远拿不到上游身份。所以
// 配对不能限定 codex 家族——任何带身份的原生记录都可能是这条兜底的真身。
func TestAnonymousOpenAIProtocolFallbackCrossMatchesNonCodexUpstream(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	native.Provider = "gemini"
	native.AuthID = "gemini-user@example.com.json"
	native.AuthIndex = "gemini-auth"
	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)

	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 1 || reconciled.TotalRequests != 1 {
		t.Fatalf("reconciled count/requests = %d/%d, want 1/1", count, reconciled.TotalRequests)
	}
	if _, ok := reconciled.APIs["openai-compatible"]; ok {
		t.Fatal("兜底记录仍留在 openai-compatible 分组里")
	}
}

// 真实现场(usage-export-2026-08-23):Claude 协议上游 42cfbd4d51456110 提供
// deepseek 模型,客户端走 /v1/chat/completions。原生记录的 input 不含缓存,兜底
// 记录解析的是 OpenAI 形态响应体、input 含缓存,旧的逐字段 token 比对因此永不
// 相等,这条请求既被记成 openai-compatible 又被双计。
func TestAnonymousOpenAIFallbackMatchesClaudeNativeWithExclusiveCacheInput(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	nativeAt := time.Date(2026, 8, 23, 13, 1, 10, 143_258_000, location)
	latency := 126022 * time.Millisecond

	native := UsageRecord{
		Provider:     "claude",
		ExecutorType: "ClaudeExecutor",
		Model:        "deepseek-v4-flash-vision-exp",
		Alias:        "deepseek-v4-flash-vision-exp",
		APIKey:       "sk-client-claude-upstream-test",
		AuthID:       "claude-upstream-42cfbd4d51456110.json",
		AuthIndex:    "42cfbd4d51456110",
		AuthType:     "apikey",
		Source:       "claude",
		Endpoint:     "/v1/messages",
		RequestedAt:  nativeAt,
		Latency:      latency,
		TTFT:         2100 * time.Millisecond,
		// CPA 的 Claude 解析器让 input 排除缓存,总量单独把缓存加回来。
		Detail: UsageDetail{
			InputTokens: 3451, OutputTokens: 2072,
			CachedTokens: 182400, CacheReadTokens: 182400,
			TotalTokens: 187923,
		},
	}
	fallback := UsageRecord{
		Provider:     "openai-compatible",
		ExecutorType: "ResponseInterceptorFallback",
		Model:        native.Model,
		Alias:        native.Alias,
		APIKey:       native.APIKey,
		Endpoint:     "/v1/chat/completions",
		RequestedAt:  nativeAt.Add(latency).Add(21 * time.Millisecond),
		// OpenAI 形态的 prompt_tokens 已经含缓存。
		Detail: UsageDetail{
			InputTokens: 185851, OutputTokens: 2072,
			CachedTokens: 182400,
			TotalTokens:  187923,
		},
	}

	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)

	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 1 || reconciled.TotalRequests != 1 {
		t.Fatalf("reconciled count/requests = %d/%d, want 1/1", count, reconciled.TotalRequests)
	}
	if _, ok := reconciled.APIs["openai-compatible"]; ok {
		t.Fatal("Claude 上游的请求仍被记在 openai-compatible 分组")
	}
	api, ok := reconciled.APIs[usageGroupKey(native)]
	if !ok {
		t.Fatalf("原生 Claude 分组丢失: %#v", reconciled.APIs)
	}
	if got := api.Models[native.Model].TotalRequests; got != 1 {
		t.Fatalf("Claude 分组请求数 = %d, want 1", got)
	}
}

// 兜底时间戳取自响应回调,原生完成时刻是 RequestedAt+Latency,漂移随请求时长累积。
// 真实现场:latency 730.308s 的 Codex 流式请求,两侧完成时刻差 1.028s,固定 1s
// 窗口把它判成了两笔。
func TestAnonymousOpenAIFallbackMatchesAcrossLongStreamingDrift(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	native.Latency = 730308 * time.Millisecond
	fallback.RequestedAt = native.RequestedAt.Add(native.Latency).Add(1028 * time.Millisecond)

	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)

	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 1 || reconciled.TotalRequests != 1 {
		t.Fatalf("reconciled count/requests = %d/%d, want 1/1", count, reconciled.TotalRequests)
	}
}

// 放宽的窗口必须有上限:再长的请求也不能让任意两条 token 相同的记录合并。
func TestProtocolFallbackToleranceStaysBounded(t *testing.T) {
	if got := protocolFallbackTolerance(0); got != protocolFallbackCompletionTolerance {
		t.Fatalf("tolerance(0) = %v, want %v", got, protocolFallbackCompletionTolerance)
	}
	if got := protocolFallbackTolerance(10 * time.Hour); got != protocolFallbackCompletionToleranceMax {
		t.Fatalf("tolerance(10h) = %v, want %v", got, protocolFallbackCompletionToleranceMax)
	}

	fallback, native := protocolFallbackTestRecords()
	native.Latency = 10 * time.Hour
	fallback.RequestedAt = native.RequestedAt.Add(native.Latency).Add(protocolFallbackCompletionToleranceMax + time.Second)
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

// 归并窗口必须用固定上界推进指针。两条 native 的完成时刻只差一点,但后一条是
// 长请求、窗口宽得多;若用前一条的窄窗口就把 fallback 丢掉,本该成立的配对会
// 丢失。两条 native 的关联键必须相同(同模型、同客户端、同 token),才会落进
// 同一个归并序列里。
func TestProtocolFallbackPairingPrefersLaterNativeWithWiderWindow(t *testing.T) {
	fallback, shortNative := protocolFallbackTestRecords()
	shortNative.Latency = 500 * time.Millisecond
	shortCompletedAt := shortNative.RequestedAt.Add(shortNative.Latency)

	longNative := shortNative
	longNative.AuthID = "codex-second@privaterelay.appleid.com.json"
	longNative.AuthIndex = "b3f0cd186fd7dee9"
	longNative.Latency = 600 * time.Second
	// 完成时刻比短请求晚 300ms,但自身窗口是 1s+3s=4s。
	longNative.RequestedAt = shortCompletedAt.Add(300 * time.Millisecond).Add(-longNative.Latency)

	// fallback 落在短请求窗口(1.05s)之外、长请求窗口之内。
	fallback.RequestedAt = shortCompletedAt.Add(-2 * time.Second)

	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(shortNative)
	oldStats.Record(longNative)

	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 1 {
		t.Fatalf("配对数 = %d, want 1", count)
	}
	if reconciled.TotalRequests != 2 {
		t.Fatalf("配对后请求数 = %d, want 2", reconciled.TotalRequests)
	}
	if _, ok := reconciled.APIs["openai-compatible"]; ok {
		t.Fatal("兜底记录应被合并掉")
	}
}

// 关联键只比 (总量−输出, 输出, 推理),两笔缓存拆分不同但总量相同的请求会撞键。
// 缓存命中作为判别项(而非键的一部分)把它们分开:CPA 的 Claude→OpenAI 翻译把
// cache_read_input_tokens 原样写进 cached_tokens,两侧口径一致,可以比。
func TestProtocolFallbackRejectsMismatchedCacheReads(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	// 两侧 (总量−输出) 均为 1000、输出均为 11,键相同;命中量 300 vs 400。
	fallback.Detail = UsageDetail{InputTokens: 1000, OutputTokens: 11, CachedTokens: 300, TotalTokens: 1011}
	native.Detail = UsageDetail{InputTokens: 1000, OutputTokens: 11, CachedTokens: 400, CacheReadTokens: 400, TotalTokens: 1011}
	if usageProtocolCorrelationKey(fallback) != usageProtocolCorrelationKey(native) {
		t.Fatal("前提不成立:两条记录的关联键应当相同,否则测不到判别项")
	}

	oldStats := NewRequestStatistics()
	oldStats.Record(fallback)
	oldStats.Record(native)
	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 0 || reconciled.TotalRequests != 2 {
		t.Fatalf("命中量不同的两笔请求被合并了: count/requests = %d/%d, want 0/2", count, reconciled.TotalRequests)
	}

	// 兜底侧没报缓存时必须保持宽松,否则又会退回「配不上」。
	fallback.Detail.CachedTokens = 0
	fallback.Detail.InputTokens = 1000
	permissive := NewRequestStatistics()
	permissive.Record(fallback)
	permissive.Record(native)
	if _, count := reconcileProtocolFallbackSnapshot(permissive.Snapshot()); count != 1 {
		t.Fatalf("兜底侧缺缓存字段时应照常配对, count = %d, want 1", count)
	}
}

// 贪心「各挑最近的」不保证最大匹配。构造:F1@100、F2@103;N1 完成于 101、
// N2 完成于 98,两者 latency 均 200s(窗口 2s)。贪心让 F1 抢走 N1(距离 1 < 2),
// F2 就够不着 N2(距离 5 > 2),留下一条本该消掉的重复记录;最大匹配取
// F1→N2 / F2→N1,两条都配上。
func TestProtocolFallbackPairingFindsMaximumMatching(t *testing.T) {
	base, native := protocolFallbackTestRecords()
	latency := 200 * time.Second
	origin := native.RequestedAt

	makeFallback := func(offset time.Duration) UsageRecord {
		record := base
		record.RequestedAt = origin.Add(offset)
		return record
	}
	makeNative := func(completion time.Duration, authIndex string) UsageRecord {
		record := native
		record.Latency = latency
		record.AuthIndex = authIndex
		record.AuthID = "codex-" + authIndex + ".json"
		record.RequestedAt = origin.Add(completion).Add(-latency)
		return record
	}

	oldStats := NewRequestStatistics()
	oldStats.Record(makeFallback(100 * time.Second))
	oldStats.Record(makeFallback(103 * time.Second))
	oldStats.Record(makeNative(101*time.Second, "auth-n1"))
	oldStats.Record(makeNative(98*time.Second, "auth-n2"))

	reconciled, count := reconcileProtocolFallbackSnapshot(oldStats.Snapshot())
	if count != 2 {
		t.Fatalf("配对数 = %d, want 2(贪心只能配到 1)", count)
	}
	if reconciled.TotalRequests != 2 {
		t.Fatalf("配对后请求数 = %d, want 2", reconciled.TotalRequests)
	}
	if _, ok := reconciled.APIs["openai-compatible"]; ok {
		t.Fatal("两条兜底记录都应被合并掉")
	}
}
