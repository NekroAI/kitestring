# 命名记录

最终：**纸鸢 / Kitestring**（2026-10-22 确认）。

寓意：风筝放得再远，线在你手里——agent 分散在不同地点的设备上各自干活，需求与决策这根线始终连回一个人。

## 约束

- 在 NekroAI 组织下开源，独立品牌，不带 `nekro-` 前缀（组织惯例：生态组件用 `nekro-*`，面向更广用户的独立工具如 qantrip、one-tracker 用独立名）。
- 中英文都好读，不同质化：避开 monitor / deck / kanban / hub / board / agent / vibe 等已被大量使用的品类词。

## 淘汰候选（占用情况为 2026-10-22 GitHub 仓库名搜索与 npm 包名查询）

| 候选 | 淘汰原因 |
|---|---|
| vibedeck | 已有同定位项目 quentinzhang/vibedeck；且与 ccdeck 同质 |
| pilothouse | npm 被占；GitHub 有 96★ 同名项目 |
| switchboard | doctly/switchboard（354★）恰是 CLI 编码会话管理器 |
| foreman / agent-foreman | mylukin/agent-foreman（249★）同领域 |
| purrview / 猫眼 | 中文与“猫眼电影”撞名 |
| loopdesk、nekro-inloop、nekro-muster | 前缀方向被否；loopdesk 偏工具味 |
| 拾穗 / Gleaner | npm 被占 |
| 栖台 / Purrch | 英文易误读为 purchase |
| farsight、tiller、cairn、sextant、waypost、queqiao、tidemark | 普通词全部重度占用；tidemark 为同领域额度监控工具，queqiao 为 417★ WAN 代理 |
| 喵驿 / Nyanpost、喵台 / Mewdesk、线知 / Threadwise | 进入最后一轮，未选 |

## 待办

- 开源前再确认 GitHub `NekroAI/kitestring`、npm（必要时用 `@nekroai/kitestring`）、域名可用性。
