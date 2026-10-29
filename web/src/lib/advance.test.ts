import assert from 'node:assert/strict'
import { test } from 'node:test'
import { advanceStep, type AdvanceEntry } from './advance.ts'

type Step = { e?: AdvanceEntry; leave?: boolean }
// 按时间顺序喂状态；leave = 用户翻走又回来（useAdvance 在 key 变化时把 armed 清零）
const run = (steps: Step[], on = true) => {
  let armed = 0
  return steps.map((s) => {
    if (s.leave) armed = 0
    const r = advanceStep(armed, s.e, on)
    armed = r.armed
    return r.fire
  })
}
const w = (at = 1): Step => ({ e: { at, phase: 'wait' } })
const s = (at = 1): Step => ({ e: { at, phase: 'seal' } })
const d = (at = 1): Step => ({ e: { at, phase: 'done' } })

test('情形一：撤销期里不翻，走完进入盖印才翻，且只翻一次', () => {
  assert.deepEqual(run([w(), w(), s(), d()]), [false, false, true, false])
})

test('情形二：撤销期内撤回，条目没了，不翻；重新下判断按新的一次算', () => {
  assert.deepEqual(run([w(), {}, {}]), [false, false, false])
  assert.deepEqual(run([w(1), {}, w(2), s(2)]), [false, false, false, true])
})

test('情形三：撤销期内用户自己翻走，自动跳转取消，翻回来也不再翻', () => {
  assert.deepEqual(run([w(), { leave: true }, { leave: true, ...s() }, { leave: true, ...d() }]), [false, false, false, false])
})

test('撤销期过后才翻回这一件、或不在连续审批时，不翻', () => {
  assert.deepEqual(run([d()]), [false])
  assert.deepEqual(run([w(), s()], false), [false, false])
})
