import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { PenLine } from 'lucide-react'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { DRAFT_STORE, LEGACY_STORE, hasContent, migrate, ownerOf, parse, prune, serialize, sweep, type Closed, type DraftData, type DraftMap } from '../lib/drafts'
import { useDecisions, usePlans, useRequirements, useSessions } from '../api/queries'
import { PendingBar, schedule, undo, usePending } from './verdict'

// 输入草稿（R-202，docs/principles.md 第 9 条）：所有带缓存的输入框共用这一个 useDraft(key)。
// - 只存在本机浏览器；单段文字最多 2 万字、贴图只存已上传文件的引用、总量约 1MB，超出先清最久没动的（规则在 lib/drafts.ts）。
// - 什么时候清：提交成功（drop）；撤销期内撤回不清（提交成功的回调里才 drop）；对象关闭（已完成 / 已答复 / 已搁置）后自动清（useDraftSweep）；7 天没动的清。
// - 回来时直接恢复，下面一行小字「已恢复上次没写完的内容 · 清空」（DraftLine）；「清空」立即清掉文字和贴图并删除草稿，不走撤销期（用户 2026-10-24 退回意见）；
//   编辑已有内容时的「恢复原文」走全站同一套撤销期（verdict.tsx 的 schedule，第 8 条），3 秒内可撤回。
// - localStorage 读写都包在 try 里：拿不到（隐私模式、被禁用）时只在内存里存，照常能写能发。

let mem: DraftMap | null = null
let ver = 0
const subs = new Set<() => void>()
const emit = () => {
  ver++
  subs.forEach((f) => f())
}
let timer = 0
let lastKey: string | undefined

function store(): DraftMap {
  if (mem) return mem
  const now = Date.now()
  let m: DraftMap = {}
  let legacy = false
  try {
    m = parse(localStorage.getItem(DRAFT_STORE))
    const old = localStorage.getItem(LEGACY_STORE)
    if (old != null) {
      m = { ...migrate(parse(old), now), ...m }
      legacy = true
    }
  } catch {
    /* 拿不到本机存储：只在内存里存 */
  }
  mem = prune(m, now)
  // 决策答复的老草稿（ks.drafts.v2）搬过来：新格式写成功了才删老的
  if (legacy && persist()) {
    try {
      localStorage.removeItem(LEGACY_STORE)
    } catch {
      /* 忽略 */
    }
  }
  return mem
}

function persist(): boolean {
  timer = 0
  if (!mem) return false
  const out = serialize(mem, Date.now(), lastKey)
  // 过期的、总量超了被挤掉的，内存里也去掉
  if (Object.keys(out).length !== Object.keys(mem).length) {
    const keep = new Set(Object.keys(out))
    mem = Object.fromEntries(Object.entries(mem).filter(([k]) => keep.has(k)))
    emit()
  }
  try {
    localStorage.setItem(DRAFT_STORE, JSON.stringify(out))
    return true
  } catch {
    return false
  }
}

const persistSoon = () => {
  if (typeof window === 'undefined') return
  if (!timer) timer = window.setTimeout(persist, 300)
}

if (typeof window !== 'undefined') {
  // 关页面、切走标签页时立刻写下去
  const flush = () => timer && (window.clearTimeout(timer), persist())
  window.addEventListener('pagehide', flush)
  document.addEventListener('visibilitychange', () => document.visibilityState === 'hidden' && flush())
  // 别的标签页改了草稿：跟着更新
  window.addEventListener('storage', (e) => {
    if (e.key !== DRAFT_STORE) return
    mem = prune(parse(e.newValue), Date.now())
    emit()
  })
}

function put(key: string, d: DraftData) {
  const m = store()
  mem = { ...m, [key]: { at: Date.now(), d } }
  lastKey = key
  emit()
  persistSoon()
}

// dropDraft：提交成功后清掉这条草稿。
export function dropDraft(...keys: string[]) {
  const m = store()
  if (!keys.some((k) => k in m)) return
  mem = { ...m }
  keys.forEach((k) => delete mem![k])
  emit()
  persistSoon()
}

// draftExists：这一条有没有草稿（不订阅变化，打开时判断用）
export const draftExists = (key: string) => key in store()

function useStore() {
  useSyncExternalStore(
    (f) => {
      subs.add(f)
      return () => subs.delete(f)
    },
    () => ver,
  )
  return store()
}

// 「恢复原文」的撤销期键；撤销期里显示初始值（原文），走完才删草稿
const clearKeyOf = (key: string) => `draft:${key}`
const clearNext = new Map<string, DraftData | undefined>()

export type DraftHandle<T extends DraftData> = {
  value: T
  set: (patch: Partial<T>) => void
  // clear：清空，立即生效、不给撤销（next 不传时回到初始值；编辑已有内容时传 { text: '' }）
  clear: (next?: Partial<T>) => void
  // revert：恢复原文（回到初始值），3 秒撤销期
  revert: () => void
  drop: () => void // 提交成功：立刻清掉，不给撤销
  dirty: boolean // 有内容（有草稿）
  restored: boolean // 打开时就有上次没写完的内容
  clearing: boolean // 在「恢复原文」的撤销期里
  clearKey?: string
}

// useDraft：key 为空时只在组件里存（不缓存），用法一样。has 判断有没有要存的内容（默认：有非空文字、贴图、选项）。
// epoch：一直挂着的抽屉（新建需求、编辑计划）传 open，每次打开重新判断「回来时有没有草稿」。
export function useDraft<T extends DraftData>(key: string | undefined, init: T, has: (v: T) => boolean = hasContent, epoch?: unknown): DraftHandle<T> {
  const m = useStore()
  const [local, setLocal] = useState<Partial<T> | undefined>(undefined)
  const ck = key ? clearKeyOf(key) : undefined
  const pend = usePending(ck)
  const clearing = !!pend && pend.phase !== 'done'
  const stored = key ? (m[key]?.d as Partial<T> | undefined) : local
  const value = (clearing ? { ...init, ...clearNext.get(ck!) } : { ...init, ...stored }) as T
  const dirty = has(value)
  // 打开时（或换到另一件时）有没有上次的草稿
  const first = useRef<{ key?: string; epoch?: unknown; v: boolean } | null>(null)
  if (!first.current || first.current.key !== key || first.current.epoch !== epoch) first.current = { key, epoch, v: !!key && !!stored && has({ ...init, ...stored } as T) }
  const write = (v: T) => {
    if (!key) return setLocal(v)
    if (has(v)) put(key, v)
    else dropDraft(key)
  }
  return {
    value,
    dirty,
    restored: first.current.v,
    clearing,
    clearKey: ck,
    set: (patch) => {
      // 恢复原文的撤销期里又写了：不再撤回，以新写的为准
      if (clearing && ck) {
        undo(ck)
        return write({ ...value, ...patch })
      }
      // 以存着的最新值为底（同一下里连着改两处也不丢）
      const cur = key ? (store()[key]?.d as Partial<T> | undefined) : local
      write({ ...init, ...cur, ...patch } as T)
    },
    clear: (next) => {
      if (clearing && ck) undo(ck)
      if (!key) return setLocal(next)
      const v = { ...init, ...next } as T
      if (next && has(v)) put(key, v)
      else dropDraft(key)
    },
    revert: () => {
      if (!key || !ck) return setLocal(undefined)
      clearNext.set(ck, undefined)
      schedule(ck, {
        act: 'move',
        label: t.draft.revert,
        subject: t.draft.subject,
        commit: async () => {
          dropDraft(key)
          return true
        },
      })
    },
    drop: () => (key ? dropDraft(key) : setLocal(undefined)),
  }
}

// DraftLine：输入框下面的一行小字——回来时「已恢复上次没写完的内容 · 清空」，平时有内容就只有「清空」；
// 编辑已有内容时还有「恢复原文」（revert）。清空立即生效；恢复原文后这一行换成撤销条，3 秒内可撤回。
// 点这几个字不让输入框失焦（失焦就保存了）。
export function DraftLine<T extends DraftData>({ h, empty, revert, noClear, className }: { h: DraftHandle<T>; empty?: Partial<T>; revert?: boolean; noClear?: boolean; className?: string }) {
  if (h.clearing && h.clearKey) return <PendingBar k={h.clearKey} className={cx('mt-1.5', className)} />
  if (!h.dirty) return null
  const canClear = !noClear && (!empty || hasContent(Object.fromEntries(Object.keys(empty).map((k) => [k, h.value[k]]))))
  const keep = (e: React.MouseEvent | React.PointerEvent) => e.preventDefault()
  const link = 'shrink-0 text-fg-3 underline-offset-2 transition hover:text-fg hover:underline'
  const parts = [
    canClear && (
      <button key="c" type="button" onMouseDown={keep} onClick={() => h.clear(empty)} className={link}>
        {t.draft.clear}
      </button>
    ),
    revert && (
      <button key="r" type="button" onMouseDown={keep} onClick={h.revert} className={link}>
        {t.draft.revert}
      </button>
    ),
  ].filter(Boolean)
  if (!parts.length) return null
  return (
    <div data-draft-line="" className={cx('mt-1 flex min-w-0 items-center gap-1.5 text-xs text-fg-3', className)}>
      {h.restored && (
        <>
          <span className="min-w-0 truncate">{t.draft.restored}</span>
          <span aria-hidden>·</span>
        </>
      )}
      {parts.flatMap((p, i) => (i ? [<span key={`s${i}`} aria-hidden>·</span>, p] : [p]))}
    </div>
  )
}

// useHasDraft：这些对象（需求 / 决策 / 计划编号，会话为 s:会话键）有没有没发出的草稿——「草稿」记号用。
export function useHasDraft(...owners: (string | undefined)[]) {
  const m = useStore()
  const want = owners.filter(Boolean)
  if (!want.length) return false
  return Object.keys(m).some((k) => want.includes(ownerOf(k)))
}

// DraftMark：「要你处理」条目、需求卡上的「草稿」记号。
export function DraftMark({ owners, className }: { owners: (string | undefined)[]; className?: string }) {
  const on = useHasDraft(...owners)
  if (!on) return null
  return (
    <span data-draft-mark="" className={cx('inline-flex shrink-0 items-center gap-0.5 rounded bg-wait-soft px-1 text-[11px] leading-4 font-medium text-wait', className)}>
      <PenLine className="size-2.5" />
      {t.draft.mark}
    </span>
  )
}

// useDraftSweep：对象关闭后自动清掉它的草稿（全站挂一次，Shell）。
// 需求已完成 / 已搁置：清掉比关闭时间早的（关了以后再写的留言照留）；决策不再待决策、计划已批准 / 退回、阶段已过、会话结束或不再问你：直接清。
export function useDraftSweep() {
  const reqs = useRequirements().data
  const decs = useDecisions().data
  const plans = usePlans().data
  const sessions = useSessions().data?.sessions
  useEffect(() => {
    const m = store()
    if (!Object.keys(m).length) return
    const rq = reqs && new Map(reqs.map((r) => [r.code, r]))
    const dc = decs && new Map(decs.map((d) => [d.code, d]))
    const pl = plans && new Map(plans.map((x) => [x.code, x]))
    const ss = sessions && new Map(sessions.map((s) => [s.key, s]))
    const closed: Closed = (k) => {
      const [kind, code, sub] = k.split(':')
      switch (kind) {
        case 'req':
        case 'edit':
        case 'reason': {
          const r = rq?.get(code)
          return r && (r.status === 'done' || r.status === 'shelved') ? { at: Date.parse(r.updated_at) } : undefined
        }
        case 'dec': {
          const d = dc?.get(code)
          return d && d.status !== 'pending' ? {} : undefined
        }
        case 'plan': {
          const x = pl?.get(code)
          return x && (x.status !== 'draft' || x.returned) ? {} : undefined
        }
        case 'stage': {
          const x = pl?.get(code)
          const st = x?.stages.find((s) => String(s.seq) === sub)
          return x && (x.status !== 'active' || !st || st.status === 'passed') ? {} : undefined
        }
        case 'planedit': {
          const x = pl?.get(code)
          return x && (x.status === 'done' || x.status === 'shelved') ? {} : undefined
        }
        case 'reply': {
          if (!ss) return undefined
          const s = ss.get(k.slice('reply:'.length))
          return !s || s.state === 'ended' ? {} : undefined
        }
        default:
          return undefined
      }
    }
    const gone = sweep(m, closed)
    if (gone.length) dropDraft(...gone)
  }, [reqs, decs, plans, sessions])
}
