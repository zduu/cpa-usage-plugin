# 在 CPA 中使用

## 1. 下载插件

从 [GitHub Releases](https://github.com/zduu/cpa-usage-plugin/releases) 下载最新版本的 `usage-statistics.so`。

> 也可以从 GitHub Actions 的 `Build Plugin` workflow 下载 `usage-statistics-plugin` artifact 自行构建。

## 2. 放入插件目录

### 直接部署

将 `usage-statistics.so` 放到 CPA/CLIProxyAPI 的插件目录中。示例：

```bash
mkdir -p /opt/cliproxyapi/plugins
cp usage-statistics.so /opt/cliproxyapi/plugins/
chmod 755 /opt/cliproxyapi/plugins/usage-statistics.so
```

实际目录以你的 CPA 配置为准。

### Docker 部署

如果 CPA 以 Docker 方式运行，通过 volume 将插件挂载到容器内：

```yaml
# docker-compose.yml 示例
version: "3"
services:
  cliproxyapi:
    image: your-cpa-image:tag
    ports:
      - "8787:8787"
    volumes:
      - ./plugins:/app/plugins        # 将插件目录挂载到容器
      - ./config:/app/config
      - ./data:/app/data
```

或将宿主目录映射到容器：

```bash
docker run -d \
  --name cliproxyapi \
  -v /opt/cliproxyapi/plugins:/app/plugins \
  -v /opt/cliproxyapi/config:/app/config \
  -p 8787:8787 \
  your-cpa-image:tag
```

> 插件目录路径（`/app/plugins`）需与 CPA 容器内的实际路径一致，请参考你的 CPA Docker 镜像文档。

## 3. 启用插件

在 CPA 配置中启用插件系统，并启用 `usage-statistics`：

```yaml
plugins:
  enabled: true
  configs:
    usage-statistics:
      enabled: true
```

然后重启 CPA 服务。

## 4. 查看统计

登录 CPA 管理端，在菜单中打开“用量统计”。

页面包含：

- 总请求数、成功/失败、总 token、估算花费。
- 服务健康监测：鼠标悬停网格查看 15 分钟窗口信息，灰色格表示无请求。
- 接口详细统计：点击接口行查看该接口的模型分布、凭证/来源分布、错误统计和最近请求。
- 模型统计、凭证统计、请求事件明细。
- 当前接口明细和全量事件的 CSV/JSON 导出。

## 5. 数据导入导出

页面右上角可导入/导出统计数据。

也可以使用管理接口：

```bash
curl http://127.0.0.1:8787/v0/management/plugins/usage-statistics/usage/export
curl -X POST http://127.0.0.1:8787/v0/management/plugins/usage-statistics/usage/import \
  -H 'Content-Type: application/json' \
  --data-binary @usage-export.json
```

## 注意

- 当前统计存储在插件进程内存中，CPA 重启前请先导出数据。
- 多实例部署时，每个实例独立统计。
- token 是否完整取决于上游返回的 usage 信息；CPA 主程序需向插件传递 snake_case usage 字段。
