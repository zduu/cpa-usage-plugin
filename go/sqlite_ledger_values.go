package main

// Large request/identity values are immutable, checksummed chunk streams.
// Their references commit with the record, generation, state and progress.
// The existing SQL row guard stays in place; it is not raised to fit a large
// legacy request. API/model sort keys and opaque state retain their current
// inline limits and still need a separate oversized-key/runtime policy.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
)

const (
	sqliteLedgerInlineValueBytes = 64 << 10
	sqliteLedgerValueChunkBytes  = 256 << 10
)

var errSQLiteLedgerValueCorrupt = errors.New("sqlite ledger value is missing, corrupt or oversized inline")

const sqliteLedgerValueSchema = `
CREATE TABLE IF NOT EXISTS ledger_values (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 kind TEXT NOT NULL CHECK(kind IN ('identity','request')),
 size INTEGER NOT NULL CHECK(size>0),
 checksum BLOB NOT NULL CHECK(length(CAST(checksum AS BLOB))=32)
);
CREATE INDEX IF NOT EXISTS ledger_values_content ON ledger_values(kind,checksum,size);
CREATE TABLE IF NOT EXISTS ledger_value_chunks (
 value_id INTEGER NOT NULL REFERENCES ledger_values(id) ON DELETE CASCADE,
 position INTEGER NOT NULL CHECK(position>=0),
 payload BLOB NOT NULL CHECK(length(CAST(payload AS BLOB))>0 AND length(CAST(payload AS BLOB))<=262144),
 PRIMARY KEY(value_id,position)
) WITHOUT ROWID;
`

const sqliteLedgerValueReferenceIndexes = `
CREATE INDEX IF NOT EXISTS ledger_identities_value ON ledger_identities(payload_value) WHERE payload_value IS NOT NULL;
CREATE INDEX IF NOT EXISTS ledger_records_value ON ledger_records(payload_value) WHERE payload_value IS NOT NULL;
`

func initializeSQLiteLedgerValues(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, sqliteLedgerValueSchema); err != nil {
		return err
	}
	// Additive/idempotent just like ledger_state: pre-existing extension
	// tables do not require rewriting the original authority or its IDs.
	for _, table := range []string{"ledger_identities", "ledger_records"} {
		var found int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info(?) WHERE name='payload_value'", table).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN payload_value INTEGER REFERENCES ledger_values(id)"); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, sqliteLedgerValueReferenceIndexes)
	return err
}

// IF NOT EXISTS is only for our own already-created extension, not a claim
// that arbitrary tables/indexes with these names are compatible. In
// particular, chunk reclamation relies on its cascading foreign key and
// bounded candidate lookup relies on the content index. These new objects
// have one supported definition; SQLite removes IF NOT EXISTS in sqlite_schema.
// Validate before committing user_version, and also when reopening schema 6.
func validateSQLiteLedgerValues(ctx context.Context, tx *sql.Tx) error {
	canonical := func(definition string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(definition, "IF NOT EXISTS ", "")), " ")
	}
	for _, definition := range strings.Split(sqliteLedgerValueSchema+sqliteLedgerValueReferenceIndexes, ";") {
		want := canonical(definition)
		if want == "" {
			continue
		}
		fields := strings.Fields(want)
		name := fields[2] // All definitions are fixed CREATE TABLE/INDEX statements.
		var actual sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(sql AS BLOB))<=16384 THEN sql ELSE NULL END
 FROM sqlite_schema WHERE name=? AND type=?`, name, strings.ToLower(fields[1])).Scan(&actual)
		if err != nil {
			return err
		}
		if !actual.Valid || canonical(actual.String) != want {
			return fmt.Errorf("incompatible sqlite ledger value schema: %s", name)
		}
	}
	for _, table := range []string{"ledger_identities", "ledger_records"} {
		var kind sql.NullString
		var notNull, noDefault, primaryKey int
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(type AS BLOB))<=32 THEN type ELSE NULL END,
 "notnull",dflt_value IS NULL,pk FROM pragma_table_info(?) WHERE name='payload_value'`, table).Scan(&kind, &notNull, &noDefault, &primaryKey)
		if err != nil {
			return err
		}
		if !kind.Valid || kind.String != "INTEGER" || notNull != 0 || noDefault != 1 || primaryKey != 0 {
			return fmt.Errorf("incompatible sqlite ledger value column: %s.payload_value", table)
		}
		var total, compatible int
		err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum("table"='ledger_values' AND "to"='id'
 AND on_delete='NO ACTION' AND on_update='NO ACTION' AND match='NONE'),0)
 FROM pragma_foreign_key_list(?) WHERE "from"='payload_value'`, table).Scan(&total, &compatible)
		if err != nil {
			return err
		}
		if total != 1 || compatible != 1 {
			return fmt.Errorf("incompatible sqlite ledger value reference: %s.payload_value", table)
		}
	}
	return nil
}

// The marker cannot be a JSON identity or request: its first byte is NUL.
// Keeping a unique marker preserves the legacy identity UNIQUE constraint.
func sqliteLedgerValueMarker(id int64) []byte {
	marker := make([]byte, 9)
	binary.BigEndian.PutUint64(marker[1:], uint64(id))
	return marker
}

func sqliteLedgerCombinedBytes(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		var ok bool
		total, ok = checkedProtocolAdd(total, value)
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func validateSQLiteLedgerValueReference(raw []byte, id, size sql.NullInt64) (int64, error) {
	if !id.Valid {
		if size.Valid || len(raw) == 0 || len(raw) > sqliteLedgerRecordBytes || raw[0] == 0 {
			return 0, errSQLiteLedgerValueCorrupt
		}
		return int64(len(raw)), nil
	}
	if id.Int64 <= 0 || !size.Valid || size.Int64 <= 0 || len(raw) != 9 || !bytes.Equal(raw, sqliteLedgerValueMarker(id.Int64)) {
		return 0, errSQLiteLedgerValueCorrupt
	}
	return size.Int64, nil
}

// storeSQLiteLedgerValue leaves common small values inline. Hashes only select
// candidate large values; complete byte equality is checked before sharing.
// This shares immutable bytes, never record IDs or real requests.
func storeSQLiteLedgerValue(ctx context.Context, tx *sql.Tx, kind string, raw []byte) ([]byte, any, error) {
	if len(raw) <= sqliteLedgerInlineValueBytes {
		return raw, nil, nil
	}
	checksum := sha256.Sum256(raw)
	var after int64
	for {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM ledger_values
 WHERE kind=? AND checksum=? AND size=? AND id>? ORDER BY id LIMIT 1`, kind, checksum[:], len(raw), after).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		equal, err := sqliteLedgerValueEquals(ctx, tx, id, kind, raw)
		if err != nil {
			return nil, nil, err
		}
		if equal {
			return sqliteLedgerValueMarker(id), id, nil
		}
		after = id
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO ledger_values(kind,size,checksum) VALUES(?,?,?)", kind, len(raw), checksum[:])
	if err != nil {
		return nil, nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, nil, err
	}
	statement, err := tx.PrepareContext(ctx, "INSERT INTO ledger_value_chunks(value_id,position,payload) VALUES(?,?,?)")
	if err != nil {
		return nil, nil, err
	}
	defer statement.Close()
	for position := 0; position < len(raw); {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		end := position + min(sqliteLedgerValueChunkBytes, len(raw)-position)
		if _, err := statement.ExecContext(ctx, id, position, raw[position:end]); err != nil {
			return nil, nil, err
		}
		position = end
	}
	return sqliteLedgerValueMarker(id), id, nil
}

type sqliteLedgerValueReader struct {
	rows     *sql.Rows
	ctx      context.Context
	size     int64
	position int64
	checksum []byte
	hash     hash.Hash
	pending  []byte
	done     bool
}

func openSQLiteLedgerValueReader(ctx context.Context, tx *sql.Tx, id int64, kind string, size int64) (*sqliteLedgerValueReader, error) {
	var actualSize int64
	var checksum []byte
	err := tx.QueryRowContext(ctx, `SELECT size,CASE WHEN length(CAST(checksum AS BLOB))=32 THEN checksum ELSE NULL END
 FROM ledger_values WHERE id=? AND kind=?`, id, kind).Scan(&actualSize, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errSQLiteLedgerValueCorrupt
	}
	if err != nil {
		return nil, err
	}
	if id <= 0 || size <= 0 || actualSize != size || len(checksum) != sha256.Size {
		return nil, errSQLiteLedgerValueCorrupt
	}
	// Do not size an allocation from database metadata. CASE also protects the
	// pure-Go driver's conversion into Go from an externally oversized BLOB.
	rows, err := tx.QueryContext(ctx, `SELECT position,length(CAST(payload AS BLOB)),
 CASE WHEN length(CAST(payload AS BLOB))<=? THEN payload ELSE NULL END
 FROM ledger_value_chunks WHERE value_id=? ORDER BY position`, sqliteLedgerValueChunkBytes, id)
	if err != nil {
		return nil, err
	}
	return &sqliteLedgerValueReader{rows: rows, ctx: ctx, size: size, checksum: checksum, hash: sha256.New()}, nil
}

func (r *sqliteLedgerValueReader) Close() error { return r.rows.Close() }

func (r *sqliteLedgerValueReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		if r.done {
			return 0, io.EOF
		}
		if !r.rows.Next() {
			if err := r.rows.Err(); err != nil {
				return 0, err
			}
			if r.position != r.size || !bytes.Equal(r.hash.Sum(nil), r.checksum) {
				return 0, errSQLiteLedgerValueCorrupt
			}
			r.done = true
			return 0, io.EOF
		}
		var position, size int64
		var raw []byte
		if err := r.rows.Scan(&position, &size, &raw); err != nil {
			return 0, err
		}
		if position != r.position || size <= 0 || size > sqliteLedgerValueChunkBytes || int64(len(raw)) != size || size != min(int64(sqliteLedgerValueChunkBytes), r.size-r.position) {
			return 0, errSQLiteLedgerValueCorrupt
		}
		r.pending = raw
		r.position += size
		r.hash.Write(raw)
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func sqliteLedgerValueEquals(ctx context.Context, tx *sql.Tx, id int64, kind string, raw []byte) (bool, error) {
	reader, err := openSQLiteLedgerValueReader(ctx, tx, id, kind, int64(len(raw)))
	if err != nil {
		return false, err
	}
	defer reader.Close()
	buffer := make([]byte, sqliteLedgerValueChunkBytes)
	position, equal := 0, true
	for {
		n, err := reader.Read(buffer)
		if n > len(raw)-position {
			return false, errSQLiteLedgerValueCorrupt
		}
		if !bytes.Equal(buffer[:n], raw[position:position+n]) {
			equal = false
		}
		position += n
		if err != nil {
			if errors.Is(err, io.EOF) && position == len(raw) {
				return equal, nil
			}
			return false, err
		}
	}
}

func decodeSQLiteLedgerValue(ctx context.Context, tx *sql.Tx, raw []byte, id, size sql.NullInt64, kind string, target any) error {
	if _, err := validateSQLiteLedgerValueReference(raw, id, size); err != nil {
		return err
	}
	if !id.Valid {
		return json.Unmarshal(raw, target)
	}
	reader, err := openSQLiteLedgerValueReader(ctx, tx, id.Int64, kind, size.Int64)
	if err != nil {
		return err
	}
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	// Force the complete stream through checksum/length validation, including
	// whitespace after the JSON value; a second value is not a valid payload.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errSQLiteLedgerValueCorrupt
	}
	return nil
}

func removeUnusedSQLiteLedgerValue(ctx context.Context, tx *sql.Tx, id int64) error {
	if id <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM ledger_values WHERE id=?
 AND NOT EXISTS(SELECT 1 FROM ledger_records WHERE payload_value=?)
 AND NOT EXISTS(SELECT 1 FROM ledger_identities WHERE payload_value=?)`, id, id, id)
	return err
}
