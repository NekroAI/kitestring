import { test } from 'node:test'
import assert from 'node:assert/strict'
import { defaultWidth, frameLayout, pct } from './proto.ts'

test('适应窗口：框和可用区域一样大，不缩放', () => {
  assert.deepEqual(frameLayout('fit', 1000.6, 700), { w: 1000, h: 700, scale: 1 })
})

test('框比可用区域窄：按原宽度显示，不缩放', () => {
  assert.deepEqual(frameLayout('phone', 1200, 700), { w: 390, h: 700, scale: 1 })
})

test('框比可用区域宽：整体缩小，高度按比例放大后正好占满', () => {
  const L = frameLayout('desktop', 640, 600)
  assert.equal(L.w, 1280)
  assert.equal(L.scale, 0.5)
  assert.equal(L.h, 1200)
  assert.equal(pct(L.scale), '50%')
})

test('默认宽度：手机适应窗口，宽屏沿用上次的选择', () => {
  assert.equal(defaultWidth(390, 'desktop'), 'fit')
  assert.equal(defaultWidth(1440, 'tablet'), 'tablet')
  assert.equal(defaultWidth(1440, 'bogus'), 'desktop')
  assert.equal(defaultWidth(1440, null), 'desktop')
})
