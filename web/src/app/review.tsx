import { t } from '../i18n'

// 待验收需求的就地验收卡（ReviewRow / ReviewList）已经去掉（R-177）：验收只在右侧的审批抽屉里做，
// 列表、工作台只放固定大小的条目（app/asks.tsx）。这里只留证据类型的中文名。

// evidenceKind：证据类型的中文名（agent 上报的是 test、screenshot 等英文标识）。
export const evidenceKind = (k: string) => t.evidenceKind[k.toLowerCase()] ?? k
