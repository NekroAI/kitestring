# Install Kitestring

English · [中文](https://github.com/NekroAI/kitestring/blob/main/docs/install.zh-CN.md)

Kitestring is a single binary, `ks`, for macOS and Linux (arm64 and x64). One always-on machine runs the **Hub**; every machine where you run Claude Code or Codex runs the **daemon**.

## 1. Install `ks`

```sh
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
```

The script picks the right build for your system, verifies it against `SHA256SUMS.txt` and installs it to `~/.local/bin/ks`. Set `KS_VERSION=v0.1.0` to pin a version or `KS_INSTALL_DIR` to change the directory. Run it again to update.

Prefer to do it by hand? Download `kitestring-<version>-<darwin|linux>-<arm64|amd64>.tar.gz` below, check it against `SHA256SUMS.txt`, and put `ks` somewhere stable — `ks install` writes the binary's absolute path into your agent configuration, so don't move it afterwards.

## 2. Hub (one always-on machine)

```sh
ks hub passwd                                              # set the web login password
ks install --hub --claude=false --codex=false --daemon=false   # run the Hub as a service on 0.0.0.0:7310
```

Open `http://<hub address>:7310` in a browser and sign in. To reach the Hub from outside your network, put it behind a reverse proxy with HTTPS or forward the port yourself.

## 3. Every development machine

1. In the web UI, open **Settings → Devices**, add a device and copy its token.
2. Connect and install:

   ```sh
   ks login --hub http://<hub address>:7310 --token kst_xxx
   ks install --dry-run   # preview the Claude Code / Codex hooks, MCP server and daemon service
   ks install             # backs up every file it changes; undo with ks uninstall
   ```

3. Codex only: run `/hooks` inside Codex once and trust the `ks` hooks.

Start a Claude Code or Codex session and it shows up on the board within seconds.

## Updating

Re-run the install script, then restart the services: on macOS run `ks install` again; on Linux run `systemctl --user restart kitestring-daemon` (and `kitestring-hub` on the Hub machine).

All local data lives in `~/.kitestring` (override with `KS_HOME`) and is kept across updates.

## Build from source

```sh
# Requires Go 1.27+, Node 22+, pnpm
cd web && pnpm install && pnpm build && cd ..
make build        # output: bin/ks, with the web UI embedded
```
