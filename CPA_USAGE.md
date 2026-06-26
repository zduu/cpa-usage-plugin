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
      # 可选：API key 分组哈希 salt。留空使用进程内随机 salt。
      api_key_hash_salt: ""
      # 可选：启用 JSONL 事件持久化，避免重启丢失统计。默认 false。
      storage_enabled: false
      # 可选：JSONL 持久化文件路径。相对路径基于 CPA 工作目录。
      storage_path: usage-statistics.jsonl
      # 可选：持久化 flush 间隔秒数。默认 30。
      storage_flush_interval_seconds: 30
      # 可选：模型价格表 JSON 文件路径。相对路径基于 CPA 工作目录。
      price_storage_path: usage-statistics-prices.json
      # 可选：允许外部脚本更新插件文件。默认 false。
      update_enabled: false
      # 可选：latest 或指定版本号，例如 v1.1.0。
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
pluginhost: plugin registered plugin_id=usage-statistics plugin_name=用量统计 version=1.2.4
```

## 按配置更新插件文件

如果希望在配置中控制是否更新、更新到最新版本还是指定版本，可以使用仓库中的更新脚本。脚本只替换 `.so` 文件，不会自动重启 CPA。

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
      update_version: latest   # 或 v1.1.0
```

执行更新脚本：

```bash
cd /home/<用户>/docker/CLIProxyAPI
bash update-usage-statistics.sh        # 手动运行。或用 --restart 自动重启容器
```

# 脚本完成后手动重启 CPA 容器（如果没用 --restart）：

```bash
docker restart cli-proxy-api
```

说明：插件 `.so` 被 CPA 进程加载后，直接覆盖文件不会让运行中的进程使用新代码；需要重启 CPA 后才会加载新版本。

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
- **来源统计**：按上游来源聚合请求数和成功率。
- **上游接口统计**：按上游接口聚合，点击查看模型分布详情。
- **模型统计**：跨接口的模型汇总，包含请求数、token、平均延迟、成功率和费用。
- **模型价格设置**：在后端全局保存输入/输出/缓存 token 价格（US$/M token），跨设备访问看板可见同一份最新价格。
- **请求事件明细**：按模型、来源、凭证筛选，滚动表格查看。默认最多显示 500 条。
- **导出**：当前接口明细或全量事件的 CSV/JSON 导出。
- **导入**：上传 JSON 文件导入统计数据，完成后显示新增/跳过/过期忽略的明细数。

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

响应包含 `usage`（无 details 聚合数据）、`health_grid`（672 个 15 分钟槽位）、`source_stats`、`model_stats` 和 `_meta` 元数据。

### 查询事件

```bash
# 查询最近 24 小时 gpt-4 的请求事件
curl "http://127.0.0.1:8317/v0/management/plugins/usage-statistics/dashboard-events?limit=20&offset=0&range=24h&model=gpt-4" \
  -H 'x-management-key: <你的管理密钥>'
```

### 健康检查

```bash
curl http://127.0.0.1:8317/v0/management/plugins/usage-statistics/health \
  -H 'x-management-key: <你的管理密钥>'
```

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

## 注意

- 当前统计存储在插件进程内存中，CPA 重启前请先导出数据。
- 多实例部署时，每个实例独立统计。
- token 是否完整取决于上游返回的 usage 信息；CPA 主程序需向插件传递 snake_case usage 字段。
- 明细记录受 `max_details_per_model` 和 `retention_days` 限制，超出部分自动淘汰并更新计数器。
