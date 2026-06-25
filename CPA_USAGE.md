# 在 CPA 中使用

## 1. 下载插件

从 [GitHub Releases](https://github.com/zduu/cpa-usage-plugin/releases) 下载最新版本的 `usage-statistics.so`。

> 也可以从 GitHub Actions 的 `Build Plugin` workflow 下载 `usage-statistics-plugin` artifact 自行构建。

## 2. 放入插件目录

### Docker 部署（推荐）

CPA（CLIProxyAPI）通常以 Docker 方式运行，镜像为 `eceasy/cli-proxy-api:latest`。

#### 方式一：docker cp（简单快速）

将下载的 `.so` 文件复制到运行中的容器内：

```bash
docker cp usage-statistics.so cli-proxy-api:/CLIProxyAPI/plugins/
docker exec cli-proxy-api chmod 755 /CLIProxyAPI/plugins/usage-statistics.so
```

> 容器名和插件目录以实际为准：可通过 `docker ps` 查看容器名，通过 `docker exec <容器名> ls /CLIProxyAPI/plugins/` 确认插件目录。

#### 方式二：volume 挂载（持久化）

将宿主目录挂载到容器，插件放在宿主目录即可：

```bash
# 先在宿主创建插件目录并放入 .so 文件
mkdir -p /home/<用户>/docker/CLIProxyAPI/plugins
cp usage-statistics.so /home/<用户>/docker/CLIProxyAPI/plugins/
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
cp usage-statistics.so /path/to/CLIProxyAPI/plugins/
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
pluginhost: plugin registered plugin_id=usage-statistics plugin_name=用量统计 version=1.2.0
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
CONFIG_FILE=/home/<用户>/docker/CLIProxyAPI/config.yaml \
PLUGIN_DIR=/home/<用户>/docker/CLIProxyAPI/plugins \
/home/<用户>/docker/CLIProxyAPI/update-usage-statistics.sh
```

脚本完成后手动重启 CPA 容器：

```bash
docker restart cli-proxy-api
```

说明：插件 `.so` 被 CPA 进程加载后，直接覆盖文件不会让运行中的进程使用新代码；需要重启 CPA 后才会加载新版本。

## 4. 查看统计

登录 CPA 管理端（默认 `http://<服务器IP>:8317/management.html`），在菜单中打开"用量统计"。

> 管理 API 调用需要在请求头中包含管理密钥（`x-management-key`），密钥为 CPA 配置中 `remote-management.secret-key` 设置的值。

页面包含：

- 总请求数、成功/失败、总 token、估算花费。
- 服务健康监测：鼠标悬停网格查看 15 分钟窗口信息，灰色格表示无请求。
- 接口详细统计：点击接口行查看该接口的模型分布、凭证/来源分布、错误统计和最近请求。
- 模型统计、凭证统计、请求事件明细。
- 当前接口明细和全量事件的 CSV/JSON 导出。

## 5. 数据导入导出

页面右上角可导入/导出统计数据。

也可以使用管理接口（需要携带管理密钥）：

```bash
# 导出
curl http://127.0.0.1:8317/v0/management/plugins/usage-statistics/usage/export \
  -H 'x-management-key: <你的管理密钥>'

# 导入
curl -X POST http://127.0.0.1:8317/v0/management/plugins/usage-statistics/usage/import \
  -H 'Content-Type: application/json' \
  -H 'x-management-key: <你的管理密钥>' \
  --data-binary @usage-export.json
```

## 注意

- 当前统计存储在插件进程内存中，CPA 重启前请先导出数据。
- 多实例部署时，每个实例独立统计。
- token 是否完整取决于上游返回的 usage 信息；CPA 主程序需向插件传递 snake_case usage 字段。
