import { SquareDashed } from 'lucide-react'
import { t } from '../i18n'
import { cx } from '../lib/util'

// 截图的标题与改动框（R-183）：Agent 交证据时给每张图写一句话标题，并可以用矩形框标出这次改了哪里。
// 框的坐标是图片原始尺寸的比例（0～1），叠在图上画，所以图怎么缩放都对得上；可一键隐藏看原图。旧证据没有标题和框，照常显示。

export type Box = { x: number; y: number; w: number; h: number; label?: string | null; criterion?: number | null }
type Shot = { name: string; title?: string | null; boxes?: Box[] | null }

// shotTitle：图的标题；没写时用文件名兜底。
export const shotTitle = (f: Shot) => f.title?.trim() || f.name

const pct = (v: number) => `${Math.max(0, Math.min(1, v)) * 100}%`
// 只有一个框且没有标签时不编号，其它情况按顺序编号，与图下的标签列表对应
const numbered = (boxes: Box[]) => boxes.length > 1 || boxes.some((b) => b.label)

// BoxLayer：画在图上的改动框。放在一个和图片同尺寸、position: relative 的容器里。
// focus（看图里放大到某个框）：这个框加粗、四周压暗，其余的框变淡。
export function BoxLayer({ boxes, hidden, small, focus }: { boxes?: Box[] | null; hidden?: boolean; small?: boolean; focus?: number | null }) {
  if (!boxes?.length) return null
  const num = numbered(boxes)
  return (
    <span aria-hidden className={cx('pointer-events-none absolute inset-0 overflow-hidden rounded-[inherit] transition-opacity duration-200', hidden ? 'opacity-0' : 'opacity-100')}>
      {boxes.map((b, i) => {
        const on = focus === i
        const dim = focus != null && !on
        return (
          <span
            key={i}
            className={cx(
              'absolute rounded-[3px] border-seal transition-[opacity,box-shadow] duration-300',
              small ? 'border-[1.5px]' : on ? 'border-[3px]' : 'border-2',
              on ? 'shadow-[0_0_0_9999px_rgb(0_0_0/0.5)]' : 'shadow-[0_0_0_1px_rgb(255_255_255/0.75),inset_0_0_0_1px_rgb(255_255_255/0.6)]',
              dim && 'opacity-30',
            )}
            style={{ left: pct(b.x), top: pct(b.y), width: pct(b.w), height: pct(b.h) }}
          >
            {num && (
              <span
                className={cx(
                  // 编号贴在框的左上角外侧，不挡框里的内容；框贴着图片顶边时放回框内
                  'absolute -left-[2px] flex items-center justify-center bg-seal font-semibold text-seal-fg tabular',
                  b.y < 0.04 ? '-top-px rounded-br-[4px]' : 'top-0 -translate-y-full rounded-t-[4px]',
                  small ? 'size-3.5 text-[9px]' : 'size-[18px] text-[11px]',
                )}
              >
                {i + 1}
              </span>
            )}
          </span>
        )
      })}
    </span>
  )
}

// BoxLegend：图下的标签列表（① 这里改成标题优先），标签不写在图上，免得挡住内容、被截断。
export function BoxLegend({ boxes, className }: { boxes?: Box[] | null; className?: string }) {
  if (!boxes?.length || !boxes.some((b) => b.label)) return null
  return (
    <ol className={cx('grid gap-1 text-xs text-fg-2', className)}>
      {boxes.map(
        (b, i) =>
          b.label && (
            <li key={i} className="flex min-w-0 items-start gap-1.5">
              <span className="mt-px flex size-4 shrink-0 items-center justify-center rounded-[4px] bg-seal text-[10px] font-semibold text-seal-fg tabular">{i + 1}</span>
              <span className="min-w-0 break-words">{b.label}</span>
            </li>
          ),
      )}
    </ol>
  )
}

// BoxToggle：隐藏 / 显示改动框。
export function BoxToggle({ hidden, onToggle, className, dark }: { hidden: boolean; onToggle: () => void; className?: string; dark?: boolean }) {
  return (
    <button
      type="button"
      aria-pressed={!hidden}
      onClick={(e) => {
        e.stopPropagation()
        onToggle()
      }}
      className={cx(
        'inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-xs transition-colors pointer-coarse:px-2.5 pointer-coarse:py-1.5',
        dark ? 'pointer-events-auto bg-black/55 px-3 py-1.5 text-white backdrop-blur hover:bg-black/75' : 'text-fg-3 hover:bg-surface-2 hover:text-fg',
        className,
      )}
    >
      <SquareDashed className="size-3.5" />
      {hidden ? t.blocks.showBoxes : t.blocks.hideBoxes}
    </button>
  )
}
