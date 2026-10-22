import { Send, Archive, ArchiveRestore, Check, CircleDot, ClipboardCopy, ExternalLink, Flag, ListTodo, PanelRight, Terminal, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import type { ReqFields, Requirement, Session } from '../api/client'
import { t } from '../i18n'
import { copyText } from '../lib/util'
import type { CtxItem } from '../ui/contextMenu'
import { PriorityDot } from '../ui/marks'
import { useConfirm } from '../ui/overlay'
import { useActions } from './actions'
import { useSessions } from '../api/queries'
import { assignItems, useAssign } from './assign'
import { useOpenReq, useOpenSession } from './nav'
import { allStatuses, priorities, statusCls, toFields, useSetStatus } from './requirements'
import { sessionTitle } from './SessionCard'

const copy = async (text: string) => (await copyText(text)) && toast.success(t.common.copied)

// resumeCommand：在终端里接着这个对话的命令。
export const resumeCommand = (s: Session) => (s.tool === 'codex' ? `codex resume ${s.session_id}` : `claude --resume ${s.session_id}`)

// useSessionMenu：会话的右键 / 长按菜单。
export function useSessionMenu() {
  const open = useOpenSession()
  const openReq = useOpenReq()
  const { markDone } = useActions()
  return (s: Session): CtxItem[] => {
    const reqs = s.requirements.filter((r) => r.status !== 'done' && r.status !== 'shelved')
    const canDone = s.state !== 'ended'
    const busy = s.state === 'running' || s.state === 'background'
    return [
      { type: 'label', label: sessionTitle(s) },
      { label: t.menu.openSession, icon: <PanelRight />, onSelect: () => open(s.key) },
      ...(canDone
        ? [
            s.done_at
              ? { label: t.menu.undoDone, icon: <ArchiveRestore />, onSelect: () => void markDone(s.key, false) }
              : {
                  label: busy ? t.menu.markStopped : t.menu.markDone,
                  hint: busy ? t.menu.markStoppedHint : t.menu.markDoneHint,
                  icon: <Archive />,
                  onSelect: async () => (await markDone(s.key, true)) && toast.success(t.menu.markedDone),
                },
          ]
        : []),
      ...(reqs.length
        ? [{ type: 'sub' as const, label: t.menu.reqs(reqs.length), icon: <ListTodo />, items: reqs.map((r) => ({ label: r.title, hint: r.code, onSelect: () => openReq(r.code) })) }]
        : []),
      { type: 'separator' },
      { label: t.menu.copyResume, hint: resumeCommand(s), icon: <Terminal />, onSelect: () => void copy(resumeCommand(s)) },
      { label: t.menu.copySessionId, icon: <ClipboardCopy />, onSelect: () => void copy(s.session_id) },
    ]
  }
}

// useReqMenu：需求的右键 / 长按菜单。
export function useReqMenu() {
  const openReq = useOpenReq()
  const sessions = useSessions()
  const pick = useAssign()
  const setStatus = useSetStatus()
  const { review, updateReq, deleteReq } = useActions()
  const confirm = useConfirm()
  return (r: Requirement): CtxItem[] => [
    { type: 'label', label: `${r.code} ${r.title}` },
    { label: t.menu.openReq, icon: <PanelRight />, onSelect: () => openReq(r.code) },
    ...(r.status === 'review' ? [{ label: t.review.pass, icon: <Check />, onSelect: async () => (await review([{ ref: r.code, verdict: 'accept' }])) && toast.success(t.review.passed(r.code)) }] : []),
    ...(r.status !== 'done' && r.status !== 'shelved'
      ? [{ type: 'sub' as const, label: t.assign.menu, icon: <Send />, items: assignItems(sessions.data?.sessions ?? [], r.project_id, (s) => void pick(r.code, s)) }]
      : []),
    {
      type: 'sub',
      label: t.menu.status,
      icon: <CircleDot />,
      items: allStatuses.map((st) => ({ label: t.reqStatus[st], checked: st === r.status, icon: <span className={`size-2 rounded-full ${statusCls[st].dot}`} />, onSelect: () => void setStatus(r, st) })),
    },
    {
      type: 'sub',
      label: t.menu.priority,
      icon: <Flag />,
      items: priorities.map((p) => ({ label: t.priority[p], checked: p === r.priority, icon: <PriorityDot p={p} />, onSelect: () => void updateReq(r.code, toFields(r, { priority: p as ReqFields['priority'] })) })),
    },
    { type: 'separator' },
    { label: t.menu.copyCode, icon: <ClipboardCopy />, onSelect: () => void copy(r.code) },
    { label: t.menu.copyLink, icon: <ExternalLink />, onSelect: () => void copy(`${location.origin}/requirements?r=${encodeURIComponent(r.code)}`) },
    { type: 'separator' },
    {
      label: t.common.delete,
      icon: <Trash2 />,
      danger: true,
      onSelect: async () => {
        if (await confirm({ title: t.common.delete, description: t.reqs.deleteConfirm, confirmText: t.common.delete, danger: true })) {
          if (await deleteReq(r.code)) toast(t.reqs.deleted)
        }
      },
    },
  ]
}
