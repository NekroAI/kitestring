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
| `Stop.last_assistant_message` | ✅ | ✅ |
| `PermissionRequest` | ✅ 实测（含 `tool_input`、`permission_suggestions`） | 官方文档列出；本次因审批策略为免审批未触发，未实测 |
| `Notification` | ✅ 官方事件（本次未触发） | ❌ 无此事件；另有 `Interrupt` |

## 3. 进程与会话关联

| 项 | Claude Code | Codex |
|---|---|---|
| hook 进程的父进程 | agent 进程本身 | CLI 直跑时为 agent 进程；**经 app-server 运行时为共享的 app-server 进程**（一个进程承载多个会话） |
| MCP 子进程环境里的会话标识 | ✅ `CLAUDE_CODE_SESSION_ID`，与 hook 的 `session_id` 一致 | ❌ Codex 给 MCP 子进程的环境是精简过的，没有会话标识 |
| MCP 子进程的父进程 | agent 进程 | agent 进程（CLI 直跑时与 hook 的父进程相同） |

结论：Claude 侧 `ks mcp` 可直接从环境变量拿到会话；Codex 侧只能在 CLI 直跑时用父进程 PID 关联，经 app-server（桌面 App、IDE 插件等）时一个 PID 对应多个会话，需要别的办法（见 decisions.md Q9）。

## 4. 性能

`ks` 探针（Go 静态二进制，含一次 `ps` 调用）单次冷启动约 18ms，远低于感知阈值。

## 5. 状态推导规则（Q5 结论）

两边都以 `Stop` 作为“回合结束、等待回复”的统一信号，不依赖 Claude 独有的 `Notification`：

| 事件 | 推导状态 |
|---|---|
| `SessionStart` | 空闲 |
| `UserPromptSubmit` | 运行中 |
| `PreToolUse` / `PostToolUse` | 运行中（刷新活跃时间） |
| `PermissionRequest` | 等待授权（直到同一工具的 `PostToolUse`、`Stop` 或 `Interrupt`） |
| `Stop` | 等待回复 |
| `Interrupt`（Codex） | 等待回复 |
| `Notification`（Claude） | 按类型补充：权限提示 → 等待授权；空闲提示 → 等待回复 |
| `SessionEnd` | 已结束 |
| daemon 心跳超时 | 失联 |

## 6. 其他观察

- Codex 项目级 hook 与项目级 MCP 都需要用户先信任项目、并在 `/hooks` 中逐条信任；未信任时静默跳过，不报错。`ks install` 需要引导用户完成这一步，不应使用绕过信任的参数。
- Codex 用户级与项目级 hook 叠加执行，不互相覆盖。
- Codex 0.163.0-alpha 的 `exec` 在 stdin 不是终端时会等待 stdin 结束，与 hook 无关，仅影响测试脚本。
