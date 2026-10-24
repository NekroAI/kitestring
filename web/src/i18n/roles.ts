import { useSyncExternalStore } from 'react'

// 身份名（R-190）：主助理 / 顾问 / 评审 / 主调度 / 开发，英文 Aide / Adv / Rev / PM / Dev。
// 设置里选显示中文或英文，默认跟随界面语言（界面目前只有中文）；偏好存在本机。

export type RoleLang = 'auto' | 'zh' | 'en'

const KEY = 'ks.roleNames'
const NAMES: Record<'zh' | 'en', Record<string, string>> = {
  zh: { aide: '主助理', advisor: '顾问', reviewer: '评审', pm: '主调度', dev: '开发' },
  en: { aide: 'Aide', advisor: 'Adv', reviewer: 'Rev', pm: 'PM', dev: 'Dev' },
}

// 界面语言：目前只有中文。将来加了英文界面，这里返回当前界面语言即可。
const uiLang = (): 'zh' | 'en' => 'zh'

// 本机偏好：和 lib/util 的 loadPref / savePref 同一格式（JSON），这里单写一份，免得文案模块依赖工具模块
function load(): RoleLang {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? '"auto"')
    return v === 'zh' || v === 'en' ? v : 'auto'
  } catch {
    return 'auto'
  }
}

let lang: RoleLang = load()
const subs = new Set<() => void>()

export const roleLangOf = (v: RoleLang): 'zh' | 'en' => (v === 'auto' ? uiLang() : v)

// roleName：身份显示名；不认识的身份返回 undefined。
export const roleName = (role: string): string | undefined => NAMES[roleLangOf(lang)][role]

// 两种写法：zh / en 各自的名字（悬停说明里两个都写）。
export const roleNameIn = (role: string, l: 'zh' | 'en') => NAMES[l][role]

// both：悬停说明里两种叫法都写，当前语言在前：主调度（PM） / PM（主调度）。
export const both = (role: string) => {
  const l = roleLangOf(lang)
  return `${roleNameIn(role, l)}（${roleNameIn(role, l === 'zh' ? 'en' : 'zh')}）`
}

export function setRoleLang(v: RoleLang) {
  lang = v
  try {
    localStorage.setItem(KEY, JSON.stringify(v))
  } catch {
    // 隐私模式等情况下忽略
  }
  subs.forEach((f) => f())
}

// useRoleLang：订阅身份名语言；App 根组件用它，切换后整棵树重画。
export function useRoleLang(): [RoleLang, (v: RoleLang) => void] {
  const v = useSyncExternalStore(
    (f) => {
      subs.add(f)
      return () => subs.delete(f)
    },
    () => lang,
  )
  return [v, setRoleLang]
}

// roleNames：t.role[x] 的实现——按当前偏好取名字。
export const roleNames = new Proxy({} as Record<string, string>, {
  get: (_, k) => (typeof k === 'string' ? roleName(k) : undefined),
})

// rt：把身份名嵌进文案。按中文写（名字前后不留空格），英文名时自动在名字两边补空格、去掉标点旁多余的空格。
//   rt`允许${'pm'} / ${'aide'}放行草稿` → 允许主调度 / 主助理放行草稿 · 允许 PM / Aide 放行草稿
export function rt(strs: TemplateStringsArray, ...roles: string[]): string {
  const en = roleLangOf(lang) === 'en'
  let out = strs[0]
  roles.forEach((r, i) => {
    const n = roleName(r) // 不是身份标识的值（编号、数字）原样插入
    out += (n === undefined ? r : en ? ` ${n} ` : n) + strs[i + 1]
  })
  if (!en) return out
  return out
    .replace(/ {2,}/g, ' ')
    .replace(/ ([，。、；：（）「」『』！？])/g, '$1')
    .replace(/([（「『]) /g, '$1')
    .replace(/^ +| +$/g, '')
}
