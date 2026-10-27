// 把 MCP 的 inputSchema 收敛到 DSH 工具 schema 支持的子集。
// 目前实际遇到的差异只有一处：Go 侧把可选字段写成 type: ["null","string"] / ["null","array"]，
// 而 DSH 只接受单个 type 字符串。其余未知关键字直接丢弃，避免 Loader 在注册时拒绝整个工具。

const ALLOWED = new Set([
  'type',
  'oneOf',
  'properties',
  'required',
  'additionalProperties',
  'items',
  'enum',
  'const',
  'description',
  'title',
  'default',
  'examples',
])

function normalizeType(type) {
  if (typeof type === 'string') return type
  if (!Array.isArray(type)) return undefined
  const picked = type.find((t) => typeof t === 'string' && t !== 'null')
  return picked ?? 'string'
}

export function normalizeToolSchema(node) {
  if (node === null || typeof node !== 'object' || Array.isArray(node)) {
    return { type: 'object', properties: {} }
  }
  const out = {}
  for (const [key, value] of Object.entries(node)) {
    if (!ALLOWED.has(key)) continue
    if (key === 'type') {
      const type = normalizeType(value)
      if (type) out.type = type
    } else if (key === 'properties') {
      if (value && typeof value === 'object' && !Array.isArray(value)) {
        const props = {}
        for (const [name, schema] of Object.entries(value)) props[name] = normalizeToolSchema(schema)
        out.properties = props
      }
    } else if (key === 'items') {
      if (Array.isArray(value)) {
        // DSH 不支持 tuple；退化成第一项的 schema。
        out.items = value.length > 0 ? normalizeToolSchema(value[0]) : {}
      } else if (value && typeof value === 'object') {
        out.items = normalizeToolSchema(value)
      }
    } else if (key === 'oneOf') {
      if (Array.isArray(value)) out.oneOf = value.map((item) => normalizeToolSchema(item))
    } else if (key === 'additionalProperties') {
      out.additionalProperties = typeof value === 'object' && value !== null ? normalizeToolSchema(value) : value
    } else if (key === 'required') {
      if (Array.isArray(value)) out.required = value.filter((v) => typeof v === 'string')
    } else if (key === 'enum' || key === 'examples') {
      if (Array.isArray(value)) out[key] = value
    } else {
      out[key] = value
    }
  }
  if (out.type === 'object' && out.properties === undefined) out.properties = {}
  return out
}
