# 纸鸢 Kitestring 仓库约定

- 部署一律用 `scripts/deploy.sh`（或 `make deploy`），不要手工替换 `~/.local/bin/ks` 或手工重启 launchd 服务。脚本会构建前后端、备份程序和数据库、替换、重启、核对，任一步不对自动回滚并以非零状态退出。可用 `--dry-run` 预览步骤；`KS_HOME`、`KS_BIN`、`KS_HUB_URL`、`LAUNCHD_PREFIX`、`KS_LAUNCHCTL` 等环境变量可在演示环境里试跑（用法见脚本头部注释）。
- 设计原则见 [docs/principles.md](docs/principles.md)，做界面或产品取舍前先对照；界面截图用真实数据的快照做法见其中第 6 条。
- 写界面文案遵守 [docs/copywriting.md](docs/copywriting.md)：界面文字（`web/src/i18n/zh.ts`、后端显示在网页和推送上的字符串）按其中的写法和术语表写，身份提示词和 MCP 说明里的术语也与它一致。改完跑 web 单测（含 `copy.test.ts` 文案检查）；豁免只能写在 `docs/copywriting-rules.json` 的 allow 里并写理由。
