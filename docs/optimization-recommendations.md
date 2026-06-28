# CPA Usage Statistics Plugin 优化建议

本文基于当前项目代码审查，重点覆盖数据持久性、减轻 CPA 服务压力、看板查询性能和发布说明流程。

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
- 正常关闭、日期切换、达到时间间隔或记录间隔会写入 `snapshot.json`，下次启动先加载快照再 replay 增量。
- 可选 `storage_sync_interval_seconds` 和 `storage_sync_record_interval` 可对 JSONL 文件执行周期 fsync。
- 摘要聚合、健康网格、模型/来源/凭证/客户端 API 统计已增量维护。
- 事件查询已有版本化缓存和时间倒序索引，当前分支继续补了模型、来源、凭证筛选的按需二级索引。
- `/health.runtime` 暴露摘要缓存、事件缓存、索引规模和最近查询耗时指标。
- 页面会显示持久化状态、待 flush 记录数、最后 flush 时间和最近导入结果。

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

当前 GitHub Actions 使用 `generate_release_notes: true`，但自动说明不一定覆盖用户关心的配置和兼容性。建议每次发版前维护一段人工 release notes，至少包含：

- 新增功能。
- 行为变化。
- 配置变更和推荐值。
- 升级注意事项。
- 验证命令和测试环境。

建议新增 `CHANGELOG.md` 或 `.github/release-template.md`，发布时把本次条目复制到 GitHub Release body。对于持久化、数据格式、管理接口变更，不能只依赖自动生成。

## P1 建议

### 1. 增加快照压缩策略

当前已支持按 `storage_snapshot_interval_seconds` 和 `storage_snapshot_record_interval` 周期写入 snapshot。后续建议继续增加：

- 快照成功后，可选择压缩或标记老分片，减少下次启动 replay 范围。

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

推荐默认仍不要每条请求 fsync，避免磁盘 I/O 放大。后续应结合后台 writer，把 flush/sync 从统计锁内迁出。

### 3. 将持久化写入改为后台批量写

当前记录路径会在统计锁内完成 JSON marshal、文件打开、写入和可能 flush。高并发下建议：

- 统计更新仍同步完成。
- 持久化事件进入有界队列。
- 单独 writer goroutine 批量 marshal、写入、flush、sync。
- 队列满时采用明确策略：阻塞、降级为同步写、或拒绝并记录错误，不建议静默丢弃。
- `/health` 暴露队列长度、最近错误、最后成功写入时间。

收益：

- 请求记录路径不被磁盘波动放大。
- flush 合并后对服务压力更低。

### 4. 管理接口增加 HTTP 条件请求

前端已经可在数据未变化时跳过部分详情请求。后端可以继续补：

- `/dashboard-summary` 返回 `ETag` 或版本号。
- 支持 `If-None-Match`，未变化时返回 304。
- `/dashboard-events` 可在相同筛选、相同版本下返回 304 或轻量空响应。

收益：

- 多浏览器或频繁轮询时减少 JSON 编码和传输。
- 对反向代理缓存或浏览器缓存更友好。

### 5. 导出改为流式输出

当前后端导出会先得到匹配事件集合再编码响应。数据量上来后建议：

- 支持 JSONL/CSV 流式导出。
- 大导出按筛选条件边扫描边写，不构造完整数组。
- 可选 gzip。
- 给导出增加最大行数或后台任务模式。

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

后续建议 CI 增加固定 benchmark：

```bash
cd go
go test -run '^$' -bench 'BenchmarkSummaryWithoutDetails|BenchmarkQueryEvents|BenchmarkQueryAPIDetail' -benchmem
```

## 推荐落地顺序

1. 保持当前轻量摘要、事件分页、事件索引优化，并补齐对应测试。
2. 新增 release notes 模板，下一次发版必须手写说明。
3. 生产配置默认开启持久化，文档强调 volume、flush、retention。
4. 增加周期性 snapshot 和可选 sync。
5. 把持久化写入从统计锁中移到后台队列。
6. 给管理接口补 ETag/304。
7. 大数据量场景再评估 SQLite 或聚合归档。

## 发布前检查清单

- `go test ./...`
- `go test -race ./...`
- `node --check go/dashboard/helpers.js go/dashboard/script.js`
- `node --test go/dashboard/*.test.js`
- 持久化开启后重启验证：数据恢复、`last_error` 为空。
- 看板验证：首屏不调用 `/dashboard-data`，筛选/分页/导出正常。
- Release notes 已写明功能、配置、兼容性和升级建议。
