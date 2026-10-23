#!/usr/bin/env python3
"""生成 README 架构图：中英文 × 明暗主题，共 4 个 SVG。用法：python3 docs/assets/src/architecture.py docs/assets"""
import sys
from xml.sax.saxutils import escape

OUT = sys.argv[1]

THEMES = {
    "light": dict(bg="#f4f0e6", surface="#fbf8f1", sunken="#ece6d9", line="#e3dccb", strong="#d3c9b3",
                  fg="#23211d", fg2="#4f493c", fg3="#776f5f", accent="#2f5d8c", accent_soft="#e3eaf2",
                  seal="#b5382a", seal_soft="#f6e6e0", run="#2f7a5b", run_soft="#e0eee6", shadow="rgb(70 50 18 / 0.10)"),
    "dark": dict(bg="#1c2232", surface="#242b3d", sunken="#181d2b", line="#343c52", strong="#434c64",
                 fg="#ece6d8", fg2="#c2bbaa", fg3="#9a9585", accent="#8db3e2", accent_soft="#26364f",
                 seal="#c8483a", seal_soft="#352a31", run="#72c7a0", run_soft="#21383a", shadow="rgb(0 0 0 / 0.35)"),
}

TEXT = {
    "en": dict(
        machines="Your machines", hub_t="ks hub", hub_s="one always-on machine", you="You", you_s="any browser, phone or iPad",
        dev1="MacBook Pro", dev2="Mac mini", more="+ as many as you like",
        agents_lbl="hooks · MCP",
        hub_items=["Session wall", "Decision inbox", "Requirement board", "Plans"], hub_foot="SQLite · Web UI · Web Push",
        you_items=["See what every agent is doing", "Answer decisions", "Review and accept work", "Hand off the next task"],
        ev="events", dec="decisions", live="updates", ans="answers",
        steps=["Agents work and raise decisions", "You answer from any device", "The agent picks it up next turn"],
    ),
    "zh": dict(
        machines="你的各台设备", hub_t="ks hub", hub_s="一台常开的机器", you="你", you_s="任意浏览器、手机或 iPad",
        dev1="MacBook Pro", dev2="Mac mini", more="+ 想接几台接几台",
        agents_lbl="hooks · MCP",
        hub_items=["会话墙", "决策收件箱", "需求看板", "计划"], hub_foot="SQLite · 网页 · 系统推送",
        you_items=["看清每个 agent 在做什么", "回答决策", "验收成果", "交出下一件事"],
        ev="事件", dec="决策", live="实时推送", ans="答复",
        steps=["Agent 干活，遇事提出决策", "你在任意设备上作答", "答复注入 agent 的下一轮"],
    ),
}

FONT = "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', Helvetica, Arial, sans-serif"
MONO = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
W, H = 960, 560


def kite(x, y, s):
    """纸鸢图标（与 web/public/icon.svg 相同），左上角 (x, y)，边长 s。"""
    k = s / 128
    return (f'<g transform="translate({x} {y}) scale({k}) rotate(-10 64 64)">'
            '<polygon points="64,10 22,54 64,54" fill="#D9503D"/><polygon points="64,10 64,54 106,54" fill="#EE7A60"/>'
            '<polygon points="22,54 64,118 64,54" fill="#A9311F"/><polygon points="64,54 64,118 106,54" fill="#C2402D"/></g>')


def build(lang, theme):
    c, t = THEMES[theme], TEXT[lang]
    o = []
    a = o.append
    a(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" font-family="{FONT}">')
    a('<defs>')
    a(f'<filter id="sh" x="-10%" y="-10%" width="120%" height="130%"><feDropShadow dx="0" dy="4" stdDeviation="8" flood-color="{c["shadow"]}"/></filter>')
    for name, col in (("ac", c["accent"]), ("se", c["seal"])):
        a(f'<marker id="{name}" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">'
          f'<path d="M1,1 L9,5 L1,9 z" fill="{col}"/></marker>')
    a('</defs>')
    a(f'<rect width="{W}" height="{H}" rx="20" fill="{c["bg"]}"/>')

    def text(x, y, s, size=14, fill=None, weight=400, anchor="start", family=None):
        f = f' font-family="{family}"' if family else ""
        a(f'<text x="{x}" y="{y}" font-size="{size}" font-weight="{weight}" fill="{fill or c["fg"]}" text-anchor="{anchor}"{f}>{escape(s)}</text>')

    def card(x, y, w, h, fill=None, stroke=None, r=14, shadow=True, dash=None):
        d = f' stroke-dasharray="{dash}"' if dash else ""
        fl = ' filter="url(#sh)"' if shadow else ""
        a(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="{fill or c["surface"]}" stroke="{stroke or c["line"]}"{d}{fl}/>')

    def chip(x, y, w, label, fill, fg, mono=False, h=30):
        a(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="8" fill="{fill}"/>')
        text(x + w / 2, y + h / 2 + 5, label, 13, fg, 600, "middle", MONO if mono else None)

    def arrow(x1, y1, x2, y2, col, marker):
        a(f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{col}" stroke-width="2" marker-end="url(#{marker})"/>')

    # 列标题
    top = 74
    text(28, 48, t["machines"], 13, c["fg3"], 600)

    # 设备卡片
    dx, dw, dh = 28, 278, 150
    daemons = []
    for i, name in enumerate((t["dev1"], t["dev2"])):
        y = top + i * (dh + 22)
        card(dx, y, dw, dh)
        a(f'<circle cx="{dx + 20}" cy="{y + 24}" r="4" fill="{c["run"]}"/>')
        text(dx + 32, y + 29, name, 14, c["fg"], 600)
        chip(dx + 16, y + 44, 118, "Claude Code", c["sunken"], c["fg2"])
        chip(dx + 144, y + 44, 118, "Codex", c["sunken"], c["fg2"])
        a(f'<line x1="{dx + dw / 2}" y1="{y + 80}" x2="{dx + dw / 2}" y2="{y + 98}" stroke="{c["strong"]}" stroke-width="1.5" stroke-dasharray="3 3"/>')
        text(dx + dw / 2 + 10, y + 94, t["agents_lbl"], 11, c["fg3"], 500)
        chip(dx + 16, y + 102, dw - 32, "ks daemon", c["accent_soft"], c["accent"], mono=True, h=32)
        daemons.append(y + 118)
    my = top + 2 * (dh + 22)
    card(dx, my, dw, 44, fill="none", stroke=c["strong"], shadow=False, dash="5 5")
    text(dx + dw / 2, my + 27, t["more"], 13, c["fg3"], 500, "middle")

    # Hub
    hx, hw = 390, 216
    hy, hh = top, my + 44 - top
    card(hx, hy, hw, hh, stroke=c["strong"])
    a(kite(hx + hw / 2 - 26, hy + 22, 52))
    text(hx + hw / 2, hy + 102, t["hub_t"], 18, c["fg"], 700, "middle", MONO)
    text(hx + hw / 2, hy + 124, t["hub_s"], 12, c["fg3"], 400, "middle")
    for i, item in enumerate(t["hub_items"]):
        iy = hy + 150 + i * 48
        a(f'<rect x="{hx + 18}" y="{iy}" width="{hw - 36}" height="34" rx="9" fill="{c["bg"]}" stroke="{c["line"]}"/>')
        a(f'<rect x="{hx + 30}" y="{iy + 13}" width="8" height="8" rx="2" fill="{c["seal"] if i == 1 else c["accent"]}"/>')
        text(hx + 48, iy + 22, item, 13, c["fg2"], 600)
    text(hx + hw / 2, hy + hh - 18, t["hub_foot"], 11, c["fg3"], 500, "middle")

    # 设备 ↔ Hub
    for y in daemons:
        x1, x2 = dx + dw - 14, hx
        arrow(x1, y - 7, x2 - 4, y - 7, c["accent"], "ac")
        arrow(x2, y + 7, x1 + 4, y + 7, c["seal"], "se")
    lx = (dx + dw - 14 + hx) / 2
    text(lx, daemons[0] - 16, t["ev"], 11, c["accent"], 600, "middle")
    text(lx, daemons[0] + 26, t["dec"], 11, c["seal"], 600, "middle")

    # 你
    yx, yw = 680, 252
    card(yx, hy, yw, hh)
    text(yx + 22, hy + 34, t["you"], 16, c["fg"], 700)
    text(yx + 22, hy + 54, t["you_s"], 12, c["fg3"])
    # 浏览器 + 手机小样
    bx, by = yx + 22, hy + 72
    a(f'<rect x="{bx}" y="{by}" width="150" height="96" rx="8" fill="{c["bg"]}" stroke="{c["strong"]}"/>')
    a(f'<line x1="{bx}" y1="{by + 16}" x2="{bx + 150}" y2="{by + 16}" stroke="{c["line"]}"/>')
    for k, col in enumerate((c["seal"], c["strong"], c["strong"])):
        a(f'<circle cx="{bx + 10 + k * 9}" cy="{by + 8}" r="2.5" fill="{col}"/>')
    for k in range(3):
        a(f'<rect x="{bx + 10 + k * 46}" y="{by + 26}" width="38" height="60" rx="4" fill="{c["surface"]}" stroke="{c["line"]}"/>')
        a(f'<rect x="{bx + 15 + k * 46}" y="{by + 32}" width="20" height="4" rx="2" fill="{(c["run"], c["seal"], c["accent"])[k]}"/>')
        a(f'<rect x="{bx + 15 + k * 46}" y="{by + 42}" width="28" height="3" rx="1.5" fill="{c["line"]}"/>')
        a(f'<rect x="{bx + 15 + k * 46}" y="{by + 50}" width="22" height="3" rx="1.5" fill="{c["line"]}"/>')
    px, py = bx + 168, by + 4
    a(f'<rect x="{px}" y="{py}" width="48" height="88" rx="9" fill="{c["bg"]}" stroke="{c["strong"]}"/>')
    a(f'<rect x="{px + 6}" y="{py + 14}" width="36" height="22" rx="4" fill="{c["seal_soft"]}" stroke="{c["seal"]}" stroke-width="1"/>')
    a(f'<rect x="{px + 11}" y="{py + 21}" width="20" height="3" rx="1.5" fill="{c["seal"]}"/>')
    a(f'<rect x="{px + 11}" y="{py + 28}" width="14" height="3" rx="1.5" fill="{c["seal"]}"/>')
    a(f'<rect x="{px + 6}" y="{py + 42}" width="36" height="12" rx="3" fill="{c["surface"]}" stroke="{c["line"]}"/>')
    a(f'<rect x="{px + 6}" y="{py + 58}" width="36" height="12" rx="3" fill="{c["surface"]}" stroke="{c["line"]}"/>')
    for i, item in enumerate(t["you_items"]):
        iy = hy + 200 + i * 36
        a(f'<circle cx="{yx + 30}" cy="{iy + 10}" r="9" fill="{c["seal_soft"] if i == 1 else c["sunken"]}"/>')
        a(f'<path d="M{yx + 26},{iy + 10} l3,3 l5,-6" fill="none" stroke="{c["seal"] if i == 1 else c["fg3"]}" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>')
        text(yx + 48, iy + 15, item, 13, c["fg2"], 500)

    # Hub ↔ 你
    ay = hy + hh / 2 - 20
    arrow(hx + hw, ay - 7, yx - 4, ay - 7, c["accent"], "ac")
    arrow(yx, ay + 7, hx + hw + 4, ay + 7, c["seal"], "se")
    text((hx + hw + yx) / 2, ay - 16, t["live"], 11, c["accent"], 600, "middle")
    text((hx + hw + yx) / 2, ay + 26, t["ans"], 11, c["seal"], 600, "middle")

    # 底部闭环
    sy = H - 62
    a(f'<rect x="28" y="{sy}" width="{W - 56}" height="40" rx="12" fill="{c["surface"]}" stroke="{c["line"]}"/>')
    seg = (W - 56) / 3
    for i, s in enumerate(t["steps"]):
        cx = 28 + seg * i + 26
        a(f'<circle cx="{cx}" cy="{sy + 20}" r="10" fill="{c["seal"]}"/>')
        text(cx, sy + 24.5, str(i + 1), 12, "#fbf6ec", 700, "middle")
        text(cx + 18, sy + 25, s, 13, c["fg2"], 500)
        if i < 2:
            ex = 28 + seg * (i + 1)
            a(f'<path d="M{ex - 14},{sy + 14} l6,6 l-6,6" fill="none" stroke="{c["strong"]}" stroke-width="1.8" stroke-linecap="round"/>')
    a('</svg>')
    return "\n".join(o)


for lang in TEXT:
    for theme in THEMES:
        suffix = "" if lang == "en" else ".zh-CN"
        with open(f"{OUT}/architecture-{theme}{suffix}.svg", "w") as f:
            f.write(build(lang, theme))
