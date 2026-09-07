# CPA 原生用量端点修复

`cpa-v7.2.152-usage-endpoint.patch` 修复 CPA 主程序向用量插件发送记录时丢失请求端点和流式标记的问题。补丁基于 Go 模块代理发布的 `github.com/router-for-me/CLIProxyAPI/v7@v7.2.152`，不增加看板列，也不重新启用响应拦截兜底。

## 根因

CPA 的 HTTP handler 已通过 `logging.WithEndpoint` 将 `METHOD /path` 写入请求上下文，但 `sdk/pluginapi.UsageRecord` 没有 `Endpoint` 和 `Stream` 字段，`internal/pluginhost/adapters_usage_translation.go` 的用量适配器也没有转发它们。用量插件 v2.6.3 停止响应拦截补齐后，原有端点列收到空值，显示为 `-`；流式生成速度也会因缺少 `Stream` 标记而采用非流式耗时。

补丁将上下文中的请求路径传入原生 `usage.handle`，并转发核心用量记录已有的 `Stream`。端点取自本次请求上下文，不根据模型或上游名称猜测；未提供上下文的 SDK 调用仍保留空端点。

## 应用

在对应版本的 CPA 主程序源码目录中执行（将补丁路径替换为实际路径）：

```sh
git apply --check /path/to/cpa-usage-plugin/patches/cpa-v7.2.152-usage-endpoint.patch
git apply /path/to/cpa-usage-plugin/patches/cpa-v7.2.152-usage-endpoint.patch
go test ./internal/pluginhost -run TestUsageAdapter -count=1
```

然后按 CPA 原有构建部署流程重新编译并重启主程序。**仅重编译或重启用量插件无法修复主程序未发送字段的问题。** 不同 CPA 版本应先核对相同的结构和适配器，再应用修改。

## 验证与范围

补丁包含适配器回归测试，覆盖 Responses、Chat Completions、Messages、取消后的请求、纯路径和无端点上下文。移除两个字段的转发后，四个端点用例失败，恢复转发后通过。

插件仓库的 `TestNativeUsageEndpointReachesDashboard` 验证原生 JSON 上报、流式/非流式标记、快照序列化恢复和 `all`/`24h` 查询。现有前端测试验证原有端点列的渲染。

本地验证已通过：CPA `internal/pluginhost` 全包测试、用量适配器 race 测试；插件全量 race 测试、`go vet ./...`、共享库构建，以及 141 项前端测试。补丁也通过了在未修改的 v7.2.152 源码上的 `git apply --check`。

部署后需通过实际请求检查 `dashboard-api-detail` 的 `recent_events[].endpoint` 和页面中的端点值。本补丁不会补造已经保存为空的历史端点；历史恢复需要原始请求日志等证据。
