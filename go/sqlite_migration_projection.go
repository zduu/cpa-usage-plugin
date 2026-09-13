package main

// A disk-backed, typed projection of a sealed legacy source. This resolves
// encoding/json's duplicate-member semantics, but does NOT reconcile snapshot
// residuals with JSONL or publish requests to the authoritative ledger.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	sqliteProjectionFormat      = 1
	sqliteProjectionBatchEvents = 256
	sqliteProjectionBatchBytes  = 2 << 20
	sqliteProjectionMaxDepth    = 8
)

var errSQLiteProjectionIncomplete = errors.New("sqlite migration projection is not complete")

const sqliteMigrationProjectionSchema = `
CREATE TABLE migration_projection_nodes (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 source TEXT NOT NULL REFERENCES migration_sources(source),
 kind TEXT NOT NULL CHECK(kind IN ('object','array','value','null','invalid')),
 scope TEXT NOT NULL, length INTEGER NOT NULL DEFAULT 0 CHECK(length>=0)
);
CREATE TABLE migration_projections (
 source TEXT PRIMARY KEY REFERENCES migration_sources(source),
 format INTEGER NOT NULL, fingerprint TEXT NOT NULL,
 root INTEGER NOT NULL REFERENCES migration_projection_nodes(id),
 events INTEGER NOT NULL CHECK(events>=0),
 stack BLOB NOT NULL CHECK(length(stack)<=4096),
 complete INTEGER NOT NULL CHECK(complete IN (0,1)),
 result BLOB NOT NULL CHECK(length(result)<=4096)
) WITHOUT ROWID;
CREATE TABLE migration_projection_edges (
 parent INTEGER NOT NULL REFERENCES migration_projection_nodes(id),
 name BLOB NOT NULL, position INTEGER NOT NULL CHECK(position>=-1),
 child INTEGER NOT NULL REFERENCES migration_projection_nodes(id),
 PRIMARY KEY(parent,name)
) WITHOUT ROWID;
CREATE INDEX migration_projection_array ON migration_projection_edges(parent,position) WHERE position>=0;
CREATE TABLE migration_projection_values (
 node INTEGER NOT NULL REFERENCES migration_projection_nodes(id),
 event INTEGER NOT NULL CHECK(event>0),
 start INTEGER NOT NULL CHECK(start>=0), finish INTEGER NOT NULL CHECK(finish>start),
 checksum BLOB NOT NULL CHECK(length(checksum)=32),
 PRIMARY KEY(node,event)
) WITHOUT ROWID;
`

type sqliteMigrationProjection struct {
	Source, Fingerprint string
	Root, Events        int64
	Complete            bool // Only typed projection, never migration activation.
	Result              sqliteMigrationParseResult
}

type sqliteProjectionFrame struct {
	ID          int64
	Kind, Scope string
	Length      int64
	Reuse       bool
}

type sqliteProjectionCheckpoint struct {
	sqliteMigrationProjection
	Format int
	Stack  []sqliteProjectionFrame
}

type sqliteProjectionQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readSQLiteProjectionCheckpoint(ctx context.Context, db sqliteProjectionQueryRower, source sqliteMigrationSource) (p sqliteProjectionCheckpoint, found bool, err error) {
	p.Source = source.Source
	var stack, result []byte
	err = db.QueryRowContext(ctx, `SELECT format,fingerprint,root,events,stack,complete,result
 FROM migration_projections WHERE source=?`, source.Source).Scan(&p.Format, &p.Fingerprint, &p.Root, &p.Events, &stack, &p.Complete, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if p.Format != sqliteProjectionFormat || p.Fingerprint != source.Fingerprint || p.Root <= 0 || p.Events < 0 || p.Events > source.Size {
		return p, false, errSQLiteLedgerConflict
	}
	if len(stack) > 4096 || len(result) > 4096 {
		return p, false, errors.New("sqlite migration projection checkpoint exceeds limit")
	}
	if err = json.Unmarshal(stack, &p.Stack); err != nil {
		return p, false, err
	}
	if err = json.Unmarshal(result, &p.Result); err != nil {
		return p, false, err
	}
	if len(p.Stack) > sqliteProjectionMaxDepth {
		return p, false, errors.New("invalid sqlite migration projection stack")
	}
	for _, frame := range p.Stack {
		if frame.ID <= 0 || (frame.Kind != "array" && frame.Kind != "object") || frame.Length < 0 || frame.Length > source.Size+1 {
			return p, false, errors.New("invalid sqlite migration projection frame")
		}
	}
	return p, true, nil
}

func (s *sqliteLedger) initializeMigrationProjection(ctx context.Context, source sqliteMigrationSource) (p sqliteProjectionCheckpoint, err error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var found bool
	p, found, err = readSQLiteProjectionCheckpoint(ctx, tx, source)
	if err != nil || found {
		return p, err
	}
	kind, scope := "object", "snapshot"
	if source.Format == "jsonl" {
		kind, scope = "array", "jsonl"
	}
	row, err := tx.ExecContext(ctx, "INSERT INTO migration_projection_nodes(source,kind,scope) VALUES(?,?,?)", source.Source, kind, scope)
	if err != nil {
		return p, err
	}
	p.Root, err = row.LastInsertId()
	if err != nil {
		return p, err
	}
	p.Format, p.Fingerprint = sqliteProjectionFormat, source.Fingerprint
	if source.Format == "jsonl" {
		p.Stack = []sqliteProjectionFrame{{ID: p.Root, Kind: kind, Scope: scope}}
	}
	stack, err := json.Marshal(p.Stack)
	if err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO migration_projections VALUES(?,?,?,?,0,?,0,?)", source.Source, p.Format, source.Fingerprint, p.Root, stack, []byte(`{}`)); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// ProjectMigrationSource stores only nodes, edges and checked source spans,
// not a second copy of every raw record. At most one record/scalar is decoded
// at once. Large legacy records are not subject to Apply's 1 MiB record limit.
// Memory still depends on the largest individual value or map key.
//
// Checkpoint, container stack and projection changes commit together. A retry
// revalidates/reparses the prefix, skips already committed events, and resumes
// from the saved stack. It does not replay committed mutations. Replacement
// subtrees remain unreachable on disk: removing a million-element map/slice
// must not turn one bounded batch into a million-row deletion transaction.
func (s *sqliteLedger) ProjectMigrationSource(parent context.Context, path string) (projection sqliteMigrationProjection, err error) {
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	path, err = canonicalSQLiteMigrationPath(path)
	if err != nil {
		return projection, err
	}
	source, err := s.migrationSource(ctx, path)
	if err != nil {
		return projection, err
	}
	if !source.Ready {
		return projection, errors.New("sqlite migration source is not verified")
	}
	p, err := s.initializeMigrationProjection(ctx, source)
	if err != nil {
		return projection, err
	}
	if p.Complete {
		return p.sqliteMigrationProjection, s.verifyProjectionSource(ctx, path)
	}
	b := sqliteProjectionBuilder{s: s, ctx: ctx, source: source, checkpoint: p, events: p.Events, frames: append([]sqliteProjectionFrame(nil), p.Stack...)}
	defer b.rollback()
	var scanned int64
	result, err := s.ScanMigrationSource(ctx, path, func(item sqliteMigrationItem) error {
		scanned++
		if scanned <= p.Events {
			return nil
		}
		if err := b.apply(item); err != nil {
			return err
		}
		b.events++
		b.pending++
		// Raw values are NOT retained in the batch. This additional threshold
		// bounds work between commits; a single oversized value is still kept.
		b.inputBytes += int64(len(item.Value))
		if len(item.Path) > 0 {
			b.inputBytes += int64(len(item.Path[len(item.Path)-1]))
		}
		if b.pending >= sqliteProjectionBatchEvents || b.inputBytes >= sqliteProjectionBatchBytes {
			return b.commit(nil)
		}
		return nil
	})
	if err != nil {
		return b.checkpoint.sqliteMigrationProjection, err
	}
	if scanned < p.Events || (source.Format == "snapshot" && len(b.frames) != 0) || (source.Format == "jsonl" && len(b.frames) != 1) {
		return b.checkpoint.sqliteMigrationProjection, errors.New("sqlite migration projection cursor/stack does not match source")
	}
	// ScanMigrationSource has now checked EOF, the staged SHA-256 and the
	// effective snapshot header. Until this final commit readers see no data.
	if err := b.commit(&result); err != nil {
		return b.checkpoint.sqliteMigrationProjection, err
	}
	return b.checkpoint.sqliteMigrationProjection, nil
}

func (s *sqliteLedger) verifyProjectionSource(ctx context.Context, path string) error {
	return s.WithStagedSource(ctx, path, func(reader io.Reader) error {
		_, err := io.Copy(io.Discard, reader) // Reading EOF verifies the full staged checksum.
		return err
	})
}

type sqliteProjectionBuilder struct {
	s          *sqliteLedger
	ctx        context.Context
	source     sqliteMigrationSource
	checkpoint sqliteProjectionCheckpoint
	frames     []sqliteProjectionFrame
	events     int64
	pending    int
	inputBytes int64
	tx         *sql.Tx
	insertNode, putEdge, putValue, getChild, setLength *sql.Stmt
}

func (b *sqliteProjectionBuilder) begin() error {
	if b.tx != nil {
		return nil
	}
	var err error
	b.tx, err = b.s.writer.BeginTx(b.ctx, nil)
	if err != nil {
		return err
	}
	// Acquire the writer and reject a stale checkpoint before making changes.
	changed, err := b.tx.ExecContext(b.ctx, `UPDATE migration_projections SET events=events
 WHERE source=? AND format=? AND fingerprint=? AND root=? AND events=? AND complete=0`, b.source.Source, sqliteProjectionFormat, b.source.Fingerprint, b.checkpoint.Root, b.checkpoint.Events)
	if err := sqliteRequireChanged(changed, err); err != nil {
		return err
	}
	for _, prepared := range []struct {
		target **sql.Stmt
		query  string
	}{
		{&b.insertNode, "INSERT INTO migration_projection_nodes(source,kind,scope) VALUES(?,?,?)"},
		{&b.putEdge, "INSERT INTO migration_projection_edges VALUES(?,?,?,?) ON CONFLICT(parent,name) DO UPDATE SET child=excluded.child,position=excluded.position"},
		{&b.putValue, "INSERT INTO migration_projection_values VALUES(?,?,?,?,?)"},
		{&b.getChild, "SELECT n.id,n.kind,n.scope FROM migration_projection_edges e JOIN migration_projection_nodes n ON n.id=e.child WHERE e.parent=? AND e.name=?"},
		{&b.setLength, "UPDATE migration_projection_nodes SET length=? WHERE id=?"},
	} {
		*prepared.target, err = b.tx.PrepareContext(b.ctx, prepared.query)
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *sqliteProjectionBuilder) rollback() {
	if b.tx != nil {
		_ = b.tx.Rollback()
		b.tx = nil
	}
}

func (b *sqliteProjectionBuilder) commit(result *sqliteMigrationParseResult) error {
	if err := b.begin(); err != nil {
		return err
	}
	if result != nil && b.source.Format == "jsonl" {
		if _, err := b.setLength.ExecContext(b.ctx, b.frames[0].Length, b.checkpoint.Root); err != nil {
			return err
		}
	}
	stack, err := json.Marshal(b.frames)
	if err != nil {
		return err
	}
	parsed := b.checkpoint.Result
	if result != nil {
		parsed = *result
	}
	raw, err := json.Marshal(parsed)
	if err != nil {
		return err
	}
	changed, err := b.tx.ExecContext(b.ctx, `UPDATE migration_projections SET events=?,stack=?,complete=?,result=?
 WHERE source=? AND events=? AND complete=0`, b.events, stack, result != nil, raw, b.source.Source, b.checkpoint.Events)
	if err := sqliteRequireChanged(changed, err); err != nil {
		return err
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.tx.Commit(); err != nil {
		return err
	}
	b.tx = nil
	b.checkpoint.Events, b.checkpoint.Result = b.events, parsed
	b.checkpoint.Stack = append(b.checkpoint.Stack[:0], b.frames...)
	b.checkpoint.Complete = result != nil
	b.pending, b.inputBytes = 0, 0
	return nil
}

func (b *sqliteProjectionBuilder) newNode(kind, scope string) (int64, error) {
	result, err := b.insertNode.ExecContext(b.ctx, b.source.Source, kind, scope)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (b *sqliteProjectionBuilder) child(parent int64, name string) (frame sqliteProjectionFrame, found bool, err error) {
	err = b.getChild.QueryRowContext(b.ctx, parent, []byte(name)).Scan(&frame.ID, &frame.Kind, &frame.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return frame, false, nil
	}
	return frame, err == nil, err
}

func (b *sqliteProjectionBuilder) edge(parent int64, name string, position, child int64) error {
	_, err := b.putEdge.ExecContext(b.ctx, parent, []byte(name), position, child)
	return err
}

func (b *sqliteProjectionBuilder) apply(item sqliteMigrationItem) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.begin(); err != nil {
		return err
	}
	if item.Start < 0 || item.End < item.Start || item.End > b.source.Size || len(item.Path) > sqliteProjectionMaxDepth {
		return errors.New("invalid sqlite migration projection event")
	}
	if item.Kind == "end" {
		if len(b.frames) != len(item.Path)+1 {
			return errors.New("unbalanced sqlite migration projection end")
		}
		last := b.frames[len(b.frames)-1]
		if last.Scope != item.Scope {
			return errors.New("mismatched sqlite migration projection scope")
		}
		if last.Kind == "array" {
			if last.Length == 0 && last.Reuse {
				// encoding/json's empty [] discards the slice backing array. A
				// later duplicate must not resurrect elements hidden by a prior
				// truncation. Detach it in O(1), preserving bounded transactions.
				id, err := b.newNode("array", last.Scope)
				if err != nil {
					return err
				}
				parent := b.frames[len(b.frames)-2]
				if err := b.edge(parent.ID, item.Path[len(item.Path)-1], -1, id); err != nil {
					return err
				}
			} else if _, err := b.setLength.ExecContext(b.ctx, last.Length, last.ID); err != nil {
				return err
			}
		}
		b.frames = b.frames[:len(b.frames)-1]
		return nil
	}
	if len(b.frames) != len(item.Path) {
		return errors.New("unbalanced sqlite migration projection path")
	}
	if len(item.Path) == 0 {
		if item.Kind == "null" {
			return nil // null into the non-pointer snapshot struct is a no-op.
		}
		if item.Kind != "object" || item.Scope != "snapshot" {
			return errors.New("invalid sqlite migration projection root")
		}
		b.frames = append(b.frames, sqliteProjectionFrame{ID: b.checkpoint.Root, Kind: "object", Scope: "snapshot"})
		return nil
	}
	parent := &b.frames[len(b.frames)-1]
	name := item.Path[len(item.Path)-1]
	position := int64(-1)
	if parent.Kind == "array" {
		var err error
		position, err = strconv.ParseInt(name, 10, 64)
		if err != nil || position < 0 || position < parent.Length || (parent.Scope != "jsonl" && position != parent.Length) {
			return errors.New("invalid sqlite migration projection array index")
		}
		parent.Length = position + 1
	}
	if item.Kind == "null" && item.Scope == "usage" {
		return nil // usage is also a non-pointer struct, not a resettable map.
	}
	if item.Kind == "value" && parent.Kind == "object" && (parent.Scope == "snapshot" || parent.Scope == "usage" || parent.Scope == "api" || parent.Scope == "model") && bytes.Equal(bytes.TrimSpace(item.Value), []byte("null")) {
		return nil // A scalar struct field's null preserves its previous value.
	}
	var node sqliteProjectionFrame
	reuse := item.Kind == "object" && (item.Scope == "usage" || item.Scope == "apis" || item.Scope == "models" || strings.HasPrefix(item.Scope, "map:"))
	reuse = reuse || (item.Kind == "array" && parent.Scope == "model") || (item.Kind == "value" && parent.Kind == "array" && parent.Reuse)
	if reuse {
		previous, found, err := b.child(parent.ID, name)
		if err != nil {
			return err
		}
		if found && previous.Kind == item.Kind && previous.Scope == item.Scope {
			node = previous
			node.Reuse = true
		}
	}
	if node.ID == 0 {
		var err error
		node.ID, err = b.newNode(item.Kind, item.Scope)
		if err != nil {
			return err
		}
		node.Kind, node.Scope = item.Kind, item.Scope
		if err := b.edge(parent.ID, name, position, node.ID); err != nil {
			return err
		}
	}
	switch item.Kind {
	case "object", "array":
		b.frames = append(b.frames, node)
	case "value", "invalid":
		if item.End-item.Start != int64(len(item.Value)) || len(item.Value) == 0 {
			return errors.New("invalid sqlite migration projection value span")
		}
		checksum := sha256.Sum256(item.Value)
		_, err := b.putValue.ExecContext(b.ctx, node.ID, b.events+1, item.Start, item.End, checksum[:])
		return err
	case "null":
	default:
		return fmt.Errorf("unknown sqlite migration projection kind %q", item.Kind)
	}
	return nil
}
