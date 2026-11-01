// 全局快捷键（R-127）：按键 → 动作的判定是纯函数，单测见 keys.test.ts。
// 避开浏览器与系统已占用的组合：除 ⌘K / Ctrl+K（打开面板）、已有的 ⌘\（侧边栏）与判断动作的 ⌘↵ / ⌘⌫ 外都是不带修饰键的单键或 G 开头的两键序列；
// 带 ⌘ / Ctrl / Alt 的其它组合一律放行给浏览器。输入框、可编辑区、菜单里只认 ⌘K，单键一律不拦。
// ⌘↵ 通过、⌘⌫ 退回（R-161）：抽屉里待验收、焦点不在输入框时在这里判定；在附言框和卡片里由 Verdict 自己接。

export type KeyAction =
  | 'palette'
  | 'help'
  | 'new-req'
  | 'go-home'
  | 'go-projects'
  | 'go-reqs'
  | 'go-comms'
  | 'go-settings'
  | 'next'
  | 'prev'
  | 'pass'
  | 'revise'
  | 'back'
  | 'view'

export type KeyEv = { key: string; metaKey?: boolean; ctrlKey?: boolean; altKey?: boolean; shiftKey?: boolean; isComposing?: boolean; repeat?: boolean }

export type KeyCtx = {
  typing: boolean // 焦点在输入框、可编辑区或菜单里
  blocked: boolean // 面板、帮助表、对话框（新建需求等）开着，单键不生效
  drawer: boolean // 需求抽屉开着（J / K 切换上下一条）
  reviewing: boolean // 抽屉里的需求待验收（A 通过、R 退回）
  reqPage: boolean // 在需求页（V 轮换视图）
  pendingG: boolean // 上一下按了 G，等第二个键
}

export type KeyResult = { action: KeyAction | null; pendingG: boolean }

const goKeys: Record<string, KeyAction> = { h: 'go-home', p: 'go-projects', r: 'go-reqs', c: 'go-comms', s: 'go-settings' }

export function resolveKey(e: KeyEv, c: KeyCtx): KeyResult {
  const none: KeyResult = { action: null, pendingG: false }
  if (e.isComposing) return none
  const mod = !!(e.metaKey || e.ctrlKey)
  // ⌘K / Ctrl+K：任何地方都能打开（带修饰键不会误触）
  if (mod && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k') return { action: 'palette', pendingG: false }
  if (mod && !e.altKey && !e.shiftKey && !c.typing && !c.blocked && c.drawer && c.reviewing) {
    if (e.key === 'Enter') return { action: 'pass', pendingG: false }
    if (e.key === 'Backspace') return { action: 'back', pendingG: false }
  }
  if (mod || e.altKey) return none
  if (c.typing || c.blocked || e.repeat) return none
  const k = e.key.length === 1 ? e.key.toLowerCase() : e.key
  if (c.pendingG) return { action: goKeys[k] ?? null, pendingG: false }
  if (e.key === '?') return { action: 'help', pendingG: false }
  if (e.shiftKey) return none
  switch (k) {
    case 'g':
      return { action: null, pendingG: true }
    case 'n':
      return { action: 'new-req', pendingG: false }
    case 'j':
      return { action: 'next', pendingG: false }
    case 'k':
      return { action: 'prev', pendingG: false }
    case 'a':
      return { action: c.drawer && c.reviewing ? 'pass' : null, pendingG: false }
    case 'r':
      return { action: c.drawer && c.reviewing ? 'revise' : null, pendingG: false }
    case 'v':
      return { action: c.reqPage && !c.drawer ? 'view' : null, pendingG: false }
  }
  return none
}

// 帮助表（按 ? 打开）：分组列出，keys 是要画成键帽的几个键；mod 表示 ⌘ / Ctrl。
export type HelpRow = { id: string; keys: string[]; then?: boolean }
export const helpGroups: { id: 'global' | 'go' | 'list' | 'req'; rows: HelpRow[] }[] = [
  {
    id: 'global',
    rows: [
      { id: 'palette', keys: ['mod', 'K'] },
      { id: 'help', keys: ['?'] },
      { id: 'new-req', keys: ['N'] },
      { id: 'search', keys: ['/'] },
      { id: 'sidebar', keys: ['mod', '\\'] },
      { id: 'close', keys: ['Esc'] },
    ],
  },
  {
    id: 'go',
    rows: [
      { id: 'go-home', keys: ['G', 'H'], then: true },
      { id: 'go-projects', keys: ['G', 'P'], then: true },
      { id: 'go-reqs', keys: ['G', 'R'], then: true },
      { id: 'go-comms', keys: ['G', 'C'], then: true },
      { id: 'go-settings', keys: ['G', 'S'], then: true },
    ],
  },
  {
    id: 'list',
    rows: [
      { id: 'next', keys: ['J'] },
      { id: 'prev', keys: ['K'] },
      { id: 'open', keys: ['Enter'] },
      { id: 'view', keys: ['V'] },
    ],
  },
  {
    id: 'req',
    rows: [
      { id: 'switch', keys: ['←', '→'] },
      { id: 'pass', keys: ['A'] },
      { id: 'revise', keys: ['R'] },
      { id: 'verdict-pass', keys: ['mod', 'Enter'] },
      { id: 'verdict-back', keys: ['mod', '⌫'] },
    ],
  },
]

// isTyping：焦点在可输入的地方（或菜单、下拉里）时单键不生效。
export function isTyping(el: Element | null): boolean {
  if (!el || typeof (el as HTMLElement).closest !== 'function') return false
  if (el.closest('textarea, select, [contenteditable=""], [contenteditable="true"], [role=menu], [role=listbox], [role=combobox]')) return true
  if (el.tagName === 'INPUT') return !/^(checkbox|radio|button|submit|reset|range|color|file)$/i.test((el as HTMLInputElement).type)
  return false
}
