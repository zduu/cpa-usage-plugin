# SQLite 基础层与驱动对照（2026-09-10）

状态：可测试的存储基础及旧文件分块暂存已实现，**尚未接入运行时存储配置、语义迁移或统计查询**。现有插件仍使用内存/JSONL；不能根据这里的数据库测试宣传插件常驻内存已降低。B0=`v2.6.4`，B1=`967915b`。本页基准为 Go 1.26.4 的历史样本，当前工具链已升级到 1.26.6，新增迁移与安全验证见[本轮记录](validation-go1266.md)。

## 实现与边界

后续已增加[流式解析与快照恢复保护](validation-stream-parser.md)：可以逐项读取暂存的快照/JSONL，区分明细、账本、汇总及元数据；还没有把它们协调成可接管运行时的数据库历史。本页以下基础层测试/基准保持原检查点含义。

- 独立自增记录 ID；相同内容的真实请求不会合并。身份字典在磁盘复用，修改/删除后仅回收本批涉及的孤立身份，不做全表扫描。
- 插入、替换、归档、删除和迁移游标在同一事务提交；更新/删除检查记录版本，迁移检查来源指纹与预期游标。这里只提供迁移基础操作，没有实现旧文件解析和激活状态机。
- 一致读事务立即读取 generation 来固定快照，跨页不受后续修改、删除和插入影响。按时间/API/模型/ID 做键集分页，EXPLAIN 核验索引 seek，而不是读取全部记录后排序。
- 秒、纳秒、原偏移秒数、时区名、零时间及合成时间单独保存；token 在 Go 类型化 JSON 中保留 int64，不经过 float64 或 UnixNano。时区规则仍由后续业务计价层处理。
- 归档字段与现有 accountingRecord 一致；Headers、Stream、Thinking 不进入账本，Correlation、身份和计价所需字段保留。查询期 CostUSD 不持久化。
- 一写两读；配置页缓存总计 4 MiB、关闭 mmap、临时排序落文件、busy timeout 250ms、FULL 同步、读视图最长 30 秒。页缓存预算不等于 SQLite 总内存或进程 RSS。
- 原型上限：256 条/批、8 MiB 编码数据/批或页、1 MiB/记录、512 条/页。超限明确回滚报错，分页按最后实际返回记录继续。**运行时接入前必须完成兼容原有大记录的处理路径，不能拿这些实验上限削减既有功能。**
- CGO 每连接额外设置 4 MiB SQLite 行长度防护；纯 Go 对照尚未具备等价防护，不能直接作为发布选择。
- 新文件私有创建，拒绝非普通文件、未知/未来 schema；拒绝 UNC 路径。WAL 不支持网络文件系统；本轮未证明同路径多个 CPA 实例的业务缓存一致性。
- Checkpoint 显式报告 Busy；它不是备份。仍未实现一致性备份、精确分段汇总/旧残差事务、持久化接受队列、运行时错误传播和全后端适配。

## 驱动与基准

CGO 候选：github.com/mattn/go-sqlite3 v1.14.52（MIT）。纯 Go 对照：modernc.org/sqlite v1.58.0（BSD-3-Clause），使用 `sqlite_purego` 构建标签。
两者实际返回相同 SQLite 3.53.4、source ID `2026-07-24 19:02:57 bf7c7f30031888f4e796e429ab3978879485813aaca6f641c7b33e4e09459bcc`。
直接驱动许可现由 CI 打包时从依赖模块生成 `THIRD_PARTY_NOTICES.txt`；全部传递依赖和发布包许可仍需最终交付检查。

Apple M1 / macOS 26.6.2 / Go 1.26.4 / darwin arm64、GOMAXPROCS 默认 8。两驱动顺序运行，无并行 benchmark 或构建；每项重复三次、300ms。
完整 24 条重复样本见 [driver-samples.csv](driver-samples.csv)，下表为中位数。

| 场景 | CGO | 纯 Go |
| --- | ---: | ---: |
| 128 条批次插入（含 FULL 提交） | 6.369 ms | 6.969 ms |
| 10 万条历史，第一页 100 条 | 1.142 ms | 1.230 ms |
| 从第 50000 条后的键开始，100 条 | 1.160 ms | 1.255 ms |
| 从第 99000 条后的键开始，100 条 | 1.148 ms | 1.242 ms |
| 相同未 strip 构建参数的 arm64 dylib | 9,586,322 B | 12,008,098 B |

批次插入每次是 128 条，不是一次 usage 回调；不能与 B1 单条内存 Record 耗时直接比较。批次基准包含继续增长的磁盘历史；分页 fixture 用批量 SQL 生成 10 万条来单独测查询，带完整身份、头、关联元数据和大 int64 token。它不是 dashboard 查询，也不是冷磁盘读或端到端导出。
两驱动此分布都未呈现随页深度线性增长的解码耗时；这不能替代所有筛选组合的性能验证。CGO 的批次中位数较低且库更小，但重复范围有重叠；没有 CPU/RSS 结果，不因 B/op 略低而判定纯 Go 更省内存。

暂保留 CGO 为原型默认、纯 Go 为构建对照，最终选择仍须补齐五平台装载、原生内存和故障预算。

## Bug 审查证据

1. Windows `C:/...` 原先编码成 `file://C:/...`，盘符成了 URI authority；新增测试先失败，修复为 `file:///C:/...` 后两驱动通过。
2. Linux 原生容器复现关闭后读视图仍可查询：通过 AfterFunc 异步转发关闭信号存在调度间隙。现在读写操作直接派生自数据库生命周期，关闭同步取消，读取也显式检查上下文。
3. 100 次 race 重复检查又发现父截止时间和转发取消竞争，偶发把超时误报为普通取消；修复为保留调用者错误原因。两驱动分别 100 次 race 检查通过，没有放宽测试断言。
4. 原性能 Dockerfile 用 Alpine/musl 构建、在 Debian/glibc CPA 中执行，出现“文件存在但 no such file”。已改为 Go bookworm 镜像，并增加可覆盖的 GOPROXY。默认依赖源超时单独记录为网络问题，不作为驱动不兼容结论。
5. CGO dylib 可见 271 个 SQLite C 符号；纯 Go 对照没有这些符号。Go 的 ELF c-shared 构建使用 Bsymbolic，Docker 构建新增 SYMBOLIC 断言并通过；macOS 两候选均通过 stock CPA SDK 装载。不是所有宿主/平台冲突测试已完成。

本轮 13 项数据库回归覆盖：重启/真实重复、Windows URI、一致视图/版本冲突、迁移原子性/预算、排序/查询计划、连接数/取消/关闭、拒绝其他数据库及未来版本、身份回收/ID 不复用、字节限额分页、引擎版本、进程异常退出、WAL 读视图钉住检查点、写锁取消。

## 复现与源码

```sh
cd go
go test -race ./...
go test -race -tags sqlite_purego -run '^TestSQLiteLedger' -count=1
go test -race -run '^TestSQLiteLedgerReaderBudgetCancellationAndClose$' -count=100
go test -race -tags sqlite_purego -run '^TestSQLiteLedgerReaderBudgetCancellationAndClose$' -count=100
go test -run '^$' -bench '^BenchmarkSQLiteLedger(BatchInsert|Page100k)$' -benchmem -count=3 -benchtime=300ms
go test -tags sqlite_purego -run '^$' -bench '^BenchmarkSQLiteLedger(BatchInsert|Page100k)$' -benchmem -count=3 -benchtime=300ms
```

原生容器（从仓库根执行；镜像内测试不代表已经启用数据库的 CPA 进程资源）：

```sh
docker build --build-arg GOPROXY=https://goproxy.cn,direct -f tests/performance/Dockerfile -t cpa-usage-performance:sqlite-prototype .
docker run --rm --entrypoint /performance/performance.test cpa-usage-performance:sqlite-prototype -test.run '^TestSQLiteLedger' -test.v
docker run --rm --entrypoint /performance/stock-host cpa-usage-performance:sqlite-prototype /performance/usage-dashboard-zduu.so
```

历史构建镜像 `golang:1.26.4-bookworm` 实际 digest 为 `sha256:b305420a68d0f229d91eb3b3ed9e519fcf2cf5461da4bef997bf927e8c0bfd2b`；CPA digest 沿用总计划。当前 Dockerfile 已使用 1.26.6，不能用当前构建命令冒充重现旧工具链样本。
基准当时源码是 B1 加工作区修改，尚未提交。以下为**历史基准快照**的 SHA-256，不是后续 schema 2/安全升级后的当前源码校验和：

| 文件（相对根目录） | SHA-256 |
| --- | --- |
| go/sqlite_ledger.go | b32ab1a67fc78c7195759a48a8216df6dbd06b36f72b33313610d481e99e8c68 |
| go/sqlite_ledger_driver_cgo.go | cd53e1c44f0d7d6c5d2937b72187ca1642d76ccd2f5c983165bef5c336bc8529 |
| go/sqlite_ledger_driver_purego.go | 7814cf2d4170dce6734497c00a11940bb270190fb6fac9ffd9ec534989acfd4f |
| go/sqlite_ledger_test.go | 137f9f07da5b46b1a2b947fc9750ecc329776102bf19a430c6e0eb84394e7668 |
| go/go.mod | 23cd06f0c09ddfb5abac9cd591ee545657aa61cd09cb93204909cb5e677de1ee |
| go/go.sum | a4930aa231914648093477b8553d00d257b336ebe2bab93914b84ce2c3ae2e58 |
| tests/performance/Dockerfile | 0859c8aef84af8bca3a0b8fd18ccb118076d697a04492c43de215877b98ca73c |

历史轮次全量 Go race/vet、前端 144 项及更新脚本 1 项通过；macOS arm64 两驱动已构建装载，macOS amd64 CGO 已交叉构建但未实际运行。Go 1.26.6 基础层镜像 `sha256:bc6af978a414b80c84e81fb3641c6865b733319918b956d3510d09620f0d2965` 的 Linux arm64 原生 13 项数据库回归、关闭行为 100 次重复和 stock CPA SDK 装载均通过。新增迁移代码另行验证，不与这个镜像混同；Windows/Linux amd64、本轮完整 RSS/CPU、语义迁移及 24 小时 soak 均未验收。
