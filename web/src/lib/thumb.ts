// 缩略图选档（R-246）：Hub 出 480 / 960 / 1920 三档，显示宽度（CSS 像素 × 设备像素比）超过 960 就用 1920 档，
// 不再回落原图——原图只在用户点开大图、灯箱、大切片时才取（那些地方直接用 fileUrl）。
export type ThumbTier = 480 | 960 | 1920

export function pickTier(cssWidth: number, dpr = 1): ThumbTier {
  const px = cssWidth * (dpr > 0 ? dpr : 1)
  if (!(px > 0) || px <= 480) return 480
  return px <= 960 ? 960 : 1920
}

export const tierRank = (t: ThumbTier) => (t === 1920 ? 3 : t === 960 ? 2 : 1)
