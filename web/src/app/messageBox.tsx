import { CornerDownLeft, MessageCircleQuestionMark } from 'lucide-react'
import { toast } from 'sonner'
import type { Session } from '../api/client'
import { t } from '../i18n'
import { Button, Textarea } from '../ui/base'
import { AttachZone, useUploading, type FileRef } from '../ui/attach'
import { Md } from '../ui/markdown'
import { displayState } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { useOpenSession } from './nav'
import { DraftLine, useDraft } from './draft'

// 「在等你回复」（R-117）：会话最后的回复在问你，却没有提正式决策。Hub 判断后把最后一段存为 reply_ask；
// 只在它停下来等你（一轮结束 / 挂着 ks wait）时算数，你回复或在终端里发了消息后清空。
export const awaitingReply = (s: Session | undefined): s is Session => {
  if (!s || !s.reply_ask || s.state === 'ended') return false
  const ds = displayState(s)
  return ds === 'waiting_reply' || ds === 'parked'
}

// ReplyBox：大卡右栏，显示它最后回复的最后一段，就地回复（送达方式同留言）。
export function ReplyBox({ s }: { s: Session }) {
  const { replySession } = useActions()
  const open = useOpenSession()
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
    <div className="rounded-xl border border-reply/25 bg-surface p-3">
      <div className="mb-2 flex items-center gap-2 text-xs">
        <MessageCircleQuestionMark className="size-4 shrink-0 text-reply" />
        <span className="shrink-0 font-medium whitespace-nowrap text-reply">{t.desk.awaiting}</span>
        <span className="min-w-0 truncate text-fg-3 max-sm:hidden">{t.desk.awaitingHint}</span>
        <button onClick={() => open(s.key)} className="ml-auto shrink-0 text-accent hover:opacity-80">
          {t.desk.fullReply}
        </button>
      </div>
      <div className="mb-3 max-h-48 overflow-y-auto rounded-lg bg-reply-soft/60 px-3 py-2 text-sm">
        <Md text={s.reply_ask ?? ''} />
      </div>
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
    </div>
  )
}
