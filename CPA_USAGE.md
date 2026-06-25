# 在 CPA 中使用

## 1. 下载插件

从 GitHub Actions 的 `Build Plugin` workflow 下载 `usage-statistics-plugin` artifact，解压得到：

```text
usage-statistics.so
```

## 2. 放入插件目录

将 `usage-statistics.so` 放到 CPA/CLIProxyAPI 的插件目录中。示例：

```bash
mkdir -p /opt/cliproxyapi/plugins
cp usage-statistics.so /opt/cliproxyapi/plugins/
chmod 755 /opt/cliproxyapi/plugins/usage-statistics.so
```

实际目录以你的 CPA 配置为准。

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
