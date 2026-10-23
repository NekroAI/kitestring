import { useEffect, useState } from 'react'
import { t } from '../i18n'

// mermaid 体积大（约 1 MB），只在页面上真有流程图时才加载（R-97）。
type Mermaid = (typeof import('mermaid'))['default']
let loading: Promise<Mermaid> | null = null
const load = () => (loading ??= import('mermaid').then((m) => m.default))

// useDark：跟着 html 上的 .dark 类走（主题切换或跟随系统时都会变）。
function useDark() {
  const root = document.documentElement
  const [dark, setDark] = useState(() => root.classList.contains('dark'))
  useEffect(() => {
    const o = new MutationObserver(() => setDark(root.classList.contains('dark')))
    o.observe(root, { attributes: true, attributeFilter: ['class'] })
    return () => o.disconnect()
  }, [root])
  return dark
}

// 颜色取自当前主题的设计令牌，图和页面是同一套色。
function themeVars() {
  const css = getComputedStyle(document.documentElement)
  const v = (n: string) => css.getPropertyValue(n).trim()
  return {
    fontFamily: css.getPropertyValue('font-family'),
    fontSize: '13px',
    background: v('--surface'),
    mainBkg: v('--surface-2'),
    primaryColor: v('--surface-2'),
    primaryTextColor: v('--fg'),
    primaryBorderColor: v('--line-strong'),
    nodeBorder: v('--line-strong'),
    secondaryColor: v('--accent-soft'),
    tertiaryColor: v('--run-soft'),
    textColor: v('--fg'),
    lineColor: v('--fg-3'),
    edgeLabelBackground: v('--surface'),
    clusterBkg: v('--bg-sunken'),
    clusterBorder: v('--line'),
    titleColor: v('--fg'),
    noteBkgColor: v('--wait-soft'),
    noteTextColor: v('--fg'),
    noteBorderColor: v('--line-strong'),
    actorBkg: v('--surface-2'),
    actorBorder: v('--line-strong'),
    actorTextColor: v('--fg'),
    signalColor: v('--fg-2'),
    signalTextColor: v('--fg'),
    labelBoxBkgColor: v('--surface-2'),
    labelTextColor: v('--fg'),
  }
}

let seq = 0

// MermaidView：Agent 写的 mermaid 源码。securityLevel strict（不执行脚本、不允许点击回调，输出经 DOMPurify 清洗）；
// 画不出来时退回显示原文。
export function MermaidView({ source }: { source: string }) {
  const dark = useDark()
  const [svg, setSvg] = useState<{ html: string; w: number } | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let alive = true
    setFailed(false)
    load()
      .then(async (m) => {
        m.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'base', darkMode: dark, themeVariables: themeVars(), flowchart: { htmlLabels: false } })
        await m.parse(source) // 先校验：写错的源码不会在页面上留下 mermaid 的报错图
        const out = await m.render(`ks-flow-${++seq}`, source)
        const w = Number(/viewBox="[\d.-]+ [\d.-]+ ([\d.]+)/.exec(out.svg)?.[1] ?? 0)
        if (alive) setSvg({ html: out.svg, w })
      })
      .catch(() => alive && setFailed(true))
    return () => {
      alive = false
    }
  }, [source, dark])

  if (failed)
    return (
      <div className="space-y-1.5">
        <p className="text-xs text-fg-3">{t.blocks.flowFailed}</p>
        <pre className="overflow-x-auto rounded-lg bg-sunken p-3 font-mono text-xs leading-relaxed text-fg-2">{source}</pre>
      </div>
    )
  if (!svg) return <div className="flex h-24 items-center justify-center text-xs text-fg-3">{t.blocks.flowLoading}</div>
  // svg 由 mermaid 在 strict 模式下生成并清洗过。窄屏上不把图缩到看不清：最窄 480px，放不下就横向滚动。
  return (
    <div className="overflow-x-auto">
      <div className="[&_svg]:mx-auto [&_svg]:block [&_svg]:h-auto [&_svg]:w-full" style={{ minWidth: Math.min(svg.w || 480, 480) }} dangerouslySetInnerHTML={{ __html: svg.html }} />
    </div>
  )
}
