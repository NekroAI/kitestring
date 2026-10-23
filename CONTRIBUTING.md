# Contributing to Kitestring

Thanks for your interest! Kitestring is a small project, so the process is light. This guide covers how to build, test and submit changes.

## Before you start

- **Bugs:** open an issue with the [bug report template](https://github.com/NekroAI/kitestring/issues/new?template=bug_report.yml). Include `ks version`, your OS and architecture, and the agent (Claude Code / Codex) and its version.
- **Features:** open an issue first so we can agree on the approach. Product decisions and their reasoning are recorded in [`docs/decisions.md`](docs/decisions.md) — please read the relevant entries before proposing a change to something already decided.
- **Security issues:** do not open a public issue. See [SECURITY.md](SECURITY.md).

## Setting up

| Component | Requirements |
|---|---|
| `ks` (Go, `cmd/` and `internal/`) | Go 1.27+ |
| Web UI (`web/`) | Node.js 22, pnpm 11 |

```sh
cd web && pnpm install && pnpm build && cd ..   # the web UI is embedded into the binary
make build                                      # bin/ks
go vet ./... && go test ./...
```

For a local Hub, run `./bin/ks hub passwd` and `./bin/ks hub serve`, then `cd web && pnpm dev` for hot reload. Set `KS_HOME` to a scratch directory so experiments don't touch your real `~/.kitestring`.

## Making changes

- **Match the surrounding code.** Comment density, naming and idioms should look like the code around your change. Design documents in `docs/` and the UI copy are written in Chinese; code comments follow the file you are editing.
- **Hooks must never get in the way.** `ks hook` has a hard timeout and fails open, and `ks` never exits with code 2 (agents treat it as "block"). Keep it that way.
- **Only documented extension points.** Kitestring uses hooks, `additionalContext` and MCP as Claude Code and Codex document them; it doesn't patch agents, read credentials or proxy model traffic (D13, D14).
- **API changes:** the OpenAPI document is generated from Go types. After changing the API, run `go run ./cmd/ks hub openapi > web/openapi.json && (cd web && pnpm gen:api)` and commit both files — CI checks they are in sync.

## Commits and pull requests

- Write commit messages that say what changed and why, with a prefix such as `feat:`, `fix:`, `docs:` or `chore:`.
- One logical change per pull request; include tests for behavior changes.
- CI must pass: `go vet`, `go test`, the web type check and build, the OpenAPI sync check and `shellcheck` on the install script.
- By contributing, you agree that your contributions are licensed under [AGPL-3.0-or-later](LICENSE).

## Releasing (maintainers)

1. Update `VERSION` and move the `[Unreleased]` entries in `CHANGELOG.md` under the new version.
2. Commit, then tag and push: `git tag v$(cat VERSION) && git push origin main --tags`.
3. The Release workflow checks that the tag matches `VERSION`, runs the tests, builds the four archives and `SHA256SUMS.txt`, and publishes them with `docs/install.md` as the release notes.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
