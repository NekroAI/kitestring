import { Component, type ReactNode } from 'react'
import { moves } from '../lib/pending'
import { reducedMotion } from './verdict'

// 需求池换列动画（R-200）：需求从原列滑到新列，看得出从哪到哪。
// 同一套动画用在三处（R-201 / D-422）：用户拖拽 / 改状态后立刻进新列、撤回或提交失败时回到原列、Agent 改状态后页面实时刷新。
// - 做法：换列前（getSnapshotBeforeUpdate，DOM 还是旧的）量出旧位置并复制一份旧的块；换列后（componentDidUpdate）新块先藏起来，
//   复制品从旧位置飞到新位置（transform + 宽高），落地后换回新块。复制品挂在 body 上，不被列的滚动区裁掉。
// - Agent 发起的变化（不是本机撤销期带来的）落地后有一次轻微强调（短暂描边）；本机操作块上已有撤销条遮罩，不另强调。
// - 目标在列内滚动区外时先滚到能看到；旧位置不在屏幕上（手机上别的列、已完成里没显示的）时不飞，只在新位置强调。
// - 同时多条变化一起飞；系统开了「减少动效」时直接到位，只保留强调。
// 块上要有 data-tile="R-12"，列的滚动区要有 data-col-scroll。

export const FLY_MS = 360
const EASE = 'cubic-bezier(0.2, 0.8, 0.2, 1)'

type Snap = Map<string, { rect: DOMRect; clone: HTMLElement }>
type Props = { shown: Map<string, string>; over: Map<string, string>; children: ReactNode }

const tileOf = (code: string) => document.querySelector<HTMLElement>(`[data-tile="${CSS.escape(code)}"]`)

// onScreen：块至少有一部分在视口里，并且没被所在滚动区裁掉
function onScreen(el: HTMLElement, r = el.getBoundingClientRect()) {
  if (!r.width || !r.height) return false
  if (r.bottom <= 0 || r.top >= window.innerHeight || r.right <= 0 || r.left >= window.innerWidth) return false
  const box = el.closest('[data-col-scroll]')?.getBoundingClientRect()
  return !box || (r.bottom > box.top + 4 && r.top < box.bottom - 4)
}

// 落地后的一次轻微强调：外圈描边淡入淡出
export function pulse(el: HTMLElement) {
  el.animate(
    [{ boxShadow: '0 0 0 0 transparent' }, { boxShadow: '0 0 0 2px color-mix(in oklab, var(--accent) 70%, transparent)', offset: 0.25 }, { boxShadow: '0 0 0 0 transparent' }],
    { duration: 1400, easing: 'ease-out' },
  )
}

function fly(code: string, from: { rect: DOMRect; clone: HTMLElement } | undefined, local: boolean) {
  const el = tileOf(code)
  if (!el) return
  // 目标在列内滚动区外：先滚到能看到（只滚这一列，不动整页）
  const scroller = el.closest<HTMLElement>('[data-col-scroll]')
  if (scroller) {
    const b = scroller.getBoundingClientRect()
    const r = el.getBoundingClientRect()
    if (r.top < b.top) scroller.scrollTop -= b.top - r.top + 8
    else if (r.bottom > b.bottom) scroller.scrollTop += r.bottom - b.bottom + 8
  }
  const to = el.getBoundingClientRect()
  if (reducedMotion() || !from || !onScreen(el, to)) {
    if (!local) pulse(el)
    return
  }
  const { rect, clone } = from
  Object.assign(clone.style, {
    position: 'fixed',
    left: `${rect.left}px`,
    top: `${rect.top}px`,
    width: `${rect.width}px`,
    height: `${rect.height}px`,
    margin: '0',
    zIndex: '35', // 在页面之上、在抽屉遮罩（z-40）之下：抽屉开着时不从抽屉上面飞过
    pointerEvents: 'none',
    transform: 'none',
    boxSizing: 'border-box',
  })
  clone.removeAttribute('data-tile')
  clone.setAttribute('aria-hidden', 'true')
  document.body.appendChild(clone)
  el.style.visibility = 'hidden'
  const a = clone.animate(
    [
      { transform: 'translate(0, 0)', width: `${rect.width}px`, height: `${rect.height}px`, boxShadow: 'var(--shadow-card, 0 1px 2px rgb(0 0 0 / 0.08))' },
      { transform: `translate(${(to.left - rect.left) / 2}px, ${(to.top - rect.top) / 2 - 6}px)`, boxShadow: '0 12px 28px rgb(0 0 0 / 0.16)', offset: 0.5 },
      { transform: `translate(${to.left - rect.left}px, ${to.top - rect.top}px)`, width: `${to.width}px`, height: `${to.height}px`, boxShadow: '0 1px 2px rgb(0 0 0 / 0.08)' },
    ],
    { duration: FLY_MS, easing: EASE },
  )
  const land = () => {
    clone.remove()
    el.style.visibility = ''
    if (!local) pulse(el)
  }
  a.onfinish = land
  a.oncancel = land
}

// Flights：包住需求池盘面；shown 是每条需求此刻显示的状态（含撤销期里的），over 是撤销期改到的状态（区分本机操作和 Agent）。
export class Flights extends Component<Props> {
  getSnapshotBeforeUpdate(prev: Props): Snap | null {
    const ms = moves(prev.shown, this.props.shown, prev.over, this.props.over)
    if (!ms.length) return null
    const snap: Snap = new Map()
    for (const m of ms) {
      const el = tileOf(m.code)
      if (!el) continue
      const rect = el.getBoundingClientRect()
      if (!onScreen(el, rect)) continue
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('[data-cover]').forEach((x) => x.remove()) // 撤销条遮罩不跟着飞
      snap.set(m.code, { rect, clone })
    }
    return snap
  }

  componentDidUpdate(prev: Props, _state: unknown, snap: Snap | null) {
    if (!snap) return
    for (const m of moves(prev.shown, this.props.shown, prev.over, this.props.over)) fly(m.code, snap.get(m.code), m.local)
  }

  render() {
    return this.props.children
  }
}
