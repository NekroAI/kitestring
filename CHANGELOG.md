# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.4.0] — 2026-11-10

0.3.0 之后的版本，按 git log 归纳主要变化。

### 凭据库
- 新增凭据库：会话用 `ks secret request` / MCP `secret_request` 申请第三方凭据，申请进「待处理」，你在网页上填值、设项目 / 有效期 / 允许的命令 / 是否可写入磁盘后授权，结果送回申请方。
- `ks secret run` 只在授权有效、项目匹配、命令在允许范围内时把值放进这条命令的环境变量；过期、撤销、越界一律拒绝并记下。已知的值在记录、推送、分享页里一律隐去。
- 侧边栏新增「凭据」页（手机从设置进）：列表掩码、按状态筛选、详情时间线，可手动替换值、撤销授权。
- 值可标「不敏感」（如用户名、Account ID），申请时可用 `--plain` 声明。
- 会话可对已授权（含已失效、已撤回）的凭据发起修改授权申请（`ks secret amend`）：改允许的命令、有效期、写入磁盘，或申请替换值；批准后凭据名和取用记录延续，换掉的敏感旧值仍参与脱敏。
- 「保存并授权」「拒绝」都能带附言，随结果送回申请方，拒绝时必填。
- 每条凭据有稳定编号 S-x，`ks secret run S-x` 按编号取用；名称可在凭据页改，也可在修改申请里提出。

### 通讯与会话
- 通讯页把只告知、按设计攒着的消息标成灰色「攒着」，侧边栏「通讯」黄标只算要对方动手却还没送到的。
- 最近动态改由后端按时间线给出，补上会话的关键动作。
- 有身份的会话接任、不再担任身份、下线时只告知本项目其他在线会话；接手概览加「当前在线」。

### 界面与命令
- 草稿输入框的附件、清空、恢复原文挪进框内右上角固定工具槽，框和按钮行不再移动。
- 凭据值和 API Key 框不再弹浏览器已存的登录密码；凭据页按钮去掉「…」，手机宽度下标题独占一行；会话泳道后台命令行标签改为「命令」。
- `ks hub password` 与 `ks hub passwd` 相同；敲错 hub 子命令时报错列出可用的子命令。

### 修复
- 凭据时间线同一毫秒里的记录按写入顺序排。
- 泳道测速用例先预热、取 3 次最快，消除 CI 偶发误报。

## [0.3.0] — 2026-11-06

0.2.0 之后的版本，按 git log 归纳主要变化。

### 分享与访客
- 新增分享：按计划 → 阶段 → 需求 → 会话分层勾选，可设名称、访问密码、有效期，可勾「包含以后新增的」；每个访客一条可单独复制、撤回、恢复的链接。
- 所有者网页新增「分享」入口（桌面侧边栏、手机设置页），需求、计划的「更多」菜单和右键菜单可直接分享。
- 新增访客网页 `/s/<token>`，免登录只读：设了密码先输密码，撤回或到期后显示被拒绝；含概览、需求详情、计划与阶段小结、会话详情，内容变化自动更新。
- 访客能在分享里的需求、决策、证据、阶段上留言，先进「待处理」，由你原样或改写后放入、或忽略（3 秒撤销）；放入后写明来自哪个分享链接。
- 分享链接发到 IM 时显示摘要：无密码的有效分享显示名称和概况，其余情况一律显示通用介绍。
- 访客页侧边栏需求树只到需求，另起「会话」一组；概览在宽屏下动态列自己滚动，中等和手机宽度仍单列整页滚。

### 原型与会话
- 证据和决策能交可交互原型包，需求抽屉和决策卡显示原型卡片，可全屏查看、切宽度、切历史版本、下载整包，原型在隔离沙箱里运行。
- 工作台上能让会话「停下」：会话卡、会话详情都有（3 秒撤销期），项目页「全部停下…」先说明后果；纸鸢只送停下提醒，不强行中止。
- 会话详情页重做：顶部一处状态、对象索引、按轮对话时间线、底部输入框，网页发的话显示送达状态。
- 会话时间线按真实来源标出是谁唤醒了会话，连续自动唤醒收成一行，访客页同步。
- `ks wait` 单次等待最多 55 分钟，常驻会话空等累计到项目设置的时长（默认 4 小时）后写交接下线、开新会话接任。
- 指派送达后迟迟没开始处理时兜底：超时再提醒一次，仍没开始进「待处理」（项目设置可调，默认 10 分钟）。
- 受阻需求等的事完成后自动解除，会话关联开工转进行中。

### 通讯与接入
- 点侧边栏「通讯」的待送达数字，直接落到有待送达消息的项目并打开「只看未送达」；项目菜单标出各项目待送达条数。
- 新增 `ks update`：从 GitHub Release 取最新版、校验 `SHA256SUMS.txt`、原子替换 `ks` 并重启 Hub 和后台服务；`--check` 只查不装。
- `ks` 子命令用不上的选项不再被静默丢掉：req / team / plan / commit 按子命令检查选项并报错，login / install 的选项前后都能写。
- 接入设备弹窗改成完整指引：选平台和工具拼出完整命令，含 PATH、后台服务、核对。

### 文案
- 界面文案规范定稿为 `docs/copywriting.md`，文案自动检查上线，前后端共用一份规则。
- 工作台、需求池、会话、计划、审批、设置、设备接入等界面文案按规范改写，术语统一（如「提交评审」「指派」「待处理」）。
- 只给网页和推送看的后端文字收拢到 `internal/hub/copy.go`，并按同一份规则检查。

### 文档
- 中英 README 与截图更新到现状：补分享与访客、原型、通讯、各身份分工和 `ks update`，截图用真实数据重拍；架构图与安装、工作原理文档同步。

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
