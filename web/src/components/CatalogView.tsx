import { useEffect, useRef, useState } from 'react'
import type { Locale, Peer, Service, State } from '../api'
import { catalogViewError, catalogViewSelector, readCatalogView, type CatalogViewObservation } from '../catalog-view'
import { advertisedDraft, newServiceDraft, type ServiceDraft } from '../service-form'
import type { Translate } from '../i18n'
import { errorDetail, errorText } from '../i18n'
import type { Server } from '../useServer'
import { Button, useAlive } from './ui'
import { ServiceDialog } from './Dialogs'
export function CatalogView({ peer, state, server, locale, t }: { peer: Peer; state: State; server: Server; locale: Locale; t: Translate }) {
  const alive = useAlive()
  const ja = locale === 'ja'
  const [resourceId, setResourceId] = useState(''), [grantId, setGrantId] = useState(''), [revision, setRevision] = useState('')
  const [observation, setObservation] = useState<CatalogViewObservation | null>(null)
  const [selected, setSelected] = useState<Service | null>(null), [draft, setDraft] = useState<ServiceDraft>()
  const [busy, setBusy] = useState(false), [inFlight, setInFlight] = useState(false), [error, setError] = useState('')
  const pending = useRef(false), generation = useRef(0)
  const [loadedContext, setLoadedContext] = useState('')
  const context = `${peer.id}:${state.resourceCatalogProcessId}:${state.settings?.network}:${server.auth}:${server.stale}`
  const latestContext = useRef(context); latestContext.current = context
  const currentObservation = loadedContext === context && server.auth === 'ready' && !server.stale ? observation : null
  useEffect(() => { ++generation.current; pending.current = false; setInFlight(false); setBusy(false); setObservation(null); setSelected(null); setDraft(undefined); setError(''); return () => { ++generation.current } }, [context])
  const clear = () => { ++generation.current; setObservation(null); setSelected(null); setDraft(undefined); setError('') }
  const edit = (fn: () => void) => { clear(); fn() }
  const selector = catalogViewSelector(resourceId.trim(), grantId.trim(), revision.trim())
  const fetchList = async () => {
    if (pending.current || !selector || server.stale || server.auth !== 'ready') return
    clear(); server.setError(null); const current = generation.current, requestedContext = context; pending.current = true; setInFlight(true); setBusy(true)
    try {
      const result = await server.run('resource.catalog.view.list', { peerKey: peer.id, request: selector }, `catalog-view:${peer.id}`)
      if (!alive.current || current !== generation.current || requestedContext !== latestContext.current) return
      if (result) { setObservation(readCatalogView(result.result, peer.id, selector)); setLoadedContext(requestedContext) }
      else setError('')
    } catch { if (current === generation.current) setError(ja ? '一覧の応答を確認できません。再取得してください。' : 'The list response could not be verified. Fetch it again.') }
    finally { if (alive.current && requestedContext === latestContext.current) { pending.current = false; setInFlight(false); setBusy(false) } }
  }
  const open = (service: Service) => { if (!currentObservation || busy || server.stale) return; server.setError(null); setDraft({ ...newServiceDraft(peer, 'connect', state), ...advertisedDraft(service) }); setSelected(service) }
  return <section aria-label={ja ? '許可された共有一覧' : 'Permitted shared services'}><h3>{ja ? '許可された共有一覧' : 'Permitted shared services'}</h3><p>{ja ? `${peer.name} の一覧を閲覧します。閲覧許可は接続許可を与えません。` : `View services from ${peer.name}. Permission to view the list does not grant connection permission.`}</p><p className="small muted">{ja ? '提供側の catalog view inspect が返した対象 ID・許可 ID・現在の変更番号を指定してください。' : 'Use the resource ID, grant ID and current revision from catalog view inspect on the sharing device.'}</p><div className="form-stack"><label className="field">{ja ? '対象 ID' : 'Resource ID'}<input value={resourceId} disabled={inFlight} onChange={e => edit(() => setResourceId(e.target.value))} spellCheck={false} /></label><label className="field">{ja ? '閲覧許可 ID' : 'View grant ID'}<input value={grantId} disabled={inFlight} onChange={e => edit(() => setGrantId(e.target.value))} spellCheck={false} /></label><label className="field">{ja ? '許可の変更番号' : 'Grant revision'}<input value={revision} inputMode="numeric" disabled={inFlight} onChange={e => edit(() => setRevision(e.target.value))} /></label><Button busy={busy} disabled={inFlight || !selector || server.stale || server.auth !== 'ready'} onClick={() => void fetchList()}>{ja ? '一覧を取得' : 'Fetch services'}</Button>{busy && <Button onClick={() => { clear(); setBusy(false) }}>{ja ? '待機をやめる' : 'Stop waiting'}</Button>}</div>{(error || server.error != null) && <p role="alert">{error || catalogViewError(server.error, ja) || errorText(server.error, t)}{server.error != null && errorDetail(server.error, t) && <small>{errorDetail(server.error, t)}</small>}</p>}{currentObservation && <><p role="status">{currentObservation.services.length === 0 ? (ja ? '現在閲覧できる共有サービスはありません。' : 'No shared services are currently available to view.') : (ja ? '閲覧を許可された一覧' : 'Services permitted for viewing')}</p><ul className="available-services">{currentObservation.services.map(row => <li key={row.service.id}><div><strong>{row.service.purpose || 'generic'}</strong><p className="code-value">{row.service.network.toUpperCase()} {row.service.ports}</p><p>{ja ? '共有期限' : 'Shared service expiry'}: {row.service.expiresAt || (ja ? '取消まで' : 'Until revoked')}</p><small>{ja ? '共有 ID' : 'Shared service ID'}: {row.service.id}</small><p>{row.connectAllowed ? (ja ? '接続前に現在の許可を再確認します。' : 'Current permission is checked again before connecting.') : (ja ? '閲覧のみ・接続許可なし' : 'View only; connection is not permitted')}</p></div><Button disabled={!row.connectAllowed || busy} onClick={() => open(row.service)}>{ja ? '接続をレビュー' : 'Review connection'}</Button></li>)}</ul><Button onClick={clear}>{ja ? '一覧を閉じる' : 'Close list'}</Button></>}{selected && currentObservation && <ServiceDialog mode="connect" peer={peer} state={{ ...state, availableServices: [selected] }} locale={locale} t={t} server={server} draft={draft} onDraft={setDraft} onClose={() => { setSelected(null); setDraft(undefined) }} onStarted={clear} catalogView={{ reviewRevision: currentObservation.reviewRevision, serviceId: selected.id, onConsumed: clear }} />}</section>
}
