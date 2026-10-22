# 同类项目调研（2026-10-22）

结论来自搜索结果与项目 README 摘要，未逐个部署验证。

| 类别 | 项目 | 要点 | 与 Kitestring 的差异 |
|---|---|---|---|
| 单机会话监控 | [ccdeck](https://ccdeck.dev/) | 本地 Claude Code + Codex 实时面板；自述 Codex 只能读 rollout 日志，无法检测“等待中” | 单机，无需求 / 决策概念 |
| 单机会话监控 | [Stargx/claude-code-dashboard](https://github.com/Stargx/claude-code-dashboard) | localhost 面板，token、工具、子 agent 状态 | 仅 Claude Code，单机 |
| 局域网监控 | [bruceyxli/claude-code-monitor](https://github.com/bruceyxli/claude-code-monitor) | 实时面板 + 远程审批，支持局域网 | 仅 Claude Code，局域网 |
| 跨机监控 | [hoangsonww/Claude-Code-Agent-Monitor](https://github.com/hoangsonww/Claude-Code-Agent-Monitor) | Claude Code + Codex，经 SSH 拉取他机会话，有 Kanban 状态板与通知 | 中心拉取（需 SSH 可达），无决策回流 |
| 远程控制 | [slopus/happy](https://github.com/slopus/happy) | 手机 / Web 远程操控 Claude Code、Codex，授权推送，E2E 加密，可自托管 | 聚焦单会话操控，不管需求 |
| 远程控制 | Omnara（[对比文章](https://seawork.ai/en/blogs/omnara-vs-happy-vs-seawork/)） | 手机端指挥中心，支持 Claude Code 与 Codex | 同上 |
| 任务执行看板 | [Vibe Kanban](https://jimmysong.io/en/ai/vibe-kanban) | 卡片拖入进行中即建 worktree 并启动 agent | 看板驱动执行，方向相反 |
| 仓库内需求 | [Backlog.md](https://www.ai.engineer/talks/zMXKhhwiCIc-backlog-md-terminal-kanban-board-managing-tasks) | md 原生任务管理 + CLI / MCP | 单仓库，无实例视角 |
| 用量 | [Javis603/token-monitor](https://github.com/Javis603/token-monitor) | 46+ 工具的 token / 额度，多设备同步 | 只管额度；可作额度面板参考 |
| 同名风险 | [quentinzhang/vibedeck](https://github.com/quentinzhang/vibedeck) | 本地优先的 OpenClaw 看板 | 命名避开 |

## 差异化定位

现有项目要么“看 agent”（监控），要么“遥控 agent”（远程），要么“让看板驱动 agent”（编排）。Kitestring 的核心是 **人与多个 agent 之间的需求和决策闭环**：

1. 设备主动推送，跨地域、跨 NAT。
2. 决策收件箱 + hook 注入回 Claude Code 与 Codex 上下文。
3. 工作空间级通知路由（公司 / 个人分流到不同渠道）。
4. 只存元数据与摘要。
