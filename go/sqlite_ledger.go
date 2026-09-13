package main

// SQLite ledger foundation. Runtime storage selection deliberately does not
// call this yet: migration, aggregate transactions and backend parity must be
// implemented before it can replace the existing JSONL/memory authority.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	sqliteLedgerApplicationID = 0x43504155 // CPAU
	sqliteLedgerSchemaVersion = 7
	sqliteLedgerBatchRecords  = 256
	sqliteLedgerBatchBytes    = 8 << 20
	sqliteLedgerRecordBytes   = 1 << 20 // Inline envelope/keys/state, not a spilled request limit.
	sqliteLedgerPageRecords   = 512
	sqliteLedgerReadLifetime  = 30 * time.Second
)

var (
	errSQLiteLedgerConflict = errors.New("sqlite ledger revision or migration progress conflict")
	errSQLiteLedgerBudget   = errors.New("sqlite ledger batch exceeds prototype budget")
)

type sqliteLedger struct {
	writer, reader *sql.DB
	ctx            context.Context
	cancel         context.CancelFunc
}

type sqliteLedgerRecord struct {
	ID, Revision int64
	API, Model   string // Model is the grouping key, not necessarily Detail.Model.
	Detail       RequestDetail
	Archived     bool
}

type sqliteLedgerMutation struct {
	Record sqliteLedgerRecord // ID=0 inserts a new, independent real request.
	Delete bool               // Updates/deletes require the current Revision.
}

type sqliteLedgerProgress struct {
	Source, Fingerprint        string
	ExpectedOffset, NextOffset int64
}

type sqliteLedgerIdentity struct {
	Fields accountingIdentity
}

const sqliteLedgerSchema = `
CREATE TABLE ledger_meta (singleton INTEGER PRIMARY KEY CHECK(singleton=1), generation INTEGER NOT NULL CHECK(generation>=0));
INSERT INTO ledger_meta VALUES(1,0);
CREATE TABLE ledger_identities (id INTEGER PRIMARY KEY, payload BLOB NOT NULL UNIQUE);
CREATE TABLE ledger_records (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 revision INTEGER NOT NULL CHECK(revision>0),
 identity_id INTEGER NOT NULL REFERENCES ledger_identities(id),
 api TEXT NOT NULL, model_group TEXT NOT NULL,
 seconds INTEGER NOT NULL, nanos INTEGER NOT NULL CHECK(nanos>=0 AND nanos<1000000000),
 sort_seconds INTEGER GENERATED ALWAYS AS (-seconds) VIRTUAL,
 sort_nanos INTEGER GENERATED ALWAYS AS (-nanos) VIRTUAL,
 zone TEXT NOT NULL, utc_offset INTEGER NOT NULL, zero_time INTEGER NOT NULL CHECK(zero_time IN(0,1)),
 archived INTEGER NOT NULL CHECK(archived IN(0,1)), payload BLOB NOT NULL
);
CREATE INDEX ledger_records_order ON ledger_records(sort_seconds,sort_nanos,api,model_group,id);
CREATE INDEX ledger_records_api_order ON ledger_records(api,sort_seconds,sort_nanos,model_group,id);
CREATE INDEX ledger_records_model_order ON ledger_records(model_group,sort_seconds,sort_nanos,api,id);
CREATE INDEX ledger_records_identity ON ledger_records(identity_id);
CREATE TABLE ledger_progress (source TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, position INTEGER NOT NULL CHECK(position>=0)) WITHOUT ROWID;
`

func openSQLiteLedger(ctx context.Context, path string) (*sqliteLedger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite ledger requires an explicit file path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(filepath.ToSlash(abs), "//") {
		return nil, errors.New("sqlite WAL ledger does not support UNC/network paths")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	// Do not truncate existing data or follow a final-component symlink. New
	// files (and the SQLite sidecars that inherit their mode) are private.
	if info, statErr := os.Lstat(abs); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("sqlite ledger path is not a regular file")
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		file, createErr := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return nil, createErr
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else {
		return nil, statErr
	}
	writer, err := sql.Open(sqliteLedgerDriverName, sqliteLedgerDSN(abs, false))
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	s := &sqliteLedger{writer: writer}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	failed := true
	defer func() {
		if failed {
			s.Close()
		}
	}()
	if err := s.initialize(ctx); err != nil {
		return nil, err
	}
	var journal string
	if err := writer.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return nil, err
	}
	if journal != "wal" {
		return nil, fmt.Errorf("sqlite ledger needs WAL, got %q", journal)
	}
	s.reader, err = sql.Open(sqliteLedgerDriverName, sqliteLedgerDSN(abs, true))
	if err != nil {
		return nil, err
	}
	// Total configured SQLite page caches: writer 2 MiB + two readers 1 MiB
	// each. This is not a claim that all SQLite native memory is <=4 MiB.
	s.reader.SetMaxOpenConns(2)
	s.reader.SetMaxIdleConns(2)
	if err := s.reader.PingContext(ctx); err != nil {
		return nil, err
	}
	failed = false
	return s, nil
}

func sqliteLedgerFileURI(path string, params url.Values) string {
	path = filepath.ToSlash(path)
	// A Windows drive must be part of the path, not the URI authority.
	// Explicit handling also lets the URI contract run on non-Windows CI.
	if len(path) >= 2 && path[1] == ':' {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: params.Encode()}
	return u.String()
}

func (s *sqliteLedger) initialize(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var appID, version, tables int
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return err
	}
	if appID == 0 && version == 0 && tables == 0 {
		if _, err := tx.ExecContext(ctx, sqliteLedgerSchema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=%d", sqliteLedgerApplicationID, sqliteLedgerSchemaVersion)); err != nil {
			return err
		}
	} else if appID == sqliteLedgerApplicationID && version >= 1 && version < sqliteLedgerSchemaVersion {
		// Upgrade additive storage primitives without changing request IDs,
		// revisions, generations or staged migration bytes.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", sqliteLedgerSchemaVersion)); err != nil {
			return err
		}
	} else if appID != sqliteLedgerApplicationID || version != sqliteLedgerSchemaVersion {
		return fmt.Errorf("unsupported sqlite ledger application/schema: %d/%d", appID, version)
	}
	if version < 2 {
		if _, err := tx.ExecContext(ctx, sqliteMigrationStagingSchema); err != nil {
			return err
		}
	}
	if version < 3 {
		if _, err := tx.ExecContext(ctx, sqliteLedgerStateSchema); err != nil {
			return err
		}
	}
	if version < 4 {
		if _, err := tx.ExecContext(ctx, sqliteMigrationProjectionSchema); err != nil {
			return err
		}
	}
	if version == 4 {
		// Schema 4 projections predate prefix verification. Preserve their
		// bytes/nodes, but format 1 checkpoints must be reverified and rebuilt
		// before use by the format 2 projector. Authority is unchanged.
		if _, err := tx.ExecContext(ctx, sqliteMigrationProjectionPrefixSchema); err != nil {
			return err
		}
	}
	if version < 6 {
		if err := initializeSQLiteLedgerValues(ctx, tx); err != nil {
			return err
		}
	}
	if err := validateSQLiteLedgerValues(ctx, tx); err != nil {
		return err
	}
	if version < 7 {
		if _, err := tx.ExecContext(ctx, sqliteMigrationSnapshotSchema); err != nil {
			return err
		}
	}
	if err := validateSQLiteSnapshotSchema(ctx, tx); err != nil {
		return err
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM ledger_meta WHERE singleton=1").Scan(&generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteLedger) Close() error {
	if s == nil {
		return nil
	}
	s.cancel() // Roll back views before DB.Close waits on their connections.
	var readerErr error
	if s.reader != nil {
		readerErr = s.reader.Close()
	}
	return errors.Join(readerErr, s.writer.Close())
}

// Shutdown must synchronously cancel child operations. Making the caller the
// parent and forwarding store shutdown with AfterFunc leaves a scheduling gap
// in which a transaction can still answer queries after Close has returned.
func (s *sqliteLedger) operationContext(parent context.Context, lifetime time.Duration) (context.Context, context.CancelFunc) {
	deadline, hasDeadline := parent.Deadline()
	if lifetime > 0 {
		maximum := time.Now().Add(lifetime)
		if !hasDeadline || maximum.Before(deadline) {
			deadline, hasDeadline = maximum, true
		}
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if hasDeadline {
		ctx, cancel = context.WithDeadline(s.ctx, deadline)
	} else {
		ctx, cancel = context.WithCancel(s.ctx)
	}
	stop := context.AfterFunc(parent, cancel)
	if parent.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

// Apply commits records and an optional migration cursor together. It does not
// deduplicate equal usage content. A caller resuming migration first reads its
// cursor; a stale cursor or record revision aborts the whole batch.
func (s *sqliteLedger) Apply(ctx context.Context, mutations []sqliteLedgerMutation, progress *sqliteLedgerProgress) (ids []int64, err error) {
	return s.ApplyState(ctx, mutations, nil, progress)
}

// ApplyState commits request mutations, derived state and migration progress
// in one generation. State values are opaque here: the runtime coordinator
// owns aggregate/residual/config semantics and must supply version checks.
// Its existing per-record budget remains a contract for bounded callers;
// migration of larger legacy records must explicitly use ApplyLargeState.
func (s *sqliteLedger) ApplyState(ctx context.Context, mutations []sqliteLedgerMutation, states []sqliteLedgerStateMutation, progress *sqliteLedgerProgress) (ids []int64, err error) {
	return s.applyState(ctx, mutations, states, progress, false)
}

// ApplyLargeState permits large legacy values without changing the bounded
// Apply/ApplyState contract. One oversized request may exceed the ordinary
// batch byte budget; its chunks, state and cursor commit in the SAME
// transaction. Multi-record batches remain bounded and must be split by the
// caller. Memory/work still depend on the largest request, not a hard RSS cap.
func (s *sqliteLedger) ApplyLargeState(ctx context.Context, mutations []sqliteLedgerMutation, states []sqliteLedgerStateMutation, progress *sqliteLedgerProgress) (ids []int64, err error) {
	return s.applyState(ctx, mutations, states, progress, true)
}

func (s *sqliteLedger) applyState(ctx context.Context, mutations []sqliteLedgerMutation, states []sqliteLedgerStateMutation, progress *sqliteLedgerProgress, allowLarge bool) (ids []int64, err error) {
	parent := ctx
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	if len(mutations) > sqliteLedgerBatchRecords {
		return nil, errSQLiteLedgerBudget
	}
	stateBytes, err := validateSQLiteLedgerStates(states)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if progress != nil {
		if progress.Source == "" || progress.Fingerprint == "" || progress.ExpectedOffset < 0 || progress.NextOffset < progress.ExpectedOffset || (len(mutations) > 0 && progress.NextOffset == progress.ExpectedOffset) {
			return nil, errors.New("invalid sqlite migration progress")
		}
		if len(progress.Source)+len(progress.Fingerprint) > sqliteLedgerRecordBytes {
			return nil, errSQLiteLedgerBudget
		}
		var fingerprint string
		var offset int64
		err := tx.QueryRowContext(ctx, "SELECT fingerprint,position FROM ledger_progress WHERE source=?", progress.Source).Scan(&fingerprint, &offset)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if (err == nil && fingerprint != progress.Fingerprint) || offset != progress.ExpectedOffset {
			return nil, errSQLiteLedgerConflict
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO ledger_progress VALUES(?,?,?) ON CONFLICT(source) DO UPDATE SET position=excluded.position", progress.Source, progress.Fingerprint, progress.NextOffset); err != nil {
			return nil, err
		}
	}
	if len(mutations) == 0 && len(states) == 0 && progress == nil {
		return nil, nil
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, "UPDATE ledger_meta SET generation=generation+1 WHERE singleton=1 AND generation<9223372036854775807 RETURNING generation").Scan(&revision); err != nil {
		return nil, err
	}
	ids = make([]int64, 0, len(mutations))
	if err := applySQLiteLedgerStates(ctx, tx, revision, states); err != nil {
		return nil, err
	}
	bytes := int64(stateBytes)
	oldIdentities := make(map[int64]struct{})
	oldValues := make(map[int64]struct{})
	for _, mutation := range mutations {
		r := mutation.Record
		if r.ID < 0 || (r.ID == 0 && mutation.Delete) || (r.ID != 0 && r.Revision <= 0) {
			return nil, errors.New("invalid sqlite record mutation")
		}
		if r.ID != 0 {
			var identityID int64
			var valueID sql.NullInt64
			if err := tx.QueryRowContext(ctx, "SELECT identity_id,payload_value FROM ledger_records WHERE id=? AND revision=?", r.ID, r.Revision).Scan(&identityID, &valueID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, errSQLiteLedgerConflict
				}
				return nil, err
			}
			oldIdentities[identityID] = struct{}{}
			if valueID.Valid {
				oldValues[valueID.Int64] = struct{}{}
			}
		}
		if mutation.Delete {
			result, err := tx.ExecContext(ctx, "DELETE FROM ledger_records WHERE id=? AND revision=?", r.ID, r.Revision)
			if err := sqliteRequireChanged(result, err); err != nil {
				return nil, err
			}
			ids = append(ids, r.ID)
			continue
		}
		encodingBudget := int64(sqliteLedgerRecordBytes)
		if allowLarge {
			encodingBudget = 0
		}
		if len(mutations) > 1 {
			remaining := sqliteLedgerBatchBytes - bytes
			if remaining <= 0 {
				return nil, errSQLiteLedgerBudget
			}
			if encodingBudget == 0 || remaining < encodingBudget {
				encodingBudget = remaining
			}
		}
		identity, payload, err := encodeSQLiteLedgerRecord(r, encodingBudget)
		if err != nil {
			return nil, err
		}
		zone, offset := r.Detail.Timestamp.Zone()
		recordBytes, valid := sqliteLedgerCombinedBytes(int64(len(identity)), int64(len(payload)), int64(len(r.API)), int64(len(r.Model)), int64(len(zone)))
		if !valid || (!allowLarge && recordBytes > sqliteLedgerRecordBytes) {
			return nil, errSQLiteLedgerBudget
		}
		bytes, valid = sqliteLedgerCombinedBytes(bytes, recordBytes)
		if !valid || (bytes > sqliteLedgerBatchBytes && !(allowLarge && len(mutations) == 1 && recordBytes > sqliteLedgerBatchBytes)) {
			return nil, errSQLiteLedgerBudget
		}
		identity, identityValue, err := storeSQLiteLedgerValue(ctx, tx, "identity", identity)
		if err != nil {
			return nil, err
		}
		payload, payloadValue, err := storeSQLiteLedgerValue(ctx, tx, "request", payload)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO ledger_identities(payload,payload_value) VALUES(?,?) ON CONFLICT(payload) DO NOTHING", identity, identityValue); err != nil {
			return nil, err
		}
		var identityID int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM ledger_identities WHERE payload=? AND payload_value IS ?", identity, identityValue).Scan(&identityID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errSQLiteLedgerValueCorrupt
			}
			return nil, err
		}
		args := []any{revision, identityID, r.API, r.Model, r.Detail.Timestamp.Unix(), r.Detail.Timestamp.Nanosecond(), zone, offset, r.Detail.Timestamp.IsZero(), r.Archived, payload, payloadValue}
		if r.ID == 0 {
			result, err := tx.ExecContext(ctx, "INSERT INTO ledger_records(revision,identity_id,api,model_group,seconds,nanos,zone,utc_offset,zero_time,archived,payload,payload_value) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", args...)
			if err != nil {
				return nil, err
			}
			r.ID, err = result.LastInsertId()
			if err != nil {
				return nil, err
			}
		} else {
			args = append(args, r.ID, r.Revision)
			result, err := tx.ExecContext(ctx, "UPDATE ledger_records SET revision=?,identity_id=?,api=?,model_group=?,seconds=?,nanos=?,zone=?,utc_offset=?,zero_time=?,archived=?,payload=?,payload_value=? WHERE id=? AND revision=?", args...)
			if err := sqliteRequireChanged(result, err); err != nil {
				return nil, err
			}
		}
		ids = append(ids, r.ID)
	}
	// Only examine identities touched by this bounded batch. A full dictionary
	// sweep on every write would recreate the history-dependent hot path.
	for identityID := range oldIdentities {
		var valueID sql.NullInt64
		err := tx.QueryRowContext(ctx, `DELETE FROM ledger_identities WHERE id=?
 AND NOT EXISTS(SELECT 1 FROM ledger_records WHERE identity_id=?) RETURNING payload_value`, identityID, identityID).Scan(&valueID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil && valueID.Valid {
			oldValues[valueID.Int64] = struct{}{}
		}
	}
	for valueID := range oldValues {
		if err := removeUnusedSQLiteLedgerValue(ctx, tx, valueID); err != nil {
			return nil, err
		}
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func sqliteRequireChanged(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errSQLiteLedgerConflict
	}
	return nil
}

func encodeSQLiteLedgerRecord(r sqliteLedgerRecord, encodingBudget int64) ([]byte, []byte, error) {
	d := r.Detail
	if year := d.Timestamp.Year(); year < 0 || year > 9999 {
		return nil, nil, errors.New("sqlite timestamp is outside the supported JSON year range")
	}
	fields := accountingIdentity{d.Model, d.Provider, d.Source, d.AuthIndex, d.AuthID, d.AuthType, d.APIKey, d.APIKeyHash, d.BaseURL, d.RequestedModel, d.ExecutorType, d.Endpoint}
	if r.Archived {
		// Use exactly the memory ledger's field-retention contract.
		d = (accountingRecord{Identity: &fields, Timestamp: d.Timestamp, Correlation: d.Correlation, Tokens: d.Tokens, LatencyMs: d.LatencyMs, TTFTMs: d.TTFTMs, Failure: d.Failure, StatusCode: d.StatusCode, Failed: d.Failed, Synthetic: d.TimestampSynthetic}).detail()
	}
	// Sort keys stay inline until a lossless oversized-key ordering policy is
	// implemented. Large request/identity fields are not silently truncated.
	zone, _ := d.Timestamp.Zone()
	if len(r.API) > sqliteLedgerRecordBytes || len(r.Model) > sqliteLedgerRecordBytes-len(r.API) || len(zone) > sqliteLedgerRecordBytes-len(r.API)-len(r.Model) {
		return nil, nil, errSQLiteLedgerBudget
	}
	// A multi-record batch also bounds the encoder's input before allocating
	// escaped JSON. A single large record is deliberately allowed through.
	minimum := len(r.API) + len(r.Model) + len(zone) + len(d.Model) + len(d.Provider) + len(d.Source) + len(d.AuthIndex) + len(d.AuthID) + len(d.AuthType) + len(d.APIKey) + len(d.APIKeyHash) + len(d.BaseURL) + len(d.RequestedModel) + len(d.ExecutorType) + len(d.Endpoint) + len(d.Failure)
	minimum += len(d.Thinking.Intensity) + len(d.Thinking.Mode) + len(d.Thinking.Level)
	if d.Correlation != nil {
		minimum += len(d.Correlation.InputMode) + len(d.Correlation.OutputMode) + len(d.Correlation.CacheMode)
	}
	for key, values := range d.Headers {
		minimum += len(key) + 4
		for _, value := range values {
			minimum += len(value) + 2
		}
	}
	if encodingBudget > 0 && int64(minimum) > encodingBudget {
		return nil, nil, errSQLiteLedgerBudget
	}
	identity, err := json.Marshal(sqliteLedgerIdentity{Fields: fields})
	if err != nil {
		return nil, nil, err
	}
	d.Model, d.Provider, d.Source, d.AuthIndex, d.AuthID, d.AuthType = "", "", "", "", "", ""
	d.APIKey, d.APIKeyHash, d.BaseURL, d.RequestedModel, d.ExecutorType, d.Endpoint = "", "", "", "", "", ""
	d.UpstreamAPI, d.CostUSD = "", nil // Query-time values, never persisted prices.
	// Time is stored losslessly as seconds/nanoseconds/offset, not UnixNano
	// (which overflows outside 1678–2262) or RFC3339's minute-only offset.
	d.Timestamp = time.Time{}
	payload, err := json.Marshal(d)
	return identity, payload, err
}

type sqliteLedgerView struct {
	tx         *sql.Tx
	ctx        context.Context
	parent     context.Context
	cancel     context.CancelFunc
	Generation int64
}

func (s *sqliteLedger) ReadView(ctx context.Context) (view *sqliteLedgerView, err error) {
	parent := ctx
	defer func() {
		// Parent deadline and forwarded cancellation may race. Report the
		// caller's actual reason, not a spurious generic context.Canceled.
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	ctx, cancel := s.operationContext(parent, sqliteLedgerReadLifetime)
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		cancel()
		return nil, err
	}
	v := &sqliteLedgerView{tx: tx, ctx: ctx, parent: parent, cancel: cancel}
	// BEGIN alone does not pin a SQLite snapshot. This read pins it now, before
	// a writer can update/delete records between export pages.
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM ledger_meta WHERE singleton=1").Scan(&v.Generation); err != nil {
		v.Close()
		return nil, err
	}
	return v, nil
}

func (v *sqliteLedgerView) Close() error {
	v.cancel()
	err := v.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

func (v *sqliteLedgerView) Progress(source string) (fingerprint string, offset int64, found bool, err error) {
	if err := v.contextError(); err != nil {
		return "", 0, false, err
	}
	err = v.tx.QueryRowContext(v.ctx, "SELECT fingerprint,position FROM ledger_progress WHERE source=?", source).Scan(&fingerprint, &offset)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	return fingerprint, offset, err == nil, err
}

func (v *sqliteLedgerView) contextError() error {
	if err := v.parent.Err(); err != nil {
		v.cancel()
		return err
	}
	return v.ctx.Err()
}

type sqliteLedgerCheckpoint struct {
	Busy                          bool
	LogFrames, CheckpointedFrames int
}

// Checkpoint is not a backup. An active read view may prevent truncation; the
// caller must observe Busy instead of reporting a completed checkpoint.
func (s *sqliteLedger) Checkpoint(ctx context.Context) (sqliteLedgerCheckpoint, error) {
	var result sqliteLedgerCheckpoint
	var busy int
	err := s.writer.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &result.LogFrames, &result.CheckpointedFrames)
	result.Busy = busy != 0
	return result, err
}

type sqliteLedgerQuery struct {
	API, Model        string
	Cutoff            time.Time
	IncludeAccounting bool
}

// Page uses a stable keyset, not OFFSET. The cursor belongs to this read view
// and query; callers must not carry it across generations or filter changes.
func (v *sqliteLedgerView) Page(query sqliteLedgerQuery, after *sqliteLedgerRecord, limit int) ([]sqliteLedgerRecord, error) {
	statement, args, err := sqliteLedgerPageQuery(query, after, limit)
	if err != nil {
		return nil, err
	}
	return v.readPage(statement, args)
}

func sqliteLedgerPageQuery(query sqliteLedgerQuery, after *sqliteLedgerRecord, limit int) (string, []any, error) {
	if limit <= 0 || limit > sqliteLedgerPageRecords {
		return "", nil, errors.New("invalid sqlite ledger page size")
	}
	where := []string{"1=1"}
	args := []any{}
	if !query.IncludeAccounting {
		where = append(where, "r.archived=0")
	}
	if query.API != "" {
		where = append(where, "r.api=?")
		args = append(args, query.API)
	}
	if query.Model != "" {
		where = append(where, "r.model_group=?")
		args = append(args, query.Model)
	}
	if !query.Cutoff.IsZero() {
		where = append(where, "r.zero_time=0 AND (r.sort_seconds,r.sort_nanos)<=(?,?)")
		args = append(args, -query.Cutoff.Unix(), -int64(query.Cutoff.Nanosecond()))
	}
	if after != nil {
		// Negate the descending time keys only; valid time.Time.Unix values fit
		// well within signed int64, including dates beyond UnixNano's range.
		where = append(where, "(r.sort_seconds,r.sort_nanos,r.api,r.model_group,r.id)>(?,?,?,?,?)")
		args = append(args, -after.Detail.Timestamp.Unix(), -int64(after.Detail.Timestamp.Nanosecond()), after.API, after.Model, after.ID)
	}
	args = append(args, limit)
	// CASE guards driver copies, including with the comparison-only pure-Go
	// driver. Large values are only markers here; preflight their logical size
	// before deciding whether the page has room to decode another record.
	return fmt.Sprintf(`SELECT r.id,r.revision,
 CASE WHEN length(CAST(r.api AS BLOB))<=%d THEN r.api ELSE NULL END,
 CASE WHEN length(CAST(r.model_group AS BLOB))<=%d THEN r.model_group ELSE NULL END,
 r.seconds,r.nanos,
 CASE WHEN length(CAST(r.zone AS BLOB))<=%d THEN r.zone ELSE NULL END,
 r.utc_offset,r.zero_time,r.archived,
 CASE WHEN length(CAST(i.payload AS BLOB))<=%d THEN i.payload ELSE NULL END,
 CASE WHEN length(CAST(r.payload AS BLOB))<=%d THEN r.payload ELSE NULL END,
 i.payload_value,iv.size,r.payload_value,rv.size
 FROM ledger_records r LEFT JOIN ledger_identities i ON i.id=r.identity_id
 LEFT JOIN ledger_values iv ON iv.id=i.payload_value
 LEFT JOIN ledger_values rv ON rv.id=r.payload_value WHERE `, sqliteLedgerRecordBytes, sqliteLedgerRecordBytes, sqliteLedgerRecordBytes, sqliteLedgerRecordBytes, sqliteLedgerRecordBytes) + strings.Join(where, " AND ") + ` ORDER BY r.sort_seconds,r.sort_nanos,r.api,r.model_group,r.id LIMIT ?`, args, nil
}

func (v *sqliteLedgerView) readPage(statement string, args []any) ([]sqliteLedgerRecord, error) {
	if err := v.contextError(); err != nil {
		return nil, err
	}
	rows, err := v.tx.QueryContext(v.ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type pendingRecord struct {
		index                       int
		identity, payload           []byte
		identityValue, payloadValue sql.NullInt64
		identitySize, payloadSize   sql.NullInt64
	}
	decode := func(record *sqliteLedgerRecord, next pendingRecord) error {
		stamp := record.Detail.Timestamp
		var id sqliteLedgerIdentity
		if err := decodeSQLiteLedgerValue(v.ctx, v.tx, next.identity, next.identityValue, next.identitySize, "identity", &id); err != nil {
			return err
		}
		if err := decodeSQLiteLedgerValue(v.ctx, v.tx, next.payload, next.payloadValue, next.payloadSize, "request", &record.Detail); err != nil {
			return err
		}
		d, f := &record.Detail, id.Fields
		d.Model, d.Provider, d.Source, d.AuthIndex, d.AuthID, d.AuthType = f.Model, f.Provider, f.Source, f.AuthIndex, f.AuthID, f.AuthType
		d.APIKey, d.APIKeyHash, d.BaseURL, d.RequestedModel, d.ExecutorType, d.Endpoint = f.APIKey, f.APIKeyHash, f.BaseURL, f.RequestedModel, f.ExecutorType, f.Endpoint
		d.Timestamp = stamp
		return nil
	}
	result := make([]sqliteLedgerRecord, 0, args[len(args)-1].(int))
	var pending []pendingRecord
	bytes := int64(0)
	for rows.Next() {
		next := pendingRecord{index: len(result)}
		result = append(result, sqliteLedgerRecord{})
		record := &result[next.index]
		var seconds, nanos int64
		var offset int
		var api, model, zone sql.NullString
		var zero bool
		if err := rows.Scan(&record.ID, &record.Revision, &api, &model, &seconds, &nanos, &zone, &offset, &zero, &record.Archived, &next.identity, &next.payload, &next.identityValue, &next.identitySize, &next.payloadValue, &next.payloadSize); err != nil {
			return nil, err
		}
		if !api.Valid || !model.Valid || !zone.Valid || len(api.String)+len(model.String)+len(zone.String) > sqliteLedgerRecordBytes {
			return nil, errSQLiteLedgerBudget
		}
		identityBytes, err := validateSQLiteLedgerValueReference(next.identity, next.identityValue, next.identitySize)
		if err != nil {
			return nil, err
		}
		payloadBytes, err := validateSQLiteLedgerValueReference(next.payload, next.payloadValue, next.payloadSize)
		if err != nil {
			return nil, err
		}
		recordBytes, valid := sqliteLedgerCombinedBytes(identityBytes, payloadBytes, int64(len(api.String)), int64(len(model.String)), int64(len(zone.String)))
		if !valid {
			return nil, errSQLiteLedgerValueCorrupt
		}
		if next.index > 0 && recordBytes > int64(sqliteLedgerBatchBytes)-bytes {
			result = result[:next.index]
			break // Do not materialize the next large value before paging.
		}
		bytes += recordBytes
		record.API, record.Model = api.String, model.String
		if !zero {
			record.Detail.Timestamp = time.Unix(seconds, nanos).In(time.FixedZone(zone.String, offset))
		}
		if next.identityValue.Valid || next.payloadValue.Valid {
			pending = append(pending, next)
		} else if err := decode(record, next); err != nil {
			// Inline JSON needs no nested SQL query, so do not retain a second
			// page of record headers/raw values for the common small path.
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, next := range pending {
		if err := decode(&result[next.index], next); err != nil {
			return nil, err
		}
	}
	if err := v.contextError(); err != nil {
		return nil, err
	}
	return result, nil
}
