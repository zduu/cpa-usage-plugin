#!/usr/bin/env python3
"""Validate a released -> candidate upgrade on an isolated stock CPA container.

The first phase runs the previously released shared library so that its own
persistence path produces the data directory: a snapshot whose per-request
details were truncated by ``max_details_per_model``, plus the same day's JSONL
shard that still holds every request. The host is then stopped cleanly, the
shared library is swapped for the candidate, and the same data directory is
reopened. Restoring must not count the already-counted requests a second time,
while requests that arrive after the upgrade must still be counted.

Uses generated data, temporary credentials, a dedicated network and a random
loopback port. Only the container and network created by this run are removed.
This is a correctness check, not the CPU/RSS or 24-hour performance gate.
"""
import argparse
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import re
import secrets
import shutil
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
STORAGE = "/performance-data/usage-statistics"


def load_validator():
    spec = importlib.util.spec_from_file_location("validate_stock_cpa", ROOT / "scripts" / "validate-stock-cpa.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def snapshot_shape(path):
    """Report the persisted snapshot shape without assuming either version."""
    payload = json.loads(path.read_text())
    models = [model for api in payload["usage"].get("apis", {}).values() for model in api.get("models", {}).values()]
    return {
        "version": payload.get("version"),
        "generated_at": payload.get("generated_at"),
        "total_requests": payload["usage"].get("total_requests"),
        "visible_details": sum(len(model.get("details") or []) for model in models),
        "accounting_entries": sum(len(model.get("accounting") or []) for model in models),
        "has_accounting_field": any("accounting" in model for model in models),
    }


def shutdown(validator, container):
    validator.command("docker", "stop", "--time", "30", container)
    state = json.loads(validator.command("docker", "inspect", "--format", "{{json .State}}", container))
    validator.require(not state["Running"], "container did not stop")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--old-plugin", required=True, type=Path, help="shared library of the released version")
    parser.add_argument("--new-plugin", required=True, type=Path, help="shared library of the candidate")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--records", type=int, default=1200, help="records imported before the upgrade")
    parser.add_argument("--after-records", type=int, default=40, help="records imported after the upgrade")
    parser.add_argument("--max-details", type=int, default=100, help="detail cap that forces truncation")
    args = parser.parse_args()

    validator = load_validator()
    old_plugin = args.old_plugin.resolve(strict=True)
    new_plugin = args.new_plugin.resolve(strict=True)
    validator.require(0 < args.max_details < args.records, "the detail cap must truncate the imported records")
    validator.require(args.after_records > 0, "records must be imported after the upgrade")

    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    report = {
        "passed": False,
        "image": validator.CPA_IMAGE,
        "old_plugin_sha256": validator.sha256(old_plugin),
        "new_plugin_sha256": validator.sha256(new_plugin),
        "records": args.records,
        "after_records": args.after_records,
        "max_details_per_model": args.max_details,
        "started_at": datetime.now(timezone.utc).isoformat(),
        "phases": [],
    }
    report["source_sha256"] = {str(path.relative_to(ROOT)): validator.sha256(path)
                              for path in sorted((ROOT / "go").rglob("*"))
                              if path.is_file() and path.suffix in (".go", ".mod", ".sum", ".js", ".html", ".css")}
    report["script_sha256"] = validator.sha256(Path(__file__))
    report["old_script_sha256"] = validator.sha256(ROOT / "scripts" / "validate-stock-cpa.py")

    container = network = temporary = None
    key, client_key = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    name = "cpa-upgrade-" + secrets.token_hex(6)
    plugin_dir = storage_dir = None
    try:
        work_root = ROOT / "tests/performance/.work"
        work_root.mkdir(parents=True, exist_ok=True)
        temporary = tempfile.TemporaryDirectory(prefix="cpa-upgrade-validation-", dir=work_root)
        temp = Path(temporary.name)
        (temp / "plugin").mkdir()
        (temp / "data").mkdir()
        plugin_dir = temp / "plugin"
        storage_dir = Path(temp / "data") / Path(STORAGE).name
        shutil.copyfile(old_plugin, plugin_dir / "usage-dashboard-zduu.so")

        config = (ROOT / "tests/performance/config.yaml").read_text()
        config = config.replace("max_details_per_model: 5000", f"max_details_per_model: {args.max_details}")
        # Keep the shutdown snapshot the only one so the upgrade restores exactly
        # the file the released version leaves behind.
        config = config.replace("storage_snapshot_record_interval: 1000", "storage_snapshot_record_interval: 100000000")
        validator.require(f"max_details_per_model: {args.max_details}" in config, "detail cap was not applied to the config")
        config = config.replace("isolated-performance-test-only", key).replace("isolated-performance-client-only", client_key)
        (temp / "config.yaml").write_text(config)
        (temp / "config.yaml").chmod(0o600)
        (output / "config.redacted.yaml").write_text(config.replace(key, "<temporary-management-key>").replace(client_key, "<temporary-client-key>"))

        network = validator.command("docker", "network", "create", name)
        container = validator.command("docker", "create", "--name", name, "--network", network, "--cpus", "2", "--memory", "2g",
                                      "-p", "127.0.0.1::8317", "-v", f"{temp / 'config.yaml'}:/CLIProxyAPI/config.yaml",
                                      "-v", f"{temp / 'data'}:/performance-data", "-v", f"{plugin_dir}:/performance:ro",
                                      validator.CPA_IMAGE)
        validator.require(re.fullmatch(r"[0-9a-f]{64}", container), "unexpected Docker container ID")

        print("Phase 1: released plugin writes the data directory", flush=True)
        validator.command("docker", "start", container)
        client = validator.Client(int(validator.command("docker", "port", container, "8317/tcp").rsplit(":", 1)[1]), key, [])
        client.ready()
        anchor, rows = validator.fixture(args.records)
        imported = client.request("POST", "/usage/import", body=validator.import_payload(rows))[0]
        validator.require(imported["added"] == len(rows) and imported["total_requests"] == len(rows),
                          "released plugin did not publish every record")
        before = client.request("GET", "/usage")[0]
        validator.check_totals(before["usage"], rows, {"model-0": (0, 0), "model-1": (0, 0)}, None)
        health = client.request("GET", "/health")[0]
        # The fixture uses two models, and the visible-detail budget is per model.
        visible_limit = 2 * args.max_details
        validator.require(health["detail_count"] == visible_limit,
                          f"detail cap did not truncate: {health['detail_count']} visible, want {visible_limit}")
        shutdown(validator, container)
        legacy = snapshot_shape(Path(temp / "data") / f"{Path(STORAGE).name}/snapshot.json")
        validator.require(not legacy["has_accounting_field"] and legacy["visible_details"] == visible_limit
                          and legacy["total_requests"] == args.records,
                          f"released plugin did not leave the expected truncated snapshot: {legacy}")
        report["phases"].append({"phase": "released", "before": before["usage"], "health_detail_count": health["detail_count"],
                                 "snapshot": legacy})

        print("Phase 2: candidate reopens the same directory", flush=True)
        shutil.copyfile(new_plugin, plugin_dir / "usage-dashboard-zduu.so")
        validator.command("docker", "start", container)
        client.rebind(container)
        client.ready()
        after = client.request("GET", "/usage")[0]
        validator.check_totals(after["usage"], rows, {"model-0": (0, 0), "model-1": (0, 0)}, None)
        report["phases"].append({"phase": "upgraded", "after": after["usage"], "health": client.request("GET", "/health")[0]})

        print("Phase 3: requests that arrive after the upgrade are still counted", flush=True)
        extra_anchor, extra_rows = validator.fixture(args.after_records)
        for index, row in enumerate(extra_rows):
            # Older than every pre-upgrade record but still inside retention, so
            # the new requests are unique and are not dropped as expired.
            row["timestamp"] = (extra_anchor - timedelta(hours=200) - timedelta(minutes=index)).isoformat()
            row["api_key"] = f"post-upgrade-client-{index % 3}"
        merged = rows + extra_rows
        delta = client.request("POST", "/usage/import", body=validator.import_payload(extra_rows))[0]
        validator.require(delta["added"] == len(extra_rows), "post-upgrade records were not added")
        grown = client.request("GET", "/usage")[0]
        validator.check_totals(grown["usage"], merged, {"model-0": (0, 0), "model-1": (0, 0)}, None)
        shutdown(validator, container)
        report["phases"].append({"phase": "post-upgrade", "after": grown["usage"]})

        print("Phase 4: another restart keeps the same totals", flush=True)
        validator.command("docker", "start", container)
        client.rebind(container)
        client.ready()
        restarted = client.request("GET", "/usage")[0]
        validator.check_totals(restarted["usage"], merged, {"model-0": (0, 0), "model-1": (0, 0)}, None)
        report["phases"].append({"phase": "restarted", "after": restarted["usage"],
                                 "health": client.request("GET", "/health")[0]})
        report["passed"] = True
    except Exception as error:
        report["error"] = str(error).replace(key, "<redacted>").replace(client_key, "<redacted>")
        raise
    finally:
        if container and re.fullmatch(r"[0-9a-f]{64}", container):
            logs = validator.command("docker", "logs", container, check=False)
            (output / "cpa.log").write_text(logs.replace(key, "<redacted>").replace(client_key, "<redacted>") + "\n")
            validator.command("docker", "rm", "--force", container, check=False)
        if network and re.fullmatch(r"[0-9a-f]{64}", network):
            validator.command("docker", "network", "rm", network, check=False)
        if temporary:
            temporary.cleanup()
        report["finished_at"] = datetime.now(timezone.utc).isoformat()
        (output / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(f"PASS: stock CPA upgrade validation; evidence: {output}", flush=True)


if __name__ == "__main__":
    main()
