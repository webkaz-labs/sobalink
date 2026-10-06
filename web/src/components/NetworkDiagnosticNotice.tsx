import type { Locale, State } from '../api'
import { errorText, type Translate } from '../i18n'
import { networkGuidanceAction, readNetworkGuidance } from '../network-guidance'
import { Button, Icon } from './ui'

export function NetworkDiagnosticNotice({ self, locale, t, stale, reviewNetwork, reviewCapacity, refresh }: {
  self: State['self']; locale: Locale; t: Translate; stale: boolean
  reviewNetwork: () => void; reviewCapacity: () => void; refresh: () => void
}) {
  if (stale) return null
  const guidance = readNetworkGuidance(self.guidance)
  if (!guidance && !self.error) return null
  const action = guidance?.action || 'review_network'
  const callback = action === 'review_capacity' ? reviewCapacity : action === 'refresh_state' ? refresh : reviewNetwork
  return <div className="stale-banner network-diagnostic" role="status"><Icon name="info" /><div className="error-copy"><p><strong>{guidance?.summary[locale] || (self.errorCode ? errorText({ code: self.errorCode }, t) : t('networkProblem'))}</strong></p>{guidance && <p>{guidance.nextSteps[locale]}</p>}<details><summary>{t('technicalDetails')}</summary><p className="code-value">{guidance?.code || self.errorCode}</p>{self.error && <p>{self.error}</p>}</details></div>{action !== 'wait' && <Button onClick={callback}>{networkGuidanceAction(locale, action)}</Button>}</div>
}
