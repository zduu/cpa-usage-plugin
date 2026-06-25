# GitHub Actions 自动构建指南

## 🚀 功能说明

本项目配置了GitHub Actions自动构建，每次推送代码或创建标签时，会自动编译适用于Linux x86_64的插件。

## 📋 触发条件

Actions会在以下情况自动运行：

1. **推送到主分支**
   ```bash
   git push origin main
   ```

2. **创建版本标签**
   ```bash
   git tag v1.0.0
   git push origin v1.0.0
   ```

3. **Pull Request**
   - 提交PR到main分支时自动构建验证

4. **手动触发**
   - 在GitHub仓库的Actions页面手动运行

## 📦 构建产物

### Artifacts（构建产物）
每次构建成功后，会生成一个artifact：
- **名称**: `usage-statistics-plugin`
- **文件**: `usage-statistics.so`
- **保留期**: 90天
- **下载位置**: GitHub Actions运行页面

### Release（发布版本）
当推送tag时（如 `v1.0.0`），会自动创建GitHub Release：
- 包含编译好的 `usage-statistics.so`
- 自动生成release notes
- 可直接下载使用

## 🔧 使用方法

### 方法1: 从Artifacts下载

1. 访问仓库的Actions页面
2. 选择最新的成功构建
3. 在"Artifacts"区域下载 `usage-statistics-plugin`
4. 解压得到 `usage-statistics.so`

### 方法2: 从Release下载

1. 访问仓库的Releases页面
2. 选择需要的版本
3. 直接下载 `usage-statistics.so`

### 方法3: 使用脚本自动下载

创建下载脚本 `download-latest.sh`：

```bash
#!/bin/bash
# 从GitHub Actions最新构建下载插件

REPO="your-username/cpa-usage-plugin"  # 修改为你的仓库
TOKEN="${GITHUB_TOKEN}"  # 可选：私有仓库需要

# 获取最新artifact
echo "获取最新构建..."
ARTIFACTS=$(curl -s -H "Authorization: Bearer $TOKEN" \
  "https://api.github.com/repos/$REPO/actions/artifacts?per_page=1")

DOWNLOAD_URL=$(echo "$ARTIFACTS" | jq -r '.artifacts[0].archive_download_url')

if [ "$DOWNLOAD_URL" == "null" ]; then
  echo "错误: 未找到构建产物"
  exit 1
fi

# 下载
echo "下载插件..."
curl -L -H "Authorization: Bearer $TOKEN" \
  -o usage-statistics-plugin.zip \
  "$DOWNLOAD_URL"

# 解压
unzip -o usage-statistics-plugin.zip
rm usage-statistics-plugin.zip

echo "✓ 下载完成: usage-statistics.so"
ls -lh usage-statistics.so
```

### 方法4: 从Release下载（推荐）

```bash
#!/bin/bash
# 从最新Release下载

REPO="your-username/cpa-usage-plugin"
LATEST_RELEASE=$(curl -s "https://api.github.com/repos/$REPO/releases/latest")
DOWNLOAD_URL=$(echo "$LATEST_RELEASE" | jq -r '.assets[] | select(.name=="usage-statistics.so") | .browser_download_url')

curl -L -o usage-statistics.so "$DOWNLOAD_URL"
echo "✓ 下载完成"
ls -lh usage-statistics.so
```

## 🚀 部署流程

### 快速部署（使用Actions构建）

1. **推送代码触发构建**
   ```bash
   git add .
   git commit -m "Update plugin"
   git push
   ```

2. **等待构建完成**（约1-2分钟）
   - 访问Actions页面查看进度

3. **下载并部署**
   ```bash
   # 下载最新构建
   gh run download --name usage-statistics-plugin
   
   # 部署到远程
   scp -J me usage-statistics.so mine:/tmp/
   ssh -J me mine "mv /tmp/usage-statistics.so /home/zoex/docker/CLIProxyAPI/plugins/"
   ssh -J me mine "cd /home/zoex/docker/CLIProxyAPI && docker compose restart cli-proxy-api"
   ```

### 自动化部署（推荐）

更新 `deploy-to-vps.sh` 支持从GitHub下载：

```bash
#!/bin/bash
# 增强版部署脚本 - 支持从GitHub Actions下载

DOWNLOAD_FROM_GITHUB=false
GITHUB_REPO="your-username/cpa-usage-plugin"

# 参数解析
if [ "$1" == "--github" ]; then
    DOWNLOAD_FROM_GITHUB=true
    shift
fi

# 如果指定从GitHub下载
if [ "$DOWNLOAD_FROM_GITHUB" = true ]; then
    echo "从GitHub Actions下载最新构建..."
    gh run download --name usage-statistics-plugin --dir /tmp/
    cp /tmp/usage-statistics.so ./usage-statistics.so
    rm -rf /tmp/usage-statistics-plugin
else
    # 本地构建
    ./build.sh
fi

# 继续原有部署流程...
```

使用方法：
```bash
# 从GitHub下载并部署
./deploy-to-vps.sh --github me mine /home/zoex/docker/CLIProxyAPI

# 或本地构建并部署
./deploy-to-vps.sh me mine /home/zoex/docker/CLIProxyAPI
```

## 📌 版本管理

### 创建新版本

```bash
# 1. 更新代码
git add .
git commit -m "feat: add new feature"

# 2. 创建标签
git tag -a v1.0.0 -m "Release v1.0.0"

# 3. 推送标签
git push origin v1.0.0

# 4. Actions自动构建并创建Release
```

### 语义化版本

- `v1.0.0` - 主版本（重大更新）
- `v1.1.0` - 次版本（功能更新）
- `v1.0.1` - 修订版本（bug修复）

## 🔍 故障排查

### Actions构建失败

1. **检查Go版本**
   - 确保 `go.mod` 中的Go版本与Actions配置一致

2. **检查依赖**
   ```bash
   go mod tidy
   go mod verify
   ```

3. **查看构建日志**
   - 访问Actions页面查看详细错误信息

### 下载失败

1. **私有仓库**
   - 需要设置 `GITHUB_TOKEN`
   - 或使用 `gh` CLI工具

2. **网络问题**
   - 使用代理或镜像加速

### 插件加载失败

1. **架构不匹配**
   - 确认Actions构建的是 `linux/amd64`
   - 检查远程服务器架构：`uname -m`

2. **文件损坏**
   - 重新下载
   - 验证文件大小和类型：`file usage-statistics.so`

## 💡 高级配置

### 多架构构建

如需支持多架构，修改 `.github/workflows/build.yml`：

```yaml
strategy:
  matrix:
    include:
      - os: ubuntu-latest
        goos: linux
        goarch: amd64
        artifact: usage-statistics-linux-amd64.so
      - os: ubuntu-latest
        goos: linux
        goarch: arm64
        artifact: usage-statistics-linux-arm64.so
```

### 缓存优化

加快构建速度：

```yaml
- name: Cache Go modules
  uses: actions/cache@v3
  with:
    path: ~/go/pkg/mod
    key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
```

### 构建通知

添加构建结果通知：

```yaml
- name: Notify on failure
  if: failure()
  uses: actions/github-script@v7
  with:
    script: |
      github.rest.issues.createComment({
        issue_number: context.issue.number,
        owner: context.repo.owner,
        repo: context.repo.repo,
        body: '❌ Build failed! Please check the logs.'
      })
```

## 📚 相关资源

- [GitHub Actions文档](https://docs.github.com/actions)
- [Go构建模式](https://pkg.go.dev/cmd/go#hdr-Build_modes)
- [交叉编译指南](https://go.dev/doc/install/source#environment)

---

**推荐工作流**：
1. 本地开发和测试
2. 推送到GitHub触发Actions构建
3. 从Actions下载编译好的插件
4. 使用脚本部署到远程服务器
