# 远程VPS部署指南

## 📋 部署说明

本插件支持部署到远程VPS服务器，通过SSH连接即可完成部署。

## 🚀 快速部署（推荐）

### 方案: 本地构建 + 远程部署

适用于大多数场景，特别是网络不稳定时。

```bash
# 1. 本地构建插件
cd cpa-usage-plugin
./build.sh

# 2. 传输到远程VPS（通过SSH，可能需要跳板机）
# 如果直接连接:
scp usage-statistics.so your-vps:/tmp/

# 如果需要跳板机:
scp -J jump-host usage-statistics.so target-host:/tmp/

# 3. 连接到远程VPS
ssh your-vps
# 或通过跳板机:
ssh -J jump-host target-host

# 4. 在远程VPS上安装
sudo mv /tmp/usage-statistics.so /path/to/cliproxyapi/plugins/
sudo chmod 644 /path/to/cliproxyapi/plugins/usage-statistics.so

# 5. 配置插件
sudo nano /path/to/cliproxyapi/config.yaml
# 添加:
plugins:
  enabled: true
  configs:
    usage-statistics:
      enabled: true

# 6. 重启服务
sudo systemctl restart cliproxyapi

# 7. 验证
curl http://localhost:8787/v0/management/plugins/usage-statistics/usage
```

## 📝 详细步骤

### 步骤1: 本地构建

```bash
cd cpa-usage-plugin
./build.sh

# 验证构建产物
ls -lh usage-statistics.so
```

### 步骤2: 传输到远程

#### 直接SSH连接
```bash
scp usage-statistics.so your-vps:/tmp/
```

#### 通过跳板机连接
```bash
# 方式1: 使用ProxyJump
scp -J jump-host usage-statistics.so target-host:/tmp/

# 方式2: 配置SSH config后直接使用
# 编辑 ~/.ssh/config 添加:
# Host my-vps
#   HostName target-ip
#   User username
#   ProxyJump jump-host

scp usage-statistics.so my-vps:/tmp/
```

### 步骤3: 远程安装

```bash
# 连接到远程
ssh your-vps
# 或: ssh -J jump-host target-host

# 移动到插件目录
sudo mv /tmp/usage-statistics.so /path/to/cliproxyapi/plugins/

# 设置权限
sudo chmod 644 /path/to/cliproxyapi/plugins/usage-statistics.so
sudo chown cliproxy:cliproxy /path/to/cliproxyapi/plugins/usage-statistics.so

# 验证
ls -lh /path/to/cliproxyapi/plugins/usage-statistics.so
```

### 步骤4: 配置插件

```bash
# 备份配置
sudo cp /path/to/cliproxyapi/config.yaml /path/to/cliproxyapi/config.yaml.backup

# 编辑配置
sudo nano /path/to/cliproxyapi/config.yaml

# 添加或修改plugins部分:
plugins:
  enabled: true
  dir: plugins
  configs:
    usage-statistics:
      enabled: true
      priority: 0
```

### 步骤5: 重启服务

```bash
# 重启CLIProxyAPI
sudo systemctl restart cliproxyapi

# 等待启动
sleep 3

# 检查状态
sudo systemctl status cliproxyapi

# 查看日志确认插件加载
sudo journalctl -u cliproxyapi -n 50 | grep -i plugin
```

### 步骤6: 验证部署

```bash
# 检查插件列表
curl -s http://localhost:8787/v0/management/plugins | jq '.plugins[] | select(.id == "usage-statistics")'

# 测试用量API
curl -s http://localhost:8787/v0/management/plugins/usage-statistics/usage | jq .
```

## 🔧 一键部署脚本

创建自动化脚本 `deploy-to-vps.sh`:

```bash
#!/bin/bash
set -e

# 配置变量（根据实际情况修改）
VPS_HOST="${1:-your-vps}"           # SSH主机名
CPA_PATH="${2:-/opt/cliproxyapi}"   # CLIProxyAPI路径
JUMP_HOST="${3}"                     # 跳板机（可选）

echo "🚀 开始部署到 $VPS_HOST"

# 1. 本地构建
echo "📦 步骤 1/6: 本地构建插件..."
./build.sh

# 2. 传输
echo "📤 步骤 2/6: 传输到远程..."
if [ -n "$JUMP_HOST" ]; then
    scp -J "$JUMP_HOST" usage-statistics.so "$VPS_HOST:/tmp/"
else
    scp usage-statistics.so "$VPS_HOST:/tmp/"
fi

# 3. 安装
echo "💾 步骤 3/6: 安装插件..."
SSH_CMD="ssh"
[ -n "$JUMP_HOST" ] && SSH_CMD="ssh -J $JUMP_HOST"

$SSH_CMD "$VPS_HOST" << EOF
sudo mv /tmp/usage-statistics.so $CPA_PATH/plugins/
sudo chmod 644 $CPA_PATH/plugins/usage-statistics.so
ls -lh $CPA_PATH/plugins/usage-statistics.so
EOF

# 4. 提示配置
echo "⚙️  步骤 4/6: 配置插件"
echo "请确保 $CPA_PATH/config.yaml 中包含:"
echo "plugins:"
echo "  enabled: true"
echo "  configs:"
echo "    usage-statistics:"
echo "      enabled: true"
read -p "已配置？按回车继续，或Ctrl+C退出手动配置..." -r

# 5. 重启
echo "🔄 步骤 5/6: 重启服务..."
$SSH_CMD "$VPS_HOST" << EOF
sudo systemctl restart cliproxyapi
sleep 3
sudo systemctl status cliproxyapi --no-pager -l
EOF

# 6. 验证
echo "✅ 步骤 6/6: 验证部署..."
$SSH_CMD "$VPS_HOST" << EOF
curl -s http://localhost:8787/v0/management/plugins | jq '.plugins[] | select(.id == "usage-statistics")' || echo "插件可能还在加载中..."
EOF

echo ""
echo "🎉 部署完成！"
echo ""
echo "下一步:"
echo "1. 访问管理面板确认插件状态"
echo "2. 发送测试请求验证统计功能"
```

使用方法:

```bash
# 直接连接
./deploy-to-vps.sh my-vps /opt/cliproxyapi

# 通过跳板机
./deploy-to-vps.sh target-host /opt/cliproxyapi jump-host
```

## 🐛 常见问题

### Q: 网络不稳定传输中断？

```bash
# 使用rsync增量传输
rsync -avz -e "ssh -J jump-host" usage-statistics.so target-host:/tmp/

# 启用压缩
scp -C usage-statistics.so your-vps:/tmp/
```

### Q: 插件加载失败？

```bash
# 检查日志
sudo journalctl -u cliproxyapi -n 100 | grep -i "plugin\|error"

# 检查文件完整性
ls -lh /path/to/cliproxyapi/plugins/usage-statistics.so
file /path/to/cliproxyapi/plugins/usage-statistics.so

# 检查权限
ls -la /path/to/cliproxyapi/plugins/
```

### Q: 如何回滚？

```bash
# 停止服务
sudo systemctl stop cliproxyapi

# 删除插件
sudo rm /path/to/cliproxyapi/plugins/usage-statistics.so

# 恢复配置
sudo cp /path/to/cliproxyapi/config.yaml.backup /path/to/cliproxyapi/config.yaml

# 启动服务
sudo systemctl start cliproxyapi
```

## 📋 部署检查清单

### 部署前
- [ ] 本地Go环境正常 (go version)
- [ ] SSH连接测试通过
- [ ] 确认远程CLIProxyAPI路径
- [ ] 确认有足够权限
- [ ] 备份远程config.yaml

### 部署后
- [ ] 插件文件存在于plugins目录
- [ ] 插件在管理面板显示
- [ ] API端点可访问
- [ ] 日志无错误
- [ ] 用量统计正常工作

## 💡 SSH配置优化

在 `~/.ssh/config` 中添加：

```ssh-config
# 示例：配置跳板机
Host jump
    HostName jump-server.com
    User username
    IdentityFile ~/.ssh/id_rsa

# 示例：配置目标服务器
Host my-vps
    HostName target-server.com
    User username
    ProxyJump jump
    IdentityFile ~/.ssh/id_rsa
    ServerAliveInterval 60
    ControlMaster auto
    ControlPath ~/.ssh/cm-%r@%h:%p
    ControlPersist 10m
```

配置后可以直接使用：
```bash
scp usage-statistics.so my-vps:/tmp/
ssh my-vps
```

## 🔐 安全建议

1. ✅ 使用SSH密钥认证
2. ✅ 部署后删除/tmp中的临时文件
3. ✅ 定期备份config.yaml
4. ✅ 使用非root用户+sudo
5. ✅ 检查插件文件权限（644）

---

**需要帮助？** 查看 INSTALL.md 或 REFERENCE.md
