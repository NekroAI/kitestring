# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.0] — 2026-11-01

0.1.0 之后的第一个正式版，按 git log 归纳主要变化。

### 团队与身份
- 新增会话身份：主调度（PM）、开发（Dev）、主助理（Aide）、顾问（Adv）、评审（Rev），`ks role` 声明，认中文名和别称。
- 主调度、主助理每项目唯一，新会话接任时旧的自动卸任；接任时按身份生成接手概览。
- 身份提示词可在设置页改写、一键恢复默认；身份名可切换中英文。
- 团队消息按身份寻址（`ks team tell --to aide|pm|reviewer`），可回复、可发给顾问，分「要动手 / 只告知」两档。
- 跨工具会话通信：需求下留言、在线会话列表、点对点消息与广播、送达状态、订阅需求，网页新增「通讯」页。
- 主调度可查看工人池、派活给开发；开发上线、交验收、受阻会通知派活的主调度。
- 交验收后自动派给在线评审验证，评审意见挂在需求上。
- 会话交接固化成流程：`handoff_write` / `ks handoff`、`ks takeover`，压缩后自动补回身份和关键状态。

### 需求、决策与计划
- 需求池改为盘面：分布条、池子指标、按状态分列，宽屏铺满一屏、超宽屏用满宽度。
- 需求抽屉改回一条时间线；每条记录写明是哪个会话、当时叫什么。
- 需求状态变更历史、需求关联 git 提交（含回滚识别）。
- Agent 可改需求内容（`req_edit`）、申请改状态、作废自己交错的证据。
- 决策支持逐点选择、撤回、后果链、可选的一句话理由；「拍板」统一改称「决策」。
- 证据、决策、阶段小结支持表达块（清单、对比、截图、流程、指标、改动范围、选项、表格）；截图带标题和改动框。
- 所有附件入口支持任意文件，不只图片。
- 计划退回回到草稿可修改重交；批准计划不再拉回已搁置的需求。
- 「要你处理」只认明确记录，等你的事都进来，等待链写明谁在等。

### 工作台与网页
- 工作台改为三栏一屏驾驶舱：要你处理、会话泳道、右栏。
- 会话泳道：每分钟汇总的调用时间线、token 用量与缓存命中、shell 泳道、子任务行显示实际模型。
- 上下文圆环，超过阈值提示压缩并复制 `/compact` 命令。
- 列出会话已结束仍在跑的孤儿进程，可停止或忽略。
- 顶部简报条与「暂时离开」：离开期间主助理在授权范围内代答，推送分档过滤，回来看离开期间小结。
- 引用在抽屉左边展开只读预览纸；⌘K 命令面板与全局快捷键。
- 撤销期统一成一套；手势、动画、触屏与妙控板翻页统一。
- 输入草稿统一缓存；资源加载提速（gzip、缩略图分档）。

### 接入与运行
- 接入 DeepSeek Harness（DSH）原生 Host 插件：会话上墙、用量与上下文、待命唤醒。
- 支持多个 Claude 配置目录（`ks install --claude-dir`）。
- `ks wait` 可订阅项目事件（`--on decision,review,blocked,stage`）。
- Codex 后台自动任务不再当成用户会话显示。
- daemon 重启后子任务名、进程核对不丢；已退出进程的会话不再显示运行中。
- 新增 `scripts/deploy.sh`（`make deploy`）：构建、备份、替换、重启、核对，失败自动回滚。

### 修复
- 安装脚本在 macOS `/bin/sh` 下读错变量名而中断。
- 每轮小结因推理内容占满 token 上限而解析失败。
- 常驻会话先结束一轮、ks wait 稍后挂上时，偶尔显示「等你回复」而不是待命（上送事件排序错位）。

### 工程
- CI 恢复全绿：修掉 Linux 上几条 e2e 测试的时序竞态；新增前端单测、类型检查和 DSH 插件测试。
- 本地 `make build` 的版本号去掉前导 v，与 Release 包一致（显示 `0.2.0`）。

## [0.1.0] — 2026-10-23

First public release.

### Added
- **Install from GitHub:** `curl … /scripts/install.sh | sh` downloads the build for your system (macOS / Linux, arm64 / x64), verifies its SHA-256 and installs `ks` to `~/.local/bin`. Every tagged version is built and published by GitHub Actions.
- **Session wall:** every Claude Code / Codex session on every machine, grouped by project and conversation, with state (running, waiting on you, standing by, finished), a live action stream, background tasks, context usage and the last prompt.
- **Decision inbox:** agents raise decisions with options, a recommendation and option images; approvals and rejections can carry a note. Your answer is injected into the agent's context on your next message.
- **Requirement board:** drafts, to-do, in progress, review and done, with a list view, drag to change status (long-press on touch screens), comments, screenshot and screen-recording evidence, image annotation with stylus support, and design requirements that come with proposals to choose from.
- **Hand off work:** give a requirement to a session; to-do requirements queue up and arrive together when the session is idle. Sessions with lighter context are suggested first.
- **Plans:** split a larger goal into ordered stages; each stage is handed over when unlocked and accepted as a whole.
- **`ks wait`:** an agent can wait for your answer, acceptance or comment and continue as soon as you respond — in the background for Claude Code, in the foreground (up to 30 minutes) for Codex.
- **Agent tools:** a stdio MCP server and an equivalent `ks req` / `ks plan` / `ks decision` CLI.
- **Notifications:** system push on phones, iPad and desktop browsers (Web Push).
- **Sign-in:** password and passkeys for the web UI, a revocable token per device, and rate-limited login for public exposure.
- **Projects:** merged by git remote; can be hidden or merged into another project.
- **Daemon:** buffers events while offline and replays them, reconnects on its own, and fails open so hooks never block your agent.
