package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeSQLiteMigrationSource(t *testing.T, payload []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy ?# 源.jsonl")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertSQLiteStagedBytes(t *testing.T, s *sqliteLedger, path string, want []byte) {
	t.Helper()
	err := s.WithStagedSource(context.Background(), path, func(r io.Reader) error {
		got, err := io.ReadAll(r)
		if err == nil && !bytes.Equal(got, want) {
			t.Fatalf("staged bytes changed: got %d bytes, want %d", len(got), len(want))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteMigrationStagesLargeRecordsAndMetadataLosslessly(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	detail := sqliteTestRecord(1).Detail
	// Existing persistence can contain records larger than the prototype
	// ledger's 1 MiB row limit. Staging must not truncate or reject these.
	detail.Failure = strings.Repeat("x", 3<<20)
	record := persistedDetail{API: "api", Model: "group", Detail: detail}
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for _, metadata := range []bool{false, false, true} {
		record.MetadataOnly = metadata
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	path := writeSQLiteMigrationSource(t, payload.Bytes())
	info, _ := os.Stat(path)
	result, err := s.StageSource(context.Background(), path, "jsonl")
	if err != nil || !result.Ready || result.Position != int64(payload.Len()) {
		t.Fatalf("stage: %+v err=%v", result, err)
	}
	assertSQLiteStagedBytes(t, s, path, payload.Bytes())
	again, err := s.StageSource(context.Background(), path, "jsonl")
	if err != nil || again != result {
		t.Fatalf("idempotent stage: %+v err=%v", again, err)
	}
	var rows, maxChunk, generation int
	if err := s.reader.QueryRow("SELECT count(*),max(length(payload)) FROM migration_chunks").Scan(&rows, &maxChunk); err != nil || maxChunk > sqliteMigrationChunkBytes || rows < 30 {
		t.Fatalf("chunk budget: rows=%d max=%d err=%v", rows, maxChunk, err)
	}
	if err := s.reader.QueryRow("SELECT generation FROM ledger_meta").Scan(&generation); err != nil || generation != 0 {
		t.Fatalf("staging altered business generation: %d err=%v", generation, err)
	}
	if err := s.reader.QueryRow("SELECT count(*) FROM ledger_records").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("staging prematurely counted metadata/duplicates as requests: %d err=%v", rows, err)
	}
	got, err := os.ReadFile(path)
	after, statErr := os.Stat(path)
	if err != nil || statErr != nil || !bytes.Equal(got, payload.Bytes()) || !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("migration modified the original source")
	}
}

func TestSQLiteMigrationAtomicResumeAfterWriteFailure(t *testing.T) {
	s, dbPath := testSQLiteLedger(t)
	payload := bytes.Repeat([]byte("0123456789"), 500000)
	path := writeSQLiteMigrationSource(t, payload)
	// Fail after one chunk of the second transaction has been inserted. Its
	// earlier chunk AND its cursor must roll back, while batch one survives.
	if _, err := s.writer.Exec(`CREATE TRIGGER fail_migration BEFORE INSERT ON migration_chunks
 WHEN NEW.position>=2359296 BEGIN SELECT RAISE(ABORT,'injected disk write failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.StageSource(context.Background(), path, "snapshot")
	if err == nil || result.Ready {
		t.Fatalf("write failure hidden: %+v err=%v", result, err)
	}
	stored, err := s.migrationSource(context.Background(), path)
	if err != nil || stored.Position != 2<<20 || stored.Ready {
		t.Fatalf("transaction cursor not rolled back: %+v err=%v", stored, err)
	}
	var chunks int
	if err := s.reader.QueryRow("SELECT count(*) FROM migration_chunks").Scan(&chunks); err != nil || chunks != sqliteMigrationBatchChunks {
		t.Fatalf("partial transaction leaked: chunks=%d err=%v", chunks, err)
	}
	if err := s.WithStagedSource(context.Background(), path, func(io.Reader) error { t.Fatal("unverified source exposed"); return nil }); err == nil {
		t.Fatal("unverified source accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openSQLiteLedger(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.writer.Exec("DROP TRIGGER fail_migration"); err != nil {
		t.Fatal(err)
	}
	result, err = s.StageSource(context.Background(), path, "snapshot")
	if err != nil || !result.Ready || result.Position != int64(len(payload)) {
		t.Fatalf("resume: %+v err=%v", result, err)
	}
	assertSQLiteStagedBytes(t, s, path, payload)
}

func TestSQLiteMigrationRejectsChangedSourcesAndFormats(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	path := writeSQLiteMigrationSource(t, []byte("original"))
	if _, err := s.StageSource(ctx, path, "jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageSource(ctx, path, "snapshot"); !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("format changed without conflict: %v", err)
	}
	info, _ := os.Stat(path)
	if err := os.WriteFile(path, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Same size and restored mtime must still fail the content fingerprint.
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageSource(ctx, path, "jsonl"); !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("content change accepted: %v", err)
	}
	assertSQLiteStagedBytes(t, s, path, []byte("original"))
	link := filepath.Join(t.TempDir(), "source-link")
	if err := os.Symlink(path, link); err == nil {
		if _, err := s.StageSource(ctx, link, "jsonl"); err == nil {
			t.Fatal("symlink accepted")
		}
	}
	if _, err := s.StageSource(ctx, filepath.Dir(path), "jsonl"); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestSQLiteMigrationEmptyCancellationAndReaderLifetime(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	path := writeSQLiteMigrationSource(t, nil)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.StageSource(canceled, path, "jsonl"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if _, err := s.migrationSource(ctx, path); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("canceled source created state: %v", err)
	}
	result, err := s.StageSource(ctx, path, "jsonl")
	if err != nil || !result.Ready || result.Size != 0 {
		t.Fatalf("empty file not completed: %+v err=%v", result, err)
	}
	assertSQLiteStagedBytes(t, s, path, nil)
	var borrowed io.Reader
	if err := s.WithStagedSource(ctx, path, func(r io.Reader) error { borrowed = r; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := borrowed.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("reader escaped callback lifetime: %v", err)
	}
}

func TestSQLiteMigrationChecksumPreventsActivation(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	payload := []byte("original")
	path := writeSQLiteMigrationSource(t, payload)
	hash, err := sqliteMigrationHash(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a fully copied but not verified source with corrupted bytes.
	if _, err := s.writer.Exec("INSERT INTO migration_sources VALUES(?,?,?,?,?,0)", path, "jsonl", hash, len(payload), len(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec("INSERT INTO migration_chunks VALUES(?,0,?)", path, []byte("modified")); err != nil {
		t.Fatal(err)
	}
	if result, err := s.StageSource(ctx, path, "jsonl"); err == nil || result.Ready {
		t.Fatalf("corrupted bytes marked ready: %+v err=%v", result, err)
	}
	stored, err := s.migrationSource(ctx, path)
	if err != nil || stored.Ready {
		t.Fatalf("checksum error persisted readiness: %+v err=%v", stored, err)
	}
}

func TestSQLiteMigrationSchemaUpgradePreservesLedger(t *testing.T) {
	s, path := testSQLiteLedger(t)
	ctx := context.Background()
	if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := sqliteReadAll(t, v, sqliteLedgerQuery{}, 10)
	wantGeneration := v.Generation
	v.Close()
	dropSQLiteProjectionSchemaForTest(t, s)
	if _, err := s.writer.Exec("DROP TABLE migration_chunks; DROP TABLE migration_sources; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = openSQLiteLedger(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err = s.ReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if got := sqliteReadAll(t, v, sqliteLedgerQuery{}, 10); !reflect.DeepEqual(got, want) || v.Generation != wantGeneration {
		t.Fatal("schema upgrade altered business records or generation")
	}
	var version int
	if err := s.reader.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != sqliteLedgerSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}

func TestSQLiteMigrationDetectsCorruptionAfterVerification(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	path := writeSQLiteMigrationSource(t, []byte("original"))
	if _, err := s.StageSource(ctx, path, "jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec("UPDATE migration_chunks SET payload=? WHERE source=?", []byte("modified"), path); err != nil {
		t.Fatal(err)
	}
	if err := s.WithStagedSource(ctx, path, func(r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }); err == nil {
		t.Fatal("reader did not report post-verification corruption")
	}
	if _, err := s.StageSource(ctx, path, "jsonl"); err == nil {
		t.Fatal("ready flag bypassed checksum on retry")
	}
}

func TestSQLiteMigrationStaleCursorRollsBack(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	payload := []byte("onetwo")
	hash, err := sqliteMigrationHash(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	source := sqliteMigrationSource{Source: "sealed-source", Format: "jsonl", Fingerprint: hash, Size: int64(len(payload))}
	if _, err := s.writer.Exec("INSERT INTO migration_sources VALUES(?,?,?,?,0,0)", source.Source, source.Format, source.Fingerprint, source.Size); err != nil {
		t.Fatal(err)
	}
	if err := s.stageSourceBatch(ctx, source, payload[:3]); err != nil {
		t.Fatal(err)
	}
	if err := s.stageSourceBatch(ctx, source, payload[3:]); !errors.Is(err, errSQLiteLedgerConflict) {
		t.Fatalf("stale source position not rejected: %v", err)
	}
	stored, err := s.migrationSource(ctx, source.Source)
	if err != nil || stored.Position != 3 {
		t.Fatalf("stale writer advanced cursor: %+v err=%v", stored, err)
	}
	var chunks int
	if err := s.reader.QueryRow("SELECT count(*) FROM migration_chunks").Scan(&chunks); err != nil || chunks != 1 {
		t.Fatalf("stale writer left chunks: count=%d err=%v", chunks, err)
	}
}
