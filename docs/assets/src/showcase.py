#!/usr/bin/env python3
"""把 iPad 与 iPhone 截图拼成带设备边框的展示图（明暗两套）。
用法：python3 docs/assets/src/showcase.py docs/assets（需要 Pillow）"""
import sys
from PIL import Image, ImageDraw, ImageFilter

D = sys.argv[1]
THEMES = {
    "light": dict(bg=(244, 240, 230), glow=(233, 222, 200), bezel=(28, 28, 30), shadow=(70, 50, 18, 70)),
    "dark": dict(bg=(28, 34, 50), glow=(40, 50, 72), bezel=(8, 9, 12), shadow=(0, 0, 0, 140)),
}
W, H = 2400, 1400


def rounded(im, r):
    m = Image.new("L", im.size, 0)
    ImageDraw.Draw(m).rounded_rectangle((0, 0, im.width - 1, im.height - 1), r, fill=255)
    out = Image.new("RGBA", im.size)
    out.paste(im, (0, 0), m)
    return out


def device(shot, width, bezel, r_screen, color):
    s = shot.convert("RGB")
    s = s.resize((width, round(s.height * width / s.width)), Image.LANCZOS)
    s = rounded(s, r_screen)
    f = Image.new("RGBA", (s.width + bezel * 2, s.height + bezel * 2))
    ImageDraw.Draw(f).rounded_rectangle((0, 0, f.width - 1, f.height - 1), r_screen + bezel, fill=color)
    f.alpha_composite(s, (bezel, bezel))
    return f


def shadowed(canvas, dev, xy, color):
    sh = Image.new("RGBA", canvas.size)
    m = Image.new("L", dev.size, 0)
    m.paste(dev.getchannel("A"))
    blob = Image.new("RGBA", dev.size, color)
    sh.paste(blob, (xy[0], xy[1] + 28), m)
    sh = sh.filter(ImageFilter.GaussianBlur(40))
    canvas.alpha_composite(sh)
    canvas.alpha_composite(dev, xy)


def build(theme, tablet, phone, out):
    c = THEMES[theme]
    canvas = Image.new("RGBA", (W, H), c["bg"] + (255,))
    glow = Image.new("RGBA", (W, H))
    ImageDraw.Draw(glow).ellipse((W * 0.18, H * 0.05, W * 0.95, H * 1.1), fill=c["glow"] + (255,))
    canvas.alpha_composite(glow.filter(ImageFilter.GaussianBlur(160)))
    pad = device(Image.open(f"{D}/{tablet}-{theme}.png"), 1640, 34, 30, c["bezel"])
    ph = device(Image.open(f"{D}/{phone}-{theme}.png"), 520, 22, 64, c["bezel"])
    shadowed(canvas, pad, (150, (H - pad.height) // 2), c["shadow"])
    shadowed(canvas, ph, (W - ph.width - 170, H - ph.height - 70), c["shadow"])
    m = Image.new("L", (W, H), 0)
    ImageDraw.Draw(m).rounded_rectangle((0, 0, W - 1, H - 1), 48, fill=255)
    final = Image.new("RGBA", (W, H))
    final.paste(canvas, (0, 0), m)
    final.save(out, optimize=True)


for t in THEMES:
    build(t, "ipad-proposals", "phone-sessions", f"{D}/showcase-{t}.png")
