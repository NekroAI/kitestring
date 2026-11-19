import assert from 'node:assert/strict'
import { createHash, createHmac } from 'node:crypto'
import { test } from 'node:test'
import { AccelState, accelSig, downUntil, fromHex, hmacSha256, keyUsable, mainFileUrl, sha256, signedFileUrl, toHex, toMain, type AccelKey } from './accelcore.ts'

const enc = (s: string) => new TextEncoder().encode(s)

test('sha256 与 node 一致（含跨块长度）', () => {
  for (const n of [0, 1, 55, 56, 63, 64, 65, 119, 120, 1000]) {
    const s = 'a'.repeat(n) + '纸鸢'
    assert.equal(toHex(sha256(enc(s))), createHash('sha256').update(s).digest('hex'), `长度 ${n}`)
  }
})

test('HMAC-SHA256 与 node 一致（含长于 64 字节的密钥）', () => {
  for (const key of ['k', 'x'.repeat(64), 'y'.repeat(100)]) {
    assert.equal(toHex(hmacSha256(enc(key), enc('消息 msg'))), createHmac('sha256', key).update('消息 msg').digest('hex'))
  }
})

// 与 internal/hub/accel_test.go 的 TestAccelSigVector 同一组数：Hub 和网页算出的签名必须一样
test('签名与 Hub 的固定向量一致', () => {
  const k = fromHex('7066e1dcbb9353abe2a8297d833bf7ec252a62e73797b16bff3f725726cc1bdc')
  assert.equal(accelSig(k, '01ABC', '480', false), '44bc731671791a2342f64064fc9fc710')
  assert.notEqual(accelSig(k, '01ABC', '480', true), '44bc731671791a2342f64064fc9fc710')
})

const key: AccelKey = { base: 'https://accel.example.com:8081', scope: 'user', key: '7066e1dcbb9353abe2a8297d833bf7ec252a62e73797b16bff3f725726cc1bdc', exp: 1_700_003_600 }

test('拼签名链接', () => {
  const u = new URL(signedFileUrl(key, '01ABC', { w: 480 }))
  assert.equal(u.origin, 'https://accel.example.com:8081')
  assert.equal(u.pathname, '/api/accel/f/01ABC')
  assert.equal(u.searchParams.get('w'), '480')
  assert.equal(u.searchParams.get('c'), 'user')
  assert.equal(u.searchParams.get('e'), '1700003600')
  assert.equal(u.searchParams.get('s'), '44bc731671791a2342f64064fc9fc710')
  assert.ok(signedFileUrl(key, '01ABC', { download: true }).includes('download=1'))
  assert.ok(signedFileUrl({ ...key, scope: 'share:01LINK' }, '01ABC').includes('c=share%3A01LINK'))
})

test('密钥快过期就不再用', () => {
  const now = key.exp * 1000
  assert.equal(keyUsable(key, now - 60 * 60_000), true)
  assert.equal(keyUsable(key, now - 4 * 60_000), false)
  assert.equal(keyUsable(null, 0), false)
})

test('失败时回主地址：附件换回登录或访客地址，静态文件和原型去掉前缀', () => {
  const b = key.base
  assert.equal(toMain(signedFileUrl(key, '01ABC', { w: 960 }), b, '/api/files/'), '/api/files/01ABC?w=960')
  assert.equal(toMain(signedFileUrl(key, '01ABC', { download: true }), b, '/api/share/ks_s_x/files/'), '/api/share/ks_s_x/files/01ABC?download=1')
  assert.equal(toMain(`${b}/assets/index-abc.js`, b, '/api/files/'), '/assets/index-abc.js')
  assert.equal(toMain(`${b}/p/tok/index.html`, b, '/api/files/'), '/p/tok/index.html')
  assert.equal(toMain('/api/files/01ABC', b, '/api/files/'), null)
  assert.equal(toMain('https://accel.example.com:80810/x', b, '/api/files/'), null)
  assert.equal(toMain('https://x.example.com/a', '', '/api/files/'), null)
  assert.equal(mainFileUrl('/api/files/', 'a b'), '/api/files/a%20b')
})

test('读「记住不可用」', () => {
  assert.equal(downUntil(JSON.stringify({ down: 123 })), 123)
  assert.equal(downUntil(null), 0)
  assert.equal(downUntil('坏的'), 0)
  assert.equal(downUntil('{"down":"x"}'), 0)
})

function fakeEnv(probe: () => Promise<boolean>) {
  const e = { t: 1_700_000_000_000, stored: '', timers: [] as { fn: () => void; ms: number }[], downs: [] as number[] }
  const env = {
    now: () => e.t,
    save: (v: string) => {
      e.stored = v
    },
    probe,
    later: (fn: () => void, ms: number) => {
      e.timers.push({ fn, ms })
    },
    onDown: (u: number) => {
      e.downs.push(u)
    },
  }
  return { e, env }
}

test('空着加速地址：链接和改动前一样', () => {
  const { env } = fakeEnv(async () => true)
  const s = new AccelState('', true, env)
  s.key = key
  assert.equal(s.on, false)
  assert.equal(s.fileUrl('01ABC'), '/api/files/01ABC')
  assert.equal(s.fileUrl('01ABC', { w: 480 }), '/api/files/01ABC?w=480')
  assert.equal(s.fileUrl('01ABC', { download: true }), '/api/files/01ABC?download=1')
  assert.equal(s.asset('/p/tok/'), '/p/tok/')
  assert.equal(s.failed('https://accel.example.com:8081/api/accel/f/01ABC'), null)
})

test('可用时走加速；失败逐个回主地址，探测不通就记住一段时间不可用，到期重新探测后恢复', async () => {
  let up = true
  const { e, env } = fakeEnv(async () => up)
  const s = new AccelState(key.base, true, env)
  assert.equal(s.fileUrl('01ABC'), '/api/files/01ABC', '还没有签名密钥时走主地址')
  s.key = key
  e.t = key.exp * 1000 - 30 * 60_000
  const u = s.fileUrl('01ABC', { w: 480 })
  assert.ok(u.startsWith(key.base + '/api/accel/f/01ABC?'))
  assert.equal(s.asset('/p/tok/'), key.base + '/p/tok/')

  // 单个文件失败但加速地址探测得通（例如链接过期）：这一个回主地址，整体不记不可用
  assert.equal(s.failed(u), '/api/files/01ABC?w=480')
  await s.recheck()
  assert.equal(s.on, true)
  assert.equal(e.downs.length, 0)

  // 加速地址断了：失败的回主地址，探测不通 → 记住 10 分钟不可用，之后的链接直接走主地址
  up = false
  let changed = 0
  s.subscribe(() => changed++)
  assert.equal(s.failed(u), '/api/files/01ABC?w=480')
  await s.recheck()
  assert.equal(s.on, false)
  assert.equal(changed, 1)
  assert.equal(downUntil(e.stored), e.t + 10 * 60_000)
  assert.deepEqual(e.downs, [e.t + 10 * 60_000])
  assert.equal(s.fileUrl('01ABC', { w: 480 }), '/api/files/01ABC?w=480')
  assert.equal(e.timers.length, 1)
  assert.equal(e.timers[0].ms, 10 * 60_000)

  // 到期重新探测：恢复了就重新走加速
  up = true
  e.timers[0].fn()
  await s.recheck()
  assert.equal(s.on, true)
  assert.equal(downUntil(e.stored), 0)
  assert.ok(s.fileUrl('01ABC').startsWith(key.base))
})

test('同时多个文件失败只探测一次', async () => {
  let n = 0
  const { env } = fakeEnv(async () => {
    n++
    return false
  })
  const s = new AccelState(key.base, true, env)
  s.key = key
  for (let i = 0; i < 5; i++) s.failed(`${key.base}/assets/a${i}.js`)
  await s.recheck()
  assert.equal(n, 1)
  assert.equal(s.on, false)
})
