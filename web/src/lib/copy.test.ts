// 界面文案自动检查（R-274）：按 docs/copywriting.md 检查 zh.ts 的全部文案和 fail.ts 的失败提示。
// 规则、状态集合、按钮清单和豁免都在 docs/copywriting-rules.json（后端 internal/copyrules 读同一份）。
// 豁免只能写在 JSON 的 allow 里，必须写理由；一条豁免拦不到任何问题时报错，提醒删掉。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { zh } from '../i18n/zh.ts'
import { setRoleLang } from '../i18n/roles.ts'
import { failText, reasonOf, type Failure } from './fail.ts'

type Allow = { key: string; rules?: string[]; except?: string[]; reason: string }
type Rules = {
  forbidden: { word: string; use: string }[]
  statusSets: Record<string, string[]>
  statusKeys: Record<string, string[]>
  buttons: string[]
  buttonMax: number
  allow: Allow[]
}
const rules: Rules = JSON.parse(readFileSync(new URL('../../../docs/copywriting-rules.json', import.meta.url), 'utf8'))

// ---------- 收集文案：路径 → 字符串 ----------

// 函数按参数个数用占位值调用：先都传汉字（插值多是名字、时长，汉字占位不会误报空格），不行再试数字、数组、场景名
// 另用数字优先再取一遍（numTexts），专门查「5m 前」这类缩写：数字参数用汉字占位时看不出来
const FILL: unknown[] = ['甲', 3, ['R-1', 'R-2'], 'accept', true]
const FILL_NUM: unknown[] = [3, '甲', ['R-1', 'R-2'], 'accept', true]
function call(fn: (...a: unknown[]) => unknown, fill: unknown[]): unknown {
  for (const v of fill) {
    try {
      return fn(...Array.from({ length: Math.max(fn.length, 1) }, () => v))
    } catch {
      // 换一种占位值
    }
  }
  return undefined
}

function collect(v: unknown, path: string, out: Map<string, string>, fill = FILL) {
  if (typeof v === 'string') {
    // 中文 / 英文身份名下各取一次，同一路径两个结果不同时都查
    const prev = out.get(path)
    out.set(path, prev && prev !== v ? `${prev}\n${v}` : v)
  } else if (typeof v === 'function') collect(call(v as (...a: unknown[]) => unknown, fill), path, out, fill)
  else if (Array.isArray(v)) v.forEach((x, i) => collect(x, `${path}.${i}`, out, fill))
  else if (v && typeof v === 'object') for (const k of Object.keys(v)) collect((v as Record<string, unknown>)[k], path ? `${path}.${k}` : k, out, fill)
}

const texts = new Map<string, string>()
for (const lang of ['zh', 'en'] as const) {
  setRoleLang(lang)
  collect(zh, '', texts)
}
const numTexts = new Map<string, string>()
collect(zh, '', numTexts, FILL_NUM)
setRoleLang('auto')
const failures: Record<string, Failure> = {
  network: { kind: 'network' },
  timeout: { kind: 'timeout' },
  http401: { kind: 'http', status: 401 },
  http403: { kind: 'http', status: 403 },
  http404: { kind: 'http', status: 404 },
  http409: { kind: 'http', status: 409 },
  http413: { kind: 'http', status: 413 },
  http422: { kind: 'http', status: 422 },
  http500: { kind: 'http', status: 500 },
  http418: { kind: 'http', status: 418 },
  other: { kind: 'other' },
}
for (const [k, f] of Object.entries(failures)) {
  texts.set(`fail.reason.${k}`, reasonOf(f))
  texts.set(`fail.text.${k}`, failText(f, '保存设置'))
}

// ---------- 规则 ----------

const glob = (pat: string, path: string) => (pat.endsWith('*') ? path.startsWith(pat.slice(0, -1)) : path === pat)
const isButton = (path: string) => rules.buttons.some((p) => glob(p, path))
const strip = (s: string) => s.replace(/甲|R-1、R-2|R-1 等 \d+ 条|\d+/g, '').replace(/…$/, '')

type Rule = { id: string; check: (s: string, path: string) => string | null }
const RULES: Rule[] = [
  {
    id: 'forbidden',
    check: (s) => {
      // 标准词本身含禁用词时（「指派给」含「派给」）先去掉标准词再查
      const hit = rules.forbidden.filter((f) => (f.use.includes(f.word) ? s.split(f.use).join('') : s).includes(f.word))
      return hit.length ? hit.map((f) => `「${f.word}」→「${f.use}」`).join('，') : null
    },
  },
  { id: 'it', check: (s) => (/(?<!其)它/.test(s) ? '用了「它」，写会话名或省略' : null) },
  {
    id: 'dot',
    check: (s) => {
      for (const m of s.matchAll(/·/g)) {
        const i = m.index ?? 0
        if (s[i - 1] !== ' ' || s[i + 1] !== ' ') return '「·」两侧要各留一个半角空格'
        if (/[，。；]/.test(s.slice(Math.max(0, i - 8), i)) || /[，。；]/.test(s.slice(i + 1, i + 9))) return '「·」连接了句子，改用逗号或句号'
      }
      return null
    },
  },
  { id: 'quote', check: (s) => (/[“”"]/.test(s) ? '引号用「」' : null) },
  { id: 'ellipsis', check: (s) => (/……|\.\.\./.test(s) ? '省略号只用单个「…」，列举用「等」' : null) },
  { id: 'exclaim', check: (s) => (/[！!]/.test(s) ? '不用感叹号' : null) },
  {
    id: 'spacing',
    check: (s) => {
      const m = s.match(/[一-鿿][A-Za-z0-9]|[A-Za-z0-9][一-鿿]/)
      return m ? `中英文 / 数字之间要留空格：「${m[0]}」` : null
    },
  },
  { id: 'timeAbbrev', check: (s, path) => (/\d+\s?[smhd]\s?前/.test(`${s}\n${numTexts.get(path) ?? ''}`) ? '相对时间用中文单位：「5 分钟前」' : null) },
  {
    id: 'button',
    check: (s, path) => {
      if (!isButton(path)) return null
      const n = [...strip(s).trim()].length
      if (s.startsWith('去')) return '按钮不以「去」开头'
      return n > rules.buttonMax ? `按钮超过 ${rules.buttonMax} 字` : null
    },
  },
]

type Hit = { path: string; rule: string; msg: string; text: string }
const hits: Hit[] = []
for (const [path, s] of texts) {
  for (const r of RULES) {
    const msg = r.check(s, path)
    if (msg) hits.push({ path, rule: r.id, msg, text: s })
  }
}

const used = new Set<Allow>()
const allowed = (h: Hit) => {
  const a = rules.allow.find((x) => glob(x.key, h.path) && !(x.except ?? []).some((e) => glob(e, h.path)) && (!x.rules || x.rules.includes(h.rule)))
  if (a) used.add(a)
  return !!a
}
const open = hits.filter((h) => !allowed(h))
const show = (list: Hit[]) => list.map((h) => `${h.path}：${h.msg}\n    ${h.text.replace(/\n/g, ' | ')}`).join('\n')

test('读到了全部文案', () => {
  assert.ok(texts.size > 1500, `只读到 ${texts.size} 条，遍历可能坏了`)
})

for (const r of RULES) {
  test(`文案规则 ${r.id}`, () => {
    const list = open.filter((h) => h.rule === r.id)
    assert.equal(list.length, 0, `${list.length} 处不符合 docs/copywriting.md（确属例外的在 docs/copywriting-rules.json 的 allow 里写理由）：\n${show(list)}`)
  })
}

test('状态只取术语表里的词（不可豁免）', () => {
  const bad: string[] = []
  for (const [pat, sets] of Object.entries(rules.statusKeys)) {
    const ok = new Set(sets.flatMap((k) => rules.statusSets[k] ?? []))
    const paths = [...texts.keys()].filter((p) => glob(pat, p))
    assert.ok(paths.length, `statusKeys 里的 ${pat} 在 zh.ts 里不存在`)
    for (const p of paths) for (const v of texts.get(p)!.split('\n')) if (!ok.has(v)) bad.push(`${p}：「${v}」不在 ${sets.join(' / ')} 里`)
  }
  assert.deepEqual(bad, [])
})

test('豁免都写了理由，且仍然有效', () => {
  const noReason = rules.allow.filter((a) => !a.reason?.trim()).map((a) => a.key)
  assert.deepEqual(noReason, [], '豁免必须写理由')
  const stale = rules.allow.filter((a) => !used.has(a)).map((a) => `${a.key}${a.rules ? `（${a.rules.join('、')}）` : ''}`)
  assert.deepEqual(stale, [], '这些豁免已拦不到任何问题，从 docs/copywriting-rules.json 删掉')
})
