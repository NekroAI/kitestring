import { CornerDownLeft } from 'lucide-react'
import { toast } from 'sonner'
import type { Session } from '../api/client'
import { t } from '../i18n'
import { Button, Textarea } from '../ui/base'
import { AttachZone, useUploading, type FileRef } from '../ui/attach'
import { displayState } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { DraftLine, useDraft } from './draft'

// canMessage：会话面板上能不能直接给它发一句话——挂着 ks wait 待命、或一轮结束停着的会话。
export const canMessage = (s: Session) => {
  if (s.state === 'ended') return false
  const ds = displayState(s)
  return ds === 'parked' || ds === 'waiting_reply'
}

// MessageBox：在会话面板上直接给会话发一句话（送达方式同留言）：挂着 ks wait 时立刻唤醒它，否则在你下次给它发消息时送达。
// 只是留言，不算待办、不进「要你处理」（R-239：对话里的问句不再当成在等你）。
export function MessageBox({ s }: { s: Session }) {
  const { replySession } = useActions()
  const [busy, wrap] = useBusy()
  const uploading = useUploading()
  // 草稿（R-202）：reply:会话键
  const dr = useDraft<{ text: string; files: FileRef[] }>(`reply:${s.key}`, { text: '', files: [] })
  const { text, files } = dr.value
  const setText = (v: string) => dr.set({ text: v })
  const setFiles = (v: FileRef[]) => dr.set({ files: v })
  const dirty = dr.dirty
  const parked = displayState(s) === 'parked'
  const send = () =>
    wrap(async () => {
      if (await replySession(s.key, text, files.map((f) => f.id))) {
        toast.success(t.desk.replySent(parked))
        dr.drop()
      }
    })
  return (
    <div
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && dirty) {
          e.preventDefault()
          void send()
        }
      }}
    >
      <AttachZone value={files} onChange={setFiles} compact>
        <Textarea minRows={1} value={text} placeholder={t.desk.replyPlaceholder} onChange={(e) => setText(e.target.value)} className="pr-10" />
      </AttachZone>
      <DraftLine h={dr} />
      <div className="mt-2 flex items-center gap-2">
        <span className="min-w-0 flex-1 text-xs text-fg-3">{parked ? t.desk.replyParked : t.desk.replyIdle}</span>
        <Button variant="primary" disabled={!dirty || uploading} loading={busy} icon={<CornerDownLeft className="size-4" />} onClick={send}>
          {t.desk.replySend}
        </Button>
      </div>
    </div>
  )
}
