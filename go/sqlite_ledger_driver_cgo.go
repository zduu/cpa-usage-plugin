//go:build !sqlite_purego

package main

import (
	"database/sql"
	"net/url"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const sqliteLedgerDriverName = "cpa-usage-ledger"

func init() {
	sql.Register(sqliteLedgerDriverName, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		// Reject externally oversized rows before the driver copies their BLOBs
		// into Go. No mmap or unbounded memory-only temporary sort database.
		c.SetLimit(sqlite3.SQLITE_LIMIT_LENGTH, 4*sqliteLedgerRecordBytes)
		_, err := c.Exec("PRAGMA mmap_size=0; PRAGMA temp_store=FILE; PRAGMA trusted_schema=OFF; PRAGMA wal_autocheckpoint=256", nil)
		return err
	}})
}

func sqliteLedgerDSN(path string, reader bool) string {
	params := url.Values{"mode": {"rw"}, "_busy_timeout": {"250"}, "_foreign_keys": {"on"}, "_synchronous": {"FULL"}, "_cache_size": {"-2048"}, "_txlock": {"immediate"}}
	if reader {
		params.Set("_query_only", "on")
		params.Set("_txlock", "deferred")
		params.Set("_cache_size", "-1024")
	}
	return sqliteLedgerFileURI(path, params)
}
