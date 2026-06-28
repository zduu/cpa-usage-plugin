# 在 CPA 中使用

## 1. 下载插件

从 [GitHub Releases](https://github.com/zduu/cpa-usage-plugin/releases) 下载最新版本的插件：

- Linux x86_64 / amd64：`usage-statistics-linux-amd64.so`
- Linux arm64 / aarch64：`usage-statistics-linux-arm64.so`
- macOS Intel：`usage-statistics-darwin-amd64.dylib`
- macOS Apple Silicon：`usage-statistics-darwin-arm64.dylib`
- Windows x86_64 / amd64：`usage-statistics-windows-amd64.dll`
- `usage-statistics.so` 保留为 amd64 兼容别名。

Linux 下载后将文件重命名为 `usage-statistics.so` 放入 CPA 插件目录；macOS/Windows 通常应分别保留或重命名为 `.dylib`/`.dll`，并确保 CPA 配置引用对应文件名。

> 也可以从 GitHub Actions 的 `Build Plugin` workflow 下载对应架构的 `usage-statistics-plugin-*` artifact 自行构建。

## 2. 放入插件目录

### Docker 部署（推荐）

CPA（CLIProxyAPI）通常以 Docker 方式运行，镜像为 `eceasy/cli-proxy-api:latest`。

#### 方式一：docker cp（简单快速）

将下载的 `.so` 文件复制到运行中的容器内：

```bash
cp usage-statistics-linux-amd64.so usage-statistics.so
docker cp usage-statistics.so cli-proxy-api:/CLIProxyAPI/plugins/
docker exec cli-proxy-api chmod 755 /CLIProxyAPI/plugins/usage-statistics.so
```

> 容器名和插件目录以实际为准：可通过 `docker ps` 查看容器名，通过 `docker exec <容器名> ls /CLIProxyAPI/plugins/` 确认插件目录。

#### 方式二：volume 挂载（持久化）

将宿主目录挂载到容器，插件放在宿主目录即可：

```bash
# 先在宿主创建插件目录并放入 .so 文件
mkdir -p /home/<用户>/docker/CLIProxyAPI/plugins
cp usage-statistics-linux-amd64.so /home/<用户>/docker/CLIProxyAPI/plugins/usage-statistics.so
```

然后更新 `docker run` 命令，添加插件目录挂载：

```bash
docker run -d \
  --name cli-proxy-api \
  -v /home/<用户>/docker/CLIProxyAPI/config.yaml:/CLIProxyAPI/config.yaml \
  -v /home/<用户>/docker/CLIProxyAPI/auths:/root/.cli-proxy-api \
  -v /home/<用户>/docker/CLIProxyAPI/logs:/CLIProxyAPI/logs \
  -v /home/<用户>/docker/CLIProxyAPI/plugins:/CLIProxyAPI/plugins \
  -p 8317:8317 \
  -e TZ=Asia/Shanghai \
  eceasy/cli-proxy-api:latest
```

> **注意**：容器内工作目录为 `/CLIProxyAPI`，插件目录路径为 `/CLIProxyAPI/plugins`（在插件配置中通过 `dir` 字段指定，默认值通常为 `plugins`，即相对于工作目录的路径）。挂载时确保宿主目录路径和容器内路径正确对应。

### 直接部署（非 Docker）

如果 CPA 直接运行在宿主机上，将 `usage-statistics.so` 放到 CPA 工作目录下的 `plugins` 子目录：

```bash
cp usage-statistics-linux-amd64.so /path/to/CLIProxyAPI/plugins/usage-statistics.so
chmod 755 /path/to/CLIProxyAPI/plugins/usage-statistics.so
```

## 3. 启用插件

在 CPA 配置文件（通常为 `config.yaml`）中启用插件系统，并启用 `usage-statistics`：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    usage-statistics:
      enabled: true
      # 每个上游接口/模型最多保留的请求明细条数。默认 5000。
      max_details_per_model: 5000
      # 内存统计最多保留的天数，0 表示不按时间淘汰。默认 30。
      retention_days: 30
      # usage 记录去重窗口分钟数，0 表示关闭去重。默认 1440（24小时）。
      dedup_window_minutes: 1440
      # 可选：允许记录的响应头名称列表（逗号分隔），* 表示全部。留空不记录。
      log_response_headers: ""
      # 可选：API key 分组 hash salt。留空使用进程内随机 salt；固定后同一实例跨重启 hash 稳定。
      api_key_hash_salt: ""
      # 可选：启用 JSONL 事件持久化，避免重启丢失统计。默认 false。
      storage_enabled: false
      # 可选：JSONL 持久化路径。相对路径基于 CPA 工作目录；*.jsonl 旧单文件会兼容读取。
      storage_path: usage-statistics.jsonl
      # 可选：持久化 flush 间隔秒数。默认 30。
      storage_flush_interval_seconds: 30
      # 可选：持久化 snapshot 最大写入间隔秒数。默认 300，0 表示只按记录数触发。
      storage_snapshot_interval_seconds: 300
      # 可选：每新增多少条持久化记录写一次 snapshot。默认 1000，0 表示只按时间触发。
      storage_snapshot_record_interval: 1000
      # 可选：持久化文件 fsync 最大间隔秒数。默认 0，不按时间强制同步。
      storage_sync_interval_seconds: 0
      # 可选：每新增多少条持久化记录执行一次 fsync。默认 0，不按记录数强制同步。
      storage_sync_record_interval: 0
      # 可选：看板事件导出最多返回的明细条数。默认 100000，0 表示不限制。
      export_max_records: 100000
      # 可选：模型价格表 JSON 文件路径。相对路径基于 CPA 工作目录。
      price_storage_path: usage-statistics-prices.json
      # 可选：允许外部脚本更新插件文件。默认 false。
      update_enabled: false
      # 可选：latest 或指定版本号，例如 v1.2.18。
      update_version: latest
```

然后重启 CPA 服务：

```bash
# Docker 方式
docker restart cli-proxy-api
```

启动后查看日志确认插件加载成功：

```text
pluginhost: plugin loaded plugin_id=usage-statistics path=plugins/usage-statistics.so
pluginhost: plugin registered plugin_id=usage-statistics plugin_name=用量统计 version=1.2.18
```

## 数据持久化（可选）

默认 `storage_enabled: false`，统计只保存在插件进程内存中，重启 CPA/容器后会清零。需要重启或更新插件后保留数据时，开启 JSONL 持久化，并把 `storage_path` 放到宿主机挂载目录中。

推荐单独挂载一个数据目录：

```bash
docker run -d \
  --name cli-proxy-api \
  -v /home/<用户>/docker/CLIProxyAPI/config.yaml:/CLIProxyAPI/config.yaml \
  -v /home/<用户>/docker/CLIProxyAPI/auths:/root/.cli-proxy-api \
  -v /home/<用户>/docker/CLIProxyAPI/logs:/CLIProxyAPI/logs \
  -v /home/<用户>/docker/CLIProxyAPI/plugins:/CLIProxyAPI/plugins \
  -v /home/<用户>/docker/CLIProxyAPI/data:/CLIProxyAPI/data \
  -p 8317:8317 \
  -e TZ=Asia/Shanghai \
  eceasy/cli-proxy-api:latest
```

然后在 `config.yaml` 中开启：

```yaml
plugins:
  configs:
    usage-statistics:
      enabled: true
      storage_enabled: true
      storage_path: data/usage-statistics.jsonl
      storage_flush_interval_seconds: 5
      storage_snapshot_interval_seconds: 300
      storage_snapshot_record_interval: 1000
      storage_sync_interval_seconds: 0
      storage_sync_record_interval: 0
```

说明：

- 不配置或保持 `storage_enabled: false` 时，就是原来的内存模式，重启清零。
- 开启后每条新请求会进入后台写入队列，由后台 writer 批量追加写入日期分片，例如 `data/usage-statistics/usage-2026-06-28.jsonl`；插件启动时只 replay 保留窗口内的日期分片。
- 如果 `storage_path` 配置为历史单文件路径（如 `data/usage-statistics.jsonl`），插件会继续读取该旧文件作为兼容输入，新数据会写入同名目录 `data/usage-statistics/` 下的日期分片。
- 插件正常关闭、日期分片切换、达到 `storage_snapshot_interval_seconds` 或达到 `storage_snapshot_record_interval` 时会写入 `snapshot.json`；snapshot 成功后会清理 snapshot 日期之前的旧 JSONL 分片。下次启动会先加载 snapshot，再 replay snapshot 当天及之后的分片增量。
- `storage_path` 是相对 CPA 工作目录的路径；Docker 中建议放到已挂载的 `/CLIProxyAPI/data` 或其他宿主机 volume。
- 当 `retention_days` 大于 0 时，保留窗口外的日期分片会被清理；旧单文件不会自动删除。
- `storage_flush_interval_seconds` 越小，异常退出时最多丢失的数据越少；默认 30 秒，想更稳可以设为 5 或 1。
- `storage_snapshot_interval_seconds` 和 `storage_snapshot_record_interval` 控制启动恢复成本；默认 300 秒或 1000 条写一次快照，高请求量实例可降低记录间隔，低频实例可保持默认。
- `storage_sync_interval_seconds` 和 `storage_sync_record_interval` 默认关闭；如果需要更强的异常断电保护，可配置如 `storage_sync_interval_seconds: 30` 或 `storage_sync_record_interval: 1000`，但会增加磁盘 I/O。
- `/health` 的 `storage.write_queue_length` 和 `storage.write_queue_capacity` 可观察后台写入队列积压；`storage.last_write_batch_records`、`storage.last_write_batch_duration_ms`、`storage.last_write_queue_wait_ms` 可观察最近 writer 批次规模、写入耗时和最长排队时长；`storage.write_batch_avg_duration_ms`、`storage.write_batch_p95_duration_ms`、`storage.write_batch_p99_duration_ms`、`storage.write_queue_wait_avg_ms`、`storage.write_queue_wait_p95_ms`、`storage.write_queue_wait_p99_ms` 和 `storage.write_pressure` 可观察持续磁盘压力与长尾抖动。看板底部出现“持久化排队中”或“持久化写入偏慢”时，说明磁盘写入速度短时间低于请求记录速度。
- 如果已经有内存数据，建议先导出；开启持久化并重启后，再把导出的 JSON 导入一次，后续数据才会继续写入持久化文件。

## 按配置更新插件文件

如果希望在配置中控制是否更新、更新到最新版本还是指定版本，可以使用仓库中的更新脚本。下面的命令会把仓库脚本下载到 CPA 工作目录。脚本会读取同目录 `config.yaml` 中的 `update_enabled` 和 `update_version`，自动选择当前系统对应的 release 资产并安装到插件目录；默认不会重启 CPA，只有传入 `--restart` 或 `--auto-restart` 时才会重启 Docker 容器。

```bash
curl -fsSL https://raw.githubusercontent.com/zduu/cpa-usage-plugin/main/scripts/update-latest-release.sh \
  -o /home/<用户>/docker/CLIProxyAPI/update-usage-statistics.sh
chmod +x /home/<用户>/docker/CLIProxyAPI/update-usage-statistics.sh
```

在 `config.yaml` 中开启更新并选择版本：

```yaml
plugins:
  configs:
    usage-statistics:
      enabled: true
      update_enabled: true
      update_version: latest   # 或 v1.2.18
```

执行更新脚本：

```bash
cd /home/<用户>/docker/CLIProxyAPI
bash update-usage-statistics.sh        # 只安装新插件文件，不重启
bash update-usage-statistics.sh --restart  # 安装后自动 docker restart cli-proxy-api
```

脚本完成后如果没有使用 `--restart`，需要手动重启 CPA 容器：

```bash
docker restart cli-proxy-api
```

说明：插件文件被 CPA 进程加载后，直接覆盖文件不会让运行中的进程使用新代码；需要重启 CPA 后才会加载新版本。Linux 默认安装文件名为 `usage-statistics.so`，macOS 为 `usage-statistics.dylib`，Windows 为 `usage-statistics.dll`。

## 自动更新（crontab）

如果希望插件在后台自动检查更新并重启 CPA，在宿主机配一条 crontab 即可：

```bash
# 每 6 小时检查一次，有更新自动重启容器
crontab -e
```

添加一行：

```text
0 */6 * * * cd ~/docker/CLIProxyAPI && bash update-usage-statistics.sh --restart >> ~/docker/CLIProxyAPI/update.log 2>&1
```

说明：

- 脚本通过 `--restart` 在安装新插件后自动执行 `docker restart cli-proxy-api`
- 如果已是最新版本，脚本直接退出，**不会误重启容器**
- 日志写入 `update.log`，方便排查
- 默认容器名为 `cli-proxy-api`，可通过环境变量 `DOCKER_CONTAINER` 覆盖

也可以通过 `--force --restart` 组合强制重新下载并重启：

```bash
bash update-usage-statistics.sh --force --restart
```

更新脚本会根据当前系统自动选择 release 资产：

- `x86_64` / `amd64` 下载 `usage-statistics-linux-amd64.so`
- `aarch64` / `arm64` 下载 `usage-statistics-linux-arm64.so`
- macOS 会下载对应的 `usage-statistics-darwin-*.dylib`
- Windows amd64 会下载 `usage-statistics-windows-amd64.dll`

安装到插件目录时，Linux 默认保存为 `usage-statistics.so`，macOS 默认保存为 `usage-statistics.dylib`，Windows 默认保存为 `usage-statistics.dll`。如果需要手动指定目标平台，可设置 `PLUGIN_PLATFORM=linux-amd64`、`PLUGIN_PLATFORM=linux-arm64`、`PLUGIN_PLATFORM=darwin-amd64`、`PLUGIN_PLATFORM=darwin-arm64` 或 `PLUGIN_PLATFORM=windows-amd64`；如果使用自定义资产名或安装文件名，可设置 `PLUGIN_ASSET` / `PLUGIN_FILE`。

脚本支持的参数：

| 参数 | 作用 |
|------|------|
| `--restart` | 更新成功后自动重启容器 |
| `--force` | 跳过版本和 SHA 检查，强制重新下载安装 |

## 4. 查看统计

登录 CPA 管理端（默认 `http://<服务器IP>:8317/management.html`），在菜单中打开"用量统计"。

> 管理 API 调用需要在请求头中包含管理密钥（`x-management-key`），密钥为 CPA 配置中 `remote-management.secret-key` 设置的值。

### 页面功能

- **统计卡片**：总请求数、成功/失败、总 token、每分钟请求、估算花费，附带小时级折线图。
- **服务健康监测**：7 天 × 15 分钟粒度的彩色网格，鼠标悬停显示窗口详情，灰色格表示无请求。
- **API 详细统计**：按调用 CPA 服务的客户端 API key 聚合。页面显示脱敏 key；导入不同实例导出的同一脱敏 key 时会合并展示。
- **上游接口统计**：按上游接口聚合，点击查看模型分布详情。
- **模型统计**：跨接口的模型汇总，包含请求数、token、平均延迟、成功率和费用。
- **模型价格设置**：在后端全局保存输入/输出/缓存 token 价格（US$/M token），跨设备访问看板可见同一份最新价格。
- **请求事件明细**：按模型、来源、凭证筛选，滚动表格查看。默认最多显示 500 条。
- **导出**：当前接口明细或全量事件的 CSV/JSON 导出。
- **导入**：上传 JSON 文件导入统计数据，完成后显示新增/跳过/过期忽略的明细数；导入后摘要会重新聚合客户端 API、模型、来源和健康网格。

### 性能说明

- 看板首页使用 `/dashboard-summary` 端点，**不传输请求明细**，首包体积极小，即使存储数十万条记录也能快速打开。
- 事件明细表格通过 `/dashboard-events` 加载，页面以滚动表格展示，单次最多 500 条。
- 保留策略（`retention_days` + `max_details_per_model`）自动淘汰过期和超量记录。
- 可选 JSONL 持久化通过 `storage_enabled` 开启，重启后会 replay 持久化事件并继续应用保留策略。
- 页面底部 `_meta` 区域可见当前保留配置、已存储明细数和累积淘汰数。

## 5. 管理 API 使用

以下端点可通过管理 API 调用（需要管理密钥）：

### 获取摘要（推荐日常使用）

```bash
curl http://127.0.0.1:8317/v0/management/plugins/usage-statistics/dashboard-summary \
  -H 'x-management-key: <你的管理密钥>'
```

响应包含 `usage`（无 details 聚合数据）、`health_grid`（672 个 15 分钟槽位）、`source_stats`（用于事件来源筛选）、`credential_stats`、`client_api_stats`、`model_stats` 和 `_meta` 元数据。

`/dashboard-summary`、`/dashboard-events`、`/dashboard-api-detail` 和 `/dashboard-events-export` 会返回弱 `ETag`；内置看板轮询会自动带上 `If-None-Match`，数据未变化时复用本地缓存。外部脚本也可在下一次请求带上 `If-None-Match`，接口返回 304 时跳过重复解析和传输。

```bash
curl -sD /tmp/cpa-usage.headers \
  "http://127.0.0.1:8317/v0/management/plugins/usage-statistics/dashboard-summary" \
  -H 'x-management-key: <你的管理密钥>' \
  -o /tmp/cpa-usage-summary.json

etag=$(awk 'tolower($1)=="etag:" {print $2}' /tmp/cpa-usage.headers | tr -d '\r')
curl -i \
  "http://127.0.0.1:8317/v0/management/plugins/usage-statistics/dashboard-summary" \
  -H 'x-management-key: <你的管理密钥>' \
  -H "If-None-Match: $etag"
```

### 查询事件

```bash
# 查询最近 24 小时 gpt-4 的请求事件
curl "http://127.0.0.1:8317/v0/management/plugins/usage-statistics/dashboard-events?limit=20&offset=0&range=24h&model=gpt-4" \
  -H 'x-management-key: <你的管理密钥>'
```

### 按筛选导出事件

默认返回 JSON；需要直接下载 CSV 或便于逐行处理的 JSONL 时，可增加 `format` 参数。大导出可增加 `gzip=1`，客户端需按 gzip 解压响应体。

```bash
# 导出最近 24 小时事件为 CSV
curl "http://127.0.0.1:8317/v0/management/plugins/usage-statistics/dashboard-events-export?range=24h&format=csv" \
  -H 'x-management-key: <你的管理密钥>' \
  -o usage-events.csv

# 导出最近 7 天事件为 gzip 压缩 JSONL
curl "http://127.0.0.1:8317/v0/management/plugins/usage-statistics/dashboard-events-export?range=7d&format=jsonl&gzip=1&limit=50000" \
  -H 'x-management-key: <你的管理密钥>' \
  -o usage-events.jsonl.gz
```

### 健康检查

```bash
curl http://127.0.0.1:8317/v0/management/plugins/usage-statistics/health \
  -H 'x-management-key: <你的管理密钥>'
```

`dashboard-events-export` 默认最多返回 `export_max_records` 条明细，也可以用 `limit` 为单次导出指定更小上限；JSON 响应会带 `truncated`，CSV/JSONL 响应头会带 `X-Total-Count`、`X-Exported-Count` 和 `X-Export-Truncated`。需要完全不限制时可配置 `export_max_records: 0`，但超大导出会增加 CPA 管理接口内存和响应体压力。

`storage` 字段会返回持久化状态、后台写入队列长度、最近 writer 批次指标、writer 滑动平均、p95/p99 长尾指标和 `write_pressure`、最近和累计清理旧分片数量、待 flush/sync/snapshot 记录数和最近错误；`runtime` 字段会返回摘要缓存命中/未命中、事件缓存命中/未命中、事件索引条目数、条件请求 304 命中率，以及最近 summary/events/api-detail 查询耗时，便于观察看板压力和筛选性能。

### 数据导出

```bash
curl http://127.0.0.1:8317/v0/management/plugins/usage-statistics/usage/export \
  -H 'x-management-key: <你的管理密钥>'
```

### 数据导入

```bash
curl -X POST http://127.0.0.1:8317/v0/management/plugins/usage-statistics/usage/import \
  -H 'Content-Type: application/json' \
  -H 'x-management-key: <你的管理密钥>' \
  --data-binary @usage-export.json
```

导入响应包含 `added`（新增条数）、`skipped`（去重跳过）、`ignored_by_retention`（超出保留窗口忽略）。
同时包含 `input_records`（输入记录数）、`accepted_records`（被处理记录数）、`rejected_records`（校验拒绝数）、`total_requests` 和 `failed_requests`，便于核对导入结果。

## 注意

- 默认仅使用插件进程内存；如需 CPA 重启后自动恢复统计，请开启 `storage_enabled` 并将 `storage_path` 放在持久化目录。未开启持久化时，重启前请先导出数据。
- 多实例部署时，每个实例独立统计。
- token 是否完整取决于上游返回的 usage 信息；CPA 主程序需向插件传递 snake_case usage 字段。
- 明细记录受 `max_details_per_model` 和 `retention_days` 限制，超出部分自动淘汰并更新计数器。
- `api_key_hash_salt` 只影响新记录的 `api_key_hash`。客户端 API 统计优先按脱敏后的 `api_key` 展示值聚合，缺失时再使用 hash；hash 仅用于分组/排查，不能反推原始 key。
