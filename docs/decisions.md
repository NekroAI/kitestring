# 决策台账

状态：需求定义中。本文是 Kitestring 需求阶段的上下文恢复事实源，新增或改变决策时同步更新。

## 已确认决策

| # | 决策 | 确认时间 |
|---|---|---|
| D1 | **需求真相源**：需求清单、状态、决策以 Kitestring 数据库为准，跨项目、跨设备统一；技术方案仍写在各项目仓库的 md 中，看板只存链接。agent 通过 MCP 写状态 | 2026-10-22 |
| D2 | **Hub 部署**：运行在作者家里云设备 nexus-mini 上；该设备有 DDNS 公网动态 IP 并可映射服务，服务可用后由作者手动配置公网访问 | 2026-10-22 |
| D3 | **采集深度**：只采集元数据 + AI 一句话摘要（状态、cwd、分支、模型、最后一条 prompt 截断、进展摘要），不上传完整对话与代码 | 2026-10-22 |
| D4 | **MVP 范围**：实例墙 + 决策收件箱优先；需求看板只做最简版本；时间线/日报、额度面板后置；远程派工与自动续跑暂不做（见 D14） | 2026-10-22 |
| D5 | **会话 ↔ 需求关联**：以 agent 主动调用（`session link-req`）为主，Web 端可手动调整 | 2026-10-22 |
| D6 | **决策回流**：人在 Web 上做完的决策直接通过 hook 注入 agent 上下文，必须同时兼容 Claude Code 与 Codex（可行性见 requirements.md 第 5 节） | 2026-10-22 |
| D7 | **AI 摘要**：生成位置（设备本地 / Hub）与所用模型可手动配置；模型通过 OpenAI 兼容的通用接口接入（`base_url` + `api_key` + `model`），由用户单独配置，不使用 Claude / ChatGPT 订阅额度；未配置时退化为“最后一条 prompt 截断 + 规则推导状态” | 2026-10-22 |
| D8 | **通知**：企业微信与 Bark 都支持，按工作空间 / 项目手动配置路由（例：公司项目走企微，个人项目走 Bark） | 2026-10-22 |
| D9 | **开发位置**：MVP 在 nexus-mini 的 `~/Projects/kitestring` 开发 | 2026-10-22 |
| D10 | **命名**：纸鸢 / Kitestring；在 NekroAI 组织下开源，独立品牌，不带 `nekro-` 前缀（过程见 naming.md） | 2026-10-22 |
| D11 | **开源边界**：含公司项目、个人设备与账号细节的背景资料放 `notes/private/` 并 git 忽略；`docs/` 只放可公开内容 | 2026-10-22 |
| D12 | **技术栈与形态**：核心为 Go 单二进制 `kitestring`（简称 `ks`），按子命令分角色——`hub`（API + SSE + 内嵌 Web + 定时任务 + 通知）、`daemon`（设备常驻：hook 汇聚、会话文件尾随、git 采集、离线队列、到 Hub 的双向 WebSocket）、`hook <event>`（daemon 的薄客户端，经 unix socket 通信，硬超时放行）、`mcp`（本地 stdio MCP，借父进程链关联会话）、`req` / `decision` 等 CLI、`install`。Web 用 TypeScript + React + Vite（PWA），构建产物 `go:embed` 进二进制；API 契约由 Go 类型生成 OpenAPI，前端据此生成类型。Hub 存储为 SQLite，底层为事件流，数据访问层预留切换 Postgres 的余地；数据模型预留 owner 维度但不实现多用户。选型依据：设备端不能假设有 Node 运行时、需要极快的 hook 冷启动与免依赖交叉编译，系统以 I/O 胶水为主 | 2026-10-22 |
| D13 | **账号安全边界**（主力使用 Claude / ChatGPT 订阅）：只使用官方文档化的扩展点（hooks、`additionalContext`、MCP、本地会话文件只读）且不修改 agent 二进制；永不读取、存储或转发 `~/.claude`、`~/.codex` 中的凭证与 keychain 项，不充当模型流量代理；Kitestring 自身的任何功能都不消耗订阅额度；Codex app-server 如接入只做只读状态读取。依据：Claude Code Legal and compliance 页（2026-10-07 版）的 Authentication and credential use 一节，及 OpenAI 关于第三方 Codex 客户端使用 Sign in with ChatGPT 的公开表态。开源 README 需公开声明此边界 | 2026-10-22 |
| D14 | **不做远程派工与自动续跑**：不从 Web 向实例发送消息，不经 `claude -p --resume`、tmux 送键或 Codex app-server 驱动新回合，不做定时 / 链式自动触发。如将来重新考虑，须逐次由人触发，并在开工前按当时政策重新评估 | 2026-10-22 |
| D15 | **决策注入路由（原 Q4）**：每条决策只有一个目标会话，即提出它的会话，同项目其他会话不会收到；在该会话的下一次 `UserPromptSubmit` 注入，hook 成功输出即标记“已接收”并记录 `prompt_id` / `turn_id`；同一会话恢复（`SessionStart.source=resume`）或上下文压缩（`source=compact`）时补注入该会话近期已接收的决策；来源会话已结束的决策停在“已决策待接收”并在 Web 标出，由用户手动转交其他会话或关闭，系统不自动猜测目标；但同一会话被恢复（Claude 已实测 `--resume` 后 `session_id` 不变）时仍会收到结束后才作答的决策 | 2026-10-22 |
| D16 | **项目归并（原 Q6）**：按 git remote URL 归并为同一项目；无 remote 时按设备 + 路径区分 | 2026-10-22 |
| D17 | **MCP 会话关联（原 Q9）**：Codex 读 MCP 请求 `_meta.sessionId`；Claude 读 MCP 子进程环境变量 `CLAUDE_CODE_SESSION_ID`；两边都用 `tool_use_id` / `callId` 与 `PreToolUse` hook 交叉校验（实测见 hook-matrix.md 第 3 节） | 2026-10-22 |
| D18 | **鉴权（原 Q1）**：设备侧每台设备一个可吊销的独立 token；Web 端 MVP 用单用户密码（bcrypt）+ HttpOnly 会话 cookie，局域网 http 可用；passkey 待配好 HTTPS 域名后再加 | 2026-10-22 |
| D19 | **需求状态（原 Q8）**：旧看板仅作参考，按新定位重设为 7 态——`草稿`（只有标题，未明确）｜`待开始`｜`进行中`｜`阻塞`｜`待验收`（agent 认为完成，等用户确认）｜`已完成`｜`搁置`。不再设人工维护的 `nextActor` 字段，改为推导：有待决决策或处于 `待验收` → 轮到用户；`进行中` → 轮到 agent。“等待决策”由关联的待决决策推导显示，不单设状态。用户把 `待验收` 退回时，该需求已有证据标记作废 | 2026-10-22 |
| D20 | **界面语言**：MVP 只做中文，文案集中在一处，预留多语言，开源前补英文 | 2026-10-22 |
| D21 | **MVP 安装方式**：开发期只在测试项目中端到端验证；在 nexus-mini 上的真实安装（launchd 常驻、写入全局 Claude / Codex 配置）等作者在场时一起完成，改动前备份 | 2026-10-22 |
| D22 | **开源与发版**：许可证 AGPL-3.0-or-later；仓库 `NekroAI/kitestring` 先私有，作者检查后手动公开；推送 `v*` tag（须与 `VERSION` 一致）后由 GitHub Actions 交叉编译 darwin / linux × arm64 / amd64，连同 `SHA256SUMS.txt` 发到 Release（非预发布，供 `releases/latest` 使用）；设备通过 `scripts/install.sh` 在线安装到 `~/.local/bin/ks`。README 英文为主、另附 `README.zh-CN.md`，`docs/` 仍为中文 | 2026-10-23 |

## 待决问题

| # | 问题 | 当前倾向 |
|---|---|---|
| Q7 | 是否导入外部需求源（GitHub issue、业务系统反馈、截图批量） | MVP 不做，留接口 |

已关闭：Q2（技术栈）→ D12；Q3（采集器形态）→ D12，结论为 daemon 是核心组件，hook 只是它的薄客户端；Q4 → D15；Q5 → 状态推导规则见 hook-matrix.md 第 5 节；Q6 → D16；Q9 → D17；Q1 → D18；Q8 → D19。
