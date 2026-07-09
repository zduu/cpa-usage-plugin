# deepseek-v4 记录缺失问题审查与 v2.3.0 修复总结

- 审查日期：2026-07-09 ~ 2026-07-10
- 涉及版本：插件 v2.2.7 → v2.2.9（审查对象），v2.3.0（本次修复）
- CPA 版本：本地 `/Users/zhouzhou/Downloads/edit/CLIProxyAPI`，v7.2.54（问题始于 v7.2.49 → v7.2.53 升级后）

## 一、问题回顾

用户现象：本地 Claude Code CLI 通过 CPA 的 OpenAI 兼容渠道（`opencode-go`，提供 `deepseek-v4-flash` / `deepseek-v4-pro` 等模型）发起请求，模型正常响应，但用量看板没有对应的上游和调用记录；同一 Claude Code CLI 调用 Codex OAuth 渠道的 `gpt-5.5` 则记录正常。问题出现在 CPA 从 v7.2.49 升级到 v7.2.53 之后。

v2.2.8 / v2.2.9 两次提交（`71576df`、`900866d`、`07faa63`）为此新增了 `response_interceptor` / `response_stream_interceptor` 兜底统计与去重机制。本次审查的目标是确认这两次提交是否完全改正问题。

## 二、审查方法与证据

1. 通读插件 `response_intercept.go` 全部逻辑与两次提交的 diff。
2. 对照本地 CPA v7.2.54 源码逐层核实插件挂钩机制：
   - `usage.handle` 分发链：`UsageReporter.publishWithOutcome`（`once.Do` 保证每请求一条）→ `usage.Manager` 异步队列 → pluginhost `usageAdapter` → 插件（无过滤，全量透传）。
   - 响应拦截链：`sdk/api/handlers/handlers.go` 在响应翻译为客户端协议之后调用 `response.intercept_after` / `response.intercept_stream_chunk`，`Body` 为翻译后的客户端协议载荷，`Metadata` 含 conductor 写入的 `selected_auth_id`。
   - v7.2.49→v7.2.53 的 usage 相关变更只有 `dea47879`（`StreamUsageBuffer`：OpenAI 兼容执行器从「每条 usage 行立即发布」改为「流结束发布最后一条」，配合 `defer` 与 `EnsurePublished`）。
   - 各翻译器 usage 口径：`openai→claude` 与 `codex→claude` 均把 cached 从 input 中扣出放入 `cache_read_input_tokens`；`claude→openai` 把 cache 折进 `prompt_tokens`；`gemini→claude` 直接透传 `promptTokenCount`（cache 全含、无 cache 字段）；CPA 原生 Claude 解析（`parseClaudeUsageNode`）input 不含 cache、total 计入 cache。
3. 用本地真实持久化数据验证（`CLIProxyAPI/data/usage-statistics/usage-2026-07-09.jsonl`，5642 条）：以 `latency_ms/ttft_ms > 0` 区分原生记录与兜底记录（兜底没有耗时上下文）。

## 三、审查结论

### 3.1 根因确认

1. **CPA 侧（触发条件）**：v7.2.53/54 下 OpenAI 兼容渠道的原生 `usage.handle` 确实**间歇性缺失**。实测同一渠道（opencode-go）同一时段内，部分请求有原生记录、部分请求只有模型响应而无任何原生记录（如 14:19–15:09 多条只有兜底记录補位、且时间点不靠近插件重载）。从源码看 `ExecuteStream` 的发布链在正常路径是完备的，缺失最可能与请求提前取消（客户端中断时 `ctx.Done` 早退路径不经过 `EnsurePublished`）或上游异常关闭有关；这属于 CPA 侧行为，插件无法从根上修复，只能兜底。
2. **插件侧（旧版缺陷）**：v2.2.7 无兜底能力，原生缺失即无记录 —— 这就是用户看到的「deepseek-v4 不显示记录」。`gpt-5.5` 走 Codex 执行器（usage 内嵌于 `response.completed` 事件，发布时机不同），未受影响。

### 3.2 v2.2.8 / v2.2.9 的修复是否完全

**方向正确、主路径已验证有效，但有 5 处缺口**：

| # | 缺口 | 后果 | 实证 |
|---|---|---|---|
| 1 | 兜底 provider 链把推测性 `upstream_provider` 置于 `selected_auth_id` 之前，且 2.2.8 开发早期版本完全没有 auth id 识别 | deepseek 兜底记录被分组为 `claude · 上游 f85c45252fee`，与原生 `openai-compatible-opencode-go` 分裂；家族不同导致去重失效、同请求双计 | JSONL 中 22 条错误分组记录；13:48:59 原生与 13:49:01 兜底 token 完全相同（85/8/93）双计实锤 |
| 2 | 去重指纹含 `reasoning_effort`/`service_tier`，两侧来源不同（原生取自翻译后载荷、兜底取自拦截 metadata） | 任一值不一致即去重失效 → 双计 | 代码审查确认来源不对称 |
| 3 | `normalizeAnthropicUsageDetail` 对**所有** Claude 形态响应把 cache 折回 input，但真实 Anthropic 上游的原生解析 input 不含 cache | Claude 家族上游带缓存请求指纹错位 → 双计（Claude Code 恒用 prompt cache，命中面大） | 对照 CPA `parseClaudeUsageNode` 确认口径相反 |
| 4 | 流式兜底解析 `message.usage`（`message_start` 预生成快照） | Claude 协议流多调度一条永不匹配的幽灵兜底 | 代码路径确认 |
| 5 | 兜底 `auth_index` 用 auth id 尾段合成，与原生 `EnsureIndex`（服务端 sha256）不同 | 凭证维度同一凭证分裂为两行 | JSONL：原生 `5312415661d8a481` vs 兜底 `f85c45252fee` |

另有两处小缺口：全缓存命中（input=0）时 cache 不回补的边界；每 chunk 携带累计 usage 的上游会重复调度兜底（Codex 双计的一般化形态，2.2.9 只修了 provider 家族这一例）。

**同时确认了设计中已经做对的部分**：2.2.8 最终版部署后（15:14 之后)的实测数据显示，原生存在时兜底被正确去重（无兜底残留），原生缺失时兜底正确补位（token 数与上游一致），`opencode-go` 的 deepseek-v4 记录恢复正常 —— 用户主诉场景在 2.2.9 已基本修复，上述缺口是次生风险（特定上游/协议组合下双计或分组分裂）。

## 四、v2.3.0 修复内容

全部缺口在 `go/response_intercept.go`、`go/stats.go`、`go/management.go` 中修复：

1. **provider 链重排**：`providerFromSelectedAuthID`（CPA 真实下发）→ 推测性 metadata 键 → SourceFormat 回退。
2. **指纹规范化**：input/total 统一为「缓存全含 + 重算」口径（Claude 家族补入 `cache_read+cache_creation`，其余家族 prompt 原语义）；移除 `reasoning_effort`/`service_tier`。经推演覆盖四种组合：OpenAI 系上游 × Claude/OpenAI 客户端、Claude 上游 × Claude/OpenAI 客户端全部对齐。
3. **口径分家**：`normalizeAnthropicUsageDetail` 按上游家族决定显示口径 —— Claude 家族保持 input 不含 cache、total 计入 cache（与原生一致）；其余家族折回 input，并去掉 `InputTokens > 0` 前置条件修复全缓存边界。
4. **流式排除 `message.usage`**：新增 `usageDetailStreamPaths`，与 CPA 原生 `ParseClaudeStreamUsage`（只读顶层 `usage`）对称。
5. **HistoryChunks supersede**：同流较新 usage chunk 的指纹替代此前调度的 pending，running-usage 上游只提交最终快照；并发同参请求经推演计数守恒。
6. **auth index 学习**：从原生记录（`handleUsage`）与启动恢复明细（`normalizeStorageSnapshotDetail`）学习 auth id → CPA auth index 映射（上限 4096 条），兜底构建与提交前两次查询,凭证维度不再分裂。
7. **嵌套 usage 字段解析**：补 `prompt_tokens_details.cached_tokens` 等四个嵌套路径，兜底记录缓存/推理 token 与原生对齐。

### 测试

- 更新 2 个旧测试（其一原本以自相矛盾的 metadata 锁定了错误分组行为，改为断言正确分组并锁定修复）。
- 新增 5 个测试：deepseek-via-Claude-Code 回归、真实 Claude 上游口径、message_start 幽灵兜底、running-usage supersede、auth index 学习。
- `go test -race ./...` 全绿（Go 全部用例），`node --test` 84 例全绿，`go vet` 干净。

## 五、v2.3.0 发布评估

**建议发布 minor 版本 v2.3.0**，理由：

- 去重指纹语义整体变更（跨版本内存态不兼容属可接受范围：指纹只在进程内存活）。
- 新增两类机制（HistoryChunks supersede、auth index 学习）与一类口径策略（按上游家族分家），属功能级演进而非补丁。
- Claude 家族上游的兜底记录显示口径变化（input/total 与原生对齐），对用户可见。

发布前检查单：

1. `docs/releases/changelog.md` 已有 `## v2.3.0` 小节 ✅
2. `go/register.go` `pluginVersion = "2.3.0"` ✅
3. README / docs 版本号同步 ✅
4. 打 tag `v2.3.0` 前建议先本地构建 dylib 部署实测一轮 deepseek-v4 + gpt-5.5 混合流量，确认看板无双计、无 `claude · 上游` 新增分组、凭证维度单行。

## 六、遗留与建议

1. **CPA 侧根因**（原生 usage 间歇缺失）建议向上游 CLIProxyAPI 反馈：`openai_compat_executor.ExecuteStream` 在 `ctx.Done` 早退且尚未观察到 usage 行时既不 `Publish` 也不 `EnsurePublished`（后者不在 defer 中），取消的请求会完全丢失记录。
2. 旧错误分组（`claude · 上游 xxx`）历史数据随 30 天保留窗口自然过期，不做自动迁移。
3. Claude 上游 × OpenAI 客户端 × `cache_creation>0` 的组合中,翻译器把 cache_creation 无痕折进 prompt_tokens,兜底无法拆分;指纹已通过规范口径对齐,但兜底记录的缓存明细在该组合下会缺 cache_creation 细分(计数与 total 正确)。
4. 插件无法自行复现 CPA `EnsureIndex`(依赖服务端文件路径/密钥),学习机制冷启动时(重启后首个请求即走兜底且无历史可学)凭证仍会短暂用合成 index,首条原生记录到达后自愈。
