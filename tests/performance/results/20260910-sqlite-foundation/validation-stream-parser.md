# 流式迁移解析与现有快照恢复 Bug 修复

2026-09-10，B1=`967915b` 加未提交工作区修改，Go 1.26.6。前一阶段见 [安全升级与分块暂存](validation-go1266.md)；该文件的镜像/源码 hash 是前一检查点，不代表本轮源码。

## 已实现

`ScanMigrationSource` 从已验证的 SQLite 暂存数据中逐项解析：

- 快照按全局、API、模型、明细、账本、provider 和时间序列逐项输出，不先展开整个模型或整个快照。
- 输出保留原始值及精确字节区间，并用旧类型校验；int64 最大值不经过 float64。全局/模型总量仍独立输出，不用现存明细重算覆盖，因此没有丢弃缺少逐请求信息的历史汇总。
- 容器开始/结束、空数组、null、重复成员顺序及字段 Unicode 大小写匹配保持可重建；API、模型和时间桶键不做字段名规范化。
- 快照版本和时间可以出现在 `usage` 之后；完整解析成功后才返回有效头部。v1 缓存 token 转换留给后续协调层，避免边读边按错误版本计价。
- JSONL 逐行分类真实记录、`metadata_only` 和无效行；相同真实请求不在解析时合并，损坏行原字节仍可定位。该统计是物理输入行数，不是迁移后的请求总数。
- 取消和下游错误停止解析。读到 EOF 并核验暂存校验和，拒绝尾随 JSON、未来版本和 int64 溢出。统一相对路径解析，修复“相对路径能暂存却不能再读取”的接口不一致。

消费者必须在磁盘上暂存/协调这个有序输出，并在整次扫描成功后才能激活；**本轮没有实现磁盘解析索引、解析阶段事务游标、快照/日志重叠协调、旧残差计算、元数据补充应用或后端切换**。不能将成功解析当作迁移完成。重复成员/null 的最终合并语义也需由消费者按旧解码规则执行，不能把每个容器开始都简单理解为清空。

内存边界是“单条记录/单个值 + 解析缓冲”，不是对任意超大字段的固定 RSS 保证。单模型 20,000 条、约 20 MiB 测试验证：首条记录交付前读取不超过 16 KiB；消费者主动停止后没有继续读取整个模型。这证明增量交付，而不是全流程 CPU/RSS benchmark。首次迁移仍承担暂存、校验和解析 I/O。

## 当前线上路径的 Bug 修复证据

`loadStorageSnapshotLocked` 原本先恢复计数、最后才解析 `generated_at`。新增测试在修复前复现：无效时间返回错误，但实时统计已从 0 变为 99 条/100 token；未来和负版本快照也被接受。

现在共用 `validateStorageSnapshotHeader`，先验证版本及时间，再恢复或合并统计。头部不兼容时，初始化不启动 writer、不执行保留清理，并清空写入目标，避免 `Close` 再将内存快照写回该目录。错误通过现有存储状态报告；不静默宣称持久化成功。合法的 v0/v1/v2 快照保持兼容。

回归还检查失败后关闭插件不会覆盖原文件，过期分片也保留供恢复。此保护针对启动/配置装载时发现的无效头部，不是对正在使用的数据目录被其他程序改写的全局防护。

## 验证

新增 7 项解析回归、2 项现有快照恢复回归；加上已有 13 项账本、8 项暂存，共 30 项相关顶层测试。测试重建输出后与旧 `json.Unmarshal` 的结构逐字段比较，包含总量高于明细、逐条账本、provider、时间桶、重复字段和版本顺序；JSONL 则与现有读取函数对照。

```sh
cd go
go test -race ./...
go vet ./...
go test -race -tags sqlite_purego -run '^Test(SQLite(Ledger|Migration)|StorageSnapshotInvalidHeader)' -count=1
go vet -tags sqlite_purego ./...
```

全量默认 race/vet 通过（12.659s）；纯 Go 相关 race/全量 vet 通过（9.673s）。取消/校验和与快照头部保护额外做 30 次 race 重复，通过（2.178s）。macOS arm64 两驱动 c-shared 构建及 stock CPA SDK 装载通过；Linux arm64 glibc 容器 30 项相关回归和 stock CPA SDK 装载通过，构建时 SYMBOLIC 断言通过。容器回归未启用 race。历史 144 项前端结果没有冒充新跑结果；本轮未修改前端代码。完整五平台、容器资源目标、24 小时 soak 及业务迁移仍未验收。

Linux 镜像 `cpa-usage-performance:sqlite-parser`：`sha256:89aebb6c2a0b318d93187ad4d584d6fd704204c88a85103fc3a578d1cbc3730e`，构建器/CPA digest 与前一检查点一致。macOS 未 strip 库 SHA-256：CGO `cdf428731b1d2fbf327a7a3118426f1110efe13f6f90f6046f6f756e45fbc497`，纯 Go `2fcb36b9084109db4dcef2de8cedbb54fc300e18e563babf0e32ee96f3cc667c`。

```sh
docker build --build-arg GOPROXY=https://goproxy.cn,direct -f tests/performance/Dockerfile -t cpa-usage-performance:sqlite-parser .
docker run --rm --entrypoint /performance/performance.test cpa-usage-performance:sqlite-parser -test.run '^Test(SQLite(Ledger|Migration)|StorageSnapshotInvalidHeader)' -test.v
docker run --rm --entrypoint /performance/stock-host cpa-usage-performance:sqlite-parser /performance/usage-dashboard-zduu.so
```

## 源码校验和

| 文件 | SHA-256 |
| --- | --- |
| go/stats.go | 39a396eb44f137d6be39dd75f85b38ce6fd43939eed9dc75b9bd0b13add30a30 |
| go/storage_snapshot_validation_test.go | b61d8989e94b1cd0fbdca168ef79796060f19adbd61a587c3053bb56062136d6 |
| go/sqlite_migration.go | 4edb6daffd5831dac8534fcda543af83cbe87a0ab8a7bd94884535fb4d6dd54e |
| go/sqlite_migration_parse.go | a76186003689fb391b4adadaad04840accf3a87b7fe0d968a2cc0cf26cbe2b59 |
| go/sqlite_migration_parse_test.go | 493c2286c77b55e2059fe13bbd5833871b490370cbcb353603a59e911de79585 |
