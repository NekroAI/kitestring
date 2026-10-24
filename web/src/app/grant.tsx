import { useState } from 'react'
import { DraftLine, useDraft } from './draft'
import { Undo2 } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision } from '../api/client'
import { t } from '../i18n'
import { absTime, cx } from '../lib/util'
import { Badge, Button, Textarea } from '../ui/base'
import { RoleTag } from '../ui/marks'
import { Linkify } from '../ui/refmark'
import { useActions } from './actions'

// 主助理代答留痕（R-163、R-190）：代答可推翻，推翻的更正送回提问方。开窗口的入口只剩「暂时离开」（R-210，app/brief.tsx）。

// OverrideBox：推翻参谋的代答，写下你的答复作为更正送回提问方。
// startOpen / onClose：离开期间小结里由行内按钮打开，取消或送出后收回（R-165）。
export function OverrideBox({ d, startOpen = false, onClose }: { d: Decision; startOpen?: boolean; onClose?: () => void }) {
  const { overrideDecision } = useActions()
  // 草稿（R-202）：override:D-8；有没写完的更正时直接展开
  const dr = useDraft<{ text: string }>(`override:${d.code}`, { text: '' })
  const text = dr.value.text
  const setText = (v: string) => dr.set({ text: v })
  const [open, setOpenRaw] = useState(startOpen || dr.dirty)
  const setOpen = (v: boolean) => {
    setOpenRaw(v)
    if (!v) onClose?.()
  }
  const [busy, setBusy] = useState(false)
  if (!open)
    return (
      <div>
        <Button size="sm" onClick={() => setOpen(true)}>
          <Undo2 className="size-3.5" />
          {t.grant.override}
        </Button>
      </div>
    )
  return (
    <div className="space-y-2">
      <div>
        <Textarea autoFocus minRows={2} value={text} placeholder={t.grant.overridePlaceholder} onChange={(e) => setText(e.target.value)} />
        <DraftLine h={dr} />
      </div>
      <div className="flex justify-end gap-2">
        <Button size="sm" onClick={() => setOpen(false)}>
          {t.common.cancel}
        </Button>
        <Button
          size="sm"
          variant="primary"
          disabled={!text.trim() || busy}
          onClick={async () => {
            setBusy(true)
            if (await overrideDecision(d.code, text.trim())) {
              toast.success(t.grant.overrideDone)
              setOpen(false)
              dr.drop()
            }
            setBusy(false)
          }}
        >
          {t.grant.overrideSend}
        </Button>
      </div>
    </div>
  )
}

// ProxyNote：决策上的代答留痕——Adv 代答、风险档、理由；推翻后标出并保留代答原文。
export function ProxyNote({ d, withOverride = true }: { d: Decision; withOverride?: boolean }) {
  const over = !!d.overridden_at
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5 text-[13px] text-fg-2">
        <RoleTag role="aide" />
        <span>{t.grant.proxied}</span>
        {d.proxy_risk && <Badge tone="neutral">{t.grant.risk[d.proxy_risk]}</Badge>}
        {d.proxy_at && <span className="text-fg-3 tabular">{absTime(d.proxy_at)}</span>}
        {over && <Badge tone="accent">{t.grant.overridden}</Badge>}
      </div>
      <div className={cx('rounded-lg px-3 py-2 text-sm leading-relaxed text-fg', over ? 'bg-sunken/70 text-fg-3' : 'bg-sunken/70')}>
        <span className="mr-1.5 text-fg-3">{t.grant.answer}</span>
        <Linkify text={d.proxy_answer ?? ''} />
      </div>
      <p className="text-[13px] leading-relaxed text-fg-2">
        <span className="mr-1.5 text-fg-3">{t.grant.reason}</span>
        <Linkify text={d.proxy_reason ?? ''} />
      </p>
      {over && (
        <div className="rounded-lg bg-accent-soft/60 px-3 py-2 text-sm leading-relaxed text-fg">
          <span className="mr-1.5 text-fg-3">{t.grant.yours}</span>
          <Linkify text={d.answer} />
        </div>
      )}
      {withOverride && !over && d.status !== 'closed' && <OverrideBox d={d} />}
    </div>
  )
}
