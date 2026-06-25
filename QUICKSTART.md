# 快速开始 - 5分钟安装指南

## 前提条件

- CLIProxyAPI v7.0.0+ 已安装
- Go 1.21+ 
- 对应的CPAMC (管理中心前端)

## 3步快速安装

### 步骤1: 构建并安装插件 (2分钟)

```bash
cd cpa-usage-plugin

# 构建Go插件
./build.sh

# 复制到CLIProxyAPI插件目录 (修改路径为你的实际路径)
sudo cp usage-statistics.so /path/to/cliproxyapi/plugins/
```

### 步骤2: 启用插件 (1分钟)

编辑 CLIProxyAPI 的 `config.yaml`:

```yaml
plugins:
  enabled: true
  configs:
    usage-statistics:
      enabled: true
```

重启 CLIProxyAPI:

```bash
sudo systemctl restart cliproxyapi
# 或者使用你的启动方式
```

### 步骤3: 集成前端 (2分钟)

```bash
# 自动集成脚本
./integrate-frontend.sh

# 按照提示完成剩余步骤（添加路由和导航）
```

## 验证安装

### 1. 检查插件状态

访问管理面板的插件页面，确认 "CPA Usage Statistics" 显示为已启用。

### 2. 测试API

```bash
curl http://localhost:8787/v0/management/plugins/usage-statistics/usage
```

应该返回用量统计数据（初始为空）。

### 3. 访问前端

在管理中心点击"用量统计"菜单，应该能看到统计页面。

## 手动安装 (如果脚本失败)

<details>
<summary>点击展开手动步骤</summary>

### 后端

```bash
# 1. 构建
cd cpa-usage-plugin/go
go build -buildmode=plugin -o ../usage-statistics.so main.go

# 2. 安装
sudo cp ../usage-statistics.so /path/to/cliproxyapi/plugins/

# 3. 配置 (编辑 config.yaml)
plugins:
  enabled: true
  configs:
    usage-statistics:
      enabled: true
```

### 前端

```bash
# 1. 复制组件
cp -r old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/components/usage \
      new_file/Cli-Proxy-API-Management-Center-main/src/components/

# 2. 复制页面
cp old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/pages/UsagePage.tsx \
   new_file/Cli-Proxy-API-Management-Center-main/src/pages/

# 3. 复制工具
cp old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/utils/usage.ts \
   new_file/Cli-Proxy-API-Management-Center-main/src/utils/

# 4. 创建API服务 (见 INSTALL.md)

# 5. 添加路由 (见 INSTALL.md)

# 6. 构建
cd new_file/Cli-Proxy-API-Management-Center-main
npm install
npm run build
```

</details>

## 常见问题

### Q: 插件未加载？

**检查**:
```bash
# 查看插件列表
curl http://localhost:8787/v0/management/plugins

# 查看日志
tail -f /var/log/cliproxyapi.log | grep plugin
```

**解决**: 确认文件权限和路径正确

### Q: 前端显示错误？

**检查**:
- 浏览器控制台有无错误
- API路径是否正确 (`/plugins/usage-statistics/usage`)
- 依赖是否安装完整

**解决**: 运行 `npm install` 并重新构建

### Q: 没有数据显示？

**原因**: 刚安装时没有历史数据

**解决**: 
1. 发送一些测试请求
2. 或导入旧版本的数据：
```bash
curl -X POST http://localhost:8787/v0/management/plugins/usage-statistics/usage/import \
  -H "Content-Type: application/json" \
  -d @old-usage-export.json
```

## 下一步

- 📖 阅读 [INSTALL.md](INSTALL.md) 了解详细配置
- 🎨 查看 [VISUAL_CONSISTENCY.md](VISUAL_CONSISTENCY.md) 确保视觉一致
- 📊 查看 [PROJECT_SUMMARY.md](PROJECT_SUMMARY.md) 了解完整功能

## 需要帮助？

- 查看详细文档: `INSTALL.md`
- 提交问题: https://github.com/router-for-me/CLIProxyAPI/issues
- 查看项目主页: https://github.com/router-for-me/CLIProxyAPI

---

**预计安装时间**: 5-10分钟  
**难度**: ⭐⭐ (中等)  
**状态**: ✅ 生产就绪
