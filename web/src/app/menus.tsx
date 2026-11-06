import { Palette, Send, Share2, Archive, ArchiveRestore, Check, CircleDot, ClipboardCopy, ExternalLink, Flag, ListTodo, PanelRight, Terminal, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { useNavigate } from 'react-router'
import type { ReqFields, Requirement, Session } from '../api/client'
import { t } from '../i18n'
import { copyText } from '../lib/util'
import { resumeCommand, resumeNeedsLogin } from '../lib/resume'
import type { CtxItem } from '../ui/contextMenu'
import { PriorityDot } from '../ui/marks'
import { useConfirm } from '../ui/overlay'
import { useActions } from './actions'
import { useSessions } from '../api/queries'
import { assignItems, useAssign } from './assign'
import { useOpenReq, useOpenSession } from './nav'
import { allStatuses, priorities, toFields, useSetStatus } from './requirements'
import { ReqGlyph } from '../ui/refmark'
import { sessionTitle } from './SessionCard'
import { canHandoff, canStop, HandoffIcon, StopIcon, useRequestHandoff, useStopSession } from './compact'

const copy = async (text: string) => (await copyText(text)) && toast.success(t.common.copied)


// useSessionMenu：会话的右键 / 长按菜单。
export function useSessionMenu() {
  const open = useOpenSession()
  const openReq = useOpenReq()
  const { markDone } = useActions()
  const handoff = useRequestHandoff()
  const stop = useStopSession()
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
      ...(canHandoff(s) ? [{ label: t.menu.handoff, hint: t.menu.handoffHint, icon: <HandoffIcon />, onSelect: () => void handoff(s) }] : []),
      ...(canStop(s) ? [{ label: t.stop.action, hint: t.stop.hint, icon: <StopIcon />, onSelect: () => void stop(s, sessionTitle(s)) }] : []),
      { type: 'separator' },
      ...(s.tool !== 'dsh'
        ? [
            { label: t.menu.copyResume, hint: resumeCommand(s), icon: <Terminal />, onSelect: () => void copy(resumeCommand(s)) },
            ...(resumeNeedsLogin(s) ? [{ type: 'label' as const, label: t.menu.resumeLoginHint }] : []),
          ]
        : []),
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
  const { updateReq, deleteReq } = useActions()
  const confirm = useConfirm()
  const navigate = useNavigate()
  return (r: Requirement): CtxItem[] => [
    { type: 'label', label: `${r.code} ${r.title}` },
    { label: t.menu.openReq, icon: <PanelRight />, onSelect: () => openReq(r.code) },
    // 待验收：只给「去验收」，打开抽屉在底部判断（R-177：审批只在抽屉里做，不在菜单里就地通过 / 退回）
    ...(r.status === 'review' ? [{ label: t.desk.review, icon: <Check />, onSelect: () => openReq(r.code) }] : []),
    ...(r.status !== 'done' && r.status !== 'shelved'
      ? [{ type: 'sub' as const, label: t.assign.menu, icon: <Send />, items: assignItems(sessions.data?.sessions ?? [], r.project_id, (s) => void pick(r.code, s)) }]
      : []),
    r.kind === 'design'
      ? { label: t.design.unmake, icon: <Palette />, onSelect: () => void updateReq(r.code, { ...toFields(r), kind: 'feature' }) }
      : { label: t.design.make, hint: t.design.makeHint, icon: <Palette />, onSelect: () => void updateReq(r.code, { ...toFields(r), kind: 'design' }) },
    {
      type: 'sub',
      label: t.menu.status,
      icon: <CircleDot />,
      items: allStatuses.map((st) => ({ label: t.reqStatus[st], checked: st === r.status, icon: <ReqGlyph status={st} />, onSelect: () => void setStatus(r, st) })),
    },
    {
      type: 'sub',
      label: t.menu.priority,
      icon: <Flag />,
      items: priorities.map((p) => ({ label: t.priority[p], checked: p === r.priority, icon: <PriorityDot p={p} />, onSelect: () => void updateReq(r.code, toFields(r, { priority: p as ReqFields['priority'] })) })),
    },
    { type: 'separator' },
    // 分享（R-281）：新建分享并预先勾好这条需求和它下面的会话
    { label: t.share.menu, icon: <Share2 />, onSelect: () => navigate(`/shares/new?with=${encodeURIComponent(r.code)}`) },
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
