import type { Activity } from '../api/client'
import { zh as t } from '../i18n/zh.ts'
import { roleName } from '../i18n/roles.ts'
import { approvedOf } from './waiting.ts'

// 最近动态（R-296）的句子：主语 + 动词 + 对象（+ ：内容），copywriting 3.7。数据来自后端 /api/activity。

// whoOf：主语——你 / 纸鸢 / 会话的身份名，没记身份时写会话名「x」，都没有写「会话」。
export function whoOf(a: Pick<Activity, 'actor' | 'role' | 'session_title'>): string {
  if (a.actor === 'user') return t.who.user
  if (a.actor === 'system') return t.who.system
  return (a.role && roleName(a.role)) || (a.session_title ? `「${a.session_title}」` : t.who.session)
}

// targetOf：指派给谁——会话名「x」，没有标题时写身份名，都没有写「会话」。
const targetOf = (a: Activity) => (a.target_title ? `「${a.target_title}」` : (a.target_role && roleName(a.target_role)) || t.who.session)

// blockedWhy：受阻在等什么；原因里已写了编号就不重复。
function blockedWhy(a: Activity) {
  const text = a.text ?? ''
  const parts = [a.ref && !text.includes(a.ref) ? t.projects.evWaiting(a.ref) : '', text].filter(Boolean)
  return parts.join('，')
}

// answerText：决策有了结果——你回答 / 批准 / 拒绝（读结论字段，R-239）、主助理代答、到期视为拒绝、申请作废。
function answerText(a: Activity) {
  switch (a.verdict) {
    case 'timeout':
      return t.projects.evTimeout(a.code)
    case 'voided':
      return t.projects.evVoided(a.code)
    case 'proxy':
      return t.projects.evProxy(a.code)
    case 'answer':
      return t.projects.evAnswer(a.code, t.verdict.pass('answer'))
    default:
      return t.projects.evAnswer(a.code, approvedOf({ approval: true, verdict: a.verdict }) === false ? t.verdict.back('approval') : t.verdict.pass('approval'))
  }
}

// lineOf：一条动态的句子和圆点颜色。
export function lineOf(a: Activity): { text: string; dot: string } {
  const w = whoOf(a)
  const c = a.code
  switch (a.kind) {
    case 'evidence':
      return { text: t.projects.evEvidence(w, c), dot: 'bg-run' }
    case 'submit':
      return { text: a.evidence ? t.projects.evSubmitWith(w, c) : t.projects.evSubmit(w, c), dot: 'bg-run' }
    case 'review':
      return { text: t.projects.evReview(w, c, a.text ?? ''), dot: 'bg-reply' }
    case 'blocked':
      return { text: t.projects.evBlocked(w, c, blockedWhy(a)), dot: 'bg-danger' }
    case 'unblock':
      return { text: t.projects.evUnblock(c, a.ref ?? ''), dot: 'bg-accent' }
    case 'shelved':
      return { text: t.projects.evShelved(w, c, a.text ?? ''), dot: 'bg-line-strong' }
    case 'done':
      return { text: t.projects.evDone(c), dot: 'bg-run' }
    case 'raise':
      return { text: a.approval ? t.projects.evAskApproval(w, c) : t.projects.evRaise(w, c), dot: 'bg-wait' }
    case 'answer':
      return { text: answerText(a), dot: 'bg-wait' }
    case 'assign':
      return { text: t.projects.evAssign(w, c, targetOf(a)), dot: 'bg-accent' }
    case 'review_task':
      return { text: t.projects.evReviewTask(w, c), dot: 'bg-reply' }
    case 'release':
      return { text: t.projects.evRelease(w, c), dot: 'bg-accent' }
    case 'note':
      return { text: t.projects.evNote(c, a.text ?? ''), dot: 'bg-accent' }
    case 'comment':
      return { text: t.projects.evComment(w, c, a.text ?? ''), dot: 'bg-line-strong' }
    case 'revise':
      return { text: t.projects.evRevise(c, a.text ?? ''), dot: 'bg-reply' }
    case 'stage':
      return {
        text: a.stage_with ? t.projects.evStageWith(c, a.stage_seq ?? 0, a.stage_title ?? '', a.stage_with) : t.projects.evStage(c, a.stage_seq ?? 0, a.stage_title ?? ''),
        dot: 'bg-run',
      }
  }
  return { text: c, dot: 'bg-line-strong' }
}
