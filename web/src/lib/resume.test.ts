import { test } from 'node:test'
import assert from 'node:assert/strict'
import { resumeCommand, resumeNeedsLogin } from './resume.ts'

const base = { tool: 'claude', session_id: 'abc' }

test('默认目录的恢复命令照旧', () => {
  assert.equal(resumeCommand(base), 'claude --resume abc')
  assert.equal(resumeNeedsLogin(base), false)
})
test('配了启动命令用启动命令', () => {
  const s = { ...base, profile: 'claude-x', profile_dir: '/h/.claude-x', launcher: 'claude-x' }
  assert.equal(resumeCommand(s), 'claude-x --resume abc')
  assert.equal(resumeNeedsLogin(s), false)
})
test('没配启动命令带上 CLAUDE_CONFIG_DIR 并提示登录凭证', () => {
  const s = { ...base, profile: 'claude-x', profile_dir: '/h/.claude-x' }
  assert.equal(resumeCommand(s), 'CLAUDE_CONFIG_DIR=/h/.claude-x claude --resume abc')
  assert.equal(resumeNeedsLogin(s), true)
})
test('路径有空格时加引号', () => {
  assert.equal(resumeCommand({ ...base, profile_dir: '/h/my cfg' }), "CLAUDE_CONFIG_DIR='/h/my cfg' claude --resume abc")
})
test('Codex 不受影响', () => {
  assert.equal(resumeCommand({ tool: 'codex', session_id: 'z', profile_dir: '/x' }), 'codex resume z')
})
