# 安装 Kitestring

[English](install.md) · 中文

Kitestring 是一个二进制 `ks`，支持 macOS 与 Linux（arm64 / x64）。一台常开的机器运行 **Hub**；每台跑 Claude Code 或 Codex 的机器运行 **daemon**。

## 1. 安装 `ks`

```sh
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
```

脚本按系统和架构下载对应版本，用 `SHA256SUMS.txt` 校验后装到 `~/.local/bin/ks`。`KS_VERSION=v0.1.0` 可固定版本，`KS_INSTALL_DIR` 可改安装目录。之后升级用 `ks update`（见[升级](#升级)）。

想手动安装：从 [Releases](https://github.com/NekroAI/kitestring/releases/latest) 下载 `kitestring-<版本>-<darwin|linux>-<arm64|amd64>.tar.gz`，对照 `SHA256SUMS.txt` 校验，把 `ks` 放到固定位置——`ks install` 会把它的绝对路径写进 agent 配置，之后不要再移动。

## 2. Hub（一台常开的机器）

```sh
ks hub passwd                                                  # 设置网页登录密码
ks install --hub --claude=false --codex=false --daemon=false   # 注册为常驻服务，监听 0.0.0.0:7310
```

浏览器打开 `http://<hub 地址>:7310` 登录。要从外网访问，请放在带 HTTPS 的反向代理后面，或自行映射端口。

## 3. 每台开发设备

在网页「设置 → 设备」点「接入设备」，起个名字。弹出的接入指引按平台和你用的工具给出完整命令，照着执行就能连上；令牌只在这时显示一次。还没连上的设备，列表里显示「已创建，等它连上」，点「接入指引」可以再打开（不再显示令牌）。下面是同样的步骤。

**1. 安装 `ks`**，并把 `~/.local/bin` 加进 PATH（macOS 写 `~/.zshrc`，Linux 写 `~/.bashrc`）：

```sh
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
export PATH="$HOME/.local/bin:$PATH"
```

最小化的 Linux 可能没有 curl，先装上（如 `sudo apt update && sudo apt install -y curl`）。

**2. 登录到 Hub**：地址填别的设备能访问的 Hub 地址，不要用 `127.0.0.1` 或 `localhost`。

```sh
ks login --hub http://<hub 地址>:7310 --token kst_xxx
```

**3. 接入工具，启动后台服务**：

```sh
ks install                    # 同时接入 Claude Code 和 Codex
ks install --codex=false      # 只用 Claude Code
ks install --claude=false     # 只用 Codex
```

`ks install` 写入 hook 和 MCP（改动前自动备份，`--dry-run` 先预览，`ks uninstall` 撤销），并把 daemon 注册成常驻服务：macOS 用 launchd，登录后自动启动；Linux 用 systemd 用户服务。接入 Claude Code 需要终端里能运行 `claude`。

Linux 服务器上要开机就启动、退出登录也不停，再执行一次：

```sh
sudo loginctl enable-linger "$USER"
```

没有 systemd 的环境（如 Docker 容器）里，`ks install` 会报找不到 `systemctl`，hook 和 MCP 照常写入。这时手动启动 daemon，它不会开机自启：

```sh
nohup ks daemon >> ~/.kitestring/daemon.log 2>&1 &
```

**4. 仅 Codex**：打开 codex，输入 `/hooks`，把 `ks hook codex` 开头的各条都设为信任。不信任的话，Codex 会话不会出现在纸鸢。

**5. 核对**：

```sh
ks status
```

输出里有 `"connected": true` 就连上了。网页设备列表里这台设备变成「在线」，带上主机名、平台和版本。之后开一个 Claude Code 或 Codex 会话，几秒内出现在工作台。

- 报连不上 `daemon.sock`：daemon 没启动，回到第 3 步（容器里用 `nohup` 那行）。
- `"connected": false`：Hub 地址或令牌不对。日志在 `~/.kitestring/logs/daemon.log`（macOS），Linux 用 `journalctl --user -u kitestring-daemon -n 50`。

接入 DSH 见下面的 [DeepSeek Harness（DSH）](#deepseek-harnessdsh)。

## 多个 Claude 配置

如果你用不同的配置目录开 Claude Code（比如用 shell 函数设置 `CLAUDE_CONFIG_DIR`），需要把每个目录手动接入，纸鸢不会自动发现。

```sh
claude-x() ( export CLAUDE_CONFIG_DIR="$HOME/.claude-x"; export ANTHROPIC_BASE_URL=…; export ANTHROPIC_API_KEY=…; command claude "$@" )

ks install --claude-dir ~/.claude-x --launcher claude-x   # --launcher 可省；多个目录就重复写成对的参数
ks install --status                                        # 逐个目录列出 hook、MCP、权限是否到位，以及相对 ~/.claude 缺了什么
ks uninstall --claude-dir ~/.claude-x                      # 只移除这一个目录；不带参数的 ks uninstall 移除全部
```

- 目录列表记在本机 `~/.kitestring/claude-dirs.json`，之后不加参数重跑 `ks install`（或升级）会给 `~/.claude` 和列表里所有目录一起更新。当前 shell 里设了 `CLAUDE_CONFIG_DIR` 时也一并接入。`--dry-run` 按目录分别列出改动。
- 每个目录都会写入 hooks、`mcp__kitestring` 权限白名单和 user scope 的 MCP 注册（执行 `claude mcp add` 时带上对应的 `CLAUDE_CONFIG_DIR`）。
- 非默认目录的会话，卡片上有一个写目录名的小标签（去掉开头的点，如 `claude-x`），会话详情的客户端一行写成「Claude Code · claude-x · …」；默认的 `~/.claude` 不额外标注。
- 「复制恢复命令」：配了启动命令给 `<启动命令> --resume <id>`，没配给 `CLAUDE_CONFIG_DIR=<目录> claude --resume <id>`（需要这个目录里已有登录凭证）。
- 不碰凭证：只上报目录路径和显示名，`ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL` 不读、不存、不写日志。

哪些共用、哪些隔开：Hub、会话列表、需求和决策数据、角色提示词（由 Hub 注入）对所有目录都一样；用户级 `CLAUDE.md`、项目记忆、自定义子 Agent、skills 和登录凭证按配置目录隔开。`ks install --status` 会列出 `~/.claude` 里有、这个目录里缺的项。个人偏好想共用，请自己建软链接，例如 `ln -s ~/.claude/CLAUDE.md ~/.claude-x/CLAUDE.md`（纸鸢不会替你建）。

## DeepSeek Harness（DSH）

DSH 会话也能上墙：用本仓库里的 Host 插件（`dsh-plugin/`）。它走 DSH 自己的会话事件和 `agent.followup()` / `steer()`，不需要 `ks wait` 进程——插件在两轮之间挂着一个 `/wait`，你在纸鸢上答复时唤醒同一个 agent（正在跑的一轮在下一步就收到）。

要求 DSH 0.2.1-rc.4、同机运行的 `ks daemon`、`pnpm`（`dsh plugin add` 用它），并且运行过一次 `dsh --profile web`（初始化 profile）。

新设备（用安装脚本装的 `ks`）：插件不在发布包里。取与本机 `ks` 同版本的源码，放到 `~/.kitestring/dsh-plugin` 再启用：

```sh
v=$(ks version | awk '{print $2}')
git clone --depth 1 --branch "v$v" https://github.com/NekroAI/kitestring.git ~/.kitestring/src
mkdir -p ~/.kitestring/dsh-plugin && cp -R ~/.kitestring/src/dsh-plugin/. ~/.kitestring/dsh-plugin/
sed -i.bak '/^\[\]$/d' ~/.dsh/profiles/web/cordis.patch.yml
bash ~/.kitestring/src/scripts/install-dsh-plugin.sh --profile web
```

`sed` 那行去掉新 profile 里的空数组 `[]`：0.2.0 的启用脚本会把记录接在它后面，DSH 启动时报 YAML 解析错误。之后的版本脚本自己处理，这行多余但无害。

升级 `ks` 后先删掉 `~/.kitestring/src`，再按新版本做一遍。

Hub 所在机器（从源码目录部署）：

```sh
scripts/deploy.sh                           # 部署时顺带把插件放到 $KS_HOME/dsh-plugin（稳定副本）
scripts/install-dsh-plugin.sh --profile web # 链接这份副本，并在 profile 里启用
```

在用的 Host 链接的是 `$KS_HOME` 下的副本，只随部署更新、部署失败时一起回滚；仓库里改代码不会被热加载进去。开发插件本身时加 `--dev` 直接链接仓库。

web profile 会自己热重载 profile patch；没有热重载时重启 `dsh --profile web`。之后会话会带 DSH 标记出现在工作台上。DSH 的 shell 里能正常使用 `ks role …`（插件注入根会话键 `DSH_KITESTRING_SESSION`，子代理里、从 Claude Code 终端启动的 DSH 里也认对）；不要在那里运行 `ks wait`——daemon 会立即返回并说明待命由插件负责。

插件会报 token 用量和上下文大小（会话圆环），Host 进程消失时会结束它承载的所有会话。目前只在 `web` profile 上验证过；桌面版和 Windows 没有验证。从纸鸢创建/恢复 DSH 会话、原生审批双向同步还没有做。

## 会话交接

压缩或换会话时可以交接：在网页会话上点「交接」，旧会话写下结构化交接；在项目目录运行 `ks takeover <身份>`（如 `ks takeover pm`，`--tool codex` 用 Codex 开，`--print` 只打印命令）启动接任会话。旧版 MCP 没有 `handoff_write` 时，旧会话可用 `ks handoff --doing … --next …` 写交接。流程详见 [roles.md](roles.md)。

## 升级

运行 `ks update`：下载最新发布版、用 `SHA256SUMS.txt` 校验、替换 `ks`，再重启用这个 `ks` 的 Hub 和后台服务（launchd / systemd）。`ks update --check` 只查有没有新版本。从源码构建的开发版默认不更新，加 `--force` 才换成发布版。

0.2.0 及更早的版本还没有 `ks update`：重跑安装脚本，再重启常驻服务：macOS 再运行一次 `ks install`；Linux 运行 `systemctl --user restart kitestring-daemon`（Hub 所在机器再重启 `kitestring-hub`）。

本机数据都在 `~/.kitestring`（可用 `KS_HOME` 覆盖），升级时保留。

## 从源码构建

```sh
# 需要 Go 1.27+、Node 22+、pnpm
cd web && pnpm install && pnpm build && cd ..
make build        # 产物：bin/ks（已内嵌网页前端）
```
