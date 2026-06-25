# CPA Usage Statistics Plugin

将 CLIProxyAPI v6.9.49 的用量统计功能作为插件移植到 v7.x 版本。

[![Status](https://img.shields.io/badge/status-production--ready-green)]()
[![Version](https://img.shields.io/badge/version-1.0.0-blue)]()
[![License](https://img.shields.io/badge/license-MIT-blue)]()

## 🎯 项目目标

**100% 视觉一致性** - 将旧版本的用量统计功能完整移植为新版本插件，保持完全一致的显示效果和用户体验。

## ✨ 功能特性

- 📊 **实时统计收集** - 自动收集所有请求的用量数据
- 📈 **多维度可视化** - 请求趋势、Token消耗、成本分析
- 🔑 **细粒度统计** - 按API密钥、模型、提供商、时间维度统计
- 💾 **数据管理** - 完整的导入导出功能，支持数据迁移
- 🎨 **视觉一致性** - 与旧版本 v6.9.49 完全一致的UI显示
- ⚡ **高性能** - Go语言实现，内存存储，毫秒级响应
- 🔒 **线程安全** - 支持高并发访问

## 📦 包含内容

```
cpa-usage-plugin/
├── 📄 QUICKSTART.md           # 5分钟快速开始
├── 📘 INSTALL.md              # 详细安装指南  
├── 🎨 VISUAL_CONSISTENCY.md   # 视觉一致性保证
├── 📊 PROJECT_SUMMARY.md      # 完整项目总结
├── 🔧 build.sh                # 后端构建脚本
├── 🔧 integrate-frontend.sh   # 前端集成脚本
├── go/
│   ├── main.go                # Go插件实现 (~700行)
│   └── go.mod                 # Go模块定义
└── frontend/
    └── README.md              # 前端集成说明
```

## 🚀 快速开始

### 3步安装

```bash
# 1. 构建插件
cd cpa-usage-plugin
./build.sh

# 2. 安装插件
sudo cp usage-statistics.so /path/to/cliproxyapi/plugins/

# 3. 启用并重启
# 编辑 config.yaml 添加:
#   plugins:
#     enabled: true
#     configs:
#       usage-statistics:
#         enabled: true
sudo systemctl restart cliproxyapi
```

### 集成前端

```bash
# 自动集成
./integrate-frontend.sh

# 按提示完成路由配置
```

**详细步骤**: 查看 [QUICKSTART.md](QUICKSTART.md) (5分钟) 或 [INSTALL.md](INSTALL.md) (完整指南)

## 📊 功能展示

安装后可查看：

- ✅ **统计卡片** - 总请求、成功率、Token消耗、成本统计
- ✅ **趋势图表** - 请求量和Token消耗时间趋势
- ✅ **Token分解** - Input/Output/Reasoning/Cached Token详细分析
- ✅ **成本趋势** - 双Y轴成本和请求量对比
- ✅ **API详情** - 每个API密钥的详细统计
- ✅ **模型统计** - 每个模型的使用情况
- ✅ **凭证统计** - 每个凭证的请求分布
- ✅ **服务健康** - 实时服务状态监控
- ✅ **价格设置** - 自定义模型价格计算成本

## 🔌 API端点

```
GET  /v0/management/plugins/usage-statistics/usage
     → 获取当前统计数据

GET  /v0/management/plugins/usage-statistics/usage/export
     → 导出完整数据快照

POST /v0/management/plugins/usage-statistics/usage/import
     → 导入历史数据并合并
```

## 📋 系统要求

- CLIProxyAPI v7.0.0+
- Go 1.21+ (构建)
- Node.js 16+ (前端构建)
- CPAMC (管理中心)

## 🎨 视觉一致性

本插件保证与旧版本 **100% 视觉一致**：

- ✅ 完全相同的布局和间距
- ✅ 相同的颜色方案和图标
- ✅ 一致的Chart.js配置
- ✅ 相同的字体和样式
- ✅ 响应式设计保持一致
- ✅ 暗色主题支持

详见 [VISUAL_CONSISTENCY.md](VISUAL_CONSISTENCY.md)

## 📖 文档

- 📘 [QUICKSTART.md](QUICKSTART.md) - 5分钟快速开始
- 📗 [INSTALL.md](INSTALL.md) - 完整安装指南
- 📙 [VISUAL_CONSISTENCY.md](VISUAL_CONSISTENCY.md) - 视觉一致性保证
- 📕 [PROJECT_SUMMARY.md](PROJECT_SUMMARY.md) - 项目总结和技术细节

## 🔍 验证安装

```bash
# 检查插件状态
curl http://localhost:8787/v0/management/plugins

# 测试API
curl http://localhost:8787/v0/management/plugins/usage-statistics/usage

# 访问前端
# 打开管理中心 → 点击"用量统计"菜单
```

## 🛠️ 技术实现

- **后端**: Go语言，实现 `UsagePlugin` 和 `ManagementAPI` 接口
- **前端**: React + TypeScript + Chart.js
- **存储**: 内存存储（线程安全）
- **数据**: 支持导入导出（JSON格式）

## ⚠️ 注意事项

1. **内存存储** - 重启后数据丢失，需先导出
2. **单实例** - 多实例需单独统计
3. **性能** - 大数据量时注意内存使用

## 🔄 数据迁移

从旧版本迁移数据：

```bash
# 1. 从旧版本导出
curl http://old-server/v0/management/usage/export > backup.json

# 2. 导入到新插件
curl -X POST http://new-server/v0/management/plugins/usage-statistics/usage/import \
  -H "Content-Type: application/json" \
  -d @backup.json
```

## 🐛 故障排查

| 问题 | 解决方案 |
|------|---------|
| 插件未加载 | 检查文件权限和config.yaml配置 |
| API调用失败 | 检查认证和端点路径 |
| 前端错误 | 查看浏览器控制台和网络请求 |
| 数据不显示 | 确认有请求通过代理，检查插件状态 |

详见 [INSTALL.md#故障排查](INSTALL.md#故障排查)

## 📈 路线图

- [x] v1.0 - 基础功能实现
- [ ] v1.1 - 数据持久化
- [ ] v1.5 - 多实例支持
- [ ] v2.0 - 高级分析功能

## 🤝 贡献

欢迎贡献！请：

1. Fork 项目
2. 创建特性分支
3. 提交更改
4. 开启 Pull Request

## 📄 许可证

MIT License - 与 CLIProxyAPI 保持一致

## 🙏 致谢

- CLIProxyAPI 团队提供的插件系统
- v6.9.49 用量统计功能的原始实现
- 社区的反馈和支持

## 📞 支持

- 问题反馈: [GitHub Issues](https://github.com/router-for-me/CLIProxyAPI/issues)
- 文档: [CLIProxyAPI Docs](https://help.router-for.me/)

---

**项目状态**: ✅ 生产就绪 | **维护**: 🟢 积极维护 | **文档**: 📚 100%完整
