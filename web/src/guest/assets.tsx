import { useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Download, Images, Upload } from 'lucide-react'
import { errMsg } from '../api/client'
import { t } from '../i18n'
import { facetsOf, filterAssets, wallTiles, type AssetLite } from '../lib/assets'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Input, Segmented, Skeleton, Textarea, Tip } from '../ui/base'
import { downloadUrl, NoDownloadContext } from '../ui/attach'
import { DraftField, useDraft } from '../app/draft'
import { AssetCard, AssetDrawer, AssetExtrasContext, CompareDrawer, FacetMenu, GroupCard, type AssetExtras, type Detail } from '../app/assets'
import { gk, guestAssetDetail, postComment, postUpload, useGuestAssets, useMyComments } from './data'
import { mineText } from './slots'
import { Failed } from './views'

// 访客页的资源库（R-365，D-1785）：分享里的素材，图片墙、大图、3D（手机上拖动旋转）、版本对比，和所有者的资源库页共用组件。
// 抽屉底栏按这条链接的权限：只看 / 可留言（留给正在看的那一版）/ 可上传（文件给这条素材，分享者收下后成为新版本或附件）；
// 分享开了下载才有「下载原文件」，关时连看图里的下载按钮也不显示。审核（通过 / 返工）只有分享者能做，访客这里没有入口。

export function GuestAssets({ token }: { token: string }) {
  const q = useGuestAssets(token)
  const [params, setParams] = useSearchParams()
  const set = (kv: Record<string, string | null>) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      for (const [k, v] of Object.entries(kv)) {
        if (v) n.set(k, v)
        else n.delete(k)
      }
      return n
    })
  const all = useMemo(() => (q.data?.assets ?? []) as AssetLite[], [q.data])
  const batch = params.get('b') ?? ''
  const category = params.get('c') ?? ''
  const status = params.get('st') ?? ''
  const facets = useMemo(() => facetsOf(all, ''), [all])
  const shown = useMemo(() => filterAssets(all, { project: '', batch, category, status }), [all, batch, category, status])
  const tiles = useMemo(() => wallTiles(shown), [shown])
  const perm = q.data?.perm ?? 'view'
  const allow = !!q.data?.allow_download
  const useDetail = useMemo(() => guestAssetDetail(token), [token])
  const extras = useMemo<AssetExtras>(
    () => ({ useDetail: useDetail as unknown as AssetExtras['useDetail'], footer: (a, v) => <GuestFooter key={`${a.code}:${v}`} token={token} a={a} shown={v} perm={perm} allow={allow} /> }),
    [useDetail, token, perm, allow],
  )

  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-5 sm:px-6" data-guest-assets="">
      <div className="mb-4 flex flex-wrap items-center gap-x-3 gap-y-2">
        <h1 className="font-display text-[22px] font-semibold tracking-wide text-fg">{t.guest.assets}</h1>
        <span className="text-[13px] text-fg-3 tabular">{t.asset.total(facets.total)}</span>
      </div>
      {facets.total > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <Segmented
            size="sm"
            value={status || 'all'}
            onChange={(v) => set({ st: v === 'all' ? null : v })}
            options={[{ value: 'all', label: t.asset.all }, ...(q.data?.statuses ?? []).map((s) => ({ value: s, label: t.asset.status[s] ?? s, count: facets.counts[s] }))]}
          />
          <FacetMenu label={t.asset.batch} allLabel={t.asset.allBatches} value={batch} options={facets.batches} onChange={(v) => set({ b: v || null })} />
          <FacetMenu label={t.asset.category} allLabel={t.asset.allCategories} value={category} options={facets.categories} onChange={(v) => set({ c: v || null })} />
        </div>
      )}
      {q.isPending ? (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-[repeat(auto-fill,minmax(210px,1fr))]">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-[232px] rounded-2xl" />
          ))}
        </div>
      ) : !tiles.length ? (
        <div className="flex flex-col items-center gap-2 py-16 text-sm text-fg-3">
          <Images className="size-8" strokeWidth={1.4} />
          {facets.total ? t.asset.noMatch : t.guest.noAssets}
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-[repeat(auto-fill,minmax(210px,1fr))]">
          {tiles.map((tile) =>
            tile.type === 'one' ? (
              <AssetCard key={tile.asset.id} a={tile.asset} active={params.get('asset') === tile.asset.code} onOpen={() => set({ asset: tile.asset.code, cmp: null })} />
            ) : (
              <GroupCard key={tile.key} tile={tile} active={params.get('cmp') === tile.key} onOpen={() => set({ cmp: tile.key, asset: null })} />
            ),
          )}
        </div>
      )}
      <NoDownloadContext.Provider value={!allow}>
        <AssetExtrasContext.Provider value={extras}>
          <AssetDrawer code={params.get('asset')} onClose={() => set({ asset: null })} />
        </AssetExtrasContext.Provider>
        <CompareDrawer gkey={params.get('cmp')} all={all} onClose={() => set({ cmp: null })} onOpen={(code) => set({ asset: code, cmp: null })} />
      </NoDownloadContext.Provider>
    </div>
  )
}

const statusTone: Record<string, 'neutral' | 'run' | 'wait'> = { pending: 'wait', placed: 'run', ignored: 'neutral' }

// GuestFooter：访客抽屉底栏——下载原文件（分享开了下载时）、留言给正在看的这一版、上传文件、自己在这条素材上的留言和上传。
function GuestFooter({ token, a, shown, perm, allow }: { token: string; a: Detail; shown: number; perm: string; allow: boolean }) {
  const qc = useQueryClient()
  const now = useNow()
  const mine = useMyComments(token)
  const canComment = perm === 'comment' || perm === 'upload'
  const canUpload = perm === 'upload'
  const v = a.versions.find((x) => x.version === shown) ?? a.versions[0]
  const orig = v?.file ?? null
  const draft = useDraft(canComment ? `guestc:${token}:asset:${a.code}:v${shown}` : undefined, { text: '' })
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [up, setUp] = useState(false)
  const file = useRef<HTMLInputElement>(null)
  const label = t.guest.onAsset(a.code, shown)
  const mineHere = (mine.data ?? []).filter((x) => x.target_kind === 'asset' && x.target_ref === a.code).slice(0, 3)
  const refresh = () => void qc.invalidateQueries({ queryKey: gk.comments(token) })

  const send = async () => {
    const text = draft.value.text.trim()
    if (!text || busy) return
    setBusy(true)
    try {
      await postComment(token, { target_kind: 'asset', target_ref: a.code, version: shown, text })
      draft.drop()
      toast.success(t.guest.sent)
      refresh()
    } catch (e) {
      toast.error(errMsg(e, t.guest.sendFail))
    } finally {
      setBusy(false)
    }
  }
  const pick = async (f: File | undefined) => {
    if (!f) return
    setUp(true)
    try {
      await postUpload(token, a.code, shown, f, note.trim())
      setNote('')
      toast.success(t.guest.uploaded)
      refresh()
    } catch (e) {
      toast.error(errMsg(e, t.guest.uploadFail))
    } finally {
      setUp(false)
      if (file.current) file.current.value = ''
    }
  }

  return (
    <footer className="shrink-0 border-t border-line bg-surface px-4 pt-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] sm:px-5" data-guest-asset-footer="">
      <div className="grid gap-2.5">
        {mineHere.length > 0 && (
          <div className="grid gap-1" data-guest-mine="">
            {mineHere.map((x) => (
              <div key={x.id} className="flex min-w-0 items-center gap-2 text-xs text-fg-3">
                <span className="shrink-0 font-mono">{t.asset.ver(x.version ?? 0)}</span>
                <span className="min-w-0 flex-1 truncate text-fg-2">{x.file_name || x.text}</span>
                <Tip content={absTime(x.created_at)}>
                  <span className="shrink-0 tabular">{relTime(x.created_at, now)}</span>
                </Tip>
                <Badge tone={statusTone[x.status] ?? 'neutral'}>{mineText(x)}</Badge>
              </div>
            ))}
          </div>
        )}
        {canComment ? (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              void send()
            }}
            className="grid gap-1.5"
          >
            <span className="text-[12.5px] text-fg-3">
              {t.guest.leaveTo} <b className="font-mono font-medium text-fg-2">{label}</b>
            </span>
            <div className="flex items-end gap-2">
              <DraftField h={draft} className="min-w-0 flex-1">
                {(pad) => (
                  <Textarea
                    minRows={2}
                    maxLength={2000}
                    placeholder={t.guest.assetPlaceholder}
                    value={draft.value.text}
                    onChange={(e) => draft.set({ text: e.target.value })}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
                        e.preventDefault()
                        void send()
                      }
                    }}
                    className={pad}
                  />
                )}
              </DraftField>
              <Button type="submit" variant="primary" loading={busy} disabled={!draft.value.text.trim()}>
                {t.guest.send}
              </Button>
            </div>
          </form>
        ) : (
          <p className="text-[12.5px] text-fg-3">{t.guest.viewOnly}</p>
        )}
        {(canUpload || (allow && orig)) && (
          <div className="flex flex-wrap items-center gap-2">
            {canUpload && (
              <>
                <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder={t.guest.uploadNote} maxLength={2000} className="h-8 min-w-0 flex-1 basis-40 text-[13px]" aria-label={t.guest.uploadNote} />
                <input ref={file} type="file" hidden accept=".glb,.fbx,image/png,image/jpeg,image/webp,image/gif,video/mp4,video/webm,video/quicktime" onChange={(e) => void pick(e.target.files?.[0])} />
                <Tip content={t.guest.uploadHint}>
                  <Button size="sm" loading={up} icon={<Upload className="size-4" />} onClick={() => file.current?.click()}>
                    {up ? t.guest.uploading : t.guest.upload}
                  </Button>
                </Tip>
              </>
            )}
            {allow && orig && (
              <a href={downloadUrl(orig.id)} download={orig.name} className={cx('ml-auto', !canUpload && 'ml-0')}>
                <Button size="sm" variant="ghost" icon={<Download className="size-4" />} tabIndex={-1}>
                  {t.guest.download}
                </Button>
              </a>
            )}
          </div>
        )}
      </div>
    </footer>
  )
}
