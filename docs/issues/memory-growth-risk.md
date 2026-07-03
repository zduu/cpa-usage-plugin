# Dashboard Range Summary Cache 内存增长风险

## 背景

排查插件装载到 CPA 后是否会导致 VPS 内存持续升高时，未发现典型的 goroutine、文件句柄或 C buffer 泄漏。主要统计数据也有保留策略控制：

- 请求明细默认 `max_details_per_model = 5000`，按“上游接口/模型”保留最近明细。
- 默认 `retention_days = 30`，超过保留窗口的记录会被淘汰。
- 存储写入队列固定容量为 `4096`。
- 后台导出任务最多 `2` 个 active、`16` 个 retained，完成后默认 `15` 分钟过期。
- `storage` worker 和 `models.dev` 价格刷新 worker 都有 shutdown/stop 逻辑。

dashboard 的 range summary 缓存曾存在一个可导致长期缓慢增长的风险点，现已加上时间分桶和容量上限。

## 风险点

旧实现中，`SummaryWithoutDetailsForRange` 会按当前时间秒生成缓存 key：

- `go/stats.go`: `summaryRangeCacheKey(rangeKey, now)`
- 旧 key 格式：`rangeKey + "|" + now.UTC().Unix()`

对应的 dashboard summary ETag 也包含当前 Unix 秒：

- `go/dashboard.go`: `dashboardSummaryETag(now, rangeKey)`

打开 dashboard 后，前端会定期轮询：

- 可见页面默认每 `30s` 请求一次 summary。
- 隐藏页面默认每 `300s` 请求一次 summary。

当用户选择 `7h`、`24h`、`7d` 这类 range 时，如果期间没有新的 usage 事件触发 `invalidateSummaryLocked()`，旧实现的 `summaryRangeCache` 会不断新增新的秒级 key。每个 key 会保存一份 `DashboardSummary`，可能导致插件内存缓慢上升。

当前实现已调整为：

- range summary cache key 按分钟分桶，而不是按秒。
- `summaryRangeCache` 最多保留 `16` 个 key，超过后清理旧缓存。
- `dashboardSummaryETag` 与同一分钟桶对齐，避免同一分钟内生成无意义的新 ETag。

## 触发条件

满足以下条件时风险更明显：

- dashboard 页面长期打开。
- 当前 range 不是 `all`。
- 期间请求量很低，甚至没有新的 usage 事件。
- 已积累的数据量较大，单份 range summary 本身较大。

高流量场景下，新 usage 记录会频繁触发 `invalidateSummaryLocked()`，清空 `summaryRangeCache`，因此该问题反而不一定明显。

## 影响范围

这是曾存在的“缓慢内存增长风险”，不是立即 OOM 的确定性问题。

旧版本预期表现：

- CPA 进程内存随 dashboard 轮询缓慢上涨。
- 有新 usage 写入时，相关缓存会被清空，内存压力可能缓解。
- 长时间低流量 + 长期开 dashboard 的 VPS 更容易观察到。

## token 记录增长的启动和内存影响

插件不会逐 token 保存原文 token，而是每次 usage 请求保存一条 `RequestDetail`，里面记录 input/output/cache/reasoning/total token 等汇总值。因此内存压力主要由“请求明细条数”和“唯一维度数量”决定，而不是 token 数值本身决定。

默认配置下，主要边界是：

- `retention_days = 30`：只保留 30 天窗口内的统计。
- `max_details_per_model = 5000`：每个“上游接口/模型”最多保留 5000 条请求明细。

运行时内存压力会随以下因素增加：

- 上游接口/模型组合数量增加。理论明细上限约为 `组合数 * max_details_per_model`。
- `retention_days = 0` 时不按时间淘汰，历史聚合数据和唯一维度更容易长期保留。
- 大量唯一 `model`、`source`、`auth_index`、客户端 API key 或 provider/base URL 组合会扩大聚合 map。
- 记录响应头时，如果 `log_response_headers` 放开过多头部，单条明细会变大。
- dashboard 事件索引、筛选缓存和 range summary 缓存会基于当前保留的明细再产生额外内存占用。

开启 `storage_enabled` 后还会有启动影响：

- 启动或重新配置时会读取 snapshot，并 replay 保留窗口内的 JSONL 分片。
- 数据分片越大，启动恢复耗时越长。
- replay 阶段会有临时解析和去重 map，占用会高于稳定运行后的常驻占用。
- 如果当天分片很大且还没被 snapshot/compact，启动恢复压力会更明显。

小内存 VPS 建议：

- 将 `max_details_per_model` 调低到 `1000` 或 `2000`。
- 将 `retention_days` 调低到 `7` 或 `14`。
- 保持 `storage_snapshot_interval_seconds` 和 `storage_snapshot_record_interval` 有合理值，减少重启 replay 压力。
- 避免记录过多响应头，`log_response_headers` 只开放必要字段。
- 避免把高基数字段写入 `source` 或 `auth_index`。

## 非风险点

本次排查中以下位置有明确上限或释放逻辑，暂未作为泄漏处理：

- `modelSt.Details`：受 `max_details_per_model` 和 `retention_days` 控制。
- `eventQueryCache`：最多 `16` 项。
- `storageQueue`：固定容量 `4096`。
- `storageWriteBatchDurations` / `storageWriteQueueWaits`：最多 `256` 个样本。
- `dashboardExportJobs`：最多 `16` 个保留任务，完成任务过期清理。
- 插件 shutdown：会关闭导出任务、storage worker、models.dev worker。

## 已采用修复

已采用低风险组合：

1. `summaryRangeCacheKey` 使用分钟级窗口分桶。
2. `summaryRangeCache` 增加容量上限，最多保留 `16` 个 key。
3. `dashboardSummaryETag` 同步使用分钟级窗口，降低无效响应和缓存膨胀。

## 验证建议

已增加测试：

- `TestSummaryRangeETagUsesMinuteBucket`: 同一分钟内 ETag 稳定，跨分钟变化。
- `TestSummaryRangeCacheIsBounded`: range summary cache 不超过上限。
- 既有缓存测试继续覆盖重复调用返回一致结果。
- `go test ./...` 通过。

## 当前状态

状态：已修复。

排查命令：

```sh
go test ./...
go test -race ./...
```

结果：

```text
ok  	github.com/zduu/cpa-usage-plugin
```
