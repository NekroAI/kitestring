import { SecretsPage } from '../app/secrets'
import { BackLink } from '../app/shares'
import { t } from '../i18n'

// 凭据（R-295）：/secrets 列表，?k=凭据 ID 打开详情抽屉。手机从设置页进入。
export default function Secrets() {
  return (
    <>
      <BackLink to="/settings" label={t.nav.settings} className="lg:hidden" />
      <SecretsPage />
    </>
  )
}
