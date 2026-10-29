# kitestring-dsh

纸鸢 Kitestring 的 DeepSeek Harness（DSH）Host 插件。它让 DSH 会话出现在纸鸢的会话墙上，把 `ks mcp` 的工具接到模型手里，并在会话空闲时自动待命——用户在纸鸢上答复、验收或留言，同一个 DSH 会话会被直接唤醒，不需要 `ks wait`，也不需要回到 DSH 里再发一条消息。

## 它做什么

| 方向 | 行为 |
|---|---|
| DSH → 纸鸢 | `SessionStart` / `UserPromptSubmit` / `Stop` / `Interrupt` / `SessionEnd`、工具调用、`PreCompact` / `PostCompact` 都翻译成纸鸢已有的 hook 事件，经本机 daemon 上报 |
| 待命 | 一轮结束直接记成待命（Stop 带 `source: standby`），不先显示“等你回复”、不推送；随后在 daemon 上挂 `/wait`（12 小时，超时自动续） |
| 纸鸢 → DSH | 收到消息时：会话空闲用 `agent.followup()` 开一轮；正在跑用 `agent.steer()`，在最近的步骤边界送进这一轮，不会滞留到下次唤醒 |
| 工具 | 连一个 `ks mcp --client dsh`，把工具原样注册给模型；每次调用用 MCP `_meta` 带根会话 `dsh:<session id>`，子代理里另带子会话 ID |
| shell 变量 | 给模型的 shell 注入 `DSH_KITESTRING_SESSION`（根会话键）和 `DSH_KITESTRING_AGENT`（子代理会话 ID），`ks role` / `ks team tell` 在 DSH 里认对会话，即使 DSH 是从 Claude Code 终端里启动的 |
| 子代理 | 子代理的开始 / 结束、工具调用、token 用量记在根会话名下（带 `agent_id`），不改根会话的状态 |
| 上下文 | `ks mcp` 的说明作为系统提示词；纸鸢的决策答复在每一轮第一步之前注入 |
| 压缩 | 一轮内自动压缩后立即 `inject` 身份提示词、仍然有效的答复和会话状态，剩下的步骤就带着；两轮之间手动压缩的留到下一轮第一步 |
| 用量 | 每次模型回复把 token 用量发给 daemon（与 Claude / Codex 同一张表），Stop 时带上下文占用与窗口大小 |
| 生命周期 | 上报 Host 的 `pid@启动时间`；Host 消失时 daemon 在一个心跳周期内给它承载的所有会话补发 SessionEnd |

插件不读任何模型凭证，不另建推理通道；推理始终由 DSH 自己的 Host 完成。等待期间不产生模型调用。

## 安装

前提：本机 `ks daemon` 在运行（`ks status` 返回 connected），`ks` 是包含 DSH 支持的版本（`ks mcp --client dsh` 能启动）。

```bash
scripts/deploy.sh                          # 部署纸鸢时把插件放到 $KS_HOME/dsh-plugin（稳定副本）
scripts/install-dsh-plugin.sh --profile web # 链接这份副本，并在 profile 的 cordis.patch.yml 里启用
```

在用的 Host 链接的是 `$KS_HOME/dsh-plugin`（默认 `~/.kitestring/dsh-plugin`），只随 `scripts/deploy.sh` 更新，部署失败时随程序一起回滚；仓库工作区里改代码不会被热加载进去。开发插件本身时用 `--dev` 直接链接仓库的 `dsh-plugin/`。

web profile 开着 HMR，保存 patch 后几秒内生效；没有 HMR 时重启 `dsh --profile web`。桌面版是另一份 profile，把 `--profile web` 换成桌面 profile 的名字。`--dry-run` 只打印要做的事，不改任何文件。

## 配置

| 字段 | 默认 | 说明 |
|---|---|---|
| `socketPath` | `$KS_HOME/daemon.sock`，否则 `~/.kitestring/daemon.sock` | 本机 daemon |
| `ksBin` | `~/.local/bin/ks`，没有就用 `ks` | 启动 `ks mcp` 的可执行文件 |
| `standby` | `true` | 空闲时挂 `/wait` 并唤醒；关掉只上报事件和工具，一轮结束显示“等你回复” |
| `usage` | `true` | 是否上报 token 用量与上下文占用 |
| `events` | `true` | 是否上报会话事件（关掉后工具仍可用） |
| `attach` | `all` | 设为 `none` 时不起任何作用，用于先观察加载 |
| `debug` | `false` | 同时把诊断写到 `logFile` |
| `logFile` | `$KS_HOME/logs/dsh-plugin.log` | 调试日志位置 |

## 使用

- 会话会自动出现在纸鸢；`ks role pm|dev|aide|advisor|reviewer` 在 DSH 的 shell 里同样有效。
- **不要运行 `ks wait`**：DSH 里它会被 daemon 直接拒绝并返回一句说明，待命由插件负责。
- 用户在纸鸢上给会话留言、答复决策或验收时，插件会在空闲的会话里开一轮，在跑的会话里送进当前这一轮；并发多个会话时每个会话各挂一个等待，互不影响。
- 会话关闭（agent 释放）会上报 `SessionEnd`；插件重载不会结束会话，也不会把会话打回“等你回复”。

## 已知边界

- 从纸鸢创建 / 恢复 DSH 会话、原生审批双向同步，尚未实现。
- 桌面 profile 需要在桌面自己的 profile 里安装并验证；容器里的 `dsh web` 需要把 daemon 装在运行 Host 的机器上。
- 同一个会话同时开在两个 Host（桌面 + web）时，以最近发消息 / 打开的那个为准：较早的 Host 挂等待会直接得到 stale，被接管后也不定时抢回；在它里面再发一条消息，它就重新领活。
- 一个 Host 同时开着多个会话时，纸鸢分不清后台 shell 属于哪个会话，不显示进程级的后台任务。

## 开发

```bash
cd dsh-plugin && npm test   # node --test：待命状态机、送达、压缩、子代理、重连、schema 收敛、ks mcp 子进程
```

## 排障

1. `ks status` 应返回 connected。
2. `ps aux | grep "ks mcp --client dsh"` 应有且只有一个插件拉起的进程。
3. 打开 `debug: true`，看 `~/.kitestring/logs/dsh-plugin.log`：正常会看到 `apply ...`、`接入会话 ...`、`已注册 N 个纸鸢工具`、`已上报 SessionStart ...`。
4. 工具调用报“认不出当前会话”时，确认 `ks` 是包含 `_meta` 支持的新版本（本仓库构建），并确认 `ks mcp --client dsh` 能启动。
