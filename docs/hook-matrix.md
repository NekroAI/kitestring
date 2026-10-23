# Hook 能力矩阵（M1 探针结论）

验证日期：2026-10-22。方法：临时项目内配置项目级 hook（Claude `.claude/settings.json`、Codex `.codex/hooks.json`），hook 命令为一次性探针，记录 stdin 输入、环境变量名、父进程链，并在 `SessionStart` / `UserPromptSubmit` 回显带随机标记的 `additionalContext`，再让模型逐字复述标记以确认注入生效。

被测版本：Claude Code 2.1.285；Codex CLI 0.159.3（独立安装包）；Codex CLI 0.163.0-alpha.10.2（ChatGPT 桌面 App 内置）。

## 1. 注入

| 能力 | Claude Code 2.1.285 | Codex 0.159.3 | Codex 0.163.0-alpha |
|---|---|---|---|
| `SessionStart` 注入 `additionalContext` | ✅ 实测 | ✅ 实测 | ✅ 实测 |
| `UserPromptSubmit` 注入 `additionalContext` | ✅ 实测 | ✅ 实测 | ✅ 实测 |
| 注入长度上限 | 未见限制说明 | `additionalContextLimit`，缺省约 2,500 token，超出部分落盘（官方文档，未实测） | 同左 |

## 2. 输入字段

| 字段 | Claude Code | Codex（两版本一致） |
|---|---|---|
| `session_id` / `cwd` / `transcript_path` / `hook_event_name` | 所有事件都有 | 所有事件都有 |
| `model` | ❌ 无，需从 transcript 补 | ✅ 除 `SessionEnd` 外都有 |
| 回合标识 | `prompt_id` | `turn_id` |
| `permission_mode` | ✅ | ✅ |
| `SessionStart.source` | ✅ | ✅（startup / resume / clear / compact / fork） |
| `Stop.last_assistant_message` | ✅ | ✅（两边都随 Stop 上送，截到 2 万字，存为会话的“最后回复”，R-109） |
| `PermissionRequest` | ✅ 实测（含 `tool_input`、`permission_suggestions`） | ✅ 实测（0.159.3，需审批的 MCP 工具调用触发） |
| `Notification` | ✅ 官方事件（本次未触发） | ❌ 无此事件；另有 `Interrupt` |

## 3. 进程与会话关联

| 项 | Claude Code | Codex |
|---|---|---|
| hook 进程的父进程 | agent 进程本身 | CLI 直跑时为 agent 进程；**经 app-server 运行时为共享的 app-server 进程**（一个进程承载多个会话） |
| MCP 子进程环境里的会话标识 | ✅ `CLAUDE_CODE_SESSION_ID`，与 hook 的 `session_id` 一致 | ❌ Codex 给 MCP 子进程的环境是精简过的，没有会话标识 |
| MCP `tools/call` 请求的 `_meta` | `claudecode/toolUseId`，等于同一调用 `PreToolUse` hook 的 `tool_use_id` | `sessionId`（= hook 的 `session_id`）、`threadId`、`callId`（= hook 的 `tool_use_id`）、`x-codex-turn-metadata`（含 `model`、`codex_version` 等）；0.159.3 与 0.163.0-alpha 一致 |
| MCP 工具调用是否触发 `PreToolUse` / `PostToolUse` | ✅，`tool_name` 为 `mcp__<server>__<tool>` | ✅，同左；`tool_input` 与 MCP 收到的参数完全一致 |

实测：同一目录下并发两个 Codex 会话各调用一次 MCP 工具，`_meta.sessionId` 与各自 hook 的 `session_id` 一一对应，无串扰。

结论（Q9）：MCP 调用可以精确关联会话，不需要进程推断。
- Codex：直接读 `_meta.sessionId`。
- Claude：读环境变量 `CLAUDE_CODE_SESSION_ID`；`_meta.claudecode/toolUseId` 可与 `PreToolUse` hook 的 `tool_use_id` 交叉校验。
- 兜底：按 `tool_use_id` / `callId` 与 `PreToolUse` hook 对齐（hook 先于 MCP 调用约 15–35ms 到达）。

## 4. 性能

`ks` 探针（Go 静态二进制，含一次 `ps` 调用）单次冷启动约 18ms，远低于感知阈值。

## 5. 状态推导规则（Q5 结论）

两边都以 `Stop` 作为“回合结束、等待回复”的统一信号，不依赖 Claude 独有的 `Notification`：

| 事件 | 推导状态 |
|---|---|
| `SessionStart` | 空闲 |
| `UserPromptSubmit` | 运行中 |
| `PreToolUse` / `PostToolUse` | 运行中（刷新活跃时间） |
| 子 Agent 的 `PreToolUse` / `PostToolUse`（Claude Code 输入带 `agent_id`，`session_id` 仍是主会话的） | 不改主会话状态，只刷新活跃时间；子 Agent 数由 `SubagentStart` / `SubagentStop` 计数，待命时显示「待命 · 后台 n 个任务」（R-115，2.1.285 实测输入 schema：`agent_id` 只在子 Agent 内触发时出现） |
| `PermissionRequest` | 等待授权（直到同一工具的 `PostToolUse`、`Stop` 或 `Interrupt`） |
| `Stop` | 等待回复 |
| `Interrupt`（Codex） | 等待回复 |
| `Notification`（Claude） | 按类型补充：权限提示 → 等待授权；空闲提示 → 等待回复 |
| `SessionEnd` | 已结束（后台任务清空） |
| `KsTasks`（daemon 合成） | 不改状态：替换会话的后台 shell 全集（R-162） |
| daemon 心跳超时 | 失联 |

## 6. 其他观察

- Codex 的 MCP 工具在审批策略为 `never` 时会被自动拒绝；`ks install` 需要为 Kitestring 自己的工具配置 `[mcp_servers.<name>.tools.<tool>] approval_mode = "approve"`，或引导用户确认。
- Codex 项目级 hook 与项目级 MCP 都需要用户先信任项目、并在 `/hooks` 中逐条信任；未信任时静默跳过，不报错。`ks install` 需要引导用户完成这一步，不应使用绕过信任的参数。
- Codex 用户级与项目级 hook 叠加执行，不互相覆盖。
- Claude `--resume` 恢复会话后 `session_id` 保持不变（M2 实测），Codex 文档同样说明恢复为同一会话。
- hook 退出码 2 在两边都表示“阻止”：hook 命令配错导致进程以 2 退出时会拦住用户的 prompt（M2 实测），因此 `ks` 永不以 2 退出。
- Codex 0.163.0-alpha 的 `exec` 在 stdin 不是终端时会等待 stdin 结束，与 hook 无关，仅影响测试脚本。

## 7. Stop hook 拦下结束（R-83 实测）

验证日期：2026-10-23。被测版本：Claude Code 2.1.285；Codex CLI 0.159.3。方法：会话执行 `ks role pm|dev` 后直接结束本轮，由 `ks hook <tool> Stop` 输出 `{"decision":"block","reason":"…"}`。

| 项 | Claude Code 2.1.285 | Codex 0.159.3 |
|---|---|---|
| `decision: block` 拦下结束 | ✅ 实测：本轮继续，`reason` 作为反馈交给模型（界面显示为 “Stop hook error: …”） | ✅ 实测：本轮继续，`reason` 以 `hook_prompt` 注入 |
| 被拦后按提示挂上 `ks wait` | ✅ 后台运行，第二次 Stop 放行（交互式与 `-p` 均实测） | ✅ 前台运行；同一轮内继续 |
| `ks wait` 超时后续等 | ✅ 交互式实测：后台任务结束触发新一轮，Agent 按超时提示重新挂上 | ✅ 每次前台等待结束后 Stop 再被拦，同一轮内反复续等 |

结论：
- 两边都能用 Stop hook 保证 PM / 开发常驻在线。Kitestring 的规则：身份为 PM 或开发、daemon 上没有该会话的 `ks wait` 时拦下（先宽限 1 秒，避开后台 `ks wait` 刚启动的竞态）；挂上 `ks wait` 或用户发新消息时计数清零，连续拦 2 次仍不挂就放行。被拦下的那次 Stop 不上报 Hub，网页不会闪成“等你回复”。
- Codex 的代价：它没有后台任务，常驻等于“一轮永不结束”，每次等待结束（最长 30 分钟）都要带着当前上下文再调一次模型。实测 1 分钟一续，约 11 次累计输入 74 万 token（97% 命中缓存）；按 30 分钟一续，一天约 48 次。规模小的项目可以用 Codex 做开发会话；PM 推荐用 Claude Code（后台等待，等待期间不调模型）。
- 没有身份的会话不受影响（e2e：`TestResidentStopGuard`）。

## 8. 后台任务（R-162）

hook 不会告诉我们后台 shell 什么时候结束，子代理也只有启停事件。纸鸢这样采集，只记类型、标签、开始时间与最近动作，不读命令输出与对话内容：

| 类型 | 来源 |
|---|---|
| 子代理 | `SubagentStart` / `SubagentStop` 的 `agent_id`；标签取父会话 `Agent` / `Task` 调用的 `description`（按 `subagent_type` 先进先出对上）；子代理内的 `PreToolUse` 记为它的最近动作与时间线刻度，不进主会话的最近动作。没收到 `SubagentStop` 的 30 分钟无动作后不再显示 |
| 后台 shell | daemon 每 3 秒扫一次进程表，取 agent 进程（Claude Code / Codex）的 sh / bash / zsh / fish 子进程：命令对得上的前台调用（`PreToolUse` 到 `PostToolUse` 之间）不算，ks hook / ks wait（待命）/ ks mcp 与 MCP 服务不算，活过 2 秒的其余 shell 算后台任务；Claude 的 `run_in_background` 用其 `description` 做标签，否则取命令（截断 80 字）。进程消失即结束，有变化时上报 `KsTasks`（全集） |

Codex 经 app-server / 桌面 App 运行时一个进程承载多个会话，分不清后台 shell 归属，不显示；会话面板注明“这类会话无法识别后台任务”。
