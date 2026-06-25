# 视觉一致性指南

本文档确保移植后的用量统计页面与旧版本(v6.9.49)视觉显示完全一致。

## 核心原则

**保持100%视觉一致性** - 用户不应感觉到任何差异

## 组件对照表

| 旧版本组件 | 新版本位置 | 视觉要求 | 状态 |
|----------|----------|---------|-----|
| UsagePage.tsx | src/pages/UsagePage.tsx | 完全一致 | ✅ 已复制 |
| StatCards.tsx | src/components/usage/StatCards.tsx | 卡片布局、数值格式、图标 | ✅ 已复制 |
| UsageChart.tsx | src/components/usage/UsageChart.tsx | Chart.js配置、颜色、坐标轴 | ✅ 已复制 |
| TokenBreakdownChart.tsx | src/components/usage/TokenBreakdownChart.tsx | 堆叠柱状图样式 | ✅ 已复制 |
| CostTrendChart.tsx | src/components/usage/CostTrendChart.tsx | 双Y轴、面积图 | ✅ 已复制 |
| ApiDetailsCard.tsx | src/components/usage/ApiDetailsCard.tsx | 表格样式、排序 | ✅ 已复制 |
| ModelStatsCard.tsx | src/components/usage/ModelStatsCard.tsx | 模型列表样式 | ✅ 已复制 |
| CredentialStatsCard.tsx | src/components/usage/CredentialStatsCard.tsx | 凭证统计样式 | ✅ 已复制 |
| ServiceHealthCard.tsx | src/components/usage/ServiceHealthCard.tsx | 健康指示器 | ✅ 已复制 |
| ChartLineSelector.tsx | src/components/usage/ChartLineSelector.tsx | 多选框、标签 | ✅ 已复制 |
| PriceSettingsCard.tsx | src/components/usage/PriceSettingsCard.tsx | 价格输入框 | ✅ 已复制 |
| RequestEventsDetailsCard.tsx | src/components/usage/RequestEventsDetailsCard.tsx | 事件列表 | ✅ 已复制 |

## 样式检查清单

### 1. 页面布局

- [ ] 顶部工具栏：标题 + 时间范围选择器 + 导出/导入/刷新按钮
- [ ] 统计卡片：4-5个横向排列的卡片（响应式）
- [ ] 图表选择器：模型筛选多选框
- [ ] 服务健康：独立卡片显示状态
- [ ] 图表网格：2列布局（请求趋势 + Token趋势）
- [ ] Token分解图：全宽堆叠柱状图
- [ ] 成本趋势图：全宽双Y轴图表
- [ ] 详情网格：2列布局（API详情 + 模型统计）
- [ ] 请求事件详情：全宽表格
- [ ] 凭证统计：全宽卡片
- [ ] 价格设置：全宽表单

### 2. 颜色方案

#### 统计卡片
- 总请求数：蓝色 `#3b82f6`
- 成功率：绿色 `#10b981`
- 失败数：红色 `#ef4444`
- Token消耗：紫色 `#8b5cf6`
- 成本：橙色 `#f59e0b`

#### 图表颜色
```javascript
// Chart.js 配置保持一致
colors: [
  'rgb(59, 130, 246)',   // blue
  'rgb(16, 185, 129)',   // green
  'rgb(245, 158, 11)',   // amber
  'rgb(139, 92, 246)',   // violet
  'rgb(239, 68, 68)',    // red
  'rgb(236, 72, 153)',   // pink
  'rgb(14, 165, 233)',   // sky
  'rgb(34, 197, 94)',    // emerald
  'rgb(251, 146, 60)',   // orange
]
```

#### Token类型颜色
- Input Tokens: `rgba(59, 130, 246, 0.8)` (蓝色)
- Output Tokens: `rgba(16, 185, 129, 0.8)` (绿色)
- Reasoning Tokens: `rgba(139, 92, 246, 0.8)` (紫色)
- Cached Tokens: `rgba(245, 158, 11, 0.8)` (琥珀色)

### 3. 字体和间距

```scss
// 保持与旧版本一致
$font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', 'Oxygen', 'Ubuntu';
$font-size-base: 14px;
$font-size-large: 16px;
$font-size-small: 12px;

$spacing-xs: 4px;
$spacing-sm: 8px;
$spacing-md: 16px;
$spacing-lg: 24px;
$spacing-xl: 32px;
```

### 4. 卡片样式

```scss
.card {
  background: #ffffff;
  border-radius: 8px;
  padding: 20px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
  
  &.dark {
    background: #1f2937;
    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3);
  }
}
```

### 5. 按钮样式

```scss
.button {
  padding: 8px 16px;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  
  &.primary {
    background: #3b82f6;
    color: white;
  }
  
  &.secondary {
    background: #e5e7eb;
    color: #374151;
  }
}
```

### 6. 表格样式

```scss
.table {
  width: 100%;
  border-collapse: collapse;
  
  th {
    text-align: left;
    padding: 12px;
    background: #f9fafb;
    font-weight: 600;
    font-size: 12px;
    text-transform: uppercase;
    color: #6b7280;
  }
  
  td {
    padding: 12px;
    border-top: 1px solid #e5e7eb;
  }
}
```

## Chart.js 配置一致性

### 折线图配置

```javascript
{
  responsive: true,
  maintainAspectRatio: false,
  interaction: {
    mode: 'index',
    intersect: false,
  },
  plugins: {
    legend: {
      display: true,
      position: 'bottom',
    },
    tooltip: {
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      padding: 12,
      titleFont: { size: 14 },
      bodyFont: { size: 13 },
    },
  },
  scales: {
    x: {
      grid: {
        display: false,
      },
    },
    y: {
      beginAtZero: true,
      grid: {
        color: 'rgba(0, 0, 0, 0.05)',
      },
    },
  },
}
```

### 柱状图配置

```javascript
{
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      display: true,
      position: 'bottom',
    },
  },
  scales: {
    x: {
      stacked: true,
      grid: {
        display: false,
      },
    },
    y: {
      stacked: true,
      beginAtZero: true,
    },
  },
}
```

## 数据格式一致性

### API响应格式

确保插件返回的数据格式与旧版本完全一致：

```typescript
interface UsageResponse {
  usage: {
    total_requests: number;
    success_count: number;
    failure_count: number;
    total_tokens: number;
    apis: Record<string, APISnapshot>;
    requests_by_day: Record<string, number>;
    requests_by_hour: Record<string, number>;
    tokens_by_day: Record<string, number>;
    tokens_by_hour: Record<string, number>;
  };
  failed_requests: number;
}
```

### 时间格式

- 日期: `YYYY-MM-DD` (e.g., `2026-06-25`)
- 小时: `HH` (e.g., `09`, `14`)
- 时间戳: ISO 8601 (e.g., `2026-06-25T09:30:00Z`)

### 数值格式

- 请求数: 整数，千位分隔符 (e.g., `1,234`)
- Token数: 整数，千位分隔符 (e.g., `1,234,567`)
- 成本: 小数点后4位 (e.g., `$0.1234`)
- 百分比: 小数点后2位 (e.g., `99.12%`)
- 延迟: 毫秒 (e.g., `1,234 ms`)

## 响应式设计

### 断点

```scss
$breakpoint-mobile: 768px;
$breakpoint-tablet: 1024px;
$breakpoint-desktop: 1280px;

@media (max-width: $breakpoint-mobile) {
  // 移动端：单列布局
  .stats-grid { grid-template-columns: 1fr; }
  .charts-grid { grid-template-columns: 1fr; }
}

@media (min-width: $breakpoint-tablet) {
  // 平板：2列布局
  .stats-grid { grid-template-columns: repeat(2, 1fr); }
  .charts-grid { grid-template-columns: repeat(2, 1fr); }
}

@media (min-width: $breakpoint-desktop) {
  // 桌面：多列布局
  .stats-grid { grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); }
}
```

## 交互一致性

### 1. 时间范围筛选
- 选项：全部 / 7小时 / 24小时 / 7天
- 默认：24小时
- 持久化到 localStorage

### 2. 图表线选择
- 多选模型
- 最多9条线
- 默认：显示所有
- 持久化到 localStorage

### 3. 导出/导入
- 导出格式：JSON
- 文件名：`cpa-usage-export-YYYY-MM-DD.json`
- 导入：自动合并，去重

### 4. 价格设置
- 实时保存到 localStorage
- 输入格式：数字，最多6位小数
- 货币符号：$ (美元)

## 暗色主题

确保支持暗色主题并保持一致：

```scss
.dark {
  .card {
    background: #1f2937;
    color: #f9fafb;
  }
  
  .table {
    th {
      background: #374151;
      color: #9ca3af;
    }
    
    td {
      border-color: #374151;
    }
  }
  
  // Chart.js 暗色主题配置
  --chart-grid-color: rgba(255, 255, 255, 0.1);
  --chart-text-color: #d1d5db;
}
```

## 验证步骤

### 视觉对比

1. 并排打开旧版本和新版本页面
2. 逐一对比每个组件：
   - 布局位置
   - 尺寸比例
   - 颜色方案
   - 字体样式
   - 间距
   - 边框圆角
   - 阴影效果

### 功能测试

1. [ ] 页面加载显示正确
2. [ ] 统计卡片数值正确
3. [ ] 时间范围筛选工作正常
4. [ ] 图表渲染正确
5. [ ] 图表交互（悬停、缩放）正常
6. [ ] 图表线选择器工作正常
7. [ ] 导出功能生成正确文件
8. [ ] 导入功能正确合并数据
9. [ ] 价格设置保存并计算正确
10. [ ] 响应式布局在不同屏幕正常
11. [ ] 暗色主题显示正确
12. [ ] 数据刷新正常

### 截图对比

建议使用工具进行像素级对比：
- Percy (视觉回归测试)
- BackstopJS
- 或手动截图对比

## 常见问题

### Q: 颜色略有不同？
A: 检查CSS变量、主题配置、Chart.js颜色数组

### Q: 布局不对齐？
A: 检查CSS Grid/Flexbox配置、间距变量

### Q: 字体不一致？
A: 确保font-family完全相同，检查font-weight

### Q: 图表大小不对？
A: 检查容器高度、maintainAspectRatio配置

### Q: 移动端显示问题？
A: 检查响应式断点、媒体查询

## 最终检查清单

部署前必须确认：

- [ ] 所有组件已从旧版本正确复制
- [ ] 所有样式文件已复制或重新创建
- [ ] API端点路径已更新
- [ ] Chart.js配置与旧版本一致
- [ ] 颜色方案完全匹配
- [ ] 字体和间距一致
- [ ] 响应式布局正常
- [ ] 暗色主题正常
- [ ] 所有交互功能正常
- [ ] 数据格式兼容
- [ ] 并排对比无明显差异
- [ ] 用户反馈确认一致性

## 维护建议

1. **文档化任何偏差** - 如果必须有细微差异，记录原因
2. **定期对比** - 在更新时重新验证一致性
3. **用户反馈** - 收集用户对视觉变化的反馈
4. **版本控制** - 保留旧版本组件作为参考

---

**重要提醒**: 视觉一致性是这个迁移项目的核心要求。任何视觉差异都应该被视为bug并修复。
