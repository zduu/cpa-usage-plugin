package main

// Lossless, resumable staging, NOT migration activation. Snapshot totals,
// overlapping JSONL requests and metadata_only updates need reconciliation
// before they may enter ledger_records. Keep their original bytes separate.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

const sqliteMigrationChunkBytes = 256 << 10
const sqliteMigrationBatchChunks = 8

const sqliteMigrationStagingSchema = `
CREATE TABLE migration_sources (
 source TEXT PRIMARY KEY, format TEXT NOT NULL CHECK(format IN ('snapshot','jsonl')),
 fingerprint TEXT NOT NULL, size INTEGER NOT NULL CHECK(size>=0),
 position INTEGER NOT NULL CHECK(position>=0 AND position<=size),
 ready INTEGER NOT NULL CHECK(ready IN (0,1) AND (ready=0 OR position=size))
) WITHOUT ROWID;
CREATE TABLE migration_chunks (
 source TEXT NOT NULL REFERENCES migration_sources(source),
 position INTEGER NOT NULL CHECK(position>=0),
 payload BLOB NOT NULL CHECK(length(payload)>0 AND length(payload)<=262144),
 PRIMARY KEY(source,position)
) WITHOUT ROWID;
`

type sqliteMigrationSource struct {
	Source, Format, Fingerprint string
	Size, Position              int64
	Ready                       bool // Bytes verified; not permission to activate the backend.
}

// StageSource copies a sealed, no-longer-written legacy file without changing
// it. Each transaction retains at most 2 MiB of input, and commits its offset
// with its chunks. A restart hashes the source once again, then resumes at the
// committed offset. No line/record-size limit is imposed here: even a large
// snapshot or JSONL record is split into bounded BLOBs, without JSON decoding.
// The caller must stop the old writer before using this migration primitive.
func (s *sqliteLedger) StageSource(parent context.Context, path, format string) (result sqliteMigrationSource, err error) {
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	if format != "snapshot" && format != "jsonl" {
		return result, errors.New("unsupported sqlite migration source format")
	}
	path, err = canonicalSQLiteMigrationPath(path)
	if err != nil {
		return result, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("sqlite migration source must be a regular, non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	unchanged := func() error {
		current, err := file.Stat()
		if err != nil {
			return err
		}
		named, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !named.Mode().IsRegular() || !os.SameFile(info, current) || !os.SameFile(info, named) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return errors.New("sqlite migration source changed; stop the legacy writer before migration")
		}
		return nil
	}
	if err := unchanged(); err != nil {
		return result, err
	}
	fingerprint, err := sqliteMigrationHash(ctx, file)
	if err != nil {
		return result, err
	}
	if err := unchanged(); err != nil {
		return result, err
	}
	result = sqliteMigrationSource{Source: path, Format: format, Fingerprint: fingerprint, Size: info.Size()}
	// Empty sources also acquire an identity, so replacing an empty file later
	// cannot silently turn a completed migration into a different input.
	if _, err := s.writer.ExecContext(ctx, "INSERT INTO migration_sources VALUES(?,?,?,?,0,0) ON CONFLICT(source) DO NOTHING", path, format, fingerprint, info.Size()); err != nil {
		return result, err
	}
	stored, err := s.migrationSource(ctx, path)
	if err != nil {
		return result, err
	}
	if stored.Format != format || stored.Fingerprint != fingerprint || stored.Size != info.Size() {
		return result, errSQLiteLedgerConflict
	}
	result = stored
	if _, err := file.Seek(result.Position, io.SeekStart); err != nil {
		return result, err
	}
	buffer := make([]byte, sqliteMigrationChunkBytes*sqliteMigrationBatchChunks)
	for result.Position < result.Size {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		n := int(min(int64(len(buffer)), result.Size-result.Position))
		if _, err := io.ReadFull(file, buffer[:n]); err != nil {
			return result, err
		}
		if err := unchanged(); err != nil {
			return result, err
		}
		if err := s.stageSourceBatch(ctx, result, buffer[:n]); err != nil {
			return result, err
		}
		result.Position += int64(n)
	}
	if err := unchanged(); err != nil {
		return result, err
	}
	// Hash the staged bytes, not just the original file. This catches missing,
	// corrupt or mixed chunks before any downstream parser can use them.
	reader := &sqliteMigrationReader{ctx: ctx, db: s.reader, source: result}
	stagedHash, err := sqliteMigrationHash(ctx, reader)
	if err != nil {
		return result, err
	}
	if stagedHash != result.Fingerprint {
		return result, errors.New("sqlite migration staged checksum mismatch")
	}
	changed, err := s.writer.ExecContext(ctx, "UPDATE migration_sources SET ready=1 WHERE source=? AND fingerprint=? AND position=size", path, fingerprint)
	if err := sqliteRequireChanged(changed, err); err != nil {
		return result, err
	}
	result.Ready = true
	return result, nil
}

func (s *sqliteLedger) migrationSource(ctx context.Context, path string) (source sqliteMigrationSource, err error) {
	source.Source = path
	err = s.reader.QueryRowContext(ctx, "SELECT format,fingerprint,size,position,ready FROM migration_sources WHERE source=?", path).Scan(&source.Format, &source.Fingerprint, &source.Size, &source.Position, &source.Ready)
	return source, err
}

func (s *sqliteLedger) stageSourceBatch(ctx context.Context, source sqliteMigrationSource, payload []byte) error {
	if len(payload) == 0 || len(payload) > sqliteMigrationChunkBytes*sqliteMigrationBatchChunks || int64(len(payload)) > source.Size-source.Position {
		return errSQLiteLedgerBudget
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed, err := tx.ExecContext(ctx, "UPDATE migration_sources SET position=position+? WHERE source=? AND fingerprint=? AND position=? AND ready=0", len(payload), source.Source, source.Fingerprint, source.Position)
	if err := sqliteRequireChanged(changed, err); err != nil {
		return err
	}
	for offset := 0; offset < len(payload); offset += sqliteMigrationChunkBytes {
		chunk := payload[offset:min(offset+sqliteMigrationChunkBytes, len(payload))]
		if _, err := tx.ExecContext(ctx, "INSERT INTO migration_chunks VALUES(?,?,?)", source.Source, source.Position+int64(offset), chunk); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// WithStagedSource lends a bounded reader only for the callback's lifetime.
// It doesn't hold a long read transaction or pin the WAL: ready source chunks
// are immutable. Snapshot/JSONL parsing and semantic migration come later.
func (s *sqliteLedger) WithStagedSource(parent context.Context, path string, consume func(io.Reader) error) (err error) {
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	if consume == nil {
		return errors.New("sqlite migration requires a consumer")
	}
	path, err = canonicalSQLiteMigrationPath(path)
	if err != nil {
		return err
	}
	source, err := s.migrationSource(ctx, path)
	if err != nil {
		return err
	}
	if !source.Ready {
		return errors.New("sqlite migration source is not verified")
	}
	return consume(&sqliteMigrationReader{ctx: ctx, db: s.reader, source: source})
}

func canonicalSQLiteMigrationPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("sqlite migration requires an explicit source path")
	}
	return filepath.Abs(path)
}

type sqliteMigrationReader struct {
	ctx      context.Context
	db       *sql.DB
	source   sqliteMigrationSource
	position int64
	chunk    *bytes.Reader
	hash     hash.Hash
}

func (r *sqliteMigrationReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.chunk != nil && r.chunk.Len() > 0 {
		return r.chunk.Read(p)
	}
	if r.position == r.source.Size {
		if r.hash == nil {
			r.hash = sha256.New()
		}
		if hex.EncodeToString(r.hash.Sum(nil)) != r.source.Fingerprint {
			return 0, errors.New("sqlite migration staged checksum mismatch")
		}
		return 0, io.EOF
	}
	var size int
	var payload []byte
	// Check the length in SQL as well, so even a damaged/external oversized
	// row is not copied into Go by the comparison driver without native limits.
	err := r.db.QueryRowContext(r.ctx, "SELECT length(payload),CASE WHEN length(payload)<=? THEN payload ELSE NULL END FROM migration_chunks WHERE source=? AND position=?", sqliteMigrationChunkBytes, r.source.Source, r.position).Scan(&size, &payload)
	if err != nil {
		return 0, fmt.Errorf("read sqlite migration chunk at %d: %w", r.position, err)
	}
	if size <= 0 || size > sqliteMigrationChunkBytes || len(payload) != size || int64(size) > r.source.Size-r.position {
		return 0, errors.New("invalid sqlite migration chunk size")
	}
	r.position += int64(size)
	if r.hash == nil {
		r.hash = sha256.New()
	}
	_, _ = r.hash.Write(payload)
	r.chunk = bytes.NewReader(payload)
	return r.chunk.Read(p)
}

func sqliteMigrationHash(ctx context.Context, reader io.Reader) (string, error) {
	hash := sha256.New()
	buffer := make([]byte, sqliteMigrationChunkBytes)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := reader.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}
