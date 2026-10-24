import { test } from 'node:test'
import assert from 'node:assert/strict'
import { roleName, rt, setRoleLang } from '../i18n/roles.ts'

// 身份名（R-190）：默认跟随界面语言（中文），可切英文；rt 在英文名两边补空格、标点旁不留空格。
test('身份名中英文切换', () => {
  assert.equal(roleName('pm'), '主调度')
  assert.equal(roleName('aide'), '主助理')
  assert.equal(rt`允许${'pm'} / ${'aide'}放行草稿`, '允许主调度 / 主助理放行草稿')
  setRoleLang('en')
  assert.equal(roleName('pm'), 'PM')
  assert.equal(roleName('reviewer'), 'Rev')
  assert.equal(rt`允许${'pm'} / ${'aide'}放行草稿`, '允许 PM / Aide 放行草稿')
  assert.equal(rt`交给${'reviewer'}「${'x'}」，`, '交给 Rev「x」，')
  assert.equal(rt`${'aide'}代答了 ${'D-3'}`, 'Aide 代答了 D-3')
  setRoleLang('auto')
  assert.equal(roleName('advisor'), '顾问')
  assert.equal(roleName('nobody'), undefined)
})
