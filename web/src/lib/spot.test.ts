import assert from 'node:assert/strict'
import { test } from 'node:test'
import { spotRule } from './spot.ts'

test('没有聚光键时不出规则', () => {
  assert.equal(spotRule(null), '')
  assert.equal(spotRule(''), '')
})

test('规则淡出其余元素、保留同键元素，呼吸灯随之暂停', () => {
  const r = spotRule('claude:abc')
  assert.ok(r.includes('[data-spot-key]:not([data-spot-key="claude:abc"]){opacity:.32;filter:saturate(.5);transition-delay:90ms}'))
  assert.ok(r.includes('.desk-lamp{animation-play-state:paused}'))
})

test('键里的引号和反斜杠被转义，不能跳出属性选择器', () => {
  const r = spotRule('a"]{x}\\')
  assert.ok(r.includes('[data-spot-key="a\\"]{x}\\\\"]'))
  assert.ok(!r.includes('"a"]'))
})
