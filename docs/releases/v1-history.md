# v1 Release History (Legacy)

`v1.0.0` 到 `v1.2.18` 属于仓库早期发布历史，发布时间集中在 2026-06-25 到 2026-06-28。这个阶段的 tag 和 GitHub Release 未完全采用统一流程，存在以下遗留情况：

- patch 版本号存在跳号
- annotated tag 和 lightweight tag 混用
- 部分 tag 指向版本 bump 或文档提交，而不是独立的 release 提交
- 提交信息中英文混用，未完全遵循 Conventional Commits

为避免破坏现有下载链接、历史资产和用户固定版本引用，本仓库对 `v1` 版本采用非破坏性整理策略：

- 保留现有 tag 名称和提交指向，不回写历史
- 保留已有 GitHub Release 和资产下载地址
- 用本页补充 `v1` 历史说明，作为 legacy 索引
- 自 `v2.0.0` 起，以 [changelog.md](changelog.md) 和 GitHub Actions 发布流程作为正式发布记录

## 历史标签清单

| Tag | 日期 | 提交 | 类型 | 当前指向 | 说明 |
|------|------|------|------|------|------|
| `v1.0.0` | 2026-06-25 | `92ff0bf` | annotated | `docs: 添加优化计划并精简 README` | tag 注释为“初始发布版本”，但 tag 实际落在文档整理提交上；插件初始引入提交为 `2ad48f3`。 |
| `v1.1.0` | 2026-06-26 | `367473e` | annotated | `chore: bump plugin version to 1.1.0` | 版本 bump 提交。 |
| `v1.2.0` | 2026-06-26 | `a7fecc1` | annotated | `feat: add config-driven plugin updater` | 新增配置驱动的插件更新能力。 |
| `v1.2.4` | 2026-06-26 | `8669d90` | lightweight | `fix: 修正用量看板统计与导入兼容性` | patch 号跳号，保留原历史。 |
| `v1.2.8` | 2026-06-26 | `18dbb40` | lightweight | `fix: 复用管理中心登录态导入` | patch 号跳号，保留原历史。 |
| `v1.2.9` | 2026-06-26 | `35b957c` | annotated | `chore(release): bump version to 1.2.9` | 版本 bump 提交。 |
| `v1.2.10` | 2026-06-26 | `a16ea6f` | annotated | `chore(release): bump version to 1.2.10` | 版本 bump 提交。 |
| `v1.2.11` | 2026-06-26 | `7a5cb9c` | lightweight | `chore(release): bump version to 1.2.11` | lightweight tag。 |
| `v1.2.12` | 2026-06-26 | `d50555f` | annotated | `fix(dashboard): merge imported client api stats` | 仪表盘导入统计修复。 |
| `v1.2.13` | 2026-06-26 | `1781a19` | annotated | `fix(dashboard): decouple event list from api detail selection` | 仪表盘事件列表与 API 详情解耦。 |
| `v1.2.15` | 2026-06-27 | `1e8c358` | annotated | `Bump version to 1.2.15` | 提交信息未遵循当前提交规范。 |
| `v1.2.17` | 2026-06-27 | `db738a8` | lightweight | `Fix upstream channel grouping for provider API keys` | lightweight tag，提交信息为英文。 |
| `v1.2.18` | 2026-06-28 | `1c9ecdc` | annotated | `Restore upstream API detail widgets` | `v1` 阶段最后一个历史版本。 |

## 维护约定

今后如需整理 `v1` 历史，仅做补充说明，不做下列操作：

- 不移动既有 tag 指向
- 不删除并重建 `v1` tag
- 不补造缺失的 patch 版本号
- 不修改既有 release 资产文件名

如需在 GitHub Releases 页面补充可读性，建议仅做以下非破坏性操作：

- 将 `v1` 历史 release 标题追加 `Legacy`
- 在 release notes 第一段说明该版本属于 `v2` 规范化发布流程之前的历史版本
- 引用本页作为历史索引
