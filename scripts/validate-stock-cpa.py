#!/usr/bin/env python3
"""Validate a Linux plugin in an isolated, unmodified stock CPA container.

Requires Docker and Python 3.12+. Uses generated data and temporary credentials,
a dedicated Docker network, a random loopback port and a fresh data directory.
Only the container/network created by this run are removed. Evidence goes to
a new --output directory; existing output directories are never overwritten.
This is a correctness check, not the CPU/RSS or 24-hour performance gate.
"""
import argparse
import base64
from datetime import datetime, timedelta, timezone
import hashlib
import json
import math
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import build_opener, ProxyHandler, Request
import zlib

CPA_IMAGE = "eceasy/cli-proxy-api@sha256:02b3bb12d866cfc8255b5ee066eee86ad425669da6a79de5b55569eddbe48ba8"
PREFIX = "/v0/management/plugins/usage-dashboard-zduu"
API = "openai · validation"


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def command(*args, check=True):
    result = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
    if check and result.returncode:
        raise RuntimeError(f"{args[0]} {args[1]} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def fixture(count):
    anchor = datetime.now(timezone.utc).replace(microsecond=0)
    rows = []
    for i in range(count):
        age = [144, 48, 12, 1][i * 4 // count]
        at = anchor - timedelta(hours=age) + timedelta(milliseconds=(i // 10) % 1000)
        rows.append({
            "model": f"model-{i % 2}", "provider": "openai", "source": "validation",
            "timestamp": at.isoformat(), "api_key": f"isolated-fixture-client-{i % 3}",
            "auth_index": f"fixture-auth-{i % 5}", "latency_ms": 100 + i % 300,
            "failed": i % 17 == 0, "status_code": 503 if i % 17 == 0 else 200,
            "failure": f"synthetic-error-{i % 7}" if i % 17 == 0 else "",
            "tokens": {"input_tokens": 9007199254740993 if i == count - 2 else 100 + i % 79,
                       "output_tokens": i % 31},
        })
    return anchor, rows


def import_payload(rows):
    models = {}
    for row in rows:
        models.setdefault(row["model"], {"details": []})["details"].append(row)
    return {"version": 1, "usage": {"apis": {API: {"models": models}}}}


def check_totals(actual, rows, prices, cost_key):
    expected = {
        "total_requests": len(rows), "failure_count": sum(row["failed"] for row in rows),
        "success_count": sum(not row["failed"] for row in rows),
        "input_tokens": sum(row["tokens"]["input_tokens"] for row in rows),
        "output_tokens": sum(row["tokens"]["output_tokens"] for row in rows),
    }
    expected["total_tokens"] = expected["input_tokens"] + expected["output_tokens"]
    for key, value in expected.items():
        require(actual.get(key, 0) == value, f"{key}: got {actual.get(key)}, expected {value}")
    if cost_key:
        cost = math.fsum((row["tokens"]["input_tokens"] * prices[row["model"]][0]
                         + row["tokens"]["output_tokens"] * prices[row["model"]][1]) / 1_000_000 for row in rows)
        require(math.isclose(actual[cost_key], cost, rel_tol=2e-11, abs_tol=1e-12),
                f"{cost_key}: got {actual[cost_key]}, expected {cost}")


class Client:
    def __init__(self, port, key, samples):
        self.base = f"http://127.0.0.1:{port}" + PREFIX
        self.key, self.samples = key, samples
        self.opener = build_opener(ProxyHandler({}))

    def rebind(self, container):
        # Docker may assign a new random published port on each start.
        port = command("docker", "port", container, "8317/tcp").rsplit(":", 1)[1]
        self.base = f"http://127.0.0.1:{int(port)}" + PREFIX

    def request(self, method, route, query=None, body=None, headers=None, auth=True, status=200, resource=False):
        base = self.base.replace("/v0/management/plugins/", "/v0/resource/plugins/") if resource else self.base
        url = base + route + ("?" + urlencode(query) if query else "")
        request_headers = {"Content-Type": "application/json"}
        if auth:
            request_headers["Authorization"] = "Bearer " + self.key
        request_headers.update(headers or {})
        encoded = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode() if body is not None else None
        started = time.perf_counter()
        request = Request(url, data=encoded, headers=request_headers, method=method)
        try:
            response = self.opener.open(request, timeout=45)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            code, response_headers = response.code, dict(response.headers.items())
        self.samples.append({"method": method, "route": route, "resource": resource, "status": code, "bytes": len(raw),
                             "duration_ms": (time.perf_counter() - started) * 1000})
        allowed = (status,) if isinstance(status, int) else status
        require(code in allowed, f"{method} {route}: HTTP {code}, expected {allowed}; {raw[:300]!r}")
        try:
            value = json.loads(raw) if raw else None
        except json.JSONDecodeError:
            require(code >= 400, f"{method} {route}: expected a JSON response")
            value = None  # Unregistered CPA routes may return a plain-text 404.
        require(not (isinstance(value, dict) and value.get("ok") is False), f"{route}: plugin error {value}")
        return value, response_headers

    def ready(self):
        deadline = time.monotonic() + 55
        last_error = None
        while time.monotonic() < deadline:
            try:
                return self.request("GET", "/health")[0]
            except (URLError, TimeoutError, ConnectionError, AssertionError) as error:
                last_error = str(error)
                time.sleep(0.25)
        raise RuntimeError("CPA did not become ready: " + str(last_error))


def run_checks(client, container, rows, anchor, report):
    print("Checking management authentication and import", flush=True)
    client.request("GET", "/health", auth=False, status=(401, 403))
    client.request("GET", "/health", headers={"Authorization": "Bearer invalid-test-key"}, status=(401, 403))
    prices = {"model-0": (2, 3), "model-1": (4, 5)}
    for model, (prompt, completion) in prices.items():
        client.request("PUT", "/model-prices", body={"model": model, "price": {"prompt": prompt, "completion": completion}})
    payload = import_payload(rows)
    first = client.request("POST", "/usage/import", body=payload)[0]
    require(first["added"] == len(rows) and first["total_requests"] == len(rows), "import did not publish all records")
    repeat = client.request("POST", "/usage/import", body=payload)[0]
    require(repeat["added"] == 0 and repeat["skipped"] == len(rows), "repeat import changed record identity")
    report["import"] = {"first": first, "repeat": repeat}

    print("Checking cold/warm ranges, API detail, client filters and conditional requests", flush=True)
    for pass_index in range(2):
        for range_key, hours in [("all", None), ("7h", 7), ("24h", 24), ("7d", 168)]:
            selected = [row for row in rows if hours is None or datetime.fromisoformat(row["timestamp"]) >= anchor - timedelta(hours=hours)]
            summary = client.request("GET", "/dashboard-summary", {"range": range_key})[0]
            check_totals(summary["usage"], selected, prices, "total_cost")
            detail = client.request("GET", "/dashboard-api-detail", {"api": API, "range": range_key})[0]
            check_totals(detail["summary"], selected, prices, "estimated_cost")
            require(detail["total_events"] == len(selected), "API event aggregate excluded accounting")
    summary, headers = client.request("GET", "/dashboard-summary", {"range": "all"})
    etag = next(value for key, value in headers.items() if key.lower() == "etag")
    client.request("GET", "/dashboard-summary", {"range": "all"}, headers={"If-None-Match": etag}, status=304)
    for item in summary["client_api_stats"]:
        selector = "h." + item["api_key_hash"] + "." + base64.urlsafe_b64encode(item["api_key"].encode()).decode().rstrip("=")
        filtered = client.request("GET", "/dashboard-summary", {"range": "all", "client_api": selector})[0]
        require(filtered["usage"]["total_requests"] == item["total_requests"], "client selector lost or merged records")
        detail = client.request("GET", "/dashboard-api-detail", {"api": API, "range": "all", "client_api": selector})[0]
        require(detail["summary"]["total_requests"] == item["total_requests"], "API client filter changed counts")
    health = client.request("GET", "/health")[0]
    require(health["detail_count"] == 10000, "visible detail limit changed")
    runtime = health["runtime"]
    require(runtime["range_cache_hits"] > 0 and 0 < runtime["range_cache_estimated_bytes"] <= runtime["range_cache_budget_bytes"],
            "bounded range cache was not used through real CPA")
    report["cache"] = {key: value for key, value in runtime.items() if key.startswith("range_")}

    print("Checking frozen export, repricing, chunk retry and integrity", flush=True)
    job = client.request("POST", "/dashboard-events-export-jobs", {"format": "json", "json_rows": "true", "model": "model-0"}, status=202)[0]
    deadline = time.monotonic() + 45
    while job["status"] in ("queued", "running") and time.monotonic() < deadline:
        time.sleep(0.05)
        job = client.request("GET", "/dashboard-events-export-jobs", {"id": job["id"]})[0]
    require(job["status"] == "succeeded" and job["exported"] == 5000, "event export failed or included archived rows")
    client.request("POST", "/usage/export-jobs", auth=False, status=(401, 403))
    full_job = client.request("POST", "/usage/export-jobs", {"limit": 1, "model": "not-a-model", "range": "7h"}, status=202)[0]
    deadline = time.monotonic() + 45
    while full_job["status"] in ("queued", "running") and time.monotonic() < deadline:
        time.sleep(0.05)
        full_job = client.request("GET", "/usage/export-jobs", {"id": full_job["id"]})[0]
    require(full_job["status"] == "succeeded" and full_job["kind"] == "usage" and full_job["format"] == "json"
            and full_job["exported"] == len(rows) and not full_job.get("truncated") and not full_job.get("json_rows"),
            "full backup negotiation failed or inherited event filtering")
    print("Checking anonymous resource aliases cannot expose management data", flush=True)
    resource_probes = [
        ("/dashboard-data", None), ("/dashboard-summary", {"range": "all"}),
        ("/dashboard-events", {"limit": 1}), ("/dashboard-api-detail", {"api": "openai"}),
        ("/dashboard-events-export", {"limit": 1}), ("/dashboard-events-export-jobs", None),
        ("/dashboard-events-export-download", {"id": job["id"], "chunk": 1, "offset": 0, "length": 32, "version": job["version"]}),
        ("/model-prices", None), ("/health", None), ("/usage/export", None),
        ("/usage/export-jobs", None), ("/usage/export-jobs", {"id": full_job["id"]}),
        ("/usage/export-download", {"id": full_job["id"], "chunk": 1, "offset": 0, "length": 32, "version": full_job["version"]}),
    ]
    report["resource_auth"] = []
    for route, query in resource_probes:
        client.request("GET", route, query, auth=False, resource=True, status=(200, 401, 403, 404))
        sample = client.samples[-1]
        report["resource_auth"].append({"route": route, "with_job_id": bool(query and "id" in query),
                                        "status": sample["status"], "bytes": sample["bytes"]})
    require(all(sample["status"] in (401, 403, 404) for sample in report["resource_auth"]),
            "anonymous resource aliases exposed management data")
    old_prices = dict(prices)
    prices["model-0"] = (7, 11)
    client.request("PUT", "/model-prices", body={"model": "model-0", "price": {"prompt": 7, "completion": 11}})
    extra = {**rows[-2], "timestamp": datetime.now(timezone.utc).isoformat(),
             "tokens": {"input_tokens": 271, "output_tokens": 19}, "failed": False, "failure": "", "status_code": 200}
    added = client.request("POST", "/usage/import", body=import_payload([extra]))[0]
    require(added["added"] == 1, "post-freeze write was not accepted")
    current = rows + [extra]
    check_totals(client.request("GET", "/dashboard-summary", {"range": "all"})[0]["usage"], current, prices, "total_cost")

    data = bytearray()
    chunks = 0
    require(re.fullmatch(r"[0-9a-f]{64}", job["version"]), "export lacks a transport-safe version token")
    query = {"id": job["id"], "chunk": "true", "version": job["version"], "offset": 0, "length": 65537}
    while len(data) < job["body_bytes"]:
        query["offset"] = len(data)
        chunk = client.request("GET", "/dashboard-events-export-download", query)[0]
        raw = base64.b64decode(chunk["data"], validate=True)
        require(chunk["offset"] == len(data) and chunk["total"] == job["body_bytes"] and chunk["version"] == job["version"] and chunk["etag"] == job["etag"], "chunk identity/offset drifted")
        require(0 < len(raw) <= query["length"] and f"{zlib.crc32(raw):08x}" == chunk["checksum_crc32"], "chunk size/CRC32 mismatch")
        if chunks == 0:
            retry = client.request("GET", "/dashboard-events-export-download", query)[0]
            require(retry == chunk, "retry changed frozen bytes")
            legacy = client.request("GET", "/dashboard-events-export-download", {**query, "version": job["etag"]})[0]
            require(legacy == chunk, "escaped legacy ETag no longer identifies the same file")
        data.extend(raw)
        chunks += 1
    exported = json.loads(data)
    require(len(exported) == 5000 and all(row["model"] == "model-0" for row in exported), "export changed filtering or row count")
    require(any(row["tokens"]["input_tokens"] == 9007199254740993 for row in exported), "export lost int64 precision")
    for row in exported:
        expected_cost = (row["tokens"]["input_tokens"] * old_prices["model-0"][0] + row["tokens"]["output_tokens"] * old_prices["model-0"][1]) / 1_000_000
        require(math.isclose(row["cost_usd"], expected_cost, rel_tol=2e-12, abs_tol=1e-12), "export price snapshot changed")
    for override, status in [({"version": "wrong-version"}, 412), ({"offset": job["body_bytes"] + 1}, 416), ({"length": 262145}, 400), ({"length": 0}, 400)]:
        client.request("GET", "/dashboard-events-export-download", {**query, **override}, status=status)
    client.request("DELETE", "/dashboard-events-export-jobs", {"id": job["id"]})
    client.request("GET", "/dashboard-events-export-download", query, status=404)
    report["export"] = {"records": len(exported), "chunks": chunks, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest(),
                        "frozen_prices": True, "int64_precision": True, "retry_identical": True, "validation_statuses": [412, 416, 400, 404]}

    print("Checking full backup chunks, frozen history, authentication and import compatibility", flush=True)
    full_query = {"id": full_job["id"], "chunk": "true", "version": full_job["version"], "offset": 0, "length": 262144}
    require(re.fullmatch(r"[0-9a-f]{64}", full_job["version"]), "backup lacks a transport-safe version")
    client.request("GET", "/usage/export-download", full_query, auth=False, status=(401, 403))
    client.request("GET", "/dashboard-events-export-download", full_query, status=404)
    client.request("DELETE", "/dashboard-events-export-jobs", {"id": full_job["id"]}, status=404)
    backup_data = bytearray()
    backup_chunks = 0
    while len(backup_data) < full_job["body_bytes"]:
        full_query["offset"] = len(backup_data)
        chunk = client.request("GET", "/usage/export-download", full_query)[0]
        raw = base64.b64decode(chunk["data"], validate=True)
        require(chunk["offset"] == len(backup_data) and chunk["total"] == full_job["body_bytes"] and chunk["version"] == full_job["version"], "backup chunk version/offset changed")
        require(len(raw) == min(full_query["length"], full_job["body_bytes"] - len(backup_data))
                and f"{zlib.crc32(raw):08x}" == chunk["checksum_crc32"], "backup chunk size/CRC32 mismatch")
        if backup_chunks == 0:
            require(client.request("GET", "/usage/export-download", full_query)[0] == chunk, "backup retry changed bytes")
        backup_data.extend(raw)
        backup_chunks += 1
    frozen_backup = json.loads(backup_data)
    require(frozen_backup["version"] == 1 and frozen_backup["detail_count"] == len(rows), "full backup format or count changed")
    check_totals(frozen_backup["usage"], rows, old_prices, None)
    backup_counts = {key: sum(len(model.get(key, [])) for api in frozen_backup["usage"]["apis"].values() for model in api["models"].values()) for key in ("details", "accounting")}
    require(backup_counts["details"] == 10000 and sum(backup_counts.values()) == len(rows), "full backup lost archived rows or included post-freeze writes")
    require(b"9007199254740993" in backup_data, "full backup lost int64 precision")
    frozen_cost = math.fsum((row["tokens"]["input_tokens"] * old_prices[row["model"]][0]
                           + row["tokens"]["output_tokens"] * old_prices[row["model"]][1]) / 1_000_000 for row in rows)
    require(math.isclose(math.fsum(frozen_backup["usage"]["cost_by_day"].values()), frozen_cost, rel_tol=2e-11), "full backup cost series changed after repricing")
    duplicate_backup = client.request("POST", "/usage/import", body=frozen_backup)[0]
    report["usage_backup_reimport"] = duplicate_backup
    require(duplicate_backup["added"] == 0 and duplicate_backup["skipped"] == len(rows)
            and duplicate_backup["total_requests"] == len(current), "backup re-import duplicated or lost history")
    for override, status in [({"version": "wrong-version"}, 412), ({"offset": full_job["body_bytes"]}, 416), ({"length": 0}, 400)]:
        client.request("GET", "/usage/export-download", {**full_query, **override}, status=status)
    client.request("DELETE", "/usage/export-jobs", {"id": full_job["id"]})
    client.request("GET", "/usage/export-download", full_query, status=404)
    report["usage_backup"] = {"records": len(rows), **backup_counts, "bytes": len(backup_data), "chunks": backup_chunks,
                              "sha256": hashlib.sha256(backup_data).hexdigest(), "frozen_prices_and_history": True,
                              "int64_precision": True, "retry_identical": True, "reimport": duplicate_backup,
                              "validation_statuses": [401, 404, 412, 416, 400]}

    print("Checking full-history export, graceful restart and durable-tail crash recovery", flush=True)
    backup = client.request("GET", "/usage/export")[0]
    check_totals(backup["usage"], current, prices, None)
    counts = {key: sum(len(model.get(key, [])) for api in backup["usage"]["apis"].values() for model in api["models"].values()) for key in ("details", "accounting")}
    require(counts["details"] == 10000 and sum(counts.values()) == len(current), "full export lost archived history")
    command("docker", "restart", "--time", "20", container)
    client.rebind(container)
    client.ready()
    check_totals(client.request("GET", "/dashboard-summary", {"range": "all"})[0]["usage"], current, prices, "total_cost")
    tail = {**extra, "model": "model-1", "timestamp": datetime.now(timezone.utc).isoformat(),
            "tokens": {"input_tokens": 317, "output_tokens": 23}}
    require(client.request("POST", "/usage/import", body=import_payload([tail]))[0]["added"] == 1, "durable tail was not accepted")
    current.append(tail)
    # Verify the announced durable boundary before SIGKILL. This deliberately
    # does not claim that records before flush/fsync survive a power failure.
    deadline = time.monotonic() + 45
    while True:
        health = client.request("GET", "/health")[0]
        storage = health["storage"]
        require(not storage.get("last_error"), "storage reported a persistence error")
        if storage.get("write_records_total", 0) >= 1 and storage.get("last_sync_at") and not any(storage.get(key, 0) for key in ("pending_buffered_records", "pending_unsynced_records", "write_queue_length")):
            break
        require(time.monotonic() < deadline, "storage did not reach its durable boundary")
        time.sleep(0.2)
    command("docker", "kill", "--signal", "KILL", container)
    command("docker", "start", container)
    client.rebind(container)
    recovered = client.ready()
    check_totals(client.request("GET", "/dashboard-summary", {"range": "all"})[0]["usage"], current, prices, "total_cost")
    require(recovered["detail_count"] == 10000, "restart changed visible detail count")
    report["recovery"] = {"total_requests": len(current), "details": 10000, "accounting": len(current) - 10000,
                          "graceful_restart": True, "sigkill_after_durable_boundary": True, "new_tail_records_after_restart": 1,
                          "storage_before_sigkill": storage}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plugin", required=True, type=Path, help="freshly built native Linux shared library")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--records", type=int, default=32000)
    parser.add_argument("--browser", action="store_true", help="also check the real CPA page with Playwright and Chromium")
    args = parser.parse_args()
    require(12000 <= args.records <= 100000 and args.records % 8 == 0, "records must be a multiple of 8 between 12000 and 100000")
    plugin = args.plugin.resolve(strict=True)
    root = Path(__file__).resolve().parent.parent
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    report = {"passed": False, "image": CPA_IMAGE, "plugin_sha256": sha256(plugin), "records": args.records,
              "cpus": 2, "memory_bytes": 2 << 30, "requests": [], "started_at": datetime.now(timezone.utc).isoformat()}
    report["source_sha256"] = {str(path.relative_to(root)): sha256(path) for path in sorted((root / "go").rglob("*")) if path.is_file() and path.suffix in (".go", ".mod", ".sum", ".js", ".html", ".css")}
    report["script_sha256"] = sha256(Path(__file__))
    report["docker_server"] = json.loads(command("docker", "version", "--format", "{{json .Server}}"))
    container = network = temporary = None
    key, client_key = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    name = "cpa-validation-" + secrets.token_hex(6)
    try:
        # Colima commonly shares /Users but not the host's /private/tmp.
        # Keep bind-mount sources under the already-shared repository path.
        work_root = root / "tests/performance/.work"
        work_root.mkdir(parents=True, exist_ok=True)
        temporary = tempfile.TemporaryDirectory(prefix="cpa-stock-validation-", dir=work_root)
        temp = Path(temporary.name)
        try:
            (temp / "plugin").mkdir()
            (temp / "data").mkdir()
            shutil.copyfile(plugin, temp / "plugin" / "usage-dashboard-zduu.so")
            config = (root / "tests/performance/config.yaml").read_text()
            config = config.replace("isolated-performance-test-only", key).replace("isolated-performance-client-only", client_key)
            (temp / "config.yaml").write_text(config)
            (temp / "config.yaml").chmod(0o600)
            (output / "config.redacted.yaml").write_text(config.replace(key, "<temporary-management-key>").replace(client_key, "<temporary-client-key>"))
            # No upstreams are configured and external price/rate fetches are
            # disabled. A normal isolated bridge is needed for published ports
            # on Docker versions that suppress them on internal networks.
            network = command("docker", "network", "create", name)
            require(re.fullmatch(r"[0-9a-f]{64}", network), "unexpected Docker network ID")
            container = command("docker", "create", "--name", name, "--network", network, "--cpus", "2", "--memory", "2g",
                                "-p", "127.0.0.1::8317", "-v", f"{temp / 'config.yaml'}:/CLIProxyAPI/config.yaml",
                                "-v", f"{temp / 'data'}:/performance-data", "-v", f"{temp / 'plugin'}:/performance:ro", CPA_IMAGE)
            require(re.fullmatch(r"[0-9a-f]{64}", container), "unexpected Docker container ID")
            command("docker", "start", container)
            port = command("docker", "port", container, "8317/tcp").rsplit(":", 1)[1]
            client = Client(int(port), key, report["requests"])
            client.ready()
            anchor, rows = fixture(args.records)
            report["fixture_anchor"] = anchor.isoformat()
            report["fixture_sha256"] = hashlib.sha256(json.dumps(import_payload(rows), sort_keys=True).encode()).hexdigest()
            run_checks(client, container, rows, anchor, report)
            if args.browser:
                print("Checking the real CPA resource page and browser chunk downloads", flush=True)
                browser_script = root / "scripts/browser-stock-cpa.cjs"
                result = subprocess.run(["node", str(browser_script)], input=json.dumps({"base": client.base, "key": key, "records": len(rows) + 2}),
                                        text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
                require(result.returncode == 0, "stock CPA browser validation failed: " + result.stderr)
                report["browser"] = json.loads(result.stdout)
                report["browser_script_sha256"] = sha256(browser_script)
            report["container_snapshot_not_performance_gate"] = json.loads(command("docker", "stats", "--no-stream", "--format", "{{json .}}", container))
            report["passed"] = True
        finally:
            if container and re.fullmatch(r"[0-9a-f]{64}", container):
                report["container_state"] = json.loads(command("docker", "inspect", "--format", "{{json .State}}", container))
    except Exception as error:
        report["error"] = str(error).replace(key, "<redacted>").replace(client_key, "<redacted>")
        raise
    finally:
        if container and re.fullmatch(r"[0-9a-f]{64}", container):
            logs = command("docker", "logs", container, check=False)
            (output / "cpa.log").write_text(logs.replace(key, "<redacted>").replace(client_key, "<redacted>") + "\n")
            command("docker", "rm", "--force", container, check=False)
        if network and re.fullmatch(r"[0-9a-f]{64}", network):
            command("docker", "network", "rm", network, check=False)
        if temporary:
            temporary.cleanup()
        report["finished_at"] = datetime.now(timezone.utc).isoformat()
        (output / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(f"PASS: stock CPA validation; evidence: {output}", flush=True)


if __name__ == "__main__":
    main()
