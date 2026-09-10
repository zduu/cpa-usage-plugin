//go:build sqlite_purego

package main

// Comparison-only driver candidate. This build tag is not a runtime backend
// option. In particular, equivalent per-connection BLOB limits and platform
// loading must be verified before this can be a production choice.

import (
	"net/url"

	_ "modernc.org/sqlite"
)

const sqliteLedgerDriverName = "sqlite"

func sqliteLedgerDSN(path string, reader bool) string {
	params := url.Values{"mode": {"rw"}, "_busy_timeout": {"250"}, "_foreign_keys": {"on"}, "_synchronous": {"FULL"}, "_txlock": {"immediate"}}
	pragmas := []string{"mmap_size=0", "temp_store=FILE", "trusted_schema=OFF", "wal_autocheckpoint=256", "cache_size=-2048"}
	if reader {
		params.Set("_query_only", "on")
		params.Set("_txlock", "deferred")
		pragmas[len(pragmas)-1] = "cache_size=-1024"
	}
	for _, pragma := range pragmas {
		params.Add("_pragma", pragma)
	}
	return sqliteLedgerFileURI(path, params)
}
