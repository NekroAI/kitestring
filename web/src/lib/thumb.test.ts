import assert from 'node:assert/strict'
import { test } from 'node:test'
import { pickTier } from './thumb.ts'

test('按显示宽度 × 设备像素比选档', () => {
  assert.equal(pickTier(0, 2), 480)
  assert.equal(pickTier(200, 2), 480)
  assert.equal(pickTier(240, 2), 480)
  assert.equal(pickTier(241, 2), 960)
  assert.equal(pickTier(480, 2), 960)
  assert.equal(pickTier(481, 2), 1920)
  assert.equal(pickTier(900, 1), 960)
  assert.equal(pickTier(961, 1), 1920)
  assert.equal(pickTier(300, 0), 480)
  assert.equal(pickTier(1500, 2), 1920) // 再大也不回落原图
  assert.equal(pickTier(5000, 3), 1920)
})
