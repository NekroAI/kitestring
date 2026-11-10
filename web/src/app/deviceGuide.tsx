// 接入设备的指引弹窗（R-268）：新建设备后弹出，未连上过的设备也能从设备列表再打开。
// 只靠这一页就能把一台新设备从零接上：安装、登录、接入工具并起后台服务、核对。
import { useState, type ReactNode } from 'react'
import { CheckCircle2, Copy, ExternalLink, Loader2, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'
import { t } from '../i18n'
import { useDevices } from '../api/queries'
import { copyText, cx } from '../lib/util'
import { DOCS_URL, DSH_VERSION, LINGER_CMD, NOHUP_CMD, STATUS_CMD, defaultPlatform, dshCmd, dshSupported, installCmd, isLoopback, logHint, loginCmd, toolsCmd, type Platform, type Tool } from '../lib/deviceGuide'
import { Button, Segmented, Tip } from '../ui/base'
import { Modal } from '../ui/overlay'

// 命令块：多行、长令牌都完整显示（折行，不截断），一键复制整块。
export function CommandBlock({ cmd }: { cmd: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg bg-sunken py-2 pr-1.5 pl-3">
      {/* 每条命令一行；太长折行时续行缩进，和下一条命令区分开 */}
      <code className="min-w-0 flex-1 py-0.5 font-mono text-[12px] leading-relaxed text-fg [overflow-wrap:anywhere]">
        {cmd.split('\n').map((line, i) => (
          <span key={i} className="block pl-4 -indent-4 whitespace-pre-wrap">
            {line}
          </span>
        ))}
      </code>
      <Tip content={t.common.copy}>
        <Button size="icon-sm" variant="ghost" aria-label={t.common.copy} onClick={async () => (await copyText(cmd)) && toast.success(t.common.copied)}>
          <Copy className="size-3.5" />
        </Button>
      </Tip>
    </div>
  )
}

function Note({ children, warn }: { children: ReactNode; warn?: boolean }) {
  return (
    <p className={cx('text-[13px] leading-relaxed', warn ? 'flex gap-1.5 text-wait' : 'text-fg-3')}>
      {warn && <TriangleAlert className="mt-[3px] size-3.5 shrink-0" />}
      <span>{children}</span>
    </p>
  )
}

function Step({ n, title, children }: { n: number; title: ReactNode; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="mt-px flex size-5 shrink-0 items-center justify-center rounded-full bg-accent-soft text-[11px] font-semibold text-accent">{n}</span>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="text-sm font-medium text-fg">{title}</div>
        {children}
      </div>
    </li>
  )
}

const toolLabel: Record<Tool, string> = { claude: 'Claude Code', codex: 'Codex', dsh: 'DSH' }

export function DeviceGuide({ open, deviceId, name, token, onClose }: { open: boolean; deviceId: string; name: string; token: string | null; onClose: () => void }) {
  const G = t.settings.devices.g
  const [platform, setPlatform] = useState<Platform>(() => defaultPlatform(navigator.userAgent))
  const [tools, setTools] = useState<Tool[]>(['claude', 'codex'])
  const dev = useDevices().data?.find((d) => d.id === deviceId)
  const hub = window.location.origin
  const loop = isLoopback(window.location.hostname) ? window.location.hostname : ''
  const toggle = (x: Tool) => setTools((cur) => (cur.includes(x) ? cur.filter((y) => y !== x) : [...cur, x]))
  const dsh = dshSupported(platform) && tools.includes('dsh')
  let n = 0

  return (
    <Modal
      open={open}
      onClose={onClose}
      wide
      title={token ? `${t.settings.devices.created} · ${name}` : t.settings.devices.guideTitle(name)}
      description={token ? t.settings.devices.tokenOnce : t.settings.devices.guideDesc}
      footer={
        <>
          <a href={DOCS_URL} target="_blank" rel="noreferrer" className="mr-auto inline-flex items-center gap-1 text-[13px] text-fg-3 hover:text-fg-2">
            {G.docs}
            <ExternalLink className="size-3.5" />
          </a>
          <Button variant="primary" onClick={onClose}>
            {t.common.close}
          </Button>
        </>
      }
    >
      <div className="mb-4 flex flex-wrap items-center gap-x-5 gap-y-2.5">
        <div className="flex items-center gap-2">
          <span className="text-[13px] text-fg-3">{G.platform}</span>
          <Segmented
            size="sm"
            value={platform}
            onChange={setPlatform}
            options={[
              { value: 'mac', label: G.mac },
              { value: 'linux', label: G.linux },
              { value: 'windows', label: G.windows },
            ]}
          />
        </div>
        <div className="flex items-center gap-2">
          <span className="text-[13px] text-fg-3">{G.tools}</span>
          {(['claude', 'codex', 'dsh'] as Tool[]).filter((x) => x !== 'dsh' || dshSupported(platform)).map((x) => {
            const on = tools.includes(x)
            return (
              <button
                key={x}
                onClick={() => toggle(x)}
                aria-pressed={on}
                className={cx('rounded-full border px-2.5 py-1 text-[12px] font-medium transition-colors', on ? 'border-accent bg-accent-soft text-accent' : 'border-line text-fg-3 hover:border-line-strong')}
              >
                {toolLabel[x]}
              </button>
            )
          })}
        </div>
      </div>

      <ol className="space-y-5 text-sm text-fg-2">
        <Step n={++n} title={G.install}>
          <CommandBlock cmd={installCmd(platform)} />
          <Note>
            {platform === 'windows' ? G.installNoteWin : G.installNote}
            {platform === 'linux' && G.installNoCurl}
          </Note>
        </Step>

        <Step n={++n} title={G.login}>
          <CommandBlock cmd={loginCmd(hub, token)} />
          <Note>{G.loginNote}</Note>
          {loop && <Note warn>{G.loopback(loop)}</Note>}
          {!token && <Note warn>{G.noToken}</Note>}
        </Step>

        <Step n={++n} title={G.service}>
          <CommandBlock cmd={toolsCmd(tools)} />
          <Note>
            {platform === 'mac' ? G.serviceMac : platform === 'windows' ? G.serviceWin : G.serviceLinux}
            {tools.includes('claude') ? G.needClaude : !tools.includes('codex') && G.noTools}
          </Note>
          {platform === 'linux' && (
            <>
              <Note>{G.linger}</Note>
              <CommandBlock cmd={LINGER_CMD} />
              <Note>{G.noSystemd}</Note>
              <CommandBlock cmd={NOHUP_CMD} />
            </>
          )}
        </Step>

        {tools.includes('codex') && (
          <Step n={++n} title={G.codex}>
            <Note>{G.codexNote}</Note>
          </Step>
        )}

        {dsh && (
          <Step n={++n} title={G.dsh}>
            <Note>{G.dshNeed(DSH_VERSION)}</Note>
            <CommandBlock cmd={dshCmd()} />
            <Note>{G.dshAfter}</Note>
          </Step>
        )}

        <Step n={++n} title={G.verify}>
          <CommandBlock cmd={STATUS_CMD} />
          <Note>{G.verifyNote}</Note>
          {dev?.online ? (
            <div className="flex items-start gap-2 rounded-lg bg-run-soft px-3 py-2 text-[13px] text-run">
              <CheckCircle2 className="mt-[3px] size-3.5 shrink-0" />
              <span>
                {[G.connected, dev.hostname, dev.platform, dev.version].filter(Boolean).join(t.common.sep)}
                <span className="block text-fg-2">{G.afterConnect}</span>
              </span>
            </div>
          ) : (
            <div className="flex items-center gap-2 rounded-lg bg-sunken px-3 py-2 text-[13px] text-fg-3">
              <Loader2 className="size-3.5 shrink-0 animate-spin" />
              {G.waiting}
            </div>
          )}
          <Note>
            {G.noSock}
            <br />
            {G.notConnected(logHint(platform))}
          </Note>
        </Step>
      </ol>
    </Modal>
  )
}
