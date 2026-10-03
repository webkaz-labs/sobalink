import type { Locale, Peer } from '../api'
import { discoveryText } from '../discovery-i18n'
import type { Translate } from '../i18n'
import type { Server } from '../useServer'
import { Button } from './ui'

export function DiscoveryObservation({ peer, locale, t, server }: { peer: Peer; locale: Locale; t: Translate; server: Server }) {
  const value = peer.discovery
  if (!value) return null
  const d = (key: string) => discoveryText(locale, key)
  const hint = value.state === 'confirmed' && value.services === 0 ? 'emptyHint' : `${value.state}Hint`
  return <section className="scope-note discovery-observation" aria-label={d('title')}><strong>{d(value.state)}</strong><p className="small">{d(hint)}</p>{value.state === 'confirmed' && value.services > 0 && <p className="small">{d('services')}: {value.services}</p>}{value.checkedAt && <p className="small muted">{d('checkedAt')}: <time dateTime={value.checkedAt}>{value.checkedAt}</time></p>}{value.code && <details><summary>{t('technicalDetails')}</summary><p className="code-value">{value.code}</p></details>}<Button type="button" variant="ghost" disabled={server.stale} busy={server.busy.has(`discovery:${peer.id}`)} onClick={() => server.run('discovery.refresh', { peerId: peer.id }, `discovery:${peer.id}`)}>{d('refresh')}</Button></section>
}
