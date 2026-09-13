package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSQLiteBackupIncludesWALAndAtomicState(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	old, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	for i := 0; i < 5; i++ {
		r := sqliteTestRecord(i)
		r.Archived = i%2 == 0
		state := sqliteLedgerStateMutation{Name: "aggregate", Revision: int64(i), Value: []byte(fmt.Sprint(i + 1))}
		_, err = s.ApplyState(ctx, []sqliteLedgerMutation{{Record: r}}, []sqliteLedgerStateMutation{state}, &sqliteLedgerProgress{Source: "source", Fingerprint: "verified", ExpectedOffset: int64(i), NextOffset: int64(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := s.Checkpoint(ctx)
	if err != nil || !checkpoint.Busy {
		t.Fatalf("expected pinned WAL: %+v %v", checkpoint, err)
	}
	dir := filepath.Join(t.TempDir(), "backup")
	backup, err := s.Backup(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if backup.Generation != 5 || backup.Schema != sqliteLedgerSchemaVersion || backup.Bytes == 0 {
		t.Fatalf("backup: %+v", backup)
	}
	if _, err := verifySQLiteLedgerBackup(ctx, dir); err != nil {
		t.Fatal(err)
	}
	// Later writes and repricing state cannot alter a completed backup.
	_, err = s.ApplyState(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(10)}}, []sqliteLedgerStateMutation{{Name: "aggregate", Revision: 5, Value: []byte("6")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := restoreSQLiteLedgerBackup(ctx, dir, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := openSQLiteLedger(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	view, err := restored.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	records := sqliteReadAll(t, view, sqliteLedgerQuery{IncludeAccounting: true}, 2)
	if len(records) != 5 || view.Generation != 5 {
		t.Fatalf("restore lost WAL records: %d generation %d", len(records), view.Generation)
	}
	for _, got := range records {
		i := int(got.ID - 1)
		want := sqliteTestRecord(i)
		want.Archived = i%2 == 0
		if want.Archived {
			fields := accountingIdentity{want.Detail.Model, want.Detail.Provider, want.Detail.Source, want.Detail.AuthIndex, want.Detail.AuthID, want.Detail.AuthType, want.Detail.APIKey, want.Detail.APIKeyHash, want.Detail.BaseURL, want.Detail.RequestedModel, want.Detail.ExecutorType, want.Detail.Endpoint}
			want.Detail = (accountingRecord{Identity: &fields, Timestamp: want.Detail.Timestamp, Correlation: want.Detail.Correlation, Tokens: want.Detail.Tokens, LatencyMs: want.Detail.LatencyMs, TTFTMs: want.Detail.TTFTMs, Failure: want.Detail.Failure, StatusCode: want.Detail.StatusCode, Failed: want.Detail.Failed, Synthetic: want.Detail.TimestampSynthetic}).detail()
		}
		if got.Archived != want.Archived || !reflect.DeepEqual(got.Detail, want.Detail) {
			t.Fatalf("restore changed row %d", i)
		}
	}
	state, found, err := view.State("aggregate")
	if err != nil || !found || string(state.Value) != "5" || state.Revision != 5 {
		t.Fatal("restored state does not match records")
	}
	fingerprint, position, found, err := view.Progress("source")
	if err != nil || !found || fingerprint != "verified" || position != 5 {
		t.Fatal("restored progress differs")
	}
	// This also proves backups keep their source intact when restore opens WAL.
	again, err := verifySQLiteLedgerBackup(ctx, dir)
	if err != nil || again.SHA256 != backup.SHA256 {
		t.Fatal("restore changed backup")
	}
}

func TestSQLiteBackupPreviousSchemaCanStillRestore(t *testing.T) {
	for _, schema := range []int{3, 4} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			s, _ := testSQLiteLedger(t)
			ctx := context.Background()
			if _, err := s.ApplyState(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, []sqliteLedgerStateMutation{{Name: "old-state", Value: []byte("keep")}}, nil); err != nil {
				t.Fatal(err)
			}
			path := writeSQLiteMigrationSource(t, []byte(projectionModelJSON(`"details":[{}]`)))
			if _, err := s.StageSource(ctx, path, "snapshot"); err != nil {
				t.Fatal(err)
			}
			if schema == 3 {
				dropSQLiteProjectionSchemaForTest(t, s)
			} else {
				if _, err := s.ProjectMigrationSource(ctx, path); err != nil {
					t.Fatal(err)
				}
				if _, err := s.writer.Exec(`ALTER TABLE migration_projections DROP COLUMN prefix; UPDATE migration_projections SET format=1;
 DROP INDEX migration_projection_array; CREATE INDEX migration_projection_array ON migration_projection_edges(parent,position) WHERE position>=0;`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.writer.Exec(fmt.Sprintf("PRAGMA user_version=%d", schema)); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(t.TempDir(), "previous-backup")
			backup, err := s.Backup(ctx, directory)
			if err != nil || backup.Schema != schema {
				t.Fatalf("previous backup schema rejected: %+v %v", backup, err)
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
			if p, err := restored.ProjectMigrationSource(ctx, path); err != nil || !p.Complete {
				t.Fatalf("restored old staging cannot be projected: %+v %v", p, err)
			}
			v, err := restored.ReadView(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			state, found, err := v.State("old-state")
			if err != nil || !found || string(state.Value) != "keep" || v.Generation != 1 || len(sqliteReadAll(t, v, sqliteLedgerQuery{}, 1)) != 1 {
				t.Fatal("restoring/upgrading old backup changed authority")
			}
			again, err := verifySQLiteLedgerBackup(ctx, directory)
			if err != nil || again.Schema != schema || again.SHA256 != backup.SHA256 {
				t.Fatal("opening restored copy changed the old backup")
			}
		})
	}
}

func TestSQLiteBackupCancellationAndExistingTargets(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	parent := t.TempDir()
	dir := filepath.Join(parent, "backup")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "keep")
	if err := os.WriteFile(keep, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup(ctx, dir); err == nil {
		t.Fatal("replaced existing backup")
	}
	if raw, err := os.ReadFile(keep); err != nil || string(raw) != "original" {
		t.Fatal("backup changed existing directory")
	}
	dir = filepath.Join(parent, "cancelled")
	// Occupy the writer connection: cancellation must release the pending
	// VACUUM request and clean the incomplete backup, without publishing it.
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	_, err = s.Backup(cancelled, dir)
	cancel()
	tx.Rollback()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel backup: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete backup remains: %v", err)
	}
	dir = filepath.Join(parent, "complete")
	if _, err := s.Backup(ctx, dir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "existing.sqlite")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreSQLiteLedgerBackup(ctx, dir, target); err == nil {
		t.Fatal("overwrote restore target")
	}
	if raw, _ := os.ReadFile(target); string(raw) != "keep" {
		t.Fatal("restore modified target")
	}
	target = filepath.Join(parent, "active.sqlite")
	if err := os.WriteFile(target+"-wal", []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreSQLiteLedgerBackup(ctx, dir, target); err == nil {
		t.Fatal("ignored existing WAL")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published over active destination")
	}
	cancelled, cancel = context.WithCancel(ctx)
	cancel()
	if err := restoreSQLiteLedgerBackup(cancelled, dir, filepath.Join(parent, "cancelled.sqlite")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel restore: %v", err)
	}
}

func TestSQLiteBackupRejectsIncompleteCorruptAndForeignData(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	for _, mode := range []string{"missing-manifest", "checksum", "generation", "format", "database", "journal", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "backup")
			backup, err := s.Backup(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, sqliteBackupDatabase)
			switch mode {
			case "missing-manifest":
				err = os.Remove(filepath.Join(dir, sqliteBackupManifest))
			case "checksum":
				backup.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
			case "generation":
				backup.Generation++
			case "format":
				backup.Format++
			case "database":
				err = os.WriteFile(path, []byte("not a database"), 0600)
			case "journal":
				err = os.WriteFile(path+"-wal", []byte("uncommitted"), 0600)
			case "symlink":
				other := filepath.Join(t.TempDir(), "other")
				if err = os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(other, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "checksum" || mode == "generation" || mode == "format" {
				raw, err := json.Marshal(backup)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, sqliteBackupManifest), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			destination := filepath.Join(t.TempDir(), "restore.sqlite")
			if err := restoreSQLiteLedgerBackup(ctx, dir, destination); err == nil {
				t.Fatal("invalid backup accepted")
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid backup created destination")
			}
		})
	}
}
