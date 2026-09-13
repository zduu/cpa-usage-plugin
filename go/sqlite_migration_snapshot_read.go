package main

import (
	"context"
	"database/sql"
	"errors"
)

// The reader borrows only immutable completed catalog rows, not a long-lived
// SQLite transaction. It cannot outlive its callback or the ledger. Its
// contents are PRE-protocol accounting facts, not publishable live statistics.
type sqliteSnapshotCatalogReader struct {
	s       *sqliteLedger
	ctx     context.Context
	Catalog sqliteMigrationSnapshotCatalog
	Root    sqliteMigrationSnapshotGroup
}

func (s *sqliteLedger) WithMigrationSnapshotCatalog(parent context.Context, path string, consume func(*sqliteSnapshotCatalogReader) error) (err error) {
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	if consume == nil {
		return errors.New("sqlite snapshot catalog requires a consumer")
	}
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	source, projection, err := s.snapshotCatalogSource(ctx, path)
	if err != nil {
		return err
	}
	catalog, err := readSQLiteSnapshotCatalog(ctx, s.reader, source.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return errSQLiteSnapshotCatalogIncomplete
	}
	if err != nil {
		return err
	}
	if !catalog.Complete {
		return errSQLiteSnapshotCatalogIncomplete
	}
	if !sqliteSnapshotCatalogMatches(catalog, projection, catalog.clock) {
		return errSQLiteLedgerConflict
	}
	catalog.GeneratedAt = projection.Result.GeneratedAt
	// Revalidate the effective projection prefix as well as the raw source.
	// This completed-catalog path performs no writes or generation changes.
	if _, err := s.CatalogMigrationSnapshot(ctx, source.Source, catalog.Now); err != nil {
		return err
	}
	root, err := readSnapshotCatalogGroup(ctx, s.reader, source.Source, catalog.Root)
	if err != nil {
		return err
	}
	if !root.Closed || root.Scope != "usage" || root.Parent != 0 {
		return errSQLiteSnapshotCatalogCorrupt
	}
	reader := &sqliteSnapshotCatalogReader{s: s, ctx: ctx, Catalog: catalog, Root: root}
	if err := consume(reader); err != nil {
		return err
	}
	return ctx.Err()
}

func (r *sqliteSnapshotCatalogReader) WalkGroups(consume func(sqliteMigrationSnapshotGroup) error) error {
	if consume == nil {
		return errors.New("sqlite snapshot catalog requires a group consumer")
	}
	var after, count int64
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		rows, err := r.s.reader.QueryContext(r.ctx, `SELECT node FROM migration_snapshot_groups
 WHERE source=? AND node>? ORDER BY node LIMIT ?`, r.Catalog.Source, after, sqliteProjectionPageNodes)
		if err != nil {
			return err
		}
		var nodes []int64
		for rows.Next() {
			var node int64
			if err := rows.Scan(&node); err != nil {
				_ = rows.Close()
				return err
			}
			nodes = append(nodes, node)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(nodes) == 0 {
			if count != r.Root.CatalogGroups {
				return errSQLiteSnapshotCatalogCorrupt
			}
			return nil
		}
		// Close the ID page before loading any payload or calling user code.
		// Even a page of maximum-sized group names never accumulates in Go.
		for _, node := range nodes {
			group, err := readSnapshotCatalogGroup(r.ctx, r.s.reader, r.Catalog.Source, node)
			if err != nil {
				return err
			}
			if !group.Closed {
				return errSQLiteSnapshotCatalogCorrupt
			}
			if err := consume(group); err != nil {
				return err
			}
			count++
		}
		after = nodes[len(nodes)-1]
	}
}

func (r *sqliteSnapshotCatalogReader) WalkProviders(modelNode int64, consume func(sqliteMigrationSnapshotProvider) error) error {
	if consume == nil {
		return errors.New("sqlite snapshot catalog requires a provider consumer")
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	group, err := readSnapshotCatalogGroup(r.ctx, r.s.reader, r.Catalog.Source, modelNode)
	if err != nil {
		return err
	}
	if group.Scope != "model" || !group.Closed {
		return errSQLiteSnapshotCatalogCorrupt
	}
	var after []byte
	var count int64
	for {
		query := `SELECT length(CAST(name AS BLOB)),CASE WHEN length(CAST(name AS BLOB))<=1048576 THEN name ELSE NULL END
 FROM migration_snapshot_providers WHERE source=? AND node=?`
		args := []any{r.Catalog.Source, modelNode}
		if after != nil {
			query += " AND name>?"
			args = append(args, after)
		}
		query += " ORDER BY name LIMIT ?"
		args = append(args, sqliteProjectionPageNodes)
		rows, err := r.s.reader.QueryContext(r.ctx, query, args...)
		if err != nil {
			return err
		}
		var names []string
		var nameBytes int
		for rows.Next() {
			var size int
			var name []byte
			if err := rows.Scan(&size, &name); err != nil {
				_ = rows.Close()
				return err
			}
			if size < 0 || size > sqliteLedgerRecordBytes || len(name) != size {
				_ = rows.Close()
				return errSQLiteSnapshotCatalogCorrupt
			}
			if len(names) > 0 && size > sqliteProjectionPageBytes-nameBytes {
				break
			}
			names = append(names, string(name))
			nameBytes += size
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(names) == 0 {
			if count != group.CatalogProviders {
				return errSQLiteSnapshotCatalogCorrupt
			}
			return nil
		}
		for _, name := range names {
			provider, found, err := readSnapshotCatalogProvider(r.ctx, r.s.reader, r.Catalog.Source, modelNode, name)
			if err != nil {
				return err
			}
			if !found {
				return errSQLiteSnapshotCatalogCorrupt
			}
			if err := consume(provider); err != nil {
				return err
			}
			count++
		}
		// make keeps the empty provider key distinct from the initial cursor.
		after = append(make([]byte, 0, len(names[len(names)-1])), names[len(names)-1]...)
	}
}
