# kitestring-dsh

纸鸢 Kitestring 的 DeepSeek Harness（DSH）Host 插件。它让 DSH 会话出现在纸鸢的会话墙上，把 `ks mcp` 的工具接到模型手里，并在会话空闲时自动待命——用户在纸鸢上答复、验收或留言，同一个 DSH 会话会被直接唤醒，不需要 `ks wait`，也不需要回到 DSH 里再发一条消息。

## 它做什么

| 方向 | 行为 |
|---|---|
| DSH → 纸鸢 | `SessionStart` / `UserPromptSubmit` / `Stop` / `SessionEnd`、工具调用都翻译成纸鸢已有的 hook 事件，经本机 daemon 上报 |
| 纸鸢 → DSH | 空闲时在 daemon 上挂 `/wait`（12 小时，超时自动续）；收到消息用 `agent.followup()` 唤醒同一个会话 |
| 工具 | 连一个 `ks mcp --client dsh`，把 28 个工具原样注册给模型；每次调用用 MCP `_meta` 带准确的 `dsh:<session id>` |
| 上下文 | `ks mcp` 的说明作为系统提示词；纸鸢的决策答复在每一步之前注入，不靠猜时机 |

插件不读任何模型凭证，不另建推理通道；推理始终由 DSH 自己的 Host 完成。等待期间不产生模型调用。

## 安装

前提：本机 `ks daemon` 在运行（`ks status` 返回 connected），`ks` 是包含 DSH 支持的版本（`ks mcp --client dsh` 能启动）。

```bash
# 1. 把插件装进 DSH 的 web profile（链接方式，改代码后 HMR 可重载）
dsh plugin --profile web add link:/Users/miose/Projects/kitestring/dsh-plugin

# 2. 在 ~/.dsh/profiles/web/cordis.patch.yml 末尾加一条 insert
cat >> ~/.dsh/profiles/web/cordis.patch.yml <<'YAML'
- insert:
    - id: kitestring
      name: kitestring-dsh
      config:
        standby: true
```

web profile 开着 HMR，保存 patch 后几秒内生效；没有 HMR 时重启 `dsh web`。桌面版是另一份 profile，安装方式相同，把 `--profile web` 换成桌面 profile 的名字。

## 配置

| 字段 | 默认 | 说明 |
|---|---|---|
| `socketPath` | `$KS_HOME/daemon.sock`，否则 `~/.kitestring/daemon.sock` | 本机 daemon |
| `ksBin` | `~/.local/bin/ks`，没有就用 `ks` | 启动 `ks mcp` 的可执行文件 |
| `standby` | `true` | 空闲时挂 `/wait` 并唤醒；关掉只上报事件和工具 |
| `events` | `true` | 是否上报会话事件（关掉后工具仍可用） |
| `attach` | `all` | 设为 `none` 时不起任何作用，用于先观察加载 |
| `debug` | `false` | 同时把诊断写到 `/tmp/kitestring-dsh.log` |

## 使用

- 会话会自动出现在纸鸢；`ks role pm|dev|aide|advisor|reviewer` 在 DSH 的 shell 里同样有效（`DSH_SESSION_ID` 已被识别）。
- **不要运行 `ks wait`**：DSH 里它会被 daemon 直接拒绝并返回一句说明，待命由插件负责。
- 用户在纸鸢上给会话留言、答复决策或验收时，插件会在空闲的会话里开一轮；并发多个会话时每个会话各挂一个等待，互不影响。
- 会话关闭（agent 释放）会上报 `SessionEnd`；插件重载不会结束会话。

## 已知边界

- 还没做 token / 上下文占用统计，纸鸢里的用量圆环对 DSH 会话暂不显示。
- 从纸鸢创建 / 恢复 DSH 会话、原生审批双向同步，尚未实现。
- 桌面 profile 需要在桌面自己的 profile 里安装并验证；容器里的 `dsh web` 需要把 daemon 装在运行 Host 的机器上。
- 同一个会话同时开在两个 Host（桌面 + web）时，后挂上的等待会接管，另一个进入重试，不会两边都唤醒。

## 排障

1. `ks status` 应返回 connected。
2. `ps aux | grep "ks mcp --client dsh"` 应有且只有一个插件拉起的进程。
3. 打开 `debug: true`，看 `/tmp/kitestring-dsh.log`：正常会看到 `apply ...`、`接入会话 ...`、`已注册 28 个纸鸢工具`、`已上报 SessionStart ...`。
4. 工具调用报“认不出当前会话”时，确认 `ks` 是包含 `_meta` 支持的新版本（本仓库构建），并确认 `ks mcp --client dsh` 能启动。
