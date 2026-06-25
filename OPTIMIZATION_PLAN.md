# 优化计划

本文档记录当前代码审查后的优化项。范围限定在内存统计插件本身；按用户要求，不加入持久化能力。

## P0：性能与内存风险

### 1. 优化写入热路径

- 现状：`Record()` 每次记录请求后都会执行 `pruneLocked()`、`rebuildAggregatesLocked()` 和 `rebuildSeenLocked()`。
- 影响：在保留明细较多时，每次请求都会扫描并重建全部保留数据；高并发或长期运行下会放大锁持有时间。
- 参考位置：`go/main.go:1342`、`go/main.go:1372`、`go/main.go:1417`、`go/main.go:1638`
- 建议：
  - `Record()` 只做增量计数。
  - 只有发生淘汰时才对被淘汰记录做反向扣减。
  - 去重表只增量维护，定期或按阈值清理过期 key。
  - 将全量 `rebuildAggregatesLocked()` 保留为导入、重配置或修复工具路径。
- 验收：
  - 增加 benchmark，覆盖 1k/10k/100k 明细写入。
  - `go test -race ./...` 通过。
  - 写入耗时不随全量保留明细线性增长。

### 2. 避免每次写入排序

- 现状：`pruneLocked()` 每次都会对每个模型的明细按时间排序。
- 影响：请求通常按时间进入，反复排序会产生不必要的 O(n log n) 成本。
- 参考位置：`go/main.go:1400`
- 建议：
  - 正常写入使用追加顺序。
  - 导入数据后单次排序。
  - 每个模型明细用 ring buffer 或按时间有序切片，只在乱序导入时归并。
- 验收：
  - 普通 `Record()` 不调用排序。
  - 导入乱序快照后展示顺序仍正确。

### 3. 降低 dashboard 数据传输量

- 现状：`Snapshot()` 会复制所有 retained details；`dashboard-data` 每 30 秒返回完整快照，前端再做时间范围过滤和聚合。
- 影响：模型或接口数量变多后，管理页加载慢、浏览器计算重、网络响应大。
- 参考位置：`go/main.go:347`、`go/main.go:1470`、`go/main.go:1493`、`go/main.go:801`
- 建议：
  - 新增轻量接口：总览、健康网格、接口/模型聚合由服务端直接返回。
  - 请求明细单独分页：支持 `limit`、`offset`、`range`、`model`、`source`、`auth`。
  - 导出接口继续返回全量或按筛选导出。
- 验收：
  - 默认 dashboard 首包不携带全部请求明细。
  - 最近请求表仍可分页查看和导出。
  - 10 万条 retained details 下页面仍能快速打开。

### 4. 不再保留完整响应头

- 现状：`RequestDetail.Headers` 保存 `record.ResponseHeaders`，但当前 UI 和导出并未使用该字段。
- 影响：响应头可能包含敏感信息或较大字段，也会增加内存和导出体积。
- 参考位置：`go/main.go:1186`、`go/main.go:1320`
- 建议：
  - 默认不保存 headers。
  - 如确实需要排障，仅保留白名单 header，并设置长度上限。
- 验收：
  - 导出的请求明细不包含未脱敏响应头。
  - 测试覆盖 header 丢弃或白名单行为。

## P1：统计准确性与安全边界

### 5. API key 聚合避免 masked key 碰撞

- 现状：请求明细中只保存 `maskAPIKey(record.APIKey)`，API 详细统计用该显示值聚合。
- 影响：不同 CPA client API key 如果前后缀相同，会被聚合到同一项。
- 参考位置：`go/main.go:1302`、`go/main.go:624`、`go/main.go:728`
- 建议：
  - 内部保存不可逆稳定分组 ID，例如带插件运行期 salt 的 hash。
  - UI 显示仍使用 masked key。
  - 导出中可以包含 `api_key_label` 和 `api_key_group`，不保存原始 key。
- 验收：
  - 两个前后缀相同的 API key 统计不会合并。
  - UI 和导出不泄露完整 API key。

### 6. 导入统计的 added/skipped 语义修正

- 现状：`MergeSnapshot()` 先计入 `Added`，最后再执行保留策略淘汰。
- 影响：导入过期数据时，响应可能显示 added 增加，但快照中不会保留这些数据。
- 参考位置：`go/main.go:1589`、`go/main.go:1595`
- 建议：
  - 导入前先按当前保留策略过滤。
  - `MergeResult` 增加 `pruned` 或 `ignored_by_retention`。
  - 导入响应区分“新增保留”和“因策略忽略”。
- 验收：
  - 导入全是过期记录时 `added` 为 0 或明确返回 ignored 数量。

### 7. 错误体脱敏

- 现状：失败响应 body 截断到 500 字符后保存和导出。
- 影响：上游错误可能回显 key、authorization header、请求片段或用户内容。
- 参考位置：`go/main.go:1319`
- 建议：
  - 在 `trimLong()` 前增加 `redactSensitiveText()`。
  - 覆盖常见 key 前缀、Authorization、Bearer、长 token。
  - UI 展示截断后的脱敏文本。
- 验收：
  - 测试覆盖 `sk-`、`Bearer`、长 token、URL query key。

### 8. 导入接口加限制和校验

- 现状：`handleImportUsage()` 直接反序列化整个 body，没有大小、条数或时间范围校验。
- 影响：误导入超大 JSON 会造成内存峰值；恶意输入也可能拖慢管理端。
- 参考位置：`go/main.go:850`
- 建议：
  - 设置导入 body 最大大小配置。
  - 统计导入记录数，超过阈值拒绝或分批。
  - 对 timestamp、token、latency 做边界校验。
- 验收：
  - 超限导入返回明确错误。
  - 畸形记录不会污染统计。

## P2：可维护性

### 9. 拆分单文件

- 现状：`go/main.go` 同时包含 CGO 入口、插件注册、管理接口、HTML/CSS/JS、数据结构、统计引擎和工具函数。
- 影响：单文件超过 1800 行，后续改 UI 或统计逻辑容易互相干扰。
- 参考位置：`go/main.go`
- 建议结构：

```text
go/
├── main.go
├── register.go
├── management.go
├── stats.go
├── types.go
├── source.go
├── dashboard.go
└── dashboard_test.go
```

- 验收：
  - 拆分后 `go test ./...` 通过。
  - CGO export 仍只集中在 `main.go`。

### 10. dashboard JS 独立测试

- 现状：dashboard JS 内联在 Go 字符串中，目前只能做语法检查。
- 影响：来源脱敏、API 聚合、筛选、导出逻辑缺少单元测试。
- 参考位置：`go/main.go:589`
- 建议：
  - 抽出可测试的 JS helpers，或在 Go 测试中提取 `<script>` 后用 Node 执行断言。
  - 覆盖 `trimCredentialSuffix()`、`sourceLabel()`、`friendlyApiName()`、`clientApiLabel()`。
- 验收：
  - `node --check` 和 helper 单测都纳入本地/CI。

### 11. 用具名 struct 替换注册响应中的 map

- 现状：插件注册、management 注册和导入导出响应中使用较多 `map[string]interface{}`。
- 影响：字段拼写、类型和 JSON 结构缺少编译期约束。
- 参考位置：`go/main.go:158`、`go/main.go:301`、`go/main.go:825`
- 建议：
  - 定义 `PluginRegisterResponse`、`ConfigField`、`ManagementRegisterResponse`、`ExportPayload`、`ImportResponse`。
- 验收：
  - JSON 输出结构不变。
  - 测试覆盖注册响应关键字段。

### 12. 补齐并发和快照一致性测试

- 现状：已有基础测试，但缺少高并发写入、快照隔离和导入边界测试。
- 建议：
  - `Record()` 并发写入正确性。
  - `Snapshot()` 返回深拷贝，调用方修改快照不影响内部状态。
  - `MergeSnapshot()` 对重复、过期、乱序数据的处理。
  - `Configure()` 降低 retention/max 时的扣减正确性。
- 验收：
  - `go test -race ./...` 长期保持通过。

## 建议执行顺序

1. 先做 P0-1/P0-2：把写入路径从全量重建改为增量维护。
2. 再做 P0-3/P0-4：控制 dashboard payload 和明细中的敏感/大字段。
3. 然后做 P1-5/P1-7：提升 API key 聚合准确性和错误体脱敏。
4. 最后做 P2 拆分与测试工程化，降低后续迭代成本。

## P0 补充

### 13. 运行时配置 YAML 解析不支持嵌套

- 现状：`yamlInt()` 只处理 `key: value` 顶层单行。
- 影响：CPA 实际传过来的 `config_yaml` 可能是嵌套结构：
  ```yaml
  configs:
    usage-statistics:
      max_details_per_model: 3000
      retention_days: 14
  ```
  当前代码解析不到嵌套值，静默回退到默认值，用户修改配置不会生效。
- 参考位置：`go/main.go:245`
- 建议：
  - 按行扫描 keyword，不关心缩进层级，遇 `max_details_per_model:` 即取值。
  - 或一次性引入 `gopkg.in/yaml.v3` 做小依赖解析。
- 验收：
  - 嵌套 YAML 中 `max_details_per_model: 100` 能正确覆盖默认 `5000`。

### 14. dashboard 轮询无退避

- 现状：`setInterval(load, 30000)` 固定间隔，失败后不做退避。
- 影响：CPA 短暂不可用时，所有打开的 dashboard 浏览器同步每 30 秒打一次失败请求，持续施压。
- 参考位置：`go/main.go:820`、`go/main.go:799`
- 建议：
  - `load()` catch 中指数退避（1s → 2s → 4s → … 上限 300s）。
  - 成功后恢复 30 秒。
- 验收：
  - 连续 3 次失败后，下一次请求间隔 >30s。
  - 恢复后间隔回到 30s。

## P1 补充

### 15. stripCredentialSuffix 不支持其他分隔符

- 现状：只处理 ` · ` 分隔符。
- 影响：旧版本导入数据或不同插件可能用 ` - `、` | `、`/` 分隔，后缀清理会失效。
- 参考位置：`go/main.go:1725`
- 建议：
  - 用正则 `/[·|\-\/]/` 匹配常见分隔符做通用剥离。
  - 增加 ` · ` 之外的分隔符测试用例。
- 验收：
  - `"opencode - sk-abc123"` 清洗后为 `"opencode"`。

### 16. usageGroupKey 去重条件可能过度合并

- 现状：
  ```go
  if source != "" && !looksLikeSecretKey(source) && source != provider && source != executor
  ```
- 影响：当 `provider = "openai"` 且 `source = "openai-prod"` 时，source 不会加入 parts，两个不同上游被合并为同一个接口。
- 参考位置：`go/main.go:1799`
- 建议：
  - 去掉 `source != provider` 条件，仅保留 `looksLikeSecretKey` 检查。
  - 或用 `strings.HasPrefix` 做更宽松的去重。
- 验收：
  - `provider="openai"`, `source="openai-prod"` → 接口名包含 `source`，不会合并。

### 17. 响应头白名单应可配置

- 现状：P0-4 建议加 header 白名单过滤敏感信息。
- 影响：硬编码白名单不适合所有部署环境。
- 参考位置：P0-4 下的 `RequestDetail.Headers`
- 建议：
  - 新增配置项 `log_response_headers`（如 `"x-request-id,x-ratelimit-*"`），支持通配符。
  - 不在白名单的 header 不写入 `RequestDetail`。
  - 空白名单表示不记录任何响应头。
- 验收：
  - 配置 `x-request-id` 后只保存该 header。
  - 默认空白名单不保存任何 header。

### 18. 错误体脱敏覆盖常见密钥前缀

- 现状：计划 P1-7 提出错误体脱敏，但未列出具体规则。
- 影响：上游错误回显的敏感数据模式多样。
- 参考位置：`go/main.go:1319`
- 建议：在 `trimLong()` 前增加 `redactSensitiveText()`，至少覆盖：
  - 常见 key 前缀：`sk-`、`AIza`、`hf_`、`pk_`、`rk_`
  - `Authorization:` / `Bearer` 头及其值
  - `x-api-key:` 头及其值
  - URL query 中的 `key=` / `token=` / `api_key=` 参数
  - 长度 ≥ 40 的连续无空格字符串
- 验收：
  - 错误体 `"Authorization: Bearer sk-abc123"` → `"Authorization: Bearer sk******23"`。
  - 脱敏不改变正常错误信息（如 JSON 字段名、模型名）。

## P2 补充

### 19. Snapshot hour key 每次字符串分配

- 现状：`fmt.Sprintf("%02d", hour)` 在每次 `Snapshot()` 调用中分配新字符串。
- 影响：dashboard 每 30 秒拉一次快照，产生不必要的 GC 压力。
- 参考位置：`go/main.go:1513`、`go/main.go:1524`
- 建议：
  - 包级别预定义 `var hourKeys = [24]string{"00","01",...,"23"}`。
  - 或直接将 `requestsByHour`/`tokensByHour` 类型改为 `[24]int64`，Snapshot 时用预计算 key 构造 map。
- 验收：
  - `Snapshot()` 不再调用 `fmt.Sprintf` 生成 hour key。

### 20. rebuildSeenLocked 在 Record 写入路径中多余

- 现状：`Record()` 写入时已执行 `s.seen[dedup] = now`，末尾又调用 `rebuildSeenLocked` 全量重建 seen 集合。
- 影响：每条记录写入都要遍历全部有效详情重建 seen map，与增量维护重复。
- 参考位置：`go/main.go:1346`、`go/main.go:1350`
- 建议：
  - `Record()` 中移除 `rebuildSeenLocked` 调用。
  - `rebuildSeenLocked` 仅在 `Configure()`（调整去重窗口后）和 `MergeSnapshot()` 后调用。
  - `pruneSeenLocked` 改为由定时器或每隔 N 次写入触发，而非每次写入。
- 验收：
  - 普通 `Record()` 路径不再遍历全量 seen map。
  - 去重行为不变。

### 21. 测试使用固定未来时间

- 现状：测试中硬编码 `"2026-06-25"`，到达该日期后测试可能失败。
- 参考位置：`go/main_test.go:22`
- 建议：
  - 用 `time.Now().Add(-24 * time.Hour)` 等相对时间替换绝对时间。
  - 涉及 retention 测试用 `time.Now().Add(-31 * 24 * time.Hour)` 构造过期记录。
- 验收：
  - 2027 年后 `go test ./...` 仍然通过。

## 调整后的执行顺序

| 阶段 | 内容 |
|------|------|
| **P0** | 计划 1（增量写入）+ 计划 2（排序优化）+ 补充 13（YAML 解析）+ 补充 14（dashboard 退避）+ 补充 20（移除多余重建） |
| **P1** | 计划 5（API key 分组）+ 计划 7（错误脱敏）+ 计划 8（导入限制）+ 补充 15（分隔符兼容）+ 补充 16（groupKey 条件）+ 补充 17（header 白名单）+ 补充 18（脱敏规则） |
| **P2** | 计划 9（拆文件）+ 计划 10（JS 测试）+ 计划 11（struct 替换 map）+ 计划 12（并发测试）+ 补充 19（hourKey）+ 补充 21（测试时间） |

## 暂不处理

- 持久化存储：用户已明确不需要。
- 国际化：当前使用场景以中文为主，先不引入 i18n 框架。
- 上游 CPA Source 字段根因修复：应另开 CPA 主仓 issue，本插件继续做防御性脱敏。
