import { useContext, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Columns2, Trash2 } from 'lucide-react'
import { api, errMsg, unwrap } from '../api/client'
import { t } from '../i18n'
import { VARIANT_CMP, type AssetRefLite, type AssetStatus } from '../lib/assets'
import { cx } from '../lib/util'
import { Button } from '../ui/base'
import { useConfirm } from '../ui/overlay'
import { AssetBadge, AssetExtrasContext, type Detail } from './assets'

// 素材详情里的变体与取代关系（R-380），和删除推错的素材（只有所有者，网页上确认）。
// 变体、上级、取代关系都可点开那件素材；访客页同样显示，点开范围外的素材时显示「素材不存在或已删除」。

function useOpen() {
  const [, setParams] = useSearchParams()
  return (kv: Record<string, string | null>) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      for (const [k, v] of Object.entries(kv)) {
        if (v) n.set(k, v)
        else n.delete(k)
      }
      return n
    })
}

const refText = (r: AssetRefLite) => r.uid || `${r.batch ? `${r.batch}/` : ''}${r.key}`

function RefChip({ r, label, onOpen }: { r: AssetRefLite; label?: string; onOpen: () => void }) {
  return (
    <button onClick={onOpen} className="flex h-7 max-w-full min-w-0 items-center gap-1.5 rounded-lg border border-line bg-surface px-2 text-[12.5px] text-fg-2 transition hover:border-line-strong hover:text-fg" title={refText(r)} data-asset-ref={r.code}>
      <span className="truncate">{label ?? refText(r)}</span>
      <AssetBadge status={r.status as AssetStatus} code={r.code} className="h-[18px] shrink-0 px-1.5 text-[11px]" />
    </button>
  )
}

function Row({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 gap-3 border-b border-line px-3 py-2 last:border-b-0">
      <dt className="w-[4.5em] shrink-0 pt-1 text-fg-3">{k}</dt>
      <dd className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">{children}</dd>
    </div>
  )
}

// AssetLinks：变体（素材本身列出全部变体，变体写它属于哪件素材）、取代关系。没有时不显示。
export function AssetLinks({ a }: { a: Detail }) {
  const set = useOpen()
  const open = (code: string) => set({ asset: code, cmp: null })
  const variants = a.variants ?? []
  const supersedes = a.supersedes ?? []
  if (!variants.length && !a.parent && !a.superseded_by && !supersedes.length) return null
  return (
    <dl className="mt-3 overflow-hidden rounded-xl border border-line text-[13px]" data-asset-links="">
      {variants.length > 0 && (
        <Row k={t.asset.meta.variants}>
          {a.variant && <span className="rounded-lg bg-accent-soft px-2 text-[12.5px] leading-7 text-accent">{t.asset.defaultVariant(a.variant)}</span>}
          {variants.map((v) => (
            <RefChip key={v.id} r={v} label={v.variant} onOpen={() => open(v.code)} />
          ))}
          {a.uid && (
            <Button size="sm" variant="ghost" icon={<Columns2 className="size-4" />} onClick={() => set({ cmp: VARIANT_CMP + a.uid, asset: null })}>
              {t.asset.compareVariants}
            </Button>
          )}
        </Row>
      )}
      {a.parent && (
        <Row k={t.asset.variantOf}>
          <RefChip r={a.parent} onOpen={() => open(a.parent!.code)} />
          <span className="text-fg-3">{t.asset.variantName(a.variant ?? '')}</span>
        </Row>
      )}
      {a.superseded_by && (
        <Row k={t.asset.superseded}>
          <RefChip r={a.superseded_by} onOpen={() => open(a.superseded_by!.code)} />
        </Row>
      )}
      {supersedes.length > 0 && (
        <Row k={t.asset.supersedes}>
          {supersedes.map((r) => (
            <RefChip key={r.id} r={r} onOpen={() => open(r.code)} />
          ))}
        </Row>
      )}
    </dl>
  )
}

// DeleteAsset：删除推错的素材。先查会连带什么（版本、审核记录、变体、被它取代的条目、引用着它的分享），确认框里逐条写出；
// 有审核记录或被分享引用时额外提示。删除不走撤销期（真删，无法恢复），所以必须确认。
export function DeleteAsset({ a, onDone }: { a: Detail; onDone?: () => void }) {
  const ex = useContext(AssetExtrasContext)
  const confirm = useConfirm()
  const qc = useQueryClient()
  const set = useOpen()
  const [busy, setBusy] = useState(false)
  if (ex) return null // 访客页不能删
  const what = `${t.asset.del.verb} ${a.code}`
  const run = async () => {
    setBusy(true)
    try {
      const info = await unwrap(api.GET('/api/assets/{ref}/delete-check', { params: { path: { ref: a.code } } }))
      const shares = info.shares ?? []
      const variants = info.variants ?? []
      const supersedes = info.supersedes ?? []
      const warn: string[] = []
      if (info.reviews > 0) warn.push(t.asset.del.reviews(info.reviews))
      if (shares.length) warn.push(t.asset.del.shares([...new Set(shares.map((x) => x.name))]))
      const also: string[] = []
      if (variants.length) also.push(t.asset.del.variants(variants.map((v) => v.variant || v.code)))
      if (supersedes.length) also.push(t.asset.del.supersedes(supersedes.map((x) => x.code)))
      const ok = await confirm({
        title: t.asset.del.title(a.code),
        danger: true,
        confirmText: t.asset.del.verb,
        description: (
          <span className="grid gap-2" data-delete-check="">
            <span>{t.asset.del.body(info.versions)}</span>
            {also.map((x) => (
              <span key={x}>{x}</span>
            ))}
            {warn.map((x) => (
              <span key={x} className="rounded-lg bg-danger-soft px-2.5 py-1.5 text-danger" data-delete-warn="">
                {x}
              </span>
            ))}
          </span>
        ),
      })
      if (!ok) return
      await unwrap(api.DELETE('/api/assets/{ref}', { params: { path: { ref: a.code } } }))
      toast(t.asset.del.done(a.code))
      set({ asset: null })
      onDone?.()
      await qc.invalidateQueries({ queryKey: ['assets'] })
    } catch (e) {
      toast.error(errMsg(e, what))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className={cx('mt-6 border-t border-line pt-4')}>
      <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 className="size-4" />} loading={busy} onClick={run} data-delete-asset="">
        {t.asset.del.button}
      </Button>
      <p className="mt-1 text-xs text-fg-3">{t.asset.del.hint}</p>
    </div>
  )
}
