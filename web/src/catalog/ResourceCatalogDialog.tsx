import { useEffect, useState } from 'react'
import type { Locale } from '../api'
import type { Translate } from '../i18n'
import { Button, Modal } from '../components/ui'
import { readID } from '../resource/decode'
import { ResourceCatalogController } from './controller'
import { integer } from './decode'
import { catalogText, type CatalogTextKey } from './i18n'
import type { CatalogRow, SourceRequest, Workflow } from './types'
import { useResourceCatalog } from './useResourceCatalog'
import { workflows } from './workflow'
import './catalog.css'
export function ResourceCatalogDialog({ controller, locale, t, onClose, onNavigate, onOpenServiceConnections, navigating = false }: { controller: ResourceCatalogController; locale: Locale; t: Translate; onClose: () => void; onNavigate: (workflow: Workflow) => void; onOpenServiceConnections?: (peerId: string, contextRevision: string) => void; navigating?: boolean }) {
  const view = useResourceCatalog(controller), text = (key: CatalogTextKey) => catalogText(locale, key)
  const [categories, setCategories] = useState({ local_settings: true, local_service: true, transfer_activity: true })
  const [target, setTarget] = useState(''), [peer, setPeer] = useState(''), [remoteSettings, setRemoteSettings] = useState(false), [remoteServices, setRemoteServices] = useState(false)
  const [protocol, setProtocol] = useState<'1' | '2'>('1'), [resourceId, setResourceId] = useState(''), [grantId, setGrantId] = useState(''), [grantRevision, setGrantRevision] = useState('')
  useEffect(() => { controller.open(); return () => controller.close() }, [controller])
  useEffect(() => { setTarget(''); setPeer(''); setRemoteSettings(false); setRemoteServices(false); setResourceId(''); setGrantId(''); setGrantRevision('') }, [view.context.revision])
  const edit = (change: () => void) => { controller.invalidateSelection(); change() }
  const refresh = () => {
    const sources: SourceRequest[] = []
    try {
      if (categories.local_settings) sources.push({ kind: 'local_settings', target: { schemaVersion: 1, resourceId: readID(target) } })
      if (categories.local_service) sources.push({ kind: 'local_service' })
      if (categories.transfer_activity) sources.push({ kind: 'transfer_activity' })
      if (remoteServices) sources.push({ kind: 'remote_service', peerId: peer })
      if (remoteSettings) {
        if (!/^[1-9][0-9]{0,15}$/.test(grantRevision)) throw new Error('Invalid revision')
        sources.push({ kind: protocol === '1' ? 'remote_settings_v1' : 'remote_settings_v2', peerKey: peer, target: { schemaVersion: 1, resourceId: readID(resourceId) }, grantId: readID(grantId), grantRevision: integer(Number(grantRevision), 1) })
      }
      if (controller.select({ schemaVersion: 1, sources })) void controller.refresh()
    } catch { controller.select(null) }
  }
  const busy = view.busy !== null || navigating
  const peers = [...view.context.peers, ...view.context.managedKeys.filter(id => !view.context.peers.some(peer => peer.id === id)).map(id => ({ id, name: '' }))]
  const rowValues = (row: CatalogRow) => {
    if ('localService' in row) return <p>{row.localService.name} · {text(row.localService.direction)} · {row.localService.network.toUpperCase()} {row.localService.ports} · {text(row.localService.lifetime || 'finite')} · {text(row.localService.state)}</p>
    if ('remoteService' in row) return <><p>{row.remoteService.purpose || 'generic'} · {row.remoteService.network.toUpperCase()} {row.remoteService.ports} · {text(row.remoteService.lifetime || 'finite')}</p><p>{text('discoveryHint')}</p></>
    if ('transferActivity' in row) return <p>{text(row.identity.direction === 'incoming' ? 'incoming' : 'outgoing')} · {row.transferActivity.peerId} · {text(row.transferActivity.state)} · {text('bytes')}: {row.transferActivity.completedBytes} / {row.transferActivity.totalBytes}</p>
    const values = 'localSettings' in row ? row.localSettings : 'remoteSettingsV1' in row ? row.remoteSettingsV1 : row.remoteSettingsV2
    return <dl>{(['transferConcurrentFiles', 'transferConcurrentPerPeer'] as const).map(field => <div key={field}><dt>{text(field === 'transferConcurrentFiles' ? 'files' : 'perPeer')}</dt><dd>{text('requested')}: {values.requested[field].mode === 'default' ? text('default') : values.requested[field].value} · {text('effective')}: {values.effective[field]}</dd></div>)}</dl>
  }
  return <Modal title={text('title')} t={t} onClose={onClose} wide><div className="resource-catalog"><p>{text('intro')}</p>
    {!view.context.available ? <p role="status">{text('contextUnavailable')}</p> : <>
      <fieldset disabled={busy}><legend>{text('scope')}</legend>{(['local_settings', 'local_service', 'transfer_activity'] as const).map(kind => <label key={kind}><input type="checkbox" checked={categories[kind]} onChange={event => edit(() => setCategories({ ...categories, [kind]: event.target.checked }))} />{text(kind)}</label>)}
        {categories.local_settings && <div><Button onClick={() => { setTarget(''); void controller.loadLocal() }}>{text('loadLocal')}</Button><label>{text('chooseTarget')}<select value={target} onChange={event => edit(() => setTarget(event.target.value))}><option value="">{text('chooseTarget')}</option>{view.localResources?.map(resource => <option key={resource.resourceId} value={resource.resourceId}>{resource.resourceId}</option>)}</select></label>{!target && <p role="status">{text('unresolved')}</p>}</div>}
      </fieldset>
      <details><summary>{text('remote')}</summary><fieldset disabled={busy}><p>{text('remoteHint')}</p><label>{text('peer')}<select value={peer} onChange={event => edit(() => setPeer(event.target.value))}><option value="">{text('choosePeer')}</option>{peers.map(item => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label>
        {onOpenServiceConnections && <div><Button disabled={!view.context.peers.some(item => item.id === peer)} onClick={() => onOpenServiceConnections(peer, view.context.revision)}>{text('openServiceConnections')}</Button><p className="small muted">{text('genericServiceNavigation')}</p></div>}
        <label><input type="checkbox" checked={remoteServices} onChange={event => edit(() => setRemoteServices(event.target.checked))} />{text('remoteServices')}</label><label><input type="checkbox" checked={remoteSettings} onChange={event => edit(() => setRemoteSettings(event.target.checked))} />{text('remoteSettings')}</label>
        {remoteSettings && <><label>{text('protocol')}<select value={protocol} onChange={event => edit(() => setProtocol(event.target.value === '2' ? '2' : '1'))}><option value="1">{text('remote_settings_v1')}</option><option value="2">{text('remote_settings_v2')}</option></select></label>{([{ key: 'resourceId', value: resourceId, change: setResourceId }, { key: 'grantId', value: grantId, change: setGrantId }, { key: 'grantRevision', value: grantRevision, change: setGrantRevision }] as const).map(field => <label key={field.key}>{text(field.key)}<input autoComplete="off" spellCheck={false} maxLength={field.key === 'grantRevision' ? 16 : 32} value={field.value} onChange={event => edit(() => field.change(event.target.value))} /></label>)}</>}
      </fieldset></details>
      <div className="catalog-actions"><Button disabled={busy} onClick={refresh}>{text('refresh')}</Button>{view.busy && <Button onClick={() => controller.stopWaiting()}>{text('stop')}</Button>}</div>
      {view.busy && <p role="status">{t('loading')}</p>}{view.notice && <p role="alert">{text(view.notice)}</p>}{view.observation === 'stale' && <p role="status">{text('stale')}</p>}
      {view.candidate ? <><p>{text(view.candidate.snapshot.complete ? 'complete' : 'partial')}</p>{view.candidate.snapshot.sources.map(source => <section key={source.selection.sourceId} aria-label={text(source.selection.kind)}><h3>{text(source.selection.kind)}</h3><p>{text(source.state)} · {text('count')}: {source.rows.length} / {source.total ?? text('unknown')}</p>{source.checkedAt > 0 && <p>{text('checkedAt')}: <time dateTime={new Date(source.checkedAt).toISOString()}>{new Date(source.checkedAt).toLocaleString(locale)}</time></p>}{source.complete && source.rows.length === 0 && <p>{text('empty')}</p>}{source.rows.map(row => <article key={JSON.stringify(row.identity)}><p>{text(row.identity.lifetime)}: <span className="catalog-identity">{row.identity.id}</span></p>{rowValues(row)}<div className="catalog-actions">{workflows(source, row).map(workflow => <Button key={workflow.kind} disabled={busy || view.observation !== 'current'} onClick={() => onNavigate(workflow)}>{text(workflow.kind)}</Button>)}</div></article>)}</section>)}</> : <p>{text('unread')}</p>}
      <p className="small muted">{text('actionHint')}</p><p className="small muted">{text('limitsHint')}</p><p className="small muted">{text('stoppedHint')}</p>
    </>}<div className="modal-actions"><Button onClick={onClose}>{t('close')}</Button></div></div></Modal>
}
