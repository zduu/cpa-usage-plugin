# CPA Usage Statistics Plugin 优化建议

本文基于当前代码审查结果整理，重点关注两类目标：

- 持久性：重启、更新插件、容器迁移或异常退出后，统计数据尽量可恢复，且丢失窗口可解释、可配置、可监控。
- 减轻服务压力：看板、筛选、导出、持久化写入不应把 CPA 管理接口或请求记录路径拖慢。

建议按“生产配置先收敛、运行指标可观测、超大数据再换存储形态”的顺序推进。

## 当前状态

当前版本已经具备以下基础能力：

- 看板首屏使用 `/dashboard-summary`，不返回完整 `details` 明细。
- 事件明细使用 `/dashboard-events` 分页查询，单次请求有上限。
- 上游接口详情使用 `/dashboard-api-detail`，返回模型分布、错误统计和最近请求。
- JSONL 持久化支持日期分片、周期 snapshot、旧分片清理、可选 fsync。
- 持久化写入已改为后台有界队列和批量 writer，请求记录路径不直接执行 JSON marshal、文件写入、flush、fsync 和 snapshot。
- 看板摘要、事件分页、上游详情和事件导出支持弱 ETag 与 `If-None-Match`。
- 事件导出支持 JSON、CSV、JSONL、gzip、默认最大导出条数和单次 `limit`。
- 看板导出使用后台导出任务，生成阶段按页扫描并写入临时文件，避免构造完整事件数组。
- `/health` 已暴露持久化状态、writer 队列、批次耗时、p95/p99、导出压力、条件请求命中率和结构化 `alerts`。
- release workflow 已从 `CHANGELOG.md` 抽取版本说明，避免新 release 没有人工说明。

主要剩余风险：

- 默认配置仍是内存模式，生产环境如果不显式开启 `storage_enabled`，重启后统计会丢失。
- JSONL 适合作为 append log 和中等规模恢复机制，不适合作为百万级明细的主要查询引擎。
- CPA 插件管理响应当前仍是一次性 `Body []byte`，后台导出可以降低生成阶段内存峰值，但下载阶段还不是真流式响应。
- 告警阈值需要用真实流量校准，否则可能过早或过晚提示压力。

## P0：生产配置建议

### 1. 默认开启持久化

生产环境建议显式开启持久化，并把数据目录放到宿主机 volume：

```yaml
plugins:
  configs:
    usage-statistics:
      enabled: true
      storage_enabled: true
      storage_path: data/usage-statistics.jsonl
      storage_flush_interval_seconds: 5
      storage_snapshot_interval_seconds: 300
      storage_snapshot_record_interval: 1000
      storage_sync_interval_seconds: 0
      storage_sync_record_interval: 0
      retention_days: 30
      max_details_per_model: 5000
      export_max_records: 100000
```

说明：

- `storage_path` 可继续使用 `.jsonl` 后缀；新数据会写入同名目录下的日期分片，并兼容读取历史单文件。
- `storage_flush_interval_seconds: 5` 能把异常退出丢失窗口控制在较小范围，通常比默认 30 秒更适合生产。
- `storage_sync_interval_seconds` 和 `storage_sync_record_interval` 默认建议保持 `0`，避免 fsync 放大磁盘 I/O；只有需要更强断电保护时再启用。
- 发布、升级或迁移前建议先调用 `/usage/export` 导出一次备份。

验收方式：

- 重启 CPA 后，`/health.detail_count` 和 `/health.total_requests` 与重启前基本一致。
- `/health.storage.enabled` 为 `true`。
- `/health.storage.last_error` 为空。
- 看板底部显示“持久化已同步”“持久化待同步”或“持久化待落盘”，不应长期停留在“持久化排队中”。

### 2. 控制保留窗口和明细上限

建议按使用规模设置：

| 场景 | retention_days | max_details_per_model | export_max_records |
| --- | ---: | ---: | ---: |
| 个人或低频实例 | 30 | 5000 到 20000 | 100000 |
| 中等团队 | 14 到 30 | 5000 到 10000 | 50000 到 100000 |
| 高频实例 | 7 到 14 | 1000 到 5000 | 20000 到 50000 |

原因：

- `max_details_per_model` 是按上游接口和模型保留，模型数量多时总明细仍会放大。
- 长期趋势应依赖聚合数据，排障明细只保留近期窗口。
- 超大导出会消耗 CPA 管理接口内存和网络带宽，应默认限制。

验收方式：

- `/health.detail_count` 在预期范围内稳定。
- `/health.evicted_total` 随淘汰增长，但总请求、token、成功率等聚合不出现异常跳变。
- 导出超出上限时，响应中的 `truncated` 或 `X-Export-Truncated` 能明确提示截断。

### 3. 避免日常使用旧全量接口

日常页面和脚本应优先使用：

- `/dashboard-summary`：看板摘要。
- `/dashboard-events?limit=...&offset=...`：事件明细分页。
- `/dashboard-api-detail?api=...`：单个上游接口详情。
- `/dashboard-events-export` 或后台导出任务：按筛选条件导出。

`/dashboard-data` 和 `/usage` 会返回完整数据，只建议用于兼容、人工排障或一次性备份。

验收方式：

- 打开看板时不应出现大体积 `/dashboard-data` 请求。
- 10 万条明细量级下，首屏仍只加载摘要和第一页事件。
- 外部脚本重复拉取看板数据时，应复用 ETag，服务端 304 时不重复解析大 JSON。

### 4. Release 必须带人工说明

发布 tag 前，需要把 `CHANGELOG.md` 的 `Unreleased` 内容整理到目标版本小节，例如：

```markdown
## v1.2.19 - 2026-06-28

### 新增

- ...

### 优化

- ...

### 升级建议

- ...
```

release notes 至少应包含：

- 新增功能。
- 行为变化。
- 配置项变化和推荐值。
- 升级注意事项。
- 验证命令和测试环境。

验收方式：

- tag 名和 `go/register.go` 中的 `pluginVersion` 一致。
- GitHub Release body 来自对应版本 changelog，不是空说明。
- 对持久化、导出、管理接口和兼容性变化有明确升级说明。

## P1：运行观测和压力控制

### 1. 接入 `/health.alerts` 到外部监控

建议外部监控直接检查：

- `status != ok`
- `alerts[].severity == error`
- `alerts[].code`
- `storage.last_error`
- `storage.write_pressure`
- `storage.write_queue_length`
- `storage.write_batch_p99_duration_ms`
- `storage.write_queue_wait_p99_ms`
- `runtime.last_events_export_truncated`
- `runtime.last_events_export_duration_ms`
- `runtime.conditional_requests.*.hit_rate`

建议初始处理规则：

| 指标 | 初始判断 |
| --- | --- |
| `storage.last_error` 非空 | 立即排查目录权限、磁盘空间、文件系统状态 |
| `storage.write_pressure == slow` 持续 5 分钟 | 降低 flush/fsync 频率或检查磁盘 I/O |
| `write_queue_length` 持续增长 | 请求写入速度超过后台 writer 能力 |
| `write_batch_p99_duration_ms > 1000` | 存在明显磁盘长尾抖动 |
| `last_events_export_truncated == true` | 用户导出范围过大，需要缩小时间窗口或提高上限 |
| 条件请求命中率长期过低 | 检查前端轮询、反向代理或外部脚本是否传递 ETag |

### 2. 校准持久化参数

如果目标是更强可靠性：

- 保持 `storage_flush_interval_seconds: 1` 到 `5`。
- 可设置 `storage_sync_interval_seconds: 30` 或 `storage_sync_record_interval: 1000`。
- 观察 `write_batch_p99_duration_ms` 和 `write_queue_wait_p99_ms`，如果明显升高，需要放宽同步策略。

如果目标是减轻磁盘压力：

- 使用 `storage_flush_interval_seconds: 10` 到 `30`。
- 保持 fsync 关闭。
- 降低 `max_details_per_model` 和 `retention_days`。

不要为每条请求执行 fsync。当前后台 writer 已经把磁盘操作移出请求锁，下一步调优应优先使用批量和周期同步。

### 3. 继续压缩导出压力

当前后台导出已经降低生成阶段压力，但下载阶段仍受 CPA 管理响应协议限制。建议后续推进：

- 管理接口支持 chunked streaming 或文件句柄式下载。
- 大导出强制要求时间范围，例如超过 7 天必须显式确认或使用更小 `limit`。
- 对导出任务增加更细的统计：队列等待、生成耗时分布、文件大小分布、失败原因分类。
- 对同一筛选条件的重复导出增加短 TTL 结果复用，避免多人重复生成同一份大文件。

### 4. 给压测建立固定基线

当前 CI 已运行 Go 测试、race 测试、JS 测试和关键 benchmark。建议保留并定期关注这些路径：

```bash
cd go
go test ./...
go test -race -count=1 ./...
go test -run '^$' -bench='Benchmark(RecordIncremental|Snapshot|SummaryWithoutDetails|QueryEvents|QueryAPIDetail)' -benchtime=100ms -count=1 ./...
```

```bash
node --check go/dashboard/helpers.js go/dashboard/script.js
node --test go/dashboard/*.test.js
```

建议把基线结果记录到发布说明或 PR 描述中，至少覆盖：

- 1 万、10 万、50 万明细下的摘要查询耗时。
- 事件分页查询耗时。
- 上游详情查询耗时。
- 后台导出任务生成耗时和响应大小。
- 持久化 replay 启动耗时。

## P2：大规模数据方案

当明细数据达到百万级以上，建议不要继续把 JSONL 作为主查询来源。更合理的方向是“近期明细 + 长期聚合”。

### 1. 引入嵌入式索引存储

可评估：

- SQLite：按 `timestamp`、`api`、`model`、`source`、`auth_index` 建索引，适合筛选、分页和聚合查询。
- bbolt/Badger：按时间和维度维护 key 前缀，适合 append 和范围扫描。

迁移要求：

- 保留 JSONL 导入能力。
- 启动时检测旧分片并一次性导入新存储。
- 保留导出 JSON/JSONL 的兜底能力。
- 迁移过程需要有进度、失败回滚和重复执行保护。

### 2. 增加 daily aggregate 归档

建议长期趋势使用按天聚合表：

- 按天、上游接口、模型、来源、凭证、客户端 API key 聚合请求数、成功失败、token、延迟。
- 明细只保留 7 到 30 天。
- 趋势图优先读取 daily aggregate，排障表格读取近期明细。

收益：

- 长期统计和近期排障解耦。
- 明细总量可控，启动 replay 和看板查询压力稳定。
- 后续即使换 SQLite，也能保持查询模型清晰。

### 3. 多实例或迁移场景

如果未来存在多个 CPA 实例或频繁迁移：

- 使用固定 `api_key_hash_salt`，避免不同实例同一客户端 API key 分组不一致。
- 持久化目录必须放在宿主机 volume 或网络盘，不能放在容器临时层。
- 升级前导出 `/usage/export`，升级后验证 `/health` 和看板聚合。
- 如需跨实例统一统计，应考虑集中式存储或离线合并，不建议多个进程同时写同一 JSONL 目录。

## 推荐落地顺序

1. 生产配置默认开启 `storage_enabled`，并把 `storage_path` 放到宿主机 volume。
2. 固定 `retention_days`、`max_details_per_model`、`export_max_records`，避免明细和导出无限增长。
3. 外部脚本和日常看板只使用轻量接口，避免 `/dashboard-data` 成为常规路径。
4. 把 `/health.status`、`/health.alerts`、`storage.write_pressure`、导出压力指标接入监控。
5. 每次发版前整理 `CHANGELOG.md`，确保 release body 有人工说明和升级建议。
6. 根据真实流量校准 flush、snapshot、fsync、导出上限和告警阈值。
7. 当明细达到百万级或启动 replay 明显变慢时，再推进 SQLite/bbolt 和 daily aggregate。

## 发布前检查清单

- `go test ./...`
- `go test -race -count=1 ./...`
- `node --check go/dashboard/helpers.js go/dashboard/script.js`
- `node --test go/dashboard/*.test.js`
- benchmark 覆盖记录、snapshot、摘要、事件查询和上游详情查询。
- 持久化开启后重启验证：数据恢复、`storage.last_error` 为空。
- 看板验证：首屏不调用 `/dashboard-data`，筛选、分页、上游详情、导出正常。
- 导出验证：JSON、CSV、JSONL、gzip、截断提示、后台任务创建/查询/下载/删除正常。
- `/health.status` 为 `ok`，无异常 `alerts`。
- Release notes 已写明功能、配置、兼容性和升级建议。
