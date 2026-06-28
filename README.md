# CPA Usage Statistics Plugin

CPA 用量统计插件，用于在 CLIProxyAPI/CPA v7 插件系统中记录请求用量，并提供管理页面查看统计数据。

当前代码版本：`1.2.18`。

## 功能

- 记录请求数、成功/失败、延迟、TTFT。
- 记录 input/output/reasoning/cache/total token。
- 按上游接口、模型、来源、CPA 凭证和调用 CPA 的客户端 API key 聚合统计。
- 轻量级首屏摘要：看板数据不含请求明细，首包体积不随记录数增长。
- 请求事件明细支持按模型、来源、凭证、时间范围筛选，页面以滚动表格展示。
- 服务健康网格按 15 分钟展示最近 7 天状态，鼠标悬停显示窗口信息。
- 支持导入/导出统计数据，导出包含版本、插件版本、明细数和配置摘要；导入返回输入/接收/拒绝/新增/跳过/过期忽略明细。
- API key 只保存脱敏显示值和分组 hash；导入不同实例导出的同一脱敏 API key 时会合并到同一客户端 API 统计。
- 支持后端全局模型价格表并估算成本，跨设备打开看板可见同一份最新价格。
- 默认使用内存统计；可通过 `storage_enabled` + `storage_path` 开启后台队列 JSONL 持久化，配合周期 snapshot、旧分片清理和可选 fsync 在重启或更新插件后恢复保留窗口内的统计。
- 运行时元数据：页面可见当前保留策略、存储明细数、淘汰数、最近导入结果。
- 健康检查端点 `/health` 可查看插件运行状态、顶层 `alerts` 告警、持久化状态、后台 writer 批次/滑动平均/p95/p99/压力指标、看板查询/缓存指标和事件导出压力指标。
- 事件导出支持按筛选条件输出 JSON、CSV、JSONL，可通过 `gzip=1` 压缩响应，并用 `export_max_records`/`limit` 控制超大导出的返回行数。

## 构建

本仓库使用 GitHub Actions 构建 Linux、macOS 和 Windows 插件。

1. 推送到 `main` / `master` 或手动运行 `Build Plugin` workflow。
2. CI 自动运行 Go 测试 (`go test -v -race ./...`) 和 JS 测试 (`node --test`)。
3. 在 Actions 运行结果中下载对应架构 artifact，例如 `usage-statistics-plugin-linux-amd64`。
4. Release 中会上传按平台命名的资产，例如 `usage-statistics-linux-amd64.so`、`usage-statistics-darwin-arm64.dylib`、`usage-statistics-windows-amd64.dll`，并保留 `usage-statistics.so` 作为 `linux-amd64` 兼容别名。

本地构建（需要 Go 1.26+ 和 CGO）：

```bash
cd go
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildmode=c-shared -buildvcs=false -o ../usage-statistics.so .
```

本地交叉构建 arm64 需要安装对应 C 交叉编译器，例如 `aarch64-linux-gnu-gcc`：

```bash
cd go
CC=aarch64-linux-gnu-gcc CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -buildmode=c-shared -buildvcs=false -o ../usage-statistics-linux-arm64.so .
```

本地测试：

```bash
cd go && go test -v -race ./...
node --check go/dashboard/helpers.js go/dashboard/script.js
node --test go/dashboard/*.test.js
```

## 目录

```text
cpa-usage-plugin/
├── .github/workflows/build.yml
├── CPA_USAGE.md
├── README.md
├── scripts/update-latest-release.sh
└── go/
    ├── go.mod
    ├── main.go              # CGO 入口 + 分发器 + 信封工具
    ├── types.go             # 全部数据结构
    ├── stats.go             # 统计引擎 + 摘要 + limit/offset 事件查询
    ├── source.go            # 密钥脱敏 / 来源清洗 / 响应头过滤
    ├── register.go          # 注册 / 配置 / YAML 解析
    ├── management.go        # 管理接口路由 + 处理器
    ├── dashboard.go         # go:embed 前端嵌入 + 摘要/事件 API
    ├── main_test.go         # 原有测试
    ├── dashboard_test.go    # 新 API 测试
    └── dashboard/
        ├── index.html       # 纯 HTML 结构
        ├── style.css        # 纯 CSS
        ├── helpers.js       # 可单测的纯函数
        ├── helpers.test.js  # JS 单元测试
        └── script.js        # JS 主逻辑
```

## 管理接口

插件注册以下管理接口：

```text
GET  /v0/management/plugins/usage-statistics/usage
GET  /v0/management/plugins/usage-statistics/usage/export
POST /v0/management/plugins/usage-statistics/usage/import
GET  /v0/management/plugins/usage-statistics/model-prices
PUT  /v0/management/plugins/usage-statistics/model-prices
DELETE /v0/management/plugins/usage-statistics/model-prices
GET  /v0/management/plugins/usage-statistics/dashboard-summary
GET  /v0/management/plugins/usage-statistics/dashboard-events
GET  /v0/management/plugins/usage-statistics/dashboard-events-export
GET  /v0/management/plugins/usage-statistics/dashboard-api-detail
GET  /v0/management/plugins/usage-statistics/dashboard-data
GET  /v0/management/plugins/usage-statistics/health
```

### 接口说明

| 端点 | 方法 | 说明 |
|------|------|------|
| `/usage` | GET | 获取完整统计数据（含明细）。 |
| `/usage/export` | GET | 导出全量统计数据（JSON），包含 `version`、`plugin`、`detail_count`、`config` 和 `usage`。 |
| `/usage/import` | POST | 导入统计数据，返回 `input_records`/`accepted_records`/`rejected_records`/`added`/`skipped`/`ignored_by_retention`。 |
| `/model-prices` | GET/PUT/DELETE | 获取、新增/更新、删除全局模型价格表。 |
| `/dashboard-summary` | GET | **推荐** — 轻量看板摘要，不含请求明细，含预计算健康网格/来源/客户端 API/模型聚合和 `_meta` 元数据。 |
| `/dashboard-events` | GET | 事件查询，支持 `?limit=50&offset=0&range=24h&model=gpt-4&source=xxx&auth=xxx&api=xxx`。 |
| `/dashboard-events-export` | GET | 按筛选条件导出事件，默认 JSON；支持 `format=csv|jsonl`、`gzip=1` 和 `limit`，默认受 `export_max_records` 保护。 |
| `/dashboard-api-detail` | GET | 单个上游接口详情，支持 `?api=xxx&range=24h&model=gpt-4&source=xxx&auth=xxx`，返回模型分布、错误统计和最近请求。 |
| `/dashboard-data` | GET | 兼容旧版，返回含全部 `details` 数组的完整数据。 |
| `/health` | GET | 运行健康状态：`status`、`alerts`、`detail_count`、`evicted_total`、`total_requests`。 |

`/dashboard-summary`、`/dashboard-events`、`/dashboard-api-detail` 和 `/dashboard-events-export` 支持弱 ETag；内置看板轮询会自动使用 `If-None-Match`，外部脚本也可用条件请求减少未变化数据的重复传输。`/health.runtime.conditional_requests` 会按端点统计条件请求的 304 命中率。

浏览器资源入口由插件注册到 CPA 管理端，菜单名为"用量统计"。

## 使用说明

安装和启用步骤见 [CPA_USAGE.md](CPA_USAGE.md)。
