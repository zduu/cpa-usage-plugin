# Stock CPA ABI smoke test

This isolated test module pins the unmodified CPA `v7.2.152` SDK. It loads the built plugin through `cliproxy_plugin_init`, serializes the real SDK request/completion/usage structures, and queries the plugin's management ABI. It requires a native Linux or macOS shared library. It does not read or change a local CPA checkout, launch CPA, or send upstream requests.

From the repository root:

```sh
cd go
go build -buildmode=c-shared -buildvcs=false -trimpath -o /tmp/usage-dashboard-zduu.so .
cd ../tests/stock-host
go run . /tmp/usage-dashboard-zduu.so 2.6.4
```

The test asserts that the pinned SDK usage message has no `Endpoint`, `Stream`, or `RequestID`. It verifies capability registration, empty request modifications, four protocol paths, stream flags, a canceled request's native failure record, usage before/after completion, `all`/`24h` dashboard queries, and 304/404 status decoding. Plugin unit tests separately cover ambiguous concurrent requests, retries, capacity/expiry, explicit native fields, snapshot and JSONL replay. CI runs this smoke test before release builds.

The optional second argument is the expected plugin version; CI passes the version from `go/register.go`. Without it, registration still requires a numeric semantic version. The smoke test also negotiates both an event JSON array and a full v1 usage backup, reassembles 37-byte chunks through the actual ABI, verifies CRC32/offset/size/opaque version and record count, checks 412, and deletes each job. Full backups must ignore event filters and limits. This harness requires the candidate chunk-export and usage-backup APIs. Actual HTTP sanitizer and browser compatibility are checked separately by `scripts/validate-stock-cpa.py --browser`.

Only the static `/dashboard` bootstrap may be registered as a resource: stock CPA serves that namespace without management authentication. The harness checks this registration boundary and rejection of stale data-resource routes even if an unverified Authorization header is supplied. The HTTP validator additionally probes anonymous resource aliases against the real server; the browser must use authenticated management routes for every data request.
