<p align="center">
  <img src="web/public/icon.svg" width="88" height="88" alt="Kitestring">
</p>

<h1 align="center">Kitestring</h1>

<p align="center">
  <strong>One board for every Claude Code and Codex session on every machine you own.</strong><br>
  See what each agent is doing, answer what it's waiting on, and hand it the next task — from your desk or your phone.
</p>

<p align="center">
  <a href="docs/install.md">Install</a> ·
  <a href="docs/how-it-works.md">How it works</a> ·
  <a href="README.zh-CN.md">中文</a>
</p>

<p align="center">
  <a href="https://github.com/NekroAI/kitestring/actions/workflows/ci.yml"><img src="https://github.com/NekroAI/kitestring/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/NekroAI/kitestring/releases/latest"><img src="https://img.shields.io/github/v/release/NekroAI/kitestring" alt="Release"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey" alt="Platform: macOS | Linux">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="License: AGPL-3.0"></a>
</p>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/screenshot-sessions-dark.png">
  <img src="docs/assets/screenshot-sessions-light.png" alt="The session wall: agent sessions on several machines, grouped by project, each showing what it is doing and whether it needs you.">
</picture>

## What it does

- **See every session at a glance.** Sessions on all your machines, grouped by project — running, waiting on you, standing by or done, with what each one is doing right now.
- **Answer from anywhere.** When an agent needs a decision, you get a push notification. Pick an option on your phone, and the answer lands in that agent's next turn.
- **Keep the work in one place.** Agents record requirements as they go; you confirm them, hand them to a session, and accept the results with screenshots attached.

<table>
  <tr>
    <td width="50%">
      <picture>
        <source media="(prefers-color-scheme: dark)" srcset="docs/assets/screenshot-decision-dark.png">
        <img src="docs/assets/screenshot-decision-light.png" alt="A decision raised by an agent, with options and its recommendation.">
      </picture>
      <p align="center"><sub>Decisions come with options and the agent's recommendation</sub></p>
    </td>
    <td width="50%">
      <picture>
        <source media="(prefers-color-scheme: dark)" srcset="docs/assets/screenshot-board-dark.png">
        <img src="docs/assets/screenshot-board-light.png" alt="The requirement board with draft, to-do, in-progress, review and done columns.">
      </picture>
      <p align="center"><sub>Requirements move from draft to done, with evidence to review</sub></p>
    </td>
  </tr>
</table>

## How it works

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/architecture-dark.svg">
  <img src="docs/assets/architecture-light.svg" alt="Claude Code and Codex on each machine report to ks daemon; the daemons talk to ks hub; you use the Hub's web board from any browser or phone.">
</picture>

A small daemon on each machine listens to your agents through their official hooks and MCP, and reports to a Hub on one always-on machine. You use the Hub's web board from any browser. Details: [How it works](docs/how-it-works.md).

## Get started

**1. Install `ks`** on every machine — the Hub and each machine you code on (macOS or Linux):

```bash
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
```

**2. Start the Hub** on one always-on machine, then open `http://<hub>:7310`:

```bash
ks hub passwd && ks install --hub --claude=false --codex=false --daemon=false
```

**3. Connect each machine** with a token from **Settings → Devices**:

```bash
ks login --hub http://<hub>:7310 --token kst_xxx && ks install
```

Open Claude Code or Codex and your session appears on the board. Updating, Codex setup and building from source: [Install guide](docs/install.md).

## Safe for your subscription

Kitestring only uses the extension points Claude Code and Codex document officially. It never touches your login credentials, never proxies model traffic, never consumes your subscription and never drives agents on its own. [Read more](docs/how-it-works.md#account-safety).

## Contributing

Issues and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Report security problems privately as described in [SECURITY.md](SECURITY.md).

## License

[AGPL-3.0-or-later](LICENSE)
