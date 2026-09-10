# 2026-09-10 发布审查证据

对应[审查报告](../../../../docs/issues/2026-09-10-release-readiness-review.md)。被审查提交、环境及源码 hash 见 `manifest.json`。业务代码未修改。

`v2.6.4-bench.txt`、`HEAD-bench.txt`、`B1-bench.txt` 分别为正式版、候选版及完整功能基线的原始组件输出。三个版本均使用 Go 1.26.6，按 B0 → HEAD → B1 顺序运行，没有同时执行其他测试或 benchmark；未绑核，也未控制机器的其他后台活动。每项 3 次、300ms，属于本机诊断对比，不是容器资源验收。

在各提交的独立检出目录 `go/` 执行：

```sh
GOTOOLCHAIN=go1.26.6 go test -run '^$' \
  -bench 'Benchmark(QueryAPIDetail100k|SummaryRange7d100k|QueryEventsCached100k|QueryEvents100k|QueryEventsColdModelIndex100k|RecordRetainedHistory)$' \
  -benchmem -count=3 -benchtime=300ms
```

查询 fixture 为各版未修改的 `buildBenchmarkStats(100000)`，4 个提供商/模型组、时间起点取运行时钟减 7 天。写入基准是仓库现有 `BenchmarkRecordRetainedHistory`，按 1/20 个模型预填明细后连续写入；各次迭代次数不同，账本会随写入增长。不能把 ns/op 当作请求分位数，也不能把 B/op 当作驻留堆或 RSS。

`repro_test.go.txt` 为审查专用的两个顶层复现用例，未加入常规测试目录；`HEAD-repro.txt` 为当前实现失败输出，`baseline-repro.txt` 仅运行首个用例，确认快照覆盖是已有缺陷。第二项依赖新增 Accounting 字段，无法用于 B0。用下面的 overlay 方法运行当前代码，工作区源码不会被修改；当前预期退出码为 1，修复后应变为 0。

从仓库根目录执行：

```sh
python3 - <<'PY'
import json
import pathlib
import subprocess
import tempfile

root = pathlib.Path.cwd()
evidence = root / 'tests/performance/results/20260910-release-review'
with tempfile.TemporaryDirectory(prefix='cpa-review-overlay-') as temporary:
    overlay = pathlib.Path(temporary) / 'overlay.json'
    overlay.write_text(json.dumps({'Replace': {
        str(root / 'go/zz_release_audit_test.go'): str(evidence / 'repro_test.go.txt')
    }}))
    result = subprocess.run([
        'go', 'test', '-overlay', str(overlay), '-run', '^TestAudit', '-count=1', '-v'
    ], cwd=root / 'go')
    raise SystemExit(result.returncode)
PY
```

归档恢复用例保存的是 `mergeSnapshotLocked` 实际产生的日志记录，在独立目录只重放该日志，模拟日志完成而新快照未完成时的恢复，不会触及用户数据。快照用例也只操作测试临时目录。

`go-race.txt`、`purego-race.txt`、`js-tests.txt` 是现有套件通过记录，与上述额外复现失败分开保存。完整发布、长期性能和全部故障场景未由本次审查验收。
