# Kitestring

> However far the kite flies, the string stays in your hand.

<p>
  <a href="https://github.com/NekroAI/kitestring/actions/workflows/ci.yml"><img src="https://github.com/NekroAI/kitestring/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/NekroAI/kitestring/releases/latest"><img src="https://img.shields.io/github/v/release/NekroAI/kitestring" alt="Release"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey" alt="Platform: macOS | Linux">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0--or--later-blue" alt="License: AGPL-3.0-or-later"></a>
</p>

English · [中文](README.zh-CN.md)

Kitestring is a personal cockpit for vibe coding. When one person has several Claude Code / Codex sessions working at once across several machines, a single web board answers four questions:

1. What requirements are there, and how far along is each one?
2. Which agent sessions are running, on which machine, doing what?
3. What is waiting on me — permissions, replies, decisions?
4. What actually got done over a stretch of time (this afternoon, overnight)?

It doesn't replace the Claude Code / Codex interface, and it isn't a "PM agent on autopilot". Session state is collected passively on each machine; only requirements and decisions are written by agents, through a very small MCP / CLI surface. Your decisions are injected straight back into the agent's context through hooks.

Status: **0.x preview** — session wall, decision inbox, requirement board and plans.

## Account safety

Kitestring only uses extension points that Claude Code and Codex document officially (hooks, `additionalContext`, MCP). It doesn't modify agent binaries; it never reads, stores or forwards login credentials; it doesn't proxy model traffic; and nothing it does consumes your Claude / ChatGPT subscription. AI summaries only call an OpenAI-compatible endpoint you configure separately. There is no remote task dispatch or automatic continuation. See D13 and D14 in [docs/decisions.md](docs/decisions.md).

## Components

One binary, `ks`, with a subcommand per role:

| Command | Runs on | Does |
|---|---|---|
| `ks hub` | one always-on machine | receives events, derives state, stores everything (SQLite), serves the web UI and API, notifications, summaries |
| `ks daemon` | every development machine | collects local hook events, buffers and replays them while offline, reads git state, caches decisions waiting to be injected |
| `ks hook <claude\|codex> <event>` | called by the agent | thin client to the daemon; 1.5 s hard timeout, fails open silently |
| `ks mcp` | started by the agent | requirement and decision tools: `req_list` `req_get` `req_create` `req_update_status` `evidence_attach` `decision_raise` `session_link_req` |

## Install

`ks` runs on macOS and Linux (arm64 / x64). The same command installs or updates it on every machine, Hub and development machines alike:

```bash
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
```

The script downloads the right build from [Releases](https://github.com/NekroAI/kitestring/releases/latest), verifies its SHA-256 and installs it to `~/.local/bin/ks`. Set `KS_VERSION=v0.1.0` to pin a version, or `KS_INSTALL_DIR` to change the directory.

```bash
# The Hub machine (one always-on machine)
ks hub passwd                   # set the web login password
ks install --hub --claude=false --codex=false --daemon=false   # run as a service on 0.0.0.0:7310
#   or in the foreground: ks hub serve

# Every development machine (add a device under Settings → Devices in the web UI to get a token)
ks login --hub http://<hub address>:7310 --token kst_xxx
ks install --dry-run            # preview the Claude / Codex configuration and the service
ks install                      # backs up before writing; undo with ks uninstall
#   Codex: run /hooks inside Codex yourself and trust each ks hook
```

To update, re-run the install script and restart the services: on macOS run `ks install` again; on Linux run `systemctl --user restart kitestring-daemon` (and `kitestring-hub` on the Hub machine).

All local data lives in `~/.kitestring` (override with `KS_HOME`). Full guide: [docs/install.md](docs/install.md).

### Build from source

```bash
# Requires Go 1.27+, Node 22+, pnpm
cd web && pnpm install && pnpm build && cd ..
make build                      # output: bin/ks, with the web UI embedded
```

## Waiting for you (`ks wait`)

When an agent finishes a piece of work and needs your review or answer to continue, it can run `ks wait`. As soon as you answer, accept, send back, comment or hand it a new requirement in Kitestring, it wakes up with your input and carries on — no need to go back to the terminal. It's only for waiting on a person, never for timers or automatic continuation.

| | Claude Code | Codex |
|---|---|---|
| How it runs | background task: the turn ends normally, and Claude Code wakes the session when `ks wait` exits | foreground command: Codex can't wake on background exit, so the turn stays open until the command ends |
| Longest wait | 1 hour by default, adjustable with `--timeout`, up to 12 hours | 30 minutes, which is also the default |
| Usage cost | none while waiting | Codex polls the command output, costing a little each time (mostly cache reads), hence the 30-minute cap |
| On timeout | ends quietly; continue in the conversation when you're back | tells the agent to wrap up — report progress and what it's waiting for, then end the turn; later answers arrive in the next conversation |

Waiting sessions show as "standing by" on the board, with how long they have waited and until when.

## Documentation

Design documents are written in Chinese.

| Document | Contents |
|---|---|
| [docs/install.md](docs/install.md) | Installing, connecting machines and updating (also the release notes of every version) |
| [docs/requirements.md](docs/requirements.md) | MVP requirements: positioning, architecture, views, data model, hook feasibility |
| [docs/decisions.md](docs/decisions.md) | Decision log and open questions |
| [docs/hook-matrix.md](docs/hook-matrix.md) | Tested Claude Code / Codex hook capabilities and state derivation rules |
| [docs/landscape.md](docs/landscape.md) | Survey of similar open-source projects |
| [docs/naming.md](docs/naming.md) | How the name was chosen |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Report security problems privately as described in [SECURITY.md](SECURITY.md).

## License

[AGPL-3.0-or-later](LICENSE).
