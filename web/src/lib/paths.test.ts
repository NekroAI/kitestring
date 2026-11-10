import { test } from 'node:test'
import assert from 'node:assert/strict'
import { pathBase, shortPath } from './paths.ts'

test('路径：Windows 设备的家目录换成 ~，最后一段认两种分隔符', () => {
  assert.equal(shortPath('/Users/me/proj'), '~/proj')
  assert.equal(shortPath('C:\\Users\\me\\proj'), '~\\proj')
  assert.equal(pathBase('/Users/me/proj/'), 'proj')
  assert.equal(pathBase('D:\\Projects\\kite'), 'kite')
  assert.equal(pathBase(''), '')
})
