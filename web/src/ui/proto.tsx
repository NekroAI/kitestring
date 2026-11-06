import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Dialog } from 'radix-ui'
import { AppWindow, ChevronDown, Download, ExternalLink, X } from 'lucide-react'
import type { Prototype } from '../api/client'
import { t } from '../i18n'
import { fmtSize } from '../lib/files'
import { defaultWidth, frameLayout, pct, PROTO_WIDTHS, type ProtoWidth } from '../lib/proto'
import { absTime, cx } from '../lib/util'
import { ThumbImg } from './attach'

// 可交互原型（R-197）：证据、决策里的原型卡片，点开全屏查看。
// 原型在无来源的沙箱框里运行（iframe sandbox 不给 allow-same-origin，服务端另带 CSP sandbox），读不到纸鸢的接口和登录态。

const WIDTH_KEY = 'ks.proto.width'
const readWidth = () => {
  try {
    return localStorage.getItem(WIDTH_KEY)
  } catch {
    return null
  }
}
const saveWidth = (w: ProtoWidth) => {
  try {
    localStorage.setItem(WIDTH_KEY, w)
  } catch {
    /* 存不了就不记 */
  }
}

// ProtoCover：封面（Agent 交的截图）或占位图，右下角标「原型」。
export function ProtoCover({ p, className }: { p: Prototype; className?: string }) {
  return (
    <span className={cx('relative block overflow-hidden bg-sunken', className)}>
      {p.cover ? (
        <ThumbImg id={p.cover.id} alt="" loading="lazy" draggable={false} className="size-full object-cover object-top" />
      ) : (
        <span aria-hidden className="flex size-full flex-col gap-[9%] bg-gradient-to-b from-sunken to-surface-2 p-[9%]">
          <span className="h-[8%] w-3/5 rounded-sm bg-line-strong/70" />
          <span className="h-[8%] w-full rounded-sm bg-line-strong/50" />
          <span className="h-[8%] w-4/5 rounded-sm bg-line-strong/50" />
          <AppWindow className="mt-auto size-5 self-center text-fg-3/60" />
        </span>
      )}
      <span className="absolute right-1.5 bottom-1.5 rounded-[5px] bg-black/65 px-1.5 text-[11px] leading-[17px] font-semibold text-white">{t.proto.badge}</span>
    </span>
  )
}

const sizeOf = (p: Pick<Prototype, 'size'>) => fmtSize(p.size)
const olderOf = (p: Prototype) => Math.max(0, (p.versions?.length ?? 1) - 1)

// ProtoCard：一行原型卡片（和截图同一行高）：封面 · 标题 · 第 n 版 · 文件数 · 大小 · 历史版本 n · 打开。
export function ProtoCard({ p }: { p: Prototype }) {
  const [open, setOpen] = useState(false)
  const older = olderOf(p)
  return (
    <>
      <div
        role="button"
        tabIndex={0}
        aria-label={t.proto.openAria(p.title)}
        onClick={() => setOpen(true)}
        onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), setOpen(true))}
        data-proto={p.id}
        className="group/proto flex cursor-pointer items-center gap-3 rounded-xl border border-line bg-surface p-2.5 outline-none transition hover:border-line-strong focus-visible:ring-2 focus-visible:ring-accent/40"
      >
        <ProtoCover p={p} className="aspect-[4/3] w-[120px] shrink-0 rounded-lg border border-line @max-[520px]:w-[88px]" />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-semibold text-fg">{p.title}</span>
          <span className="mt-0.5 block truncate text-xs text-fg-3 tabular">{t.proto.meta(p.version, p.files, sizeOf(p))}</span>
          {older > 0 && <span className="mt-1 block text-xs text-accent">{t.proto.history(older)}</span>}
        </span>
        <span className="shrink-0 rounded-lg bg-accent px-3 py-1.5 text-[13px] font-semibold text-accent-fg transition group-hover/proto:brightness-110">{t.proto.open}</span>
      </div>
      <ProtoViewer p={p} open={open} onOpenChange={setOpen} />
    </>
  )
}

// ProtoList：一组原型卡片。
export function ProtoList({ ps, className }: { ps?: Prototype[] | null; className?: string }) {
  if (!ps?.length) return null
  return (
    <div className={cx('@container grid gap-2', className)}>
      {ps.map((p) => (
        <ProtoCard key={p.id} p={p} />
      ))}
    </div>
  )
}

// ProtoOpen：包住任意触发元素（例如选项卡片上的封面），点它打开查看器；点击不冒泡（不会顺带选中选项）。
export function ProtoOpen({ p, children, className }: { p: Prototype; children: ReactNode; className?: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <span
        role="button"
        tabIndex={0}
        aria-label={t.proto.openAria(p.title)}
        className={cx('block cursor-zoom-in', className)}
        onClick={(e) => {
          e.stopPropagation()
          setOpen(true)
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            e.stopPropagation()
            setOpen(true)
          }
        }}
      >
        {children}
      </span>
      <ProtoViewer p={p} open={open} onOpenChange={setOpen} />
    </>
  )
}

// ProtoViewer：全屏查看器。顶栏：标题、版本、宽度（手机 / 平板 / 电脑 / 适应窗口）、比例、新标签打开、下载、关闭。
// Esc、点框外关闭；手机上在顶栏下滑也能关闭，框里的手势留给原型。
export function ProtoViewer({ p, open, onOpenChange }: { p: Prototype; open: boolean; onOpenChange: (v: boolean) => void }) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-[80] bg-[#1d1a16]" />
        <Dialog.Content
          aria-describedby={undefined}
          onClick={(e) => e.stopPropagation()}
          className="fixed inset-0 z-[81] flex flex-col bg-[#1d1a16] text-[#f3eee6] outline-none"
        >
          {open && <ViewerBody p={p} onClose={() => onOpenChange(false)} />}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function ViewerBody({ p, onClose }: { p: Prototype; onClose: () => void }) {
  const versions = p.versions?.length ? p.versions : [{ id: p.id, version: p.version, files: p.files, size: p.size, url: p.url, download_url: p.download_url, created_at: p.created_at }]
  const [vid, setVid] = useState(p.id)
  const cur = versions.find((v) => v.id === vid) ?? versions[0]
  const [width, setWidthState] = useState<ProtoWidth>(() => defaultWidth(typeof window === 'undefined' ? 1280 : window.innerWidth, readWidth()))
  const setWidth = (w: ProtoWidth) => {
    setWidthState(w)
    saveWidth(w)
  }
  const stage = useRef<HTMLDivElement>(null)
  const [box, setBox] = useState({ w: 0, h: 0 })
  useLayoutEffect(() => {
    const el = stage.current
    if (!el) return
    const measure = () => setBox({ w: el.clientWidth, h: el.clientHeight })
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const pad = width === 'fit' ? 0 : 16
  const L = frameLayout(width, box.w - pad * 2, box.h - pad * 2)
  // 顶栏下滑关闭（手机）
  const swipe = useRef<number | null>(null)
  const btn = 'inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg border border-white/15 bg-white/5 px-2.5 text-[13px] font-medium text-[#f3eee6] transition hover:bg-white/12'
  return (
    <>
      <header
        className="flex shrink-0 flex-wrap items-center gap-2 border-b border-white/10 px-3 py-2 sm:flex-nowrap sm:px-4"
        onPointerDown={(e) => (swipe.current = e.clientY)}
        onPointerUp={(e) => {
          if (swipe.current != null && e.clientY - swipe.current > 60) onClose()
          swipe.current = null
        }}
      >
        <Dialog.Title className="min-w-0 flex-1 truncate text-[15px] font-semibold sm:flex-none">{p.title}</Dialog.Title>
        {versions.length > 1 ? (
          <label className="relative shrink-0">
            <select
              value={cur.id}
              onChange={(e) => setVid(e.target.value)}
              className="h-8 max-w-[8.5rem] appearance-none truncate rounded-lg border border-white/15 bg-white/5 pr-7 pl-2.5 sm:max-w-none text-[13px] text-[#f3eee6] tabular outline-none hover:bg-white/12"
            >
              {versions.map((v) => (
                <option key={v.id} value={v.id} className="text-black">
                  {t.proto.version(v.version)} · {absTime(v.created_at)}
                </option>
              ))}
            </select>
            <ChevronDown aria-hidden className="pointer-events-none absolute top-2 right-2 size-4 opacity-70" />
          </label>
        ) : (
          <span className="shrink-0 text-[13px] text-white/60 tabular">{t.proto.version(cur.version)}</span>
        )}
        <span className="flex-1 max-sm:hidden" />
        <div role="radiogroup" className="flex shrink-0 overflow-hidden rounded-lg border border-white/15 max-sm:order-last max-sm:w-full">
          {PROTO_WIDTHS.map((w) => (
            <button
              key={w}
              type="button"
              role="radio"
              aria-checked={width === w}
              onClick={() => setWidth(w)}
              className={cx('h-8 px-2.5 text-[13px] whitespace-nowrap transition max-sm:flex-1 max-sm:px-1', width === w ? 'bg-[#efe7d8] font-semibold text-[#1d1a16]' : 'text-[#f3eee6] hover:bg-white/10')}
            >
              {t.proto.widths[w]}
            </button>
          ))}
        </div>
        <span className="w-10 shrink-0 text-right text-xs text-white/55 tabular max-sm:hidden">{pct(L.scale)}</span>
        <a href={cur.url} target="_blank" rel="noopener noreferrer" className={cx(btn, 'max-sm:hidden')}>
          <ExternalLink className="size-3.5" />
          {t.proto.newTab}
        </a>
        <a href={cur.download_url} download className={btn}>
          <Download className="size-3.5" />
          <span className="max-sm:hidden">{t.proto.download}</span>
        </a>
        <Dialog.Close className={cx(btn, 'px-2')} aria-label={t.proto.close}>
          <X className="size-4" />
        </Dialog.Close>
      </header>
      <div
        ref={stage}
        className="relative min-h-0 flex-1 overflow-hidden"
        onClick={(e) => {
          if (e.target === e.currentTarget) onClose()
        }}
      >
        {box.w > 0 && (
          <div
            className={cx('absolute overflow-hidden bg-white shadow-[0_10px_40px_rgb(0_0_0/0.45)]', width !== 'fit' && 'rounded-[14px]')}
            style={{ width: L.w * L.scale, height: L.h * L.scale, left: Math.max(0, (box.w - L.w * L.scale) / 2), top: pad }}
          >
            <iframe
              key={cur.id}
              src={cur.url}
              title={p.title}
              sandbox="allow-scripts allow-forms"
              referrerPolicy="no-referrer"
              className="block origin-top-left border-0"
              style={{ width: L.w, height: L.h, transform: L.scale === 1 ? undefined : `scale(${L.scale})` }}
            />
          </div>
        )}
      </div>
      <p className="shrink-0 px-4 py-1.5 text-center text-xs text-white/50">{t.proto.isolated}</p>
    </>
  )
}
