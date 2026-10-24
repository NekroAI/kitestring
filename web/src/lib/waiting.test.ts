import { test } from 'node:test'
import assert from 'node:assert/strict'
import { approvedOf, recOf, sessionAsk, verdictOf } from './waiting.ts'

// R-235 两张截图和 R-239 截图里的原文：只是对话里的话，没有提决策、没用提问工具
const texts = [
  '主调度正在部署这两条，部署时 hub 会重启约 1 分钟。\n\n失败提示改中文这件事，要不要我记成一条需求草稿？我在后台继续等新决策和交验收。',
  '主调度排了下一批：R-233、R-234 先做。\n\nR-235 和 R-236 等你确认后再排。',
  '确认「等你回复」卡片只截了会话最后两段（R-235）、提交失败的提示只写英文（R-236），草稿后再排期。',
]

test('对话里的问句不进要你处理', () => {
  for (const text of texts) {
    for (const state of ['waiting_reply', 'parked', 'idle']) {
      const s = { state, wait_kind: '', summary: text, digest_done: text, last_prompt: text }
      assert.equal(sessionAsk(s), '', `${state}：${text}`)
    }
  }
})

test('提问工具在问、等授权才算等你', () => {
  assert.equal(sessionAsk({ state: 'waiting_reply', wait_kind: 'question' }), 'question')
  assert.equal(sessionAsk({ state: 'waiting_approval', wait_kind: 'approval' }), 'permit')
  // 状态说明里写着工具名也不算，只认字段
  assert.equal(sessionAsk({ state: 'waiting_reply', wait_kind: '', state_detail: 'AskUserQuestion · 用哪个？' } as never), '')
  // 提问工具答完继续跑了，就不再等你
  assert.equal(sessionAsk({ state: 'running', wait_kind: 'question' }), '')
})

test('批准 / 拒绝读字段，不看答复文字', () => {
  assert.equal(approvedOf({ approval: true, verdict: 'approved', answer: '同意，按这个改' } as never), true)
  assert.equal(approvedOf({ approval: true, verdict: 'rejected', answer: '批准不了，范围太大' } as never), false)
  assert.equal(approvedOf({ approval: true, verdict: '', answer: '批准' } as never), undefined)
  assert.equal(approvedOf({ approval: false, verdict: 'approved' }), undefined)
})

test('推荐选项读字段，不模糊匹配', () => {
  const options = ['方案 A', '方案 B']
  assert.equal(recOf({ options, rec_option: 1, recommendation: '推荐 B' } as never), 1)
  assert.equal(recOf({ options, rec_option: -1, recommendation: '推荐「方案 B」' } as never), -1)
  assert.equal(recOf({ options, rec_option: 5 }), -1)
  // 老数据：Hub 补列时按原规则回填过，这里照字段显示
  assert.equal(recOf({ options, rec_option: 0 }), 0)
})

test('作答时随答复带上结论', () => {
  assert.equal(verdictOf('approval', 'pass'), 'approved')
  assert.equal(verdictOf('request', 'back'), 'rejected')
  assert.equal(verdictOf('answer', 'answer'), undefined)
})
