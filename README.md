# 纸鸢 · Kitestring

> 风筝飞得再远，线在你手里。

Kitestring 是面向 vibe coding 场景的个人驾驶舱：一个人、多台设备、多个 Claude Code / Codex 实例同时干活时，用一个 Web 看板回答四个问题——

1. 现在有哪些需求，各自到了哪一步？
2. 哪些 agent 实例正在跑、在哪台设备、在做什么？
3. 哪些事情在等我（授权、回复、决策）？
4. 一段时间内（今天下午、昨晚整夜）实际推进了什么？

它不替代 Claude Code / Codex 的交互界面，也不做“PM agent 自动驾驶”。实例状态靠设备采集器被动获取，只有需求与决策由 agent 通过极小的 MCP / CLI 接口主动写入；人做的决策通过 hook 直接注入回 agent 上下文。

当前阶段：**MVP 开发中**（实例墙、决策收件箱、最简需求看板）。计划在 NekroAI 组织下开源，独立品牌，不带 `nekro-` 前缀。

## 账号安全边界

Kitestring 只使用 Claude Code / Codex 官方文档化的扩展点（hooks、`additionalContext`、MCP），不修改 agent 二进制，不读取、存储或转发任何登录凭证，不代理模型流量，自身功能不消耗 Claude / ChatGPT 订阅额度；AI 摘要只调用你单独配置的 OpenAI 兼容接口。不提供远程派工或自动续跑。详见 [docs/decisions.md](docs/decisions.md) D13、D14。

## 组成

一个二进制 `ks`，按子命令承担不同角色：

| 命令 | 运行在 | 作用 |
|---|---|---|
| `ks hub` | 一台常开的机器 | 接收事件、推导状态、存储（SQLite）、Web 界面与 API、通知、摘要 |
| `ks daemon` | 每台开发设备 | 汇聚本机 hook 事件、离线缓存补传、采集 git 状态、缓存待注入的决策 |
| `ks hook <claude\|codex> <event>` | 由 agent 调用 | daemon 的薄客户端，1.5 秒硬超时，失败静默放行 |
| `ks mcp` | 由 agent 拉起 | 需求与决策工具：`req_list` `req_get` `req_create` `req_update_status` `evidence_attach` `decision_raise` `session_link_req` |

## 快速上手

```bash
# 构建（需要 Go 1.27+、Node 22+、pnpm）
cd web && pnpm install && pnpm build && cd ..
make build                      # 产物：bin/ks（已内嵌 Web 前端）

# Hub 所在机器
./bin/ks hub passwd             # 设置 Web 登录密码
./bin/ks hub serve              # 默认监听 0.0.0.0:7310，浏览器打开即可登录
#   或作为常驻服务：./bin/ks install --hub --claude=false --codex=false --daemon=false

# 每台开发设备（在 Web 设置页「设备」中新增设备，拿到 token）
./bin/ks login --hub http://<hub 地址>:7310 --token kst_xxx
./bin/ks install --dry-run      # 预览将写入的 Claude / Codex 配置与常驻服务
./bin/ks install                # 写入前自动备份；卸载：./bin/ks uninstall
#   Codex 需要你亲自在 codex 中执行 /hooks，信任 ks 的各条 hook
```

所有本机数据位于 `~/.kitestring`（可用 `KS_HOME` 覆盖）。

## 挂起等待（ks wait）

Agent 做完一段、需要你验收或答复才能继续时，可以运行 `ks wait` 挂起：你在 Kitestring 上答复、验收、退回修改、留言或交给它新需求时，它立刻带着内容醒来继续，不用回到终端。只用于等人，不做定时或自动续跑。

| | Claude Code | Codex |
|---|---|---|
| 运行方式 | 后台任务：这一轮正常结束，`ks wait` 退出时 Claude Code 唤醒会话 | 前台命令：Codex 没有“后台命令结束后唤醒”，这一轮一直开着等命令结束 |
| 最长等待 | 默认 1 小时，`--timeout` 可调，最长 12 小时 | 默认、也最多 30 分钟 |
| 额度消耗 | 等待期间不消耗 | Codex 会定期查看命令输出，每次消耗少量额度（主要是缓存读取），所以限制在 30 分钟 |
| 超时后 | 安静结束，你回来后在对话里继续 | 提示 Agent 收尾：说明进度和在等什么，结束本轮、不再续等；之后的答复在下次对话时送达 |

工作台上挂起的会话显示为“待命中”，并显示已等多久、最长等到何时。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/requirements.md](docs/requirements.md) | MVP 需求草案：定位、架构、视图、数据模型、hook 可行性 |
| [docs/decisions.md](docs/decisions.md) | 已确认决策台账与待决问题（上下文恢复事实源） |
| [docs/hook-matrix.md](docs/hook-matrix.md) | Claude Code / Codex hook 能力实测矩阵与状态推导规则 |
| [docs/landscape.md](docs/landscape.md) | 同类开源项目调研与差异化 |
| [docs/naming.md](docs/naming.md) | 命名过程、淘汰候选与原因 |
| `notes/private/` | 作者本人的使用画像、旧看板复盘等含私有信息的背景资料（已 git 忽略，不进开源仓库） |
