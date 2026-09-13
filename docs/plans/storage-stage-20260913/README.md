# 2026-09-13 存储与浏览器验收进展

本轮基线为 `63e14a2`，继承上一轮全部优化。新增能力分为**已接入现有运行时**和**SQLite 接管前的基础能力**，不将后者描述为数据库后端已启用。

## 已接入运行时

1. 日常后台及关闭快照冻结归档分块，并逐记录编码写入。保留 v2 JSON、计数、模型/来源分组、成本及恢复语义；避免把所有归档账本展开成完整 `RequestDetail` 数组，也不构造整份 JSON 缓冲。可见明细与聚合仍需要复制，因此不是恒定内存快照。
2. 冻结后的账本块在修复、删除或过期压缩时按 256 条复制；普通查询只读，不因读操作复制。冻结快照持有自己的块头及旧数据，追加不改变快照长度。新增并发 race 回归核对完整旧/新编码语义、错误处理及恢复结果。
3. 身份字典不再把 12 个字符串组成的完整身份同时保存为大 map key 和独立 value。使用进程随机哈希缩小 map key，哈希冲突仍逐字段比较；强制碰撞和过期重建测试确保不会错误合并客户端身份。

## SQLite 接管前的基础能力

- schema 3 增加带版本的 `ledger_state`；从 schema 1/2 自动做增量结构升级，保持记录 ID、generation、revision 和暂存数据。
- `ApplyState` 在同一个事务、同一个 generation 提交请求记录、派生状态与迁移游标。状态插入要求不存在，更新/删除要求匹配 revision。任一冲突、预算超限或失败回滚整个事务；同一 ReadView 读取记录、状态和进度，避免混用数据代次。
- 状态数据仍由调用者解释。它可用于未来存放汇总、残差或配置版本，但本轮**没有实现这些状态的运行时维护和迁移协调**。
- `Backup` 通过 SQLite `VACUUM INTO` 创建包含已提交 WAL 数据的一致独立库。新建私有目录，校验 application/schema、generation、quick_check、外键及 SHA-256，最后发布完成清单；取消或失败不留下可被误认为完成的清单，也不替换已有目录。
- 恢复先验证清单、内容和数据库结构，再复制到同目录临时文件并重新计算 SHA-256，最终使用不覆盖目标的文件链接发布。已有数据库或 WAL/SHM/journal 会拒绝恢复；仅支持恢复到未激活的新路径，不自动切换当前实例。
- 备份仅包括 SQLite 库内数据与暂存/状态表，不包含外部模型价格文件。不得用它替代当前 JSONL 插件的完整备份。Windows 文件系统及断电耐久性仍需目标平台验收。

## 浏览器验证

新增 `scripts/browser-dashboard.cjs` 和只在 `CPA_BROWSER_TEST=1` 时启动的 Go loopback 测试服务。浏览器使用实际嵌入页面与生产管理处理器，没有模拟 API 数据；该服务不是完整 CPA HTTP 服务，不能代替 CPA 管理鉴权链路验证。

真实 Chrome 加载 12000 条请求，快速切换范围/模型后正确保留最终筛选，翻页后筛选不变。导出 4000 条筛选记录时人为注入一次 HTTP 503，验证分块重试、JSON/CSV 内容、成本字段、文件扩展名、`9007199254740993` 大整数精度及任务删除。页面无 JavaScript 异常。原始结果见 `browser.json`；50ms 采样的 JS heap 只是观测峰值，不是浏览器进程 RSS，也不能覆盖所有瞬时尖峰或证明相对上一版更快。

复现：安装 Playwright 与 Chromium 后从仓库根目录运行：

```sh
node scripts/browser-dashboard.cjs
```

也可设置 `CPA_PLAYWRIGHT` 指向已安装的 playwright-core 包、`CPA_CHROME` 指向本机 Chrome 可执行文件。脚本使用临时浏览器配置及 loopback 随机端口，结束后关闭浏览器与测试服务，不读取个人浏览器资料或连接上游。

## 测量与验证

Apple M1 / macOS arm64，Go 1.26.6；每项组件基准 3 次、300ms，中位数。堆探针每个版本/规模重复 3 次。

| 场景 | 本轮前 / 原路径 | 最终候选 | 变化 |
| --- | ---: | ---: | ---: |
| 10 万条历史，GC 后存活堆增量 | 72.79 MiB | 69.89 MiB | -4.0% |
| 百万条历史，GC 后存活堆增量 | 226.80 MiB | 215.79 MiB | -4.9% |
| 10 万条整次快照文件写入分配 | 143.22 MB/op | 44.96 MB/op | -68.6% |
| 10 万条整次快照文件写入耗时 | 134.27 ms | 129.94 ms | -3.2% |
| 百万条快照冻结分配 | 458.78 MB/op | 39.30 MB/op | -91.4% |
| 百万条快照冻结耗时 | 104.05 ms | 9.06 ms | -91.3% |

快照两组基准是在同一候选可执行文件中对照原全量路径与新路径，数据和完整账本语义相同。整次写入包含冻结、编码、文件写入与 fsync；百万条冻结组只测冻结阶段，不能当作整次百万条落盘耗时。分配字节数不是峰值 RSS，CPU/RSS 容器门槛尚未测得。

普通写入仍约 1.6 μs、256 B/op、6 allocs/op；模型数不同和运行次序会影响小幅差异，未将其宣传为提速。查询与写入全部样本保留在 `before-bench.txt`/`candidate-bench.txt`。

上一正式版 B0 的历史对照见[上一轮报告](../release-readiness-20260912.md)：10 万条存活堆为 60.17 MiB，候选本轮仍高于 B0，完整账本的新功能成本没有完全消除；不能将相对本轮前的改善写成所有规模均低于正式版。

通过：完整 Go race、完整 sqlite_purego race、vet、149 项前端测试、更新脚本测试；最后的编码分配微调另以两种驱动跑并发快照专项 race。真实浏览器结果见上文。共享库装载结果另见 `darwin-abi.txt`、`linux-abi.txt`，Linux 构建同时运行新增 SQLite 状态/备份测试。


文件名中的 `before` 对应 `63e14a2` 的隔离归档，`candidate` 为本轮工作区。Go 统一使用模块指定的 1.26.6；根目录不在 Go module 内，系统 `go version` 可能显示 1.26.4，不能据此混算工具链。样本、运行次序、测量源码及最终源码差异、共享库大小/SHA-256 和验证日志校验和见 `manifest.json`。最后审查仅纠正身份字典注释中的字符串数量描述，不影响已测二进制行为。SQLite 双驱动均运行测试，但正式构建仍使用 CGO 驱动。

从 `go/` 目录复现组件测量；前两组堆探针及混合基准还需在 `63e14a2` 的隔离归档中使用同一工具链执行：

```sh
CPA_PERF_PROBE=1 CPA_PERF_MILLION=0 go test -run '^TestPerformanceResourceProbe$' -count=3 -v
CPA_PERF_PROBE=1 CPA_PERF_MILLION=1 go test -run '^TestPerformanceResourceProbe$' -count=3 -v
go test -run '^$' -bench '^Benchmark(PerformanceMixed|RecordRetainedHistory)$' -benchmem -benchtime=300ms -count=3
go test -run '^$' -bench '^BenchmarkStorageSnapshotPipeline$' -benchmem -benchtime=300ms -count=3
go test -run '^$' -bench '^BenchmarkStorageSnapshotCaptureMillion$' -benchmem -benchtime=300ms -count=3
```

基准不加 `-race`；并发正确性另以 `go test -race -count=1 ./...` 和 `go test -race -tags sqlite_purego -count=1 ./...` 验证。测试使用临时目录和生成数据。

## 尚未完成的发布关键路径

SQLite 运行时读写协调、各查询/修复路径的存储错误传播、精确聚合及历史残差迁移、激活状态机仍未完成。当前 JSONL 启动仍读取完整快照；完整管理导入/导出和事件导出冻结也仍有随数据量增长的副本。容器内成对 CPU/RSS、24 小时稳态、最终五平台装载和无损降级仍需继续验收。本轮不修改正式版本号或创建发布 tag。
