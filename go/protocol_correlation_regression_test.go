package main

import (
	"encoding/json"
	"testing"
	"time"
)

func testCorrelationMeta(known uint16, inputMode, outputMode, cacheMode string) *ProtocolCorrelationMeta {
	return &ProtocolCorrelationMeta{
		SchemaVersion: protocolCorrelationSchemaVersion,
		KnownFields:   known,
		InputMode:     inputMode,
		OutputMode:    outputMode,
		CacheMode:     cacheMode,
	}
}

func testCorrelationRecord(provider, executor, model, alias, endpoint, apiKey string, at time.Time, latency time.Duration, detail UsageDetail, meta *ProtocolCorrelationMeta) UsageRecord {
	record := UsageRecord{
		Provider:            provider,
		ExecutorType:        executor,
		Model:               model,
		Alias:               alias,
		APIKey:              apiKey,
		Endpoint:            endpoint,
		RequestedAt:         at,
		Latency:             latency,
		Detail:              detail,
		protocolCorrelation: meta,
	}
	if executor != responseInterceptorFallbackExecutor {
		record.AuthID = provider + "-test-auth.json"
		record.AuthIndex = provider + "-test-auth"
	}
	return record
}

func TestProtocolCorrelationCanonicalShapesAcrossTranslation(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	knownIO := protocolCorrelationKnownInput | protocolCorrelationKnownOutput | protocolCorrelationKnownTotal
	knownReasoning := knownIO | protocolCorrelationKnownReasoning
	knownCacheSplit := knownIO | protocolCorrelationKnownCacheRead | protocolCorrelationKnownCacheWrite
	knownCacheCombined := knownIO | protocolCorrelationKnownCacheCombined

	tests := []struct {
		name     string
		fallback UsageRecord
		native   UsageRecord
	}{
		{
			name: "codex to claude drops reasoning detail",
			fallback: testCorrelationRecord("claude", responseInterceptorFallbackExecutor, "route-model", "requested-model", "/v1/messages", "client-key", base.Add(2*time.Second), 0,
				UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}, testCorrelationMeta(knownIO, protocolInputModeAmbiguous, protocolOutputModeReasoningSubset, protocolCacheModeSplit)),
			native: testCorrelationRecord("codex", "CodexExecutor", "route-model", "requested-model", "/v1/responses", "client-key", base, 2*time.Second,
				UsageDetail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, TotalTokens: 120}, testCorrelationMeta(knownReasoning, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit)),
		},
		{
			name: "gemini to claude folds reasoning into output",
			fallback: testCorrelationRecord("claude", responseInterceptorFallbackExecutor, "route-model", "requested-model", "/v1/messages", "client-key", base.Add(2*time.Second), 0,
				UsageDetail{InputTokens: 100, OutputTokens: 25, TotalTokens: 125}, testCorrelationMeta(knownIO, protocolInputModeAmbiguous, protocolOutputModeReasoningSubset, protocolCacheModeSplit)),
			native: testCorrelationRecord("gemini", "GeminiExecutor", "route-model", "requested-model", "/v1beta/models/x:generateContent", "client-key", base, 2*time.Second,
				UsageDetail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, TotalTokens: 125}, testCorrelationMeta(knownReasoning, protocolInputModeCacheSubset, protocolOutputModeReasoningIndependent, protocolCacheModeCombined)),
		},
		{
			name: "claude to gemini combines cache and omits it from total",
			fallback: testCorrelationRecord("gemini", responseInterceptorFallbackExecutor, "route-model", "requested-model", "/v1beta/models/x:generateContent", "client-key", base.Add(2*time.Second), 0,
				UsageDetail{InputTokens: 100, OutputTokens: 20, CachedTokens: 40, TotalTokens: 120}, testCorrelationMeta(knownCacheCombined, protocolInputModeCacheSubset, protocolOutputModeAmbiguous, protocolCacheModeCombined)),
			native: testCorrelationRecord("claude", "ClaudeExecutor", "route-model", "requested-model", "/v1/messages", "client-key", base, 2*time.Second,
				UsageDetail{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheCreationTokens: 10, TotalTokens: 160}, testCorrelationMeta(knownCacheSplit, protocolInputModeCacheIndependent, protocolOutputModeReasoningSubset, protocolCacheModeSplit)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fallback := protocolObservationFromUsageRecord(tt.fallback)
			native := protocolObservationFromUsageRecord(tt.native)
			if len(fallback.Tokens.Shapes) == 0 || len(native.Tokens.Shapes) == 0 {
				t.Fatalf("shapes fallback/native = %#v/%#v", fallback.Tokens.Shapes, native.Tokens.Shapes)
			}
			if _, ok := protocolCorrelationEdge(fallback, native); !ok {
				t.Fatalf("translation observations did not correlate: fallback=%#v native=%#v", fallback.Tokens, native.Tokens)
			}
		})
	}
}

func TestProtocolCorrelationFromRealResponseUsageShapes(t *testing.T) {
	tests := []struct {
		name           string
		sourceFormat   string
		body           string
		selectedAuthID string
		nativeProvider string
		nativeDetail   UsageDetail
		wantProvider   string
	}{
		{
			name:           "OpenAI response with Codex reasoning details",
			sourceFormat:   "openai",
			body:           `{"model":"route-model","usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":5}}}`,
			nativeProvider: "codex",
			nativeDetail:   UsageDetail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, TotalTokens: 120},
			wantProvider:   "openai-compatible",
		},
		{
			name:           "Claude response translated from Codex with split cache",
			sourceFormat:   "claude",
			body:           `{"model":"route-model","usage":{"input_tokens":100,"output_tokens":25,"cache_read_input_tokens":30,"cache_creation_input_tokens":10}}`,
			selectedAuthID: "codex:relay",
			nativeProvider: "codex",
			nativeDetail:   UsageDetail{InputTokens: 140, OutputTokens: 25, TotalTokens: 165},
			wantProvider:   "codex",
		},
		{
			name:           "Claude response folds Gemini reasoning into output",
			sourceFormat:   "claude",
			body:           `{"model":"route-model","usage":{"input_tokens":100,"output_tokens":25,"total_tokens":125}}`,
			selectedAuthID: "gemini:relay",
			nativeProvider: "gemini",
			nativeDetail:   UsageDetail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, TotalTokens: 125},
			wantProvider:   "gemini",
		},
		{
			name:           "Gemini response combines Claude cache buckets",
			sourceFormat:   "gemini",
			body:           `{"model":"route-model","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"cachedContentTokenCount":40,"totalTokenCount":120}}`,
			selectedAuthID: "claude:relay",
			nativeProvider: "claude",
			nativeDetail:   UsageDetail{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheCreationTokens: 10, TotalTokens: 160},
			wantProvider:   "claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fallback, ok := usageRecordFromResponseIntercept(ResponseInterceptRequest{
				SourceFormat:   tt.sourceFormat,
				Model:          "route-model",
				RequestedModel: "requested-model",
				RequestHeaders: map[string][]string{"Authorization": {"Bearer client-key"}},
				RequestBody:    []byte(`{"model":"requested-model"}`),
				Body:           []byte(tt.body),
				StatusCode:     200,
				Metadata:       map[string]any{"selected_auth_id": tt.selectedAuthID},
			})
			if !ok {
				t.Fatal("usageRecordFromResponseIntercept() ok = false")
			}
			if fallback.Provider != tt.wantProvider {
				t.Fatalf("fallback provider = %q, want %q", fallback.Provider, tt.wantProvider)
			}
			latency := 2 * time.Second
			nativeEndpoint := "/v1/responses"
			if tt.sourceFormat == "openai" {
				nativeEndpoint = "/v1/chat/completions"
			}
			native := testCorrelationRecord(tt.nativeProvider, tt.nativeProvider+"Executor", "route-model", "requested-model", nativeEndpoint, "client-key",
				fallback.RequestedAt.Add(-latency), latency, tt.nativeDetail, nil)
			if _, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(fallback), protocolObservationFromUsageRecord(native)); !ok {
				t.Fatalf("real usage shape did not correlate: fallback detail=%#v meta=%#v native=%#v", fallback.Detail, fallback.protocolCorrelation, native.Detail)
			}
		})
	}
}

func TestClaudeResponseWithoutOptionalCacheFieldsCountsOnce(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "gemini"} {
		for _, order := range []string{"fallback-first", "native-first", "persisted"} {
			t.Run(provider+"/"+order, func(t *testing.T) {
				coordinator := withProtocolFallbackTestGlobals(t, time.Hour)
				fallback, ok := usageRecordFromResponseIntercept(ResponseInterceptRequest{
					SourceFormat: "claude", Model: "model", RequestedModel: "model",
					RequestHeaders: map[string][]string{"Authorization": {"Bearer client-key"}},
					RequestBody:    []byte(`{"model":"model"}`),
					Body:           []byte(`{"model":"model","usage":{"input_tokens":100,"output_tokens":20}}`),
					StatusCode:     200,
				})
				if !ok {
					t.Fatal("response usage was not parsed")
				}
				knownIO := protocolCorrelationKnownInput | protocolCorrelationKnownOutput
				if fallback.protocolCorrelation.KnownFields != knownIO {
					t.Fatalf("invented presence for omitted fields: %#v", fallback.protocolCorrelation)
				}
				native := testCorrelationRecord(provider, provider+"Executor", "model", "model", "/v1/messages", "client-key",
					fallback.RequestedAt.Add(-2*time.Second), 2*time.Second,
					UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}, nil)
				// A different complete native input must not become a match merely
				// because the fallback omitted its optional cache fields.
				other := native
				other.Detail.InputTokens = 130
				other.Detail.TotalTokens = 150
				if _, matched := protocolCorrelationEdge(protocolObservationFromUsageRecord(fallback), protocolObservationFromUsageRecord(other)); matched {
					t.Fatal("mismatched native input was accepted")
				}
				recordNative := func() {
					if record, keep := coordinator.HandleNative(native); keep {
						stats.Record(record)
					}
				}
				switch order {
				case "fallback-first":
					coordinator.Schedule(fallback)
					recordNative()
				case "native-first":
					recordNative()
					coordinator.Schedule(fallback)
				case "persisted":
					stats.Record(fallback)
					stats.Record(native)
					raw, err := json.Marshal(stats.Snapshot())
					if err != nil {
						t.Fatal(err)
					}
					var snapshot StatisticsSnapshot
					if err := json.Unmarshal(raw, &snapshot); err != nil {
						t.Fatal(err)
					}
					reconciled, count := reconcileProtocolFallbackSnapshot(snapshot)
					if count != 1 || reconciled.TotalRequests != 1 {
						t.Fatalf("persisted reconciliation: removed %d, requests %d", count, reconciled.TotalRequests)
					}
				}
				coordinator.Flush()
				if order != "persisted" && stats.Snapshot().TotalRequests != 1 {
					t.Fatal("live coordinator retained duplicate usage")
				}
				raw, err := handleExportUsage()
				if err != nil {
					t.Fatal(err)
				}
				var env envelope
				if err := json.Unmarshal(raw, &env); err != nil {
					t.Fatal(err)
				}
				var response ManagementResponse
				if err := json.Unmarshal(env.Result, &response); err != nil {
					t.Fatal(err)
				}
				var exported ExportPayload
				if err := json.Unmarshal(response.Body, &exported); err != nil {
					t.Fatal(err)
				}
				if exported.Usage.TotalRequests != 1 || exported.DetailCount != 1 || exported.Usage.TotalTokens != 120 {
					t.Fatalf("export requests/details/tokens = %d/%d/%d, want 1/1/120", exported.Usage.TotalRequests, exported.DetailCount, exported.Usage.TotalTokens)
				}
			})
		}
	}
}

func TestResponseUsageCorrelationCapturesPresenceBeforeNormalization(t *testing.T) {
	zero, ok := usageRecordFromResponseIntercept(ResponseInterceptRequest{
		SourceFormat:   "gemini",
		RequestedModel: "model",
		RequestHeaders: map[string][]string{"Authorization": {"Bearer client"}},
		Body:           []byte(`{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":20,"thoughtsTokenCount":0,"cachedContentTokenCount":0,"totalTokenCount":20}}`),
		StatusCode:     200,
	})
	if !ok || zero.protocolCorrelation == nil {
		t.Fatalf("zero usage/meta = %#v/%#v, want an observation", zero.Detail, zero.protocolCorrelation)
	}
	known := zero.protocolCorrelation.KnownFields
	for _, bit := range []uint16{protocolCorrelationKnownInput, protocolCorrelationKnownOutput, protocolCorrelationKnownReasoning, protocolCorrelationKnownCacheCombined, protocolCorrelationKnownTotal} {
		if known&bit == 0 {
			t.Fatalf("known fields = %#x, missing bit %#x", known, bit)
		}
	}

	missing, ok := usageRecordFromResponseIntercept(ResponseInterceptRequest{
		SourceFormat:   "gemini",
		RequestedModel: "model",
		RequestHeaders: map[string][]string{"Authorization": {"Bearer client"}},
		Body:           []byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"totalTokenCount":30}}`),
		StatusCode:     200,
	})
	if !ok || missing.protocolCorrelation == nil {
		t.Fatal("usage without optional fields was not parsed")
	}
	if missing.protocolCorrelation.KnownFields&(protocolCorrelationKnownReasoning|protocolCorrelationKnownCacheCombined) != 0 {
		t.Fatalf("missing optional fields became known: %#x", missing.protocolCorrelation.KnownFields)
	}
}

func TestProtocolCorrelationUnknownAndExplicitZeroEvidence(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	knownIO := protocolCorrelationKnownInput | protocolCorrelationKnownOutput | protocolCorrelationKnownTotal
	native := testCorrelationRecord("codex", "CodexExecutor", "model", "model", "/v1/responses", "client", base, time.Second,
		UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		testCorrelationMeta(knownIO|protocolCorrelationKnownReasoning, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit))
	explicitNonzero := testCorrelationRecord("openai-compatible", responseInterceptorFallbackExecutor, "model", "model", "/v1/responses", "client", base.Add(time.Second), 0,
		UsageDetail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, TotalTokens: 120},
		testCorrelationMeta(knownIO|protocolCorrelationKnownReasoning, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit))
	if _, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(explicitNonzero), protocolObservationFromUsageRecord(native)); ok {
		t.Fatal("known reasoning conflict was accepted")
	}

	missing := explicitNonzero
	missing.Detail.ReasoningTokens = 0
	missing.protocolCorrelation = testCorrelationMeta(knownIO, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit)
	if _, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(missing), protocolObservationFromUsageRecord(native)); !ok {
		t.Fatal("missing reasoning evidence should not veto the match")
	}

	zero := missing
	zero.protocolCorrelation = testCorrelationMeta(knownIO|protocolCorrelationKnownReasoning, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit)
	if _, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(zero), protocolObservationFromUsageRecord(native)); !ok {
		t.Fatal("matching explicit zero reasoning was rejected")
	}
}

func TestProtocolCorrelationRejectsWeakOrUnsafeEvidence(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	native := testCorrelationRecord("codex", "CodexExecutor", "model", "model", "/v1/responses", "client", base, time.Second,
		UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		testCorrelationMeta(protocolCorrelationKnownInput|protocolCorrelationKnownOutput|protocolCorrelationKnownTotal, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit))
	weak := testCorrelationRecord("openai-compatible", responseInterceptorFallbackExecutor, "model", "model", "/v1/responses", "client", base.Add(time.Second), 0,
		UsageDetail{TotalTokens: 120}, nil)
	if _, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(weak), protocolObservationFromUsageRecord(native)); ok {
		t.Fatal("total-only fallback was correlated")
	}

	unsafe := native
	unsafe.Detail.InputTokens = int64(^uint64(0) >> 1)
	unsafe.Detail.OutputTokens = 1
	unsafe.protocolCorrelation = testCorrelationMeta(protocolCorrelationKnownInput|protocolCorrelationKnownOutput, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit)
	if _, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(unsafe), protocolObservationFromUsageRecord(native)); ok {
		t.Fatal("overflowing shape was correlated")
	}

	failedNative := native
	failedNative.Failed = true
	failedNative.Failure.StatusCode = 500
	failedFallback := weak
	failedFallback.Failed = true
	failedFallback.Failure.StatusCode = 0 // status missing is unknown, not zero
	failedFallback.Detail = native.Detail
	failedFallback.protocolCorrelation = native.protocolCorrelation
	if _, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(failedFallback), protocolObservationFromUsageRecord(failedNative)); !ok {
		t.Fatal("missing failure status should not veto a candidate")
	}
	failedFallback.Failure.StatusCode = 501
	if _, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(failedFallback), protocolObservationFromUsageRecord(failedNative)); ok {
		t.Fatal("different known failure statuses were correlated")
	}

	synthetic := native
	synthetic.RequestedAt = base.Add(2 * time.Second)
	if _, ok := protocolCorrelationEdge(protocolCorrelationObservation{
		Role: protocolCorrelationRoleFallback, RequestedModel: "model", ClientIdentity: "client", Timestamp: synthetic.RequestedAt,
		Tokens: protocolObservationFromUsageRecord(native).Tokens,
	}, protocolObservationFromRequestDetail("model", RequestDetail{})); ok {
		t.Fatal("invalid observation was correlated")
	}
}

func TestProtocolCorrelationRejectsUnknownFutureSchema(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	knownIO := protocolCorrelationKnownInput | protocolCorrelationKnownOutput | protocolCorrelationKnownTotal
	fallback := testCorrelationRecord("openai-compatible", responseInterceptorFallbackExecutor, "model", "model", "/v1/responses", "client", base.Add(time.Second), 0,
		UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		testCorrelationMeta(knownIO, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit))
	native := testCorrelationRecord("codex", "CodexExecutor", "model", "model", "/v1/responses", "client", base, time.Second,
		UsageDetail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		testCorrelationMeta(knownIO, protocolInputModeCacheSubset, protocolOutputModeReasoningSubset, protocolCacheModeSplit))
	fallback.protocolCorrelation.SchemaVersion = protocolCorrelationSchemaVersion + 1

	observation := protocolObservationFromUsageRecord(fallback)
	if !observation.Tokens.Invalid || len(observation.Tokens.Shapes) != 0 {
		t.Fatalf("future schema evidence = %#v, want invalid with no shapes", observation.Tokens)
	}
	if _, ok := protocolCorrelationEdge(observation, protocolObservationFromUsageRecord(native)); ok {
		t.Fatal("unknown future correlation schema was used to delete a fallback")
	}

	stored := NewRequestStatistics()
	stored.Record(fallback)
	stored.Record(native)
	reconciled, removed := reconcileProtocolFallbackSnapshot(stored.Snapshot())
	if removed != 0 || reconciled.TotalRequests != 2 {
		t.Fatalf("future schema snapshot reconciliation removed/requests = %d/%d, want 0/2", removed, reconciled.TotalRequests)
	}
}

func TestRequestedModelSurvivesDetailAndSnapshotRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	record := testCorrelationRecord("codex", "CodexExecutor", "route-model", "requested-model", "/v1/responses", "client", at, time.Second,
		UsageDetail{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}, nil)
	detail := requestDetailFromUsageRecord(record, at, headerWhitelist{})
	if detail.RequestedModel != "requested-model" || detail.Model != "route-model" {
		t.Fatalf("requested/model = %q/%q", detail.RequestedModel, detail.Model)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RequestDetail
	err = json.Unmarshal(encoded, &decoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RequestedModel != detail.RequestedModel || decoded.Correlation == nil {
		t.Fatalf("round-tripped detail = %#v", decoded)
	}

	stats := NewRequestStatistics()
	stats.Record(record)
	snapshot := stats.Snapshot()
	got := snapshot.APIs[usageGroupKey(record)].Models[record.Model].Details[0]
	got.Correlation.KnownFields = 0
	if stats.Snapshot().APIs[usageGroupKey(record)].Models[record.Model].Details[0].Correlation.KnownFields == 0 {
		t.Fatal("snapshot correlation pointer aliases stored detail")
	}
}

func TestRequestedModelAliasCorrelatesAcrossPersistenceBoundaries(t *testing.T) {
	fallback, native := protocolFallbackTestRecords()
	fallback.Model = "route-model"
	fallback.Alias = "requested-model"
	fallback.Endpoint = "/v1/responses"
	native.Model = fallback.Model
	native.Alias = fallback.Alias
	native.Endpoint = fallback.Endpoint

	assertRequestedModel := func(t *testing.T, snapshot StatisticsSnapshot) {
		t.Helper()
		api := snapshot.APIs[usageGroupKey(native)]
		details := api.Models[native.Model].Details
		if len(details) != 1 {
			t.Fatalf("persisted details = %d, want 1", len(details))
		}
		if details[0].RequestedModel != native.Alias || details[0].Model != native.Model {
			t.Fatalf("requested/model = %q/%q, want %q/%q", details[0].RequestedModel, details[0].Model, native.Alias, native.Model)
		}
	}

	stats := NewRequestStatistics()
	stats.Record(fallback)
	stats.Record(native)
	original := stats.Snapshot()
	reconciled, count := reconcileProtocolFallbackSnapshot(original)
	if count != 1 {
		t.Fatalf("snapshot reconciliation count = %d, want 1", count)
	}
	assertRequestedModel(t, reconciled)

	encoded, err := json.Marshal(ExportPayload{Version: 1, Usage: original})
	if err != nil {
		t.Fatal(err)
	}
	var exported ExportPayload
	if err := json.Unmarshal(encoded, &exported); err != nil {
		t.Fatal(err)
	}
	imported := NewRequestStatistics()
	result := imported.MergeSnapshot(exported.Usage)
	if result.Added != 1 || result.Skipped != 1 {
		t.Fatalf("import result = %#v, want added=1 skipped=1", result)
	}
	assertRequestedModel(t, imported.Snapshot())

	records := []persistedDetail{
		{API: usageGroupKey(fallback), Model: fallback.Model, Detail: requestDetailFromUsageRecord(fallback, fallback.RequestedAt, headerWhitelist{})},
		{API: usageGroupKey(native), Model: native.Model, Detail: requestDetailFromUsageRecord(native, native.RequestedAt, headerWhitelist{})},
	}
	reconciledRecords, removed := reconcilePersistedProtocolFallbacks(records)
	if removed != 1 || len(reconciledRecords) != 1 {
		t.Fatalf("persisted reconciliation removed/records = %d/%d, want 1/1", removed, len(reconciledRecords))
	}
	if reconciledRecords[0].Detail.RequestedModel != native.Alias {
		t.Fatalf("persisted requested model = %q, want %q", reconciledRecords[0].Detail.RequestedModel, native.Alias)
	}
}
