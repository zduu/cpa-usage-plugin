# 单容器优化：执行与验收记录

本记录配合 [施工清单](single-container-performance-checklist.tmp.md)。基线为
`74c3c099c532652fd267a64942e4dc7676c24181`。未实际验证的门槛不视为通过。

## 固定测量条件（在修改实现前确定）

- CPA：`eceasy/cli-proxy-api:v7.2.152`，manifest digest
  `sha256:02b3bb12d866cfc8255b5ee066eee86ad425669da6a79de5b55569eddbe48ba8`。
- 本地：Apple M1 / macOS arm64，Go 1.26.4，Node 24.12.0；Docker
  client 29.6.2 / engine 29.5.2，Colima Linux arm64。
- 容器：单个 CPA，2 CPU、2 GiB 内存、独立本地 bind mount（Colima VM 约 3 GiB）；配置为
  `tests/performance/config.yaml`。跨架构的模拟运行独立记录，不与原生性能比较。
- 数据：`go/performance_workload_test.go`，PCG 固定种子 20260909，10 万和
  100 万请求；低/高基数分别改变模型、来源、凭据和客户端。包括迟到记录、
  同时间戳真实请求、错误、响应头和精简账本，保留 30 天和原明细上限。
- 正式资源目标：同负载稳定/峰值进程 RSS 至少降低 25%，CPU 至少降低 15%。
  两项必须同时测量，不用 Go B/op 代替 RSS 或 CPU。
- 体验：相同配置重复至少 3 次，优化后回调、查询、首屏、改价、导入导出及启动
  的 p50/p95/p99 不超过基线重复测量上界；如果样本误差不足以判断，增加样本，
  不自行引入允许变慢的百分比。冷、热缓存分开记录。
- 预算目标：事件结果缓存 8 MiB、查询索引 32 MiB（共享数据另计）、后台编码
  可复用缓冲不超过 256 KiB；SQLite 原型须另测连接、页缓存、WAL 和事务寿命。

## 初始回归与组件基准

初始 `go test ./...` 通过；`node --test go/dashboard/*.test.js` 141 项通过。
以下是相同机器上 3 次、200ms 的组件基准，不能作为单容器验收结果。

| 基准 | 基线耗时范围 | 基线 B/op | 改造后 |
| --- | ---: | ---: | --- |
| 摘要缓存命中 100k | 17.32–17.46 µs | 100230 | 待测 |
| 7 天范围摘要 100k | 55.33–55.40 ms | 408152–408212 | 待测 |
| 模型事件索引已建 100k | 2.12–2.13 ms | 464899–465052 | 待测 |
| 模型事件冷索引 100k | 5.89–6.76 ms | 2251152 | 待测 |
| 事件结果缓存命中 100k | 104.81–105.07 µs | 225208 | 待测 |
| 接口详情 100k | 11.86–11.88 ms | 78736 | 待测 |
| 写入已有账本（1 模型） | 2.256–2.260 µs | 2972–2995 | 待测 |

## 变更影响矩阵

| 变化 | 事件顺序/筛选索引 | 请求聚合/范围缓存 | 价格/货币 |
| --- | --- | --- | --- |
| 实时、迟到、真实重复 usage | 新增独立身份，增量插入 | 受影响分段和查询失效 | 采用当前价格 |
| 端点/流式元数据补充 | 身份和顺序不变 | 不改变计数，事件响应失效 | 不改变价格 |
| 导入、重放、历史修复 | 按已有语义重建或事务替换 | 同步重建受影响聚合 | 同版本重算 |
| 到期或明细转账本 | 删除可见引用 | 到期扣减；转账本保留统计 | 保留精确计价依据 |
| 改价（含保存失败） | 顺序/筛选不变 | 金额缓存失效 | 返回前同步更新价格版本 |
| 汇率变化 | 不变 | 仅相关响应元数据更新 | 更新货币版本 |
| 时间窗口推进、配置变化 | 边界重新校验 | 窗口/配置缓存失效 | 时区影响计价 |

## 验证入口

```sh
cd go
go test ./...
go test -race ./...
go vet ./...
go test -run '^$' -bench BenchmarkPerformanceMixed -benchmem -benchtime=200ms -count=3
CPA_PERF_MILLION=1 go test -run '^$' -bench BenchmarkPerformanceMixed -benchmem -benchtime=200ms -count=3
CPA_PERF_PROBE=1 go test -run '^TestPerformanceResourceProbe$' -v
```

容器构建入口为 `tests/performance/Dockerfile`；实际装载、容器资源、浏览器、
异常恢复和五平台验收需在下方记录结果后才能勾选施工清单。
