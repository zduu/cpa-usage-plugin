package main

// Resumable, disk-backed snapshot accounting analysis. This is a catalog for
// the semantic coordinator, NOT a completed migration or an activation flag.
// Protocol pairing, destination-group materialization, time series, JSONL
// overlap, retention and publication remain separate phases. The source and
// authoritative ledger are never modified by this analysis.

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
	"strconv"
	"strings"
	"time"
)

const sqliteSnapshotCatalogFormat = 1

var errSQLiteSnapshotCatalogIncomplete = errors.New("sqlite snapshot accounting catalog is not complete")
var errSQLiteSnapshotCatalogCorrupt = errors.New("sqlite snapshot accounting catalog is inconsistent or corrupt")

const sqliteMigrationSnapshotSchema = `
CREATE TABLE IF NOT EXISTS migration_snapshot_catalogs (
 source TEXT PRIMARY KEY REFERENCES migration_sources(source),
 format INTEGER NOT NULL, fingerprint TEXT NOT NULL,
 root INTEGER NOT NULL REFERENCES migration_projection_nodes(id),
 version INTEGER NOT NULL,
 clock BLOB NOT NULL CHECK(length(CAST(clock AS BLOB))<=4096),
 events INTEGER NOT NULL CHECK(events>=0),
 prefix BLOB NOT NULL CHECK(length(CAST(prefix AS BLOB))=32),
 complete INTEGER NOT NULL CHECK(complete IN (0,1))
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS migration_snapshot_groups (
 source TEXT NOT NULL REFERENCES migration_snapshot_catalogs(source),
 node INTEGER NOT NULL REFERENCES migration_projection_nodes(id),
 parent INTEGER, scope TEXT NOT NULL CHECK(scope IN ('usage','api','model')),
 payload BLOB NOT NULL CHECK(length(CAST(payload AS BLOB))<=1048576),
 checksum BLOB NOT NULL CHECK(length(CAST(checksum AS BLOB))=32),
 PRIMARY KEY(source,node),
 FOREIGN KEY(source,parent) REFERENCES migration_snapshot_groups(source,node)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS migration_snapshot_providers (
 source TEXT NOT NULL, node INTEGER NOT NULL,
 name BLOB NOT NULL CHECK(length(CAST(name AS BLOB))<=1048576),
 payload BLOB NOT NULL CHECK(length(CAST(payload AS BLOB))<=1048576),
 checksum BLOB NOT NULL CHECK(length(CAST(checksum AS BLOB))=32),
 PRIMARY KEY(source,node,name),
 FOREIGN KEY(source,node) REFERENCES migration_snapshot_groups(source,node)
) WITHOUT ROWID;
`

func validateSQLiteSnapshotSchema(ctx context.Context, tx *sql.Tx) error {
	canonical := func(definition string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(definition, "IF NOT EXISTS ", "")), " ")
	}
	for _, definition := range strings.Split(sqliteMigrationSnapshotSchema, ";") {
		want := canonical(definition)
		if want == "" {
			continue
		}
		name := strings.Fields(want)[2]
		var actual sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(sql AS BLOB))<=16384 THEN sql ELSE NULL END
 FROM sqlite_schema WHERE name=? AND type='table'`, name).Scan(&actual)
		if err != nil {
			return err
		}
		if !actual.Valid || canonical(actual.String) != want {
			return fmt.Errorf("incompatible sqlite snapshot catalog schema: %s", name)
		}
	}
	return nil
}

type sqliteSnapshotClock struct {
	Seconds int64
	Nanos   int
	Zone    string
	Offset  int
}

func snapshotCatalogClock(now time.Time) ([]byte, error) {
	if now.IsZero() || now.Year() < 0 || now.Year() > 9999 {
		return nil, errors.New("sqlite snapshot analysis needs an explicit, JSON-compatible recovery time")
	}
	zone, offset := now.Zone()
	if len(zone) > 2048 {
		return nil, errSQLiteLedgerBudget
	}
	value, err := json.Marshal(sqliteSnapshotClock{now.Unix(), now.Nanosecond(), zone, offset})
	if err == nil && len(value) > 4096 {
		err = errSQLiteLedgerBudget
	}
	return value, err
}

type sqliteMigrationSnapshotCatalog struct {
	Source, Fingerprint string
	Root, Events        int64
	Version             int
	Now                 time.Time
	Complete            bool // Accounting scan only; NEVER backend activation.
	clock, prefix       []byte
}

func readSQLiteSnapshotCatalog(ctx context.Context, db sqliteProjectionQueryRower, source string) (c sqliteMigrationSnapshotCatalog, err error) {
	c.Source = source
	var format int
	err = db.QueryRowContext(ctx, `SELECT format,fingerprint,root,version,
 CASE WHEN length(CAST(clock AS BLOB))<=4096 THEN clock ELSE NULL END,events,
 CASE WHEN length(CAST(prefix AS BLOB))=32 THEN prefix ELSE NULL END,complete
 FROM migration_snapshot_catalogs WHERE source=?`, source).Scan(&format, &c.Fingerprint, &c.Root, &c.Version, &c.clock, &c.Events, &c.prefix, &c.Complete)
	if err != nil {
		return c, err
	}
	var clock sqliteSnapshotClock
	if format != sqliteSnapshotCatalogFormat || c.Root <= 0 || c.Events < 0 || c.Version < 0 || c.Version > currentStorageSnapshotVersion ||
		len(c.prefix) != sha256.Size || len(c.clock) == 0 || json.Unmarshal(c.clock, &clock) != nil || clock.Nanos < 0 || clock.Nanos >= 1e9 {
		return c, errSQLiteSnapshotCatalogCorrupt
	}
	if c.Events == 0 && (c.Complete || !bytes.Equal(c.prefix, sha256.New().Sum(nil))) {
		return c, errSQLiteSnapshotCatalogCorrupt
	}
	c.Now = time.Unix(clock.Seconds, int64(clock.Nanos)).In(time.FixedZone(clock.Zone, clock.Offset))
	encoded, err := snapshotCatalogClock(c.Now)
	if err != nil || !bytes.Equal(encoded, c.clock) {
		return c, errSQLiteSnapshotCatalogCorrupt
	}
	return c, nil
}

func (s *sqliteLedger) snapshotCatalogSource(ctx context.Context, path string) (source sqliteMigrationSource, projection sqliteProjectionCheckpoint, err error) {
	path, err = canonicalSQLiteMigrationPath(path)
	if err != nil {
		return source, projection, err
	}
	source, err = s.migrationSource(ctx, path)
	if err != nil {
		return source, projection, err
	}
	if source.Format != "snapshot" {
		return source, projection, errors.New("sqlite snapshot analysis cannot consume JSONL")
	}
	var found bool
	projection, found, err = readSQLiteProjectionCheckpoint(ctx, s.reader, source)
	if err == nil && (!source.Ready || !found || !projection.Complete || projection.Format != sqliteProjectionFormat) {
		err = errSQLiteProjectionIncomplete
	}
	return source, projection, err
}

func sqliteSnapshotCatalogMatches(c sqliteMigrationSnapshotCatalog, p sqliteProjectionCheckpoint, clock []byte) bool {
	return c.Root == p.Root && c.Fingerprint == p.Fingerprint && c.Version == p.Result.Version && bytes.Equal(c.clock, clock)
}

func (s *sqliteLedger) initializeSnapshotCatalog(ctx context.Context, source sqliteMigrationSource, p sqliteProjectionCheckpoint, now time.Time) (c sqliteMigrationSnapshotCatalog, err error) {
	clock, err := snapshotCatalogClock(now)
	if err != nil {
		return c, err
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	c, err = readSQLiteSnapshotCatalog(ctx, tx, source.Source)
	if err == nil {
		if !sqliteSnapshotCatalogMatches(c, p, clock) {
			return c, errSQLiteLedgerConflict
		}
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	c = sqliteMigrationSnapshotCatalog{Source: source.Source, Fingerprint: source.Fingerprint, Root: p.Root,
		Version: p.Result.Version, Now: now, clock: clock, prefix: sha256.New().Sum(nil)}
	if err := checkSnapshotCatalogProjection(ctx, tx, source, c); err != nil {
		return c, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO migration_snapshot_catalogs VALUES(?,?,?,?,?,?,0,?,0)`,
		c.Source, sqliteSnapshotCatalogFormat, c.Fingerprint, c.Root, c.Version, c.clock, c.prefix); err != nil {
		return c, err
	}
	group := sqliteMigrationSnapshotGroup{Node: c.Root, Scope: "usage", CatalogGroups: 1}
	if err := insertSnapshotCatalogGroup(ctx, tx, c.Source, &group); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func checkSnapshotCatalogProjection(ctx context.Context, tx *sql.Tx, source sqliteMigrationSource, c sqliteMigrationSnapshotCatalog) error {
	p, found, err := readSQLiteProjectionCheckpoint(ctx, tx, source)
	if err != nil {
		return err
	}
	if !found || !p.Complete || p.Format != sqliteProjectionFormat || !sqliteSnapshotCatalogMatches(c, p, c.clock) {
		return errSQLiteLedgerConflict
	}
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT ready=1 AND format='snapshot' AND fingerprint=? AND size=?
 FROM migration_sources WHERE source=?`, source.Fingerprint, source.Size, source.Source).Scan(&matches)
	if err == nil && !matches {
		err = errSQLiteLedgerConflict
	}
	return err
}

// CatalogMigrationSnapshot consumes one detail/provider at a time. Group and
// provider dictionaries live on disk; a batch caches at most its bounded work
// set. A retry validates the committed effective-projection prefix BEFORE
// adding anything new, and commits totals/providers/cursor together.
//
// Names/state still have a 1 MiB prototype budget, and the reader retains one
// potentially large detail. Neither limit is a completed migration policy.
// The catalog intentionally does not hash client keys or seed authIndexes.
func (s *sqliteLedger) CatalogMigrationSnapshot(parent context.Context, path string, now time.Time) (catalog sqliteMigrationSnapshotCatalog, err error) {
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	source, projection, err := s.snapshotCatalogSource(ctx, path)
	if err != nil {
		return catalog, err
	}
	// A damaged source must not even initialize a new derived catalog.
	if err := s.verifyProjectionSource(ctx, source.Source); err != nil {
		return catalog, err
	}
	catalog, err = s.initializeSnapshotCatalog(ctx, source, projection, now)
	if err != nil {
		return catalog, err
	}
	b := sqliteSnapshotCatalogBuilder{s: s, ctx: ctx, source: source, catalog: catalog, prefix: sha256.New()}
	defer b.rollback()
	_, err = s.walkMigrationProjection(ctx, source.Source, b.consume, true)
	if err == nil && b.events != b.catalog.Events && b.catalog.Complete {
		err = errSQLiteLedgerConflict
	}
	if err == nil && b.events < b.catalog.Events {
		err = errSQLiteLedgerConflict
	}
	if err == nil && !b.catalog.Complete {
		err = b.flush(true)
	}
	return b.catalog, err
}

type sqliteSnapshotCatalogBuilder struct {
	s            *sqliteLedger
	ctx          context.Context
	source       sqliteMigrationSource
	catalog      sqliteMigrationSnapshotCatalog
	tx           *sql.Tx
	groups       map[int64]*sqliteMigrationSnapshotGroup
	providers    map[sqliteSnapshotProviderKey]*sqliteMigrationSnapshotProvider
	prefix       hash.Hash
	events, work int64
	api, model   int64
}

type sqliteSnapshotProviderKey struct {
	Node int64
	Name string
}

func (b *sqliteSnapshotCatalogBuilder) rollback() {
	if b.tx != nil {
		_ = b.tx.Rollback()
		b.tx = nil
	}
}

func (b *sqliteSnapshotCatalogBuilder) begin() error {
	if b.tx == nil {
		var err error
		b.tx, err = b.s.writer.BeginTx(b.ctx, nil)
		if err != nil {
			return err
		}
		b.groups = make(map[int64]*sqliteMigrationSnapshotGroup)
		b.providers = make(map[sqliteSnapshotProviderKey]*sqliteMigrationSnapshotProvider)
	}
	return nil
}

func (b *sqliteSnapshotCatalogBuilder) group(node int64) (*sqliteMigrationSnapshotGroup, error) {
	if err := b.begin(); err != nil {
		return nil, err
	}
	if group := b.groups[node]; group != nil {
		return group, nil
	}
	group, err := readSnapshotCatalogGroup(b.ctx, b.tx, b.catalog.Source, node)
	if err != nil {
		return nil, err
	}
	b.groups[node] = &group
	return &group, nil
}

func (b *sqliteSnapshotCatalogBuilder) touch(group *sqliteMigrationSnapshotGroup) error {
	size := int64(2048 + len(group.Name) + len(group.FirstAPI.API) + len(group.FirstResidualAPI.API))
	if size > sqliteLedgerRecordBytes {
		return errSQLiteLedgerBudget
	}
	b.work += size
	return nil
}

func hashSnapshotCatalogItem(h hash.Hash, item sqliteMigrationProjectedItem) {
	var number [8]byte
	put := func(value uint64) { binary.BigEndian.PutUint64(number[:], value); _, _ = h.Write(number[:]) }
	putString := func(value string) {
		put(uint64(len(value)))
		for len(value) > 0 {
			n := min(len(value), sqliteMigrationChunkBytes)
			_, _ = h.Write([]byte(value[:n]))
			value = value[n:]
		}
	}
	put(uint64(item.NodeID))
	put(uint64(len(item.Path)))
	for _, name := range item.Path {
		putString(name)
	}
	putString(item.Kind)
	putString(item.Scope)
	_, _ = h.Write(item.ValueDigest[:])
}

func (b *sqliteSnapshotCatalogBuilder) consume(item sqliteMigrationProjectedItem) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if item.NodeID <= 0 {
		return errSQLiteSnapshotCatalogCorrupt
	}
	// Maintain only the two active ancestor IDs, even while replaying a
	// committed prefix. No high-cardinality path-to-ID map is needed.
	if item.Scope == "api" {
		if item.Kind != "end" {
			b.api = item.NodeID
		}
		if item.Kind == "end" || item.Kind == "null" {
			defer func() { b.api = 0 }()
		}
	}
	if item.Scope == "model" {
		if item.Kind != "end" {
			b.model = item.NodeID
		}
		if item.Kind == "end" || item.Kind == "null" {
			defer func() { b.model = 0 }()
		}
	}
	b.events++
	hashSnapshotCatalogItem(b.prefix, item)
	if b.events <= b.catalog.Events {
		if b.events == b.catalog.Events && !bytes.Equal(b.prefix.Sum(nil), b.catalog.prefix) {
			return errSQLiteLedgerConflict
		}
		return nil
	}
	if b.catalog.Complete {
		return errSQLiteLedgerConflict
	}
	if err := b.begin(); err != nil {
		return err
	}
	if err := b.apply(item); err != nil {
		return err
	}
	if b.events-b.catalog.Events >= sqliteProjectionBatchEvents || b.work >= sqliteProjectionBatchBytes {
		return b.flush(false)
	}
	return nil
}

func (b *sqliteSnapshotCatalogBuilder) apply(item sqliteMigrationProjectedItem) error {
	if item.Scope == "api" || item.Scope == "model" {
		if item.Kind == "object" || item.Kind == "null" {
			parent := b.catalog.Root
			if item.Scope == "model" {
				parent = b.api
			}
			if len(item.Path) == 0 || parent <= 0 {
				return errSQLiteSnapshotCatalogCorrupt
			}
			ancestor, err := b.group(parent)
			if err != nil {
				return err
			}
			g := &sqliteMigrationSnapshotGroup{Node: item.NodeID, Parent: parent, Scope: item.Scope, Name: item.Path[len(item.Path)-1], CatalogGroups: 1}
			g.Ignored = ancestor.Ignored || (g.Scope == "api" && strings.TrimSpace(g.Name) == "")
			if err := b.touch(g); err != nil {
				return err
			}
			if err := insertSnapshotCatalogGroup(b.ctx, b.tx, b.catalog.Source, g); err != nil {
				return err
			}
			b.groups[g.Node] = g
		}
		if item.Kind == "end" || item.Kind == "null" {
			return b.finishGroup(item.NodeID)
		}
		return nil
	}
	if item.Scope == "snapshot" && item.Kind == "end" {
		return b.finishGroup(b.catalog.Root)
	}
	if item.Kind != "value" {
		return nil
	}
	switch item.Scope {
	case "detail":
		return b.detail(item)
	case "provider":
		value, ok := item.Value.(ModelProviderStat)
		if !ok {
			return errSQLiteSnapshotCatalogCorrupt
		}
		g, err := b.group(b.model)
		if err != nil || g.Ignored {
			return err
		}
		if b.catalog.Version < currentStorageSnapshotVersion {
			value.CachedTokens = legacyCacheReadTokens(value.CachedTokens, value.CacheWriteTokens)
		}
		for key, stat := range modelProviderStatsFromSnapshot([]ModelProviderStat{value}) {
			g.HasProviderSnapshot = true
			if err := b.provider(key, stat, nil, false, 0); err != nil {
				return err
			}
		}
		return b.touch(g)
	case "count", "float":
		var node int64
		switch {
		case len(item.Path) == 2 && item.Path[0] == "usage":
			node = b.catalog.Root
		case len(item.Path) == 4 && item.Path[1] == "apis":
			node = b.api
		case len(item.Path) == 6 && item.Path[3] == "models":
			node = b.model
		default:
			return nil // Daily/hourly series are preserved in the projection.
		}
		g, err := b.group(node)
		if err != nil {
			return err
		}
		if err := setSnapshotCatalogScalar(&g.Snapshot, item.Path[len(item.Path)-1], item.Value); err != nil {
			return err
		}
		return b.touch(g)
	}
	return nil
}

func (b *sqliteSnapshotCatalogBuilder) finishGroup(node int64) error {
	g, err := b.group(node)
	if err != nil {
		return err
	}
	if g.Closed {
		return errSQLiteLedgerConflict
	}
	g.finish(b.catalog.Version)
	if err := b.touch(g); err != nil {
		return err
	}
	if g.Parent != 0 {
		parent, err := b.group(g.Parent)
		if err != nil {
			return err
		}
		parent.addChild(g)
		return b.touch(parent)
	}
	return nil
}

func (b *sqliteSnapshotCatalogBuilder) detail(item sqliteMigrationProjectedItem) error {
	detail, ok := item.Value.(RequestDetail)
	if !ok || len(item.Path) != 7 || (item.Path[5] != "details" && item.Path[5] != "accounting") {
		return errSQLiteSnapshotCatalogCorrupt
	}
	position, err := strconv.ParseInt(item.Path[6], 10, 64)
	if err != nil || position < 0 {
		return errSQLiteSnapshotCatalogCorrupt
	}
	g, err := b.group(b.model)
	if err != nil {
		return err
	}
	if isAnonymousProtocolFallbackDetail(detail) {
		g.Fallbacks = addNonNegativeInt64(g.Fallbacks, 1)
	}
	if g.Ignored {
		return b.touch(g)
	}
	apiName := strings.TrimSpace(item.Path[2])
	modelName := normalizeModelName(g.Name)
	actualAPI := storageSnapshotDetailAPIName(apiName, detail)
	actualModel := storageSnapshotDetailModelName(modelName, detail)
	archived := item.Path[5] == "accounting"
	g.SplitAPI = g.SplitAPI || (g.FirstAPI.Set && g.FirstAPI.API != actualAPI)
	g.SplitModel = g.SplitModel || actualModel != modelName
	g.FirstAPI.consider(actualAPI, archived, position)
	if fallback := cleanStorageSnapshotFallbackAPIName(apiName, detail); fallback != "" {
		g.FirstResidualAPI.consider(fallback, archived, position)
	}
	detail = normalizeStorageSnapshotDetailFields(actualModel, detail, b.catalog.Now)
	totals := detailTotalsFromRequest(detail)
	g.Details.addDetail(detail, totals)
	if archived {
		g.Archived = addNonNegativeInt64(g.Archived, 1)
	} else {
		g.Visible = addNonNegativeInt64(g.Visible, 1)
	}
	if err := b.touch(g); err != nil {
		return err
	}
	stat := ModelProviderStat{Provider: strings.TrimSpace(detail.Provider)}
	incrementModelProviderStat(&stat, detail.Failed, totals)
	return b.provider(modelProviderStatsKey(detail.Provider), nil, &stat, archived, position)
}

func (b *sqliteSnapshotCatalogBuilder) provider(key string, snapshot, details *ModelProviderStat, archived bool, position int64) error {
	if len(key) > sqliteLedgerRecordBytes {
		return errSQLiteLedgerBudget
	}
	cacheKey := sqliteSnapshotProviderKey{b.model, key}
	p := b.providers[cacheKey]
	if p == nil {
		value, found, err := readSnapshotCatalogProvider(b.ctx, b.tx, b.catalog.Source, b.model, key)
		if err != nil {
			return err
		}
		p = &value
		p.Key = key
		b.providers[cacheKey] = p
		if !found {
			g, err := b.group(b.model)
			if err != nil {
				return err
			}
			g.CatalogProviders = addNonNegativeInt64(g.CatalogProviders, 1)
			if err := b.touch(g); err != nil {
				return err
			}
		}
	}
	merge := func(dst *ModelProviderStat, src *ModelProviderStat) {
		if src == nil {
			return
		}
		if dst.TotalRequests == 0 {
			*dst = *src
			return
		}
		mergeModelProviderStats(map[string]*ModelProviderStat{key: dst}, map[string]*ModelProviderStat{key: src})
	}
	merge(&p.Snapshot, snapshot)
	merge(&p.Details, details)
	if details != nil {
		p.FirstDetail.consider(details.Provider, archived, position)
		p.Details.Provider = p.FirstDetail.API
	}
	size := int64(1024 + 2*len(key) + len(p.Snapshot.Provider) + len(p.Details.Provider) + len(p.FirstDetail.API))
	if size > sqliteLedgerRecordBytes {
		return errSQLiteLedgerBudget
	}
	// Cache only this bounded batch. A common provider is encoded/written once
	// per batch, but a high-cardinality provider dictionary never stays in RAM.
	b.work += size
	return nil
}

func (b *sqliteSnapshotCatalogBuilder) flush(complete bool) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.begin(); err != nil {
		return err
	}
	if err := checkSnapshotCatalogProjection(b.ctx, b.tx, b.source, b.catalog); err != nil {
		return err
	}
	for _, group := range b.groups {
		raw, checksum, err := encodeSnapshotCatalogValue(group, 0)
		if err != nil {
			return err
		}
		changed, err := b.tx.ExecContext(b.ctx, `UPDATE migration_snapshot_groups SET payload=?,checksum=? WHERE source=? AND node=?`, raw, checksum, b.catalog.Source, group.Node)
		if err := sqliteRequireChanged(changed, err); err != nil {
			return err
		}
	}
	for key, provider := range b.providers {
		raw, checksum, err := encodeSnapshotCatalogValue(provider, len(key.Name))
		if err != nil {
			return err
		}
		if _, err := b.tx.ExecContext(b.ctx, `INSERT INTO migration_snapshot_providers(source,node,name,payload,checksum) VALUES(?,?,?,?,?)
 ON CONFLICT(source,node,name) DO UPDATE SET payload=excluded.payload,checksum=excluded.checksum`,
			b.catalog.Source, key.Node, []byte(key.Name), raw, checksum); err != nil {
			return err
		}
	}
	if complete {
		root, err := readSnapshotCatalogGroup(b.ctx, b.tx, b.catalog.Source, b.catalog.Root)
		if err != nil {
			return err
		}
		if !root.Closed {
			return errSQLiteSnapshotCatalogIncomplete
		}
	}
	prefix := b.prefix.Sum(nil)
	changed, err := b.tx.ExecContext(b.ctx, `UPDATE migration_snapshot_catalogs SET events=?,prefix=?,complete=?
 WHERE source=? AND root=? AND fingerprint=? AND version=? AND clock=? AND events=? AND prefix=? AND complete=0`,
		b.events, prefix, complete, b.catalog.Source, b.catalog.Root, b.catalog.Fingerprint, b.catalog.Version, b.catalog.clock, b.catalog.Events, b.catalog.prefix)
	if err := sqliteRequireChanged(changed, err); err != nil {
		return err
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.tx.Commit(); err != nil {
		return err
	}
	b.tx, b.groups, b.providers, b.work = nil, nil, nil, 0
	b.catalog.Events, b.catalog.prefix, b.catalog.Complete = b.events, prefix, complete
	return nil
}

func encodeSnapshotCatalogValue(value any, keyBytes int) ([]byte, []byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, nil, err
	}
	if keyBytes > sqliteLedgerRecordBytes || len(raw) > sqliteLedgerRecordBytes-keyBytes {
		return nil, nil, errSQLiteLedgerBudget
	}
	checksum := sha256.Sum256(raw)
	return raw, checksum[:], nil
}

func decodeSnapshotCatalogValue(raw, checksum []byte, value any) error {
	if len(raw) == 0 || len(raw) > sqliteLedgerRecordBytes || len(checksum) != sha256.Size {
		return errSQLiteSnapshotCatalogCorrupt
	}
	actual := sha256.Sum256(raw)
	if !bytes.Equal(actual[:], checksum) || json.Unmarshal(raw, value) != nil {
		return errSQLiteSnapshotCatalogCorrupt
	}
	return nil
}

func insertSnapshotCatalogGroup(ctx context.Context, tx *sql.Tx, source string, group *sqliteMigrationSnapshotGroup) error {
	raw, checksum, err := encodeSnapshotCatalogValue(group, 0)
	if err != nil {
		return err
	}
	var parent any
	if group.Parent != 0 {
		parent = group.Parent
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO migration_snapshot_groups VALUES(?,?,?,?,?,?)`, source, group.Node, parent, group.Scope, raw, checksum)
	return err
}

func readSnapshotCatalogGroup(ctx context.Context, db sqliteProjectionQueryRower, source string, node int64) (g sqliteMigrationSnapshotGroup, err error) {
	var raw, checksum []byte
	var parent sql.NullInt64
	var scope string
	err = db.QueryRowContext(ctx, `SELECT parent,scope,
 CASE WHEN length(CAST(payload AS BLOB))<=1048576 THEN payload ELSE NULL END,
 CASE WHEN length(CAST(checksum AS BLOB))=32 THEN checksum ELSE NULL END
 FROM migration_snapshot_groups WHERE source=? AND node=?`, source, node).Scan(&parent, &scope, &raw, &checksum)
	if err == nil {
		err = decodeSnapshotCatalogValue(raw, checksum, &g)
	}
	if err == nil && (g.Node != node || g.Parent != parent.Int64 || g.Scope != scope || len(g.Snapshot.Details) != 0 || len(g.Snapshot.Accounting) != 0 || len(g.Snapshot.Providers) != 0) {
		err = errSQLiteSnapshotCatalogCorrupt
	}
	return g, err
}

func readSnapshotCatalogProvider(ctx context.Context, db sqliteProjectionQueryRower, source string, node int64, key string) (p sqliteMigrationSnapshotProvider, found bool, err error) {
	var raw, checksum []byte
	err = db.QueryRowContext(ctx, `SELECT
 CASE WHEN length(CAST(payload AS BLOB))<=1048576 THEN payload ELSE NULL END,
 CASE WHEN length(CAST(checksum AS BLOB))=32 THEN checksum ELSE NULL END
 FROM migration_snapshot_providers WHERE source=? AND node=? AND name=?`, source, node, []byte(key)).Scan(&raw, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err == nil {
		err = decodeSnapshotCatalogValue(raw, checksum, &p)
	}
	if err == nil && p.Key != key {
		err = errSQLiteSnapshotCatalogCorrupt
	}
	return p, err == nil, err
}

func setSnapshotCatalogScalar(m *ModelSnapshot, name string, value any) error {
	if name == "avg_latency_ms" {
		var ok bool
		m.AvgLatencyMs, ok = value.(float64)
		if !ok {
			return errSQLiteSnapshotCatalogCorrupt
		}
		return nil
	}
	n, ok := value.(int64)
	if !ok {
		return errSQLiteSnapshotCatalogCorrupt
	}
	switch name {
	case "total_requests":
		m.TotalRequests = n
	case "success_count":
		m.SuccessCount = n
	case "failure_count":
		m.FailureCount = n
	case "total_tokens":
		m.TotalTokens = n
	case "input_tokens":
		m.InputTokens = n
	case "output_tokens":
		m.OutputTokens = n
	case "cached_tokens":
		m.CachedTokens = n
	case "cache_write_tokens":
		m.CacheWriteTokens = n
	case "reasoning_tokens":
		m.ReasoningTokens = n
	default:
		return errSQLiteSnapshotCatalogCorrupt
	}
	return nil
}
