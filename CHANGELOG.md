# Changelog

本文件用于维护每个 GitHub Release 的人工说明。发布 tag 前必须把 `Unreleased` 中本次要发布的内容整理到对应版本小节，例如 `## v2.0.0 - 2026-06-28`。

## Unreleased

暂无。

## v2.0.0 - 2026-06-28

### 新增

- 新增优化建议文档，覆盖持久化、服务压力、查询性能和发布说明流程。
- 持久化新增周期 snapshot 配置，支持按时间或新增记录数写入 `snapshot.json`，降低异常重启后的 replay 成本。
- 持久化新增可选 fsync 配置和状态展示，便于在可靠性和磁盘 I/O 之间按部署环境取舍。
- 看板摘要、事件分页、上游详情和事件导出接口支持弱 ETag 与 `If-None-Match`，未变化时可返回 304。
- 事件导出接口新增 `format=csv|jsonl`、`gzip=1`、`export_max_records` 和单次 `limit`，响应会标记总数、导出数和是否截断。
- 看板导出按钮改用后台导出任务，新增创建、查询、下载、删除任务接口。
- `/health.runtime` 新增摘要缓存、事件缓存、索引规模、最近查询耗时、条件请求命中率和事件导出压力指标。
- `/health.storage` 新增后台 writer 队列、批次耗时、排队等待、滑动平均、p95/p99 和 `write_pressure` 指标。
- `/health` 顶层新增 `status` 和结构化 `alerts`，覆盖持久化错误、写入压力、慢导出、导出截断、writer 长尾抖动和条件请求命中率过低。

### 优化

- 持久化写入改为后台有界队列，JSON marshal、文件写入、flush、fsync 和 snapshot 不再占用请求统计锁。
- 后台持久化 writer 批量处理队列记录，降低高频请求下的磁盘写入压力。
- 持久化 snapshot 成功后会清理 snapshot 日期之前的旧 JSONL 分片，减少磁盘占用和下次启动扫描范围。
- 优化看板事件查询：模型、来源、凭证筛选按需构建二级索引，减少大数据量下的扫描开销。
- 看板前端轮询会缓存摘要、事件分页和上游详情的 ETag，服务端返回 304 时复用本地数据，减少重复传输和解析。
- 看板 CSV 导出改为服务端生成，浏览器不再先下载完整 JSON 数组再转换。
- 后台导出任务生成阶段按页扫描并写入临时文件，不再构造完整事件数组，并用任务并发和保留上限保护管理接口。
- `gzip=1` 事件导出改为返回 gzip 文件内容，使用 `application/gzip` 和 `X-Export-Content-Type` 标记原始格式，不再依赖 `Content-Encoding`，避免代理或浏览器透明解压导致 `.gz` 下载文件内容不匹配。
- CI benchmark 覆盖摘要、事件查询和上游详情查询关键路径，降低后续性能回退风险。
- 发布 workflow 强制读取对应版本 changelog 作为 release body，避免 release 缺少人工说明。

### 修复

- 修复看板摘要接口偶发失败进入兼容模式时服务健康网格被清空的问题；兼容模式会从完整明细重建 7 天健康网格，空数据也保持 672 个格子。
- 修复同一秒内新增多条请求时，看板摘要已更新但请求事件明细和上游接口详情可能继续复用旧数据的问题。
- 修复上游接口详情刷新时先显示加载态导致错误统计和最近请求短暂空白的问题。
- 修复实时 usage 记录被 24 小时去重窗口误合并，导致上游接口统计、上游接口详情和请求事件明细少记请求的问题。
- 修复 `max_details_per_model` 裁剪请求明细时误扣累计统计的问题，上游接口统计和上游接口详情不再在累计数与明细保留数之间跳变。
- 实时请求和持久化重放不会按去重窗口合并；导入数据会跳过精确重复记录。
- 看板持久化徽标只显示是否开启持久化；flush、fsync、snapshot 和 writer 队列指标保留在 tooltip 和健康接口中，避免页面长期显示“待落盘”。

### 升级建议

- 本版本作为正式稳定版发布；如果从早期版本升级并担心历史 JSONL 格式兼容问题，建议先备份再清理旧统计持久化数据。
- 生产环境建议启用 `storage_enabled`，并将 `storage_path` 放到宿主机持久化 volume。
- 建议设置 `storage_flush_interval_seconds: 5`、`storage_snapshot_interval_seconds: 300`、`storage_snapshot_record_interval: 1000`，fsync 默认保持关闭。
- 高频实例建议根据实际流量降低 `retention_days` 和 `max_details_per_model`，并保留 `export_max_records` 默认上限。
- 发布后建议重点观察 `/health.status`、`/health.alerts`、`storage.write_pressure`、健康网格和后台导出任务。

## v1.2.18 - 2026-06-28

### 新增

- 看板摘要、事件分页、上游接口详情等管理端接口已支持轻量查询路径。
- 上游接口详情包含错误统计和最近请求，便于排查接口失败和异常延迟。
- JSONL 持久化支持日期分片和启动快照 replay，降低重启后恢复成本。

### 优化

- 摘要聚合、健康网格、模型/来源/凭证/客户端 API 统计改为增量维护。
- 看板事件分页支持缓存和时间倒序索引，减少重复筛选和排序。
- 页面会展示持久化状态、待 flush 记录数、最近导入结果等运行元数据。

### 升级建议

- 生产环境建议启用 `storage_enabled`，并将 `storage_path` 放到宿主机持久化 volume。
- 如果需要更稳的数据落盘窗口，可把 `storage_flush_interval_seconds` 调整为 5 秒。
- 发布或更新插件前建议通过 `/usage/export` 导出一次备份。
