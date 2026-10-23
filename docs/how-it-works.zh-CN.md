# Kitestring 的工作原理

[English](how-it-works.md) · 中文

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture-dark.zh-CN.svg">
  <img src="assets/architecture-light.zh-CN.svg" alt="每台设备上的 Claude Code 与 Codex 通过 hooks 和 MCP 连到 ks daemon；daemon 把事件发给 ks hub，并接收你的决策；你在任意浏览器或手机上使用 Hub 的网页看板。">
</picture>

## 一个二进制，四种角色

全部是同一个 `ks`，按子命令决定角色。

| 命令 | 运行在 | 作用 |
|---|---|---|
| `ks hub` | 一台常开的机器 | 接收事件、推导每个会话的状态、存储（SQLite）、网页与 API、通知、摘要 |
| `ks daemon` | 每台开发设备 | 汇聚本机 hook 事件、断网时缓存并在恢复后补传、采集 git 状态、缓存待注入的决策 |
| `ks hook <claude\|codex> <event>` | 由 agent 调用 | daemon 的薄客户端，经 unix socket 通信 |
| `ks mcp` | 由 agent 拉起 | agent 调用的需求与决策工具 |

daemon 主动通过 WebSocket 连到 Hub，Hub 不需要反向连到你的设备——NAT 后面、在外面或合盖睡眠的笔记本，重新联网后会自动重连并补传。

## 会话状态从哪来

会话状态是被动采集的，不需要 agent 主动汇报。

- **Hooks**：`ks install` 为 Claude Code 与 Codex 的生命周期事件（会话开始 / 结束、提交 prompt、工具调用、权限请求、一轮结束）注册 `ks hook`。每次 hook 都把事件交给本机 daemon，立即返回。
- **会话文件**：daemon 只读地读取 agent 的本地会话记录，得到上下文占用与最近活动。
- **Git**：daemon 上报每个会话工作目录的分支、worktree 与未提交文件数。会话按 git remote 归并为项目，没有 remote 时按设备 + 路径区分。

Hub 据此推导你在看板上看到的状态：运行中、等你处理（权限确认或提问）、待命中、已结束、失联。

**Hook 永远不挡路**：`ks hook` 有 1.5 秒硬超时，出任何错都放行，daemon 没运行也一样；`ks` 永远不以退出码 2 退出（agent 会把它当成“阻止”）。

## 决策如何回到 agent

1. agent 遇到需要你拍板的事，调用 `decision_raise`，附上选项和它的推荐。
2. 决策进入你的收件箱，并推送到你的手机或浏览器。
3. 你在看板上作答。Hub 把答复路由到提出它的那个会话，下发到该设备的 daemon。
4. 你下次给这个会话发消息时，prompt hook 把答复注入 agent 的上下文（`additionalContext`）。会话已结束的，恢复时送达。

<p align="center"><img src="assets/screenshot-mobile-light.png" width="300" alt="手机上的决策：批准或拒绝，可附言。"></p>

需求同理：agent 通过 MCP 创建、更新需求；你确认草稿、把需求交给会话、验收。

## 挂起等待（ks wait）

Agent 做完一段、需要你验收或答复才能继续时，可以运行 `ks wait` 挂起：你在 Kitestring 上答复、验收、退回修改、留言或交给它新需求时，它立刻带着内容醒来继续，不用回到终端。只用于等人，不做定时或自动续跑。

| | Claude Code | Codex |
|---|---|---|
| 运行方式 | 后台任务：这一轮正常结束，`ks wait` 退出时 Claude Code 唤醒会话 | 前台命令：Codex 没有“后台命令结束后唤醒”，这一轮一直开着等命令结束 |
| 最长等待 | 默认 1 小时，`--timeout` 可调，最长 12 小时 | 默认、也最多 30 分钟 |
| 额度消耗 | 等待期间不消耗 | Codex 会定期查看命令输出，每次消耗少量额度（主要是缓存读取），所以限制在 30 分钟 |
| 超时后 | 安静结束，你回来后在对话里继续 | 提示 Agent 收尾：说明进度和在等什么，结束本轮、不再续等；之后的答复在下次对话时送达 |

工作台上挂起的会话显示为“待命中”，并显示已等多久、最长等到何时。

## Agent 工具

`ks mcp` 是本地 stdio MCP server，由 `ks install` 注册给 Claude Code 与 Codex。它通过父进程链识别是哪个会话在调用，agent 不需要传会话 ID。

| 工具 | 用途 |
|---|---|
| `req_list` `req_get` | 查看项目的需求 |
| `req_create` | 记下新需求（你确认前是草稿） |
| `session_link_req` | 标明本会话正在做某条需求 |
| `req_update_status` | 推进需求状态，例如改为待验收 |
| `evidence_attach` | 为验收附上测试结果、提交、截图或录屏 |
| `decision_raise` | 请你拍板，附选项、推荐与方案图 |
| `plan_create` `plan_get` `stage_submit` | 把大目标拆成阶段、查看进度、提交完成的阶段 |

同样的操作也有命令行版本（`ks req`、`ks plan`、`ks decision`、`ks wait`，见 `ks --help`）；agent 的 MCP 连接还是旧版本时可以改用它。

## 账号安全边界

Kitestring 只使用 Claude Code / Codex 官方文档化的扩展点——hooks、`additionalContext`、MCP、只读的本地会话文件——不修改 agent 二进制。

- 永不读取、存储或转发 `~/.claude`、`~/.codex` 与 keychain 中的凭证。
- 不代理模型流量，自身任何功能都不消耗 Claude / ChatGPT 订阅额度。AI 摘要是可选的，只调用你单独配置的 OpenAI 兼容接口。
- 不远程驱动 agent：不从网页向实例发 prompt，不做 `--resume` 自动化、tmux 送键，不做定时或链式触发。agent 只在你发起的回合里行动。

依据见 [decisions.md](decisions.md) D13、D14。

## 数据与安全

- 本机数据都在 `~/.kitestring`（可用 `KS_HOME` 覆盖）：设备 token、daemon socket 与离线队列、日志；Hub 所在机器还有 SQLite 数据库与上传的文件。
- 每台设备一个独立 token，可在「设置 → 设备」中吊销。
- 网页登录用密码（bcrypt，登录失败限流）或 passkey，会话 cookie 为 HttpOnly。Hub 暴露到公网时请放在 HTTPS 后面。

## 延伸阅读

| 文档 | 内容 |
|---|---|
| [requirements.md](requirements.md) | MVP 需求草案：定位、架构、视图、数据模型、hook 可行性 |
| [decisions.md](decisions.md) | 已确认决策台账与待决问题 |
| [hook-matrix.md](hook-matrix.md) | Claude Code / Codex hook 能力实测矩阵与状态推导规则 |
| [landscape.md](landscape.md) | 同类开源项目调研与差异化 |
| [naming.md](naming.md) | 命名过程、淘汰候选与原因 |
