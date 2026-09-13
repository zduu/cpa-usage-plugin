package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSQLiteLedgerLargeRecordRoundTrip(t *testing.T) {
	for _, kind := range []string{"payload", "identity", "both", "escaped"} {
		t.Run(kind, func(t *testing.T) {
			s, path := testSQLiteLedger(t)
			record := sqliteTestRecord(1)
			if kind != "identity" {
				record.Detail.Failure = strings.Repeat("x", 5<<20)
			}
			if kind == "identity" || kind == "both" {
				record.Detail.Source = strings.Repeat("source", 1<<20)
			}
			if kind == "escaped" {
				record.Detail.Failure = strings.Repeat("\x00é<>\u2028", 100<<10)
			}
			ctx := context.Background()
			ids, err := s.ApplyLargeState(ctx, []sqliteLedgerMutation{{Record: record}}, []sqliteLedgerStateMutation{{Name: "migration", Value: []byte("large-record-committed")}}, &sqliteLedgerProgress{Source: "synthetic", Fingerprint: "large", NextOffset: 1})
			if err != nil || len(ids) != 1 {
				t.Fatalf("legal large legacy record was rejected: ids=%v err=%v", ids, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = openSQLiteLedger(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			view, err := s.ReadView(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			got := sqliteReadAll(t, view, sqliteLedgerQuery{}, 1)
			record.ID, record.Revision = ids[0], 1
			if len(got) != 1 || !reflect.DeepEqual(got[0], record) {
				t.Fatal("large record round trip changed data")
			}
			state, found, err := view.State("migration")
			if err != nil || !found || state.Revision != 1 || string(state.Value) != "large-record-committed" {
				t.Fatal("large record did not commit with its state")
			}
			_, position, found, err := view.Progress("synthetic")
			if err != nil || !found || position != 1 || view.Generation != 1 {
				t.Fatal("large record did not commit with its cursor/generation")
			}
		})
	}
}

func sqliteLargeExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func sqliteLargeCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func sqliteLargeView(t *testing.T, s *sqliteLedger) *sqliteLedgerView {
	t.Helper()
	v, err := s.ReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func sqliteLargeApply(t *testing.T, s *sqliteLedger, mutations ...sqliteLedgerMutation) []int64 {
	t.Helper()
	ids, err := s.ApplyLargeState(context.Background(), mutations, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestSQLiteLedgerDriverGuardPragmas(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	for _, connection := range []struct {
		name string
		db   *sql.DB
		read int
	}{{"writer", s.writer, 0}, {"reader", s.reader, 1}} {
		t.Run(connection.name, func(t *testing.T) {
			for pragma, want := range map[string]int{
				"foreign_keys": 1, "busy_timeout": 250, "synchronous": 2,
				"query_only": connection.read, "mmap_size": 0, "temp_store": 1,
				"trusted_schema": 0, "wal_autocheckpoint": 256,
			} {
				var got int
				if err := connection.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
					t.Errorf("%s = %d, want %d: %v", pragma, got, want, err)
				}
			}
		})
	}
}

func TestSQLiteLedgerLargeReuseTransitionsAndFrozenView(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	original := sqliteTestRecord(1)
	original.Detail.Source = strings.Repeat("identity", 40<<10)
	original.Detail.Failure = strings.Repeat("failure", 100<<10)
	ids := sqliteLargeApply(t, s, sqliteLedgerMutation{Record: original}, sqliteLedgerMutation{Record: original})
	if len(ids) != 2 || ids[0] == ids[1] || sqliteLargeCount(t, s.writer, "ledger_identities") != 1 || sqliteLargeCount(t, s.writer, "ledger_values") != 2 {
		t.Fatal("immutable value sharing merged real requests or duplicated stored bytes")
	}
	old := sqliteLargeView(t, s)
	before := sqliteReadAll(t, old, sqliteLedgerQuery{}, 2)
	if len(before) != 2 {
		t.Fatal("missing independent real requests")
	}
	archived := before[0]
	archived.Archived = true
	archived.Detail.Source, archived.Detail.Failure = "inline identity", "inline failure"
	archived.Detail.Headers = map[string][]string{"discarded": {strings.Repeat("header", 300<<10)}}
	sqliteLargeApply(t, s, sqliteLedgerMutation{Record: archived})
	if sqliteLargeCount(t, s.writer, "ledger_values") != 2 {
		t.Fatal("update reclaimed values still referenced by another request")
	}
	current := sqliteLargeView(t, s)
	all := sqliteReadAll(t, current, sqliteLedgerQuery{IncludeAccounting: true}, 2)
	if len(all) != 2 || all[0].ID != archived.ID || all[0].Revision != 2 || !all[0].Archived || all[0].Detail.Headers != nil || all[0].Detail.Stream || all[0].Detail.Thinking != (UsageThinking{}) || all[0].Detail.Correlation == nil || all[0].Detail.Source != archived.Detail.Source || all[0].Detail.Failure != archived.Detail.Failure {
		t.Fatal("large-to-inline archive transition violated field retention")
	}
	current.Close()
	sqliteLargeApply(t, s, sqliteLedgerMutation{Record: before[1], Delete: true})
	if sqliteLargeCount(t, s.writer, "ledger_values") != 0 || sqliteLargeCount(t, s.writer, "ledger_value_chunks") != 0 || sqliteLargeCount(t, s.writer, "ledger_identities") != 1 {
		t.Fatal("last reference deletion leaked large values/chunks or removed a live identity")
	}
	backToLarge := original
	backToLarge.ID, backToLarge.Revision = archived.ID, 2
	sqliteLargeApply(t, s, sqliteLedgerMutation{Record: backToLarge})
	if sqliteLargeCount(t, s.writer, "ledger_values") != 2 || sqliteLargeCount(t, s.writer, "ledger_value_chunks") == 0 {
		t.Fatal("inline-to-large transition did not retain complete chunk streams")
	}
	current = sqliteLargeView(t, s)
	all = sqliteReadAll(t, current, sqliteLedgerQuery{}, 2)
	backToLarge.Revision = 4
	if len(all) != 1 || !reflect.DeepEqual(all[0], backToLarge) {
		t.Fatal("inline-to-large transition changed data or record identity")
	}
	current.Close()
	sqliteLargeApply(t, s, sqliteLedgerMutation{Record: backToLarge, Delete: true})
	for _, table := range []string{"ledger_records", "ledger_identities", "ledger_values", "ledger_value_chunks"} {
		if sqliteLargeCount(t, s.writer, table) != 0 {
			t.Fatalf("delete leaked %s", table)
		}
	}
	if got := sqliteReadAll(t, old, sqliteLedgerQuery{}, 1); !reflect.DeepEqual(got, before) || old.Generation != 1 {
		t.Fatal("a pinned view lost large values after update/delete/reclamation")
	}
	if cp, err := s.Checkpoint(context.Background()); err != nil || !cp.Busy {
		t.Fatalf("large pinned view did not report retained WAL: %+v %v", cp, err)
	}
	old.Close()
	if cp, err := s.Checkpoint(context.Background()); err != nil || cp.Busy {
		t.Fatalf("closing large view did not release WAL: %+v %v", cp, err)
	}
	if next := sqliteLargeApply(t, s, sqliteLedgerMutation{Record: original}); len(next) != 1 || next[0] <= ids[1] {
		t.Fatal("deleting large records allowed real request IDs to be reused")
	}
}

func TestSQLiteLedgerLargeAtomicRollback(t *testing.T) {
	for _, mode := range []string{"middle-chunk", "record-write", "commit", "revision", "state", "cursor", "batch-budget"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testSQLiteLedger(t)
			ctx := context.Background()
			seed := sqliteTestRecord(1)
			if _, err := s.ApplyState(ctx, []sqliteLedgerMutation{{Record: seed}}, []sqliteLedgerStateMutation{{Name: "aggregate", Value: []byte("before")}}, &sqliteLedgerProgress{Source: "source", Fingerprint: "synthetic", NextOffset: 1}); err != nil {
				t.Fatal(err)
			}
			record := sqliteTestRecord(2)
			record.Detail.Source = strings.Repeat("identity", 40<<10)
			record.Detail.Failure = strings.Repeat("x", 2<<20)
			mutations := []sqliteLedgerMutation{{Record: record}}
			states := []sqliteLedgerStateMutation{{Name: "aggregate", Revision: 1, Value: []byte("after")}}
			progress := &sqliteLedgerProgress{Source: "source", Fingerprint: "synthetic", ExpectedOffset: 1, NextOffset: 2}
			var expected error
			switch mode {
			case "middle-chunk":
				sqliteLargeExec(t, s.writer, `CREATE TRIGGER fail_large_chunk BEFORE INSERT ON ledger_value_chunks
 WHEN NEW.position>=262144 BEGIN SELECT RAISE(ABORT,'synthetic chunk failure'); END`)
			case "record-write":
				sqliteLargeExec(t, s.writer, `CREATE TRIGGER fail_large_record BEFORE INSERT ON ledger_records
 BEGIN SELECT RAISE(ABORT,'synthetic record failure'); END`)
			case "commit":
				sqliteLargeExec(t, s.writer, `CREATE TABLE missing_parent(id INTEGER PRIMARY KEY);
 CREATE TABLE late_failure(ref INTEGER REFERENCES missing_parent(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER fail_large_commit AFTER INSERT ON ledger_records BEGIN INSERT INTO late_failure VALUES(123); END`)
			case "revision":
				mutations = append(mutations, sqliteLedgerMutation{Record: sqliteLedgerRecord{ID: 999, Revision: 1}, Delete: true})
				expected = errSQLiteLedgerConflict
			case "state":
				states[0].Revision = 999
				expected = errSQLiteLedgerConflict
			case "cursor":
				progress.ExpectedOffset = 0
				expected = errSQLiteLedgerConflict
			case "batch-budget":
				for len(mutations) < 4 {
					mutations = append(mutations, sqliteLedgerMutation{Record: record})
				}
				expected = errSQLiteLedgerBudget
			}
			if ids, err := s.ApplyLargeState(ctx, mutations, states, progress); err == nil || len(ids) != 0 || (expected != nil && !errors.Is(err, expected)) {
				t.Fatalf("failure did not abort batch: ids=%v error=%v", ids, err)
			}
			for _, table := range []string{"ledger_values", "ledger_value_chunks"} {
				if sqliteLargeCount(t, s.writer, table) != 0 {
					t.Fatalf("failed transaction left %s", table)
				}
			}
			if sqliteLargeCount(t, s.writer, "ledger_records") != 1 || sqliteLargeCount(t, s.writer, "ledger_identities") != 1 {
				t.Fatal("failed transaction changed authority")
			}
			view := sqliteLargeView(t, s)
			state, found, err := view.State("aggregate")
			if err != nil || !found || state.Revision != 1 || string(state.Value) != "before" || view.Generation != 1 {
				t.Fatal("failed transaction changed state/generation")
			}
			fingerprint, position, found, err := view.Progress("source")
			if err != nil || !found || fingerprint != "synthetic" || position != 1 {
				t.Fatal("failed transaction advanced migration progress")
			}
			if rows := sqliteReadAll(t, view, sqliteLedgerQuery{}, 1); len(rows) != 1 || !reflect.DeepEqual(rows[0].Detail, seed.Detail) {
				t.Fatal("failed transaction altered existing request data")
			}
			view.Close()
			// A failed COMMIT must also leave the pooled writer usable.
			if _, err := s.ApplyState(ctx, nil, []sqliteLedgerStateMutation{{Name: "after-failure", Value: []byte("ok")}}, nil); err != nil {
				t.Fatalf("failed transaction poisoned the writer: %v", err)
			}
		})
	}
}

func TestSQLiteLedgerLargePagePreflightsBeforeDecoding(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	var ids []int64
	for i := 0; i < 3; i++ {
		record := sqliteTestRecord(1) // Exact ordering ties are resolved by real ID.
		record.Detail.Failure = strings.Repeat(fmt.Sprint(i), 5<<20)
		if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: record}}, nil); !errors.Is(err, errSQLiteLedgerBudget) {
			t.Fatalf("bounded Apply lost its original record limit: %v", err)
		}
		ids = append(ids, sqliteLargeApply(t, s, sqliteLedgerMutation{Record: record})...)
	}
	v := sqliteLargeView(t, s)
	var after *sqliteLedgerRecord
	for _, id := range ids {
		page, err := v.Page(sqliteLedgerQuery{}, after, sqliteLedgerPageRecords)
		if err != nil || len(page) != 1 || page[0].ID != id || len(page[0].Detail.Failure) != 5<<20 {
			t.Fatalf("large page did not respect byte budget/order: count=%d err=%v", len(page), err)
		}
		after = &page[0]
	}
	if page, err := v.Page(sqliteLedgerQuery{}, after, sqliteLedgerPageRecords); err != nil || len(page) != 0 {
		t.Fatalf("large keyset did not reach EOF: count=%d err=%v", len(page), err)
	}
	v.Close()
	sqliteLargeExec(t, s.writer, "DELETE FROM ledger_value_chunks WHERE value_id=(SELECT payload_value FROM ledger_records WHERE id=?) AND position=0", ids[2])
	v = sqliteLargeView(t, s)
	after = nil
	for _, id := range ids[:2] {
		page, err := v.Page(sqliteLedgerQuery{}, after, sqliteLedgerPageRecords)
		if err != nil || len(page) != 1 || page[0].ID != id {
			t.Fatalf("decoded a next-page corrupt value before checking page capacity: count=%d err=%v", len(page), err)
		}
		after = &page[0]
	}
	if page, err := v.Page(sqliteLedgerQuery{}, after, sqliteLedgerPageRecords); err == nil || len(page) != 0 {
		t.Fatal("missing chunk was silently accepted")
	}
}

func TestSQLiteLedgerLargeAndInlinePagesKeepOrder(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	var expected []sqliteLedgerRecord
	for i := 0; i < 5; i++ {
		record := sqliteTestRecord(1)
		record.Detail.Failure = fmt.Sprintf("record-%d", i)
		if i%2 == 1 {
			record.Detail.Failure += strings.Repeat("x", 5<<20)
		}
		ids := sqliteLargeApply(t, s, sqliteLedgerMutation{Record: record})
		record.ID, record.Revision = ids[0], int64(i+1)
		expected = append(expected, record)
	}
	view := sqliteLargeView(t, s)
	first, err := view.Page(sqliteLedgerQuery{}, nil, 512)
	if err != nil || len(first) != 3 || !reflect.DeepEqual(first, expected[:3]) {
		t.Fatalf("mixed first page lost inline/large ordering: count=%d error=%v", len(first), err)
	}
	second, err := view.Page(sqliteLedgerQuery{}, &first[len(first)-1], 512)
	if err != nil || len(second) != 2 || !reflect.DeepEqual(second, expected[3:]) {
		t.Fatalf("mixed continuation page changed records: count=%d error=%v", len(second), err)
	}
}

func TestSQLiteLedgerLargeCancellationAndShutdown(t *testing.T) {
	s, path := testSQLiteLedger(t)
	ctx := context.Background()
	record := sqliteTestRecord(1)
	record.Detail.Failure = strings.Repeat("x", 2<<20)
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	_, err = s.ApplyLargeState(waiting, []sqliteLedgerMutation{{Record: record}}, nil, nil)
	cancel()
	tx.Rollback()
	if !errors.Is(err, context.DeadlineExceeded) || sqliteLargeCount(t, s.writer, "ledger_values") != 0 {
		t.Fatalf("canceled large writer changed data or failed to stop: %v", err)
	}
	sqliteLargeApply(t, s, sqliteLedgerMutation{Record: record})
	view := sqliteLargeView(t, s)
	var id, size int64
	if err := view.tx.QueryRow("SELECT v.id,v.size FROM ledger_records r JOIN ledger_values v ON v.id=r.payload_value").Scan(&id, &size); err != nil {
		t.Fatal(err)
	}
	stream, err := openSQLiteLedgerValueReader(view.ctx, view.tx, id, "request", size)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if n, err := stream.Read(make([]byte, 17)); n != 17 || err != nil {
		t.Fatalf("cannot begin large stream: n=%d err=%v", n, err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not release a partially consumed chunk stream")
	}
	if _, err := stream.Read(make([]byte, 17)); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed ledger kept serving a large stream: %v", err)
	}
	reopened, err := openSQLiteLedger(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current := sqliteLargeView(t, reopened)
	rows := sqliteReadAll(t, current, sqliteLedgerQuery{}, 1)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Detail, record.Detail) || current.Generation != 1 {
		t.Fatal("cancellation/close changed the committed large request")
	}
}

// An independent connection represents an externally edited/corrupt database.
// It intentionally lacks the production driver's row/check guards.
func sqliteLargeExternalDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	driver := sqliteLedgerDriverName
	if driver == "cpa-usage-ledger" {
		driver = "sqlite3"
	}
	db, err := sql.Open(driver, sqliteLedgerFileURI(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	sqliteLargeExec(t, db, "PRAGMA foreign_keys=OFF")
	sqliteLargeExec(t, db, "PRAGMA ignore_check_constraints=ON")
	return db
}

func TestSQLiteLedgerLargeCorruptionIsNotMissingHistory(t *testing.T) {
	for _, mode := range []string{
		"missing-identity", "missing-value", "missing-chunk", "chunk-position", "short-chunk",
		"checksum", "kind", "marker", "null-reference", "huge-size", "overflow-size",
		"oversized-blob", "oversized-text-nul", "checksum-text-nul", "inline-text-nul",
		"api-text-nul", "model-text-nul", "zone-text-nul",
	} {
		t.Run(mode, func(t *testing.T) {
			s, path := testSQLiteLedger(t)
			record := sqliteTestRecord(1)
			record.Detail.Source = strings.Repeat("identity", 40<<10)
			record.Detail.Failure = strings.Repeat("failure", 100<<10)
			sqliteLargeApply(t, s, sqliteLedgerMutation{Record: record})
			external := sqliteLargeExternalDB(t, path)
			var valueID, identityID int64
			if err := external.QueryRow("SELECT payload_value,identity_id FROM ledger_records").Scan(&valueID, &identityID); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-identity":
				sqliteLargeExec(t, external, "DELETE FROM ledger_identities WHERE id=?", identityID)
			case "missing-value":
				sqliteLargeExec(t, external, "DELETE FROM ledger_values WHERE id=?", valueID)
			case "missing-chunk":
				sqliteLargeExec(t, external, "DELETE FROM ledger_value_chunks WHERE value_id=? AND position=0", valueID)
			case "chunk-position":
				sqliteLargeExec(t, external, "UPDATE ledger_value_chunks SET position=1 WHERE value_id=? AND position=0", valueID)
			case "short-chunk":
				sqliteLargeExec(t, external, "UPDATE ledger_value_chunks SET payload=substr(payload,1,17) WHERE value_id=? AND position=0", valueID)
			case "checksum":
				sqliteLargeExec(t, external, "UPDATE ledger_values SET checksum=zeroblob(32) WHERE id=?", valueID)
			case "kind":
				sqliteLargeExec(t, external, "UPDATE ledger_values SET kind='identity' WHERE id=?", valueID)
			case "marker":
				sqliteLargeExec(t, external, "UPDATE ledger_records SET payload=?", sqliteLedgerValueMarker(valueID+100))
			case "null-reference":
				sqliteLargeExec(t, external, "UPDATE ledger_records SET payload_value=NULL")
			case "huge-size":
				sqliteLargeExec(t, external, "UPDATE ledger_values SET size=4611686018427387904 WHERE id=?", valueID)
			case "overflow-size":
				sqliteLargeExec(t, external, "UPDATE ledger_values SET size=9223372036854775807 WHERE id=?", valueID)
			case "oversized-blob":
				sqliteLargeExec(t, external, "UPDATE ledger_value_chunks SET payload=zeroblob(5242880) WHERE value_id=? AND position=0", valueID)
			case "oversized-text-nul":
				sqliteLargeExec(t, external, "UPDATE ledger_value_chunks SET payload=CAST(zeroblob(5242880) AS TEXT) WHERE value_id=? AND position=0", valueID)
			case "checksum-text-nul":
				sqliteLargeExec(t, external, "UPDATE ledger_values SET checksum=CAST(zeroblob(5242880) AS TEXT) WHERE id=?", valueID)
			case "inline-text-nul":
				sqliteLargeExec(t, external, "UPDATE ledger_records SET payload_value=NULL,payload=CAST(zeroblob(5242880) AS TEXT)")
			case "api-text-nul":
				sqliteLargeExec(t, external, "UPDATE ledger_records SET api=CAST(zeroblob(5242880) AS TEXT)")
			case "model-text-nul":
				sqliteLargeExec(t, external, "UPDATE ledger_records SET model_group=CAST(zeroblob(5242880) AS TEXT)")
			case "zone-text-nul":
				sqliteLargeExec(t, external, "UPDATE ledger_records SET zone=CAST(zeroblob(5242880) AS TEXT)")
			}
			external.Close()
			view := sqliteLargeView(t, s)
			if page, err := view.Page(sqliteLedgerQuery{}, nil, 1); err == nil || len(page) != 0 {
				t.Fatalf("corrupt history was accepted or silently disappeared: count=%d error=%v", len(page), err)
			}
		})
	}
}

func TestSQLiteLedgerLargeDecoderChecksTheWholeStream(t *testing.T) {
	for _, mode := range []string{"whitespace", "second-value", "trailing-checksum"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testSQLiteLedger(t)
			ctx := context.Background()
			tx, err := s.writer.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			raw := append([]byte(`{"failure":"synthetic"}`), bytes.Repeat([]byte(" "), 2*sqliteLedgerValueChunkBytes)...)
			if mode == "second-value" {
				raw = append(raw, []byte(" {}")...)
			}
			marker, reference, err := storeSQLiteLedgerValue(ctx, tx, "request", raw)
			if err != nil {
				t.Fatal(err)
			}
			id := reference.(int64)
			if mode == "trailing-checksum" {
				if _, err := tx.Exec("UPDATE ledger_value_chunks SET payload=? WHERE value_id=? AND position=?", []byte(strings.Repeat("\t", len(raw)-2*sqliteLedgerValueChunkBytes)), id, 2*sqliteLedgerValueChunkBytes); err != nil {
					t.Fatal(err)
				}
			}
			var result RequestDetail
			err = decodeSQLiteLedgerValue(ctx, tx, marker, sql.NullInt64{Int64: id, Valid: true}, sql.NullInt64{Int64: int64(len(raw)), Valid: true}, "request", &result)
			if mode == "whitespace" {
				if err != nil || result.Failure != "synthetic" {
					t.Fatalf("valid whitespace was rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("decoder accepted an unverified suffix after a complete JSON value")
			}
		})
	}
}

// Construct the actual pre-chunk schema; merely lowering user_version on a
// current database would leave extension columns and hide upgrade failures.
func sqliteLargeLegacyLedger(t *testing.T, version int) (*sqliteLedger, string, sqliteLedgerRecord) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(sqliteLedgerDriverName, sqliteLedgerDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	sqliteLargeExec(t, db, sqliteLedgerSchema)
	sqliteLargeExec(t, db, sqliteMigrationStagingSchema)
	sqliteLargeExec(t, db, sqliteLedgerStateSchema)
	if version >= 4 {
		projection := sqliteMigrationProjectionSchema
		if version == 4 {
			projection = strings.Replace(projection, " prefix BLOB NOT NULL CHECK(length(prefix)=32),\n", "", 1)
			projection = strings.Replace(projection, "migration_projection_edges(parent,position,child)", "migration_projection_edges(parent,position) WHERE position>=0", 1)
		}
		sqliteLargeExec(t, db, projection)
	}
	sqliteLargeExec(t, db, fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=%d", sqliteLedgerApplicationID, version))
	record := sqliteTestRecord(1)
	record.ID, record.Revision = 7, 3
	record.Detail.Source = strings.Repeat("legacy identity", 10<<10)
	record.Detail.Failure = strings.Repeat("legacy payload", 20<<10)
	identity, payload, err := encodeSQLiteLedgerRecord(record, sqliteLedgerRecordBytes)
	if err != nil || len(identity) <= sqliteLedgerInlineValueBytes || len(payload) <= sqliteLedgerInlineValueBytes {
		t.Fatalf("invalid legacy inline fixture: %v", err)
	}
	sqliteLargeExec(t, db, "INSERT INTO ledger_identities(id,payload) VALUES(1,?)", identity)
	zone, offset := record.Detail.Timestamp.Zone()
	sqliteLargeExec(t, db, `INSERT INTO ledger_records(id,revision,identity_id,api,model_group,seconds,nanos,zone,utc_offset,zero_time,archived,payload)
 VALUES(7,3,1,?,?,?,?,?,?,0,0,?)`, record.API, record.Model, record.Detail.Timestamp.Unix(), record.Detail.Timestamp.Nanosecond(), zone, offset, payload)
	sqliteLargeExec(t, db, "UPDATE ledger_meta SET generation=3")
	sqliteLargeExec(t, db, "INSERT INTO ledger_state VALUES('legacy-state',3,?)", []byte("preserved"))
	sqliteLargeExec(t, db, "INSERT INTO ledger_progress VALUES('legacy-progress','synthetic',55)")
	sqliteLargeExec(t, db, "PRAGMA journal_mode=WAL")
	s := &sqliteLedger{writer: db}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.reader, err = sql.Open(sqliteLedgerDriverName, sqliteLedgerDSN(path, true))
	if err != nil {
		t.Fatal(err)
	}
	s.reader.SetMaxOpenConns(2)
	t.Cleanup(func() { s.Close() })
	return s, path, record
}

func TestSQLiteLedgerLargeRealLegacyUpgradeAndBackup(t *testing.T) {
	for _, version := range []int{3, 4, 5} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			legacy, path, expected := sqliteLargeLegacyLedger(t, version)
			ctx := context.Background()
			sourceBytes := []byte("{\"api\":\"synthetic\",\"model\":\"legacy\",\"detail\":{}}\n")
			source := writeSQLiteMigrationSource(t, sourceBytes)
			if _, err := legacy.StageSource(ctx, source, "jsonl"); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(t.TempDir(), "legacy-backup")
			backup, err := legacy.Backup(ctx, directory)
			if err != nil || backup.Schema != version || backup.Generation != 3 {
				t.Fatalf("true legacy backup failed: schema=%d error=%v", backup.Schema, err)
			}
			legacy.Close()
			restored := filepath.Join(t.TempDir(), "restored.sqlite")
			if err := restoreSQLiteLedgerBackup(ctx, directory, restored); err != nil {
				t.Fatal(err)
			}
			for _, location := range []string{path, restored} {
				s, err := openSQLiteLedger(ctx, location)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				var schema int
				if err := s.writer.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != sqliteLedgerSchemaVersion {
					t.Fatalf("legacy upgrade did not publish current schema: %d %v", schema, err)
				}
				v := sqliteLargeView(t, s)
				rows := sqliteReadAll(t, v, sqliteLedgerQuery{}, 1)
				if len(rows) != 1 || !reflect.DeepEqual(rows[0], expected) || v.Generation != 3 {
					t.Fatal("schema upgrade changed original inline data, IDs or generations")
				}
				state, found, err := v.State("legacy-state")
				if err != nil || !found || state.Revision != 3 || string(state.Value) != "preserved" {
					t.Fatal("schema upgrade lost legacy derived state")
				}
				_, position, found, err := v.Progress("legacy-progress")
				if err != nil || !found || position != 55 {
					t.Fatal("schema upgrade lost legacy migration progress")
				}
				v.Close()
				if err := s.WithStagedSource(ctx, source, func(reader io.Reader) error {
					got, err := io.ReadAll(reader)
					if err == nil && !bytes.Equal(got, sourceBytes) {
						return errors.New("schema upgrade altered staged bytes")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				next := sqliteTestRecord(2)
				next.Detail.Failure = strings.Repeat("new large request", 400<<10)
				if ids := sqliteLargeApply(t, s, sqliteLedgerMutation{Record: next}); len(ids) != 1 || ids[0] <= expected.ID {
					t.Fatal("upgraded legacy database cannot accept new large records")
				}
				s.Close()
			}
			again, err := verifySQLiteLedgerBackup(ctx, directory)
			if err != nil || again.Schema != version || again.SHA256 != backup.SHA256 {
				t.Fatal("upgrading the restored copy changed its legacy backup")
			}
		})
	}
}

func TestSQLiteLedgerLargeIncompatibleExtensionsDoNotUpgrade(t *testing.T) {
	for _, mode := range []string{"no-cascade", "wrong-primary-key", "wrong-index", "column-without-reference"} {
		t.Run(mode, func(t *testing.T) {
			s, path, _ := sqliteLargeLegacyLedger(t, 5)
			switch mode {
			case "no-cascade":
				sqliteLargeExec(t, s.writer, strings.Replace(sqliteLedgerValueSchema, "ON DELETE CASCADE", "ON DELETE NO ACTION", 1))
			case "wrong-primary-key":
				sqliteLargeExec(t, s.writer, strings.Replace(sqliteLedgerValueSchema, "PRIMARY KEY(value_id,position)", "PRIMARY KEY(value_id,payload)", 1))
			case "wrong-index":
				sqliteLargeExec(t, s.writer, sqliteLedgerValueSchema)
				sqliteLargeExec(t, s.writer, "DROP INDEX ledger_values_content; CREATE INDEX ledger_values_content ON ledger_values(size)")
			case "column-without-reference":
				sqliteLargeExec(t, s.writer, "ALTER TABLE ledger_records ADD COLUMN payload_value INTEGER")
			}
			s.Close()
			upgraded, err := openSQLiteLedger(context.Background(), path)
			if err == nil {
				upgraded.Close()
				t.Fatal("incompatible pre-existing extension was silently accepted")
			}
			external := sqliteLargeExternalDB(t, path)
			var version, generation int
			if err := external.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
				t.Fatalf("failed upgrade changed schema version: %d %v", version, err)
			}
			if err := external.QueryRow("SELECT generation FROM ledger_meta").Scan(&generation); err != nil || generation != 3 || sqliteLargeCount(t, external, "ledger_records") != 1 {
				t.Fatal("failed upgrade changed original authority")
			}
		})
	}
}

func TestSQLiteLedgerLargeBackupRoundTrip(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	record := sqliteTestRecord(1)
	record.Detail.Source = strings.Repeat("source", 1<<20)
	record.Detail.Failure = strings.Repeat("failure", 1<<20)
	sqliteLargeApply(t, s, sqliteLedgerMutation{Record: record})
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "large-backup")
	backup, err := s.Backup(ctx, directory)
	if err != nil || backup.Schema != sqliteLedgerSchemaVersion || backup.Generation != 1 {
		t.Fatalf("large backup failed: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := restoreSQLiteLedgerBackup(ctx, directory, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := openSQLiteLedger(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	view := sqliteLargeView(t, restored)
	rows := sqliteReadAll(t, view, sqliteLedgerQuery{}, 1)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Detail, record.Detail) || view.Generation != 1 {
		t.Fatal("backup lost large request/identity data")
	}
	// Check exact source bytes, not only JSON equivalence, after restoration.
	type value struct {
		id       int64
		kind     string
		size     int64
		checksum []byte
	}
	meta, err := view.tx.Query("SELECT id,kind,size,checksum FROM ledger_values ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var values []value
	for meta.Next() {
		var v value
		if err := meta.Scan(&v.id, &v.kind, &v.size, &v.checksum); err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	if err := errors.Join(meta.Err(), meta.Close()); err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		reader, err := openSQLiteLedgerValueReader(view.ctx, view.tx, v.id, v.kind, v.size)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		n, err := io.Copy(hash, reader)
		reader.Close()
		if err != nil || n != v.size || !bytes.Equal(hash.Sum(nil), v.checksum) {
			t.Fatal("restored value bytes differ from their original checksums")
		}
	}
	manifest, err := os.ReadFile(filepath.Join(directory, sqliteBackupManifest))
	if err != nil {
		t.Fatal(err)
	}
	var stored sqliteLedgerBackup
	if err := json.Unmarshal(manifest, &stored); err != nil || stored.SHA256 != backup.SHA256 {
		t.Fatal("backup manifest changed during restoration")
	}
}
