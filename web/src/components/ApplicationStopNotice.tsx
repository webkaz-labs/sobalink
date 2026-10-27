import type { Locale } from '../api'
import type { ApplicationStopPhase } from '../useServer'
import { lanTranslator } from '../lan-i18n'

// The owner acknowledges cancellation before draining HTTP and closing Core.
// A browser can observe loss of contact, but cannot attest to process exit.
export function ApplicationStopNotice({ phase, locale }: { phase: ApplicationStopPhase; locale: Locale }) {
  const lt = lanTranslator(locale)
  return <div className="scope-note" role="status"><div><p>{lt(phase === 'disconnected' ? 'stopDisconnected' : phase === 'unconfirmed' ? 'stopUnconfirmed' : 'stopping')}</p><p>{lt('stopRecovery')}</p></div></div>
}
