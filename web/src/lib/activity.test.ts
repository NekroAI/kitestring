import { test } from 'node:test'
import assert from 'node:assert/strict'
import { lineOf, whoOf } from './activity.ts'

type A = Parameters<typeof lineOf>[0]
const a = (x: Partial<A>): A => ({ at: '2026-11-06T07:00:00Z', kind: 'evidence', actor: 'agent', code: 'R-1', ...x }) as A

// 最近动态（R-296）：主语写清是你、哪个身份 / 会话、还是纸鸢；会话的关键动作都有句子。
test('主语：你 / 身份名 / 会话名 / 纸鸢', () => {
  assert.equal(whoOf({ actor: 'user' }), '你')
  assert.equal(whoOf({ actor: 'system' }), '纸鸢')
  assert.equal(whoOf({ actor: 'agent', role: 'pm', session_title: '派活' }), '主调度')
  assert.equal(whoOf({ actor: 'agent', session_title: '实现接口' }), '「实现接口」')
  assert.equal(whoOf({ actor: 'agent' }), '会话')
})

test('会话动作与用户动作的句子', () => {
  const cases: [Partial<A>, string][] = [
    [{ kind: 'evidence', role: 'dev' }, '开发提交了证据 R-1'],
    [{ kind: 'submit', role: 'dev', evidence: true }, '开发提交了证据并提交验收 R-1'],
    [{ kind: 'submit', role: 'pm' }, '主调度提交了验收 R-1'],
    [{ kind: 'review', role: 'reviewer', text: '可以通过' }, '评审提交了评审意见 R-1：可以通过'],
    [{ kind: 'blocked', role: 'aide', ref: 'D-9', text: '方案等用户在 D-9 定三点' }, '主助理设为受阻 R-1：方案等用户在 D-9 定三点'],
    [{ kind: 'blocked', role: 'aide', ref: 'D-9', text: '等用户定' }, '主助理设为受阻 R-1：等待 D-9，等用户定'],
    [{ kind: 'unblock', actor: 'system', ref: 'D-9' }, '纸鸢解除了受阻 R-1：等待的 D-9 已答复'],
    [{ kind: 'unblock', actor: 'system', ref: 'R-3' }, '纸鸢解除了受阻 R-1：等待的 R-3 已完成'],
    [{ kind: 'raise', role: 'pm', code: 'D-9', approval: true }, '主调度请求批准 D-9'],
    [{ kind: 'raise', role: 'dev', code: 'D-9' }, '开发提出了决策 D-9'],
    [{ kind: 'assign', actor: 'user', target_title: '实现接口' }, '你指派了 R-1：给「实现接口」'],
    [{ kind: 'assign', role: 'pm', target_role: 'dev' }, '主调度指派了 R-1：给开发'],
    [{ kind: 'review_task', actor: 'system' }, '纸鸢提交了评审 R-1'],
    [{ kind: 'release', role: 'aide' }, '主助理放行了 R-1'],
    [{ kind: 'comment', role: 'aide', text: '细节见下' }, '主助理留言 R-1：细节见下'],
    [{ kind: 'note', actor: 'user', text: '看下手机' }, '你留言 R-1：看下手机'],
    [{ kind: 'revise', actor: 'user', text: '缺深色' }, '你退回 R-1：缺深色'],
    [{ kind: 'done', actor: 'user' }, '你通过了 R-1'],
    [{ kind: 'answer', actor: 'user', code: 'D-9', approval: true, verdict: 'approved' }, '你批准了 D-9'],
    [{ kind: 'answer', actor: 'user', code: 'D-9', approval: true, verdict: 'rejected' }, '你拒绝了 D-9'],
    [{ kind: 'answer', actor: 'user', code: 'D-9', verdict: 'answer' }, '你回答了 D-9'],
    [{ kind: 'answer', actor: 'agent', role: 'aide', code: 'D-9', verdict: 'proxy' }, '主助理代答了 D-9'],
    [{ kind: 'stage', actor: 'user', code: 'P-2', stage_seq: 1, stage_title: '后端' }, '你通过了 P-2 阶段 1：后端'],
  ]
  for (const [x, want] of cases) assert.equal(lineOf(a(x)).text, want)
})
