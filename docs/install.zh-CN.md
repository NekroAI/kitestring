# 安装 Kitestring

[English](install.md) · 中文

Kitestring 是一个二进制 `ks`，支持 macOS 与 Linux（arm64 / x64）。一台常开的机器运行 **Hub**；每台跑 Claude Code 或 Codex 的机器运行 **daemon**。

## 1. 安装 `ks`

```sh
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
```

脚本按系统和架构下载对应版本，用 `SHA256SUMS.txt` 校验后装到 `~/.local/bin/ks`。`KS_VERSION=v0.1.0` 可固定版本，`KS_INSTALL_DIR` 可改安装目录。重跑即升级。

想手动安装：从 [Releases](https://github.com/NekroAI/kitestring/releases/latest) 下载 `kitestring-<版本>-<darwin|linux>-<arm64|amd64>.tar.gz`，对照 `SHA256SUMS.txt` 校验，把 `ks` 放到固定位置——`ks install` 会把它的绝对路径写进 agent 配置，之后不要再移动。

## 2. Hub（一台常开的机器）

```sh
ks hub passwd                                                  # 设置网页登录密码
ks install --hub --claude=false --codex=false --daemon=false   # 注册为常驻服务，监听 0.0.0.0:7310
```

浏览器打开 `http://<hub 地址>:7310` 登录。要从外网访问，请放在带 HTTPS 的反向代理后面，或自行映射端口。

## 3. 每台开发设备

1. 在网页「设置 → 设备」中新增设备，复制 token。
2. 连接并安装：

   ```sh
   ks login --hub http://<hub 地址>:7310 --token kst_xxx
   ks install --dry-run   # 预览将写入的 Claude Code / Codex hook、MCP 与 daemon 常驻服务
   ks install             # 改动前自动备份；卸载：ks uninstall
   ```

3. 仅 Codex：在 Codex 里亲自执行一次 `/hooks`，信任 `ks` 的各条 hook。

开一个 Claude Code 或 Codex 会话，几秒内就会出现在看板上。

## 升级

重跑安装脚本，再重启常驻服务：macOS 再运行一次 `ks install`；Linux 运行 `systemctl --user restart kitestring-daemon`（Hub 所在机器再重启 `kitestring-hub`）。

本机数据都在 `~/.kitestring`（可用 `KS_HOME` 覆盖），升级时保留。

## 从源码构建

```sh
# 需要 Go 1.27+、Node 22+、pnpm
cd web && pnpm install && pnpm build && cd ..
make build        # 产物：bin/ks（已内嵌网页前端）
```
