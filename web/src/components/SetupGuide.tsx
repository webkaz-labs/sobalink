import type { Locale, State } from '../api'
import type { Translate } from '../i18n'
import { setupNetworkStage, setupText } from '../setup-guide'
import { Button, Icon } from './ui'

// Navigation only: no enrollment, connection starts or probes on render.
export function SetupGuide({ state, stale, stopping, locale, t, network, saved, devices, refresh }: {
  state: State; stale: boolean; stopping: boolean; locale: Locale; t: Translate
  network: () => void; saved: () => void; devices: () => void; refresh: () => void
}) {
  const stage = setupNetworkStage(state, stale)
  const s = (key: Parameters<typeof setupText>[1]) => setupText(locale, key)
  return <section className="setup-guide" aria-label={s('title')}>
    <h3>{s('title')}</h3>
    <ol>
      <li><span className="setup-step" aria-hidden="true">1</span><div><h4>{s('network')}</h4><p>{s(stage)}</p><Button disabled={stopping} onClick={stage === 'stale' ? refresh : network}><Icon name={stage === 'stale' ? 'refresh' : 'plus'} size={16} />{stage === 'stale' ? s('refresh') : t('addDevice')}</Button></div></li>
      <li><span className="setup-step" aria-hidden="true">2</span><div><h4>{s('choose')}</h4><p>{s('chooseHint')}</p><div className="setup-actions"><Button disabled={stale || stopping || state.peers.length === 0} onClick={devices}>{s('devices')}</Button><Button disabled={stale || stopping} onClick={saved}>{s('saved')}</Button></div></div></li>
      <li><span className="setup-step" aria-hidden="true">3</span><div><h4>{s('check')}</h4><p>{s('checkHint')}</p></div></li>
    </ol><p className="small muted">{s('boundary')}</p>
  </section>
}
