# Changelog

本文件用于维护每个 GitHub Release 的人工说明。发布 tag 前必须把 `Unreleased` 中本次要发布的内容整理到对应版本小节，例如 `## v1.2.19 - 2026-06-28`。

## Unreleased

- 优化看板事件查询：模型、来源、凭证筛选按需构建二级索引，减少大数据量下的扫描开销。
- 新增优化建议文档，覆盖持久化、服务压力、查询性能和发布说明流程。
- 发布 workflow 将强制读取对应版本 changelog 作为 release body，避免 release 缺少人工说明。
- 持久化新增周期 snapshot 配置，支持按时间或新增记录数写入 `snapshot.json`，降低异常重启后的 replay 成本。
- 持久化新增可选 fsync 配置和状态展示，便于在可靠性和磁盘 I/O 之间按部署环境取舍。
- 持久化写入改为后台有界队列，JSON marshal、文件写入、flush、fsync 和 snapshot 不再占用请求统计锁。
- `/health.runtime` 新增摘要缓存、事件缓存、索引规模和最近查询耗时指标，用于观察看板压力和调优效果。
- CI benchmark 覆盖摘要、事件查询和上游详情查询关键路径，降低后续性能回退风险。
- 看板摘要、事件分页和上游详情接口支持弱 ETag 与 `If-None-Match`，未变化时可返回 304。
- 看板前端轮询会缓存摘要、事件分页和上游详情的 ETag，服务端返回 304 时复用本地数据，减少重复传输和解析。
- 事件导出接口支持弱 ETag 与 `If-None-Match`，外部脚本重复导出相同筛选条件时可跳过响应体传输。
- 事件导出接口新增 `format=csv|jsonl` 和 `gzip=1`，看板 CSV 导出改为服务端生成，减少浏览器侧大数组转换开销。
- 事件导出新增 `export_max_records` 默认上限和 `limit` 查询参数，响应会标记总数、导出数和是否截断，避免超大导出一次性压垮管理接口。
- 后台持久化 writer 改为批量处理队列记录，并在 `/health.storage` 暴露最近批次条数、写入耗时和最长排队时长。
- `/health.storage` 新增 writer 累计批次、累计记录、耗时/排队滑动平均和 `write_pressure` 状态，看板可提示持续写入偏慢。
- `/health.runtime` 新增 `conditional_requests`，按端点统计带 `If-None-Match` 请求的 304 命中、未命中和命中率。
- 持久化 snapshot 成功后会清理 snapshot 日期之前的旧 JSONL 分片，并在 `/health.storage` 暴露最近和累计清理数量。

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
