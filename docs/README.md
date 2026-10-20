# Documentation

仓库文档按用途归档在 `docs/` 下：

- [guides/cpa-usage.md](guides/cpa-usage.md): 安装、配置、部署、更新及版本发布说明
- [guides/quota-cycles.md](guides/quota-cycles.md): CPA v8 凭证额度周期、模型分布、备份与验收

历史版本发布说明和资产见 [GitHub Releases](https://github.com/zduu/cpa-usage-plugin/releases)。CI 从部署指南对应的 `## vX.Y.Z - 日期` 小节提取新版本发布说明。

图片资源位于 `docs/images/readme/`，供根目录 `README.md` 引用。

发布包的第三方依赖许可（SQLite 的 MIT / BSD-3-Clause 声明）由 CI 在打包时从依赖模块的 `LICENSE` 文件生成 `THIRD_PARTY_NOTICES.txt`，仓库内不保留副本。

## 验收脚本

- `scripts/validate-stock-cpa.py`：在未修改的 stock CPA 容器里跑真实 HTTP/浏览器用例。
- `scripts/validate-upgrade-cpa.py`：用上一正式版的共享库写出数据目录，再换成候选产物重启，核对升级后的累计值、新记录入账与重复重启稳定性。
- `scripts/compare-release-performance.py`：冻结候选副本，与隔离基线做同工具链组件对照。
- `scripts/validate-quota-v8.py`：使用独立 CPA v8 配置验证额度周期 HTTP 接入、重启恢复；`--browser` 验证页面并保存截图。
