package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func testSQLiteLedger(t *testing.T) (*sqliteLedger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage ?#& 测试.sqlite")
	s, err := openSQLiteLedger(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func sqliteTestRecord(i int) sqliteLedgerRecord {
	return sqliteLedgerRecord{API: fmt.Sprintf("api-%d", i%2), Model: "group-model", Detail: RequestDetail{
		Model: "actual-model", Provider: "claude", RequestedModel: "requested", Source: "source", AuthID: "id", AuthIndex: "index", AuthType: "api-key",
		APIKey: "masked", APIKeyHash: "stable-hash", BaseURL: "https://test.invalid", Endpoint: "/v1/messages", ExecutorType: "executor",
		Timestamp: time.Date(2500, 1, 2, 3, 4, 5, i, time.FixedZone("original-zone", 5*3600+45*60+17)), TimestampSynthetic: true,
		Tokens:    TokenStats{InputTokens: math.MaxInt64, OutputTokens: 22, ReasoningTokens: 3, CacheReadTokens: 4, CachedTokens: 5, CacheTokens: 6, CacheWriteTokens: 7, TotalTokens: math.MaxInt64},
		LatencyMs: 100, TTFTMs: 20, Failed: true, StatusCode: 503, Failure: "test failure", Stream: true,
		Thinking:    UsageThinking{Intensity: "high", Mode: "enabled", Level: "test", Budget: 150},
		Correlation: &ProtocolCorrelationMeta{SchemaVersion: 1, KnownFields: 7, InputMode: "input", OutputMode: "output", CacheMode: "cache"},
		Headers:     map[string][]string{"x-test": {"one", "two"}},
	}}
}

func sqliteReadAll(t *testing.T, v *sqliteLedgerView, query sqliteLedgerQuery, size int) []sqliteLedgerRecord {
	t.Helper()
	var result []sqliteLedgerRecord
	var after *sqliteLedgerRecord
	for {
		page, err := v.Page(query, after, size)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return result
		}
		result = append(result, page...)
		after = &result[len(result)-1]
		if len(result) > 10000 {
			t.Fatal("keyset pagination did not advance")
		}
	}
}

func TestSQLiteLedgerRoundTripDuplicatesAndReopen(t *testing.T) {
	s, path := testSQLiteLedger(t)
	record := sqliteTestRecord(1)
	ids, err := s.Apply(context.Background(), []sqliteLedgerMutation{{Record: record}, {Record: record}}, nil)
	if err != nil || len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("real duplicate requests collapsed: ids=%v err=%v", ids, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openSQLiteLedger(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.ReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	records := sqliteReadAll(t, v, sqliteLedgerQuery{}, 1)
	if len(records) != 2 || v.Generation != 1 {
		t.Fatalf("reopened records=%d generation=%d", len(records), v.Generation)
	}
	for i, got := range records {
		want := record
		want.ID, want.Revision = ids[i], 1
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("record round trip changed fields: got=%+v want=%+v", got, want)
		}
	}
	var identities int
	if err := v.tx.QueryRow("SELECT count(*) FROM ledger_identities").Scan(&identities); err != nil || identities != 1 {
		t.Fatalf("identity dictionary: count=%d err=%v", identities, err)
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		t.Fatalf("new database is not private: info=%v err=%v", info, err)
	}
}

func TestSQLiteLedgerWindowsFileURI(t *testing.T) {
	path := "C:/usage ?#& 测试.sqlite"
	parsed, err := url.Parse(sqliteLedgerDSN(path, false))
	if err != nil || parsed.Host != "" || parsed.Path != "/"+path || parsed.Query().Get("mode") != "rw" {
		t.Fatalf("Windows filename became a URI authority or query: uri=%q parsed=%+v err=%v", sqliteLedgerDSN(path, false), parsed, err)
	}
}

func TestSQLiteLedgerFrozenViewAndRevisionConflicts(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(i)}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	before := sqliteReadAll(t, view, sqliteLedgerQuery{}, 2)
	modified := before[0]
	modified.Archived = true
	modified.Detail.Source = "new identity"
	modified.Detail.Headers = map[string][]string{"x-test": {"mutated"}}
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: modified}, {Record: before[1], Delete: true}, {Record: sqliteTestRecord(9)}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := sqliteReadAll(t, view, sqliteLedgerQuery{}, 1); !reflect.DeepEqual(got, before) {
		t.Fatal("snapshot changed after update, archive, delete and late insertion")
	}
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: modified}}, nil); !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	current, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if got := sqliteReadAll(t, current, sqliteLedgerQuery{}, 10); len(got) != 2 || current.Generation != 4 {
		t.Fatalf("visible records=%d generation=%d", len(got), current.Generation)
	}
	all := sqliteReadAll(t, current, sqliteLedgerQuery{IncludeAccounting: true}, 10)
	if len(all) != 3 {
		t.Fatalf("accounting excluded: %+v", all)
	}
	for _, r := range all {
		if r.ID == modified.ID && (!r.Archived || r.Detail.Headers != nil || r.Detail.Stream || r.Detail.Thinking != (UsageThinking{}) || r.Detail.Correlation == nil || r.Detail.Source != "new identity") {
			t.Fatalf("archive field contract changed: %+v", r)
		}
	}
}

func TestSQLiteLedgerAtomicMigrationAndLimits(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	progress := &sqliteLedgerProgress{Source: "shard", Fingerprint: "sha256-test", ExpectedOffset: 0, NextOffset: 2}
	record := sqliteTestRecord(1)
	invalid := record
	invalid.ID, invalid.Revision = 999, 1
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: record}, {Record: invalid, Delete: true}}, progress); !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("invalid batch error=%v", err)
	}
	for _, table := range []string{"ledger_records", "ledger_identities", "ledger_progress"} {
		var n int
		if err := s.writer.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial batch in %s: count=%d err=%v", table, n, err)
		}
	}
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: record}, {Record: record}}, progress); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: record}}, progress); !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("stale migration cursor reapplied records: %v", err)
	}
	var position, generation int64
	if err := s.writer.QueryRow("SELECT position FROM ledger_progress WHERE source='shard'").Scan(&position); err != nil || position != 2 {
		t.Fatalf("progress=%d err=%v", position, err)
	}
	if err := s.writer.QueryRow("SELECT generation FROM ledger_meta").Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("failed transaction changed generation=%d err=%v", generation, err)
	}
	for _, n := range []int{1, 20, sqliteLedgerBatchRecords + 1} {
		batch := make([]sqliteLedgerMutation, n)
		for i := range batch {
			batch[i].Record = record
			batch[i].Record.Detail.Failure = strings.Repeat("x", 500000)
		}
		if n == 1 {
			batch[0].Record.Detail.Failure = strings.Repeat("x", sqliteLedgerRecordBytes)
		}
		if _, err := s.Apply(ctx, batch, nil); !errors.Is(err, errSQLiteLedgerBudget) {
			t.Fatalf("oversized batch n=%d: %v", n, err)
		}
	}
	var count int
	if err := s.writer.QueryRow("SELECT count(*) FROM ledger_records").Scan(&count); err != nil || count != 2 {
		t.Fatalf("budget failure committed partial records=%d err=%v", count, err)
	}
}

func TestSQLiteLedgerKeysetOrderAndQueryPlan(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	var batch []sqliteLedgerMutation
	for i := 0; i < 200; i++ {
		r := sqliteTestRecord(i)
		r.Model = fmt.Sprintf("model-%d", i%3)
		// Include true ties, out-of-order timestamps and a genuine zero time.
		r.Detail.Timestamp = time.Date(2026, 9, 10, 0, 0, i%5, i%2, time.UTC)
		if i == 0 {
			r.Detail.Timestamp = time.Time{}
		}
		batch = append(batch, sqliteLedgerMutation{Record: r})
	}
	ids, err := s.Apply(ctx, batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	var expected []sqliteLedgerRecord
	for i, b := range batch {
		b.Record.ID, b.Record.Revision = ids[i], 1
		expected = append(expected, b.Record)
	}
	sort.Slice(expected, func(i, j int) bool {
		a, b := expected[i], expected[j]
		if !a.Detail.Timestamp.Equal(b.Detail.Timestamp) {
			return a.Detail.Timestamp.After(b.Detail.Timestamp)
		}
		if a.API != b.API {
			return a.API < b.API
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.ID < b.ID
	})
	v, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	for _, query := range []sqliteLedgerQuery{{}, {API: "api-0"}, {Model: "model-1"}, {Cutoff: batch[2].Record.Detail.Timestamp}} {
		got := sqliteReadAll(t, v, query, 7)
		var want []int64
		for _, r := range expected {
			if (query.API != "" && r.API != query.API) || (query.Model != "" && r.Model != query.Model) || (!query.Cutoff.IsZero() && (r.Detail.Timestamp.IsZero() || r.Detail.Timestamp.Before(query.Cutoff))) {
				continue
			}
			want = append(want, r.ID)
		}
		var actual []int64
		for _, r := range got {
			actual = append(actual, r.ID)
		}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("keyset lost/reordered records for %+v: got=%v want=%v", query, actual, want)
		}
	}
	statement, args, err := sqliteLedgerPageQuery(sqliteLedgerQuery{}, &expected[99], 10)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := v.tx.Query("EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plans []string
	for rows.Next() {
		var id, parent, unused int
		var plan string
		if err := rows.Scan(&id, &parent, &unused, &plan); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, plan)
	}
	plan := strings.Join(plans, "\n")
	if !strings.Contains(plan, "SEARCH r USING INDEX ledger_records_order") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("deep page does not seek through the ordering index:\n%s", plan)
	}
}

func TestSQLiteLedgerReaderBudgetCancellationAndClose(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	first, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := s.ReadView(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third reader did not respect connection budget/cancellation: %v", err)
	}
	if _, err := first.tx.Exec("DELETE FROM ledger_records"); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
	for pragma, want := range map[string]int{"cache_size": -1024, "mmap_size": 0, "temp_store": 1, "query_only": 1} {
		var got int
		if err := first.tx.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("reader %s=%d want=%d err=%v", pragma, got, want, err)
		}
	}
	if got := s.reader.Stats().OpenConnections; got != 2 {
		t.Fatalf("reader connections=%d", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, v := range []*sqliteLedgerView{first, second} {
		if _, err := v.Page(sqliteLedgerQuery{}, nil, 1); err == nil {
			t.Fatal("view usable after store close")
		}
		v.Close()
	}
}

func TestSQLiteLedgerRejectsForeignAndFutureSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "foreign.sqlite")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	db, err := sql.Open(sqliteLedgerDriverName, sqliteLedgerDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE private_data(value TEXT); INSERT INTO private_data VALUES('keep'); PRAGMA user_version=9"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := openSQLiteLedger(ctx, path); err == nil {
		s.Close()
		t.Fatal("foreign schema was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected database was mutated: %v", err)
	}
	s, ownPath := testSQLiteLedger(t)
	if _, err := s.writer.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if opened, err := openSQLiteLedger(ctx, ownPath); err == nil {
		opened.Close()
		t.Fatal("future schema was silently downgraded")
	}
}

func TestSQLiteLedgerDeletesReleaseIdentitiesWithoutReusingRecordIDs(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	ids, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(0)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteLedgerRecord{ID: ids[0], Revision: 1}, Delete: true}}, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.writer.QueryRow("SELECT count(*) FROM ledger_identities").Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan identities retained=%d err=%v", count, err)
	}
	newIDs, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(0)}}, nil)
	if err != nil || newIDs[0] <= ids[0] {
		t.Fatalf("record ID reused: old=%v new=%v err=%v", ids, newIDs, err)
	}
}

func TestSQLiteLedgerByteBoundedPages(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	record := sqliteTestRecord(1)
	record.Detail.Failure = strings.Repeat("x", 780000)
	for i := 0; i < 15; i++ {
		if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: record}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	v, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	first, err := v.Page(sqliteLedgerQuery{}, nil, sqliteLedgerPageRecords)
	if err != nil || len(first) != 10 {
		t.Fatalf("byte-limited first page length=%d err=%v", len(first), err)
	}
	second, err := v.Page(sqliteLedgerQuery{}, &first[len(first)-1], sqliteLedgerPageRecords)
	if err != nil || len(second) != 5 || second[0].ID != first[len(first)-1].ID+1 {
		t.Fatalf("byte boundary skipped a record: second page length=%d err=%v", len(second), err)
	}
}

func TestSQLiteLedgerEngineInfo(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	var version, sourceID string
	if err := s.writer.QueryRow("SELECT sqlite_version(), sqlite_source_id()").Scan(&version, &sourceID); err != nil {
		t.Fatal(err)
	}
	t.Logf("driver=%s sqlite=%s source_id=%s", sqliteLedgerDriverName, version, sourceID)
}

func BenchmarkSQLiteLedgerBatchInsert(b *testing.B) {
	s, err := openSQLiteLedger(context.Background(), filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	batch := make([]sqliteLedgerMutation, 128)
	for i := range batch {
		batch[i].Record = sqliteTestRecord(i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Apply(context.Background(), batch, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSQLiteLedgerCrashRecovery(t *testing.T) {
	if path := os.Getenv("CPA_TEST_SQLITE_CRASH_PATH"); path != "" {
		s, err := openSQLiteLedger(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		batch := []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}, {Record: sqliteTestRecord(1)}}
		if _, err := s.Apply(context.Background(), batch, &sqliteLedgerProgress{Source: "source", Fingerprint: "fixed", NextOffset: 2}); err != nil {
			t.Fatal(err)
		}
		tx, err := s.writer.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("DELETE FROM ledger_records; UPDATE ledger_meta SET generation=999; UPDATE ledger_progress SET position=999"); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // Simulate process death: no Commit, Rollback, Close or defers.
	}
	path := filepath.Join(t.TempDir(), "crash.sqlite")
	command := exec.Command(os.Args[0], "-test.run=^TestSQLiteLedgerCrashRecovery$")
	command.Env = append(os.Environ(), "CPA_TEST_SQLITE_CRASH_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture failed: %v\n%s", err, output)
	}
	s, err := openSQLiteLedger(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.ReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if records := sqliteReadAll(t, v, sqliteLedgerQuery{}, 1); len(records) != 2 || v.Generation != 1 {
		t.Fatalf("recovery lost committed data or kept uncommitted changes: rows=%d generation=%d", len(records), v.Generation)
	}
	fingerprint, offset, found, err := v.Progress("source")
	if err != nil || !found || offset != 2 || fingerprint != "fixed" {
		t.Fatalf("recovered progress %q/%d found=%t err=%v", fingerprint, offset, found, err)
	}
}

func TestSQLiteLedgerCheckpointReportsPinnedWAL(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	v, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(0)}}, nil); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.Checkpoint(ctx)
	if err != nil || !checkpoint.Busy || checkpoint.LogFrames <= checkpoint.CheckpointedFrames {
		t.Fatalf("pinned WAL falsely reported as complete: %+v err=%v", checkpoint, err)
	}
	v.Close()
	checkpoint, err = s.Checkpoint(ctx)
	if err != nil || checkpoint.Busy || checkpoint.LogFrames != 0 {
		t.Fatalf("WAL did not truncate after view closed: %+v err=%v", checkpoint, err)
	}
}

func TestSQLiteLedgerWriterLockCancellation(t *testing.T) {
	s, path := testSQLiteLedger(t)
	other, err := sql.Open(sqliteLedgerDriverName, sqliteLedgerDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, nil); err == nil {
		t.Fatal("locked write succeeded")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("busy writer did not respect bounded wait: %v", elapsed)
	}
	tx.Rollback()
	if _, err := s.Apply(context.Background(), []sqliteLedgerMutation{{Record: sqliteTestRecord(2)}}, nil); err != nil {
		t.Fatalf("writer unusable after cancellation: %v", err)
	}
	var count int
	if err := s.writer.QueryRow("SELECT count(*) FROM ledger_records").Scan(&count); err != nil || count != 1 {
		t.Fatalf("cancelled write left a partial record: count=%d err=%v", count, err)
	}
}

func BenchmarkSQLiteLedgerPage100k(b *testing.B) {
	s, err := openSQLiteLedger(context.Background(), filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Apply(context.Background(), []sqliteLedgerMutation{{Record: sqliteTestRecord(0)}}, nil); err != nil {
		b.Fatal(err)
	}
	// This fixture isolates query cost from ingestion. Bulk SQL constructs the
	// other 99,999 records; production ingestion is benchmarked separately.
	if _, err := s.writer.Exec(`WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers WHERE n<99999)
 INSERT INTO ledger_records(revision,identity_id,api,model_group,seconds,nanos,zone,utc_offset,zero_time,archived,payload)
 SELECT 1,r.identity_id,r.api,r.model_group,r.seconds-n,r.nanos,r.zone,r.utc_offset,r.zero_time,r.archived,r.payload
 FROM numbers CROSS JOIN ledger_records r WHERE r.id=1`); err != nil {
		b.Fatal(err)
	}
	for _, position := range []int{0, 50000, 99000} {
		b.Run(fmt.Sprintf("after_%d", position), func(b *testing.B) {
			v, err := s.ReadView(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			defer v.Close()
			var cursor *sqliteLedgerRecord
			if position > 0 {
				r := sqliteTestRecord(0)
				r.ID = int64(position)
				r.Detail.Timestamp = r.Detail.Timestamp.Add(-time.Duration(position-1) * time.Second)
				cursor = &r
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, err := v.Page(sqliteLedgerQuery{}, cursor, 100)
				if err != nil || len(page) != 100 {
					b.Fatalf("page length=%d err=%v", len(page), err)
				}
			}
		})
	}
}
