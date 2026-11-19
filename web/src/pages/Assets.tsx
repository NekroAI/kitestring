import { AssetsPage } from '../app/assets'
import { BackLink } from '../app/shares'
import { t } from '../i18n'

// 资源库（R-351）：/assets 图片墙，?asset=A-x 打开素材抽屉。手机从设置页进入。
export default function Assets() {
  return (
    <>
      <BackLink to="/settings" label={t.nav.settings} className="lg:hidden" />
      <AssetsPage />
    </>
  )
}
