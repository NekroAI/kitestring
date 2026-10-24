// headlineOf：一句话结论；老数据没有时取第一句，截到 40 字。
export function headlineOf(headline: string | undefined, text: string | undefined): string {
  const h = (headline ?? '').trim()
  if (h) return h
  const first = ((text ?? '').trim().split(/(?<=[。！？!?；;\n])/)[0] ?? '').trim()
  const chars = [...first]
  return chars.length > 40 ? chars.slice(0, 40).join('') + '…' : first
}
