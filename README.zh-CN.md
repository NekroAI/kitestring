# 纸鸢 · Kitestring

> 风筝飞得再远，线在你手里。

<p>
  <a href="https://github.com/NekroAI/kitestring/actions/workflows/ci.yml"><img src="https://github.com/NekroAI/kitestring/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/NekroAI/kitestring/releases/latest"><img src="https://img.shields.io/github/v/release/NekroAI/kitestring" alt="Release"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey" alt="Platform: macOS | Linux">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0--or--later-blue" alt="License: AGPL-3.0-or-later"></a>
</p>

[English](README.md) · 中文

Kitestring 是面向 vibe coding 场景的个人驾驶舱：一个人、多台设备、多个 Claude Code / Codex 实例同时干活时，用一个 Web 看板回答四个问题——

1. 现在有哪些需求，各自到了哪一步？
2. 哪些 agent 实例正在跑、在哪台设备、在做什么？
3. 哪些事情在等我（授权、回复、决策）？
4. 一段时间内（今天下午、昨晚整夜）实际推进了什么？

它不替代 Claude Code / Codex 的交互界面，也不做“PM agent 自动驾驶”。实例状态靠设备采集器被动获取，只有需求与决策由 agent 通过极小的 MCP / CLI 接口主动写入；人做的决策通过 hook 直接注入回 agent 上下文。

当前阶段：**0.x 预览版**，已有实例墙、决策收件箱、需求看板与计划。

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

## 安装

`ks` 支持 macOS 与 Linux（arm64 / x64）。每台机器（Hub 和开发设备）都用同一条命令安装或升级：

```bash
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
```

脚本按系统和架构从 [Releases](https://github.com/NekroAI/kitestring/releases/latest) 下载、校验 SHA256，装到 `~/.local/bin/ks`。`KS_VERSION=v0.1.0` 可固定版本，`KS_INSTALL_DIR` 可改安装目录。

```bash
# Hub 所在机器（一台常开的机器）
ks hub passwd                   # 设置 Web 登录密码
ks install --hub --claude=false --codex=false --daemon=false   # 注册常驻服务，监听 0.0.0.0:7310
#   或前台运行：ks hub serve

# 每台开发设备（在 Web「设置 → 设备」中新增设备，拿到 token）
ks login --hub http://<hub 地址>:7310 --token kst_xxx
ks install --dry-run            # 预览将写入的 Claude / Codex 配置与常驻服务
ks install                      # 写入前自动备份；卸载：ks uninstall
#   Codex 需要你亲自在 codex 中执行 /hooks，信任 ks 的各条 hook
```

升级：重跑安装脚本，再重启常驻服务——macOS 再运行一次 `ks install`；Linux 运行 `systemctl --user restart kitestring-daemon`（Hub 所在设备再重启 `kitestring-hub`）。

所有本机数据位于 `~/.kitestring`（可用 `KS_HOME` 覆盖）。完整说明见 [docs/install.md](docs/install.md)。

### 从源码构建

```bash
# 需要 Go 1.27+、Node 22+、pnpm
cd web && pnpm install && pnpm build && cd ..
make build                      # 产物：bin/ks（已内嵌 Web 前端）
```

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
| [docs/install.md](docs/install.md) | 安装、接入设备与升级（也是每个版本的发布说明） |
| `notes/private/` | 作者本人的使用画像、旧看板复盘等含私有信息的背景资料（已 git 忽略，不进开源仓库） |

## 参与贡献

见 [CONTRIBUTING.md](CONTRIBUTING.md)；安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。

## 许可证

[AGPL-3.0-or-later](LICENSE)。
