# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] — 2026-10-23

First public release.

### Added
- **Install from GitHub:** `curl … /scripts/install.sh | sh` downloads the build for your system (macOS / Linux, arm64 / x64), verifies its SHA-256 and installs `ks` to `~/.local/bin`. Every tagged version is built and published by GitHub Actions.
- **Session wall:** every Claude Code / Codex session on every machine, grouped by project and conversation, with state (running, waiting on you, standing by, finished), a live action stream, background tasks, context usage and the last prompt.
- **Decision inbox:** agents raise decisions with options, a recommendation and option images; approvals and rejections can carry a note. Your answer is injected into the agent's context on your next message.
- **Requirement board:** drafts, to-do, in progress, review and done, with a list view, drag to change status (long-press on touch screens), comments, screenshot and screen-recording evidence, image annotation with stylus support, and design requirements that come with proposals to choose from.
- **Hand off work:** give a requirement to a session; to-do requirements queue up and arrive together when the session is idle. Sessions with lighter context are suggested first.
- **Plans:** split a larger goal into ordered stages; each stage is handed over when unlocked and accepted as a whole.
- **`ks wait`:** an agent can wait for your answer, acceptance or comment and continue as soon as you respond — in the background for Claude Code, in the foreground (up to 30 minutes) for Codex.
- **Agent tools:** a stdio MCP server and an equivalent `ks req` / `ks plan` / `ks decision` CLI.
- **Notifications:** system push on phones, iPad and desktop browsers (Web Push).
- **Sign-in:** password and passkeys for the web UI, a revocable token per device, and rate-limited login for public exposure.
- **Projects:** merged by git remote; can be hidden or merged into another project.
- **Daemon:** buffers events while offline and replays them, reconnects on its own, and fails open so hooks never block your agent.
