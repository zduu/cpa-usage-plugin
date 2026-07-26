package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUsageExportRegressionMergesCodexOAuthSourceFallback(t *testing.T) {
	assertUsageExportMergesCodexOAuthSourceFallback(t,
		filepath.Join("testdata", "codex-oauth-source-fallback.json"),
		2,
		295_843,
		"codex · hide-my-email@privaterelay.example.com",
		"codex · 上游 ba5eba11ba5eba11",
	)

}

func assertUsageExportMergesCodexOAuthSourceFallback(
	t *testing.T, path string,
	wantRequests, wantTokens int64,
	wantAPI, legacyChannel string,
) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read regression export: %v", err)
	}
	var payload ExportPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode regression export: %v", err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{RetentionDays: 0, DedupWindowMinutes: 0})
	result := stats.MergeSnapshot(payload.Usage)
	if result.Added != payload.DetailCount || result.Skipped != 0 {
		t.Fatalf("merge result = %#v, want all %d exported details imported once", result, payload.DetailCount)
	}

	snapshot := stats.Snapshot()
	if _, ok := snapshot.APIs[legacyChannel]; ok {
		t.Fatalf("snapshot APIs still contain fallback Codex OAuth group: %#v", snapshot.APIs)
	}
	api := snapshot.APIs[wantAPI]
	if len(snapshot.APIs) != 1 || api.TotalRequests != wantRequests || api.TotalTokens != wantTokens {
		t.Fatalf("merged API = %#v, want one group with %d requests and %d tokens; all APIs=%#v", api, wantRequests, wantTokens, snapshot.APIs)
	}
	if snapshot.TotalRequests != payload.Usage.TotalRequests || snapshot.TotalTokens != payload.Usage.TotalTokens {
		t.Fatalf("snapshot totals = requests %d tokens %d, want export totals %d/%d", snapshot.TotalRequests, snapshot.TotalTokens, payload.Usage.TotalRequests, payload.Usage.TotalTokens)
	}
}
