package main

// Local-only migration verification. Excluded through .git/info/exclude;
// these fixtures and tests are not part of the intended implementation commit.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var snapshotCatalogTestNow = time.Date(2026, 9, 13, 9, 0, 0, 123, time.FixedZone("catalog-test", 8*3600+17))

func snapshotCatalogForTest(t *testing.T, s *sqliteLedger, raw string) (string, sqliteMigrationSnapshotCatalog, map[int64]sqliteMigrationSnapshotGroup) {
	t.Helper()
	path := compareProjectedSnapshotForTest(t, s, raw)
	catalog, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
	if err != nil || !catalog.Complete {
		t.Fatalf("catalog completion=%t events=%d err=%v", catalog.Complete, catalog.Events, err)
	}
	groups := readSnapshotCatalogGroupsForTest(t, s, path)
	return path, catalog, groups
}

func readSnapshotCatalogGroupsForTest(t *testing.T, s *sqliteLedger, path string) map[int64]sqliteMigrationSnapshotGroup {
	t.Helper()
	groups := make(map[int64]sqliteMigrationSnapshotGroup)
	err := s.WithMigrationSnapshotCatalog(context.Background(), path, func(reader *sqliteSnapshotCatalogReader) error {
		return reader.WalkGroups(func(group sqliteMigrationSnapshotGroup) error { groups[group.Node] = group; return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	return groups
}

func requestStatsTotalsForTest(s *RequestStatistics) sqliteSnapshotTotals {
	return sqliteSnapshotTotals{s.totalRequests, s.successCount, s.failureCount, s.totalTokens,
		s.inputTokens, s.outputTokens, s.cachedTokens, s.cacheWriteTokens, s.reasoningTokens, s.latencySum, s.latencyN}
}

func TestSQLiteSnapshotCatalogMatchesLegacyAggregateBasis(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"absent-usage", projectionSnapshotJSON(`"ignored":{"apis":{}}`)},
		{"null-usage", projectionSnapshotJSON(`"usage":null`)},
		{"aggregate-only", projectionSnapshotJSON(`"usage":{"total_requests":20,"success_count":14,"failure_count":6,"total_tokens":900,"avg_latency_ms":1.25,"apis":{"API":{"total_requests":20,"models":{"M":{"total_requests":18,"total_tokens":700,"input_tokens":33,"cached_tokens":15,"cache_write_tokens":5}}}}}`)},
		{"counts-not-maxed-from-details", projectionSnapshotJSON(`"usage":{"total_requests":1,"total_tokens":1,"apis":{"API":{"total_requests":1,"total_tokens":1,"models":{"M":{"total_requests":1,"total_tokens":1,"details":[{"model":"M","tokens":{"input_tokens":3},"latency_ms":5},{"model":"M","tokens":{"input_tokens":4},"latency_ms":7}]}}}}}`)},
		{"split-model-residual", projectionSnapshotJSON(`"usage":{"total_requests":10,"apis":{"API":{"total_requests":10,"models":{"M":{"total_requests":10,"success_count":8,"failure_count":2,"total_tokens":100,"input_tokens":70,"avg_latency_ms":11,"details":[{"model":"N","tokens":{"input_tokens":8},"latency_ms":13},{"model":"M","failed":true,"tokens":{"input_tokens":7},"latency_ms":9}]}}}}}`)},
		{"split-api-disregards-outer-request-counts", projectionSnapshotJSON(`"usage":{"total_requests":30,"apis":{"API":{"total_requests":25,"total_tokens":9999,"input_tokens":7777,"models":{"M":{"total_requests":10,"total_tokens":100,"input_tokens":60,"details":[{"model":"M","source":"east","tokens":{"input_tokens":3}},{"model":"M","source":"west","tokens":{"input_tokens":4}}]},"N":{"total_requests":2,"input_tokens":13,"accounting":[{"model":"K","source":"east","tokens":{"input_tokens":9}}]}}}}}`)},
		{"zero-total-rebuild", projectionSnapshotJSON(`"usage":{"total_requests":0,"input_tokens":99999,"apis":{"API":{"total_requests":50,"models":{"M":{"total_requests":50,"input_tokens":9000,"details":[{"tokens":{"input_tokens":3}}],"accounting":[{"failed":true,"tokens":{"input_tokens":4}}]}}}}}`)},
		{"ignore-blank-api-keep-empty-models", projectionSnapshotJSON(`"usage":{"total_requests":10,"apis":{" ":{"models":{"M":{"details":[{"tokens":{"input_tokens":999}}]}}},"API":null,"api":{"models":{"":{},"M":null}}}}`)},
		{"latency-saturation", projectionSnapshotJSON(`"usage":{"total_requests":9223372036854775807,"avg_latency_ms":1e308,"apis":{"API":{"total_requests":9223372036854775807,"models":{"M":{"total_requests":9223372036854775807,"avg_latency_ms":1e308,"input_tokens":9223372036854775807,"details":[{"tokens":{"input_tokens":9223372036854775807},"latency_ms":9223372036854775807},{}]}}}}}`)},
		{"normalized-model-collisions", projectionSnapshotJSON(`"usage":{"total_requests":5,"apis":{" API ":{"total_requests":5,"models":{"M":{"input_tokens":3}," M ":{"input_tokens":4},"unknown":{"details":[{"model":"N","tokens":{"input_tokens":8}}]}}},"API":{"total_requests":2,"models":{"M":{"input_tokens":9}}}}}`)},
		{"details-order-and-real-duplicates", projectionSnapshotJSON(`"usage":{"total_requests":10,"apis":{"sk-local-only-fixture":{"models":{"M":{"total_requests":10,"accounting":[{"provider":"claude","source":"archive","tokens":{"input_tokens":2}}],"details":[{"provider":"CLAUDE","source":"visible","tokens":{"input_tokens":3}},{"provider":"CLAUDE","source":"visible","tokens":{"input_tokens":3}}]}}}}}`)},
		{"series-not-mistaken-for-aggregate-scalars", projectionSnapshotJSON(`"usage":{"total_requests":4,"input_tokens":5,"requests_by_day":{"total_requests":999},"cost_by_hour":{"3":12.3},"cost_tokens_by_day":{"2026-09-13":[{"model":"M","input_tokens":9000}]},"apis":{}}`)},
	}
	for _, tc := range cases {
		for _, version := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s-v%d", tc.name, version), func(t *testing.T) {
				s, _ := testSQLiteLedger(t)
				raw := strings.Replace(tc.raw, `"version":2`, fmt.Sprintf(`"version":%d`, version), 1)
				_, catalog, groups := snapshotCatalogForTest(t, s, raw)
				var legacy persistedStorageSnapshot
				if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
					t.Fatal(err)
				}
				if tc.name == "ignore-blank-api-keep-empty-models" && len(legacy.Usage.APIs) != 3 {
					t.Fatal("invalid empty-group fixture")
				}
				if tc.name == "normalized-model-collisions" && len(legacy.Usage.APIs) != 2 {
					t.Fatal("invalid API collision fixture")
				}
				if version < currentStorageSnapshotVersion {
					migrateLegacySnapshotCacheReads(&legacy.Usage)
				}
				want := NewRequestStatistics()
				want.restoreStorageSnapshotLocked(legacy.Usage, snapshotCatalogTestNow)
				root := groups[catalog.Root]
				if root.Restored != requestStatsTotalsForTest(want) {
					t.Fatalf("aggregate basis differs: got %+v want %+v", root.Restored, requestStatsTotalsForTest(want))
				}
				if root.Fallbacks != 0 || root.CatalogGroups != int64(len(groups)) {
					t.Fatal("ordinary snapshot catalog lost groups or invented protocol fallback")
				}
				if tc.name == "details-order-and-real-duplicates" {
					for _, group := range groups {
						if group.Scope == "model" && (group.FirstResidualAPI.API != "visible" || group.Visible != 2 || group.Archived != 1 || !group.SplitAPI) {
							t.Fatalf("details/accounting order or real duplicates changed: %+v", group)
						}
					}
				}
				if tc.name == "zero-total-rebuild" && !root.RebuildFromDetails {
					t.Fatal("zero-total snapshot must discard old residuals before publication")
				}
			})
		}
	}
}

func TestSQLiteSnapshotCatalogProviderSemantics(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, _ := testSQLiteLedger(t)
			raw := fmt.Sprintf(`{"version":%d,"generated_at":"2026-09-13T00:00:00Z","usage":{"total_requests":12,"apis":{"API":{"models":{"M":{"total_requests":12,"cached_tokens":15,"cache_write_tokens":5,"providers":[{"provider":" CLAUDE ","total_requests":10,"success_count":8,"failure_count":2,"cached_tokens":15,"cache_write_tokens":5},{"provider":"claude","total_requests":2,"input_tokens":4},{"provider":"ignored","total_requests":0,"input_tokens":99},{"provider":"negative","total_requests":-2}],"accounting":[{"provider":"claude","tokens":{"input_tokens":3,"cached_tokens":2,"cache_write_tokens":1}}],"details":[{"provider":"Claude","tokens":{"input_tokens":4,"cached_tokens":2,"cache_write_tokens":1}},{"provider":"","tokens":{"input_tokens":1}}]}}}}}}`, version)
			path, _, groups := snapshotCatalogForTest(t, s, raw)
			var model sqliteMigrationSnapshotGroup
			for _, group := range groups {
				if group.Scope == "model" {
					model = group
				}
			}
			var legacy persistedStorageSnapshot
			if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
				t.Fatal(err)
			}
			if version < currentStorageSnapshotVersion {
				migrateLegacySnapshotCacheReads(&legacy.Usage)
			}
			m := legacy.Usage.APIs["API"].Models["M"]
			wantSnapshot := modelProviderStatsFromSnapshot(m.Providers)
			var wantDetails map[string]*ModelProviderStat
			for _, detail := range m.accountingDetails() {
				detail = normalizeStorageSnapshotDetailFields("M", detail, snapshotCatalogTestNow)
				wantDetails = incrementModelProviderStats(wantDetails, detail.Provider, detail.Failed, detailTotalsFromRequest(detail))
			}
			wantResidual := residualModelProviderStats(m.Providers, wantDetails)
			seen := 0
			err := s.WithMigrationSnapshotCatalog(context.Background(), path, func(reader *sqliteSnapshotCatalogReader) error {
				return reader.WalkProviders(model.Node, func(p sqliteMigrationSnapshotProvider) error {
					seen++
					if want := wantSnapshot[p.Key]; want != nil && *want != p.Snapshot {
						t.Fatalf("snapshot provider changed: got %+v want %+v", p.Snapshot, *want)
					}
					if want := wantDetails[p.Key]; want != nil && *want != p.Details {
						t.Fatalf("detail provider changed: got %+v want %+v", p.Details, *want)
					}
					if !reflect.DeepEqual(p.residual(), wantResidual[p.Key]) {
						t.Fatalf("provider residual differs for %q", p.Key)
					}
					return nil
				})
			})
			if err != nil || !model.HasProviderSnapshot || seen != 2 || model.CatalogProviders != 2 {
				t.Fatalf("provider scan count=%d catalog=%d snapshot=%t err=%v", seen, model.CatalogProviders, model.HasProviderSnapshot, err)
			}
		})
	}
}

func TestSQLiteSnapshotCatalogDoesNotTouchLiveAuthority(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	id := "local-snapshot-catalog-auth-only"
	before := authIndexes.Lookup(id)
	raw := projectionModelJSON(`"details":[{"auth_id":"` + id + `","auth_index":"index-for-local-catalog","api_key":"sk-local-fixture-only","failure":"` + strings.Repeat("x", 5<<20) + `","latency_ms":-2}]`)
	path, _, _ := snapshotCatalogForTest(t, s, raw)
	if authIndexes.Lookup(id) != before {
		t.Fatal("isolated catalog seeded live auth index")
	}
	var generation, records, states, progress int
	if err := s.reader.QueryRow(`SELECT generation,(SELECT count(*) FROM ledger_records),(SELECT count(*) FROM ledger_state),(SELECT count(*) FROM ledger_progress) FROM ledger_meta`).Scan(&generation, &records, &states, &progress); err != nil {
		t.Fatal(err)
	}
	if generation != 0 || records != 0 || states != 0 || progress != 0 {
		t.Fatal("catalog changed authoritative state")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, []byte(raw)) {
		t.Fatal("snapshot source changed")
	}
}

func TestSQLiteSnapshotCatalogResumesAtomicBatches(t *testing.T) {
	s, dbPath := testSQLiteLedger(t)
	raw := projectionModelJSON(`"total_requests":900,"details":[` + strings.Repeat(`{"tokens":{"input_tokens":3}},`, 649) + `{"tokens":{"input_tokens":3}}]`)
	path := compareProjectedSnapshotForTest(t, s, raw)
	if _, err := s.writer.Exec(`CREATE TRIGGER snapshot_batch_fail BEFORE INSERT ON migration_snapshot_providers
 WHEN (SELECT events FROM migration_snapshot_catalogs WHERE source=NEW.source)>=256
 BEGIN SELECT RAISE(ABORT,'local injected catalog failure'); END`); err != nil {
		t.Fatal(err)
	}
	first, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
	if err == nil || first.Complete || first.Events < 256 {
		t.Fatalf("batch failure did not leave resumable progress: events=%d complete=%t err=%v", first.Events, first.Complete, err)
	}
	if err := s.WithMigrationSnapshotCatalog(context.Background(), path, func(*sqliteSnapshotCatalogReader) error { t.Fatal("partial catalog exposed"); return nil }); !errors.Is(err, errSQLiteSnapshotCatalogIncomplete) {
		t.Fatalf("partial catalog read: %v", err)
	}
	var node int64
	if err := s.reader.QueryRow("SELECT node FROM migration_snapshot_groups WHERE source=? AND scope='model'", path).Scan(&node); err != nil {
		t.Fatal(err)
	}
	g, err := readSnapshotCatalogGroup(context.Background(), s.reader, path, node)
	if err != nil {
		t.Fatal(err)
	}
	p, found, err := readSnapshotCatalogProvider(context.Background(), s.reader, path, node, "")
	if err != nil || !found || g.Details.TotalRequests != p.Details.TotalRequests || g.Details.InputTokens != p.Details.InputTokens {
		t.Fatal("failed transaction separated group and provider totals")
	}
	if _, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow.Add(time.Second)); !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("retry accepted a different recovery clock: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openSQLiteLedger(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.writer.Exec("DROP TRIGGER snapshot_batch_fail"); err != nil {
		t.Fatal(err)
	}
	last, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
	if err != nil || !last.Complete || last.Root != first.Root || last.Events <= first.Events {
		t.Fatalf("reopen resume failed: %+v %v", last, err)
	}
	groups := readSnapshotCatalogGroupsForTest(t, s, path)
	if groups[node].Details.TotalRequests != 650 || groups[node].Details.InputTokens != 1950 {
		t.Fatal("retry lost or double-counted committed details")
	}
	before := groups
	if _, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, readSnapshotCatalogGroupsForTest(t, s, path)) {
		t.Fatal("completed retry changed catalog contents")
	}
}

func TestSQLiteSnapshotCatalogCompletionFailureAndPrefixMismatch(t *testing.T) {
	for _, corruptPrefix := range []bool{false, true} {
		t.Run(fmt.Sprint(corruptPrefix), func(t *testing.T) {
			s, _ := testSQLiteLedger(t)
			path := compareProjectedSnapshotForTest(t, s, projectionModelJSON(`"details":[`+strings.Repeat(`{},`, 649)+`{}]`))
			if _, err := s.writer.Exec(`CREATE TRIGGER snapshot_complete_fail BEFORE UPDATE OF complete ON migration_snapshot_catalogs
 WHEN NEW.complete=1 BEGIN SELECT RAISE(ABORT,'local completion failure'); END`); err != nil {
				t.Fatal(err)
			}
			first, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
			if err == nil || first.Complete || first.Events == 0 {
				t.Fatal("completion failure was not retained")
			}
			if _, err := s.writer.Exec("DROP TRIGGER snapshot_complete_fail"); err != nil {
				t.Fatal(err)
			}
			if corruptPrefix {
				// Rebind a committed detail to a DIFFERENT valid fragment. Raw
				// source SHA still matches; the effective prefix must reject it.
				changed, err := s.writer.Exec(`UPDATE migration_projection_values SET (start,finish,checksum)=
 (SELECT start,finish,checksum FROM migration_projection_values v JOIN migration_projection_nodes n ON n.id=v.node
 WHERE n.source=? AND n.scope='detail' ORDER BY v.node DESC LIMIT 1)
 WHERE node=(SELECT id FROM migration_projection_nodes WHERE source=? AND scope='detail' AND kind='value' ORDER BY id LIMIT 1)`, path, path)
				if err := sqliteRequireChanged(changed, err); err != nil {
					t.Fatal(err)
				}
				last, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
				if !errors.Is(err, errSQLiteLedgerConflict) || last.Complete || last.Events != first.Events {
					t.Fatalf("changed effective prefix advanced catalog: events=%d err=%v", last.Events, err)
				}
				return
			}
			last, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
			if err != nil || !last.Complete {
				t.Fatal(err)
			}
			groups := readSnapshotCatalogGroupsForTest(t, s, path)
			if groups[last.Root].Details.TotalRequests != 650 {
				t.Fatal("completion retry duplicated details")
			}
		})
	}
}

func TestSQLiteSnapshotCatalogCancellationConsumerAndConcurrency(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	path := compareProjectedSnapshotForTest(t, s, projectionModelJSON(`"details":[`+strings.Repeat(`{},`, 519)+`{}]`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CatalogMigrationSnapshot(ctx, path, snapshotCatalogTestNow); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled catalog: %v", err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, _ = s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow) })
	}
	wg.Wait()
	catalog, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
	if err != nil || !catalog.Complete {
		t.Fatal(err)
	}
	var escaped *sqliteSnapshotCatalogReader
	stop := errors.New("local consumer stop")
	err = s.WithMigrationSnapshotCatalog(context.Background(), path, func(reader *sqliteSnapshotCatalogReader) error {
		escaped = reader
		return reader.WalkGroups(func(sqliteMigrationSnapshotGroup) error { return stop })
	})
	if !errors.Is(err, stop) {
		t.Fatalf("consumer error: %v", err)
	}
	if err := escaped.WalkGroups(func(sqliteMigrationSnapshotGroup) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("escaped reader retained its lifetime: %v", err)
	}
	if got := readSnapshotCatalogGroupsForTest(t, s, path)[catalog.Root].Details.TotalRequests; got != 520 {
		t.Fatalf("concurrent catalogs duplicated details: %d", got)
	}
}

func TestSQLiteSnapshotCatalogGeneratedTotals(t *testing.T) {
	random := rand.New(rand.NewSource(73291))
	for fixture := range 24 {
		s, _ := testSQLiteLedger(t)
		snapshot := StatisticsSnapshot{TotalRequests: int64(random.Intn(100)), APIs: make(map[string]APISnapshot)}
		for apiIndex := range 3 {
			api := APISnapshot{TotalRequests: int64(random.Intn(100)), Models: make(map[string]ModelSnapshot)}
			for modelIndex := range 3 {
				model := ModelSnapshot{TotalRequests: int64(random.Intn(100)), TotalTokens: int64(random.Intn(200)), InputTokens: int64(random.Intn(50)), CachedTokens: 13, CacheWriteTokens: 5, AvgLatencyMs: float64(random.Intn(100)) / 4}
				for detailIndex := range 5 {
					detail := RequestDetail{Model: fmt.Sprintf("m%d", random.Intn(3)), Source: fmt.Sprintf("s%d", random.Intn(2)), Failed: random.Intn(2) == 0, LatencyMs: int64(random.Intn(20)), Tokens: TokenStats{InputTokens: int64(random.Intn(40)), CachedTokens: 9, CacheWriteTokens: 3}}
					if detailIndex < 3 {
						model.Details = append(model.Details, detail)
					} else {
						model.Accounting = append(model.Accounting, detail)
					}
				}
				api.Models[fmt.Sprintf("m%d", modelIndex)] = model
			}
			snapshot.APIs[fmt.Sprintf("api%d", apiIndex)] = api
		}
		version := fixture % 3
		raw, err := json.Marshal(persistedStorageSnapshot{Version: version, GeneratedAt: "2026-09-13T00:00:00Z", Usage: snapshot})
		if err != nil {
			t.Fatal(err)
		}
		_, catalog, groups := snapshotCatalogForTest(t, s, string(raw))
		if version < currentStorageSnapshotVersion {
			migrateLegacySnapshotCacheReads(&snapshot)
		}
		want := NewRequestStatistics()
		want.restoreStorageSnapshotLocked(snapshot, snapshotCatalogTestNow)
		if got := groups[catalog.Root].Restored; got != requestStatsTotalsForTest(want) {
			t.Fatalf("fixture %d aggregate mismatch: got %+v want %+v", fixture, got, requestStatsTotalsForTest(want))
		}
	}
}

func TestStorageSnapshotLatencySaturatesPortably(t *testing.T) {
	for _, tc := range []struct {
		average             float64
		count, sum, samples int64
	}{
		{1.25, 10, 13, 10}, {0.1, 1, 0, 1}, {0, 10, 0, 0}, {-1, 10, 0, 0},
		{math.NaN(), 10, 0, 0}, {math.Inf(1), 10, math.MaxInt64, 10},
		{1e308, math.MaxInt64, math.MaxInt64, math.MaxInt64}, {1, math.MaxInt64, math.MaxInt64, math.MaxInt64},
	} {
		sum, samples := restoredLatencyAggregate(tc.average, tc.count)
		if sum != tc.sum || samples != tc.samples {
			t.Fatalf("latency overflow/rounding mismatch: sum=%d samples=%d", sum, samples)
		}
	}
}
