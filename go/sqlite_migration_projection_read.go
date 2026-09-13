package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
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
	NodeID      int64
	Path        []string
	Kind, Scope string
	Value       any
	// Filled only for semantic analysis. This binds ordered, verified source
	// fragments without re-encoding a potentially large request.
	ValueDigest [sha256.Size]byte
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
	s       *sqliteLedger
	ctx     context.Context
	source  sqliteMigrationSource
	chunk   []byte
	chunkAt int64
	// One bounded page of single-fragment leaves. Duplicate-array elements
	// with multiple fragments use the paged merge path instead.
	singleValues map[int64]sqliteProjectionFragment
	valueHasher  hash.Hash
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
	return s.walkMigrationProjection(ctx, path, consume, false)
}

func (s *sqliteLedger) walkMigrationProjection(ctx context.Context, path string, consume func(sqliteMigrationProjectedItem) error, valueDigests bool) (result sqliteMigrationParseResult, err error) {
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
	if !source.Ready || !found || !p.Complete || p.Format != sqliteProjectionFormat {
		return result, errSQLiteProjectionIncomplete
	}
	// Recheck the whole immutable source before exposing values, including
	// bytes in unknown fields and overwritten subtrees. Each indexed fragment
	// is also checked when read, so changed chunks never silently alter data.
	if err := s.verifyProjectionSource(ctx, path); err != nil {
		return result, err
	}
	r := sqliteProjectionReader{s: s, ctx: ctx, source: source, chunkAt: -1}
	if valueDigests {
		r.valueHasher = sha256.New()
	}
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
	emit := func(kind string, value any, digest [sha256.Size]byte) error {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		return consume(sqliteMigrationProjectedItem{NodeID: node.ID, Path: append([]string(nil), path...), Kind: kind, Scope: node.Scope, Value: value, ValueDigest: digest})
	}
	switch node.Kind {
	case "value", "invalid":
		value, digest, err := r.decodeValue(node)
		if err != nil {
			return err
		}
		return emit(node.Kind, value, digest)
	case "null":
		return emit("null", nil, [sha256.Size]byte{})
	case "object", "array":
	default:
		return errors.New("invalid sqlite migration projection node kind")
	}
	if node.Scope != "jsonl" {
		if err := emit(node.Kind, nil, [sha256.Size]byte{}); err != nil {
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
		if err := r.preloadSingleValues(page); err != nil {
			return err
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
	return emit("end", nil, [sha256.Size]byte{})
}

// Avoid one SQL query (and database/sql cancellation goroutine) per record.
// Most leaves have exactly one source fragment. Fetch at most one page plus
// a lookahead row, and cache ONLY groups whose completeness is established.
// Reused slice elements can have arbitrarily many fragments; never accumulate
// such a group just to fill this optimization cache.
func (r *sqliteProjectionReader) preloadSingleValues(edges []sqliteProjectionEdge) error {
	var args []any
	query := "SELECT node,event,start,finish,checksum FROM migration_projection_values WHERE node IN ("
	for _, edge := range edges {
		if edge.Node.Kind != "value" && edge.Node.Kind != "invalid" {
			continue
		}
		if len(args) > 0 {
			query += ","
		}
		query += "?"
		args = append(args, edge.Node.ID)
	}
	if len(args) < 2 {
		return nil
	}
	cache := make(map[int64]sqliteProjectionFragment, len(args))
	query += ") ORDER BY node,event LIMIT ?"
	args = append(args, sqliteProjectionPageNodes+1)
	rows, err := r.s.reader.QueryContext(r.ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var previous int64
	var first sqliteProjectionFragment
	var inGroup, scanned int
	for rows.Next() {
		var node int64
		var fragment sqliteProjectionFragment
		if err := rows.Scan(&node, &fragment.Event, &fragment.Start, &fragment.End, &fragment.Checksum); err != nil {
			return err
		}
		if node != previous {
			if inGroup == 1 {
				cache[previous] = first
			}
			previous, first, inGroup = node, fragment, 0
		}
		if fragment.Event <= 0 || fragment.Start < 0 || fragment.End <= fragment.Start || fragment.End > r.source.Size || len(fragment.Checksum) != sha256.Size {
			return errors.New("invalid sqlite migration projection prefetched fragment")
		}
		inGroup++
		scanned++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if scanned < sqliteProjectionPageNodes+1 && inGroup == 1 {
		cache[previous] = first
	}
	r.singleValues = cache
	return nil
}

func (r *sqliteProjectionReader) children(parent sqliteProjectionNode, after *sqliteProjectionEdge) ([]sqliteProjectionEdge, error) {
	query, args := sqliteProjectionChildrenQuery(parent, after)
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

func sqliteProjectionChildrenQuery(parent sqliteProjectionNode, after *sqliteProjectionEdge) (string, []any) {
	query := `SELECT e.name,e.position,n.source,n.id,n.kind,n.scope,n.length FROM migration_projection_edges e
 JOIN migration_projection_nodes n ON n.id=e.child WHERE e.parent=?`
	args := []any{parent.ID}
	if parent.Kind == "array" {
		position := int64(-1)
		if after != nil {
			position = after.Position
		}
		// Exactly one lower bound. With a partial index and both >=0 and
		// >cursor, SQLite may seek to ZERO and post-filter every prior row on
		// every page, despite EXPLAIN QUERY PLAN reporting an index SEARCH.
		query += " AND e.position>? AND e.position<?"
		args = append(args, position, parent.Length)
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
	return query, args
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

func (r *sqliteProjectionReader) decodeValue(node sqliteProjectionNode) (value any, digest [sha256.Size]byte, err error) {
	if r.valueHasher != nil {
		r.valueHasher.Reset()
	}
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
		return nil, digest, fmt.Errorf("invalid sqlite migration projection value scope %q", node.Scope)
	}
	var last, values int64
	var invalid json.RawMessage
	decode := func(fragment sqliteProjectionFragment) error {
		raw, err := r.readFragment(fragment)
		if err != nil {
			return err
		}
		if node.Kind == "invalid" {
			invalid = append(json.RawMessage(nil), raw...)
		} else if err := json.Unmarshal(raw, target); err != nil {
			return err
		}
		if r.valueHasher != nil {
			var coordinates [24]byte
			binary.BigEndian.PutUint64(coordinates[:8], uint64(fragment.Event))
			binary.BigEndian.PutUint64(coordinates[8:16], uint64(fragment.Start))
			binary.BigEndian.PutUint64(coordinates[16:], uint64(fragment.End))
			_, _ = r.valueHasher.Write(coordinates[:])
			_, _ = r.valueHasher.Write(fragment.Checksum)
		}
		values++
		last = fragment.Event
		return nil
	}
	if fragment, found := r.singleValues[node.ID]; found {
		if err := decode(fragment); err != nil {
			return nil, digest, err
		}
	} else {
		for {
			page, err := r.fragments(node.ID, last)
			if err != nil {
				return nil, digest, err
			}
			for _, fragment := range page {
				if err := decode(fragment); err != nil {
					return nil, digest, err
				}
			}
			if len(page) < sqliteProjectionPageNodes {
				break
			}
		}
	}
	if values == 0 || (node.Kind == "invalid" && values != 1) {
		return nil, digest, errors.New("sqlite migration projection value has invalid fragment count")
	}
	if r.valueHasher != nil {
		r.valueHasher.Sum(digest[:0])
	}
	// Applying successive fragments to ONE typed value also preserves nested
	// struct/map/null semantics in reused slice elements. Hidden old tail
	// elements remain on disk until the slice is explicitly cleared.
	switch value := target.(type) {
	case *RequestDetail:
		return *value, digest, nil
	case *ModelProviderStat:
		return *value, digest, nil
	case *TimeSeriesTokenStat:
		return *value, digest, nil
	case *persistedDetail:
		return *value, digest, nil
	case *int64:
		return *value, digest, nil
	case *float64:
		return *value, digest, nil
	case *int:
		return *value, digest, nil
	case *string:
		return *value, digest, nil
	}
	return invalid, digest, nil
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
	if fragment.Start < 0 || fragment.End > r.source.Size || length <= 0 || int64(int(length)) != length {
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
