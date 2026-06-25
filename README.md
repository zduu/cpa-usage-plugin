# CPA Usage Statistics Plugin

CPA 用量统计插件，用于在 CLIProxyAPI/CPA v7 插件系统中记录请求用量，并提供管理页面查看统计数据。

## 功能

- 记录请求数、成功/失败、延迟、TTFT。
- 记录 input/output/reasoning/cache/total token。
- 按接口、模型、凭证/来源聚合统计。
- 提供 API 详情、错误统计、最近请求明细。
- 服务健康网格按 15 分钟展示最近 7 天状态，鼠标悬停显示窗口信息。
- 支持导入/导出统计数据。
- 支持浏览器本地配置模型价格并估算成本。

## 构建

本仓库使用 GitHub Actions 构建 Linux x86_64 插件。

1. 推送到 `main` / `master` 或手动运行 `Build Plugin` workflow。
2. 在 Actions 运行结果中下载 `usage-statistics-plugin` artifact。
3. artifact 内的文件为 `usage-statistics.so`。


## 目录

```text
cpa-usage-plugin/
├── .github/workflows/build.yml
├── CPA_USAGE.md
├── OPTIMIZATION_RECOMMENDATIONS.md
├── README.md
└── go/
    ├── go.mod
    └── main.go
```

## 管理接口

插件注册以下管理接口：

```text
GET  /v0/management/plugins/usage-statistics/usage
GET  /v0/management/plugins/usage-statistics/usage/export
POST /v0/management/plugins/usage-statistics/usage/import
```

浏览器资源入口由插件注册到 CPA 管理端，菜单名为“用量统计”。

## 使用说明

安装和启用步骤见 [CPA_USAGE.md](CPA_USAGE.md)。
