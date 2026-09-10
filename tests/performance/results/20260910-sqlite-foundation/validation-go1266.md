# Go 1.26.6 安全升级与 SQLite 迁移暂存

日期：2026-09-10。工作区基于 B1=`967915b`，未提交、未发布。历史查询/驱动 benchmark 使用 Go 1.26.4；本页不将它们重新标记为 1.26.6 实测收益。

## 安全修复

Go 1.26.4 的 `govulncheck v1.8.0` 检出三个可达标准库漏洞：GO-2026-6090（TLS 握手后限制）、GO-2026-5972（ASN.1 递归深度）、GO-2026-5856（TLS ECH 隐私）。前两项修复于 1.26.6，后一项修复于 1.26.5。

主模块、stock-host 测试模块、CI 和性能容器已统一到 Go 1.26.6。升级后以下两条命令都输出 `No vulnerabilities found.` 并以状态 0 退出：

```sh
cd go
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -tags sqlite_purego ./...
```

该扫描不覆盖 SQLite 原生 C 代码，也不等于不存在未知漏洞。

## 本轮迁移实现

- schema 2 在事务中新增 `migration_sources` / `migration_chunks`；兼容 schema 1 升级，不修改记录 ID、版本或业务查询 generation。
- 仅对旧写入器已停止写入的普通文件进行暂存，拒绝末级符号链接；不改写、不删除原文件。
- 内容 SHA-256、文件大小与格式固定来源身份。每次恢复完整扫描源文件一次以确认指纹，然后从已提交偏移续传；同路径内容或格式变化明确报冲突。
- 每块 256 KiB，每批输入最多 2 MiB，偏移与分块同事务；取消/写入失败只保留已提交批次。旧文件里超过原型 1 MiB 的记录仍可完整暂存。
- 暂存后逐块检查完整内容 SHA-256，再标记可读；重试和读取至 EOF 也校验内容。未验证来源不能交给解析器。读取没有长期读事务，不会因整文件读取一直钉住 WAL。
- 保存原始字节，不做 JSON→float64 转换，不丢 `metadata_only`、相同真实请求、坏行或旧快照残差。**这些字节尚未被解析、关联或计入业务统计**，`ready` 仅代表暂存已校验，不是后端可激活。

暂存额外占用约一份输入大小的数据库空间（另加索引/WAL）；初次需要源文件指纹读取、复制和暂存校验，恢复也需要重新验证指纹。这是可恢复与校验的成本，不宣称首次迁移更快或磁盘更小。批次/分块是组件预算，不是进程 RSS 上限。

## 回归覆盖与复现

新增 8 项回归：超大记录/真实重复/元数据原样保留；写入失败整批回滚及数据库重新打开后续传；相同大小/mtime 的来源变更及格式冲突；空文件/取消/读取器生命周期；未验证暂存损坏；schema 1 升级数据不变；验证后数据损坏；旧游标写入冲突。

```sh
cd go
go test -race ./...
go vet ./...
go test -race -tags sqlite_purego -run '^TestSQLite(Ledger|Migration)' -count=1
go vet -tags sqlite_purego ./...
```

写入失败由 SQLite trigger 在第二批的第二块报错注入，证明第二批首块和游标一起回滚；这是确定性故障回归，不冒充真实磁盘满或迁移进程被杀测试。原基础层另有子进程异常退出测试。

最终验证均以状态 0 退出：

| 检查 | 结果 |
| --- | --- |
| Go 1.26.6 全量 race / vet | 通过（race 13.075s） |
| 纯 Go 驱动 21 项 SQLite race / 全量 vet | 通过（race 7.541s） |
| 前端 / 更新脚本 | 144 项 / 1 项通过 |
| 最终源码默认及纯 Go 漏洞扫描 | 均为 `No vulnerabilities found.` |
| macOS arm64 两驱动 c-shared 构建及 stock CPA SDK 装载 | 均通过 |
| Linux arm64 glibc 镜像构建、SYMBOLIC 断言、21 项 SQLite 原生回归及 stock CPA SDK 装载 | 均通过；容器测试未启用 race |

最终镜像 `cpa-usage-performance:sqlite-migration`：`sha256:f3068211e638b648f63fc708e93c7bbf1a584be917df7071eb430db702125550`。构建器 `golang:1.26.6-bookworm`：`sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36`。CPA digest 仍为 `sha256:02b3bb12d866cfc8255b5ee066eee86ad425669da6a79de5b55569eddbe48ba8`。

原生容器复现（仓库根目录）：

```sh
docker build --build-arg GOPROXY=https://goproxy.cn,direct -f tests/performance/Dockerfile -t cpa-usage-performance:sqlite-migration .
docker run --rm --entrypoint /performance/performance.test cpa-usage-performance:sqlite-migration -test.run '^TestSQLite(Ledger|Migration)' -test.v
docker run --rm --entrypoint /performance/stock-host cpa-usage-performance:sqlite-migration /performance/usage-dashboard-zduu.so
```

macOS 构建参数为 `go build [-tags sqlite_purego] -buildmode=c-shared -buildvcs=false -trimpath`，两个库分别通过 `tests/stock-host` 的 `go run . <库路径>`。CGO 库 SHA-256：`f28750a8d12a46a329cc63f738215202ec749f67c40c7c5e4c5392710b91ab9a`；纯 Go 库：`92ec622a73d6cc1ffff02aa8f52409d6948fb6cb2a56ffac5ea4eaecf44fb65e`。这些是装载证据，不是资源 benchmark。

完整五平台运行、快照/日志重叠协调、历史残差解析、后端激活/备份/降级、容器 RSS/CPU 和 24 小时 soak 仍未验收。

## 当前实现校验和

| 文件 | SHA-256 |
| --- | --- |
| go/sqlite_ledger.go | 4bf174689aa981e434f5fc61aa1d48673eef17b6e3ffe1dafffe9698284bd256 |
| go/sqlite_migration.go | 9b48ab5e9ad3b977c26b7a87c3f3345e33b6555705f1aee410ceef6630d8e63d |
| go/sqlite_migration_test.go | a84d9d30f3fe8f607bb3062e09fd21e9823ad5fb2377eca2098f27f30d40f5b6 |
| go/go.mod | 0089bd69f4de5d1f7824f134ff16bb7926b6ec59b388bfdc9538aa59689f4b6c |
| go/go.sum | a4930aa231914648093477b8553d00d257b336ebe2bab93914b84ce2c3ae2e58 |
| tests/performance/Dockerfile | d2440f24d0c78aa339972555f70e3eb26422a31e18f8e50775ed9d721d187dd1 |
