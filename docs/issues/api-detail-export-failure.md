# 上游接口详情导出失败记录

## 现象

上游接口详情区域有两个导出按钮：

- `导出当前接口表格`
- `导出当前接口明细`

当前观察到这两个按钮点击后显示“导出失败”。

相关前端位置：

- `go/dashboard/index.html`: `exportApiCsv`、`exportApiJson`
- `go/dashboard/script.js`: `exportApiRows(kind)`

## 当前流程

`exportApiRows(kind)` 会构造事件导出参数：

- `range`: 当前时间范围
- `model`: 当前模型筛选，存在时附加
- `source`: 当前来源筛选，存在时附加
- `auth`: 当前凭证筛选，存在时附加
- `api`: 当前选中的上游接口
- `format`: `csv` 或 `json`

随后调用后台事件导出任务流程：

- `dashboard-events-export-jobs`
- `dashboard-events-export-download`

## 初步影响

影响范围暂定为 dashboard 的“上游接口详情”区域导出能力。

不影响：

- 页面正常展示上游接口详情。
- 全局“请求事件明细”区域的导出按钮是否可用，需单独确认。
- 顶部完整用量数据导出是否可用，需单独确认。

## 待排查方向

后续修复前建议确认以下点：

1. 后端 `dashboard-events-export-jobs` 对 `api` 参数的过滤是否与前端传入的 `selectedApi` 完全一致。
2. 前端传入的凭证筛选参数为 `auth`，后端查询结构是否期望同名参数或 `auth_index`。
3. 后台导出任务创建、轮询、下载三个阶段哪个阶段返回错误。
4. 导出失败时浏览器控制台、CPA 日志、插件 management response 中的具体错误内容。
5. 当当前接口没有匹配事件时，JSON 导出是否应静默返回空文件，而不是表现为失败。

## 验证建议

修复后建议覆盖：

- 选择某个上游接口后导出 CSV 成功。
- 选择某个上游接口后导出 JSON 成功。
- 加上模型、来源、凭证筛选后导出成功。
- 当前接口没有匹配事件时返回空导出或明确提示，不显示通用“导出失败”。

## 当前状态

状态：已记录，暂不修复。
