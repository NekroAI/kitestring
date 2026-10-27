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

## Multiple Claude configs

If you start Claude Code with different config directories (for example a shell function that exports `CLAUDE_CONFIG_DIR`), hook up each directory explicitly. Kitestring does not discover them on its own.

```sh
claude-x() ( export CLAUDE_CONFIG_DIR="$HOME/.claude-x"; export ANTHROPIC_BASE_URL=…; export ANTHROPIC_API_KEY=…; command claude "$@" )

ks install --claude-dir ~/.claude-x --launcher claude-x   # --launcher is optional; repeat the pair for more directories
ks install --status                                        # per directory: hooks, MCP, permission, and what is missing compared with ~/.claude
ks uninstall --claude-dir ~/.claude-x                      # remove this directory only; plain `ks uninstall` removes all
```

- The directory list is kept in `~/.kitestring/claude-dirs.json`; re-running `ks install` (or upgrading) updates `~/.claude` and every listed directory. A `CLAUDE_CONFIG_DIR` set in the current shell is hooked up too. `--dry-run` lists the changes per directory.
- Each directory gets the hooks, the `mcp__kitestring` permission and the user-scope MCP registration (`claude mcp add` runs with that `CLAUDE_CONFIG_DIR`).
- Sessions from a non-default directory show a small tag with the directory name (without the leading dot, e.g. `claude-x`) and "Claude Code · claude-x" in the session details. The default `~/.claude` shows nothing extra.
- "Copy resume command" gives `<launcher> --resume <id>` when a launcher is set, otherwise `CLAUDE_CONFIG_DIR=<dir> claude --resume <id>` (the directory must already hold a login).
- Credentials stay out: only the directory path and name are reported. `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL` are never read, stored or logged.

What is shared and what is separate: the Hub, the session list, requirement and decision data, and the role prompts (delivered by the Hub) are the same for every directory. Each config directory has its own user-level `CLAUDE.md`, project memory, custom agents, skills and login. `ks install --status` lists what `~/.claude` has that the directory lacks. To share personal preferences, symlink them yourself, e.g. `ln -s ~/.claude/CLAUDE.md ~/.claude-x/CLAUDE.md` (Kitestring never creates links for you).

## DeepSeek Harness (DSH)

DSH sessions can join the board through the Host plugin in this repository (`dsh-plugin/`). It uses DSH's own session events and `agent.followup()` / `inject()`, so no `ks wait` process is involved: the plugin keeps a `/wait` request open while the session is idle and wakes the same agent when you answer in Kitestring.

Requires DSH 0.2.1-rc.4 and a running `ks daemon` on the same machine. From a source checkout:

```sh
dsh plugin --profile web add link:"$PWD/dsh-plugin"
scripts/install-dsh-plugin.sh --profile web
```

The web profile reloads profile patches by itself; otherwise restart `dsh --profile web`. Sessions then appear with the DSH mark. `ks role …` works inside DSH shells through `DSH_SESSION_ID`; do not run `ks wait` there — the daemon answers it immediately and explains that the plugin owns standby.

The plugin reports token usage and context size (the session ring), and ends a session when the Host process disappears. It has been verified against the `web` profile; the desktop profile and Windows were not tested. Creating or resuming DSH sessions from Kitestring, and two-way native approval, are not implemented yet.

## Handoff

When compacting or switching sessions, hand off: click "Hand off" on a session in the web UI so the old session writes a structured handoff, then run `ks takeover <role>` in the project directory (for example `ks takeover pm`; `--tool codex` starts it in Codex, `--print` only prints the command). If the old session's MCP lacks `handoff_write`, it can use `ks handoff --doing … --next …` instead. See [roles.md](roles.md) for the full flow.

## Updating

Re-run the install script, then restart the services: on macOS run `ks install` again; on Linux run `systemctl --user restart kitestring-daemon` (and `kitestring-hub` on the Hub machine).

All local data lives in `~/.kitestring` (override with `KS_HOME`) and is kept across updates.

## Build from source

```sh
# Requires Go 1.27+, Node 22+, pnpm
cd web && pnpm install && pnpm build && cd ..
make build        # output: bin/ks, with the web UI embedded
```
