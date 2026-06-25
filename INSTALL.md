# CPA Usage Statistics Plugin - 完整安装指南

## 概述

将 CLIProxyAPI v6.9.49 的用量统计功能作为插件移植到 v7.x 版本，保持完全一致的视觉显示和功能。

## 系统要求

- CLIProxyAPI v7.0.0+
- Go 1.21+
- Node.js 16+ (用于前端构建)
- CPAMC (Cli-Proxy-API-Management-Center) 最新版

## 快速开始

### 方法1: 使用自动化脚本（推荐）

```bash
# 1. 构建后端插件
cd cpa-usage-plugin
./build.sh

# 2. 安装后端插件
sudo cp usage-statistics.so /path/to/cliproxyapi/plugins/

# 3. 配置插件
# 编辑 CLIProxyAPI 的 config.yaml
plugins:
  enabled: true
  configs:
    usage-statistics:
      enabled: true
      priority: 0

# 4. 集成前端
./integrate-frontend.sh

# 5. 重启服务
sudo systemctl restart cliproxyapi  # 或你的启动方式
```

### 方法2: 手动安装

#### 步骤1: 构建后端插件

```bash
cd cpa-usage-plugin/go
go build -buildmode=plugin -o ../usage-statistics.so main.go
```

#### 步骤2: 安装插件

```bash
# 复制插件到 CLIProxyAPI 的 plugins 目录
cp usage-statistics.so /path/to/cliproxyapi/plugins/

# 或者设置自定义插件目录
# 在 config.yaml 中配置
plugins:
  enabled: true
  dir: /custom/plugins/path
  configs:
    usage-statistics:
      enabled: true
```

#### 步骤3: 配置启用插件

编辑 CLIProxyAPI 的 `config.yaml`：

```yaml
plugins:
  enabled: true
  dir: plugins  # 插件目录，默认为 plugins
  configs:
    usage-statistics:
      enabled: true
      priority: 0  # 优先级，数值越大越先执行
```

#### 步骤4: 集成前端组件

```bash
# 复制组件
cp -r old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/components/usage \
      new_file/Cli-Proxy-API-Management-Center-main/src/components/

# 复制页面
cp old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/pages/UsagePage.tsx \
   new_file/Cli-Proxy-API-Management-Center-main/src/pages/

# 复制工具
cp old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/utils/usage.ts \
   new_file/Cli-Proxy-API-Management-Center-main/src/utils/

# 复制样式
cp old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/pages/UsagePage.module.scss \
   new_file/Cli-Proxy-API-Management-Center-main/src/pages/ 2>/dev/null || true
```

#### 步骤5: 创建API服务

在 `new_file/Cli-Proxy-API-Management-Center-main/src/services/api/usage.ts` 创建：

```typescript
import { apiClient } from './client';

export const usageApi = {
  getUsage: () => apiClient.get('/plugins/usage-statistics/usage'),
  exportUsage: () => apiClient.get('/plugins/usage-statistics/usage/export'),
  importUsage: (payload: unknown) => 
    apiClient.post('/plugins/usage-statistics/usage/import', payload),
};
```

#### 步骤6: 添加路由

在 `new_file/Cli-Proxy-API-Management-Center-main/src/App.tsx` 中：

```typescript
import { UsagePage } from '@/pages/UsagePage';

// 在路由配置中添加
<Route path="/usage" element={<UsagePage />} />
```

#### 步骤7: 添加导航

在侧边栏或导航菜单中添加链接：

```typescript
{
  path: '/usage',
  name: '用量统计',
  icon: <ChartIcon />
}
```

#### 步骤8: 构建前端

```bash
cd new_file/Cli-Proxy-API-Management-Center-main
npm install
npm run build
```

#### 步骤9: 重启服务

```bash
# 重启 CLIProxyAPI
sudo systemctl restart cliproxyapi

# 或使用你的启动方式
# ./cli-proxy-api --config config.yaml
```

## 验证安装

### 检查插件状态

1. 访问管理面板的插件页面
2. 确认 "CPA Usage Statistics" 插件显示为已启用
3. 状态应显示为 "effective"

### 测试API端点

```bash
# 获取用量统计
curl http://localhost:8787/v0/management/plugins/usage-statistics/usage

# 导出数据
curl http://localhost:8787/v0/management/plugins/usage-statistics/usage/export

# 测试导入（使用导出的数据）
curl -X POST http://localhost:8787/v0/management/plugins/usage-statistics/usage/import \
  -H "Content-Type: application/json" \
  -d @exported-usage.json
```

### 访问前端页面

1. 打开管理中心
2. 点击侧边栏的"用量统计"菜单
3. 应该看到与旧版本一致的统计面板：
   - 总请求数、成功率、Token消耗统计卡片
   - 请求量和Token趋势图表
   - Token分解图和成本趋势图
   - API详情、模型统计、凭证统计卡片
   - 服务健康状态
   - 价格设置面板

## 功能验证清单

- [ ] 后端插件成功加载
- [ ] 用量数据正确收集
- [ ] 统计卡片显示正确
- [ ] 趋势图表正常渲染
- [ ] 时间范围筛选功能正常
- [ ] 图表线选择器工作正常
- [ ] 导出功能正常
- [ ] 导入功能正常
- [ ] 价格设置可以保存
- [ ] 成本计算正确
- [ ] 视觉显示与旧版本一致

## 故障排查

### 插件未加载

```bash
# 检查插件文件权限
ls -l /path/to/cliproxyapi/plugins/usage-statistics.so

# 查看 CLIProxyAPI 日志
tail -f /var/log/cliproxyapi.log | grep plugin
```

### 前端编译错误

```bash
# 检查依赖
cd new_file/Cli-Proxy-API-Management-Center-main
npm install

# 检查类型错误
npm run type-check

# 查看详细构建错误
npm run build -- --verbose
```

### API调用失败

```bash
# 检查插件API是否注册
curl http://localhost:8787/v0/management/plugins

# 检查认证
curl -H "Authorization: Bearer YOUR_TOKEN" \
     http://localhost:8787/v0/management/plugins/usage-statistics/usage
```

### 数据不显示

1. 检查是否有请求通过代理
2. 确认插件的 UsagePlugin 接口正常工作
3. 查看浏览器控制台是否有错误
4. 检查API响应格式是否正确

## 卸载

```bash
# 1. 停止服务
sudo systemctl stop cliproxyapi

# 2. 删除插件文件
rm /path/to/cliproxyapi/plugins/usage-statistics.so

# 3. 从配置中移除
# 编辑 config.yaml，删除 usage-statistics 配置

# 4. 删除前端组件（可选）
rm -rf new_file/Cli-Proxy-API-Management-Center-main/src/components/usage
rm new_file/Cli-Proxy-API-Management-Center-main/src/pages/UsagePage.tsx

# 5. 重新构建前端
cd new_file/Cli-Proxy-API-Management-Center-main
npm run build

# 6. 重启服务
sudo systemctl start cliproxyapi
```

## 性能优化

### 内存使用

插件在内存中存储所有用量数据。如果数据量大：

1. 定期导出并清空数据
2. 考虑实现数据持久化（未来版本）
3. 设置数据保留策略

### 响应时间

对于大量数据：

1. 使用时间范围筛选减少数据量
2. 考虑后台异步加载
3. 实现数据分页（未来版本）

## 升级

从旧版本升级：

```bash
# 1. 导出旧版本数据
curl http://old-server/v0/management/usage/export > usage-backup.json

# 2. 安装新插件（按上述步骤）

# 3. 导入数据
curl -X POST http://new-server/v0/management/plugins/usage-statistics/usage/import \
  -H "Content-Type: application/json" \
  -d @usage-backup.json
```

## 开发

### 修改后端插件

```bash
cd cpa-usage-plugin/go
# 编辑 main.go
go build -buildmode=plugin -o ../usage-statistics.so main.go
# 重启 CLIProxyAPI
```

### 修改前端组件

```bash
cd new_file/Cli-Proxy-API-Management-Center-main
# 编辑组件
npm run dev  # 开发模式
npm run build  # 生产构建
```

## 支持

如遇问题：

1. 查看日志: `/var/log/cliproxyapi.log`
2. 检查配置: `config.yaml`
3. 提交Issue: https://github.com/router-for-me/CLIProxyAPI/issues

## 许可证

MIT License - 与 CLIProxyAPI 相同

## 致谢

基于 CLIProxyAPI v6.9.49 的用量统计功能移植。
