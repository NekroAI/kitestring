import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { cx } from '../lib/util'

// Md：显示 Agent 写的 Markdown（R-109）。用 react-markdown 渲染成 React 元素，不用 innerHTML；
// 原始 HTML 一律丢弃（skipHtml），链接走默认的安全地址过滤，图片不加载外链、只显示成链接。
export function Md({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cx('md', className)}>
      <Markdown
        remarkPlugins={[remarkGfm]}
        skipHtml
        components={{
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
