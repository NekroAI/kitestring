import { test } from 'node:test'
import assert from 'node:assert/strict'
import { fmtSize, isImage, isMedia, isText, kindOf, limitFor, previewText, typeLabel, viewable } from './files.ts'

test('只有 PNG / JPEG / GIF / WebP 与 MP4 / WebM / MOV 直接显示，SVG、HTML 是文件', () => {
  assert.equal(isImage('image/png'), true)
  assert.equal(isImage('image/svg+xml'), false)
  assert.equal(isMedia('video/quicktime'), true)
  assert.equal(isMedia('text/html; charset=utf-8'), false)
})

test('文本类能预览，压缩包不能', () => {
  assert.equal(isText({ name: 'gesture.json', mime: 'application/json' }), true)
  assert.equal(isText({ name: 'run.log', mime: 'text/plain; charset=utf-8' }), true)
  assert.equal(isText({ name: 'a.csv', mime: 'application/octet-stream' }), true)
  assert.equal(isText({ name: 'a.md', mime: 'text/markdown; charset=utf-8' }), true)
  assert.equal(isText({ name: 'b.zip', mime: 'application/zip' }), false)
  assert.equal(viewable({ name: 'b.zip', mime: 'application/zip' }), false)
  assert.equal(viewable({ name: 'a.png', mime: 'image/png' }), true)
})

test('文件块的图标、类型、大小', () => {
  assert.equal(kindOf({ name: 'gesture.json', mime: 'application/json' }), 'json')
  assert.equal(kindOf({ name: 'logs.zip', mime: 'application/zip' }), 'archive')
  assert.equal(kindOf({ name: 'a.csv', mime: 'text/csv' }), 'table')
  assert.equal(kindOf({ name: 'notes.txt', mime: 'text/plain' }), 'text')
  assert.equal(typeLabel({ name: 'logs.zip', mime: 'application/zip' }), 'ZIP')
  assert.equal(typeLabel({ name: 'README', mime: 'text/plain; charset=utf-8' }), 'PLAIN')
  assert.equal(fmtSize(512), '512 B')
  assert.equal(fmtSize(1536), '1.5 KB')
  assert.equal(fmtSize(3 * 1024 * 1024), '3.0 MB')
})

test('上传上限按类型取', () => {
  const l = { upload_image_mb: 10, upload_video_mb: 50, upload_file_mb: 20 }
  assert.deepEqual(limitFor('application/json', l), { kind: 'file', mb: 20 })
  assert.deepEqual(limitFor('image/png', l), { kind: 'image', mb: 10 })
  assert.deepEqual(limitFor('', l), { kind: 'file', mb: 20 })
  assert.deepEqual(limitFor('image/svg+xml', l), { kind: 'file', mb: 20 })
})

test('文本预览：完整 JSON 排版，超长截断', () => {
  const p = previewText('{"a":[1,2]}', { name: 'x.json', mime: 'application/json' }, false)
  assert.equal(p.text, '{\n  "a": [\n    1,\n    2\n  ]\n}')
  assert.equal(p.more, false)
  const long = Array.from({ length: 2500 }, (_, i) => `line ${i}`).join('\n')
  const q = previewText(long, { name: 'x.log', mime: 'text/plain' }, false)
  assert.equal(q.lines, 2000)
  assert.equal(q.more, true)
  // 只读了前一段的 JSON 不排版
  assert.equal(previewText('{"a":', { name: 'x.json', mime: 'application/json' }, true).text, '{"a":')
})
