import { test } from 'node:test'
import assert from 'node:assert/strict'
import { classify, failText } from './fail.ts'

test('断网（Failed to fetch）写成中文：哪件事没成功、为什么、没有改动可以再试（R-236）', () => {
  const e = new TypeError('Failed to fetch')
  assert.equal(failText(classify(e), '关闭 D-964'), '关闭 D-964 没成功：连不上纸鸢（网络断了，或纸鸢正在重启）。没有改动，可以再试一次。')
  assert.equal(classify(new TypeError('Load failed')).kind, 'network') // Safari 的说法
  assert.equal(classify(new TypeError('NetworkError when attempting to fetch resource.')).kind, 'network') // Firefox
})

test('超时', () => {
  const e = Object.assign(new Error('signal timed out'), { name: 'TimeoutError' })
  assert.equal(failText(classify(e), '提交验收'), '提交验收没成功：纸鸢太久没有回应。没有改动，可以再试一次。')
})

test('纸鸢的中文说明直接用；英文原文按状态码换成白话', () => {
  const zh = Object.assign(new Error('目标会话不存在'), { status: 400 })
  assert.equal(failText(classify(zh), '转交 D-1'), '转交 D-1 没成功：目标会话不存在。没有改动，可以再试一次。')
  for (const [status, msg, want] of [
    [404, 'Not Found', '找不到这条记录，可能已经被删除'],
    [422, 'validation failed', '提交的内容不对'],
    [500, 'Internal Server Error', '纸鸢出错了（500）'],
    [502, 'Bad Gateway', '纸鸢出错了（502）'],
  ] as const) {
    const text = failText(classify(Object.assign(new Error(msg), { status })), '保存')
    assert.ok(text.includes(want), text)
    assert.ok(!/[A-Za-z]{4,}/.test(text), `不应有英文原文：${text}`)
  }
})

test('登录过期不说「可以再试」', () => {
  assert.equal(failText(classify(Object.assign(new Error('Unauthorized'), { status: 401 })), '提交验收'), '提交验收没成功：登录已过期，要重新登录。')
})

test('不知道是什么的英文错误不原样显示', () => {
  assert.equal(failText(classify(new Error('Unexpected token < in JSON')), '保存设置'), '保存设置没成功：出了点问题。没有改动，可以再试一次。')
  assert.equal(failText(classify('x')), '出了点问题。没有改动，可以再试一次。')
})
