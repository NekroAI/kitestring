import { t } from '../i18n'
import { usePref } from '../lib/util'
import { Segmented } from '../ui/base'

// 最近动态「全部 / 只看我的」（R-296）：工作台和项目页共用一个本机偏好。
export const useRecentMine = () => usePref<boolean>('ks.recentMine', false)

// 条目多时才显示切换；已切到「只看我的」时一直显示，方便切回。
export const showRecentFilter = (n: number, mine: boolean) => mine || n >= 5

export function RecentFilter({ mine, onChange }: { mine: boolean; onChange: (mine: boolean) => void }) {
  return (
    <Segmented
      size="sm"
      value={mine ? 'mine' : 'all'}
      onChange={(v) => onChange(v === 'mine')}
      options={[
        { value: 'all', label: t.projects.recentAll },
        { value: 'mine', label: t.projects.recentMine },
      ]}
    />
  )
}
