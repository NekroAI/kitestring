import { test } from 'node:test'
import assert from 'node:assert/strict'
import { modelShort } from './model.ts'

test('模型短名', () => {
  assert.equal(modelShort('claude-opus-5-5'), 'Opus 5.5')
  assert.equal(modelShort('claude-sonnet-5-5'), 'Sonnet 5.5')
  assert.equal(modelShort('claude-haiku-4-5-20251001'), 'Haiku 4.5')
  assert.equal(modelShort('claude-opus-5'), 'Opus 5')
  assert.equal(modelShort('claude-opus-5-5[1m]'), 'Opus 5.5')
  assert.equal(modelShort('claude-3-5-sonnet-20241022'), 'Sonnet 3.5')
  assert.equal(modelShort('gpt-6.1-sol'), 'GPT-6.1 Sol')
  assert.equal(modelShort('gpt-6-astra'), 'GPT-6 Astra')
  assert.equal(modelShort('deepseek-flash'), 'deepseek-flash')
  assert.equal(modelShort('whatever-x'), 'whatever-x')
  assert.equal(modelShort(''), '')
})
