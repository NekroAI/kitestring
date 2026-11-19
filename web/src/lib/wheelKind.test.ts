import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { firstKind, isDoubleTap, KIND_GAP, swipeAsDrag, tapOf, TAP_GAP, touchOp, wheel3d, wheelImage, WheelKind, type WheelInput } from './wheelKind.ts'

const ev = (p: Partial<WheelInput>): WheelInput => ({ dx: 0, dy: 0, mode: 0, ctrl: false, t: 0, ...p })

test('捏合：带 ctrlKey 的每一下都是捏合，夹在滑动段里也一样', () => {
  const k = new WheelKind()
  assert.equal(k.feed(ev({ dy: 3, t: 0 })), 'trackpad')
  assert.equal(k.feed(ev({ dy: -2.5, ctrl: true, t: 8 })), 'pinch')
  assert.equal(k.feed(ev({ dy: 4, t: 16 })), 'trackpad')
})

test('鼠标滚轮：各平台的一格', () => {
  assert.equal(firstKind({ dx: 0, dy: 3, mode: 1 }), 'mouse') // Firefox Windows / Linux 按行
  assert.equal(firstKind({ dx: 0, dy: 100, mode: 0, wdy: -120 }), 'mouse') // Chrome Windows
  assert.equal(firstKind({ dx: 0, dy: 125, mode: 0 }), 'mouse') // 125% 缩放的 Windows，没有 wheelDeltaY
  assert.equal(firstKind({ dx: 0, dy: -4.000244140625, mode: 0, wdy: 12 }), 'mouse') // macOS Chrome 鼠标
  assert.equal(firstKind({ dx: 0, dy: 8.00048828125, mode: 0 }), 'mouse')
  assert.equal(firstKind({ dx: 0, dy: 40, mode: 0, wdy: -120 }), 'mouse')
})

test('Shift+鼠标滚轮出的横向不当成触控板', () => {
  assert.equal(firstKind({ dx: 100, dy: 0, mode: 0, shift: true, wdy: 0 }), 'mouse')
})

test('触控板双指滑动：起手轻、整数像素、wheelDeltaY = -3 × deltaY', () => {
  assert.equal(firstKind({ dx: 0, dy: 2, mode: 0, wdy: -6 }), 'trackpad')
  assert.equal(firstKind({ dx: 1, dy: 6, mode: 0, wdy: -18 }), 'trackpad')
  assert.equal(firstKind({ dx: 0, dy: -1, mode: 0 }), 'trackpad')
  assert.equal(firstKind({ dx: 0, dy: 0.5, mode: 0 }), 'trackpad') // 非 4.0002 倍数的小数（Safari 高精度）
})

test('一段之内类别不变：触控板加速到大力度、惯性尾巴都不改判', () => {
  const k = new WheelKind()
  const seq = [2, 5, 12, 30, 60, 90, 120, 80, 40, 20, 8, 3, 1]
  seq.forEach((dy, i) => assert.equal(k.feed(ev({ dy, wdy: -3 * dy, t: i * 8 })), 'trackpad'))
})

test('停够 KIND_GAP 才重新判别：鼠标转一格接着用触控板', () => {
  const k = new WheelKind()
  assert.equal(k.feed(ev({ dy: 100, wdy: -120, t: 0 })), 'mouse')
  assert.equal(k.feed(ev({ dy: 100, wdy: -120, t: 60 })), 'mouse') // 连着转
  assert.equal(k.feed(ev({ dy: 2, t: 60 + KIND_GAP - 1 })), 'mouse') // 还没停够
  assert.equal(k.feed(ev({ dy: 2, t: 60 + KIND_GAP * 3 })), 'trackpad')
})

test('真机妙控板日志（Mac Chrome，R-73）全部判成触控板', () => {
  for (const f of ['gesturelog-mac-chrome.json', 'gesturelog-mac-chrome-reverse.json']) {
    const log = JSON.parse(readFileSync(new URL(`./fixtures/${f}`, import.meta.url), 'utf8')) as { wheel: [number, number, number, number][] }
    const k = new WheelKind()
    for (const [t, dx, dy, mode] of log.wheel) assert.equal(k.feed({ t, dx, dy, mode, ctrl: false }), 'trackpad', `${f} @${t}`)
  }
})

test('3D：捏合和鼠标滚轮缩放；触控板滑动在舞台上直接转，嵌在可滚动区域里点过才转', () => {
  assert.equal(wheel3d('pinch', { active: false, embedded: true }), 'zoom')
  assert.equal(wheel3d('mouse', { active: false, embedded: true }), 'zoom')
  assert.equal(wheel3d('mouse', { active: true, embedded: false }), 'zoom')
  assert.equal(wheel3d('trackpad', { active: false, embedded: false }), 'rotate')
  assert.equal(wheel3d('trackpad', { active: false, embedded: true }), 'pass')
  assert.equal(wheel3d('trackpad', { active: true, embedded: true }), 'rotate')
})

test('看大图：捏合缩放，鼠标走原路，触控板放大后平移、没放大时交给翻页', () => {
  assert.equal(wheelImage('pinch', { zoomed: false }), 'zoom')
  assert.equal(wheelImage('mouse', { zoomed: true }), 'mouse')
  assert.equal(wheelImage('trackpad', { zoomed: true }), 'pan')
  assert.equal(wheelImage('trackpad', { zoomed: false }), 'nav')
})

test('触控板滑动转模型和拖动同向（自然滚动：手指右移 deltaX 为负）', () => {
  assert.deepEqual(swipeAsDrag(-10, 4), { x: 10, y: -4 })
})

test('触点数 → 操作', () => {
  assert.equal(touchOp(1), 'rotate')
  assert.equal(touchOp(2), 'zoomPan')
  assert.equal(touchOp(3), 'none')
  assert.equal(touchOp(0), 'none')
})

test('双击：两次轻点够近够快才算；拖动、长按不算轻点', () => {
  const a = tapOf({ t: 0, x: 100, y: 100 }, { t: 90, x: 102, y: 101 })
  assert.ok(a)
  const b = tapOf({ t: 200, x: 110, y: 104 }, { t: 280, x: 110, y: 104 })
  assert.ok(b)
  assert.equal(isDoubleTap(a, b!), true)
  assert.equal(isDoubleTap(null, b!), false)
  assert.equal(isDoubleTap(a, { t: 90 + TAP_GAP + 1, x: 100, y: 100 }), false) // 太慢
  assert.equal(isDoubleTap(a, { t: 200, x: 160, y: 100 }), false) // 太远
  assert.equal(tapOf({ t: 0, x: 0, y: 0 }, { t: 100, x: 30, y: 0 }), null) // 拖动
  assert.equal(tapOf({ t: 0, x: 0, y: 0 }, { t: 600, x: 0, y: 0 }), null) // 长按
})
