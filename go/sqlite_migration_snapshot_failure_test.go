package main

// Local-only fault, high-cardinality and upgrade checks; not submitted.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSQLiteSnapshotCatalogHighCardinalityPages(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	api := APISnapshot{TotalRequests: 2000, Models: make(map[string]ModelSnapshot)}
	for i := range 300 {
		api.Models[fmt.Sprintf("model-%04d", i)] = ModelSnapshot{TotalRequests: 1, InputTokens: int64(i)}
	}
	model := ModelSnapshot{TotalRequests: 2000, Providers: []ModelProviderStat{{Provider: "", TotalRequests: 1}}}
	for i := range 1030 {
		name := fmt.Sprintf("provider-%04d", i)
		if i < 8 {
			name += strings.Repeat("p", 48<<10) // More than a key-name page in total.
		}
		model.Providers = append(model.Providers, ModelProviderStat{Provider: name, TotalRequests: 1})
	}
	api.Models["providers"] = model
	raw, err := json.Marshal(persistedStorageSnapshot{Version: 2, GeneratedAt: "2026-09-13T00:00:00Z", Usage: StatisticsSnapshot{TotalRequests: 2000, APIs: map[string]APISnapshot{"API": api}}})
	if err != nil {
		t.Fatal(err)
	}
	path, catalog, groups := snapshotCatalogForTest(t, s, string(raw))
	if len(groups) != 303 || groups[catalog.Root].CatalogProviders != 1031 {
		t.Fatal("high-cardinality group catalog lost rows")
	}
	err = s.WithMigrationSnapshotCatalog(context.Background(), path, func(reader *sqliteSnapshotCatalogReader) error {
		return reader.WalkGroups(func(g sqliteMigrationSnapshotGroup) error {
			if g.Name != "providers" {
				return nil
			}
			seen := make(map[string]bool)
			err := reader.WalkProviders(g.Node, func(provider sqliteMigrationSnapshotProvider) error {
				if seen[provider.Key] {
					t.Fatal("provider pagination duplicated a key")
				}
				seen[provider.Key] = true
				return nil
			})
			if len(seen) != 1031 || !seen[""] {
				t.Fatal("provider pagination omitted an empty/large key")
			}
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSnapshotCatalogCorruptionCannotLookLikeEmptyData(t *testing.T) {
	for _, mode := range []string{"source", "group", "missing-group", "provider", "missing-provider", "prefix", "fingerprint", "oversized-text"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testSQLiteLedger(t)
			path, _, groups := snapshotCatalogForTest(t, s, projectionModelJSON(`"total_requests":10,"details":[{"tokens":{"input_tokens":3}}],"providers":[{"provider":"raw","total_requests":9}]`))
			var model int64
			for _, g := range groups {
				if g.Scope == "model" {
					model = g.Node
				}
			}
			var err error
			switch mode {
			case "source":
				_, err = s.writer.Exec("UPDATE migration_chunks SET payload=replace(CAST(payload AS TEXT),'input_tokens','other_tokens') WHERE source=?", path)
			case "group":
				_, err = s.writer.Exec("UPDATE migration_snapshot_groups SET payload=? WHERE source=? AND node=?", []byte(`{"Closed":true}`), path, model)
			case "missing-group":
				_, err = s.writer.Exec("DELETE FROM migration_snapshot_providers WHERE source=? AND node=?", path, model)
				if err == nil {
					_, err = s.writer.Exec("DELETE FROM migration_snapshot_groups WHERE source=? AND node=?", path, model)
				}
			case "provider":
				_, err = s.writer.Exec("UPDATE migration_snapshot_providers SET payload=? WHERE source=?", []byte(`{}`), path)
			case "missing-provider":
				_, err = s.writer.Exec("DELETE FROM migration_snapshot_providers WHERE source=? AND name=?", path, []byte("raw"))
			case "prefix":
				_, err = s.writer.Exec("UPDATE migration_snapshot_catalogs SET prefix=zeroblob(32) WHERE source=?", path)
			case "fingerprint":
				_, err = s.writer.Exec("UPDATE migration_snapshot_catalogs SET fingerprint=? WHERE source=?", strings.Repeat("0", 64), path)
			case "oversized-text":
				_, err = s.writer.Exec("PRAGMA ignore_check_constraints=ON")
				if err == nil {
					_, err = s.writer.Exec("UPDATE migration_snapshot_groups SET payload=CAST(zeroblob(2000000) AS TEXT) WHERE source=? AND node=?", path, model)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			err = s.WithMigrationSnapshotCatalog(context.Background(), path, func(reader *sqliteSnapshotCatalogReader) error {
				return reader.WalkGroups(func(g sqliteMigrationSnapshotGroup) error {
					if g.Scope == "model" {
						return reader.WalkProviders(g.Node, func(sqliteMigrationSnapshotProvider) error { return nil })
					}
					return nil
				})
			})
			if err == nil {
				t.Fatal("corruption silently produced an apparently complete catalog")
			}
		})
	}
}

func TestSQLiteSnapshotCatalogFlagsUnresolvedSemantics(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	raw := projectionSnapshotJSON(`"usage":{"total_requests":20,"apis":{"sk-local-catalog":{"models":{"A":{"total_requests":10,"details":[{"source":"east","executor_type":"ResponseInterceptorFallback","tokens":{"input_tokens":3}}]},"B":{"total_requests":10,"details":[{"source":"west","tokens":{"input_tokens":4}}]}}}}}`)
	_, catalog, groups := snapshotCatalogForTest(t, s, raw)
	root := groups[catalog.Root]
	if root.Fallbacks != 1 || !root.AmbiguousResidualAPI {
		t.Fatal("pre-protocol or ambiguous residual routing was not made explicit")
	}
	for _, g := range groups {
		if g.Scope == "api" && (!g.SplitAPI || !g.AmbiguousResidualAPI || g.FirstResidualAPI.API != "east") {
			t.Fatal("ambiguous map-order-dependent residual destination was silently resolved")
		}
	}
}

func TestSQLiteSnapshotCatalogAdmissionAndHeaderChecks(t *testing.T) {
	for _, mode := range []string{"jsonl", "unprojected", "absent-catalog", "zero-clock", "future-result", "wrong-version-result", "wrong-time-result"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testSQLiteLedger(t)
			ctx := context.Background()
			format, raw := "snapshot", projectionModelJSON(`"cached_tokens":15,"cache_write_tokens":5,"details":[{}]`)
			if mode == "jsonl" {
				format, raw = "jsonl", "{\"api\":\"local-fixture\",\"detail\":{}}\n"
			}
			path := writeSQLiteMigrationSource(t, []byte(raw))
			if _, err := s.StageSource(ctx, path, format); err != nil {
				t.Fatal(err)
			}
			if mode != "unprojected" {
				if _, err := s.ProjectMigrationSource(ctx, path); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "absent-catalog" {
				err := s.WithMigrationSnapshotCatalog(ctx, path, func(*sqliteSnapshotCatalogReader) error { t.Fatal("absent catalog read"); return nil })
				if !errors.Is(err, errSQLiteSnapshotCatalogIncomplete) {
					t.Fatalf("absent catalog: %v", err)
				}
				return
			}
			now := snapshotCatalogTestNow
			if mode == "zero-clock" {
				now = time.Time{}
			}
			if mode == "future-result" || mode == "wrong-version-result" || mode == "wrong-time-result" {
				var result sqliteMigrationParseResult
				var value []byte
				if err := s.reader.QueryRow("SELECT result FROM migration_projections WHERE source=?", path).Scan(&value); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(value, &result); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "future-result":
					result.Version = 99
				case "wrong-version-result":
					result.Version = 1
				case "wrong-time-result":
					result.GeneratedAt = result.GeneratedAt.Add(time.Hour)
				}
				value, _ = json.Marshal(result)
				if _, err := s.writer.Exec("UPDATE migration_projections SET result=? WHERE source=?", value, path); err != nil {
					t.Fatal(err)
				}
			}
			catalog, err := s.CatalogMigrationSnapshot(ctx, path, now)
			if err == nil || catalog.Complete {
				t.Fatal("invalid source/header/clock was admitted as a complete catalog")
			}
		})
	}
}

func TestSQLiteSnapshotCatalogKeyBudgetPreservesStagedInput(t *testing.T) {
	for _, scope := range []string{"model", "provider"} {
		t.Run(scope, func(t *testing.T) {
			s, _ := testSQLiteLedger(t)
			key := strings.Repeat("key", 360<<10)
			m := ModelSnapshot{TotalRequests: 1}
			name := "M"
			if scope == "model" {
				name = key
			} else {
				m.Providers = []ModelProviderStat{{Provider: key, TotalRequests: 1}}
			}
			raw, err := json.Marshal(persistedStorageSnapshot{Version: 2, GeneratedAt: "2026-09-13T00:00:00Z", Usage: StatisticsSnapshot{APIs: map[string]APISnapshot{"API": {Models: map[string]ModelSnapshot{name: m}}}}})
			if err != nil {
				t.Fatal(err)
			}
			path := compareProjectedSnapshotForTest(t, s, string(raw))
			catalog, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
			if !errors.Is(err, errSQLiteLedgerBudget) || catalog.Complete {
				t.Fatalf("key budget silently dropped/truncated data: complete=%t err=%v", catalog.Complete, err)
			}
			if err := s.verifyProjectionSource(context.Background(), path); err != nil {
				t.Fatal("key-budget failure damaged staged input")
			}
		})
	}
}

func TestSQLiteSnapshotCatalogCancelAfterCommittedProgress(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	path := compareProjectedSnapshotForTest(t, s, projectionModelJSON(`"details":[`+strings.Repeat(`{},`, 15999)+`{}]`))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.CatalogMigrationSnapshot(ctx, path, snapshotCatalogTestNow); done <- err }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("catalog ended before cancellation probe: %v", err)
		case <-deadline.C:
			cancel()
			<-done
			t.Fatal("no committed catalog progress")
		case <-ticker.C:
			var events int64
			if err := s.reader.QueryRow("SELECT events FROM migration_snapshot_catalogs WHERE source=?", path).Scan(&events); err == nil && events >= 256 {
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("in-flight cancellation: %v", err)
				}
				catalog, err := s.CatalogMigrationSnapshot(context.Background(), path, snapshotCatalogTestNow)
				if err != nil || !catalog.Complete || readSnapshotCatalogGroupsForTest(t, s, path)[catalog.Root].Details.TotalRequests != 16000 {
					t.Fatalf("cancelled catalog did not resume exactly once: %v", err)
				}
				return
			}
		}
	}
}

func dropSnapshotCatalogSchemaForTest(t *testing.T, s *sqliteLedger) {
	t.Helper()
	if _, err := s.writer.Exec(`DROP TABLE migration_snapshot_providers; DROP TABLE migration_snapshot_groups;
 DROP TABLE migration_snapshot_catalogs; PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSnapshotCatalogSchema6UpgradeAndBackup(t *testing.T) {
	s, dbPath := testSQLiteLedger(t)
	ctx := context.Background()
	ids, err := s.ApplyState(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, []sqliteLedgerStateMutation{{Name: "preserve", Value: []byte("original")}}, &sqliteLedgerProgress{Source: "original", Fingerprint: "local", NextOffset: 3})
	if err != nil {
		t.Fatal(err)
	}
	path := compareProjectedSnapshotForTest(t, s, projectionModelJSON(`"total_requests":10,"details":[{},{}]`))
	dropSnapshotCatalogSchemaForTest(t, s)
	oldBackup := filepath.Join(t.TempDir(), "schema6-backup")
	backup, err := s.Backup(ctx, oldBackup)
	if err != nil || backup.Schema != 6 {
		t.Fatalf("actual schema 6 backup: %v", err)
	}
	s.Close()
	restoredPath := filepath.Join(t.TempDir(), "restored-schema6.sqlite")
	if err := restoreSQLiteLedgerBackup(ctx, oldBackup, restoredPath); err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{dbPath, restoredPath} {
		upgraded, err := openSQLiteLedger(ctx, location)
		if err != nil {
			t.Fatal(err)
		}
		view, err := upgraded.ReadView(ctx)
		if err != nil {
			t.Fatal(err)
		}
		records := sqliteReadAll(t, view, sqliteLedgerQuery{}, 2)
		state, found, err := view.State("preserve")
		if err != nil || !found || string(state.Value) != "original" || view.Generation != 1 || len(records) != 1 || records[0].ID != ids[0] {
			t.Fatal("schema upgrade changed original authority")
		}
		view.Close()
		catalog, err := upgraded.CatalogMigrationSnapshot(ctx, path, snapshotCatalogTestNow)
		if err != nil || !catalog.Complete {
			t.Fatalf("upgraded catalog: %v", err)
		}
		before := readSnapshotCatalogGroupsForTest(t, upgraded, path)
		newBackup := filepath.Join(t.TempDir(), "catalog-backup")
		if _, err := upgraded.Backup(ctx, newBackup); err != nil {
			t.Fatal(err)
		}
		restored := filepath.Join(t.TempDir(), "catalog-restored.sqlite")
		if err := restoreSQLiteLedgerBackup(ctx, newBackup, restored); err != nil {
			t.Fatal(err)
		}
		copy, err := openSQLiteLedger(ctx, restored)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, readSnapshotCatalogGroupsForTest(t, copy, path)) {
			t.Fatal("backup restore changed catalog state")
		}
		copy.Close()
		upgraded.Close()
	}
	if current, err := verifySQLiteLedgerBackup(ctx, oldBackup); err != nil || current.SHA256 != backup.SHA256 || current.Schema != 6 {
		t.Fatal("upgrade modified the old backup")
	}
}

func TestSQLiteSnapshotCatalogIncompatibleSchemaRollsBackUpgrade(t *testing.T) {
	s, dbPath := testSQLiteLedger(t)
	dropSnapshotCatalogSchemaForTest(t, s)
	if _, err := s.writer.Exec(`CREATE TABLE migration_snapshot_catalogs(source TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reopened, err := openSQLiteLedger(context.Background(), dbPath); err == nil {
		reopened.Close()
		t.Fatal("incompatible catalog extension was silently accepted")
	}
	db, err := sql.Open(sqliteLedgerDriverName, sqliteLedgerDSN(dbPath, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatalf("failed upgrade changed schema version: %d %v", version, err)
	}
}
