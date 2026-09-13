package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const sqliteProjectionPageNodes = 128
const sqliteProjectionPageBytes = 256 << 10

// Values have their legacy Go types: int/int64/float64/string, RequestDetail,
// ModelProviderStat, TimeSeriesTokenStat, or persistedDetail. Invalid JSONL
// rows retain json.RawMessage. No float64 intermediate is used for counters.
// Container/null events distinguish nil maps/slices from empty ones. Consumers
// must stage derived output until the entire walk returns successfully.
type sqliteMigrationProjectedItem struct {
	Path        []string
	Kind, Scope string
	Value       any
}

type sqliteProjectionNode struct {
	ID, Length  int64
	Kind, Scope string
}

type sqliteProjectionEdge struct {
	Name     string
	Position int64
	Node     sqliteProjectionNode
}

type sqliteProjectionFragment struct {
	Event, Start, End int64
	Checksum          []byte
}

type sqliteProjectionReader struct {
	s      *sqliteLedger
	ctx    context.Context
	source sqliteMigrationSource
	chunk  []byte
	chunkAt int64
}

// WalkMigrationProjection reads the effective typed source with keyset pages.
// Neither raw history nor a high-cardinality API/model dictionary is loaded
// into Go memory. Completed projection tables are immutable through this API,
// so the walk uses short queries instead of a long WAL-pinning read view.
// Per level it retains at most a bounded page (plus one oversized map key),
// one staged chunk, and one decoded value with its raw input.
//
// Projection is not recovery: v1 cache conversion, residuals, normalization,
// retention, protocol reconciliation and snapshot/JSONL overlap still belong
// to the semantic migration coordinator. Real identical rows remain distinct.
func (s *sqliteLedger) WalkMigrationProjection(parent context.Context, path string, consume func(sqliteMigrationProjectedItem) error) (result sqliteMigrationParseResult, err error) {
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	if consume == nil {
		return result, errors.New("sqlite migration projection requires a consumer")
	}
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	path, err = canonicalSQLiteMigrationPath(path)
	if err != nil {
		return result, err
	}
	source, err := s.migrationSource(ctx, path)
	if err != nil {
		return result, err
	}
	p, found, err := readSQLiteProjectionCheckpoint(ctx, s.reader, source)
	if err != nil {
		return result, err
	}
	if !source.Ready || !found || !p.Complete {
		return result, errSQLiteProjectionIncomplete
	}
	// Recheck the whole immutable source before exposing values, including
	// bytes in unknown fields and overwritten subtrees. Each indexed fragment
	// is also checked when read, so changed chunks never silently alter data.
	if err := s.verifyProjectionSource(ctx, path); err != nil {
		return result, err
	}
	r := sqliteProjectionReader{s: s, ctx: ctx, source: source, chunkAt: -1}
	var root sqliteProjectionNode
	var owner string
	err = s.reader.QueryRowContext(ctx, "SELECT source,id,kind,scope,length FROM migration_projection_nodes WHERE id=?", p.Root).Scan(&owner, &root.ID, &root.Kind, &root.Scope, &root.Length)
	if err != nil {
		return result, err
	}
	if owner != path || (source.Format == "snapshot" && (root.Kind != "object" || root.Scope != "snapshot")) || (source.Format == "jsonl" && (root.Kind != "array" || root.Scope != "jsonl")) {
		return result, errors.New("invalid sqlite migration projection root")
	}
	if err := r.walk(root, nil, consume); err != nil {
		return result, err
	}
	return p.Result, ctx.Err()
}

func (r *sqliteProjectionReader) walk(node sqliteProjectionNode, path []string, consume func(sqliteMigrationProjectedItem) error) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if len(path) > sqliteProjectionMaxDepth {
		return errors.New("sqlite migration projection depth exceeds schema")
	}
	emit := func(kind string, value any) error {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		return consume(sqliteMigrationProjectedItem{Path: append([]string(nil), path...), Kind: kind, Scope: node.Scope, Value: value})
	}
	switch node.Kind {
	case "value", "invalid":
		value, err := r.decodeValue(node)
		if err != nil {
			return err
		}
		return emit(node.Kind, value)
	case "null":
		return emit("null", nil)
	case "object", "array":
	default:
		return errors.New("invalid sqlite migration projection node kind")
	}
	if node.Scope != "jsonl" {
		if err := emit(node.Kind, nil); err != nil {
			return err
		}
	}
	var after *sqliteProjectionEdge
	var arrayLength int64
	for {
		page, err := r.children(node, after)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, edge := range page {
			if node.Kind == "array" {
				if edge.Position < arrayLength || (node.Scope != "jsonl" && edge.Position != arrayLength) || edge.Name != strconv.FormatInt(edge.Position, 10) {
					return errors.New("invalid sqlite migration projection array edge")
				}
				arrayLength = edge.Position + 1
			}
			childPath := append(append([]string(nil), path...), edge.Name)
			if err := r.walk(edge.Node, childPath, consume); err != nil {
				return err
			}
		}
		last := page[len(page)-1]
		after = &last
	}
	if node.Kind == "array" && arrayLength != node.Length {
		return errors.New("sqlite migration projection array length mismatch")
	}
	if node.Scope == "jsonl" {
		return nil
	}
	return emit("end", nil)
}

func (r *sqliteProjectionReader) children(parent sqliteProjectionNode, after *sqliteProjectionEdge) ([]sqliteProjectionEdge, error) {
	query := `SELECT e.name,e.position,n.source,n.id,n.kind,n.scope,n.length FROM migration_projection_edges e
 JOIN migration_projection_nodes n ON n.id=e.child WHERE e.parent=?`
	args := []any{parent.ID}
	if parent.Kind == "array" {
		query += " AND e.position>=0 AND e.position<?"
		args = append(args, parent.Length)
		if after != nil {
			query += " AND e.position>?"
			args = append(args, after.Position)
		}
		query += " ORDER BY e.position"
	} else {
		if after != nil {
			query += " AND e.name>?"
			args = append(args, []byte(after.Name))
		}
		query += " ORDER BY e.name"
	}
	query += " LIMIT ?"
	args = append(args, sqliteProjectionPageNodes)
	rows, err := r.s.reader.QueryContext(r.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := make([]sqliteProjectionEdge, 0, sqliteProjectionPageNodes)
	nameBytes := 0
	for rows.Next() {
		var edge sqliteProjectionEdge
		var source string
		if err := rows.Scan(&edge.Name, &edge.Position, &source, &edge.Node.ID, &edge.Node.Kind, &edge.Node.Scope, &edge.Node.Length); err != nil {
			return nil, err
		}
		if source != r.source.Source || edge.Node.ID <= 0 || edge.Node.Length < 0 || (parent.Kind == "object" && edge.Position != -1) {
			return nil, errors.New("invalid sqlite migration projection edge")
		}
		if len(page) > 0 && len(edge.Name) > sqliteProjectionPageBytes-nameBytes {
			break // Resume after the last returned edge, not this unread one.
		}
		nameBytes += len(edge.Name)
		page = append(page, edge)
	}
	return page, rows.Err()
}

func (r *sqliteProjectionReader) fragments(node, after int64) ([]sqliteProjectionFragment, error) {
	rows, err := r.s.reader.QueryContext(r.ctx, `SELECT event,start,finish,checksum FROM migration_projection_values
 WHERE node=? AND event>? ORDER BY event LIMIT ?`, node, after, sqliteProjectionPageNodes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []sqliteProjectionFragment
	for rows.Next() {
		var fragment sqliteProjectionFragment
		if err := rows.Scan(&fragment.Event, &fragment.Start, &fragment.End, &fragment.Checksum); err != nil {
			return nil, err
		}
		if fragment.Event <= after || fragment.Start < 0 || fragment.End <= fragment.Start || fragment.End > r.source.Size || len(fragment.Checksum) != sha256.Size {
			return nil, errors.New("invalid sqlite migration projection fragment")
		}
		result = append(result, fragment)
		after = fragment.Event
	}
	return result, rows.Err()
}

func (r *sqliteProjectionReader) decodeValue(node sqliteProjectionNode) (any, error) {
	var target any
	switch node.Scope {
	case "detail":
		target = new(RequestDetail)
	case "provider":
		target = new(ModelProviderStat)
	case "tokens":
		target = new(TimeSeriesTokenStat)
	case "request", "metadata":
		target = new(persistedDetail)
	case "count":
		target = new(int64)
	case "float":
		target = new(float64)
	case "version":
		target = new(int)
	case "generated_at":
		target = new(string)
	case "invalid":
	default:
		return nil, fmt.Errorf("invalid sqlite migration projection value scope %q", node.Scope)
	}
	var last, values int64
	var invalid json.RawMessage
	for {
		page, err := r.fragments(node.ID, last)
		if err != nil {
			return nil, err
		}
		for _, fragment := range page {
			raw, err := r.readFragment(fragment)
			if err != nil {
				return nil, err
			}
			if node.Kind == "invalid" {
				invalid = append(json.RawMessage(nil), raw...)
			} else if err := json.Unmarshal(raw, target); err != nil {
				return nil, err
			}
			values++
			last = fragment.Event
		}
		if len(page) < sqliteProjectionPageNodes {
			break
		}
	}
	if values == 0 || (node.Kind == "invalid" && values != 1) {
		return nil, errors.New("sqlite migration projection value has invalid fragment count")
	}
	// Applying successive fragments to ONE typed value also preserves nested
	// struct/map/null semantics in reused slice elements. Hidden old tail
	// elements remain on disk until the slice is explicitly cleared.
	switch value := target.(type) {
	case *RequestDetail:
		return *value, nil
	case *ModelProviderStat:
		return *value, nil
	case *TimeSeriesTokenStat:
		return *value, nil
	case *persistedDetail:
		return *value, nil
	case *int64:
		return *value, nil
	case *float64:
		return *value, nil
	case *int:
		return *value, nil
	case *string:
		return *value, nil
	}
	return invalid, nil
}

func (r *sqliteProjectionReader) stagedChunk(position int64) ([]byte, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if r.chunkAt == position {
		return r.chunk, nil
	}
	var raw []byte
	if err := r.s.reader.QueryRowContext(r.ctx, "SELECT payload FROM migration_chunks WHERE source=? AND position=?", r.source.Source, position).Scan(&raw); err != nil {
		return nil, err
	}
	if int64(len(raw)) != min(int64(sqliteMigrationChunkBytes), r.source.Size-position) {
		return nil, errors.New("sqlite migration projection staged chunk size mismatch")
	}
	r.chunk, r.chunkAt = raw, position
	return raw, nil
}

func (r *sqliteProjectionReader) readFragment(fragment sqliteProjectionFragment) ([]byte, error) {
	length := fragment.End - fragment.Start
	if length <= 0 || int64(int(length)) != length {
		return nil, errors.New("sqlite migration projection value size is not representable")
	}
	startChunk := fragment.Start / sqliteMigrationChunkBytes * sqliteMigrationChunkBytes
	chunk, err := r.stagedChunk(startChunk)
	if err != nil {
		return nil, err
	}
	within := int(fragment.Start - startChunk)
	var raw []byte
	if int(length) <= len(chunk)-within {
		raw = chunk[within : within+int(length)]
	} else {
		raw = make([]byte, int(length))
		copied := copy(raw, chunk[within:])
		for copied < len(raw) {
			startChunk += sqliteMigrationChunkBytes
			chunk, err = r.stagedChunk(startChunk)
			if err != nil {
				return nil, err
			}
			copied += copy(raw[copied:], chunk)
		}
	}
	checksum := sha256.Sum256(raw)
	if !bytes.Equal(checksum[:], fragment.Checksum) {
		return nil, errors.New("sqlite migration projection fragment checksum mismatch")
	}
	return raw, nil
}
