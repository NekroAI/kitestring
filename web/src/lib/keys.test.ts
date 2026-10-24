import { test } from 'node:test'
import assert from 'node:assert/strict'
import { isTyping, resolveKey, type KeyCtx } from './keys.ts'

const base: KeyCtx = { typing: false, blocked: false, drawer: false, reviewing: false, reqPage: false, pendingG: false }

test('⌘K / Ctrl+K 任何地方都打开面板', () => {
  assert.equal(resolveKey({ key: 'k', metaKey: true }, base).action, 'palette')
  assert.equal(resolveKey({ key: 'K', ctrlKey: true }, { ...base, typing: true }).action, 'palette')
  assert.equal(resolveKey({ key: 'k', metaKey: true, shiftKey: true }, base).action, null)
})

test('其它带修饰键的组合放行给浏览器', () => {
  for (const key of ['n', 'r', 'a', 'j', 'l', 't', 'w'])
    assert.equal(resolveKey({ key, metaKey: true }, base).action, null)
  assert.equal(resolveKey({ key: 'n', altKey: true }, base).action, null)
})

test('输入框里、对话框开着、输入法组字时单键不生效', () => {
  for (const key of ['n', 'j', 'k', '?', 'g', 'a', 'v'])
    assert.deepEqual(resolveKey({ key }, { ...base, typing: true, drawer: true, reviewing: true, reqPage: true }), { action: null, pendingG: false })
  assert.equal(resolveKey({ key: 'n' }, { ...base, blocked: true }).action, null)
  assert.equal(resolveKey({ key: 'n', isComposing: true }, base).action, null)
  assert.equal(resolveKey({ key: 'j', repeat: true }, base).action, null)
})

test('单键动作', () => {
  assert.equal(resolveKey({ key: 'n' }, base).action, 'new-req')
  assert.equal(resolveKey({ key: '?', shiftKey: true }, base).action, 'help')
  assert.equal(resolveKey({ key: 'j' }, base).action, 'next')
  assert.equal(resolveKey({ key: 'k' }, base).action, 'prev')
  assert.equal(resolveKey({ key: 'N', shiftKey: true }, base).action, null)
})

test('A / R 只在抽屉里的待验收需求上生效', () => {
  assert.equal(resolveKey({ key: 'a' }, base).action, null)
  assert.equal(resolveKey({ key: 'a' }, { ...base, drawer: true }).action, null)
  assert.equal(resolveKey({ key: 'a' }, { ...base, drawer: true, reviewing: true }).action, 'pass')
  assert.equal(resolveKey({ key: 'r' }, { ...base, drawer: true, reviewing: true }).action, 'revise')
})

test('V 只在需求页、没开抽屉时轮换视图', () => {
  assert.equal(resolveKey({ key: 'v' }, base).action, null)
  assert.equal(resolveKey({ key: 'v' }, { ...base, reqPage: true }).action, 'view')
  assert.equal(resolveKey({ key: 'v' }, { ...base, reqPage: true, drawer: true }).action, null)
})

test('G 序列', () => {
  const g = resolveKey({ key: 'g' }, base)
  assert.deepEqual(g, { action: null, pendingG: true })
  assert.equal(resolveKey({ key: 'h' }, { ...base, pendingG: true }).action, 'go-home')
  assert.equal(resolveKey({ key: 'r' }, { ...base, pendingG: true, drawer: true, reviewing: true }).action, 'go-reqs')
  assert.deepEqual(resolveKey({ key: 'x' }, { ...base, pendingG: true }), { action: null, pendingG: false })
})

test('isTyping', () => {
  const el = (tag: string, type = '', inside = false) => ({ tagName: tag, type, closest: () => (inside ? {} : null) }) as unknown as Element
  assert.equal(isTyping(null), false)
  assert.equal(isTyping(el('INPUT', 'text')), true)
  assert.equal(isTyping(el('INPUT', 'checkbox')), false)
  assert.equal(isTyping(el('DIV', '', true)), true)
  assert.equal(isTyping(el('BUTTON')), false)
})

test('⌘↵ 通过、⌘⌫ 退回：抽屉里待验收、焦点不在输入框时（R-161）', () => {
  const rev = { ...base, drawer: true, reviewing: true }
  assert.equal(resolveKey({ key: 'Enter', metaKey: true }, rev).action, 'pass')
  assert.equal(resolveKey({ key: 'Enter', ctrlKey: true }, rev).action, 'pass')
  assert.equal(resolveKey({ key: 'Backspace', metaKey: true }, rev).action, 'back')
  // 输入框里由 Verdict 自己接；不在待验收、对话框开着时放行
  assert.equal(resolveKey({ key: 'Enter', metaKey: true }, { ...rev, typing: true }).action, null)
  assert.equal(resolveKey({ key: 'Backspace', metaKey: true }, { ...rev, blocked: true }).action, null)
  assert.equal(resolveKey({ key: 'Enter', metaKey: true }, { ...base, drawer: true }).action, null)
  assert.equal(resolveKey({ key: 'Backspace', metaKey: true }, base).action, null)
})
