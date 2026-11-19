import { useContext, useSyncExternalStore, type ReactNode } from 'react'
import { AudioLines, Pause, Play } from 'lucide-react'
import { useAsset } from '../api/queries'
import { t } from '../i18n'
import { accel } from '../lib/accel'
import { isDeclared, waveBars, type AudioLite, type FileLite } from '../lib/assets'
import { cx } from '../lib/util'
import { fileUrl } from '../ui/attach'
import { AssetExtrasContext, type Detail } from './assets'

// 音效素材（R-381）：图片墙的波形封面和试听按钮、详情里的试听台和变体试听。
// 全页只有一个在播的音效：点另一个就换过去；播放状态按文件 ID 记，同一个文件在卡片和详情里显示同一个进度。

type PlayState = { id: string | null; playing: boolean; t: number; dur: number; failed: string | null }
let state: PlayState = { id: null, playing: false, t: 0, dur: 0, failed: null }
const subs = new Set<() => void>()
let el: HTMLAudioElement | null = null
let raf = 0

function emit(p: Partial<PlayState>) {
  state = { ...state, ...p }
  subs.forEach((f) => f())
}

function tick() {
  if (!el) return
  emit({ t: el.currentTime, dur: Number.isFinite(el.duration) ? el.duration : state.dur })
  if (!el.paused) raf = requestAnimationFrame(tick)
}

function audio(): HTMLAudioElement {
  if (el) return el
  el = new Audio()
  el.preload = 'auto'
  el.addEventListener('play', () => {
    emit({ playing: true, failed: null })
    cancelAnimationFrame(raf)
    raf = requestAnimationFrame(tick)
  })
  el.addEventListener('pause', () => emit({ playing: false }))
  el.addEventListener('ended', () => emit({ playing: false, t: 0 }))
  el.addEventListener('loadedmetadata', () => emit({ dur: Number.isFinite(el!.duration) ? el!.duration : 0 }))
  el.addEventListener('error', () => {
    // 加速地址上取不到时（R-369）换回主地址再试一次
    const main = accel.failed(el!.src)
    if (main) {
      el!.src = main
      void el!.play().catch(() => undefined)
      return
    }
    emit({ playing: false, failed: state.id })
  })
  return el
}

function start(id: string, at = 0) {
  const a = audio()
  if (state.id !== id) {
    a.pause()
    a.src = fileUrl(id)
    emit({ id, t: at, dur: 0, failed: null })
  }
  if (at > 0 || state.id === id) a.currentTime = at
  void a.play().catch(() => undefined)
}

// toggle：播放 / 暂停这个文件；正在播别的就换过来从头放。
export function toggleAudio(id: string) {
  const a = audio()
  if (state.id === id && !a.paused) return a.pause()
  if (state.id === id) return void a.play().catch(() => undefined)
  start(id)
}

// seekAudio：跳到这个文件的某个位置（0–1）并播放。
export function seekAudio(id: string, frac: number, dur: number) {
  const d = state.id === id && state.dur ? state.dur : dur
  start(id, Math.max(0, Math.min(1, frac)) * (d || 0))
}

const get = () => state
const sub = (f: () => void) => {
  subs.add(f)
  return () => subs.delete(f)
}

// usePlay：这个文件的播放状态（不是它在播时 playing = false、progress = 0）。
export function usePlay(id: string | undefined, fallbackDur = 0) {
  const s = useSyncExternalStore(sub, get)
  const mine = !!id && s.id === id
  const dur = (mine && s.dur) || fallbackDur
  return { playing: mine && s.playing, t: mine ? s.t : 0, dur, progress: mine && dur ? Math.min(1, s.t / dur) : 0, failed: !!id && s.failed === id }
}

// Wave：波形柱，已播放的部分用音效的种类色（--kind-audio），其余淡一些。
export function Wave({ wave, progress = 0, bars = 48, className }: { wave?: number[] | null; progress?: number; bars?: number; className?: string }) {
  const hs = waveBars(wave, bars)
  if (!hs.length) return null
  const w = 3
  return (
    <svg viewBox={`0 0 ${bars * w} 100`} preserveAspectRatio="none" className={className} aria-hidden="true" data-wave={hs.length}>
      {hs.map((h, i) => (
        <rect key={i} x={i * w + 0.5} y={50 - h * 50} width={w - 1} height={h * 100} rx={0.8} className={cx('fill-kind-audio', (i + 0.5) / bars > progress && 'opacity-35')} />
      ))}
    </svg>
  )
}

// AudioCover：没有预览图的音效在图片墙上的封面——波形（推送时没读出波形的画图标），播放时显示进度。
export function AudioCover({ v, className }: { v?: { file?: FileLite | null; audio?: AudioLite | null } | null; className?: string }) {
  const p = usePlay(v?.file?.id, v?.audio?.duration_s)
  return (
    <div className={cx('flex h-full w-full items-center justify-center bg-kind-audio-soft', className)} data-audio-cover="">
      {v?.audio?.wave?.length ? (
        <Wave wave={v.audio.wave} progress={p.progress} className="h-[46%] w-[78%]" />
      ) : (
        <AudioLines className="size-9 text-kind-audio" strokeWidth={1.4} aria-label={t.asset.audio.noWave} />
      )}
    </div>
  )
}

// PlayChip：卡片封面右下角的试听按钮，写着时长；播放中显示暂停和已播放的秒数。
export function PlayChip({ file, audio: info, name, className }: { file: FileLite; audio?: AudioLite | null; name: string; className?: string }) {
  const p = usePlay(file.id, info?.duration_s)
  const dur = info?.duration_s ?? p.dur
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        toggleAudio(file.id)
      }}
      className={cx('flex h-[22px] items-center gap-1 rounded-md bg-black/55 px-1.5 text-[11px] text-white tabular-nums transition hover:bg-black/75', p.failed && 'bg-danger/80', className)}
      title={p.failed ? t.asset.audio.failed : p.playing ? t.asset.audio.pause : t.asset.audio.playName(name)}
      aria-label={p.playing ? t.asset.audio.pause : t.asset.audio.playName(name)}
      data-play={file.id}
      data-playing={p.playing || undefined}
    >
      {p.playing ? <Pause className="size-3" fill="currentColor" /> : <Play className="size-3" fill="currentColor" />}
      {dur ? t.asset.audio.sec(p.playing ? p.t : dur) : null}
    </button>
  )
}

// AudioStage：详情的试听台——大波形（点哪里从哪里播）、播放 / 暂停、进度。
export function AudioStage({ file, audio: info }: { file: FileLite; audio?: AudioLite | null }) {
  const p = usePlay(file.id, info?.duration_s)
  const dur = p.dur || info?.duration_s || 0
  return (
    <div className="absolute inset-0 flex flex-col bg-kind-audio-soft/60" data-audio-stage={file.id}>
      <button
        type="button"
        className="relative min-h-0 flex-1 cursor-pointer px-6 py-8"
        onClick={(e) => {
          const r = e.currentTarget.getBoundingClientRect()
          seekAudio(file.id, (e.clientX - r.left - 24) / Math.max(1, r.width - 48), dur)
        }}
        aria-label={t.asset.audio.seek}
      >
        {info?.wave?.length ? (
          <Wave wave={info.wave} progress={p.progress} bars={96} className="h-full w-full" />
        ) : (
          <span className="flex h-full flex-col items-center justify-center gap-2 text-sm text-fg-3">
            <AudioLines className="size-9 text-kind-audio" strokeWidth={1.4} />
            {t.asset.audio.noWave}
          </span>
        )}
        {!info?.wave?.length && p.progress > 0 && <span className="absolute bottom-3 left-6 h-1 rounded bg-kind-audio" style={{ width: `calc(${p.progress * 100}% - ${p.progress * 48}px)` }} />}
      </button>
      <div className="flex shrink-0 items-center gap-3 border-t border-line/60 bg-surface/70 px-4 py-2.5">
        <button
          type="button"
          onClick={() => toggleAudio(file.id)}
          className="flex size-9 items-center justify-center rounded-full bg-kind-audio text-white shadow-xs transition hover:opacity-90"
          aria-label={p.playing ? t.asset.audio.pause : t.asset.audio.play}
          data-stage-play=""
        >
          {p.playing ? <Pause className="size-4" fill="currentColor" /> : <Play className="size-4 translate-x-px" fill="currentColor" />}
        </button>
        <span className="text-[13px] text-fg-2 tabular-nums">{t.asset.audio.time(p.t, dur)}</span>
        {p.failed && <span className="text-[13px] text-danger">{t.asset.audio.failed}</span>}
      </div>
    </div>
  )
}

// AudioFacts：详情里的时长、声道、响度三行；取自 asset.json 声明的值后面标「声明」。
export function audioRows(a: AudioLite | null | undefined): [string, ReactNode][] {
  if (!a) return []
  const mark = (f: string) =>
    isDeclared(a, f) ? (
      <span className="ml-1.5 rounded bg-sunken px-1 text-[11px] text-fg-3" title={t.asset.audio.declaredTip} data-declared={f}>
        {t.asset.audio.declared}
      </span>
    ) : null
  const ch = [a.channels ? t.asset.audio.channels(a.channels) : '', a.sample_rate ? t.asset.audio.rate(a.sample_rate) : ''].filter(Boolean).join(t.common.sep)
  const loud = a.loudness_lufs != null || a.peak_dbfs != null
  return [
    [t.asset.meta.duration, a.duration_s ? <>{t.asset.audio.sec(a.duration_s)}{mark('duration_s')}</> : ''],
    [t.asset.meta.channels, ch],
    [
      t.asset.meta.loudness,
      loud ? (
        <>
          {a.loudness_lufs != null && <>{t.asset.audio.lufs(a.loudness_lufs)}{mark('loudness_lufs')}</>}
          {a.loudness_lufs != null && a.peak_dbfs != null && t.common.sep}
          {a.peak_dbfs != null && <>{t.asset.audio.peak(a.peak_dbfs)}{mark('peak_dbfs')}</>}
        </>
      ) : (
        ''
      ),
    ],
  ]
}

// VariantPlays：同一件音效的各个变体（素材本身 + 变体）逐个试听，点哪个播哪个（R-381「多个变体文件可切换」）。
// 变体是各自的一条素材（R-380），文件要取它的详情；访客页取不到（不在分享里）的不显示。
export function VariantPlays({ a, shown }: { a: Detail; shown: { file?: FileLite | null; audio?: AudioLite | null } | undefined }) {
  const ex = useContext(AssetExtrasContext)
  const rootCode = a.parent?.code ?? a.code
  const root = (ex?.useDetail ?? useAsset)(a.parent ? rootCode : null).data ?? (a.parent ? null : a)
  const refs = root ? [{ code: root.code, variant: root.variant || '' }, ...(root.variants ?? []).map((x) => ({ code: x.code, variant: x.variant || x.code }))] : []
  if (refs.length < 2) return null
  return (
    <section className="mt-3" data-variant-plays={refs.length}>
      <h3 className="mb-2 text-[13px] font-medium text-fg-3">{t.asset.audio.variants}</h3>
      <div className="flex flex-wrap gap-2">
        {refs.map((r) => (r.code === a.code ? <VariantChip key={r.code} label={r.variant} file={shown?.file} audio={shown?.audio} current /> : <VariantRef key={r.code} code={r.code} label={r.variant} />))}
      </div>
    </section>
  )
}

function VariantRef({ code, label }: { code: string; label: string }) {
  const ex = useContext(AssetExtrasContext)
  const q = (ex?.useDetail ?? useAsset)(code)
  const v = q.data?.latest
  if (!v?.file || q.data?.kind !== 'audio') return null
  return <VariantChip label={label} file={v.file} audio={v.audio as AudioLite | null | undefined} />
}

function VariantChip({ label, file, audio: info, current }: { label: string; file?: FileLite | null; audio?: AudioLite | null; current?: boolean }) {
  const p = usePlay(file?.id, info?.duration_s)
  if (!file) return null
  return (
    <button
      type="button"
      onClick={() => toggleAudio(file.id)}
      className={cx(
        'flex h-8 max-w-full min-w-0 items-center gap-1.5 rounded-lg border px-2.5 text-[12.5px] transition',
        p.playing ? 'border-kind-audio bg-kind-audio-soft text-kind-audio' : current ? 'border-line-strong bg-surface text-fg' : 'border-line bg-surface text-fg-2 hover:border-line-strong hover:text-fg',
      )}
      title={t.asset.audio.playName(label)}
      data-variant-play={label}
      data-current={current || undefined}
    >
      {p.playing ? <Pause className="size-3.5 shrink-0" fill="currentColor" /> : <Play className="size-3.5 shrink-0" fill="currentColor" />}
      <span className="truncate font-mono">{label}</span>
      {info?.duration_s ? <span className="shrink-0 text-fg-3 tabular-nums">{t.asset.audio.sec(info.duration_s)}</span> : null}
    </button>
  )
}
