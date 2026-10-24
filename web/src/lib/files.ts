// 附件类型（R-221）：哪些在页面里直接显示、哪些能预览文字、文件块用什么图标。纯函数，界面在 ui/attach.tsx。
// 只有这几种图片 / 录屏直接显示（与 Hub 的 protocol.InlineMime 一致）；其它文件（包括 SVG、HTML）显示为文件块，点了下载。

const IMAGES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])
const VIDEOS = new Set(['video/mp4', 'video/webm', 'video/quicktime'])

const base = (mime: string) => (mime || '').split(';')[0].trim().toLowerCase()
export const isImage = (mime: string) => IMAGES.has(base(mime))
export const isVideo = (mime: string) => VIDEOS.has(base(mime))
// isMedia：图片或录屏，显示缩略图
export const isMedia = (mime: string) => isImage(mime) || isVideo(mime)

export const ext = (name: string) => {
  const i = (name || '').lastIndexOf('.')
  return i > 0 ? name.slice(i + 1).toLowerCase() : ''
}

const TEXT_EXT = new Set(['txt', 'log', 'md', 'markdown', 'csv', 'tsv', 'json', 'jsonl', 'ndjson', 'yaml', 'yml', 'xml', 'toml', 'ini', 'conf', 'env', 'diff', 'patch', 'sh', 'py', 'js', 'ts', 'tsx', 'jsx', 'go', 'rs', 'java', 'kt', 'swift', 'c', 'h', 'cpp', 'css', 'html', 'htm', 'svg', 'sql'])
const TEXT_MIME = new Set(['application/json', 'application/x-ndjson', 'application/yaml', 'application/xml', 'application/toml', 'image/svg+xml'])

// isText：能在查看器里直接看内容的文本文件（只当纯文字显示，不执行）
export function isText(f: { name: string; mime: string }) {
  if (isMedia(f.mime)) return false
  const m = base(f.mime)
  return m.startsWith('text/') || TEXT_MIME.has(m) || m.endsWith('+json') || TEXT_EXT.has(ext(f.name))
}

// viewable：能进统一看图（R-195）的附件：图片、录屏、文本
export const viewable = (f: { name: string; mime: string }) => isMedia(f.mime) || isText(f)

export type FileKind = 'json' | 'table' | 'code' | 'text' | 'archive' | 'pdf' | 'image' | 'audio' | 'video' | 'file'

// kindOf：文件块用哪个图标
export function kindOf(f: { name: string; mime: string }): FileKind {
  const m = base(f.mime)
  const e = ext(f.name)
  if (m === 'application/json' || m === 'application/x-ndjson' || ['json', 'jsonl', 'ndjson'].includes(e)) return 'json'
  if (['csv', 'tsv', 'xls', 'xlsx', 'numbers'].includes(e) || m === 'text/csv') return 'table'
  if (['zip', 'gz', 'tgz', 'tar', 'rar', '7z', 'bz2', 'xz'].includes(e) || /zip|gzip|x-tar|compressed/.test(m)) return 'archive'
  if (m === 'application/pdf' || e === 'pdf') return 'pdf'
  if (m.startsWith('audio/')) return 'audio'
  if (m.startsWith('video/')) return 'video'
  if (m.startsWith('image/')) return 'image'
  if (['sh', 'py', 'js', 'ts', 'tsx', 'jsx', 'go', 'rs', 'java', 'kt', 'swift', 'c', 'h', 'cpp', 'css', 'html', 'htm', 'xml', 'yaml', 'yml', 'toml', 'sql', 'diff', 'patch'].includes(e)) return 'code'
  if (isText(f)) return 'text'
  return 'file'
}

// typeLabel：文件块上的类型小字，如 JSON、ZIP；没有扩展名时按类型
export function typeLabel(f: { name: string; mime: string }) {
  const e = ext(f.name)
  if (e && e.length <= 8) return e.toUpperCase()
  const m = base(f.mime)
  if (!m || m === 'application/octet-stream') return ''
  return (m.split('/')[1] || m).replace(/^x-/, '').toUpperCase()
}

export function fmtSize(n: number) {
  if (!(n > 0)) return '0 B'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`
  return `${(n / 1024 / 1024).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)} MB`
}

// limitFor：浏览器里按类型选上传上限（MB），没拿到设置时交给 Hub 检查
export function limitFor(type: string, limits?: { upload_image_mb?: number; upload_video_mb?: number; upload_file_mb?: number } | null) {
  if (!limits) return { kind: isVideo(type) ? 'video' : isImage(type) ? 'image' : 'file', mb: 0 }
  if (isVideo(type)) return { kind: 'video', mb: limits.upload_video_mb ?? 0 }
  if (isImage(type)) return { kind: 'image', mb: limits.upload_image_mb ?? 0 }
  return { kind: 'file', mb: limits.upload_file_mb ?? 0 }
}

// previewText：文本预览只看前若干行；JSON 整段能解析时排版后再截
export const PREVIEW_BYTES = 256 * 1024
export const PREVIEW_LINES = 2000
export function previewText(raw: string, f: { name: string; mime: string }, truncatedBytes: boolean) {
  let text = raw
  if (!truncatedBytes && kindOf(f) === 'json' && ext(f.name) !== 'jsonl' && ext(f.name) !== 'ndjson') {
    try {
      text = JSON.stringify(JSON.parse(raw), null, 2)
    } catch {
      /* 不是完整 JSON：原样显示 */
    }
  }
  const lines = text.split('\n')
  const more = truncatedBytes || lines.length > PREVIEW_LINES
  return { text: lines.slice(0, PREVIEW_LINES).join('\n'), lines: Math.min(lines.length, PREVIEW_LINES), more }
}
