# 纸鸢 · Kitestring

> 风筝飞得再远，线在你手里。

Kitestring 是面向 vibe coding 场景的个人驾驶舱：一个人、多台设备、多个 Claude Code / Codex 实例同时干活时，用一个 Web 看板回答四个问题——

1. 现在有哪些需求，各自到了哪一步？
2. 哪些 agent 实例正在跑、在哪台设备、在做什么？
3. 哪些事情在等我（授权、回复、决策）？
4. 一段时间内（今天下午、昨晚整夜）实际推进了什么？

它不替代 Claude Code / Codex 的交互界面，也不做“PM agent 自动驾驶”。实例状态靠设备采集器被动获取，只有需求与决策由 agent 通过极小的 MCP / CLI 接口主动写入；人做的决策通过 hook 直接注入回 agent 上下文。

当前阶段：**需求定义中，尚未开始开发**。计划在 NekroAI 组织下开源，独立品牌，不带 `nekro-` 前缀。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/requirements.md](docs/requirements.md) | MVP 需求草案：定位、架构、视图、数据模型、hook 可行性 |
| [docs/decisions.md](docs/decisions.md) | 已确认决策台账与待决问题（上下文恢复事实源） |
| [docs/hook-matrix.md](docs/hook-matrix.md) | Claude Code / Codex hook 能力实测矩阵与状态推导规则 |
| [docs/landscape.md](docs/landscape.md) | 同类开源项目调研与差异化 |
| [docs/naming.md](docs/naming.md) | 命名过程、淘汰候选与原因 |
| `notes/private/` | 作者本人的使用画像、旧看板复盘等含私有信息的背景资料（已 git 忽略，不进开源仓库） |
