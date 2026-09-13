package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSQLiteStateAndRecordsShareGenerationAndRollback(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	ids, err := s.ApplyState(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, []sqliteLedgerStateMutation{{Name: "aggregate", Value: []byte("one")}}, &sqliteLedgerProgress{Source: "source", Fingerprint: "hash", NextOffset: 1})
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	state, found, err := old.State("aggregate")
	if err != nil || !found || string(state.Value) != "one" || state.Revision != old.Generation {
		t.Fatalf("state=%+v found=%t err=%v", state, found, err)
	}
	state.Value[0] = 'X'
	record := sqliteTestRecord(2)
	record.ID = ids[0]
	record.Revision = old.Generation
	_, err = s.ApplyState(ctx, []sqliteLedgerMutation{{Record: record}}, []sqliteLedgerStateMutation{{Name: "aggregate", Revision: state.Revision, Value: []byte("two")}}, &sqliteLedgerProgress{Source: "source", Fingerprint: "hash", ExpectedOffset: 1, NextOffset: 2})
	if err != nil {
		t.Fatal(err)
	}
	still, _, err := old.State("aggregate")
	if err != nil || string(still.Value) != "one" {
		t.Fatal("pinned state changed or caller mutation leaked")
	}
	fresh, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newState, _, err := fresh.State("aggregate")
	if err != nil || string(newState.Value) != "two" || newState.Revision != fresh.Generation {
		t.Fatal("new state not in record generation")
	}
	generation := fresh.Generation
	fresh.Close()
	// First mutate metadata/progress, then fail on a stale record revision.
	_, err = s.ApplyState(ctx, []sqliteLedgerMutation{{Record: record}}, []sqliteLedgerStateMutation{{Name: "aggregate", Revision: generation, Value: []byte("must rollback")}}, &sqliteLedgerProgress{Source: "source", Fingerprint: "hash", ExpectedOffset: 2, NextOffset: 3})
	if !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("stale record: %v", err)
	}
	// A stale state revision must also prevent an otherwise valid insertion.
	_, err = s.ApplyState(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(3)}}, []sqliteLedgerStateMutation{{Name: "aggregate", Revision: state.Revision, Value: []byte("stale")}}, nil)
	if !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("stale state: %v", err)
	}
	latest, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer latest.Close()
	got, _, err := latest.State("aggregate")
	if err != nil || string(got.Value) != "two" || latest.Generation != generation {
		t.Fatal("failed transaction published metadata or generation")
	}
	_, position, _, err := latest.Progress("source")
	if err != nil || position != 2 {
		t.Fatal("failed transaction advanced progress")
	}
	rows := sqliteReadAll(t, latest, sqliteLedgerQuery{IncludeAccounting: true}, 1)
	if len(rows) != 1 || rows[0].Revision != generation {
		t.Fatal("failed transaction published rows")
	}
}

func TestSQLiteStateUpgradeEmptyDeleteAndValidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.sqlite")
	s, err := openSQLiteLedger(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dropSQLiteProjectionSchemaForTest(t, s)
	if _, err = s.writer.Exec("DROP TABLE ledger_state; PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = openSQLiteLedger(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.Generation != 1 || len(sqliteReadAll(t, v, sqliteLedgerQuery{}, 1)) != 1 {
		t.Fatal("schema upgrade rewrote records")
	}
	v.Close()
	_, err = s.ApplyState(ctx, nil, []sqliteLedgerStateMutation{{Name: "empty"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, found, err := v.State("empty")
	v.Close()
	if err != nil || !found || len(state.Value) != 0 {
		t.Fatal("empty BLOB missing")
	}
	for _, invalid := range [][]sqliteLedgerStateMutation{
		{{Name: ""}}, {{Name: "x", Revision: -1}}, {{Name: "x", Delete: true}}, {{Name: "x"}, {Name: "x"}}, {{Name: "x", Value: make([]byte, sqliteLedgerRecordBytes)}},
	} {
		if _, err := s.ApplyState(ctx, nil, invalid, nil); err == nil {
			t.Fatal("invalid state accepted")
		}
	}
	_, err = s.ApplyState(ctx, nil, []sqliteLedgerStateMutation{{Name: "empty", Revision: state.Revision, Delete: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, found, err := v.State("empty"); err != nil || found {
		t.Fatal("deleted state is visible")
	}
	if _, _, err := v.State("missing"); err != nil {
		t.Fatal(err)
	}
}
