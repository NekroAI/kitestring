import { useState, type CSSProperties } from 'react'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Segmented } from '../ui/base'
import { fileUrl } from '../ui/attach'

// SVG 素材（R-387）：当图片显示，一律用 <img> 加载——图片模式下浏览器不执行 SVG 里的脚本、不取外链；
// 不内联进页面（内联的 SVG 和页面同源，脚本、事件属性都会生效）。Hub 返回 SVG 时另带禁止脚本的内容安全策略，直接打开也不执行。
// 图标常是白色描边、透明底，默认铺在中灰棋盘格上，浅色、深色底都看得清；详情里可切换底色。

export type SvgBg = 'checker' | 'light' | 'dark'
const BG: Record<SvgBg, CSSProperties> = {
  checker: { background: 'repeating-conic-gradient(#a3a3a3 0 25%, #c2c2c2 0 50%) 50% / 16px 16px' },
  light: { background: '#f5f5f2' },
  dark: { background: '#1d1f24' },
}
let lastBg: SvgBg = 'checker' // 详情里切过的底色，同一页里打开下一件沿用

// SvgImg：一张 SVG，按比例放进容器，铺底色。
export function SvgImg({ id, bg = 'checker', className, pad = 'p-4' }: { id: string; bg?: SvgBg; className?: string; pad?: string }) {
  return (
    <div className={cx('flex items-center justify-center', pad, className)} style={BG[bg]} data-svg={id}>
      <img src={fileUrl(id)} alt="" draggable={false} className="max-h-full max-w-full object-contain" style={{ width: '100%', height: '100%' }} />
    </div>
  )
}

// SvgStage：详情的大图——SVG 铺满舞台，右上角切换底色。
export function SvgStage({ id }: { id: string }) {
  const [bg, setBg] = useState<SvgBg>(lastBg)
  return (
    <div className="absolute inset-0" data-svg-stage={bg}>
      <SvgImg id={id} bg={bg} className="absolute inset-0" pad="p-8" />
      <div className="absolute top-2 right-2 rounded-lg bg-surface/85 p-0.5 shadow-xs backdrop-blur" role="group" aria-label={t.asset.svg.bg}>
        <Segmented
          size="sm"
          value={bg}
          onChange={(v) => {
            lastBg = v
            setBg(v)
          }}
          options={(['checker', 'light', 'dark'] as const).map((v) => ({ value: v, label: t.asset.svg[v] }))}
        />
      </div>
    </div>
  )
}
