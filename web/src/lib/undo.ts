// 撤销期的时间换算（R-218）：一律从排上那一刻的 performance.now()（start）算起，和圆环的 WAAPI startTime 同一个起点。

// progressAt：走了多少（0～1）
export const progressAt = (start: number, now: number, ms: number) => Math.min(1, Math.max(0, (now - start) / ms))

// secsAt：原句里的「n 秒后」——向上取整，最少 1
export const secsAt = (start: number, now: number, ms: number) => Math.max(1, Math.ceil((start + ms - now) / 1000))
