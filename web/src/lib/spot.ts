// 聚光（R-215）的样式规则：当前聚光键对应的一条选择器，其余带 data-spot-key 的元素淡出。
// 键是会话键（如 claude:8174cbef-…），写进属性选择器的引号里，引号和反斜杠要转义。
export function spotRule(key: string | null): string {
  if (!key) return ''
  const k = key.replace(/["\\]/g, '\\$&')
  const other = `[data-spot-key]:not([data-spot-key="${k}"])`
  return `${other}{opacity:.32;filter:saturate(.5);transition-delay:90ms}${other} .desk-lamp{animation-play-state:paused}`
}
