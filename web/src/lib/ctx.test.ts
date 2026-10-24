import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ctxLevel } from './ctx.ts'

test('建议压缩阈值默认 30 万：一半变黄、到阈值变红', () => {
  assert.equal(ctxLevel(100_000, 0).level, 'ok')
  assert.equal(ctxLevel(150_000, 0).level, 'mid')
  assert.equal(ctxLevel(299_999, 0).level, 'mid')
  assert.equal(ctxLevel(300_000, 0).level, 'high')
  assert.equal(ctxLevel(480_000, 0).ratio, 0.48)
})

test('阈值可调，知道窗口时取阈值和窗口 80% 中小的', () => {
  assert.equal(ctxLevel(300_000, 0, 500_000).level, 'mid')
  assert.equal(ctxLevel(500_000, 0, 500_000).level, 'high')
  const codex = ctxLevel(210_000, 258_400) // 窗口 80% ≈ 20.7 万 < 30 万
  assert.equal(codex.limit, 206_720)
  assert.equal(codex.level, 'high')
  assert.equal(ctxLevel(132_379, 828_400).level, 'ok')
})
