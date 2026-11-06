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

In the web UI, open **Settings → Devices** and click **Add device**. The setup guide that opens gives the full commands for your platform and tools; the token is shown only this once. A device that has never connected is listed as waiting to connect, with a button that reopens the guide (without the token). The same steps:

**1. Install `ks`** and put `~/.local/bin` on your PATH (`~/.zshrc` on macOS, `~/.bashrc` on Linux):

```sh
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
export PATH="$HOME/.local/bin:$PATH"
```

Minimal Linux installs may lack curl; install it first (e.g. `sudo apt update && sudo apt install -y curl`).

**2. Log in to the Hub** with an address other machines can reach — not `127.0.0.1` or `localhost`:

```sh
ks login --hub http://<hub address>:7310 --token kst_xxx
```

**3. Hook up your tools and start the background service:**

```sh
ks install                    # Claude Code and Codex
ks install --codex=false      # Claude Code only
ks install --claude=false     # Codex only
```

`ks install` writes the hooks and MCP server (backing up every file first; `--dry-run` previews, `ks uninstall` undoes) and registers the daemon as a service: launchd on macOS (starts at login), a systemd user service on Linux. Hooking up Claude Code needs `claude` on your PATH.

On a Linux server, to start at boot and keep running after you log out:

```sh
sudo loginctl enable-linger "$USER"
```

Without systemd (a Docker container, for example) `ks install` reports that `systemctl` is missing; the hooks and MCP server are still written. Start the daemon by hand — it won't start at boot:

```sh
nohup ks daemon >> ~/.kitestring/daemon.log 2>&1 &
```

**4. Codex only:** open codex, run `/hooks` and trust every hook starting with `ks hook codex`. Untrusted hooks don't run, so Codex sessions won't show up.

**5. Check:**

```sh
ks status
```

`"connected": true` means you're connected; the device turns **online** in the web device list, with its hostname, platform and version. Start a Claude Code or Codex session and it shows up within seconds.

- Can't reach `daemon.sock`: the daemon isn't running — go back to step 3 (in a container, use the `nohup` line).
- `"connected": false`: wrong Hub address or token. Logs: `~/.kitestring/logs/daemon.log` on macOS, `journalctl --user -u kitestring-daemon -n 50` on Linux.

For DSH, see [DeepSeek Harness (DSH)](#deepseek-harness-dsh) below.

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

DSH sessions can join the board through the Host plugin in this repository (`dsh-plugin/`). It uses DSH's own session events and `agent.followup()` / `steer()`, so no `ks wait` process is involved: the plugin keeps a `/wait` request open between turns and wakes the same agent when you answer in Kitestring (a running turn receives the message at its next step).

Requires DSH 0.2.1-rc.4, a running `ks daemon` on the same machine, `pnpm` (used by `dsh plugin add`), and a profile initialized by running `dsh --profile web` once.

On a new machine (with `ks` from the install script) the plugin isn't in the release archive. Fetch the source at the same version as your `ks`, copy the plugin to `~/.kitestring/dsh-plugin` and enable it:

```sh
v=$(ks version | awk '{print $2}')
git clone --depth 1 --branch "v$v" https://github.com/NekroAI/kitestring.git ~/.kitestring/src
mkdir -p ~/.kitestring/dsh-plugin && cp -R ~/.kitestring/src/dsh-plugin/. ~/.kitestring/dsh-plugin/
sed -i.bak '/^\[\]$/d' ~/.dsh/profiles/web/cordis.patch.yml
bash ~/.kitestring/src/scripts/install-dsh-plugin.sh --profile web
```

The `sed` line removes the empty `[]` a new profile starts with: the 0.2.0 script appends after it and DSH then fails to parse the file. Later versions handle it themselves, so the line is redundant but harmless.

After upgrading `ks`, delete `~/.kitestring/src` and repeat with the new version.

On the Hub machine, from a source checkout:

```sh
scripts/deploy.sh                           # also places a stable plugin copy in $KS_HOME/dsh-plugin
scripts/install-dsh-plugin.sh --profile web # links that copy and enables it in the profile
```

The running Host links the copy under `$KS_HOME`, so it only changes when you deploy (and rolls back with a failed deploy); edits in the checkout are not hot-loaded into it. Use `--dev` to link the checkout while developing the plugin itself.

The web profile reloads profile patches by itself; otherwise restart `dsh --profile web`. Sessions then appear with the DSH mark. `ks role …` works inside DSH shells (the plugin injects `DSH_KITESTRING_SESSION`, the root session, which also holds inside subagents and when DSH was started from a Claude Code terminal); do not run `ks wait` there — the daemon answers it immediately and explains that the plugin owns standby.

The plugin reports token usage and context size (the session ring), and ends every session it hosted when the Host process disappears. It has been verified against the `web` profile; the desktop profile and Windows were not tested. Creating or resuming DSH sessions from Kitestring, and two-way native approval, are not implemented yet.

## Handoff

When compacting or switching sessions, hand off: click "Hand off" on a session in the web UI so the old session writes a structured handoff, then run `ks takeover <role>` in the project directory (for example `ks takeover pm`; `--tool codex` starts it in Codex, `--print` only prints the command). If the old session's MCP lacks `handoff_write`, it can use `ks handoff --doing … --next …` instead. See [roles.md](roles.md) for the full flow.

## Updating

Run `ks update`: it downloads the latest release, verifies it against `SHA256SUMS.txt`, replaces `ks` and restarts the Hub and daemon services (launchd / systemd) that run this `ks`. `ks update --check` only reports whether a newer version exists. Development builds from source are left alone unless you pass `--force`.

Versions 0.2.0 and earlier have no `ks update`: re-run the install script, then restart the services: on macOS run `ks install` again; on Linux run `systemctl --user restart kitestring-daemon` (and `kitestring-hub` on the Hub machine).

All local data lives in `~/.kitestring` (override with `KS_HOME`) and is kept across updates.

## Build from source

```sh
# Requires Go 1.27+, Node 22+, pnpm
cd web && pnpm install && pnpm build && cd ..
make build        # output: bin/ks, with the web UI embedded
```
