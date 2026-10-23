// 命令面板的搜索（R-127）：纯函数，不依赖 React / DOM，单测见 search.test.ts。
// 规则：输入 R-12 / r12 / 12 精确命中编号的排最前；其余按空格分词，每个词都要命中（标题、编号、项目、附加字段、关键词）；
// 标题开头命中 > 标题包含 > 其它字段。各类分组，组顺序按组内最高分，同分按 kindOrder。

export type DocKind = 'cmd' | 'req' | 'dec' | 'plan' | 'session' | 'project'
export const kindOrder: DocKind[] = ['cmd', 'req', 'dec', 'plan', 'session', 'project']

export type SearchDoc = {
  id: string
  kind: DocKind
  title: string
  code?: string // R-12 / D-3 / P-2
  fields?: string[] // 项目名、目录、分支等次要字段
  keywords?: string[] // 命令的别名（例如「通过」也能用 accept 找到）
  boost?: number // 轮到你、进行中等小幅加分（0～50）
}

export type Query = { raw: string; tokens: string[]; code?: { prefix: string; num: string }; digits?: string }

const norm = (s: string) => s.toLowerCase().normalize('NFKC')

export function parseQuery(input: string): Query {
  const raw = norm(input.trim())
  const tokens = raw.split(/\s+/).filter(Boolean)
  const m = /^([rpd])-?(\d+)$/.exec(raw)
  if (m) return { raw, tokens, code: { prefix: m[1].toUpperCase(), num: m[2] } }
  if (/^\d+$/.test(raw)) return { raw, tokens, digits: raw }
  return { raw, tokens }
}

// scoreDoc：0 表示不命中。
export function scoreDoc(doc: SearchDoc, q: Query): number {
  if (!q.raw) return 1 + (doc.boost ?? 0)
  const code = doc.code ?? ''
  const boost = doc.boost ?? 0
  if (code) {
    const [p, n] = code.split('-')
    if (q.code && p === q.code.prefix) {
      if (n === q.code.num) return 1000 + boost
      if (n?.startsWith(q.code.num)) return 500 + boost - n.length
    }
    if (q.digits) {
      if (n === q.digits) return 900 + boost
      if (n?.startsWith(q.digits)) return 400 + boost - n.length
    }
  }
  const title = norm(doc.title)
  const rest = [code, ...(doc.fields ?? []), ...(doc.keywords ?? [])].map(norm)
  let score = 0
  for (const tok of q.tokens) {
    if (title.startsWith(tok)) score += 300
    else if (title.includes(tok)) score += 200
    else if (rest.some((f) => f.includes(tok))) score += 100
    else return 0
  }
  // 整句连续命中标题再加分（「命令 面板」与「命令面板」都能找到，连续的排前）
  if (q.tokens.length > 1 && title.includes(q.raw)) score += 100
  return score / q.tokens.length + boost
}

export type Group = { kind: DocKind; items: SearchDoc[]; best: number }

// search：按类分组并排序；limit 为每组最多几条（0 = 不限）。
export function search(docs: SearchDoc[], input: string, limit = 6): Group[] {
  const q = parseQuery(input)
  const by = new Map<DocKind, { d: SearchDoc; s: number }[]>()
  for (const d of docs) {
    const s = scoreDoc(d, q)
    if (s <= 0) continue
    const list = by.get(d.kind) ?? []
    list.push({ d, s })
    by.set(d.kind, list)
  }
  const groups: Group[] = []
  for (const [kind, list] of by) {
    list.sort((a, b) => b.s - a.s)
    const items = (limit ? list.slice(0, limit) : list).map((x) => x.d)
    groups.push({ kind, items, best: list[0].s })
  }
  return groups.sort((a, b) => b.best - a.best || kindOrder.indexOf(a.kind) - kindOrder.indexOf(b.kind))
}

// flatten：键盘上下移动用的一维顺序。
export const flatten = (groups: Group[]) => groups.flatMap((g) => g.items)
