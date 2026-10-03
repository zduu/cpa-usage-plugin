# 凭证额度周期（CPA v8）

此功能位于上游接口详情的「额度周期」区域，适用于 Claude、Codex、Devin 的认证文件凭证。只有取得真实上游窗口时才显示卡片；仅有周额度的凭证不会出现 5h 占位。

本功能尚未发布，插件版本号暂保持 `2.6.6`。验收宿主为 CPA v8；宿主的插件管理接口仍使用 `/v0/management/plugins/usage-dashboard-zduu`，原生凭证列表使用 `/v8/management/credentials`。

## 页面用法

1. 打开用量统计看板，选择一个认证文件凭证对应的上游接口。
2. 在额度周期中查看本期实际请求、token、按模型价格计算的实际花费、上游已用比例、推算总额度及预计剩余。倒计时每秒更新，周期结束后刷新详情。
3. 点击「上一个完整周期」查看相邻的真实历史窗口及其模型分布。上期保留最后取得的比例，不自动补成 100%，不展示推算总额度。

额度按凭证统计所有客户端的请求，不受页面时间范围和客户端 API key 筛选截断。同一凭证的 5h 与周窗口可能包含相同请求，两者不能相加作为总消耗。普通详情和事件表仍按所选筛选条件展示。

两期的模型表分别显示请求、成功/失败、输入、输出、推理、缓存读写、总 token、实际花费和费用占比。页面沿用 USD/CNY 显示切换，接口和备份以 USD 计价。没有价格、没有记录、不能推算时显示 `—`；显式设置的零价格显示 0。

## 信号来源

| 提供商 | 信号 | 周期 |
| --- | --- | --- |
| Claude | `Anthropic-Ratelimit-Unified-5h/7d-Utilization` 与 `Reset` | 真实提供的 5h、7d |
| Codex | `X-Codex-…-Primary/Secondary-Used-Percent`、`Window-Minutes`、`Reset-At` 或 `Reset-After-Seconds` | 按上游实际分钟数确定，包括只有周窗口的情形 |
| Devin | `daily/weekly_quota_remaining_percent` 与 `daily/weekly_quota_reset_at` | 真实提供的日、周窗口 |

Claude/Codex 优先读取原生 usage 的响应头，不依赖 `log_response_headers`。WebSocket 配额使用 CPA 已转换出的响应头。Codex 附加额度单独分组；未提供可靠模型范围时只显示比例和重置时间，费用、模型统计与推算保持为空。不会通过名称包含某个单词来推断模型范围，也不会根据套餐结束日期生成月额度。

看板复用管理鉴权读取 v8 凭证列表，并提交白名单中的观测字段。原始观测时间会保留，同一份观测不重复提交。可见页面沿用 30 秒轮询，隐藏页面沿用 300 秒轮询；读取失败保留已有历史。浏览器关闭期间，原生 usage 继续入账，但仅存在于宿主凭证列表中的新观测要等看板再次打开后才能保存。

缺少某个响应头不表示撤销窗口。明确传递为 `null` 的使用率信号会隐藏对应窗口并保留历史；如果宿主把上游 null 丢弃、只传递其余字段，插件不会猜测撤销。没有后续观测时，到期的本期转为上期；超过该窗口的下一周期末后释放窗口，不虚构新的本期。

## 金额与周期边界

实际用量按请求开始时间分配到 `[周期开始, 周期结束)`。跨周期完成的请求和迟到回调不会改变所属周期。相对重置时间有小幅漂移时，两期按新的观测对齐共用边界，避免同一请求在两期重复计数。事实保留完成时间，推算只使用在观测截止点已经完成的请求成本。

从周期起点连续采集时，推算采用 `匹配成本 / 已用比例`。中途开始采集或重启后，使用同一周期中本次运行取得的两个有效观测，计算 `成本增量 / 比例增量`。例如新增 $5、比例从 30% 到 40%，推算总额度为 $50，预计剩余为 $30。

比例下降后重新建立基线。每期最多保留 64 条观测，截断时保留当前运行的首个观测和最近一次下降的前后样本，持续请求和备份保存不会丢失校准基线。观测超过窗口时长的四分之一、分母不为正、价格未知或无法配对时不推算。模型价格、provider/model 覆盖及按请求时间生效的时段价格均复用现有后端计价逻辑；改价后本期、上期和模型金额一起重新计算。历史记录缺少原始时间戳时，额度金额和推算与主账一样使用基础价，不套用补齐时间对应的时段价格；该标记随额度事实保存，旧备份可从仍保留的主账记录补齐。

## 保存、导出和升级

无需新增配置。`storage_enabled: true` 时，周期事实和观测使用现有 JSONL writer、flush、snapshot 与 fsync 策略；关闭时使用内存模式。

周期所需的精简事实独立保留，因此事件明细上限、分页或较短的普通保留期不会删除仍被本期/上期使用的记录。不能找回启用此功能前已经删除的历史；内存和磁盘开销随保留的真实请求数增长。

主明细全部过期或被筛选隐藏后，仍有额度周期的接口继续出现在详情选择列表中；普通统计保持当前筛选结果，周期统计按凭证展示。额度周期使用的 Claude 历史缓存事实在导入时同样遵循 `claude_cache_repair_enabled`，修复后的值随备份与存储保存。

完整备份的外层格式仍为 version 1，`usage.quota_cycles.version` 为 1。完整导出、后台分块备份、快照和导入均包含周期数据；筛选事件导出仍只导出事件。重复导入按稳定记录 ID 去重，两个内容相同但 ID 不同的真实请求分别保留。冲突的记录或周期扩展会在写入前拒绝。

存储快照版本升为 3，可读取旧版快照。升级前备份原数据目录；回退时同时恢复旧插件和升级前的数据目录。旧插件不能直接读取 v3 快照。现有实验性 SQLite 迁移工具会拒绝 v3 快照，避免丢弃周期数据；运行时持久化仍使用 JSONL。

macOS 构建会将 Go 动态库映像保留到宿主进程退出，避免宿主卸载动态库时仍存活的 Go 运行时线程访问已释放代码。停用时统计和导出 worker 正常关闭，重新启用时重建服务并从存储恢复数据。macOS 更新或覆盖插件文件后应重启 CPA，使新映像生效并释放旧映像。

## 管理 API

`GET /v0/management/plugins/usage-dashboard-zduu/dashboard-api-detail?api=…` 增加可选 `credential_quota_cycles`：按凭证返回 `groups`，每组含 `window_seconds`、`current`、`previous`。周期包含 `start_at`、`end_at`、`observed_at`、`used_percent`、`summary` 和 `model_stats`；只有 `current` 含 `estimated_total_usd`、`estimated_remaining_usd`。

`POST /v0/management/plugins/usage-dashboard-zduu/dashboard-quota-observations` 接收：

```json
{
  "version": 1,
  "observations": [{
    "provider": "devin",
    "auth_index": "credential-index",
    "auth_id": "devin-account.json",
    "observed_at": "2026-10-02T08:00:00Z",
    "signals": {
      "weekly_quota_remaining_percent": "60%",
      "weekly_quota_reset_at": "2026-10-05T08:00:00Z"
    }
  }]
}
```

响应为 `accepted`、`skipped`、`rejected`、`quota_version`，计数单位是观测条目，不是请求。请求体最多 256 KiB，每批 100 条，每条最多 64 个信号、单值最多 512 字符。后端通过 `host.auth.get_runtime` 核对实时凭证身份，拒绝 runtime-only 条目。该接口需要管理鉴权，匿名 resource 别名返回 404；提交方具有管理权限，观测值本身不属于宿主签名数据。

## 本地验收

先按根目录 README 构建本机共享库，再使用独立临时配置、凭证和数据目录验证：

```sh
python3 scripts/validate-quota-v8.py --cpa /path/to/CLIProxyAPI \
  --plugin dist/usage-dashboard-zduu-darwin-arm64.dylib
```

加 `--browser` 运行真实浏览器验收，`--restarts 5` 连续验证五次重启。需要安装 Playwright 及 Chromium；也可用 `CPA_PLAYWRIGHT=/path/to/node_modules/playwright` 和 `CPA_CHROME=/path/to/chrome` 指定已有安装。脚本验证原生 usage、凭证采集、两期模型与金额、管理鉴权、重启恢复及同进程重新启用后的完整备份，结果和截图写入 `docs/validation/quota-cycle-details-v8/`。
