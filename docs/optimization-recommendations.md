# CPA Usage Statistics Plugin 优化建议

本文基于当前项目代码审查，重点覆盖数据持久性、减轻 CPA 服务压力、看板查询性能和发布说明流程。建议按“先保护数据、再降低请求链路压力、最后处理超大规模存储”的顺序推进。

## 目标

- 重启、更新插件或容器迁移后，统计数据可恢复，且异常退出时丢失窗口可控。
- 看板日常打开、轮询、筛选、导出不把全部明细压到 CPA 管理接口。
- 记录请求时尽量少阻塞主请求链路，持久化失败可观测。
- release 能清楚说明变更、升级影响和配置建议。

## 当前已有能力

- 看板首页使用 `/dashboard-summary`，不返回 `details` 明细，首屏响应不随明细数线性增大。
- 事件明细使用 `/dashboard-events` 分页查询，单次最大 500 条。
- 上游详情使用 `/dashboard-api-detail` 返回聚合、错误统计和最近请求，避免前端自己分页拼全量。
- JSONL 持久化支持 `storage_enabled`、`storage_path`、`storage_flush_interval_seconds`。
- 新写入数据按日期分片，启动时只 replay 保留窗口内分片。
- 正常关闭、日期切换、达到时间间隔或记录间隔会写入 `snapshot.json`，snapshot 成功后会清理 snapshot 日期之前的旧 JSONL 分片，下次启动先加载快照再 replay 当天及之后的增量。
- 可选 `storage_sync_interval_seconds` 和 `storage_sync_record_interval` 可对 JSONL 文件执行周期 fsync。
- JSON marshal、文件写入、flush、fsync 和 snapshot 已由后台有界队列 worker 批量处理，请求记录路径只同步更新内存统计并排队持久化事件。
- 摘要聚合、健康网格、模型/来源/凭证/客户端 API 统计已增量维护。
- 事件查询已有版本化缓存和时间倒序索引，当前分支继续补了模型、来源、凭证筛选的按需二级索引。
- `/health.runtime` 暴露摘要缓存、事件缓存、索引规模和最近查询耗时指标。
- 看板摘要、事件分页、上游详情和事件导出接口支持弱 ETag 与 `If-None-Match` 条件请求，前端轮询会在 304 时复用本地缓存。
- `/health.runtime.conditional_requests` 按端点统计带 `If-None-Match` 请求的 304 命中、未命中和命中率。
- 事件导出接口支持 JSON、CSV、JSONL 和可选 gzip；看板 CSV 导出已改为服务端生成，浏览器不再先下载完整 JSON 数组再转换。导出默认受 `export_max_records` 保护，也可用 `limit` 为单次导出设置更小上限，响应会标记总数、导出数和是否截断。
- 页面会显示持久化状态、后台写入队列积压、最近 writer 批次指标、writer 滑动平均、p95/p99 长尾指标、写入压力状态、待 flush 记录数、最后 flush 时间和最近导入结果。

## P0 建议

### 1. 生产环境默认开启持久化

建议部署模板直接启用持久化，并把数据目录放到宿主机 volume：

```yaml
plugins:
  configs:
    usage-statistics:
      enabled: true
      storage_enabled: true
      storage_path: data/usage-statistics.jsonl
      storage_flush_interval_seconds: 5
      retention_days: 30
      dedup_window_minutes: 1440
```

说明：

- `storage_path` 即使保留 `.jsonl` 后缀，新数据也会写入同名目录下的日期分片，兼容旧单文件读取。
- `storage_flush_interval_seconds: 5` 比默认 30 秒更稳，服务压力仍然可控。
- 高请求量实例可先用 10 到 30 秒，换取更少磁盘 flush。
- 发布、升级、重启前建议保留一次 `/usage/export` 备份。

验收：

- 重启 CPA 后 `/health` 的 `detail_count`、`total_requests` 与重启前一致或只差异常退出窗口内数据。
- 看板底部显示“持久化已同步”或“持久化待同步”，无 `last_error`。

### 2. 避免前端和外部脚本调用旧全量接口

日常页面和脚本应优先使用：

- `/dashboard-summary` 获取摘要。
- `/dashboard-events?limit=...&offset=...` 获取明细页。
- `/dashboard-api-detail?api=...` 获取单个上游详情。
- `/dashboard-events-export` 做按筛选条件导出。

`/dashboard-data` 和 `/usage` 会返回完整 `details`，只建议用于兼容或人工排障。

验收：

- 打开看板时不出现大体积 `/dashboard-data` 请求。
- 10 万条明细下，首屏仍只请求轻量摘要和首批事件。

### 3. 固定保留窗口和明细上限

建议按实际排障需求配置：

- 普通个人/小团队：`retention_days: 30`，`max_details_per_model: 5000` 到 `20000`。
- 高频实例：`retention_days: 7` 到 `14`，并根据模型数量降低 `max_details_per_model`。
- 如果更重视长期趋势，不应无限保留明细，应后续增加按天聚合归档。

原因：

- 当前聚合统计可增量维护，但明细仍需要占用内存和磁盘。
- `max_details_per_model` 是按模型限制，模型数量多时总明细数仍会放大。

验收：

- `/health.detail_count` 在预期范围内稳定。
- `_meta.evicted_total` 会随淘汰增长，但总请求、token、来源、模型等聚合不应出现负数或异常跳变。

### 4. 发布流程必须写 release 说明

当前发布 workflow 会通过 `scripts/extract-release-notes.sh` 从 `CHANGELOG.md` 抽取当前 tag 对应的小节作为 release body；`generate_release_notes: true` 只能作为补充，不能替代人工说明。建议每次发版前维护一段 release notes，至少包含：

- 新增功能。
- 行为变化。
- 配置变更和推荐值。
- 升级注意事项。
- 验证命令和测试环境。

发布前必须把 `CHANGELOG.md` 的 `Unreleased` 内容整理到对应版本小节，例如 `## v1.2.19 - 2026-06-28`。对于持久化、数据格式、管理接口变更，不能只依赖自动生成。

## P1 建议

### 1. 继续完善快照压缩策略

当前已支持按 `storage_snapshot_interval_seconds` 和 `storage_snapshot_record_interval` 周期写入 snapshot，并会在 snapshot 成功后清理 snapshot 日期之前的旧 JSONL 分片。`/health.storage` 会暴露最近和累计清理数量。后续建议继续增加：

- 可选压缩 snapshot 当天之前但需要审计留存的分片，而不是直接删除。
- 清理失败次数和最近清理错误分类。

收益：

- 异常重启后 replay 更快。
- 大量历史分片不会长期拖慢启动。

注意：

- 快照写入必须继续使用临时文件加 rename。
- 写快照时不要长时间阻塞记录请求。

### 2. 减少 fsync 对请求路径的影响

当前已支持可选 fsync 策略：

- `storage_sync_interval_seconds`。
- `storage_sync_record_interval`。
- 状态中展示 `last_sync_at` 和 `pending_unsynced_records`。

推荐默认仍不要每条请求 fsync，避免磁盘 I/O 放大。当前 flush/sync 已在后台 writer 中执行，不再占用请求统计锁。

### 3. 将持久化写入改为后台批量写

当前分支已将记录路径改为后台有界队列：

- 统计更新仍同步完成。
- 持久化事件进入有界队列。
- 单独 writer goroutine 批量负责 JSON marshal、写入、flush、sync 和 snapshot。
- 队列满时阻塞在统计锁外，不静默丢弃。
- `/health.storage` 暴露 `write_queue_length`、`write_queue_capacity`、最近错误、待 flush/sync/snapshot 记录数。
- `/health.storage` 暴露最近 writer 批次的 `last_write_batch_records`、`last_write_batch_duration_ms` 和 `last_write_queue_wait_ms`，看板底部状态 tooltip 也会显示这些指标。
- `/health.storage` 暴露 writer 累计批次/记录、批次耗时 EWMA、最近窗口 p95/p99、队列等待 EWMA、队列等待 p95/p99、最长等待和 `write_pressure`，看板可在无明显积压但持续写入偏慢时提示。

后续可以继续补：

- 将 `write_pressure` 接到外部告警或管理端全局健康提示。

收益：

- 请求记录路径不被磁盘波动放大。
- flush 合并后对服务压力更低。

### 4. 继续扩大 HTTP 条件请求覆盖

当前看板摘要、事件分页、上游详情和事件导出接口已返回弱 ETag，并支持 `If-None-Match` 返回 304；前端轮询已保存 ETag，服务端返回 304 时会直接复用本地数据。`/health.runtime.conditional_requests` 已按端点暴露条件请求数、304 命中数、未命中数和命中率，`CPA_USAGE.md` 已补外部脚本示例。后续可以继续补：

- 按客户端或来源拆分条件请求命中率，便于识别频繁无效轮询来源。
- 如果 CPA 管理层支持标准 HTTP 缓存头透传，可继续补代理缓存示例。

收益：

- 多浏览器或频繁轮询时减少 JSON 编码和传输。
- 对反向代理缓存或浏览器缓存更友好。

### 5. 继续推进真流式或后台导出

当前已支持 `format=csv|jsonl`、`gzip=1`、`export_max_records` 和单次请求 `limit`，并且看板 CSV 由服务端直接生成，减少浏览器侧大数组转换开销。但 CPA 插件管理响应当前仍是单个 `ManagementResponse.Body []byte`，没有真正的 chunked streaming 能力；导出仍会先得到受上限保护的匹配事件集合并编码为一次性响应体。

数据量继续上来后建议：

- 推动管理接口协议支持 chunked streaming，或增加后台导出任务模式。
- 大导出按筛选条件边扫描边写，不构造完整数组。
- 为大导出增加强制时间窗口或异步任务状态查询。
- 为 gzip 导出补充大小/耗时指标。

收益：

- 避免一次导出占用大量内存。
- 管理端下载超大数据时对 CPA 主进程更温和。

## P2 建议

### 1. 百万级明细改用嵌入式索引存储

如果目标是百万级以上明细，JSONL 更适合作为 append log，不适合作为主要查询引擎。建议评估：

- SQLite：按 `timestamp`、`api`、`model`、`source`、`auth_index` 建索引。
- bbolt/Badger：按时间和维度维护 key 前缀。

迁移方式：

- 保留 JSONL 导入能力。
- 新版本启动时检测旧分片并一次性导入嵌入式库。
- 提供导出回 JSON/JSONL 的兜底能力。

### 2. 聚合归档

长期趋势不需要保留每条明细。建议新增 daily aggregate：

- 按天、接口、模型、来源、凭证、客户端 API 聚合请求数、成功失败、token、延迟。
- 明细只保留 7 到 30 天。
- 趋势图优先查归档聚合，排障表格查近期明细。

收益：

- 长期统计能力和服务压力解耦。
- 明细上限更容易控制。

### 3. 增加压测基线

当前 `/health.runtime` 已暴露：

- 摘要版本号。
- 事件缓存命中/未命中次数。
- 最近一次 summary、events、api-detail 查询耗时。

CI 已增加固定 benchmark：

```bash
cd go
go test -run '^$' -bench 'BenchmarkSummaryWithoutDetails|BenchmarkQueryEvents|BenchmarkQueryAPIDetail' -benchmem
```

## 推荐落地顺序

当前分支已完成或基本完成：

1. 保持轻量摘要、事件分页、上游详情接口，避免看板首屏拉取全量 `details`。
2. 补回上游详情里的错误统计和最近请求，保留排障能力。
3. 对模型、来源、凭证筛选增加按需二级索引，降低事件查询扫描成本。
4. 用 `CHANGELOG.md` 驱动 release notes，发布时强制带人工说明。
5. 增加周期性 snapshot、可选 fsync 和运行状态展示。
6. 给摘要、事件分页、上游详情接口补 ETag/304，并在 `/health.runtime` 暴露缓存、索引和查询耗时。
7. 前端轮询接入 ETag 缓存，服务端返回 304 时复用本地数据。
8. 事件导出接口支持 ETag/304，外部脚本重复导出相同筛选条件时可跳过响应体传输。
9. 持久化写入改为后台有界队列，磁盘写入、flush、fsync 和 snapshot 不再占用请求统计锁。
10. 事件导出接口支持 JSON/CSV/JSONL 和可选 gzip，看板 CSV 导出改为服务端生成。
11. 后台持久化 writer 批量处理队列记录，并暴露最近批次条数、写入耗时和最长排队时长。
12. 后台 writer 暴露滑动平均、累计写入量和 `write_pressure` 状态，看板可提示持续写入偏慢。
13. `/health.runtime` 暴露条件请求命中率，外部脚本示例已说明如何复用 ETag。
14. snapshot 成功后清理 snapshot 日期之前的旧 JSONL 分片，减少磁盘占用和下次启动目录扫描范围。
15. 事件导出支持 `export_max_records` 默认上限和单次 `limit`，JSON/响应头标记截断状态，降低超大导出对管理接口的压力。
16. 后台 writer 暴露最近窗口 p95/p99 批次耗时和排队等待，看板 tooltip 可量化长尾磁盘抖动。

下一步建议：

1. 生产配置默认开启持久化，文档强调 volume、flush、retention 和升级前导出。
2. 推动管理接口支持真流式导出，或增加后台导出任务模式，进一步降低超大导出的内存峰值。
3. 将 `write_pressure`、writer p95/p99 和导出截断指标接到外部告警，便于识别长尾磁盘压力。
4. 大数据量场景再评估 SQLite、bbolt 或 daily aggregate 归档。

## 发布前检查清单

- `go test ./...`
- `go test -race ./...`
- `node --check go/dashboard/helpers.js go/dashboard/script.js`
- `node --test go/dashboard/*.test.js`
- 持久化开启后重启验证：数据恢复、`last_error` 为空。
- 看板验证：首屏不调用 `/dashboard-data`，筛选/分页/导出正常。
- Release notes 已写明功能、配置、兼容性和升级建议。
