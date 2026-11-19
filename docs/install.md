# Install Kitestring

English · [中文](https://github.com/NekroAI/kitestring/blob/main/docs/install.zh-CN.md)

Kitestring is a single binary, `ks`, for macOS, Linux and Windows (arm64 and x64; for Windows see [Windows](#windows)). One always-on machine runs the **Hub**; every machine where you run Claude Code or Codex runs the **daemon**.

## 1. Install `ks`

```sh
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
```

The script picks the right build for your system, verifies it against `SHA256SUMS.txt` and installs it to `~/.local/bin/ks`. Set `KS_VERSION=v0.1.0` to pin a version or `KS_INSTALL_DIR` to change the directory. To update later, use `ks update` (see [Updating](#updating)).

Prefer to do it by hand? Download `kitestring-<version>-<darwin|linux>-<arm64|amd64>.tar.gz` below, check it against `SHA256SUMS.txt`, and put `ks` somewhere stable — `ks install` writes the binary's absolute path into your agent configuration, so don't move it afterwards.

## 2. Hub (one always-on machine)

```sh
ks hub passwd                                              # set the web login password
ks install --hub --claude=false --codex=false --daemon=false   # run the Hub as a service on 0.0.0.0:7310
```

Open `http://<hub address>:7310` in a browser and sign in. To reach the Hub from outside your network, put it behind a reverse proxy with HTTPS or forward the port yourself.

### Attachment directory

Attachments, prototype bundles and thumbnails live in `~/.kitestring/files` next to the database by default. If the system disk is short on space you can keep them on another disk (for example an always-attached external drive); the database stays where it is:

```sh
mkdir -p /Volumes/DataSSD/kitestring-files                     # the directory must exist
ks install --hub --claude=false --codex=false --daemon=false \
  --hub-files-dir /Volumes/DataSSD/kitestring-files            # passed to the service as --files-dir
```

You can also run `ks hub serve --files-dir <dir>` directly, or set `KS_FILES_DIR` (`--files-dir` wins). When you re-run `ks install --hub` later without `--hub-files-dir`, it keeps the directory configured in the installed service instead of going back to the default; to go back to the default, pass `--hub-files-dir default` (write `./default` for a directory literally named default).

- The directory must already exist; the Hub never creates it, because creating it under an unmounted drive's mount point would silently write to the system disk.
- If the directory is missing or not writable, the Hub still starts but rejects uploads, logs the reason, and **Settings → Account → Attachment storage** shows "storage unavailable" with the reason. Every upload checks again, so remounting the drive needs no restart.
- Attachments uploaded before the change can stay in `~/.kitestring/files` and remain viewable (read-only; new files go only to the new directory). To move them, see "Moving attachments and deployment backups to another disk" below.
- **Settings → Account → Attachment storage** shows the current location and how much space is used.
- **Attachments are not part of deployment backups**: `scripts/deploy.sh` backs up only the binary and the database. Back up the attachment directory (default or custom) yourself.

### Moving attachments and deployment backups to another disk

`scripts/deploy.sh` keeps deployment backups (old binary, database, DSH plugin copy) in `~/.kitestring/backups` by default. To use another directory, set `KS_BACKUP_DIR`, or write the path on the first line of `~/.kitestring/backup-dir` (used by every later deploy). The directory must already exist and be writable; if the drive is not mounted, the deploy fails before building, changes nothing, and never falls back to the default directory. The latest 10 backups are kept (`KS_KEEP_BACKUPS`), and rollback reads the old binary from this directory.

Move existing attachments and backups with `scripts/migrate-storage.sh`: it copies everything, checks the file count and every file's sha256, and writes a record to `~/.kitestring/migrations/`. It suggests the next step only when the check passes, and deleting the old copy is a separate step that needs explicit confirmation. Every step supports `--dry-run`. With an external drive at `/Volumes/DataSSD`:

```sh
mkdir /Volumes/DataSSD/kitestring                                        # the parent must exist
scripts/migrate-storage.sh copy ~/.kitestring/files   /Volumes/DataSSD/kitestring/files
scripts/migrate-storage.sh copy ~/.kitestring/backups /Volumes/DataSSD/kitestring/backups
# once both say the check passed, switch the configuration
ks install --hub --claude=false --codex=false --daemon=false --hub-files-dir /Volumes/DataSSD/kitestring/files
echo /Volumes/DataSSD/kitestring/backups > ~/.kitestring/backup-dir
# check Settings → Account → Attachment storage and open a few old and new attachments; copy again to pick up files added meanwhile
scripts/migrate-storage.sh copy ~/.kitestring/files /Volumes/DataSSD/kitestring/files
scripts/migrate-storage.sh delete-old ~/.kitestring/files   /Volumes/DataSSD/kitestring/files   --confirm-delete-old
scripts/migrate-storage.sh delete-old ~/.kitestring/backups /Volumes/DataSSD/kitestring/backups --confirm-delete-old
```

`verify` checks without copying. The check passes when every file in the old directory exists in the new one with the same sha256; extra files in the new directory (attachments uploaded after the switch) are counted but do not fail the check.

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

## Windows

Windows 10 1809 or later and Windows 11 (x64 / arm64) are supported. Both the Hub and the daemon run on Windows; if Windows is your only computer, install both on it. Run everything below in PowerShell; no administrator rights are needed.

**1. Install `ks`**:

```powershell
irm https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.ps1 | iex
```

It installs `~\.local\bin\ks.exe` and adds that folder to your user PATH (new terminals pick it up). `$env:KS_VERSION` and `$env:KS_INSTALL_DIR` work as on macOS / Linux. To install by hand, download `kitestring-<version>-windows-<amd64|arm64>.zip` and put `ks.exe` somewhere permanent.

**2. One computer: Hub and daemon together**

```powershell
ks hub passwd
ks install --hub --claude=false --codex=false --daemon=false   # run the Hub as a service
```

Open `http://127.0.0.1:7310`, sign in, and add this computer under Settings → Devices; follow the Windows commands in the guide (`ks login --hub http://127.0.0.1:7310 --token …`, then `ks install`). To use your phone, allow ks through Windows Firewall when it asks on the Hub's first start (Private networks for your LAN), then open `http://<your computer's LAN address>:7310` on the phone.

**3. Services**: `ks install` registers a per-user Task Scheduler task, `Kitestring daemon` (`Kitestring hub` for the Hub). It runs in the background after you sign in to Windows, without a window, and restarts a minute after it exits. To keep it from starting at sign-in:

```powershell
ks install --autostart=false    # register only; start it with schtasks /run /tn "Kitestring daemon"
```

Logs are in `~\.kitestring\logs\daemon.log` (`hub.log` for the Hub). `ks uninstall` removes the tasks and the config it wrote.

**4. Claude Code and Codex**: Claude Code on Windows runs hooks with Git Bash or PowerShell; `ks install` writes them so `ks.exe` is started directly, which works with either. For Codex, trust the `ks hook codex` entries in `/hooks` as on other systems.

The DSH plugin does not support Windows yet.

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

Run `ks update`: it downloads the latest release, verifies it against `SHA256SUMS.txt`, replaces `ks` and restarts the Hub and daemon services (launchd / systemd / Task Scheduler) that run this `ks`. `ks update --check` only reports whether a newer version exists. Development builds from source are left alone unless you pass `--force`.

Versions 0.2.0 and earlier have no `ks update`: re-run the install script, then restart the services: on macOS run `ks install` again; on Linux run `systemctl --user restart kitestring-daemon` (and `kitestring-hub` on the Hub machine).

All local data lives in `~/.kitestring` (override with `KS_HOME`) and is kept across updates.

## Build from source

```sh
# Requires Go 1.27+, Node 22+, pnpm
cd web && pnpm install && pnpm build && cd ..
make build        # output: bin/ks, with the web UI embedded
```
