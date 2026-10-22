import { useCallback, useRef, useState, type ClipboardEvent, type DragEvent, type ReactNode } from 'react'
import { Dialog } from 'radix-ui'
import { ImagePlus, Loader2, X } from 'lucide-react'
import { toast } from 'sonner'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Button, Tip } from './base'

// 图片附件：任何可以写字的地方都可以贴图（粘贴、拖拽、选择文件）。

export type FileRef = { id: string; name: string; mime: string; size: number }

export const fileUrl = (id: string) => `/api/files/${encodeURIComponent(id)}`

async function uploadOne(file: File): Promise<FileRef> {
  const fd = new FormData()
  fd.append('file', file, file.name || 'image.png')
  const res = await fetch('/api/files', { method: 'POST', body: fd, credentials: 'same-origin' })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((body as { detail?: string }).detail || t.attach.failed)
  return body as FileRef
}

const MAX = 10 << 20

// useAttach：管理一组附图的上传状态。
export function useAttach(value: FileRef[], onChange: (v: FileRef[]) => void) {
  const [pending, setPending] = useState<{ key: string; url: string }[]>([])
  const latest = useRef(value)
  latest.current = value

  const add = useCallback(
    async (files: File[]) => {
      const images = files.filter((f) => f.type.startsWith('image/'))
      if (!images.length) return
      for (const f of images) {
        if (f.size > MAX) {
          toast.error(t.attach.tooLarge)
          continue
        }
        const key = `${f.name}-${f.size}-${Math.random()}`
        const url = URL.createObjectURL(f)
        setPending((p) => [...p, { key, url }])
        try {
          const ref = await uploadOne(f)
          onChange([...latest.current, ref])
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t.attach.failed)
        } finally {
          setPending((p) => p.filter((x) => x.key !== key))
          URL.revokeObjectURL(url)
        }
      }
    },
    [onChange],
  )

  const onPaste = useCallback(
    (e: ClipboardEvent) => {
      const files = [...e.clipboardData.files]
      if (files.some((f) => f.type.startsWith('image/'))) {
        e.preventDefault()
        void add(files)
      }
    },
    [add],
  )

  return { pending, add, onPaste, remove: (id: string) => onChange(latest.current.filter((f) => f.id !== id)) }
}

// Thumb：附图缩略图，点击放大。
export function Thumb({ f, onRemove, size = 'md' }: { f: FileRef; onRemove?: () => void; size?: 'sm' | 'md' }) {
  const box = size === 'sm' ? 'size-12' : 'size-16'
  return (
    <span className={cx('group relative inline-block shrink-0', box)}>
      <Lightbox f={f}>
        <button className={cx('block overflow-hidden rounded-lg border border-line bg-sunken transition hover:border-line-strong', box)} aria-label={f.name}>
          <img src={fileUrl(f.id)} alt={f.name} className="size-full object-cover" loading="lazy" />
        </button>
      </Lightbox>
      {onRemove && (
        <button onClick={onRemove} aria-label={t.attach.remove} className="absolute -top-1.5 -right-1.5 flex size-5 items-center justify-center rounded-full border border-line bg-surface text-fg-2 shadow-xs opacity-0 transition group-hover:opacity-100 hover:text-danger focus:opacity-100 max-lg:opacity-100">
          <X className="size-3" />
        </button>
      )}
    </span>
  )
}

export function Thumbs({ files, size }: { files?: FileRef[] | null; size?: 'sm' | 'md' }) {
  if (!files?.length) return null
  return (
    <div className="flex flex-wrap gap-2">
      {files.map((f) => (
        <Thumb key={f.id} f={f} size={size} />
      ))}
    </div>
  )
}

function Lightbox({ f, children }: { f: FileRef; children: ReactNode }) {
  return (
    <Dialog.Root>
      <Dialog.Trigger asChild>{children}</Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Overlay className="modal-fade fixed inset-0 z-[70] bg-black/80" />
        <Dialog.Content aria-describedby={undefined} className="fixed inset-0 z-[70] flex items-center justify-center p-4 outline-none">
          <Dialog.Title className="sr-only">{f.name}</Dialog.Title>
          <Dialog.Close asChild>
            <img src={fileUrl(f.id)} alt={f.name} className="max-h-full max-w-full cursor-zoom-out rounded-lg object-contain shadow-pop" />
          </Dialog.Close>
          <a href={fileUrl(f.id)} target="_blank" rel="noreferrer" className="absolute right-4 bottom-4 rounded-lg bg-black/60 px-3 py-1.5 text-xs text-white hover:bg-black/80">
            {t.attach.original}
          </a>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

// AttachZone：包住输入框，支持粘贴与拖拽；下方显示附图与“添加图片”按钮。
export function AttachZone({ value, onChange, children, className, compact }: { value: FileRef[]; onChange: (v: FileRef[]) => void; children: ReactNode; className?: string; compact?: boolean }) {
  const { pending, add, onPaste, remove } = useAttach(value, onChange)
  const [over, setOver] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setOver(false)
    void add([...e.dataTransfer.files])
  }
  return (
    <div
      onPaste={onPaste}
      onDragOver={(e) => {
        if ([...e.dataTransfer.items].some((i) => i.kind === 'file')) {
          e.preventDefault()
          setOver(true)
        }
      }}
      onDragLeave={() => setOver(false)}
      onDrop={onDrop}
      className={cx('relative rounded-xl transition', over && 'ring-2 ring-accent/50', className)}
    >
      {children}
      <div className={cx('flex flex-wrap items-center gap-2', (value.length || pending.length || !compact) && 'mt-2')}>
        {value.map((f) => (
          <Thumb key={f.id} f={f} size="sm" onRemove={() => remove(f.id)} />
        ))}
        {pending.map((p) => (
          <span key={p.key} className="relative size-12 overflow-hidden rounded-lg border border-line">
            <img src={p.url} alt="" className="size-full object-cover opacity-50" />
            <Loader2 className="absolute inset-0 m-auto size-4 animate-spin text-fg" />
          </span>
        ))}
        {!compact && <AddImageButton onPick={() => input.current?.click()} />}
      </div>
      <input ref={input} type="file" accept="image/png,image/jpeg,image/gif,image/webp" multiple hidden onChange={(e) => void add([...(e.target.files ?? [])]).then(() => (e.target.value = ''))} />
      {compact && <AttachTrigger onPick={() => input.current?.click()} />}
      {over && <div className="pointer-events-none absolute inset-0 flex items-center justify-center rounded-xl bg-accent-soft/80 text-sm font-medium text-accent">{t.attach.drop}</div>}
    </div>
  )
}

function AddImageButton({ onPick }: { onPick: () => void }) {
  return (
    <Tip content={t.attach.hint}>
      <button type="button" onClick={onPick} className="flex h-8 items-center gap-1.5 rounded-lg border border-dashed border-line-strong px-2.5 text-xs text-fg-3 transition hover:border-accent hover:text-accent">
        <ImagePlus className="size-3.5" />
        {t.attach.add}
      </button>
    </Tip>
  )
}

// 紧凑模式下的触发按钮：放在输入框右下角。
function AttachTrigger({ onPick }: { onPick: () => void }) {
  return (
    <span className="absolute top-1.5 right-1.5">
      <Tip content={t.attach.hint}>
        <Button type="button" size="icon-sm" variant="ghost" aria-label={t.attach.add} onClick={onPick}>
          <ImagePlus className="size-4" />
        </Button>
      </Tip>
    </span>
  )
}
