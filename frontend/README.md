# CPA Usage Statistics Plugin - Frontend

这是用量统计插件的前端部分，完全移植自 CLIProxyAPI v6.9.49 的用量统计页面。

## 架构

由于CLIProxyAPI v7.x的插件系统主要关注后端功能，前端部分建议通过以下方式集成：

### 方案1: 作为独立前端页面（推荐）

将用量统计页面作为独立的React应用，通过插件的Resource路由提供：

1. 构建独立的前端bundle
2. 插件注册 Resource 路由指向静态HTML
3. HTML加载打包后的JS，调用插件API

### 方案2: 集成到CPAMC

直接将组件集成到新版本的Cli-Proxy-API-Management-Center中：

1. 复制 `src/components/usage/` 到CPAMC的 `src/components/` 
2. 复制 `src/pages/UsagePage.tsx` 到CPAMC的 `src/pages/`
3. 添加路由配置
4. 更新API服务指向插件端点

## 组件列表

已从旧版本移植的组件（保持视觉一致）：

- `UsagePage.tsx` - 主页面
- `StatCards.tsx` - 统计卡片
- `UsageChart.tsx` - 用量趋势图
- `TokenBreakdownChart.tsx` - Token分解图
- `CostTrendChart.tsx` - 成本趋势图
- `ApiDetailsCard.tsx` - API详情卡片
- `ModelStatsCard.tsx` - 模型统计卡片
- `CredentialStatsCard.tsx` - 凭证统计卡片
- `ServiceHealthCard.tsx` - 服务健康卡片
- `ChartLineSelector.tsx` - 图表线选择器
- `PriceSettingsCard.tsx` - 价格设置卡片

## API端点

插件提供的API端点（与旧版本兼容）：

```
GET  /v0/management/plugins/usage-statistics/usage         - 获取用量数据
GET  /v0/management/plugins/usage-statistics/usage/export  - 导出数据  
POST /v0/management/plugins/usage-statistics/usage/import  - 导入数据
```

## 集成步骤（方案2 - 推荐）

### 1. 复制组件到CPAMC

```bash
# 复制用量统计组件
cp -r old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/components/usage \
      new_file/Cli-Proxy-API-Management-Center-main/src/components/

# 复制用量统计页面
cp old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/pages/UsagePage.tsx \
   new_file/Cli-Proxy-API-Management-Center-main/src/pages/

# 复制工具函数
cp old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33/src/utils/usage.ts \
   new_file/Cli-Proxy-API-Management-Center-main/src/utils/
```

### 2. 更新API服务

在 `new_file/Cli-Proxy-API-Management-Center-main/src/services/api/` 创建 `usage.ts`:

```typescript
import { apiClient } from './client';

export const usageApi = {
  getUsage: () => 
    apiClient.get('/plugins/usage-statistics/usage'),
  
  exportUsage: () => 
    apiClient.get('/plugins/usage-statistics/usage/export'),
  
  importUsage: (payload: unknown) => 
    apiClient.post('/plugins/usage-statistics/usage/import', payload),
};
```

### 3. 添加路由

在 `App.tsx` 或路由配置文件中添加：

```typescript
import { UsagePage } from '@/pages/UsagePage';

// 在路由配置中添加
<Route path="/usage" element={<UsagePage />} />
```

### 4. 添加导航菜单

在侧边栏或导航中添加"用量统计"链接。

## 构建

```bash
cd new_file/Cli-Proxy-API-Management-Center-main
npm install
npm run build
```

## 注意事项

1. **样式兼容**: 组件使用CSS Modules，需确保CPAMC支持
2. **依赖检查**: 确保Chart.js等依赖已安装
3. **类型定义**: 可能需要调整部分TypeScript类型定义
4. **API路径**: API基础路径从 `/usage` 改为 `/plugins/usage-statistics/usage`

## 视觉一致性

所有组件保持与旧版本完全一致的视觉显示：
- 相同的图表样式和配色
- 相同的卡片布局
- 相同的交互逻辑
- 相同的数据格式
