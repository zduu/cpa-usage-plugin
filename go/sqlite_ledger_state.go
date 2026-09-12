package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const sqliteLedgerStateSchema = `CREATE TABLE IF NOT EXISTS ledger_state (
 name TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision>0),
 payload BLOB NOT NULL
) WITHOUT ROWID;`

type sqliteLedgerStateMutation struct {
	Name string
	// Zero means insert only; updates/deletes require the observed revision.
	Revision int64
	Value    []byte
	Delete   bool
}

type sqliteLedgerState struct {
	Revision int64
	Value    []byte
}

func validateSQLiteLedgerStates(states []sqliteLedgerStateMutation) (int, error) {
	if len(states) > sqliteLedgerBatchRecords {
		return 0, errSQLiteLedgerBudget
	}
	seen := make(map[string]bool, len(states))
	bytes := 0
	for _, state := range states {
		if strings.TrimSpace(state.Name) == "" || state.Revision < 0 || (state.Delete && (state.Revision == 0 || len(state.Value) != 0)) || seen[state.Name] {
			return 0, errors.New("invalid or repeated sqlite state mutation")
		}
		seen[state.Name] = true
		size := len(state.Name) + len(state.Value)
		bytes += size
		if size > sqliteLedgerRecordBytes || bytes > sqliteLedgerBatchBytes {
			return 0, errSQLiteLedgerBudget
		}
	}
	return bytes, nil
}

func applySQLiteLedgerStates(ctx context.Context, tx *sql.Tx, revision int64, states []sqliteLedgerStateMutation) error {
	for _, state := range states {
		var result sql.Result
		var err error
		switch {
		case state.Delete:
			result, err = tx.ExecContext(ctx, "DELETE FROM ledger_state WHERE name=? AND revision=?", state.Name, state.Revision)
		case state.Revision == 0:
			// A nil []byte is SQL NULL; an empty state value is a valid BLOB.
			result, err = tx.ExecContext(ctx, "INSERT INTO ledger_state VALUES(?,?,coalesce(?,x'')) ON CONFLICT(name) DO NOTHING", state.Name, revision, state.Value)
		default:
			result, err = tx.ExecContext(ctx, "UPDATE ledger_state SET revision=?,payload=coalesce(?,x'') WHERE name=? AND revision=?", revision, state.Value, state.Name, state.Revision)
		}
		if err := sqliteRequireChanged(result, err); err != nil {
			return err
		}
	}
	return nil
}

// State is read from the same pinned transaction as Page and Progress.
// A caller cannot combine a new aggregate with an older population of rows.
func (v *sqliteLedgerView) State(name string) (state sqliteLedgerState, found bool, err error) {
	if err := v.contextError(); err != nil {
		return state, false, err
	}
	// Check size before the driver copies a possibly externally edited BLOB.
	var size int
	err = v.tx.QueryRowContext(v.ctx, "SELECT revision,length(payload) FROM ledger_state WHERE name=?", name).Scan(&state.Revision, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	if size+len(name) > sqliteLedgerRecordBytes {
		return state, false, errSQLiteLedgerBudget
	}
	err = v.tx.QueryRowContext(v.ctx, "SELECT payload FROM ledger_state WHERE name=?", name).Scan(&state.Value)
	return state, err == nil, err
}
