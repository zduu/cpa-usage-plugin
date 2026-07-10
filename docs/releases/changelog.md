# Changelog

本文件用于维护每个 GitHub Release 的人工说明。发布 tag 前必须把 `Unreleased` 中本次要发布的内容整理到对应版本小节，例如 `## v2.2.2 - 2026-07-01`。

`v1.0.0` 到 `v1.2.18` 为规范化发布流程建立前的 legacy 历史版本，不在本文件中回填；对应说明见 [v1-history.md](v1-history.md)。

## v2.3.2 - 2026-07-10

本版本修复了统计卡片顶部颜色条纹右侧渐隐消失的视觉问题。

### 视觉修复
- 统计卡片顶部颜色条纹恢复为纯色填充，不再使用从左到右渐隐的渐变效果

## v2.3.1 - 2026-07-10

本版本优化了 Dashboard 看板的视觉风格与 UI 细节。

### 视觉更新
- 重构看板整体配色方案，更新主题色变量与色彩系统，提升明暗模式下的视觉一致性
- 优化统计卡片、表格、按钮、Tab 切换等组件的外观样式与交互反馈
- 请求量迷你图表（sparkline）增加面积填充效果，调整配色以增强数据区分度
- 修复多处间距、边框、阴影和圆角等 UI 细节

## v2.3.0 - 2026-07-10

本版本对 v2.2.8 引入的响应拦截兜底统计做了一轮系统性加固，是针对「CPA `v7.2.53+` 下 OpenAI 兼容渠道（如 deepseek-v4 系列）在 Claude Code CLI 调用时看板缺记录」问题的完整收尾。审查与验证过程见 [docs/issues/2026-07-09-deepseek-usage-fallback-review.md](../issues/2026-07-09-deepseek-usage-fallback-review.md)。

### 兜底统计与去重加固
- 兜底 provider 识别改为优先信任 CPA conductor 实际下发的 `selected_auth_id`，推测性的 `upstream_provider` 等 metadata 键降级为次选，客户端协议（SourceFormat）只作最后回退；彻底避免 Claude Code 调用 OpenAI 兼容渠道时兜底记录被误分组为 `claude` 上游
- 去重指纹的 input/total 改为「缓存全含 + 重算」的规范口径：Claude 家族上游（原生解析 input 不含 cache）在指纹中补入 cache_read/cache_creation，其余上游保持 prompt_tokens 原语义；同一次请求无论走原生记录、Claude 协议翻译还是 OpenAI 协议翻译，指纹一致，修复真实 Anthropic 上游带缓存请求可能双计的问题
- 去重指纹不再包含 reasoning_effort 和 service_tier：两侧来源不同（原生取自翻译后载荷、兜底取自拦截 metadata），可能取值不一致导致去重失效；token 三元组加模型、key、上游家族已足够区分请求
- Claude 家族上游的兜底记录保持 input 不含缓存、total 计入缓存的原生口径，与 CPA 原生 Claude usage 解析一致；其余上游继续把 Claude 格式响应的缓存 token 折回 input（并修复了全缓存命中时 input=0 不回补的边界）
- 流式兜底不再解析 `message_start` 的 `message.usage` 预生成快照，消除 Claude 协议流式响应可能多调度一条永不匹配的幽灵兜底记录的问题
- 利用 CPA 下发的 `HistoryChunks`：同一流中较新的 usage chunk 会替代（supersede）此前 chunk 调度的待写入兜底，支持每个 chunk 携带累计 usage 的上游（如部分 kimi/glm 渠道），只提交最终快照，不再多计
- 兜底解析补充 OpenAI 嵌套字段 `prompt_tokens_details.cached_tokens`、`input_tokens_details.cached_tokens`、`completion_tokens_details.reasoning_tokens`、`output_tokens_details.reasoning_tokens`，兜底记录的缓存/推理 token 与原生记录对齐

### 凭证维度一致性
- 新增 auth index 学习：插件从原生 usage 记录和启动恢复的持久化明细中学习每个 auth id 对应的 CPA auth index，兜底记录复用学习到的 index，凭证维度不再因原生/兜底混合而分裂成两个凭证分组
- 兜底记录提交前会再次查询学习表，捕捉等待窗口内刚学到的映射

### 说明
- CPA `v7.2.53/54` 下 OpenAI 兼容渠道原生 `usage.handle` 间歇性缺失的现象仍存在于 CPA 侧（本地实测同一渠道部分请求有原生记录、部分只有兜底记录）；本插件的兜底链路已在实测中验证可完整补位并正确去重
- 旧版本（≤2.2.8 开发版）已写入的 `claude · 上游 xxx` 错误分组历史数据不会被自动改写，会随保留窗口自然过期；如需立即清理可通过导出-筛选-导入完成

## v2.2.9 - 2026-07-09

### 问题修复
- 修复 Codex OAuth 上游使用 `codex-*.json` 形式 auth id 时，响应流兜底统计被误归类为通用 `openai-compatible`，导致同一次请求同时产生 Codex 原生 usage 和兜底 usage 两条记录的问题
- 兜底统计现在会从 `codex-*.json` auth id 正确识别 provider 为 `codex`、auth type 为 `oauth`，从而与 CPA 原生 usage 使用同一去重指纹

## v2.2.8 - 2026-07-09

### CPA 兼容性说明
- CPA `v7.2.53` 调整后，部分 OpenAI 兼容模型可能不再稳定向 usage 插件下发原生 `usage.handle` 记录，旧版插件会表现为有模型响应但看板没有上游和调用记录
- 本版本声明 `response_interceptor` 和 `response_stream_interceptor` 能力，在 CPA 原生 usage 记录缺失时，从成功响应体或流式 chunk 中读取 `usage`/`usageMetadata` 等字段并延迟写入兜底统计；如果原生 usage 随后到达，会避免重复计数。去重指纹按 input/output/total 等核心字段匹配，避免 native 与 fallback 对 reasoning/cache 等可选字段解析不一致时重复计数
- 兜底统计只读取响应中的 token 用量，不修改 CPA 响应内容；当 CPA 没有在响应拦截请求中提供已选上游信息时，兜底记录可能显示为通用 `openai-compatible` 上游，原生 usage 记录仍会保留精确上游名称。流式 chunk 兜底没有完整请求耗时上下文，因此延迟字段可能为空或为 0
- 旧版历史数据和新版稳定 hash 分组可能让同一个 CPA API key 在看板中短暂分裂，本版本会在同一脱敏显示值只有唯一 hash 时合并无 hash 历史分组；遇到多个不同 hash 时保留分组，避免误合并不同真实 key。未配置 `api_key_hash_salt` 时，新记录改用插件默认稳定 salt，避免重启或升级后继续产生新的 hash 分组

### 问题修复
- 修复 Dashboard 导出在 Safari 下可能因 `blob:` 下载链接过早释放而失败的问题
- 修复 OpenAI 兼容模型返回的 usage 载荷字段不完全标准时未写入统计，导致看板没有上游和调用记录的问题
- 修复 Claude/Anthropic 流式 usage 中缓存输入 token 口径与原生 usage 不一致时可能重复计数的问题
- 修复失败状态的流式 chunk 仍可能写入兜底 usage 的问题
- 修复同一客户端 API key 因 `Bearer ...`、`Bearer:...` 和原始 key 格式不同导致展示、hash 或去重不一致的问题
- 修复同一 CPA API key 在旧版脱敏格式和新版稳定 hash 分组之间被识别为两个客户端 API key 的问题
- 修复旧持久化/导入数据中 API key 脱敏、分组和去重的多处边界问题
- 修复 Dashboard ETag、`generated_at` 和查询数据版本不一致导致的缓存响应不稳定问题
- 修复 SSE 事件中多条独立 `data:` JSON 行可能只解析第一条或被丢弃的问题

### 改进
- 优化 Dashboard 事件索引、API 详情聚合和后台导出分页的临时分配
- Dashboard 凭证筛选改为使用摘要中的全量凭证统计，避免受当前事件页限制
- Dashboard 自动兼容合并旧版无 hash 的客户端 API key 统计，减少升级后的历史数据割裂

## v2.2.7 - 2026-07-07

### 新增功能
- 趋势图小时轴按当前小时循环排序，午夜数据点排至末尾，更符合阅读习惯

### 问题修复
- 修复 OpenAI 兼容 provider 的 source 名称中非标准 API key 泄露问题
- 修复 usage group key 中 provider 前缀与 source 首段重复拼接的问题
- 修复 Dashboard 条件请求缓存遇到 304 等价空响应时被覆盖为空，导致请求事件明细在轮询后显示 0 条的问题

### 改进
- Dashboard 摘要元数据新增 `current_hour` 字段，由后端本地时间填充，避免跨时区偏差

## v2.2.6 - 2026-07-06

### 新增功能
- 兼容模式(fallback)根据选定时间范围(7h/24h/7d)重新计算聚合，不再始终展示全量数据
- 兼容模式范围过滤优先使用 detail 级别的实际 model 名称，修复外层 alias key 与 detail model 不一致时的统计偏差
- 模型统计新增按 provider 拆分的统计数据

### 问题修复
- model-prices 接口失败时静默容错，避免误触发降级路径导致范围过滤丢失
- Go 后端零时间戳事件在范围查询(7h/24h/7d)中统一排除，防止旧数据污染范围统计

### 移除
- 移除趋势图异常检测横幅(detectAnomaly)及中/繁/英/俄 4 种语言对应的 i18n key

### 测试
- 新增兼容模式范围过滤、detail model 优先级、时区桶偏移、model prices 容错等前端测试
- 新增零时间戳事件在 Go 后端范围查询中被排除的测试

## v2.2.5 - 2026-07-03

### 发布说明
- 顺延原 `v2.2.4` 发布，纳入导出链路、models.dev 价格显示和缓存稳定性相关修复后再发版
- 该版本用于匹配插件二进制文件名、插件注册版本和文档示例中的 `v2.2.5` release tag

### 问题修复
- 修复模型统计中实际单价单位显示为 `/M tok` 的问题，改为完整显示 `/M token`
- 修复插件资源 iframe 中当前接口表格和当前接口明细导出失败的问题：后台导出任务创建、状态查询、下载和清理统一走 management 端点，并携带管理密钥鉴权
- 修复当前接口 JSON 导出在无数据时没有生成明确结果的问题，现在会导出空数组而不是静默失败
- 修复启用 models.dev 默认价格源后，价格表单仍显示为 0 的问题，并补齐 provider/model 与裸模型名的回退匹配
- 优化 API 详情 range 查询的 ETag 稳定性，减少按当前秒变化导致条件请求难以命中的情况

### 测试
- 增加按 API 筛选导出的后端覆盖，以及资源 iframe 场景下异步导出走 management 端点的前端覆盖
- 修复存储 snapshot 周期测试等待 worker 状态上报的竞态，降低 `go test ./...` 的偶发失败风险

## v2.2.4 - 2026-07-03

### 新增功能
- 新增可选 models.dev 默认模型价格源，后端定时拉取 `api.json` 并提供全局默认价格
- 支持 provider/model 价格键，按上游 provider 区分同名模型价格

### 问题修复
- 手动设置的价格始终覆盖 models.dev 默认价格
- 模型价格增删改和费用估算均改为大小写不敏感匹配
- 模型汇总费用按 provider 拆分计算，避免同名模型混用不同 provider 价格时估算错误

### 其他
- 文档补充 models.dev 价格源配置、优先级和分层价格暂不支持说明
- 增加 models.dev 拉取、重配置、取消请求、手动覆盖和 provider 计费相关测试

## v2.2.3 - 2026-07-03

### 问题修复
- 修复 API 详情错误统计长消息在桌面端过早省略的问题，改为完整单行显示并由表格容器横向滚动
- 修复 API 详情布局被超长错误文本撑开的风险
- 修复 API 详细统计中成功/失败计数贴连的问题，保留千分位格式并加入稳定间隔

### 改进
- 移除 API 详细统计卡片右侧无交互用途的箭头
- 微调仪表盘字体、阴影、表头和焦点样式，提升数据面板可读性和键盘焦点可见性

## v2.2.2 - 2026-07-01

### 问题修复
- 修复仪表盘语言不跟随 CPA 管理面板切换的问题，兼容 CPA Zustand JSON localStorage 语言状态
- 修复表格表头、指标标签、空状态、筛选选项和 API 详情在语言切换后未重新渲染的问题
- 修复静态 i18n 初始化覆盖「最后更新」动态时间的问题
- 修复日期、数字和货币格式化器在语言切换后仍沿用旧 locale 的问题
- 修复存储队列容量不可用时仍显示占位容量的问题

### 改进
- 延迟时间显示统一为小于 1 秒使用 `ms`，大于等于 1 秒使用 `x.xxs`

## v2.2.1 - 2026-06-30

### 新增功能
- 仪表盘面板自动跟随 CPA 日间/夜间主题切换
- CSS 颜色全面重构为自定义属性（`—cpa-bg-*`, `—cpa-text-*`, `—cpa-border-*` 等），支持三层主题覆盖

### 问题修复
- 修复插件运行在 iframe 中无法检测 CPA 管理面板主题的问题：通过 `window.parent.document` 跨 iframe 读取 `data-theme`，并以 `localStorage(cli-proxy-theme)` + OS `prefers-color-scheme` 作为回退策略
- 修复 `MutationObserver` 监听父窗口 `data-theme` 属性变化，CPA 切换主题时实时跟随
- 修复 `storage` 事件监听跨窗口主题变更

## v2.2.0 - 2026-06-30

### 插件商店集成
- CI 新增插件商店兼容资产：每个平台生成 `usage-statistics_{version}_{goos}_{goarch}.zip` 和 `checksums.txt`
- Release 同时上传 zip 资产，可直接用于 CPA 插件商店一键安装
- 文档新增插件商店安装指南（自定义商店源 / config store 配置 / 管理面板操作）

## v2.1.0 - 2026-06-30

### 新增功能
- 时间范围切换现在作为全局筛选条件，影响顶部统计、上游接口统计、模型统计和 API 详情
- 新增后端范围摘要接口 `SummaryWithoutDetailsForRange`，支持 7h/24h/7d 时间窗口聚合
- 前端 summary 请求自动携带当前时间范围参数
- API 详情缓存按 `api + range` 隔离，避免不同时间范围串缓存

### 问题修复
- 修复切换到"全部"时间范围时 `Cannot read properties of null (reading 'events')` 崩溃
- 修复 API 详情被硬编码为 `range=all`，现在跟随时间范围选择器
- 修复 summary 接口不接收 range 参数的问题

### 其他
- 新增 10 个后端时间范围测试用例
- 更新时间范围相关前端测试断言
- macOS 二进制部署文档补充

## v2.0.2 - 2026-06-29

### 问题修复
- 修复移动端错误统计表格超长 HTML 内容溢出边框
- 修复移动端最近请求和请求事件明细表格来源列长文本溢出
- 错误文本改用 span 包裹确保 max-width 截断在桌面端正确生效

## v2.0.2 - 2026-06-29

### 问题修复
- 修复看板 API 明细页面在 summary 已知失败数为 0 时仍显示"请求明细加载失败"的问题，改为显示"暂无失败请求"
- 修复看板 API 明细页面测试用例不匹配实际显示内容的问题

## v2.0.0 - 2026-06-29

### 重大变更
- 新增轻量级看板摘要接口，不再返回全量 details 数组
- 新增事件导出后台任务（支持 csv/json），替代同步导出
- 新增 ETag 条件请求缓存机制，大幅降低看板轮询开销
- 新增持久化写入状态监控（队列压力、p95/p99 耗时等）
- 前端 UI 全面重构：健康网格、上游接口详情、来源分布、凭证统计、Client API 分组

### 问题修复
- 修复花费显示 reasoning_tokens 重复计费问题
- 修复 totalTokens fallback 口径不一致（统一为 input + output）
- 修复平均延迟丢失毫秒精度的问题
- 修复看板空响应导致报错的问题
- 修复摘要兼容模式因新字段误回退的问题

### 其他
- 延迟格式从中文改为英文缩写（s/ms）
- GitHub Actions 依赖升级至最新版本
