# CPA Usage Statistics Plugin

CPA 用量统计插件，用于在 CLIProxyAPI/CPA v7 插件系统中记录请求用量，并提供管理页面查看统计数据。

## 功能

- 记录请求数、成功/失败、延迟、TTFT。
- 记录 input/output/reasoning/cache/total token。
- 按接口、模型、凭证/来源聚合统计。
- 轻量级首屏摘要：看板数据不含请求明细，首包体积不随记录数增长。
- 请求事件明细支持按模型、来源、凭证、时间范围筛选，页面以滚动表格展示。
- 服务健康网格按 15 分钟展示最近 7 天状态，鼠标悬停显示窗口信息。
- 支持导入/导出统计数据，导入后返回新增/跳过/过期忽略明细。
- 支持浏览器本地配置模型价格并估算成本。
- 运行时元数据：页面可见当前保留策略、存储明细数、淘汰数、最近导入结果。
- 健康检查端点 `/health` 可查看插件运行状态。

## 构建

本仓库使用 GitHub Actions 构建 Linux x86_64 插件。

1. 推送到 `main` / `master` 或手动运行 `Build Plugin` workflow。
2. CI 自动运行 Go 测试 (`go test -v -race ./...`) 和 JS 测试 (`node --test`)。
3. 在 Actions 运行结果中下载 `usage-statistics-plugin` artifact。
4. artifact 内的文件为 `usage-statistics.so`。

本地构建（需要 Go 1.26+ 和 CGO）：

```bash
cd go
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildmode=c-shared -buildvcs=false -o ../usage-statistics.so .
```

本地测试：

```bash
cd go && go test -v -race ./...
node --check go/dashboard/helpers.js go/dashboard/script.js
node --test go/dashboard/helpers.test.js
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
GET  /v0/management/plugins/usage-statistics/dashboard-summary
GET  /v0/management/plugins/usage-statistics/dashboard-events
GET  /v0/management/plugins/usage-statistics/health
```

### 接口说明

| 端点 | 方法 | 说明 |
|------|------|------|
| `/usage` | GET | 获取完整统计数据（含明细）。 |
| `/usage/export` | GET | 导出全量统计数据（JSON）。 |
| `/usage/import` | POST | 导入统计数据，返回 `added`/`skipped`/`ignored_by_retention`。 |
| `/dashboard-summary` | GET | **推荐** — 轻量看板摘要，不含请求明细，含预计算健康网格/来源/模型聚合和 `_meta` 元数据。 |
| `/dashboard-events` | GET | 事件查询，支持 `?limit=50&offset=0&range=24h&model=gpt-4&source=xxx&auth=xxx&api=xxx`。 |
| `/dashboard-data` | GET | 兼容旧版，返回含全部 `details` 数组的完整数据。 |
| `/health` | GET | 运行健康状态：`detail_count`、`evicted_total`、`total_requests`。 |

浏览器资源入口由插件注册到 CPA 管理端，菜单名为"用量统计"。

## 使用说明

安装和启用步骤见 [CPA_USAGE.md](CPA_USAGE.md)。
