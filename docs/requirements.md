# Kitestring 需求草案（MVP）

状态：草案，随 [decisions.md](decisions.md) 演进。

## 1. 目标用户与场景

一个开发者同时：

- 拥有多台设备，可能分处不同地理位置（例如办公笔记本 + 家里常开的 Mac mini）；
- 混用多种 agent 与客户端：Claude Code（终端 / cmux / 手机远程桥）、Codex（桌面 App、CLI、IDE 插件、远程 SSH）、经代理路由的多个模型；
- 同时推进公司项目与个人项目；
- 经常让多个实例并行、让 agent 整夜自主推进，第二天收结果。

典型痛点：

- **需求散落**：聊天里的 prompt、粘贴的问题清单、截图、issue、业务系统反馈、仓库内 md 台账各自为政，容易漏需求。
- **实例不可见**：不知道另一个实例在做什么、是否在等授权或回复、是否已经卡住；常需要手工让实例“了解另一个实例的进度”。
- **决策积压**：agent 抛出的决策项埋在长对话里，人需要反复要求“汇总一下还有哪些没决策”。
- **上下文过载**：长对话信息太多读不过来，需要不断要求重新汇总。

## 2. 设计原则

1. **被动采集优先**：实例状态来自 hook 与会话文件，不依赖 agent 自觉上报。旧方案（让 agent 按 30 条协议主动写表）在同步成本与表膨胀上失败。
2. **主动写接口极小**：agent 只需写需求状态、提出决策、附证据、关联会话。
3. **人是唯一的 PM**：系统帮人看全局、做决策，不做自动派工的“PM agent”。
4. **隐私默认最小**：只存元数据与摘要，不存完整对话和代码。
5. **跨地域推送**：设备主动推送到中心 Hub，适配公网 / NAT，不要求 Hub 能 SSH 到各设备。

## 3. 架构

技术栈与形态见 [decisions.md](decisions.md) D12。所有角色由同一个二进制 `ks` 的子命令承担。

```
[设备 A]
  Claude / Codex ──hook──> ks hook ──unix socket──┐
  Claude / Codex ──stdio─> ks mcp ───unix socket──┤
  会话文件 / git 状态 ─────────────────────────────┤
                                                  v
                                   ks daemon（离线队列、待接收决策缓存）
                                                  │ WebSocket（双向，事件幂等补传）
[设备 B]  同上 ──────────────────────────────────┤
                                                  v
                                       ks hub（nexus-mini）
                          事件流 / SQLite / 摘要（OpenAI 兼容接口）/ 通知路由
                                                  │
                         ┌────────────────────────┼──────────────┐
                         v                        v              v
                 Web（PWA，内嵌于二进制）       企微 / Bark     决策下行到 daemon
```

组件：

- **Hub（`ks hub`）**：事件接收、存储、状态推导、摘要生成（可配置）、通知路由、REST + SSE、托管 Web。
- **daemon（`ks daemon`）**：设备常驻核心。汇聚本机 hook 事件；尾随会话文件；采集 cwd 的 git 分支 / worktree / 脏文件数；断网缓存、恢复补传；从 Hub 同步并缓存待接收决策，供 hook 低延迟读取。
- **hook（`ks hook <event>`）**：daemon 的薄客户端，只和本机 daemon 通信；设硬超时，超时即放行，宁可不注入也不阻塞用户输入。
- **agent 接口（`ks mcp` + CLI，同一套能力）**：
  - `req list / get`
  - `req update-status`
  - `decision raise`（问题、选项、推荐、关联需求）
  - `evidence attach`
  - `session link-req`

## 4. MVP 视图

### 4.1 实例墙（P0）

按设备分组的实例卡片：工具（Claude Code / Codex）、客户端、模型、项目、分支、状态、最后一条 prompt（截断）、一句话进展摘要、关联需求、最后活跃时间。

状态集：运行中 / 等待授权 / 等待回复 / 空闲 / 已结束 / 失联（采集器心跳超时）。

### 4.2 决策收件箱（P0）

- 跨项目、跨实例汇总所有待决项，每项带背景、选项、agent 推荐、来源实例与需求。
- 支持批量作答（对应“1. 确认 2. 不加 3. 按推荐”这类习惯）。
- 三态：待决策 → 已决策待接收 → 已接收（agent 下一次注入并确认后自动流转）。
- 用户推翻已验收结论时，旧证据标记作废。

### 4.3 需求看板（P1，最简）

按工作空间（公司 / 个人）→ 项目分组；字段：标题、状态、优先级、nextActor、来源、关联会话、关联方案文档链接、证据。

### 4.4 后置

时间线 / 日报（“今天下午做了什么”“昨晚整夜跑了什么”）、额度面板、外部需求源导入。

远程派工与自动续跑不做，见 decisions.md D14。

## 5. 决策回流：hook 注入可行性

| 能力 | Claude Code | Codex |
|---|---|---|
| `SessionStart` 注入上下文 | 支持（`hookSpecificOutput.additionalContext`） | 支持：0.163.0-alpha 二进制中存在 `SessionStartHookSpecificOutputWire` 与 `additionalContext` |
| `UserPromptSubmit` 注入上下文 | 支持 | 支持：存在 `UserPromptSubmitHookSpecificOutputWire`；另有 `additionalContextLimit` 配置项 |
| 等待授权信号 | `Notification` / `PermissionRequest` 类事件 | `PermissionRequest` |
| 回合结束信号 | `Stop` / `SubagentStop` | `Stop` |
| 等待回复信号 | `Notification` | 无直接事件，待验证（见 decisions.md Q5） |

注入策略（见 decisions.md D15，实测见 hook-matrix.md）：

1. 用户在 Web 上完成决策后，决策进入“已决策待接收”。
2. 每条决策只有一个目标会话，即提出它的会话（通过 D17 精确识别）。Hub 把决策下行到该会话所在设备的 daemon 缓存。
3. 该会话下一次 `UserPromptSubmit` 时，hook 从本机 daemon 读取并以 `additionalContext` 注入；成功输出即标记“已接收”，同一决策不重复注入，可在 Web 上手动重发。
4. 该会话恢复或上下文压缩时（`SessionStart.source` 为 `resume` / `compact`），补注入其近期已接收的决策。
5. 来源会话已结束时，决策停在“已决策待接收”并在 Web 标出，由用户手动转交或关闭。
6. 注入文本需控制长度，遵守 Codex 的 `additionalContextLimit`（缺省约 2,500 token）。

待原型验证：npm 版 Codex CLI（0.159.x）与桌面 App 内置 CLI 的 hook 行为是否一致；cmux 通过 `--settings` 启动 Claude 时，用户级 hooks 是否仍然生效。

## 6. 数据模型骨架

- `Workspace`（公司 / 个人；通知路由在此或项目级配置）
- `Project`（git remote、各设备本地路径映射、方案文档根路径）
- `Requirement`（标题、状态、优先级、nextActor、来源、验收标准）
- `Decision`（问题、选项、推荐、状态、用户答复、注入记录）
- `Evidence`（类型、摘要、链接、是否作废）
- `Device`（名称、平台、最后心跳、token）
- `Session`（工具、客户端、模型、cwd、分支、状态、开始 / 最后活跃时间、摘要）↔ `Requirement` 多对多
- `Event`（底层事件流，用于状态推导与时间线）

## 7. 非目标

- 不替代 Claude Code / Codex 的对话界面，不做手机端远程操控（已有 Happy、Omnara、Mimi Remote 等）。
- 不做由看板卡片驱动 agent 执行的任务编排（Vibe Kanban 的方向）。
- 不让 agent 承担 PM 角色和定时巡检。
- 不读取、存储、转发 agent 的登录凭证，不代理模型流量，自身功能不消耗 Claude / ChatGPT 订阅额度（见 decisions.md D13）。
