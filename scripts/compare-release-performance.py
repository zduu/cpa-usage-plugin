#!/usr/bin/env python3
"""Compare release components with identical Go toolchains and shared fixtures.

Python 3.12+, Go and a C compiler are required. Outputs contain all samples,
source hashes and environment metadata. No branch, index or tag is changed.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile

BENCHMARKS = (
    "Benchmark(SummaryWithoutDetails100k|SummaryRange7d100k|QueryEvents100k|"
    "QueryEventsColdModelIndex100k|QueryEventsCached100k|QueryAPIDetail100k|"
    "RecordRetainedHistory|PerformanceMixed|ReleaseExportFile)$"
)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--refs", nargs="+", default=["v2.6.4", "967915b"])
    parser.add_argument("--count", type=int, default=3)
    parser.add_argument("--benchtime", default="300ms")
    args = parser.parse_args()
    if args.count < 3:
        parser.error("at least three samples are required")
    root = Path(__file__).resolve().parent.parent
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    toolchain = subprocess.check_output(["go", "env", "GOVERSION"], cwd=root / "go", text=True).strip()
    env = dict(os.environ, GOTOOLCHAIN=toolchain)
    metadata = {"toolchain": toolchain, "platform": platform.platform(), "machine": platform.machine(),
                "count": args.count, "benchtime": args.benchtime, "benchmarks": BENCHMARKS, "sources": {}}
    try:
        with tempfile.TemporaryDirectory(prefix="cpa-release-compare-") as temp:
            # Freeze the working copy before running any revision.
            candidate = Path(temp) / "candidate" / "go"
            shutil.copytree(root / "go", candidate, ignore=shutil.ignore_patterns("*.test"))
            revisions = []
            for index, ref in enumerate(args.refs):
                commit = subprocess.check_output(["git", "rev-parse", "--verify", ref + "^{commit}"], cwd=root, text=True).strip()
                name = f"baseline-{index}"
                dest = Path(temp) / name
                raw = subprocess.check_output(["git", "archive", commit, "go"], cwd=root)
                with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
                    archive.extractall(dest, filter="data")
                for fixture in ["performance_workload_test.go", "release_export_benchmark_test.go"]:
                    shutil.copy2(candidate / fixture, dest / "go" / fixture)
                revisions.append((name, ref, commit, dest / "go"))
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            revisions.append(("candidate", "working-copy", head, candidate))
            for name, ref, commit, source in revisions:
                metadata["sources"][name] = {"ref": ref, "commit": commit, "sha256": {
                    str(p.relative_to(source)): hashlib.sha256(p.read_bytes()).hexdigest()
                    for p in sorted(source.rglob("*")) if p.is_file() and p.suffix in (".go", ".mod", ".sum", ".js", ".html", ".css")
                }}
                cmd = ["go", "test", "-run", "^$", "-bench", BENCHMARKS, "-benchmem",
                       "-benchtime=" + args.benchtime, "-count=" + str(args.count)]
                print(f"Benchmarking {name} ({ref})", flush=True)
                with (output / (name + "-bench.txt")).open("w") as stream:
                    subprocess.run(cmd, cwd=source, env=env, stdout=stream, stderr=subprocess.STDOUT, check=True)
    finally:
        (output / "manifest.json").write_text(json.dumps(metadata, indent=2) + "\n")


if __name__ == "__main__":
    main()
