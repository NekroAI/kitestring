// 设备路径的显示：设备可能是 macOS、Linux 或 Windows（路径写法不同）。

// shortPath 把家目录替换为 ~（含 Windows 的 C:\Users\<名字>），过长时保留末尾。
export function shortPath(p: string, max = 48): string {
  let s = p.replace(/^\/(Users|home)\/[^/]+/, '~').replace(/^[A-Za-z]:\\Users\\[^\\]+/i, '~')
  if (s.length > max) s = '…' + s.slice(s.length - max + 1)
  return s
}

// pathBase 取路径最后一段：/ 与 \ 都认（Windows 设备的路径）。
export const pathBase = (p: string): string => p.split(/[\\/]/).filter(Boolean).at(-1) || ''
