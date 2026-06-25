# 📋 快速参考卡片

## 🎯 项目简介
将 CLIProxyAPI v6.9.49 的用量统计功能作为插件移植到 v7.x 版本

## 📦 文件说明

| 文件 | 用途 | 阅读顺序 |
|-----|------|---------|
| README.md | 项目概述和功能介绍 | ⭐⭐⭐ 首先阅读 |
| QUICKSTART.md | 5分钟快速安装指南 | ⭐⭐⭐ 想快速使用 |
| INSTALL.md | 详细安装步骤和故障排查 | ⭐⭐ 遇到问题时 |
| VISUAL_CONSISTENCY.md | 视觉一致性检查清单 | ⭐ 确保UI一致 |
| PROJECT_SUMMARY.md | 技术细节和实现说明 | ⭐ 深入了解 |
| DELIVERY.md | 项目交付总结 | ⭐ 项目管理 |

## 🚀 3步安装（最简化）

```bash
# 步骤1: 构建
cd cpa-usage-plugin && ./build.sh

# 步骤2: 安装
sudo cp usage-statistics.so /path/to/cliproxyapi/plugins/

# 步骤3: 配置并重启
# 编辑 config.yaml 添加:
#   plugins:
#     enabled: true
#     configs:
#       usage-statistics:
#         enabled: true
sudo systemctl restart cliproxyapi
```

## 📊 API速查

```bash
# 获取用量统计
curl http://localhost:8787/v0/management/plugins/usage-statistics/usage

# 导出数据
curl http://localhost:8787/v0/management/plugins/usage-statistics/usage/export \
  -o backup.json

# 导入数据
curl -X POST http://localhost:8787/v0/management/plugins/usage-statistics/usage/import \
  -H "Content-Type: application/json" \
  -d @backup.json
```

## 🔧 前端集成（自动）

```bash
# 运行自动集成脚本
./integrate-frontend.sh

# 然后按提示完成：
# 1. 添加路由到 App.tsx
# 2. 添加导航菜单项
# 3. 运行 npm install && npm run build
```

## 🐛 常见问题速查

| 问题 | 检查 | 解决 |
|-----|------|-----|
| 插件未加载 | 文件权限、config.yaml | `ls -l plugins/` `tail -f log` |
| API 404 | 插件是否启用 | `curl /v0/management/plugins` |
| 前端空白 | 浏览器控制台 | 检查API路径、依赖 |
| 无数据显示 | 是否有请求 | 发送测试请求 |

## 📈 核心功能清单

- [x] ✅ 实时用量统计收集
- [x] ✅ 按API/模型/时间聚合
- [x] ✅ 请求趋势可视化
- [x] ✅ Token消耗分析
- [x] ✅ 成本计算
- [x] ✅ 数据导入导出
- [x] ✅ 100%视觉一致性

## 🎨 前端组件列表

**页面**: UsagePage.tsx

**统计卡片**:
- StatCards - 总览
- ApiDetailsCard - API详情
- ModelStatsCard - 模型统计
- CredentialStatsCard - 凭证统计
- ServiceHealthCard - 健康状态

**图表**:
- UsageChart - 趋势图
- TokenBreakdownChart - Token分解
- CostTrendChart - 成本趋势
- ChartLineSelector - 线选择器

**其他**:
- PriceSettingsCard - 价格设置
- RequestEventsDetailsCard - 请求详情

## 💡 快速测试

```bash
# 1. 发送100个测试请求
for i in {1..100}; do
  curl http://localhost:8787/v1/chat/completions \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'
done

# 2. 查看统计
curl http://localhost:8787/v0/management/plugins/usage-statistics/usage | jq .

# 3. 访问前端
# 打开浏览器: http://localhost:3000/usage
```

## 📞 获取帮助

1. **查看文档**: 
   - 快速: QUICKSTART.md
   - 详细: INSTALL.md
   - 技术: PROJECT_SUMMARY.md

2. **故障排查**: INSTALL.md 的"故障排查"部分

3. **提交问题**: https://github.com/router-for-me/CLIProxyAPI/issues

4. **查看日志**: `tail -f /var/log/cliproxyapi.log | grep plugin`

## ⚙️ 配置示例

### 最小配置 (config.yaml)

```yaml
plugins:
  enabled: true
  configs:
    usage-statistics:
      enabled: true
```

### 完整配置 (config.yaml)

```yaml
plugins:
  enabled: true
  dir: plugins  # 插件目录
  configs:
    usage-statistics:
      enabled: true
      priority: 0  # 执行优先级
```

## 📊 性能参考

| 指标 | 值 |
|-----|---|
| 内存基础 | ~5MB |
| 每10万请求 | +50MB |
| 用量记录 | <1ms |
| 快照生成 | <100ms |
| API响应 | <500ms |

## 🔐 安全提示

- ✅ 插件运行在沙盒环境
- ✅ API需要管理认证
- ✅ 数据仅存储统计信息
- ⚠️ 导出文件可能包含敏感信息

## 📝 版本兼容

| 组件 | 最低版本 |
|-----|---------|
| CLIProxyAPI | v7.0.0+ |
| Go | 1.21+ |
| Node.js | 16+ |
| 浏览器 | Chrome 90+ |

## 🗺️ 升级路线

- **v1.0** (当前): 基础功能
- **v1.1**: 持久化存储
- **v1.5**: 多实例支持
- **v2.0**: 高级分析

## ✅ 部署检查清单

部署前确认：

- [ ] Go 1.21+ 已安装
- [ ] CLIProxyAPI v7.0.0+ 运行中
- [ ] 有 plugins 目录写权限
- [ ] config.yaml 可编辑
- [ ] 可以重启服务

部署后验证：

- [ ] 插件在管理面板显示
- [ ] API端点可访问
- [ ] 前端页面加载
- [ ] 统计数据收集正常
- [ ] 图表渲染正确

## 🎓 学习路径

1. **初学者**: README.md → QUICKSTART.md
2. **使用者**: QUICKSTART.md → INSTALL.md
3. **开发者**: PROJECT_SUMMARY.md → go/main.go
4. **设计师**: VISUAL_CONSISTENCY.md

## 📚 扩展阅读

- CLIProxyAPI 文档: https://help.router-for.me/
- 插件开发指南: CLIProxyAPI/examples/plugin/
- Go 插件文档: https://pkg.go.dev/plugin

---

**提示**: 保存此文件作为快速参考，所有常用操作都在这里。
