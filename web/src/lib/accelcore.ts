// 加速地址（R-369）的纯逻辑：签名、拼链接、回主地址、「记住不可用」。浏览器里的状态和监听在 accel.ts。
// 签名算法与 Hub 的 internal/hub/accel.go 一致：sig = HMAC-SHA256(派生密钥, `${id}\n${w}\n${download ? 1 : ''}`) 的十六进制前 32 位。

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])

// sha256：同步实现（WebCrypto 只有异步接口，而附件地址要在渲染时同步算出）。
export function sha256(data: Uint8Array): Uint8Array {
  const len = data.length
  const n = Math.ceil((len + 9) / 64) * 64
  const buf = new Uint8Array(n)
  buf.set(data)
  buf[len] = 0x80
  const dv = new DataView(buf.buffer)
  dv.setUint32(n - 8, Math.floor(len / 0x20000000))
  dv.setUint32(n - 4, (len << 3) >>> 0)
  const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19])
  const w = new Uint32Array(64)
  for (let off = 0; off < n; off += 64) {
    for (let i = 0; i < 16; i++) w[i] = dv.getUint32(off + i * 4)
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15], b = w[i - 2]
      const s0 = ((a >>> 7) | (a << 25)) ^ ((a >>> 18) | (a << 14)) ^ (a >>> 3)
      const s1 = ((b >>> 17) | (b << 15)) ^ ((b >>> 19) | (b << 13)) ^ (b >>> 10)
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0
    }
    let [a, b, c, d, e, f, g, hh] = h
    for (let i = 0; i < 64; i++) {
      const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7))
      const ch = (e & f) ^ (~e & g)
      const t1 = (hh + S1 + ch + K[i] + w[i]) >>> 0
      const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10))
      const maj = (a & b) ^ (a & c) ^ (b & c)
      const t2 = (S0 + maj) >>> 0
      hh = g
      g = f
      f = e
      e = (d + t1) >>> 0
      d = c
      c = b
      b = a
      a = (t1 + t2) >>> 0
    }
    h[0] += a; h[1] += b; h[2] += c; h[3] += d; h[4] += e; h[5] += f; h[6] += g; h[7] += hh
  }
  const out = new Uint8Array(32)
  const ov = new DataView(out.buffer)
  for (let i = 0; i < 8; i++) ov.setUint32(i * 4, h[i])
  return out
}

const utf8 = (s: string) => new TextEncoder().encode(s)
export const toHex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
export const fromHex = (s: string) => new Uint8Array((s.match(/../g) ?? []).map((x) => parseInt(x, 16)))

export function hmacSha256(key: Uint8Array, msg: Uint8Array): Uint8Array {
  let k = key.length > 64 ? sha256(key) : key
  const pad = new Uint8Array(64)
  pad.set(k)
  k = pad
  const inner = new Uint8Array(64 + msg.length)
  const outer = new Uint8Array(64 + 32)
  for (let i = 0; i < 64; i++) {
    inner[i] = k[i] ^ 0x36
    outer[i] = k[i] ^ 0x5c
  }
  inner.set(msg, 64)
  outer.set(sha256(inner), 64)
  return sha256(outer)
}

export function accelSig(key: Uint8Array, id: string, w: string, download: boolean): string {
  return toHex(hmacSha256(key, utf8(`${id}\n${w}\n${download ? '1' : ''}`))).slice(0, 32)
}

export type AccelKey = { base: string; scope: string; key: string; exp: number }
export type FileOpts = { w?: number; download?: boolean }

// 签名链接：exp 前 5 分钟起就当作快过期，不再用它出新链接。
export const keyUsable = (k: AccelKey | null, now: number) => !!k && !!k.key && k.exp * 1000 - now > 5 * 60_000

export function signedFileUrl(k: AccelKey, id: string, o: FileOpts = {}): string {
  const w = o.w ? String(o.w) : ''
  const q = new URLSearchParams()
  if (w) q.set('w', w)
  if (o.download) q.set('download', '1')
  q.set('c', k.scope)
  q.set('e', String(k.exp))
  q.set('s', accelSig(fromHex(k.key), id, w, !!o.download))
  return `${k.base}/api/accel/f/${encodeURIComponent(id)}?${q}`
}

// mainFileUrl：主地址上同一个文件的地址（base 是 /api/files/ 或访客的 /api/share/<token>/files/）。
export function mainFileUrl(base: string, id: string, o: FileOpts = {}): string {
  const q = new URLSearchParams()
  if (o.w) q.set('w', String(o.w))
  if (o.download) q.set('download', '1')
  const s = q.toString()
  return `${base}${encodeURIComponent(id)}${s ? `?${s}` : ''}`
}

// toMain：把加速地址上的链接换成主地址上的（附件换成 mainBase 下的地址，静态文件、原型去掉前缀）；不是加速地址的返回 null。
export function toMain(url: string, accelBase: string, mainBase: string): string | null {
  if (!accelBase || !url.startsWith(accelBase + '/')) return null
  const rel = url.slice(accelBase.length)
  const m = rel.match(/^\/api\/accel\/f\/([^?#]+)(?:\?([^#]*))?/)
  if (!m) return rel
  const q = new URLSearchParams(m[2] ?? '')
  const w = Number(q.get('w')) || undefined
  return mainFileUrl(mainBase, decodeURIComponent(m[1]), { w, download: q.has('download') })
}

// 「记住不可用」：localStorage 里存 {down: 毫秒时间戳}，网页入口的启动脚本（internal/hub/accelpage.go）读同一个键。
export const DOWN_KEY = 'ks-accel'
export const DOWN_MS = 10 * 60_000
export const downUntil = (raw: string | null): number => {
  try {
    const v = JSON.parse(raw || '{}') as { down?: unknown }
    return typeof v?.down === 'number' ? v.down : 0
  } catch {
    return 0
  }
}

// AccelState：一个页面里加速地址的状态。环境（时间、存储、探测、定时器）由外面注入，测试里换成假的。
export type AccelEnv = {
  now(): number
  save(v: string): void
  probe(): Promise<boolean>
  later(fn: () => void, ms: number): void
  onDown?(until: number): void
}

export class AccelState {
  key: AccelKey | null = null
  mainBase = '/api/files/'
  private checking: Promise<boolean> | null = null
  private subs = new Set<() => void>()
  readonly base: string
  on: boolean
  private env: AccelEnv

  constructor(base: string, on: boolean, env: AccelEnv) {
    this.base = base
    this.on = !!base && on
    this.env = env
  }

  subscribe(f: () => void) {
    this.subs.add(f)
    return () => {
      this.subs.delete(f)
    }
  }
  private emit() {
    this.subs.forEach((f) => f())
  }

  // usable：加速地址可用、且有没快过期的签名密钥
  usable() {
    return this.on && keyUsable(this.key, this.env.now())
  }

  fileUrl(id: string, o: FileOpts = {}) {
    return this.usable() && this.key ? signedFileUrl(this.key, id, o) : mainFileUrl(this.mainBase, id, o)
  }

  // asset：原型这类自带凭据的公开地址（以 / 开头）在可用时加上加速地址前缀
  asset(path: string) {
    return this.on && path.startsWith('/') ? this.base + path : path
  }

  // markDown：记住一段时间不可用，到期后重新探测
  markDown() {
    const until = this.env.now() + DOWN_MS
    const was = this.on
    this.on = false
    this.env.save(JSON.stringify({ down: until }))
    this.env.onDown?.(until)
    if (was) {
      this.emit()
      this.env.later(() => void this.recheck(), DOWN_MS)
    }
  }

  // recheck：探测一次；可用就恢复，不可用就记住不可用。同一时间只探测一次。
  recheck(): Promise<boolean> {
    if (!this.base) return Promise.resolve(false)
    if (!this.checking) {
      this.checking = this.env.probe().then(
        (ok) => {
          this.checking = null
          if (ok && !this.on) {
            this.on = true
            this.env.save('{}')
            this.emit()
          } else if (!ok) this.markDown()
          return ok
        },
        () => {
          this.checking = null
          this.markDown()
          return false
        },
      )
    }
    return this.checking
  }

  // failed：加速地址上的某个文件加载失败。返回主地址上的同一个文件（调用方立刻换过去），并探测一次判断是不是加速地址整个不可用
  // （单个文件失败也可能是链接过期之类，不直接记不可用）。不是加速地址的返回 null。
  failed(url: string): string | null {
    const main = toMain(url, this.base, this.mainBase)
    if (main !== null && this.on) void this.recheck()
    return main
  }
}
