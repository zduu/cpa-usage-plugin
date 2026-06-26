# 优化建议

> 本文档基于 2026-06-26 v1.2.2 代码审查生成。

## 本轮已完成

| 优先级 | 内容 | 结果 |
|--------|------|------|
| P0 | 轻量 dashboard 接口，首包不返回 details | 已完成：`/dashboard-summary` + `/dashboard-events` |
| P0 | 修复 v1.2.0 导出数据再次导入失败/误报失败 | 已完成：真实导出文件 `temp/usage-export-2026-06-26T02-46-40-375Z.json` 回归通过 |
| P0 | `SummaryWithoutDetails` 延迟统计去重 | 已完成：一次遍历累积 latency sum/count |
| P1 | 前端资源拆分并通过 `go:embed` 注入 | 已完成：`go/dashboard/` |
| P1 | dashboard 可观测性元数据 | 已完成：retention、max details、detail count、last import、evicted total |
| P1 | 导入清洗逻辑合并 | 已完成：handler 只做 token 负值门禁，latency/TTFT 归零集中在 `MergeSnapshot` |
| P2 | 统计引擎拆分 | 已完成：`types.go / register.go / management.go / dashboard.go / stats.go / source.go` |
| P2 | CI 缓存与 benchmark 回归 | 已完成：`setup-go` 缓存 + 短 benchmark |

说明：前端事件明细继续保持滚动列表，不增加分页按钮；后端仍保留 `limit/offset` 能力，供 dashboard 内部加载和未来导出增强使用。

---

## P0：补齐 dashboard 导入/导出端到端测试

**位置**：`go/dashboard/script.js`、`go/dashboard/helpers.js`

本轮已为 response unwrap helper 增加 Node 单测，也用真实 v1.2.0 导出文件覆盖了 Go handler。但还没有浏览器级测试能证明用户从 dashboard 点击“导出数据”再选择同一 JSON 导入时，UI 不会误报失败。

建议：
- 增加轻量 Playwright 或 jsdom 测试，mock `fetch('./usage/export')` 和 `fetch('./usage/import')`。
- 覆盖三种响应形态：直接业务 JSON、插件 envelope、ManagementResponse body。
- 校验 alert 文案包含新增/跳过/过期忽略数量。

验收：
- dashboard 导入成功路径不会出现“导入失败”。
- 插件错误 envelope 能显示后端错误信息，例如 `invalid_json`。

---

## P1：导出明细时内部自动分批拉取

**位置**：`go/dashboard/script.js`

当前前端表格按用户要求保持滚动查看，事件接口每次仍限制 `limit=500`，导出“请求事件明细”和“当前接口明细”也只取前 500 条。后端已经支持 `offset`，可以在导出时内部循环拉取所有批次，不需要增加分页 UI。

建议：
- 新增 `fetchAllEvents(params)`，按 `limit=500`、`offset += 500` 循环，直到取满 `total`。
- 表格展示仍只加载最近 500 条，避免首屏变慢。
- 导出按钮使用完整批次数据。

验收：
- 请求事件超过 500 条时，页面仍为滚动列表。
- CSV/JSON 导出包含筛选条件下的全部事件。

---

## P1：消除 `runtimeConfig` 零值歧义

**位置**：`go/stats.go:105-124`

`Configure(runtimeConfig{RetentionDays: 0})` 会把未显式设置的 `MaxDetailsPerModel` 也更新为 0，导致明细被全部裁剪。生产路径通过 `defaultRuntimeConfig()` 构造配置，风险较低，但测试和未来内部调用容易踩坑。

建议：
- 引入 `runtimeConfigPatch` 或指针字段区分“未设置”和“设置为 0”。
- 保留 YAML 配置中 `retention_days: 0`、`dedup_window_minutes: 0` 的现有语义。
- 更新测试 helper，避免每个测试都手写完整配置。

验收：
- `Configure(runtimeConfig{RetentionDays: 0})` 不再意外改变 `MaxDetailsPerModel`。
- 现有配置解析行为保持兼容。

---

## P2：发布产物增加版本一致性检查

**位置**：`.github/workflows/build.yml`、`go/register.go`

版本号目前只存在于注册响应字符串和 Git tag 中，发布前没有自动校验二者一致。

建议：
- CI tag 构建时检查 `go/register.go` 中 `Version` 等于 `${GITHUB_REF_NAME#v}`。
- Release notes 中自动写入测试和 benchmark 摘要。

验收：
- tag `v1.2.2` 只能发布注册版本 `1.2.2` 的产物。
- 版本不一致时 CI 失败。

---

## P3：导入报告增加明细统计

**位置**：`go/management.go`、`go/stats.go`

当前导入响应返回 added/skipped/ignored_by_retention。排查用户数据问题时，还需要知道输入文件中解析到多少条、因空 API/model 被忽略多少条、因 token 负值被拒绝的位置。

建议：
- `ImportResponse` 增加 `input_records`、`accepted_records`。
- 对 `invalid_record` 错误返回 API/model/索引位置。

验收：
- 用户导入失败时能定位到具体异常记录。
- 成功导入时能看出文件记录数与实际新增数差异原因。
