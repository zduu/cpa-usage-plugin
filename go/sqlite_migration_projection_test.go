package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Test-only reconstruction. Production migration consumes typed values into
// disk-backed state; it must not reassemble the entire snapshot in memory.
func projectedSnapshotForTest(t *testing.T, s *sqliteLedger, path string) persistedStorageSnapshot {
	t.Helper()
	var builder sqliteMigrationTestJSON
	_, err := s.WalkMigrationProjection(context.Background(), path, func(item sqliteMigrationProjectedItem) error {
		var raw []byte
		if item.Kind == "value" {
			value := item.Value
			if detail, ok := value.(RequestDetail); ok {
				// The normal export omits empty headers. This test additionally
				// compares nil versus empty maps in the legacy decoded value.
				value = struct {
					RequestDetail
					Headers map[string][]string `json:"headers"`
				}{detail, detail.Headers}
			}
			var err error
			raw, err = json.Marshal(value)
			if err != nil {
				return err
			}
		}
		return builder.consume(sqliteMigrationItem{Path: item.Path, Kind: item.Kind, Scope: item.Scope, Value: raw})
	})
	if err != nil {
		t.Fatal(err)
	}
	var result persistedStorageSnapshot
	if err := json.Unmarshal(builder.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func compareProjectedSnapshotForTest(t *testing.T, s *sqliteLedger, raw string) string {
	t.Helper()
	var want persistedStorageSnapshot
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatalf("invalid reference fixture: %v", err)
	}
	path := writeSQLiteMigrationSource(t, []byte(raw))
	if _, err := s.StageSource(context.Background(), path, "snapshot"); err != nil {
		t.Fatal(err)
	}
	projection, err := s.ProjectMigrationSource(context.Background(), path)
	if err != nil || !projection.Complete {
		t.Fatalf("projection complete=%t events=%d err=%v", projection.Complete, projection.Events, err)
	}
	got := projectedSnapshotForTest(t, s, path)
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("projection differs from legacy typed decode:\ngot %s\nwant %s", gotJSON, wantJSON)
	}
	return path
}

func TestSQLiteProjectionMatchesLegacyDuplicateAndNullSemantics(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	cases := []struct{ name, raw string }{
		{"empty", projectionSnapshotJSON(`"usage":{}`)},
		{"null-usage", projectionSnapshotJSON(`"usage":null`)},
		{"scalars-and-unicode-folding", projectionSnapshotJSON(`"VERSION":1,"version":null,"usage":{"total_requests":9223372036854775807,"total_requests":null,"reaſoning_tokens":7,"avg_latency_ms":1.125},"USAGE":{"success_count":-2},"usage":null`)},
		{"maps-merge-entries-replace-values", projectionSnapshotJSON(`"usage":{"apis":{"keep":{"total_requests":7},"replace":{"total_requests":5,"models":{"old":{}}}},"APIS":{"replace":{"total_tokens":3},"null":null},"requests_by_day":{"a":9,"a":null,"b":3}},"usage":{"apis":{"replace":{"failure_count":2}},"requests_by_day":{"c":4}}`)},
		{"maps-null-and-empty", projectionSnapshotJSON(`"usage":{"apis":{"old":{}},"apis":null,"apis":{},"cost_by_day":{},"cost_by_hour":null,"requests_by_day":{"a":3},"requests_by_day":null,"requests_by_day":{"b":4}}`)},
		{"identity-keys-not-folded", projectionSnapshotJSON(`"usage":{"apis":{"":{"models":{"":{},"M":{},"m":{}}},"API":{},"api":{},"中/文":{},"\u0000":{}},"requests_by_hour":{"01":7,"1":9,"":0}}`)},
		{"model-map-value-replacement", projectionSnapshotJSON(`"usage":{"apis":{"API":{"models":{"M":{"total_requests":5,"details":[{"model":"old"}]},"M":{"total_tokens":3}},"models":{"M":{"success_count":4}}}}}`)},
		{"token-map-array-replacement", projectionSnapshotJSON(`"usage":{"cost_tokens_by_day":{"a":[{"model":"old","total_tokens":9}],"a":[{}],"b":[]},"cost_tokens_by_day":{"a":[null],"c":null}}`)},
		{"array-reused-elements-and-hidden-tail", projectionModelJSON(`"details":[{"model":"first","tokens":{"input_tokens":7},"headers":{"x":["one"]}},{"model":"second","latency_ms":8},{"model":"third"}],"details":[{"ttft_ms":4}],"details":[{"tokens":{"output_tokens":9},"headers":{}},null,{"latency_ms":10}]`)},
		{"array-empty-clears-backing", projectionModelJSON(`"details":[{"model":"old"},{"model":"hidden"}],"details":[{}],"details":[],"details":[{},{}]`)},
		{"array-null-clears-backing", projectionModelJSON(`"details":[{"model":"old"}],"details":null,"details":[{}],"accounting":[{"model":"archived"}],"accounting":[null]`)},
		{"reused-provider-and-accounting-elements", projectionModelJSON(`"providers":[{"provider":"claude","total_requests":5,"cached_tokens":15}],"providers":[{"cache_write_tokens":5}],"accounting":[{"timestamp":"2500-01-01T00:00:00Z","correlation":{"schema_version":1,"input_mode":"old"},"tokens":{"total_tokens":9007199254740993}}],"accounting":[{"timestamp":null,"correlation":{"output_mode":"new"},"headers":{}}]`)},
		{"nested-map-value-replacement-in-reused-detail", projectionModelJSON(`"details":[{"headers":{"keep":[],"x":["old","tail"]},"thinking":{"mode":"enabled"},"cost_usd":1}],"details":[{"headers":{"x":[null]},"thinking":null,"cost_usd":null}]`)},
		{"empty-versus-nil-collections", projectionModelJSON(`"details":[],"accounting":null,"providers":[]`)},
		{"header-after-usage-v1-not-converted", `{"usage":{"cached_tokens":15,"cache_write_tokens":5,"apis":{"API":{"models":{"M":{"cached_tokens":15,"cache_write_tokens":5,"details":[{},{}]}}}}},"generated_at":"2026-09-13T00:00:00Z","version":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { compareProjectedSnapshotForTest(t, s, tc.raw) })
	}
	var generation, records, states, progress int
	if err := s.reader.QueryRow(`SELECT generation,(SELECT count(*) FROM ledger_records),
 (SELECT count(*) FROM ledger_state),(SELECT count(*) FROM ledger_progress) FROM ledger_meta`).Scan(&generation, &records, &states, &progress); err != nil {
		t.Fatal(err)
	}
	if generation != 0 || records != 0 || states != 0 || progress != 0 {
		t.Fatal("source projection changed authoritative records, state or generation")
	}
}

func TestSQLiteProjectionPagesKeysAndRecordsAndKeepsRealDuplicates(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	var body strings.Builder
	body.WriteString(`"usage":{"apis":{`)
	for i := 0; i < sqliteProjectionPageNodes*2+3; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `"api-%d":{"models":{"M":{"details":[`, i)
		for j := 0; j < 3; j++ {
			if j > 0 {
				body.WriteByte(',')
			}
			body.WriteString(`{"model":"identical","tokens":{"total_tokens":9007199254740993}}`)
		}
		body.WriteString(`]}}}`)
	}
	body.WriteString(`,"many":{"models":{"M":{"details":[`)
	for i := 0; i < sqliteProjectionPageNodes*2+5; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"latency_ms":%d}`, i)
	}
	body.WriteString(`]}}}}}`)
	path := compareProjectedSnapshotForTest(t, s, projectionSnapshotJSON(body.String()))
	first, err := s.ProjectMigrationSource(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err := s.reader.QueryRow("SELECT count(*) FROM migration_projection_nodes").Scan(&before); err != nil {
		t.Fatal(err)
	}
	second, err := s.ProjectMigrationSource(context.Background(), path)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("completed projection changed on retry: %v", err)
	}
	if err := s.reader.QueryRow("SELECT count(*) FROM migration_projection_nodes").Scan(&after); err != nil || before != after {
		t.Fatal("idempotent projection appended nodes")
	}
}

func TestSQLiteProjectionJSONLDoesNotCountMetadataOrDeduplicateRows(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	raw := []byte("\r\n" + `{"api":"API","model":"M","metadata_only":true,"detail":{"stream":true}}` + "\n" +
		`{"api":"API","model":"M","detail":{"tokens":{"total_tokens":9007199254740993}}}` + "\n" +
		`{"api":"API","model":"M","detail":{"tokens":{"total_tokens":9007199254740993}}}` + "\n" +
		`{"api":"API","metadata_only":true}` + "\n{broken}\nnull\n \n" + `{"api":"last","archived":true}`)
	path := writeSQLiteMigrationSource(t, raw)
	if _, err := s.StageSource(context.Background(), path, "jsonl"); err != nil {
		t.Fatal(err)
	}
	p, err := s.ProjectMigrationSource(context.Background(), path)
	if err != nil || !p.Complete || p.Result.Requests != 3 || p.Result.Metadata != 2 || p.Result.Invalid != 2 {
		t.Fatalf("JSONL projection result=%+v err=%v", p, err)
	}
	var got []persistedDetail
	var rows, metadata, invalid int
	result, err := s.WalkMigrationProjection(context.Background(), path, func(item sqliteMigrationProjectedItem) error {
		rows++
		if item.Kind == "invalid" {
			invalid++
			return nil
		}
		if item.Scope == "metadata" {
			metadata++
		}
		got = append(got, item.Value.(persistedDetail))
		return nil
	})
	want, oldInvalid, readErr := readPersistedStorageFile(path)
	if err != nil || readErr != nil || !reflect.DeepEqual(result, p.Result) || rows != 7 || metadata != 2 || invalid != oldInvalid || !reflect.DeepEqual(got, want) {
		t.Fatalf("JSONL projection differs: rows=%d metadata=%d invalid=%d err=%v read=%v", rows, metadata, invalid, err, readErr)
	}
}

func TestSQLiteProjectionLargeValueUsesSpansNotRecordSizedBlobs(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	large := strings.Repeat("x", 5<<20) // Above both the Apply and CGO BLOB limits.
	raw := projectionModelJSON(`"details":[{"failure":"` + large + `"}],"details":[{"latency_ms":7}]`)
	path := compareProjectedSnapshotForTest(t, s, raw)
	var maxBlob int
	if err := s.reader.QueryRow("SELECT max(length(payload)) FROM migration_chunks WHERE source=?", path).Scan(&maxBlob); err != nil || maxBlob > sqliteMigrationChunkBytes {
		t.Fatalf("large value staging was not bounded: %d %v", maxBlob, err)
	}
	original, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, []byte(raw)) {
		t.Fatal("projection rewrote original source")
	}
}

func TestSQLiteProjectionResumesCommittedStackAfterFailureAndReopen(t *testing.T) {
	s, dbPath := testSQLiteLedger(t)
	raw := projectionModelJSON(`"details":[` + strings.Repeat(`{"model":"old"},`, 700) + `{}],"details":[{"latency_ms":8}],"details":[{},null,{}]`)
	path := writeSQLiteMigrationSource(t, []byte(raw))
	if _, err := s.StageSource(context.Background(), path, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`CREATE TRIGGER projection_fail BEFORE UPDATE OF events ON migration_projections
 WHEN NEW.events>256 BEGIN SELECT RAISE(ABORT,'projection injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	p, err := s.ProjectMigrationSource(context.Background(), path)
	if err == nil || p.Complete || p.Events != sqliteProjectionBatchEvents {
		t.Fatalf("failed projection exposed uncommitted progress: %+v %v", p, err)
	}
	_, err = s.WalkMigrationProjection(context.Background(), path, func(sqliteMigrationProjectedItem) error {
		t.Fatal("incomplete projection delivered data")
		return nil
	})
	if !errors.Is(err, errSQLiteProjectionIncomplete) {
		t.Fatalf("incomplete projection read: %v", err)
	}
	if _, err := s.writer.Exec("DROP TRIGGER projection_fail"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openSQLiteLedger(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err = s.ProjectMigrationSource(context.Background(), path)
	if err != nil || !p.Complete || p.Events <= sqliteProjectionBatchEvents {
		t.Fatalf("resume failed: %+v %v", p, err)
	}
	var want persistedStorageSnapshot
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if got := projectedSnapshotForTest(t, s, path); !reflect.DeepEqual(got, want) {
		t.Fatal("resume changed duplicate-array or hidden-tail semantics")
	}
	// Source event identity is unique even across the interrupted batch.
	var values, uniqueEvents int
	if err := s.reader.QueryRow(`SELECT count(*),count(DISTINCT v.event) FROM migration_projection_values v
 JOIN migration_projection_nodes n ON n.id=v.node WHERE n.source=?`, path).Scan(&values, &uniqueEvents); err != nil || values != uniqueEvents {
		t.Fatal("resume applied a source event more than once")
	}
}

func TestSQLiteProjectionSchemaUpgradePreservesStagedBytesAndAuthority(t *testing.T) {
	s, dbPath := testSQLiteLedger(t)
	ctx := context.Background()
	path := writeSQLiteMigrationSource(t, []byte(projectionModelJSON(`"total_requests":99,"details":[{}]`)))
	if _, err := s.StageSource(ctx, path, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyState(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, []sqliteLedgerStateMutation{{Name: "residual", Value: []byte("keep")}}, nil); err != nil {
		t.Fatal(err)
	}
	dropSQLiteProjectionSchemaForTest(t, s)
	if _, err := s.writer.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := openSQLiteLedger(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p, err := s.ProjectMigrationSource(ctx, path); err != nil || !p.Complete {
		t.Fatalf("old staged source could not be projected: %+v %v", p, err)
	}
	v, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	state, found, err := v.State("residual")
	if err != nil || !found || string(state.Value) != "keep" || v.Generation != 1 || len(sqliteReadAll(t, v, sqliteLedgerQuery{}, 1)) != 1 {
		t.Fatal("projection schema upgrade changed authority")
	}
	backupPath := filepath.Join(t.TempDir(), "backup")
	if _, err := s.Backup(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "projection-restored.sqlite")
	if err := restoreSQLiteLedgerBackup(ctx, backupPath, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := openSQLiteLedger(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if !reflect.DeepEqual(projectedSnapshotForTest(t, restored, path), projectedSnapshotForTest(t, s, path)) {
		t.Fatal("backup/restore lost projection or its source spans")
	}
}

func TestSQLiteProjectionSchema4UpgradeRebuildsUnverifiedCheckpoints(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			s, dbPath := testSQLiteLedger(t)
			ctx := context.Background()
			raw := projectionModelJSON(`"total_requests":99,"details":[{"model":"old"}],"details":[{"latency_ms":7}]`)
			path := compareProjectedSnapshotForTest(t, s, raw)
			before, err := s.ProjectMigrationSource(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, nil); err != nil {
				t.Fatal(err)
			}
			// This is exactly the earlier committed schema/format: no prefix
			// column, so neither a partial nor a complete old index is trusted.
			if _, err := s.writer.Exec(`ALTER TABLE migration_projections DROP COLUMN prefix; UPDATE migration_projections SET format=1;
 DROP INDEX migration_projection_array; CREATE INDEX migration_projection_array ON migration_projection_edges(parent,position) WHERE position>=0;
 PRAGMA user_version=4`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.writer.Exec("UPDATE migration_projections SET complete=?", complete); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = openSQLiteLedger(ctx, dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, err = s.WalkMigrationProjection(ctx, path, func(sqliteMigrationProjectedItem) error {
				t.Fatal("old projection used without revalidation")
				return nil
			})
			if !errors.Is(err, errSQLiteProjectionIncomplete) {
				t.Fatalf("old projection read: %v", err)
			}
			after, err := s.ProjectMigrationSource(ctx, path)
			if err != nil || !after.Complete || after.Root == before.Root {
				t.Fatalf("old projection did not get a checked new root: %+v %v", after, err)
			}
			var want persistedStorageSnapshot
			if err := json.Unmarshal([]byte(raw), &want); err != nil {
				t.Fatal(err)
			}
			if got := projectedSnapshotForTest(t, s, path); !reflect.DeepEqual(got, want) {
				t.Fatal("schema 4 reindex changed source semantics")
			}
			var generation, records int
			if err := s.reader.QueryRow("SELECT generation,(SELECT count(*) FROM ledger_records) FROM ledger_meta").Scan(&generation, &records); err != nil || generation != 1 || records != 1 {
				t.Fatal("schema 4 upgrade/reindex changed authority")
			}
		})
	}
}

func TestSQLiteProjectionPreservesInputsToLegacyResidualRestore(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			raw := fmt.Sprintf(`{"version":%d,"generated_at":"2026-09-13T00:00:00Z","usage":{
 "total_requests":10,"success_count":8,"failure_count":2,"total_tokens":100,"cached_tokens":15,"cache_write_tokens":5,
 "apis":{"API":{"total_requests":10,"success_count":8,"failure_count":2,"total_tokens":100,"models":{"M":{
 "total_requests":10,"success_count":8,"failure_count":2,"total_tokens":100,"cached_tokens":15,"cache_write_tokens":5,"avg_latency_ms":20,
 "providers":[{"provider":"claude","total_requests":10,"success_count":8,"failure_count":2,"total_tokens":100,"cached_tokens":15,"cache_write_tokens":5}],
 "details":[{"model":"M","timestamp":"2026-09-13T00:00:00Z","source":"probe","provider":"claude","tokens":{"input_tokens":3,"cached_tokens":2,"cache_write_tokens":1,"total_tokens":4}}],
 "details":[{"latency_ms":10}],"accounting":[{"model":"M","source":"probe","provider":"claude","timestamp":"2026-09-13T00:01:00Z","failed":true,"tokens":{"total_tokens":2}}]
 }}}},"requests_by_day":{"2026-09-13":10},"tokens_by_day":{"2026-09-13":100}}}`, version)
			path := compareProjectedSnapshotForTest(t, s, raw)
			var legacy persistedStorageSnapshot
			if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
				t.Fatal(err)
			}
			projected := projectedSnapshotForTest(t, s, path)
			if version < currentStorageSnapshotVersion {
				migrateLegacySnapshotCacheReads(&legacy.Usage)
				migrateLegacySnapshotCacheReads(&projected.Usage)
			}
			now := time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC)
			left, right := NewRequestStatistics(), NewRequestStatistics()
			left.retention, right.retention = 0, 0
			left.restoreStorageSnapshotLocked(legacy.Usage, now)
			right.restoreStorageSnapshotLocked(projected.Usage, now)
			want, got := left.Snapshot(), right.Snapshot()
			if !reflect.DeepEqual(got, want) || got.TotalRequests != 10 {
				t.Fatal("typed projection changed legacy restored residuals, dimensions or v1 cache conversion")
			}
		})
	}
}
