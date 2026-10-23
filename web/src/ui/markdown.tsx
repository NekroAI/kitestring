import Markdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { cx } from '../lib/util'
import { refPattern, RefText } from './refmark'

// rehypeRefs：把正文里的 R-/P-/D- 编号换成 <ref-chip> 节点，渲染成印记（R-96）。代码、链接里的不动。
type HNode = { type: string; tagName?: string; value?: string; properties?: Record<string, unknown>; children?: HNode[] }
function rehypeRefs() {
  const walk = (n: HNode) => {
    if (!n.children || (n.type === 'element' && (n.tagName === 'code' || n.tagName === 'pre' || n.tagName === 'a'))) return
    const out: HNode[] = []
    for (const c of n.children) {
      if (c.type !== 'text' || !c.value) {
        walk(c)
        out.push(c)
        continue
      }
      c.value.split(refPattern).forEach((p, i) => {
        if (i % 2) out.push({ type: 'element', tagName: 'ref-chip', properties: { code: p }, children: [{ type: 'text', value: p }] })
        else if (p) out.push({ type: 'text', value: p })
      })
    }
    n.children = out
  }
  return (tree: HNode) => walk(tree)
}

// Md：显示 Agent 写的 Markdown（R-109）。用 react-markdown 渲染成 React 元素，不用 innerHTML；
// 原始 HTML 一律丢弃（skipHtml），链接走默认的安全地址过滤，图片不加载外链、只显示成链接。
export function Md({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cx('md', className)}>
      <Markdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeRefs]}
        skipHtml
        components={{
          ...({ 'ref-chip': ({ code }: { code: string }) => <RefText code={code} /> } as Components),
          a: ({ node: _n, ...p }) => <a {...p} target="_blank" rel="noopener noreferrer" />,
          img: ({ src, alt }) =>
            typeof src === 'string' && src ? (
              <a href={src} target="_blank" rel="noopener noreferrer">
                {alt || src}
              </a>
            ) : (
              <span>{alt}</span>
            ),
          table: ({ node: _n, ...p }) => (
            <div className="md-table">
              <table {...p} />
            </div>
          ),
        }}
      >
        {text}
      </Markdown>
    </div>
  )
}
