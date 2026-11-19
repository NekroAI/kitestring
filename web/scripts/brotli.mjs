// 静态文件预压缩成 .br（R-369）：Hub 在浏览器接受 br 时直接返回它（web/embed.go），Go 侧不用引入 brotli 依赖。
// 只压 dist/assets 下的文本类文件，且压完更小的才留。pnpm build 在 vite build 之后运行。
import { readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { brotliCompressSync, constants } from 'node:zlib'

const dir = join(import.meta.dirname, '..', 'dist', 'assets')
let n = 0
let before = 0
let after = 0
for (const f of readdirSync(dir)) {
  if (!/\.(js|css|svg|json|txt|html)$/.test(f)) continue
  const raw = readFileSync(join(dir, f))
  if (raw.length < 1024) continue
  const br = brotliCompressSync(raw, { params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: raw.length } })
  if (br.length >= raw.length) continue
  writeFileSync(join(dir, f + '.br'), br)
  n++
  before += raw.length
  after += br.length
}
console.log(`brotli：${n} 个文件，${(before / 1024).toFixed(0)} KB → ${(after / 1024).toFixed(0)} KB`)
