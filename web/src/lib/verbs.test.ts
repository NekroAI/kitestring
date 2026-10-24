import { test } from 'node:test'
import assert from 'node:assert/strict'
import { zh } from '../i18n/zh.ts'

const v = zh.verdict

// R-176（D-268）：按场景分词，不再把所有判断统一成「批准 / 退回」
test('各场景的肯定 / 否定用词', () => {
  assert.deepEqual([v.pass('accept'), v.back('accept')], ['通过', '退回'])
  assert.deepEqual([v.pass('plan'), v.back('plan')], ['批准', '退回'])
  assert.deepEqual([v.pass('approval'), v.back('approval')], ['批准', '拒绝'])
  assert.deepEqual([v.pass('request'), v.back('request')], ['批准', '拒绝'])
  assert.equal(v.pass('answer'), '回答')
  assert.deepEqual([v.pass('permit'), v.back('permit')], ['允许', '拒绝'])
})

test('撤销条、印章、快捷键提示随场景', () => {
  assert.equal(v.pending(v.pass('accept'), 3), '3 秒后通过')
  assert.equal(v.pending(v.back('approval'), 3), '3 秒后拒绝')
  assert.equal(v.stamp(v.pass('accept')), '已通过')
  assert.equal(v.stamp(v.back('approval')), '已拒绝')
  assert.equal(v.stamp(v.pass('plan')), '已批准')
  assert.equal(v.keyPass('accept'), '通过 · ⌘↵')
  assert.equal(v.keyBack('approval'), '拒绝 · ⌘⌫')
  assert.equal(v.keyPass('answer'), '回答 · ⌘↵')
  assert.equal(v.needNote('approval'), '拒绝要写下理由')
  assert.equal(v.needNote('plan'), '退回要写下意见：哪里要改')
})

test('⌘K 命令与验收相关文案用「通过」', () => {
  assert.equal(zh.cmd.passCur('R-12'), '通过 R-12')
  assert.equal(zh.cmd.reviseCur('R-12'), '退回 R-12（写意见）')
  assert.equal(v.passAll(3), '全部通过（3）')
  assert.match(zh.keys.row['verdict-back'], /退回 \/ 拒绝/)
})
