# 功能实现说明

> 三件事：成本/请求/token/RPM 趋势、用量突变提醒、模型效率对比。都在现有统计数据基础上增加展示层。

---

## 功能 1：每日成本走势图

### 用户看到什么

看板健康网格下方增加一个**独立的趋势图区域**，默认显示成本趋势，可切换显示请求数、token 数和平均 RPM。`7h` / `24h` 使用小时聚合，`7d` / `all` 使用日期聚合。

大概长这样：

```
 ┌─────────────────────────────────────────────────────┐
 │  📊 趋势   [每日成本 ▾]                              │
 │                                                     │
 │   $15                                              │
 │        ╱╲                                          │
 │   $10 ╱  ╲    ╱╲                                  │
 │      ╱    ╲╱╱  ╲                                  │
 │   $5 ╱          ╲___                               │
 │     ┌─────────────────────────────                  │
 │     7/1   7/2   7/3   7/4   7/5   7/6   7/7          │
 └─────────────────────────────────────────────────────┘
```

切换选项：成本 / 请求数 / token 数 / 平均 RPM。趋势图是更宽更高的独立视图。

### 后端改动

`StatisticsSnapshotWithoutDetails` 已包含 `RequestsByDay` / `RequestsByHour` / `TokensByDay` / `TokensByHour`。本次补充成本序列：

新增字段：

```go
// types.go
type StatisticsSnapshotWithoutDetails struct {
    // ... 现有字段 ...
    CostByDay  map[string]float64 `json:"cost_by_day,omitempty"`
    CostByHour map[string]float64 `json:"cost_by_hour,omitempty"`
}
```

成本序列不使用 blended price 粗略估算，而是按 `(provider, model)` 聚合 token 后用对应价格计算。完整快照额外保存 `CostTokensByDay` / `CostTokensByHour`，这样即使明细被裁剪，更新模型价格后也能对历史成本序列重新计价。

### 前端改动

- `go/dashboard/script.js`：新增 `renderTrendChart()`，从 `summaryData.usage.cost_by_day` / `cost_by_hour` / `requests_by_day` / `requests_by_hour` / `tokens_by_day` / `tokens_by_hour` 取数据
- `go/dashboard/index.html`：在健康网格上方新增趋势图区域
- `go/dashboard/style.css`：趋势图样式（200px 高，比现在 44px 的 sparkline 大得多）
- 纯 SVG 柱线组合图，X 轴标日期或小时，Y 轴标数值，hover 显示精确值

---

## 功能 2：用量突变检测

### 用户看到什么

趋势图旁边（或下方）显示一个提示条。当最近一段时间的用量相比之前明显异常时亮起：

```
  ⚠️ 今日请求数较昨日增长 320%（昨日 1,200 → 今日 5,040）
  ✅ 近7天用量稳定，无明显波动
```

切换趋势图的同一时间窗口联动：选了"每日成本"，突变检测就看每日成本的变化。

### 检测逻辑

纯前端计算，后端不需要改：

- 比较规则：从当前趋势点里取最近窗口和前一窗口做平均值对比，窗口最多 7 个点。
- 阈值：差异超过 ±100%（翻倍或减半）视为突变
- 不是告警，只是提示——让用户自己判断是不是正常业务变化

**联动设计：** 趋势图下拉框是"每日成本 / 每日请求 / 每日 token / 每日 RPM"，突变检测自动跟随这个选择，用同一个数据源。

---

## 功能 3：模型效率对比

### 用户看到什么

模型统计表新增列，让用户能直观比较各模型的性价比：

**现状（6 列）：**

| 模型 | 请求数 | Token数 | 平均延迟 | 成功率 | 花费 |
|------|--------|---------|----------|--------|------|

**改后（8 列）：**

| 模型 | 请求数 | Token数 | 平均延迟 | 成功率 | 缓存率 | 花费 | 单价 |
|------|--------|---------|----------|--------|--------|------|------|

新增两列的含义：

- **缓存率**：`cached_tokens / input_tokens × 100%`。这里的 `input_tokens` 按 API 约定已经包含 cached tokens。
- **单价**：`花费 / 总token × 1M`，即每百万 token 的实际花费。不同模型价格不同但实际使用中缓存率、推理 token 都有影响，这个"实际单价"能直观对比性价比

### 实现

纯前端：

```js
// helpers.js 新增
function cacheRate(row) {
  const cached = num(row.cached_tokens);
  const input = num(row.input_tokens);
  if (input === 0) return 0;
  return Math.min(100, cached / input * 100);
}

function costPerMillion(row, prices, manualPrices) {
  const cost = aggregateCost(row, prices, manualPrices);
  const tokens = num(row.total_tokens);
  if (tokens === 0) return 0;
  return cost / tokens * 1e6;
}
```

后端 `ModelStat` 结构体已有 `CachedTokens`，不需要改。

---

## 总结

| 功能 | 后端改动 | 前端改动 | 复杂度 |
|------|----------|----------|--------|
| 成本走势图 | 加 `cost_by_day` / `cost_by_hour` 及快照 token 聚合字段 | 新趋势图组件 + 下拉切换 | 中 |
| 用量突变检测 | 无 | 比较两段数据 + 提示 UI | 低 |
| 模型效率对比 | 无 | 模型表加两列 | 低 |

三个功能共享同一套每日数据，前端趋势图和突变检测联动同一个下拉框，逻辑内聚、改动集中。
