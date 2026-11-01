// modelShort：模型 id 转短名（claude-opus-5-5 → Opus 5.5）；认不出的原样返回 id。
const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

export function modelShort(id: string): string {
  const m = id.trim()
  if (!m) return ''
  const s = m.toLowerCase().replace(/\[[^\]]*\]$/, '') // 去掉 [1m] 之类的后缀
  // claude-opus-5-5 / claude-haiku-4-5-20251001
  let r = /^claude-(opus|sonnet|haiku)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?$/.exec(s)
  if (r) return `${cap(r[1])} ${r[2]}${r[3] ? '.' + r[3] : ''}`
  // 旧写法 claude-3-5-sonnet-20241022
  r = /^claude-(\d+)(?:-(\d{1,2}))?-(opus|sonnet|haiku)(?:-\d{8})?$/.exec(s)
  if (r) return `${cap(r[3])} ${r[1]}${r[2] ? '.' + r[2] : ''}`
  // gpt-6.1-sol / gpt-6-astra
  r = /^gpt-(\d+(?:\.\d+)?)(?:-([a-z][a-z0-9]*))?$/.exec(s)
  if (r) return `GPT-${r[1]}${r[2] ? ' ' + cap(r[2]) : ''}`
  return m
}

// 子任务用过的某个模型和它的用量（token 合计）
export type ModelUse = { id: string; short: string; tokens: number }
