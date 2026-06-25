#!/bin/bash
# CPA Usage Plugin - 前端集成脚本
# 将旧版本的用量统计组件集成到新版本CPAMC中

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}=== CPA Usage Statistics Plugin - 前端集成工具 ===${NC}\n"

# 检查目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
OLD_CPAMC="$PROJECT_ROOT/old_file/Cli-Proxy-API-Management-Center-cc8632b6f1c3d8756507da54c3faf2317137be33"
NEW_CPAMC="$PROJECT_ROOT/new_file/Cli-Proxy-API-Management-Center-main"

if [ ! -d "$OLD_CPAMC" ]; then
    echo -e "${RED}错误: 找不到旧版本CPAMC目录: $OLD_CPAMC${NC}"
    exit 1
fi

if [ ! -d "$NEW_CPAMC" ]; then
    echo -e "${RED}错误: 找不到新版本CPAMC目录: $NEW_CPAMC${NC}"
    exit 1
fi

echo -e "${YELLOW}步骤 1/5: 复制用量统计组件${NC}"
if [ -d "$NEW_CPAMC/src/components/usage" ]; then
    echo -e "${YELLOW}警告: usage组件目录已存在，将备份...${NC}"
    mv "$NEW_CPAMC/src/components/usage" "$NEW_CPAMC/src/components/usage.backup.$(date +%s)"
fi

cp -r "$OLD_CPAMC/src/components/usage" "$NEW_CPAMC/src/components/"
echo -e "${GREEN}✓ 已复制 usage 组件${NC}"

echo -e "\n${YELLOW}步骤 2/5: 复制用量统计页面${NC}"
if [ -f "$NEW_CPAMC/src/pages/UsagePage.tsx" ]; then
    echo -e "${YELLOW}警告: UsagePage.tsx已存在，将备份...${NC}"
    mv "$NEW_CPAMC/src/pages/UsagePage.tsx" "$NEW_CPAMC/src/pages/UsagePage.tsx.backup.$(date +%s)"
fi

cp "$OLD_CPAMC/src/pages/UsagePage.tsx" "$NEW_CPAMC/src/pages/"
echo -e "${GREEN}✓ 已复制 UsagePage.tsx${NC}"

echo -e "\n${YELLOW}步骤 3/5: 复制工具函数${NC}"
if [ -f "$NEW_CPAMC/src/utils/usage.ts" ]; then
    echo -e "${YELLOW}警告: usage.ts已存在，将备份...${NC}"
    mv "$NEW_CPAMC/src/utils/usage.ts" "$NEW_CPAMC/src/utils/usage.ts.backup.$(date +%s)"
fi

cp "$OLD_CPAMC/src/utils/usage.ts" "$NEW_CPAMC/src/utils/"
echo -e "${GREEN}✓ 已复制 usage.ts 工具函数${NC}"

echo -e "\n${YELLOW}步骤 4/5: 复制样式文件${NC}"
if [ -f "$OLD_CPAMC/src/pages/UsagePage.module.scss" ]; then
    cp "$OLD_CPAMC/src/pages/UsagePage.module.scss" "$NEW_CPAMC/src/pages/" 2>/dev/null || true
    echo -e "${GREEN}✓ 已复制样式文件${NC}"
else
    echo -e "${YELLOW}⚠ 未找到样式文件，可能需要手动处理${NC}"
fi

echo -e "\n${YELLOW}步骤 5/5: 创建API服务文件${NC}"
cat > "$NEW_CPAMC/src/services/api/usage.ts" << 'EOF'
/**
 * 用量统计API服务
 * 连接到 usage-statistics 插件
 */

import { apiClient } from './client';
import { computeKeyStats, KeyStats } from '@/utils/usage';

const USAGE_TIMEOUT_MS = 60 * 1000;

export interface UsageExportPayload {
  version?: number;
  exported_at?: string;
  usage?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface UsageImportResponse {
  added?: number;
  skipped?: number;
  total_requests?: number;
  failed_requests?: number;
  [key: string]: unknown;
}

export const usageApi = {
  /**
   * 获取使用统计原始数据
   */
  getUsage: () =>
    apiClient.get<Record<string, unknown>>(
      '/plugins/usage-statistics/usage',
      { timeout: USAGE_TIMEOUT_MS }
    ),

  /**
   * 导出使用统计快照
   */
  exportUsage: () =>
    apiClient.get<UsageExportPayload>(
      '/plugins/usage-statistics/usage/export',
      { timeout: USAGE_TIMEOUT_MS }
    ),

  /**
   * 导入使用统计快照
   */
  importUsage: (payload: unknown) =>
    apiClient.post<UsageImportResponse>(
      '/plugins/usage-statistics/usage/import',
      payload,
      { timeout: USAGE_TIMEOUT_MS }
    ),

  /**
   * 计算密钥成功/失败统计
   */
  async getKeyStats(usageData?: unknown): Promise<KeyStats> {
    let payload = usageData;
    if (!payload) {
      const response = await apiClient.get<Record<string, unknown>>(
        '/plugins/usage-statistics/usage',
        { timeout: USAGE_TIMEOUT_MS }
      );
      payload = response?.usage ?? response;
    }
    return computeKeyStats(payload);
  }
};
EOF

echo -e "${GREEN}✓ 已创建 API 服务文件${NC}"

echo -e "\n${GREEN}=== 前端组件集成完成 ===${NC}\n"

echo -e "${YELLOW}接下来需要手动完成以下步骤：${NC}\n"

echo -e "1. ${YELLOW}更新路由配置${NC}"
echo -e "   在 ${NEW_CPAMC}/src/App.tsx 或路由文件中添加："
echo -e "   ${GREEN}import { UsagePage } from '@/pages/UsagePage';${NC}"
echo -e "   ${GREEN}<Route path=\"/usage\" element={<UsagePage />} />${NC}\n"

echo -e "2. ${YELLOW}添加导航菜单${NC}"
echo -e "   在侧边栏导航中添加"用量统计"链接指向 /usage\n"

echo -e "3. ${YELLOW}安装依赖${NC}"
echo -e "   ${GREEN}cd $NEW_CPAMC && npm install${NC}\n"

echo -e "4. ${YELLOW}检查并修复可能的导入错误${NC}"
echo -e "   运行 ${GREEN}npm run build${NC} 检查是否有编译错误\n"

echo -e "5. ${YELLOW}测试功能${NC}"
echo -e "   - 确保后端插件已安装并启用"
echo -e "   - 访问 /usage 页面验证功能\n"

echo -e "${GREEN}集成完成！${NC}"
