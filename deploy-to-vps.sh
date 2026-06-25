#!/bin/bash
# CPA Usage Plugin - 远程VPS部署脚本
# 支持通过跳板机部署

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# 默认配置
DEFAULT_CPA_PATH="/opt/CLIProxyAPI"
DEFAULT_JUMP_HOST=""
DEFAULT_TARGET_HOST=""

# 解析参数
JUMP_HOST="${JUMP_HOST:-$1}"
TARGET_HOST="${TARGET_HOST:-$2}"
CPA_PATH="${CPA_PATH:-${3:-$DEFAULT_CPA_PATH}}"

usage() {
    echo "用法: $0 [跳板机] <目标主机> [CLIProxyAPI路径]"
    echo ""
    echo "示例:"
    echo "  $0 target-host                          # 直接连接"
    echo "  $0 jump-host target-host                # 通过跳板机"
    echo "  $0 jump-host target-host /opt/cpa       # 指定路径"
    echo ""
    echo "环境变量:"
    echo "  JUMP_HOST      跳板机主机名"
    echo "  TARGET_HOST    目标服务器主机名"
    echo "  CPA_PATH       CLIProxyAPI安装路径 (默认: $DEFAULT_CPA_PATH)"
    exit 1
}

# 参数处理
if [ -z "$TARGET_HOST" ]; then
    if [ -z "$JUMP_HOST" ]; then
        usage
    else
        # 只有一个参数，视为目标主机
        TARGET_HOST="$JUMP_HOST"
        JUMP_HOST=""
    fi
fi

# 构建SSH命令
if [ -n "$JUMP_HOST" ]; then
    SSH_CMD="ssh -J $JUMP_HOST"
    SCP_CMD="scp -J $JUMP_HOST"
    CONNECTION_INFO="$TARGET_HOST (通过 $JUMP_HOST)"
else
    SSH_CMD="ssh"
    SCP_CMD="scp"
    CONNECTION_INFO="$TARGET_HOST"
fi

echo -e "${BLUE}╔════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║${NC}  ${GREEN}CPA Usage Statistics Plugin - 远程部署工具${NC}       ${BLUE}║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${YELLOW}目标服务器:${NC} $CONNECTION_INFO"
echo -e "${YELLOW}安装路径:${NC} $CPA_PATH"
echo ""

# 步骤1: 测试连接
echo -e "${BLUE}[1/7]${NC} 测试SSH连接..."
if ! $SSH_CMD $TARGET_HOST "echo 'OK'" > /dev/null 2>&1; then
    echo -e "${RED}✗ SSH连接失败${NC}"
    echo "请检查:"
    echo "  1. SSH配置是否正确"
    echo "  2. 主机名是否正确: $TARGET_HOST"
    [ -n "$JUMP_HOST" ] && echo "  3. 跳板机是否可达: $JUMP_HOST"
    exit 1
fi
echo -e "${GREEN}✓ SSH连接成功${NC}"

# 步骤2: 检查远程环境
echo -e "\n${BLUE}[2/7]${NC} 检查远程环境..."
$SSH_CMD $TARGET_HOST << 'REMOTE_CHECK'
if [ ! -d "/opt/CLIProxyAPI" ] && [ ! -d "/opt/cliproxyapi" ] && [ ! -d "/usr/local/CLIProxyAPI" ]; then
    echo "警告: 未找到常见的CLIProxyAPI安装路径"
    echo "请确认实际路径并使用: $0 [jump] target /path/to/cpa"
fi
REMOTE_CHECK
echo -e "${GREEN}✓ 远程环境检查完成${NC}"

# 步骤3: 本地构建
echo -e "\n${BLUE}[3/7]${NC} 本地构建插件..."
if [ ! -f "go/main.go" ]; then
    echo -e "${RED}✗ 错误: 找不到 go/main.go${NC}"
    echo "请确保在 cpa-usage-plugin 目录下运行此脚本"
    exit 1
fi

if ! command -v go &> /dev/null; then
    echo -e "${RED}✗ 错误: Go未安装${NC}"
    echo "请先安装Go 1.21+"
    exit 1
fi

./build.sh > /dev/null 2>&1 || {
    echo -e "${RED}✗ 构建失败${NC}"
    ./build.sh
    exit 1
}

if [ ! -f "usage-statistics.so" ]; then
    echo -e "${RED}✗ 构建产物不存在${NC}"
    exit 1
fi

FILE_SIZE=$(ls -lh usage-statistics.so | awk '{print $5}')
echo -e "${GREEN}✓ 插件构建成功${NC} (大小: $FILE_SIZE)"

# 步骤4: 传输文件
echo -e "\n${BLUE}[4/7]${NC} 传输插件到远程..."
if ! $SCP_CMD usage-statistics.so $TARGET_HOST:/tmp/usage-statistics.so 2>&1; then
    echo -e "${RED}✗ 文件传输失败${NC}"
    exit 1
fi
echo -e "${GREEN}✓ 文件传输成功${NC}"

# 步骤5: 安装插件
echo -e "\n${BLUE}[5/7]${NC} 安装插件到 $CPA_PATH..."
$SSH_CMD $TARGET_HOST << REMOTE_INSTALL
set -e

# 检查目录
if [ ! -d "$CPA_PATH/plugins" ]; then
    echo "错误: 插件目录不存在: $CPA_PATH/plugins"
    echo "请确认CLIProxyAPI路径是否正确"
    exit 1
fi

# 备份旧版本（如果存在）
if [ -f "$CPA_PATH/plugins/usage-statistics.so" ]; then
    mv "$CPA_PATH/plugins/usage-statistics.so" "$CPA_PATH/plugins/usage-statistics.so.backup.\$(date +%s)"
    echo "已备份旧版本插件"
fi

# 安装新版本
mv /tmp/usage-statistics.so $CPA_PATH/plugins/
chmod 644 $CPA_PATH/plugins/usage-statistics.so

# 验证
ls -lh $CPA_PATH/plugins/usage-statistics.so
REMOTE_INSTALL

echo -e "${GREEN}✓ 插件安装完成${NC}"

# 步骤6: 配置检查
echo -e "\n${BLUE}[6/7]${NC} 检查配置..."
CONFIG_CHECK=$($SSH_CMD $TARGET_HOST "grep -A 3 'usage-statistics' $CPA_PATH/config.yaml 2>/dev/null" || echo "")

if [ -z "$CONFIG_CHECK" ]; then
    echo -e "${YELLOW}⚠ 配置文件中未找到 usage-statistics 配置${NC}"
    echo ""
    echo "请手动编辑 $CPA_PATH/config.yaml，添加:"
    echo ""
    echo -e "${GREEN}plugins:${NC}"
    echo -e "${GREEN}  enabled: true${NC}"
    echo -e "${GREEN}  configs:${NC}"
    echo -e "${GREEN}    usage-statistics:${NC}"
    echo -e "${GREEN}      enabled: true${NC}"
    echo ""
    read -p "配置完成后按回车继续..." -r
else
    echo -e "${GREEN}✓ 已找到插件配置${NC}"
    echo "$CONFIG_CHECK"
fi

# 步骤7: 重启服务
echo -e "\n${BLUE}[7/7]${NC} 重启CLIProxyAPI服务..."
$SSH_CMD $TARGET_HOST << REMOTE_RESTART
set -e

echo "检测部署方式..."

# 检查Docker
if [ -f "$CPA_PATH/docker-compose.yml" ] && command -v docker &> /dev/null; then
    echo "检测到Docker部署，重启容器..."
    cd "$CPA_PATH"
    docker compose restart cli-proxy-api 2>/dev/null || docker-compose restart cli-proxy-api 2>/dev/null || {
        echo "警告: 无法通过docker compose重启"
        echo "尝试docker命令..."
        docker restart cli-proxy-api 2>/dev/null || {
            echo "错误: 无法重启Docker容器"
            exit 1
        }
    }
    sleep 5
    echo "检查容器状态..."
    docker ps | grep cli-proxy-api || true
    echo "✓ Docker容器已重启"
    exit 0
fi

# 检查systemd服务
echo "查找systemd服务..."
SERVICE_NAME=""
if systemctl list-units --type=service --all 2>/dev/null | grep -q cliproxyapi; then
    SERVICE_NAME="cliproxyapi"
elif systemctl list-units --type=service --all 2>/dev/null | grep -q CLIProxyAPI; then
    SERVICE_NAME="CLIProxyAPI"
fi

if [ -n "\$SERVICE_NAME" ]; then
    echo "停止服务 \$SERVICE_NAME..."
    sudo systemctl stop "\$SERVICE_NAME" || {
        echo "警告: 无法停止服务"
    }

    sleep 2

    echo "启动服务 \$SERVICE_NAME..."
    sudo systemctl start "\$SERVICE_NAME" || {
        echo "警告: 无法启动服务"
        echo "请手动启动CLIProxyAPI"
        exit 1
    }

    sleep 3

    echo "检查服务状态..."
    sudo systemctl status "\$SERVICE_NAME" --no-pager -l || true
    echo "✓ systemd服务已重启"
else
    echo "警告: 未找到Docker或systemd服务"
    echo "请手动重启CLIProxyAPI:"
    echo "  - Docker: cd $CPA_PATH && docker compose restart"
    echo "  - systemd: sudo systemctl restart cliproxyapi"
    echo "  - 手动: 停止进程并重新启动"
fi
REMOTE_RESTART

echo -e "${GREEN}✓ 服务重启完成${NC}"

# 验证部署
echo -e "\n${BLUE}[验证]${NC} 检查插件状态..."
VERIFY_OUTPUT=$($SSH_CMD $TARGET_HOST "curl -s http://localhost:8787/v0/management/plugins 2>/dev/null | grep usage-statistics" || echo "")

if [ -n "$VERIFY_OUTPUT" ]; then
    echo -e "${GREEN}✓ 插件已成功加载！${NC}"
else
    echo -e "${YELLOW}⚠ 无法验证插件状态${NC}"
    echo "这可能是因为:"
    echo "  1. 服务还在启动中（稍等片刻再试）"
    echo "  2. 需要认证才能访问API"
    echo "  3. 端口号不是8787"
fi

# 完成
echo ""
echo -e "${BLUE}╔════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║${NC}  ${GREEN}✓ 部署完成！${NC}                                       ${BLUE}║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${YELLOW}下一步:${NC}"
echo "  1. 访问管理面板查看插件状态"
echo "  2. 发送测试请求验证统计功能"
echo "  3. 查看日志: ssh $TARGET_HOST 'sudo journalctl -u cliproxyapi -n 50 | grep plugin'"
echo ""
echo -e "${YELLOW}API端点:${NC}"
echo "  GET  /v0/management/plugins/usage-statistics/usage"
echo "  GET  /v0/management/plugins/usage-statistics/usage/export"
echo "  POST /v0/management/plugins/usage-statistics/usage/import"
echo ""
