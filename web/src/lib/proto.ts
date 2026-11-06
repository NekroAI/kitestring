// 原型查看器的宽度预览（R-197）：手机 390 / 平板 820 / 电脑 1280 / 适应窗口。
// 框比可用区域宽时整体缩小（按比例显示），高度按缩小后的比例放大，缩小后正好占满可用高度。

export type ProtoWidth = 'phone' | 'tablet' | 'desktop' | 'fit'
export const PROTO_WIDTHS: ProtoWidth[] = ['phone', 'tablet', 'desktop', 'fit']
export const WIDTH_PX: Record<Exclude<ProtoWidth, 'fit'>, number> = { phone: 390, tablet: 820, desktop: 1280 }

export interface FrameLayout {
  w: number // 框的逻辑宽度（原型看到的视口宽度）
  h: number // 框的逻辑高度
  scale: number // 显示时缩小的比例（≤ 1）
}

// frameLayout：stageW / stageH 是可用区域（已扣掉留白）。
export function frameLayout(width: ProtoWidth, stageW: number, stageH: number): FrameLayout {
  const sw = Math.max(1, Math.floor(stageW))
  const sh = Math.max(1, Math.floor(stageH))
  if (width === 'fit') return { w: sw, h: sh, scale: 1 }
  const w = WIDTH_PX[width]
  const scale = w > sw ? sw / w : 1
  return { w, h: Math.round(sh / scale), scale }
}

// defaultWidth：手机上默认适应窗口；宽屏默认上次选的，没选过用电脑宽度。
export function defaultWidth(viewportW: number, saved?: string | null): ProtoWidth {
  if (viewportW < 640) return 'fit'
  return PROTO_WIDTHS.includes(saved as ProtoWidth) ? (saved as ProtoWidth) : 'desktop'
}

// pct：缩放比例显示成百分比。
export const pct = (scale: number) => `${Math.round(scale * 100)}%`
