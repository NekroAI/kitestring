// headlineOf：一句话结论；老数据没有时取第一句，整句交给界面按可用宽度省略；只为防止异常长的句子，兜底截到 160 字。
export function headlineOf(headline: string | undefined, text: string | undefined): string {
  const h = (headline ?? '').trim()
  if (h) return h
  const first = ((text ?? '').trim().split(/(?<=[。！？!?；;\n])/)[0] ?? '').trim()
  const chars = [...first]
  return chars.length > 160 ? chars.slice(0, 160).join('') + '…' : first
}
