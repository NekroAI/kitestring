import { useEffect, useState } from 'react'
import { Type } from 'lucide-react'
import { t } from '../i18n'
import { accelFetch } from '../lib/accel'
import { FONT_SIZES, type FileLite } from '../lib/assets'
import { cx } from '../lib/util'
import { fileUrl } from '../ui/attach'

// 字体素材（R-387）：图片墙封面和详情的字形预览都用这个字体本身来写字。
// 字体文件按 ID 取一次（fetch 拿到内容再交给 FontFace，不经 CSS 引用），注册成页面里唯一的字体名，卡片和详情共用；
// 加载完成前不显示示例文字，免得用系统字体顶替、看着像是这个字体。

type FontState = { family: string | null; failed: boolean }
const loaded = new Map<string, Promise<string>>()

function loadFont(id: string): Promise<string> {
  let p = loaded.get(id)
  if (!p) {
    p = (async () => {
      const r = await accelFetch(fileUrl(id))
      if (!r.ok) throw new Error(String(r.status))
      const family = `ks-font-${id}`
      const face = new FontFace(family, await r.arrayBuffer())
      await face.load()
      document.fonts.add(face)
      return family
    })()
    p.catch(() => loaded.delete(id)) // 失败的下次打开再试
    loaded.set(id, p)
  }
  return p
}

// useFontFace：取字体并注册；el 给了时等它滚到眼前才取（图片墙上的字体可能很大，不一次全取）。
export function useFontFace(id: string | undefined, el?: HTMLElement | null, lazy = false): FontState {
  const [st, setSt] = useState<FontState>({ family: null, failed: false })
  useEffect(() => {
    if (!id) return
    let alive = true
    setSt({ family: null, failed: false })
    const go = () =>
      loadFont(id).then(
        (family) => alive && setSt({ family, failed: false }),
        () => alive && setSt({ family: null, failed: true }),
      )
    if (!lazy) {
      void go()
      return () => {
        alive = false
      }
    }
    if (!el) return
    const io = new IntersectionObserver(
      (es) => {
        if (!es.some((e) => e.isIntersecting)) return
        io.disconnect()
        void go()
      },
      { rootMargin: '240px' },
    )
    io.observe(el)
    return () => {
      alive = false
      io.disconnect()
    }
  }, [id, el, lazy])
  return st
}

const face = (family: string) => ({ fontFamily: `"${family}"` })

// FontCover：没有预览图的字体在图片墙上的封面——用这个字体写「Aa 永」。
export function FontCover({ file, className }: { file?: FileLite | null; className?: string }) {
  const [el, setEl] = useState<HTMLDivElement | null>(null)
  const f = useFontFace(file?.id, el, true)
  return (
    <div ref={setEl} className={cx('@container flex h-full w-full items-center justify-center bg-kind-font-soft text-kind-font', className)} data-font-cover={f.family ? 'ready' : f.failed ? 'failed' : 'loading'}>
      {f.family ? (
        <span className="text-[min(44px,26cqw)] leading-none whitespace-nowrap" style={face(f.family)}>
          {t.asset.font.cover}
        </span>
      ) : (
        <Type className="size-9" strokeWidth={1.4} aria-label={f.failed ? t.asset.font.failed : t.asset.font.loading} />
      )}
    </div>
  )
}

// FontStage：详情的字形预览——示例文字可改，按几档字号排开；下面是常用字符。
export function FontStage({ file }: { file: FileLite }) {
  const f = useFontFace(file.id)
  const [text, setText] = useState<string>(t.asset.font.sampleText)
  const shown = text.trim() ? text : t.asset.font.sampleText
  return (
    <div className="absolute inset-0 flex flex-col bg-surface" data-font-stage={f.family ? 'ready' : f.failed ? 'failed' : 'loading'}>
      <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-2">
        <label className="shrink-0 text-[12px] text-fg-3" htmlFor={`font-sample-${file.id}`}>
          {t.asset.font.sample}
        </label>
        <input
          id={`font-sample-${file.id}`}
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={t.asset.font.sampleHint}
          className="h-7 min-w-0 flex-1 rounded-md border border-line bg-surface-2 px-2 text-[13px] text-fg outline-none focus:border-accent"
        />
        {text !== t.asset.font.sampleText && (
          <button type="button" onClick={() => setText(t.asset.font.sampleText)} className="shrink-0 text-[12px] text-fg-3 hover:text-fg">
            {t.asset.font.reset}
          </button>
        )}
      </div>
      {f.family ? (
        <div className="min-h-0 flex-1 overflow-auto px-4 py-3" style={face(f.family)}>
          {FONT_SIZES.map((px) => (
            <div key={px} className="flex items-baseline gap-3 border-b border-line/60 py-1.5 last:border-b-0" data-font-size={px}>
              <span className="w-10 shrink-0 font-sans text-[11px] text-fg-3 tabular-nums">{t.asset.font.px(px)}</span>
              <span className="min-w-0 break-words text-fg" style={{ fontSize: px, lineHeight: 1.3 }}>
                {shown}
              </span>
            </div>
          ))}
          <div className="mt-3">
            <div className="mb-1 font-sans text-[11px] text-fg-3">{t.asset.font.glyphs}</div>
            <p className="text-[20px] leading-relaxed break-all text-fg">{t.asset.font.glyphSet}</p>
          </div>
        </div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 text-sm text-fg-3">
          <Type className="size-8 text-kind-font" strokeWidth={1.4} />
          {f.failed ? t.asset.font.failed : t.asset.font.loading}
        </div>
      )}
    </div>
  )
}
