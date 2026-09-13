package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLiteProjectionInvalidTailDoesNotPublishPartialData(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	body := `"usage":{"apis":{"API":{"models":{"M":{"details":[` + strings.Repeat(`{},`, 650) + `{}]}}}}}`
	for _, ending := range []string{
		`,"version":99}`, `,"version":-1}`, `,"generated_at":"invalid"}`,
		`,"usage":{"total_tokens":9223372036854775808}}`, `} {}`, `} garbage`, `,"usage":{`,
	} {
		t.Run(ending, func(t *testing.T) {
			raw := `{"version":2,"generated_at":"2026-09-13T00:00:00Z",` + body + ending
			path := writeSQLiteMigrationSource(t, []byte(raw))
			if _, err := s.StageSource(context.Background(), path, "snapshot"); err != nil {
				t.Fatal(err)
			}
			var previousEvents int64
			for attempt := 0; attempt < 2; attempt++ {
				p, err := s.ProjectMigrationSource(context.Background(), path)
				if err == nil || p.Complete || p.Events < sqliteProjectionBatchEvents || (attempt > 0 && p.Events != previousEvents) {
					t.Fatalf("invalid tail exposed or advanced data on retry: events=%d complete=%t err=%v", p.Events, p.Complete, err)
				}
				previousEvents = p.Events
				_, err = s.WalkMigrationProjection(context.Background(), path, func(sqliteMigrationProjectedItem) error {
					t.Fatal("invalid projection delivered data")
					return nil
				})
				if !errors.Is(err, errSQLiteProjectionIncomplete) {
					t.Fatalf("partial source read: %v", err)
				}
			}
		})
	}
}

func TestSQLiteProjectionChecksumFailureBeforeAndAfterCompletion(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	raw := []byte(projectionModelJSON(`"details":[` + strings.Repeat(`{},`, 650) + `{}]`))
	path := writeSQLiteMigrationSource(t, raw)
	if _, err := s.StageSource(context.Background(), path, "snapshot"); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(raw, []byte("2026"), []byte("2025"), 1)
	if _, err := s.writer.Exec("UPDATE migration_chunks SET payload=? WHERE source=?", corrupt, path); err != nil {
		t.Fatal(err)
	}
	p, err := s.ProjectMigrationSource(context.Background(), path)
	if err == nil || p.Complete || p.Events < sqliteProjectionBatchEvents {
		t.Fatalf("checksum failure published projection: events=%d complete=%t err=%v", p.Events, p.Complete, err)
	}
	corruptRoot := p.Root
	if _, err := s.writer.Exec("UPDATE migration_chunks SET payload=? WHERE source=?", raw, path); err != nil {
		t.Fatal(err)
	}
	if p, err = s.ProjectMigrationSource(context.Background(), path); err != nil || !p.Complete || p.Root == corruptRoot {
		t.Fatalf("verified retry could not finish: %+v %v", p, err)
	}
	var want persistedStorageSnapshot
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if got := projectedSnapshotForTest(t, s, path); !reflect.DeepEqual(got, want) {
		t.Fatal("prefix from an unverified scan survived corrected source bytes")
	}
	if _, err := s.writer.Exec("UPDATE migration_chunks SET payload=? WHERE source=?", corrupt, path); err != nil {
		t.Fatal(err)
	}
	_, err = s.WalkMigrationProjection(context.Background(), path, func(sqliteMigrationProjectedItem) error {
		t.Fatal("completed projection with corrupt bytes delivered data")
		return nil
	})
	if err == nil {
		t.Fatal("completed source checksum was not rechecked")
	}
}

func TestSQLiteProjectionChecksFragmentsAfterInitialSourceVerification(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	raw := []byte(projectionModelJSON(`"details":[{"failure":"old"},{"failure":"old"}]`))
	path := compareProjectedSnapshotForTest(t, s, string(raw))
	changed := false
	var delivered int
	_, err := s.WalkMigrationProjection(context.Background(), path, func(item sqliteMigrationProjectedItem) error {
		if !changed {
			// The full-source preflight has finished, but values have not yet
			// been read. Same-length, valid JSON corruption must still fail.
			changed = true
			_, err := s.writer.Exec("UPDATE migration_chunks SET payload=? WHERE source=?", bytes.ReplaceAll(raw, []byte("old"), []byte("bad")), path)
			return err
		}
		if item.Scope == "detail" && item.Kind == "value" {
			delivered++
		}
		return nil
	})
	if err == nil || delivered != 0 {
		t.Fatalf("changed fragment was delivered: records=%d err=%v", delivered, err)
	}
}

func TestSQLiteProjectionPrefetchDoesNotTreatPartialFragmentGroupsAsComplete(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	for _, duplicates := range []int{sqliteProjectionPageNodes - 1, sqliteProjectionPageNodes, sqliteProjectionPageNodes + 1, sqliteProjectionPageNodes*2 + 1} {
		t.Run(fmt.Sprint(duplicates), func(t *testing.T) {
			// The first element has enough fragments to straddle the prefetch
			// limit; its final field must not be dropped. The second element
			// retains an old hidden tail across the intervening short arrays.
			body := `"details":[{"model":"old"},{"model":"tail"}],` + strings.Repeat(`"details":[{}],`, duplicates) + `"details":[{"source":"last"},null]`
			compareProjectedSnapshotForTest(t, s, projectionModelJSON(body))
		})
	}
}

func TestSQLiteProjectionKeysetQueriesUseIndexes(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	arrayQuery, arrayArgs := sqliteProjectionChildrenQuery(sqliteProjectionNode{ID: 1, Kind: "array", Length: 100000}, &sqliteProjectionEdge{Position: 50000})
	bytecode, err := s.reader.Query("EXPLAIN "+arrayQuery, arrayArgs...)
	if err != nil {
		t.Fatal(err)
	}
	registerVariables := make(map[int]int)
	seeksCursor := false
	for bytecode.Next() {
		var address, p1, p2, p3, p5 int
		var operation string
		var p4, comment any
		if err := bytecode.Scan(&address, &operation, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
			t.Fatal(err)
		}
		if operation == "Variable" || operation == "Integer" || strings.HasPrefix(operation, "Seek") || operation == "Le" {
			t.Logf("cursor bytecode: %d %s %d %d %d %v", address, operation, p1, p2, p3, p4)
		}
		if operation == "Variable" {
			registerVariables[p2] = p1
		} else if operation == "Integer" {
			delete(registerVariables, p2)
		} else if operation == "SeekGT" && fmt.Sprint(p4) == "2" && registerVariables[p3] == 1 && registerVariables[p3+1] == 2 {
			seeksCursor = true // Seek uses (parent, supplied cursor), not (parent, 0).
		}
	}
	if err := errors.Join(bytecode.Err(), bytecode.Close()); err != nil {
		t.Fatal(err)
	}
	if !seeksCursor {
		t.Fatal("array query does not seek directly past the supplied cursor")
	}
	objectQuery, objectArgs := sqliteProjectionChildrenQuery(sqliteProjectionNode{ID: 1, Kind: "object"}, &sqliteProjectionEdge{Name: "a"})
	for _, query := range []struct {
		statement string
		args      []any
	}{
		{arrayQuery, arrayArgs}, {objectQuery, objectArgs},
		{`SELECT node,event,start,finish,checksum FROM migration_projection_values WHERE node IN (1,2,3) ORDER BY node,event LIMIT 129`, nil},
	} {
		rows, err := s.reader.Query("EXPLAIN QUERY PLAN "+query.statement, query.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plans []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plans = append(plans, detail)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(plans, "\n")
		t.Log(plan)
		if strings.Contains(plan, "SCAN ") || strings.Contains(plan, "TEMP B-TREE") || !strings.Contains(plan, "SEARCH ") {
			t.Fatalf("projection query scans/sorts history: %s", plan)
		}
	}
}

func TestSQLiteProjectionCompletionCommitCanResumeWithoutReapplyingEvents(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	// Arrange for the final object-end event to fall exactly on a batch edge.
	var baseEvents int
	parser := sqliteSnapshotParser{ctx: ctx, decoder: json.NewDecoder(strings.NewReader(projectionModelJSON(`"details":[]`))), consume: func(sqliteMigrationItem) error { baseEvents++; return nil }}
	if err := parser.object(nil, "snapshot"); err != nil {
		t.Fatal(err)
	}
	count := sqliteProjectionBatchEvents*2 - baseEvents
	raw := projectionModelJSON(`"details":[` + strings.Repeat(`{},`, count-1) + `{}]`)
	path := writeSQLiteMigrationSource(t, []byte(raw))
	if _, err := s.StageSource(ctx, path, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`CREATE TRIGGER projection_finish_fail BEFORE UPDATE OF complete ON migration_projections
 WHEN NEW.complete=1 BEGIN SELECT RAISE(ABORT,'completion injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	p, err := s.ProjectMigrationSource(ctx, path)
	if err == nil || p.Complete || p.Events != sqliteProjectionBatchEvents*2 {
		t.Fatalf("final batch boundary not retained: %+v %v", p, err)
	}
	var before, after int
	if err := s.reader.QueryRow("SELECT count(*) FROM migration_projection_values").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec("DROP TRIGGER projection_finish_fail"); err != nil {
		t.Fatal(err)
	}
	if p, err = s.ProjectMigrationSource(ctx, path); err != nil || !p.Complete {
		t.Fatalf("final commit could not resume: %+v %v", p, err)
	}
	if err := s.reader.QueryRow("SELECT count(*) FROM migration_projection_values").Scan(&after); err != nil || before != after {
		t.Fatal("resuming final publication replayed already committed events")
	}
	if got := projectedSnapshotForTest(t, s, path); len(got.Usage.APIs["API"].Models["M"].Details) != count {
		t.Fatal("completion retry lost array values")
	}
}

func TestSQLiteProjectionCancellationConsumerFailureAndClose(t *testing.T) {
	ctx := context.Background()
	t.Run("cancelled-and-waiting-for-writer", func(t *testing.T) {
		s, _ := testSQLiteLedger(t)
		path := writeSQLiteMigrationSource(t, []byte(projectionModelJSON(`"details":[{}]`)))
		if _, err := s.StageSource(ctx, path, "snapshot"); err != nil {
			t.Fatal(err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.ProjectMigrationSource(cancelled, path); !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-cancelled projection: %v", err)
		}
		tx, err := s.writer.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		waiting, stop := context.WithTimeout(ctx, 30*time.Millisecond)
		defer stop()
		if _, err := s.ProjectMigrationSource(waiting, path); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("writer wait did not preserve deadline: %v", err)
		}
	})
	t.Run("consumer-error", func(t *testing.T) {
		s, _ := testSQLiteLedger(t)
		path := compareProjectedSnapshotForTest(t, s, projectionModelJSON(`"details":[{},{},{}]`))
		sentinel := errors.New("consumer stopped")
		var delivered int
		_, err := s.WalkMigrationProjection(ctx, path, func(item sqliteMigrationProjectedItem) error {
			if item.Scope == "detail" && item.Kind == "value" {
				delivered++
				return sentinel
			}
			return nil
		})
		if !errors.Is(err, sentinel) || delivered != 1 {
			t.Fatalf("consumer failure did not stop the walk: delivered=%d err=%v", delivered, err)
		}
		if _, err := s.Apply(ctx, []sqliteLedgerMutation{{Record: sqliteTestRecord(1)}}, nil); err != nil {
			t.Fatalf("failed walk left a connection or transaction behind: %v", err)
		}
	})
	t.Run("close-during-walk", func(t *testing.T) {
		s, _ := testSQLiteLedger(t)
		path := compareProjectedSnapshotForTest(t, s, projectionModelJSON(`"details":[{},{},{}]`))
		var delivered int
		_, err := s.WalkMigrationProjection(ctx, path, func(item sqliteMigrationProjectedItem) error {
			if item.Scope == "detail" && item.Kind == "value" {
				delivered++
				return s.Close()
			}
			return nil
		})
		if !errors.Is(err, context.Canceled) || delivered != 1 {
			t.Fatalf("close did not stop read or released rows too late: delivered=%d err=%v", delivered, err)
		}
	})
}

func TestSQLiteProjectionConcurrentAttemptsCannotMixProgress(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	raw := projectionModelJSON(`"details":[` + strings.Repeat(`{"model":"same"},`, 1100) + `{}],"details":[{}]`)
	path := writeSQLiteMigrationSource(t, []byte(raw))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.StageSource(ctx, path, "snapshot"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ProjectMigrationSource(ctx, path)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successful int
	for err := range results {
		if err == nil {
			successful++
		} else if !errors.Is(err, errSQLiteLedgerConflict) {
			t.Fatalf("unexpected concurrent projection error: %v", err)
		}
	}
	if successful == 0 {
		t.Fatal("all concurrent projection attempts failed")
	}
	var want persistedStorageSnapshot
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if got := projectedSnapshotForTest(t, s, path); !reflect.DeepEqual(got, want) {
		t.Fatal("concurrent attempts mixed container stacks or values")
	}
}

func TestSQLiteProjectionPagesOversizedMapKeys(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	longName, _ := json.Marshal(strings.Repeat("large-key", sqliteProjectionPageBytes/9+1))
	raw := projectionSnapshotJSON(fmt.Sprintf(`"usage":{"apis":{"":{},"before":{},%s:{"models":{"M":{"details":[{}]}}},"z-after":{}}}`, longName))
	compareProjectedSnapshotForTest(t, s, raw)
}
