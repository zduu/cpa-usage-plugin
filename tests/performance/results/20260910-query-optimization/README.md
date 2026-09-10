# 2026-09-10 查询与复制优化：组件实验

这是组件证据，不是全面优化或正式发布验收。全部重复样本从工具输出逐行整理为
[component-samples.csv](component-samples.csv)，保留迭代次数、ns/op、B/op 和 allocs/op。
没有将一次 benchmark 的迭代均值当作请求 p95/p99。

## 版本与环境

- B0：`ece14ec76bfe6939d35548088ce3e710129128d4`，正式版 `v2.6.4`。
- B1：`967915b23e9d35ff62e0da975b63cf9eab0dead7`，本轮修改前的完整功能代码。
- optimized：B1 加本轮工作区修改，尚未提交；下列 SHA-256 固定被测 Go 源码。
- macOS 26.6.2 / 25G83，Apple M1，darwin/arm64，Go 1.26.4，默认 GOMAXPROCS=8；Node 24.12.0。
- 非容器、未绑核；版本顺序运行，未同时运行其他测试或 benchmark。不是整进程 CPU/RSS 测量。
- 三项主要查询及最终热/冷索引查询分别重复 5 次、400ms；B1 和首轮补充查询/写入、导出对照分别重复 3 次、400ms。混合负载重复 3 次、200ms。
- 查询数据来自各版相同的 `buildBenchmarkStats(100000)`，四个模型/提供商组、最近 7 天、无归档账本；时间来自运行时钟。它不是带账本的长期混合负载。
- B0 的 `stats.go` 和 `main_test.go` 与正式提交分别校验相同，SHA-256 为
  `3e9b40a583f7456b2a6662ce6cacb6466acf16cc99786b5feb0855e41f48e2c1`、
  `30b4ed268b669d0d044d027ebd7ebb91b598ec96e13f6e899a762979d017c895`。

| optimized 文件（相对仓库根） | SHA-256 |
| --- | --- |
| go/stats.go | 986732be7d53576bdf7c259732bafbe04037e8f4ca768b32e2addee350bc5c78 |
| go/accounting.go | cf0df0493df74e5950352d839ff57c8c075d4e030abf0c905fc8f0adb4444bb0 |
| go/dashboard_export_jobs.go | ad9fc58841b01c80753998b481e1e350b9bf6752b6a1885d806cb1f2bdbc792f |
| go/query_optimization_test.go | 6f465f3a5345c165b5a76644a3918732162e68019862c350f2922d23d4710412 |

`optimized-v1` 是尚未加入只读指针筛选的首轮实现，其 `stats.go` SHA-256 为
`e63317d9b9b5d0ee2680b070c0984e85b8efa0901ab48ff5fdf22de9d4fe9ca4`，其他 Go 文件相同。
首轮事件热/冷索引未消除回退，因此继续修改并复测；原样本保留，不与最终 optimized 混算。

## 复现

分别在 B0、B1 和 optimized 检出目录的 `go` 目录执行（不得并行测不同版本）：

```sh
go test -run '^$' -bench 'Benchmark(QueryAPIDetail100k|SummaryRange7d100k|QueryEventsCached100k)$' -benchmem -count=5 -benchtime=400ms
go test -run '^$' -bench 'Benchmark(QueryEvents100k|QueryEventsColdModelIndex100k)$' -benchmem -count=5 -benchtime=400ms
go test -run '^$' -bench 'Benchmark(RecordIncremental|SummaryWithoutDetails100k)$' -benchmem -count=3 -benchtime=400ms
```

混合基准仅在具有 `performance_workload_test.go` 的 B1 和候选执行；B0 不含该测试，不能把“无匹配 benchmark”当作通过：

```sh
go test -run '^$' -bench '^BenchmarkPerformanceMixed$' -benchmem -count=3 -benchtime=200ms
```

本轮 B1 使用改动前已编译的测试二进制，用等价的 `-test.run`、`-test.bench` 等参数运行，避免混入工作区修改。
导出实验在 optimized-v1 执行；`extra_copy=true` 明确加回原先的第二次复制，其他查询/价格路径相同，
所以它是复制策略的受控对照，不是 B0 或完整 B1 的端到端导出比较：

```sh
go test -run '^$' -bench '^BenchmarkEventExportSnapshotCopies100k$' -benchmem -count=3 -benchtime=400ms
go test -race ./...
go vet ./...
```

前端从仓库根执行 `node --test go/dashboard/*.test.js`。

## 审查与限制

- B1 CPU profile 定位到 API 详情逐条堆替换及重复价格查找；profile 含 fixture 初始化，不能把其百分比当业务 CPU 降幅。
- 最近记录改为独立逆序候选扫描；聚合仍正序，保留同来源 Provider 选择及浮点累加顺序。
- 最终事件筛选持锁借用只读指针，只有返回页深拷贝；模型/来源/凭证/客户端筛选及导出保留原语义。
- 价格仅在一次持锁查询内缓存最后一个基础价格，时段/星期/合成时间仍逐条判断；不增加随维度增长的价格缓存。
- 新回归覆盖迟到/同时间戳/归档记录的近期排序、来源 Provider、计价对照、导出 Headers/Correlation/CostUSD 隔离、到期账本容量回收。
- 前端两个旧响应覆盖问题先由测试复现失败，再修复通过；接口详情缓存上限为 32 个选择，不等于全局字节预算。
- Go race 全套、vet、前端 144 项测试及本地 darwin/arm64 c-shared 构建通过。SQLite、跨平台本轮装载、浏览器端到端、容器 RSS/CPU、24 小时 soak 未由本实验验收。
- 不保存临时 CPU/heap 二进制 profile 到仓库；正式 P0/G08 所需完整实验清单和资源产物仍待补齐。
- 混合负载沿用 `performance_workload_test.go` 的种子 20260909、10 万条高/低基数分布。低基数包含归档账本；高基数范围汇总仍约 100ms、约 66.45MB/op，无明显资源改善，不能隐藏这项后续优化重点。混合基准连续写入会改变历史长度，保留全部迭代次数，不把重复均值解读为固定历史下的端到端分位数。
