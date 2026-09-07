# Stock CPA ABI smoke test

This isolated test module pins the unmodified CPA `v7.2.152` SDK. It loads the built plugin through `cliproxy_plugin_init`, serializes the real SDK request/completion/usage structures, and queries the plugin's management ABI. It requires a native Linux or macOS shared library. It does not read or change a local CPA checkout, launch CPA, or send upstream requests.

From the repository root:

```sh
cd go
go build -buildmode=c-shared -buildvcs=false -trimpath -o /tmp/usage-dashboard-zduu.so .
cd ../tests/stock-host
go run . /tmp/usage-dashboard-zduu.so
```

The test asserts that the pinned SDK usage message has no `Endpoint`, `Stream`, or `RequestID`. It verifies capability registration, empty request modifications, four protocol paths, stream flags, a canceled request's native failure record, usage before/after completion, `all`/`24h` dashboard queries, and 304/404 status decoding. Plugin unit tests separately cover ambiguous concurrent requests, retries, capacity/expiry, explicit native fields, snapshot and JSONL replay. CI runs this smoke test before release builds.
