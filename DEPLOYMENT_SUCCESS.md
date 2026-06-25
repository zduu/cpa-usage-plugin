# 部署成功报告

## 📦 部署信息

- **目标服务器**: mine (通过跳板机 me)
- **安装路径**: `/home/zoex/docker/CLIProxyAPI`
- **部署方式**: Docker容器
- **部署时间**: 2026-06-25
- **插件版本**: usage-statistics v1.0

## ✅ 部署状态

### 后端插件
- [x] 插件文件已安装：`/home/zoex/docker/CLIProxyAPI/plugins/usage-statistics.so` (8.9M)
- [x] 配置文件已更新：`config.yaml` 中启用插件
- [x] Docker容器已重启：`cli-proxy-api`
- [x] 插件成功加载：日志显示 "pluginhost: plugin loaded"
- [x] 数据文件已创建：
  - `/home/zoex/docker/CLIProxyAPI/data/usage-statistics.json` (33K)
  - `/home/zoex/docker/CLIProxyAPI/data/usage-statistics-prices.json` (2B)

### 配置详情
```yaml
plugins:
  enabled: true
  configs:
    usage-statistics:
      enabled: true
      priority: 1
      persist_path: "/CLIProxyAPI/data/usage-statistics.json"
      flush_interval_seconds: 30
      max_details_per_model: 5000
      redact_api_keys: true
      prices_path: "/CLIProxyAPI/data/usage-statistics-prices.json"
```

### 服务信息
- **容器名称**: `cli-proxy-api`
- **镜像**: `eceasy/cli-proxy-api:latest`
- **端口映射**: `0.0.0.0:8317->8317/tcp`
- **运行状态**: Up (运行中)

## 📊 插件功能

### API端点
插件提供以下管理API端点（需要管理密钥认证）：

```bash
# 获取用量统计
GET /v0/management/plugins/usage-statistics/usage

# 导出用量数据
GET /v0/management/plugins/usage-statistics/usage/export

# 导入用量数据
POST /v0/management/plugins/usage-statistics/usage/import
```

### 数据持久化
- 自动持久化到 JSON 文件
- 每 30 秒刷新一次
- 支持导出/导入备份

### 统计功能
- 请求计数和成功率
- Token消耗统计
- 模型使用统计
- API密钥统计
- 成本计算（基于价格配置）

## 🔍 验证步骤

### 1. 检查插件加载
```bash
ssh -J me mine "docker logs cli-proxy-api 2>&1 | grep -i 'plugin loaded'"
```
✅ 输出显示：`pluginhost: plugin loaded`

### 2. 检查数据文件
```bash
ssh -J me mine "ls -lh /home/zoex/docker/CLIProxyAPI/data/usage-statistics*"
```
✅ 文件存在且有数据

### 3. 检查容器状态
```bash
ssh -J me mine "cd /home/zoex/docker/CLIProxyAPI && docker compose ps"
```
✅ 容器运行正常

### 4. 访问API（需要配置管理密钥）
```bash
# 需要在请求头中添加管理密钥
curl -H "Authorization: Bearer YOUR_MANAGEMENT_KEY" \
     http://localhost:8317/v0/management/plugins/usage-statistics/usage
```

## 🎯 下一步操作

### 1. 配置管理密钥（如果尚未配置）
在 `config.yaml` 中添加或确认管理密钥：
```yaml
management:
  key: "your-secure-management-key"
```

### 2. 集成前端（可选）
如果需要在CPAMC管理面板中显示用量统计：

```bash
# 在本地执行
cd /Users/zhouzhou/Downloads/test/test/cpa-usage-plugin
./integrate-frontend.sh
```

然后将前端部署到远程服务器。

### 3. 配置价格（可选）
编辑 `/home/zoex/docker/CLIProxyAPI/data/usage-statistics-prices.json` 添加模型价格：
```json
{
  "gpt-4": {
    "prompt": 0.03,
    "completion": 0.06
  },
  "gpt-3.5-turbo": {
    "prompt": 0.0015,
    "completion": 0.002
  }
}
```

### 4. 定期备份数据
```bash
# 导出当前数据
ssh -J me mine "curl -H 'Authorization: Bearer YOUR_KEY' \
  http://localhost:8317/v0/management/plugins/usage-statistics/usage/export \
  > usage-backup-$(date +%Y%m%d).json"
```

## 📝 维护命令

### 重启服务
```bash
ssh -J me mine "cd /home/zoex/docker/CLIProxyAPI && docker compose restart cli-proxy-api"
```

### 查看日志
```bash
# 实时日志
ssh -J me mine "docker logs -f cli-proxy-api"

# 查看插件相关日志
ssh -J me mine "docker logs cli-proxy-api 2>&1 | grep usage-statistics"
```

### 更新插件
```bash
# 使用自动化脚本
cd /Users/zhouzhou/Downloads/test/test/cpa-usage-plugin
./deploy-to-vps.sh me mine /home/zoex/docker/CLIProxyAPI
```

### 检查数据
```bash
# 查看数据文件大小
ssh -J me mine "ls -lh /home/zoex/docker/CLIProxyAPI/data/usage-statistics.json"

# 查看数据内容（前几行）
ssh -J me mine "head -50 /home/zoex/docker/CLIProxyAPI/data/usage-statistics.json"
```

## 🐛 故障排查

### 插件未加载
```bash
# 检查插件文件
ssh -J me mine "ls -l /home/zoex/docker/CLIProxyAPI/plugins/usage-statistics.so"

# 检查配置
ssh -J me mine "grep -A 10 'usage-statistics' /home/zoex/docker/CLIProxyAPI/config.yaml"

# 重启容器
ssh -J me mine "cd /home/zoex/docker/CLIProxyAPI && docker compose restart cli-proxy-api"
```

### API返回错误
- 错误 "missing management key"：需要配置管理密钥
- 错误 404：检查插件是否正确加载
- 错误 500：查看容器日志检查错误

### 数据丢失
插件数据已持久化到文件，容器重启不会丢失数据。如果数据丢失：
1. 检查挂载卷配置
2. 从备份恢复
3. 检查文件权限

## 📞 支持

如遇问题，请查看：
- 项目文档：`INSTALL.md`, `REMOTE_DEPLOY.md`
- API参考：`REFERENCE.md`
- CLIProxyAPI文档：https://github.com/router-for-me/CLIProxyAPI

---

**部署成功！** 🎉

插件已在远程VPS上正常运行，开始收集用量统计数据。
