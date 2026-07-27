# Changelog

本文件用于维护每个 GitHub Release 的人工说明。发布 tag 前必须把 `Unreleased` 中本次要发布的内容整理到对应版本小节，例如 `## v2.2.2 - 2026-07-01`。

`v1.0.0` 到 `v1.2.18` 为规范化发布流程建立前的 legacy 历史版本，不在本文件中回填；对应说明见 [v1-history.md](v1-history.md)。

## Unreleased

## v2.5.5 - 2026-07-28

### Codex OpenAI 协议兜底去重
- 修复 CPA 响应拦截元数据缺少 `selected_auth_id` 时，同一 Codex 请求同时落入 `openai-compatible` 与 Codex OAuth 上游组的问题；仅对无认证身份、零延迟的 `/v1/chat/completions` 或 `/v1/responses` 兜底，在相同模型、客户端 API、token 明细和 1 秒完成时间窗口内与 Codex 原生 usage 一对一合并
- 保留原生 Codex 的认证、延迟和 TTFT，并从响应兜底补齐端点、推理强度与流式标记；即使 CPA 未提供相关元数据，也会从 OpenAI 请求协议恢复端点和显式 reasoning effort
- 支持兜底先到、原生先到和兜底已提交后的乱序情况，并在导入、storage snapshot、JSONL 重放及热重载时修复已有重复；无原生对应项、跨时间窗口及非 Codex 上游不会被合并

### DeepSeek 上游归因兼容
- 兼容新版 CPA 将成功的 `deepseek-*` 原生 usage 记录成 `claude:apikey` 身份的情况，避免看板产生 `claude · 上游 <auth_index>` 错误分组
- 只有同一客户端 API、同一 DeepSeek 型号族存在唯一的 `openai-compatible-<name>` 具体身份旁证时才纠正；优先限定同一端点，旧记录缺少端点时要求跨端点候选仍唯一；候选冲突、失败请求、匿名请求和真实 Claude 模型均保留原记录
- 实时可疑记录最多等待 90 秒，同时支持导入、storage snapshot 和跨 JSONL 分片冷启动恢复时迁移已有异常明细
- 归因迁移保留原始模型、时间、延迟、TTFT 和执行器信息，并同步转换 Claude 独占缓存输入口径，避免影响 token 与费用计算

## v2.5.4 - 2026-07-26

### Codex OAuth 源回退修复
- 当 CPA 短暂将 Codex OAuth 请求的 Source 回退为 provider 名称时，插件自动从持久化的 OAuth credential 文件名（authID）中恢复邮箱作为分组标识，避免仪表盘出现两个分组的重复统计
- 恢复逻辑仅在 provider 为 codex、认证方式为 OAuth、且 Source 已回退为 provider/executor 名称时才生效，不影响已有有效 Source 或 API Key 认证的请求

### 测试数据脱敏
- 将测试中疑似真实 Apple Hide My Email 地址替换为虚构的 `privaterelay.example.com` 域名
- 将测试中的 hex 标识符替换为明显虚构值

## v2.5.3 - 2026-07-25

### 模型价格查询优化
- 价格查询会在完整价格表中搜索，但每次下拉仅渲染前 100 项，超过时显示匹配总数并引导继续输入缩小范围
- 避免 models.dev 等大型价格目录一次创建大量下拉选项，保持查询、键盘导航与页面响应速度

### 界面与测试
- 价格设置的展开按钮改为 SVG 箭头，展开时提供更清晰的颜色状态反馈
- Dashboard 测试新增超过 100 条匹配结果的覆盖，并验证列表外的精确模型仍可查找

## v2.5.2 - 2026-07-25

### 模型价格查询与 provider/model 作用域定价
- 在模型价格面板新增只读的价格查询组件，支持按模型名或 `provider/model` 输入匹配，显示匹配模型的价格详情和来源（手动设置 / 价格源）
- 查询结果为下拉组合框，支持键盘导航（Arrow/Enter/Escape/Tab），单次限制显示 50 项并提示总数
- 手动价格支持 `provider/modelname` 作用域键，不同上游的同名模型可分别定价；裸模型名仍可作为所有上游的回退
- 价格设置候选列表来自实际用量中出现过的 `provider/model` 组合，也允许手动输入裸模型名或新组合
- 价格查询与设置面板整合在同一个默认收起的展开区，折叠状态在重渲染后保留
- 后端新增 provider 作用域价格查询逻辑，优先匹配 provider 作用域手动价格，其次裸模型回退，最后 price source 默认价格

### 界面与国际化
- 简体中文、繁体中文、英文和俄文界面同步增加价格查询、作用域设置、结果限制等 13 个 i18n key
- 查询信息中明确标注价格来源（手动设置 / 价格源）和匹配 key

### 测试
- 新增 Go 测试验证 provider 作用域手动价格优先于裸模型回退
- 新增 Dashboard 测试覆盖：价格查询与设置的拆分、搜索筛选与数量限制、键盘选择、provider 作用域保存、裸模型回退展示、空状态与 XSS 转义、details 元素结构与折叠状态保留、4 语言 i18n 完整性

## v2.5.1 - 2026-07-21

本版本丰富上游接口详情的最近请求信息，并保证响应拦截器补充的元数据在不同到达顺序和服务重启后仍能可靠恢复。

### 最近请求元数据与生成速度
- 最近请求新增推理强度、请求端点和生成速度三列，例如 `xhigh`、`/v1/responses` 和 `35.8 t/s`
- 流式请求使用 `output tokens / (总耗时 - TTFT)` 计算生成速度；非流式请求使用完整响应耗时，数据无效或缺失时显示 `-`
- 简体中文、繁体中文、英文和俄文界面同步增加对应列名

### 元数据协调与持久化
- 原生 CPA usage 与响应拦截器无论哪一方先到，都能合并 `Endpoint`、`Thinking` 和 `Stream` 元数据，避免重复记录
- 新增 `metadata_only` 持久化更新；冷重启时兼容更新行先于基础详情写入的乱序 JSONL，并在基础详情加载后回填
- 流式 chunk 在反序列化层固定标记为流式，同时兼容多种历史字段命名，保留旧数据读取能力

### 验证
- 增加 Go 与 Dashboard 回归测试，覆盖双向 enrichment、乱序持久化重放、流式标记、生成速度边界及多语言渲染
- 通过 Go race 测试、JavaScript 全量测试，并使用本地 CPA 验证非流式、流式和冷重启恢复路径

## v2.5.0 - 2026-07-19

### 客户端 API Key 联动筛选
- “API 详细统计”新增 API Key 筛选模式；默认保持全量展示，主动点击脱敏 key 后才应用筛选，并以高亮和“已选中”标识当前身份
- 选择客户端 API key 后联动筛选上游接口统计、上游接口详情、模型统计、请求事件明细和用量趋势；全局时间范围继续与该条件组合生效
- 点击请求次数、Token数量或总花费任一排序项，或再次点击当前 key，可取消筛选并恢复全量
- 后端为客户端 API 聚合返回不可逆 selector，摘要、事件、事件导出、后台导出任务和上游接口详情统一支持 `client_api` 参数
- selector、ETag 和查询缓存沿用现有脱敏值/hash 兼容分组规则，不传输原始 API key，并避免不同筛选条件串用缓存
- 增加 Go 与 Dashboard 回归测试，并使用本地 CPA 进行联调

## v2.4.9 - 2026-07-18

本版本在上游接口详情的“模型分布”中补充每个模型的 token 消耗，便于同时比较请求量、用量和估算花费。

### 模型分布 token 用量
- 每个模型名称后显示该模型聚合后的总 token 数，并使用 `k` / `M` 紧凑格式减少长数字占用空间
- token 数值与模型花费并列展示，不额外附加 `token` 单位文字，保持条形图信息简洁
- token 标注仅应用于模型分布，来源分布继续只显示请求量，避免改变来源统计的展示语义

### 界面与测试
- 新增弱化样式的行内 token 标签，与现有花费标签保持一致的换行和移动端表现
- 扩展模型分布渲染回归测试，确认 token 和花费同时显示且不会出现在来源分布中
- Go 全量测试与 Dashboard JavaScript 测试均通过

## v2.4.8 - 2026-07-18

本版本在上游接口详情的“模型分布”中为每个模型标注估算花费，便于直接比较同一上游接口下不同模型的成本构成。

### 模型分布花费
- 每个模型名称后显示按当前模型价格计算的美元花费，金额格式与页面顶部“总花费”和模型统计保持一致
- 单模型花费复用现有 provider 分项、手动价格优先和缓存读写计价逻辑，所有模型花费之和继续作为接口总花费
- 费用标注仅应用于模型分布，来源分布保持原有请求量展示，避免混淆不同统计维度

### 界面与测试
- 新增弱化样式的行内花费标签，长模型名称和移动端布局保持原有换行行为
- 增加模型花费计算与渲染回归测试，并确认来源分布不会误显示花费
- Go 全量测试与 Dashboard JavaScript 测试均通过

## v2.4.7 - 2026-07-18

本版本统一请求事件明细与 CSV 导出的输入 token 口径：输入列现在只显示非缓存输入，缓存命中和缓存创建继续单独展示。

### 请求事件输入口径
- OpenAI 兼容、Codex 等缓存已计入输入的上游，明细输入改为扣除缓存命中和缓存创建后的非缓存输入
- 原生 Claude/Anthropic 上游的输入本来不含缓存，保持原值，避免重复扣减
- 请求事件表格及后台 CSV 导出列统一命名为“非缓存输入”，避免将缓存 token 二次相加

### 缓存统计加固
- Go 与 Dashboard 分别集中独享缓存输入判断，供总 token、明细输入、费用与缓存命中率复用，避免口径漂移
- 空 provider 仅在 `total_tokens` 为正且与输入、输出、缓存总量吻合时推断为独享缓存，修正零总量边界

### 测试
- 增加 OpenAI 与 Claude 输入口径差异、空 provider 零总量和 CSV 导出内容的回归测试
- Go 测试与 Dashboard JavaScript 测试均通过

## v2.4.6 - 2026-07-16

本版本修复同一提供商下多个上游凭证在看板中无法从来源列区分的问题，并统一页面与导出的上游接口显示。

### 完整上游接口标识
- 请求事件和 API 详情查询为明细附加精确的上游接口分组键，例如 `codex · 上游 b374b8e7c98ca23c`、`claude · 上游 f85c45252fee`
- “请求事件明细”的来源列、上游接口详情的来源分布和最近请求均优先显示完整上游接口名，不再只显示 `codex` 或 `claude`
- 后台 CSV 导出的来源列使用相同的完整上游接口名，保持页面、本地导出和后台导出口径一致

### 兼容性与内部语义
- 保留原有 `source`、凭证和筛选语义；完整接口名通过查询结果的可选 `api` 字段提供，历史统计数据无需迁移
- Go 内部使用 `RequestDetail.UpstreamAPI` 明确表示看板上游接口分组键；该字段只填充查询结果副本，不写入持久化明细
- 删除仓库中误提交的本地可执行构建产物，并补充忽略规则，避免再次提交

### 测试
- 增加 Codex、Claude 完整上游接口传播、API 详情来源、请求事件渲染和 CSV 导出的回归测试
- Go 竞态测试、`go vet`、Dashboard 94 项前端测试和查询性能基准全部通过

## v2.4.5 - 2026-07-13

本版本优化 Dashboard 面板布局与 API 详情错误统计展示，改善宽面板空间利用和超长名称的显示体验。

### 面板布局
- “上游接口统计”和“模型统计”两个宽面板改为跨满整行（`panel full`），不再与其他面板并排
- 柱状图名称改用独立 `barLabel` 样式，支持超长名称（如 credential 名称）自动折行，并通过原生 `title` 属性提供 tooltip
- 柱状图数值独立右对齐（`barValue` + `max-content`），桌面端右对齐、移动端左对齐

### API 详情错误统计
- 无错误时不再渲染空“错误统计”区域，请求明细自动占满整行，避免浪费空间
- 错误统计与请求明细改为纵向堆叠（`detailActivityGrid` 单列），为长错误消息提供更宽的展示空间

### 测试
- 增加宽面板 `panel full`、超长 `barLabel`/`barValue` 名称、空错误区域和 `detailActivityGrid` 布局的回归测试

## v2.4.4 - 2026-07-13

本版本优化请求耗时的展示语义，明确区分完整请求用时和首字用时。

### 用时与首字展示
- “请求事件明细”和“API 最近请求”将原延迟列调整为“用时 / 首字”，在同一列显示完整请求用时与 TTFT
- TTFT 缺失或为 0 时显示 `-`，避免将缺失数据误解为零延迟
- 首页、API 统计和模型统计中的“延迟”显示文案调整为“用时”，不改变后端统计口径和导出字段
- 同步更新简体中文、繁体中文、英文和俄文界面文案

### 测试
- 增加用时/TTFT 组合格式、缺失 TTFT 和多语言文案回归测试

## v2.4.3 - 2026-07-11

### 用量去重与缓存统计
- 修复路由后模型名与请求模型别名不一致时，xAI 响应兜底记录无法和原生记录匹配、产生镜像上游与重复计数的问题
- 补充 OpenAI Responses / Chat Completions 嵌套缓存创建字段解析，缓存命中和缓存创建在摘要、费用与 CSV 导出中保持独立口径
- 持久化快照升级为 v2；加载 v1 快照时会将旧版 `CachedTokens` 合计值迁移为缓存读取值。迁移后保存的 v2 快照不兼容旧版插件，降级前应备份数据并保留旧快照
- 修正缓存 token 成本计算中新版 `CachedTokens` 语义导致的读取费用为 0 的问题

### 测试
- 增加路由模型名去重、嵌套缓存创建解析、CSV 缓存读写分离、v1 快照迁移回归测试和费用计算端到端验证

## v2.4.2 - 2026-07-11

本版本修复文件认证（file-backed auth）的 fallback 用量记录去重问题，确保 Claude、Kimi、xAI、Vertex、AI Studio、Antigravity、Gemini 等文件认证的兜底记录能正确与原生用量记录匹配去重，避免重复计数。

### 文件认证回退去重
- `providerFromAuthID` 扩展支持从文件名识别 provider（claude/kimi/xai/vertex/aistudio/antigravity/gemini），包括嵌套目录路径和旧版 Grok 命名
- `usageRecordFingerprint` 在 auth ID 无法推断 provider 时使用 `auth:<id>` 作为去重标识，确保自定义文件名认证也能正确去重
- `fallbackAuthType` 对已识别的文件认证统一返回 `"oauth"`，不再依赖 auth ID 的格式推断
- 新增 `authIDHasProviderPrefix` 辅助函数，统一 provider 前缀匹配逻辑

### 测试
- 增加 `TestFileAuthFallbackProviderAndFingerprintMatchNativeUsage` 参数化测试（9 个用例）
- 增加 `TestResponseInterceptFallbackDoesNotDoubleCountNativeXAIFileAuth` 端到端去重验证测试

## v2.4.1 - 2026-07-11

本版本完善请求事件明细中的缓存 token 展示，将缓存命中和缓存创建拆分为两个独立列。

### 请求事件明细
- 在“输入、输出、思考、缓存命中、总计”之间新增“缓存创建”列
- “缓存命中”仅显示缓存读取 token，“缓存创建”独立显示 `cache_write_tokens`
- 兼容两种数据口径：存在 `cache_tokens` 总量时扣除创建量；不存在总量时直接使用 `cached_tokens` 命中量
- 前端 CSV 导出的缓存命中与缓存创建列使用相同口径，避免展示与导出不一致

### 测试
- 增加缓存命中/创建拆分计算测试
- 增加请求事件表格列名和 `7 / 2 / 15` 展示回归测试

## v2.4.0 - 2026-07-11

本版本将插件 ID 从 `usage-statistics` 更改为 `usage-dashboard-zduu`，解决与 CPA 官方商店同 ID 插件的命名空间冲突。因为 ID、商店资产名、配置节点和管理路由均发生变化，本版本按破坏性迁移版本发布。

### 插件 ID 迁移
- 插件运行 ID、商店注册表 ID 和版本化动态库文件名统一改为 `usage-dashboard-zduu`
- 管理 API 和资源路径迁移到 `/plugins/usage-dashboard-zduu/*`
- CPA 配置节点迁移到 `plugins.configs.usage-dashboard-zduu`
- 配置解析保留对旧 `usage-statistics` 节点的兼容读取，便于迁移过程中复用原有业务参数
- 持久化统计目录、旧 JSONL 文件和模型价格文件路径保持不变，历史数据无需转换

### 商店与发布资产
- GitHub Actions、Release 动态库、商店 zip 和校验文件统一使用 `usage-dashboard-zduu` 前缀
- 更新插件商店注册表和部署文档，补充 2.3.4 到 2.4.0 的安全迁移顺序
- 明确禁止新旧插件同时启用，避免重复计数或同时写入相同持久化文件

### 升级原因
- CPA 官方商店存在另一个 `usage-statistics` 插件；CPA 按插件 ID 而非来源区分安装状态与运行时配置
- 同 ID 会导致两个商店条目同时显示为已安装，并争用配置节点、管理路由和动态库选择
- 更换为带作者标识的唯一 ID 后，本插件可以独立安装、更新、降级和卸载

## v2.3.4 - 2026-07-11

本版本修复缓存价格显示和模型价格表单布局问题，作为旧插件 ID `usage-statistics` 的最后一个兼容维护版本。

### 缓存价格修复
- models.dev 未提供缓存创建价格时不再自动按输入价格的 1.25 倍推算，未知价格默认使用 0
- 显式缓存创建价格（包括免费价格 0）保持原值，缓存命中与缓存创建继续独立计费
- Dashboard 用户界面统一使用“缓存命中”和“缓存创建”术语，避免暴露内部字段名称

### Dashboard 布局修复
- 模型价格设置面板改为占满整行，长标签不再超出面板
- 价格输入框限制在网格列内，修复输入、输出、缓存命中和缓存创建价格输入框互相重叠
- 增加三列、两列和单列响应式断点，适配管理中心 iframe 与窄屏窗口

## v2.3.3 - 2026-07-10

本版本新增缓存写入 token 独立计费支持，为 GPT-5.6 等区分 cache read/write 计费的新模型做好准备。

### 缓存成本统计
- 新增 `CacheWriteTokens` 字段，贯通 `TokenStats`、`detailTotals`、`TimeSeriesTokenStat` 全链路
- 计费公式：`(input-cache_read-cache_write)×Prompt + output×Completion + cache_read×Cache + cache_write×CacheWrite`
- 兼容 Claude 家族 input 不含缓存（独占缓存输入）、其他上游 input 已包含缓存的两种记账口径
- 模型价格新增 `cache_write` 字段；models.dev 有 `cache_write` 时使用，缺失或未知时默认使用 0
- 缓存写入统计贯通摘要、范围查询、持久化快照、provider 聚合、Dashboard 和 CSV 导出
- 模型价格表单支持缓存写入价格编辑与展示

### 代码质量
- `ModelSnapshot.Providers` 支持从快照恢复 provider 维度的 token 统计，避免旧快照重放时丢失
- 新增 `modelProviderStatsFromSnapshot`、`residualModelProviderStats` 处理快照恢复时的 provider 残差

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
