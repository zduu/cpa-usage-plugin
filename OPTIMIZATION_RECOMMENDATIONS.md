# 新优化建议

本文档替代旧的 `OPTIMIZATION_PLAN.md`。旧计划中的热路径增量统计、普通写入避免排序、响应头默认不保存、错误体脱敏、导入校验、嵌套 YAML 配置解析、来源分组修正、并发/快照/导入测试补齐，以及 dashboard 轮询退避已完成或已有覆盖。

## P0：降低 dashboard 首包体积

当前 `dashboard-data` 仍返回完整 `Snapshot()`，管理页再在浏览器端聚合、筛选和渲染。保留明细达到数万到十万条时，首包体积、JSON 解析、前端聚合都会变重。

建议：

- 新增轻量 dashboard 接口，只返回总览、健康网格、上游接口聚合、模型聚合、来源聚合。
- 请求事件明细拆成分页接口，支持 `limit`、`offset`、`range`、`model`、`source`、`auth`。
- 当前导出接口保留全量导出能力，必要时增加筛选导出参数。

验收：

- 默认 dashboard 首包不携带全部请求明细。
- 10 万条 retained details 下管理页首屏仍能快速打开。
- 事件明细可分页查看，导出行为保持可用。

## P1：拆分 dashboard 前端资源

当前 HTML/CSS/JS 仍内联在 Go 字符串中，改 UI 时容易影响管理接口和统计引擎代码，测试也只能做字符串级断言。

建议：

- 将 dashboard HTML/CSS/JS 拆到独立文件，构建时通过 `go:embed` 打包。
- 将 JS helper 抽出为可单测模块，覆盖来源脱敏、接口选择、健康网格排布、导出筛选等逻辑。
- CI 中加入 `node --check`，必要时加轻量前端单测。

验收：

- `go test ./...` 和 `node --check` 均通过。
- 修改 dashboard UI 不需要编辑统计引擎主逻辑。
- 健康网格 7 行、上游接口选择、失败退避都有自动化覆盖。

## P1：运行时可观测性

插件当前主要依赖 CPA 日志确认加载状态，对统计存储、导入结果、淘汰行为和 dashboard 请求耗时缺少可观测指标。

建议：

- 在管理数据响应中返回轻量 metadata：retention、max details、当前 retained detail 数、最近一次导入结果。
- 对导入、淘汰、配置变更增加结构化日志或管理端可见状态。
- 暴露只读健康摘要，便于排查“页面无数据”和“插件未记录”的区别。

验收：

- 管理页能看到当前统计保留策略和已保留明细数量。
- 导入后能明确看到新增、跳过、因保留策略忽略的数量。
- 配置不生效时有可排查的状态信息。

## P2：统计引擎文件拆分

`go/main.go` 仍承担 CGO 入口、注册、管理接口、dashboard、数据结构、统计引擎和工具函数等职责。单文件继续增长会增加后续维护成本。

建议结构：

```text
go/
├── main.go
├── register.go
├── management.go
├── dashboard.go
├── stats.go
├── types.go
└── source.go
```

验收：

- CGO export 仍集中在 `main.go`。
- 拆分后 `go test ./...`、GitHub Actions 构建均通过。
- 行为和 JSON 结构保持兼容。
