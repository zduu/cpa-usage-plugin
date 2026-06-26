# 优化建议

> 基于 2026-06-26 对当前仓库的代码审查生成。已验证：`cd go && go test ./...`、`node --check go/dashboard/helpers.js go/dashboard/script.js`、`node --test go/dashboard/*.test.js` 均通过。

## 总体判断

项目当前结构已经比较清晰：Go 侧按注册、管理 API、统计引擎、来源清洗、看板接口拆分；前端也已从 Go 字符串拆到 `go/dashboard/` 并有 helper 单测。下一阶段最值得投入的是“避免用户拿到不完整数据”、“让兼容性测试真正进入 CI”、“提升大数据量下的统计查询性能”和“降低配置/发布维护风险”。

## 落地状态

| 优先级 | 内容 | 状态 |
|--------|------|------|
| P0 | 事件明细全量分批导出 | 已完成：导出路径复用 `fetchAllEventPages`，保留当前筛选条件 |
| P0 | 导入兼容 fixture 入库 | 已完成：迁移到 `go/testdata/usage-export-v1.2.0.json`，测试不再跳过 |
| P0 | `runtimeConfig` 零值歧义 | 已完成：新增 `runtimeConfigPatch`，运行时配置按显式字段应用 |
| P1 | 10 万/20 万级性能基准 | 已完成：新增 summary、events、merge、prune benchmark |
| P1 | 可选持久化 | 已完成：新增 JSONL append-only storage，重启 replay 并应用保留策略 |
| P1 | API key hash 跨重启稳定 | 已完成：新增 `api_key_hash_salt` 配置 |
| P1 | 响应头 wildcard 与敏感头保护 | 已完成：支持 `prefix-*`，默认拒绝敏感响应头 |
| P1 | 导入/导出报告增强 | 已完成：导入返回 input/accepted/rejected，导出带版本、detail_count、配置摘要 |
| P2 | 结构化 YAML 解析 | 已完成：使用 `gopkg.in/yaml.v3` 定位插件配置块 |
| P2 | 发布版本一致性检查 | 已完成：tag 构建校验 `pluginVersion` 与 tag 一致 |
| P2 | health 诊断增强 | 已完成：health 返回 config、storage、runtime 状态 |
| P2 | 前端关键交互测试 | 已完成：新增 Node VM + 最小 DOM mock，覆盖真实 dashboard 加载和全量分批导出 |

## P0：先修正确性和回归保障

### 1. 事件明细导出目前只导出前 500 条

位置：`go/dashboard/script.js`

当前 `exportRows` 和 `exportApiRows` 固定请求 `limit=500&offset=0`，当筛选结果超过 500 条时，CSV/JSON 文件会静默丢掉后续事件。页面文案和用户预期更接近“导出筛选条件下的全部明细”，这是最需要优先修的用户可见正确性问题。

建议：

- 新增 `fetchAllEvents(params)`，内部按 `limit=500`、`offset += 500` 循环调用 `/dashboard-events`，直到已取数量达到后端返回的 `total`。
- 表格展示仍保持最多 500 条，避免首屏和轮询变慢；只有导出走全量分批拉取。
- 导出时保留当前时间范围、模型、来源、凭证、上游接口筛选条件。
- 增加 JS 单测或轻量 fetch mock，验证 offset 调用序列和最终导出条数。

验收：

- 筛选结果 1200 条时，页面仍只展示 500 条，但导出的 CSV/JSON 包含 1200 条。
- 当前接口导出与全局事件导出都不再截断。

### 2. 导入兼容 fixture 不应放在被忽略的 `temp/`

位置：`go/main_test.go`、`.gitignore`

`TestHandleImportUsageAcceptsV120ExportFixture` 和 `TestManagementImportRouteAcceptsExportFixture` 依赖 `../temp/usage-export-2026-06-26T02-46-40-375Z.json`。`temp/` 被 `.gitignore` 忽略，因此 CI/新环境通常会跳过这两个关键回归测试，容易造成“本地曾经测过，CI 实际没测”的错觉。

建议：

- 将脱敏后的真实导出 fixture 移到 `go/testdata/usage-export-v1.2.0.json`。
- 测试从 `testdata` 读取，并取消 `t.Skipf`，fixture 缺失时直接失败。
- 如果 fixture 体积较大，保留一个最小但覆盖旧字段、PascalCase、details、导入统计的裁剪版本。

验收：

- CI 中导入兼容测试稳定执行，不再因为缺少 `temp/` 文件而跳过。
- 旧版导出文件的导入兼容性成为强制回归门禁。

### 3. `runtimeConfig` 零值存在误用风险

位置：`go/stats.go`、`go/register.go`

`Configure(runtimeConfig{RetentionDays: 0})` 无法区分“只想设置 retention 为 0”和“其他字段未设置”，因此会把 `MaxDetailsPerModel` 的零值也应用进去，导致明细被裁剪到 0。生产路径通过 `defaultRuntimeConfig()` 构造，风险较低，但测试和未来内部调用很容易踩坑。

建议：

- 引入 `runtimeConfigPatch` 或使用指针字段区分“未设置”和“显式设置为 0”。
- 保留 YAML 中 `retention_days: 0`、`dedup_window_minutes: 0` 的现有语义。
- 增加测试覆盖“只改一个字段不会影响其他字段”。

验收：

- `Configure` 的单字段调用不会意外改变其他配置。
- 现有配置解析和默认值行为保持兼容。

## P1：提升大数据量和生产可用性

### 4. 统计汇总和事件查询需要高数据量性能基准

位置：`go/stats.go`

`SummaryWithoutDetails()` 每次轮询都会遍历全部明细，`QueryEvents()` 会收集并排序所有匹配事件。当前有保留策略兜底，但在多接口、多模型、高并发请求下，30 秒轮询会带来 CPU、内存分配和 `RWMutex` 读锁持有时间增长。

建议：

- 增加 10 万、20 万明细规模的 benchmark，覆盖 summary、events query、import、prune。
- 将来源、模型、客户端 API、健康网格等聚合改为写入时增量维护，或做 summary 缓存并在 Record/prune/import 后失效。
- `QueryEvents` 优先利用 API/model 维度缩小扫描范围；只需要第一页时避免全量排序，可考虑 top-N/partial sort。

验收：

- 20 万明细下，summary 和常用事件查询耗时、分配量有明确基准。
- 看板轮询不会明显阻塞 `usage.handle` 写入。

### 5. 增加可选持久化，降低重启丢数据风险

位置：`go/stats.go`、`go/register.go`、`CPA_USAGE.md`

当前统计仅在插件进程内存中，文档已提醒重启前导出。对长期运行的 CPA 实例来说，手工导出容易遗漏，且多实例/容器重建场景风险更高。

建议：

- 增加可选配置，例如 `storage_enabled`、`storage_path`、`flush_interval_seconds`。
- 初期可用 JSONL append-only 记录请求事件，启动时 replay；后续再评估 SQLite/BoltDB。
- 启动 replay 后复用现有保留策略和去重逻辑，避免旧数据无限增长。

验收：

- 开启持久化后，CPA 重启不会丢失保留窗口内的统计。
- 存储损坏时插件可降级启动，并在 health 中暴露错误。

### 6. API key 分组哈希需要跨重启稳定

位置：`go/stats.go`

`apiKeySalt` 当前是进程启动时随机生成。这样可以减少泄露风险，但同一个客户端 API key 在重启前后会得到不同 `api_key_hash`，导入旧数据后也可能拆成多个分组，影响“按调用 CPA 的 API key 聚合”的长期统计。

建议：

- 引入可持久化的随机 salt，首次生成后保存到插件状态目录；或允许用户配置 `hash_salt`。
- 对已有数据保留旧 hash，但新版本开始保证同一实例跨重启稳定。
- 文档说明 hash 只用于分组，不可反推原 key。

验收：

- 同一 API key 在重启前后归入同一个客户端 API 分组。
- 未配置持久化 salt 时仍不保存明文 key。

### 7. 响应头白名单语义和安全边界需要收紧

位置：`go/source.go`、`go/register.go`、`CPA_USAGE.md`

配置说明提到 `*` 通配，测试样例里出现 `x-ratelimit-*`，但 `filterHeaders` 目前只支持精确 header 名或全量 `*`。同时全量 `*` 可能记录 `set-cookie`、`authorization` 等敏感响应头。

建议：

- 明确支持 `prefix-*` 形式，例如 `x-ratelimit-*`。
- 即使配置 `*`，也默认拒绝保存 `authorization`、`proxy-authorization`、`cookie`、`set-cookie`、`x-api-key` 等敏感头。
- 如果确实需要全量调试，可增加显式危险配置，例如 `log_sensitive_response_headers: true`。

验收：

- `x-ratelimit-*` 能匹配 `x-ratelimit-remaining`。
- 默认情况下敏感响应头不会进入导出文件或 dashboard events。

### 8. 导入/导出报告应更利于排查

位置：`go/management.go`、`go/stats.go`

导入响应已有 `added`、`skipped`、`ignored_by_retention`，但当用户导入失败或数量不符合预期时，还缺少输入记录总数、被忽略位置、API/model 分布等信息。

建议：

- `ImportResponse` 增加 `input_records`、`accepted_records`、`rejected_records`。
- `invalid_record` 返回具体 API、model、detail index 和字段名。
- 全量导出增加 `detail_count`、插件版本、配置摘要，便于后续兼容判断。

验收：

- 用户能从导入结果判断“文件里有多少条、实际新增多少条、为什么没进来”。
- 负 token 等错误能定位到具体记录。

## P2：降低维护和发布风险

### 9. 用 YAML 解析器替代手写行扫描

位置：`go/register.go`

当前配置解析通过逐行查找 key 实现，简单但容易被注释、同名 key、缩进层级、复杂字符串影响。插件配置来自完整 CPA YAML，长期看建议改成结构化解析。

建议：

- 使用 `gopkg.in/yaml.v3` 解析到通用 map，再定位 `plugins.configs.usage-statistics`。
- 如果不希望引入依赖，也至少限制 key 只在当前插件配置块内生效，并补充缩进/注释测试。

验收：

- 同名 key 出现在其他插件配置中时，不会误读。
- 带注释、引号、嵌套配置的样例都有单测覆盖。

### 10. 发布版本需要自动一致性检查

位置：`go/register.go`、`.github/workflows/build.yml`

插件版本硬编码在注册响应中，Git tag 和注册版本没有强制一致。CI 里 Go 版本也使用 `1.x`，可复现性不够强。

建议：

- 将插件版本提取为常量，tag 构建时校验 `Version == ${GITHUB_REF_NAME#v}`。
- CI 固定 Go 小版本，README、`go.mod`、workflow 保持一致。
- Release notes 自动写入测试结果、产物 SHA256、构建目标。

验收：

- tag `v1.2.8` 只能发布注册版本为 `1.2.8` 的产物。
- 同一提交重复构建时工具链版本明确。

### 11. 前端测试从 helper 单测扩展到关键交互

位置：`go/dashboard/script.js`、`go/dashboard/helpers.test.js`

目前 helper 单测覆盖了解包、URL、格式化等纯函数，但 dashboard 的真实点击流程主要靠字符串检查。导入、导出、筛选、轮询回退这些都属于容易回归的交互。

建议：

- 使用 jsdom 或 Playwright 增加最小交互测试。
- mock `/dashboard-summary`、`/dashboard-events`、`/usage/export`、`/usage/import`。
- 覆盖摘要加载失败后的 `/dashboard-data` 兼容回退、导入成功/失败 alert、导出全量分批。

验收：

- 修改前端路由或 unwrap 逻辑时，关键用户流程能在 CI 里失败提示。
- 不再只依赖 `strings.Contains(completeDashboardHTML, "...")` 判断前端行为。

### 12. health 端点增加运行诊断信息

位置：`go/dashboard.go`、`go/stats.go`

`/health` 目前返回状态、明细数、总请求数、淘汰数。排查现场问题时，还需要知道最近记录时间、配置、存储状态、summary/query 耗时等。

建议：

- 增加 `started_at`、`last_recorded_at`、`config`、`seen_count`、`last_import`。
- 如果做持久化，增加 `storage_status`、`storage_path`、`last_flush_at`。
- 可选记录最近一次 summary/query 的耗时，便于发现看板拖慢插件。

验收：

- 用户只通过 `/health` 就能判断插件是否还在收到 usage、是否因保留策略淘汰、是否存在存储异常。

## 建议实施顺序

1. 先完成 P0 三项：全量导出、fixture 入库、配置零值修复。
2. 再做 P1 中的性能基准与 summary/query 优化，避免在没有量化数据时过早重构。
3. 持久化、稳定 API key hash、导入报告作为生产可用性增强一起规划。
4. 最后补齐 YAML 解析、发布一致性和前端交互测试，降低长期维护成本。
