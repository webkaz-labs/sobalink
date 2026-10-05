import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import type { CommandPayloads, Locale, ServiceConfigResult, ServiceConfiguration } from '../api'
import { savedEditorText } from '../saved-editor-i18n'
import { serviceText } from '../service-i18n'
import { errorText, type Translate } from '../i18n'
import { previewPorts } from '../ports'
import { MAX_SERVICE_TTL_SECONDS, loopbackEndpoint, readServiceConfig, validLifetime, validServiceName, type SavedServiceAction, type ServiceMode } from '../service-form'
import type { Server } from '../useServer'
import { Button, ErrorBanner, Modal, useAlive } from './ui'
type DefinitionDraft = Omit<ServiceConfiguration, 'id'> & { id?: string }
const peerID = /^[a-zA-Z0-9][a-zA-Z0-9:_-]{0,127}$/
export function SavedDefinitionEditor({ server, locale, t, onClose, onSaved, mode, source, services }: { server: Server; locale: Locale; t: Translate; onClose: () => void; onSaved: () => void; mode: ServiceMode; source?: SavedServiceAction; services: ServiceConfiguration[] }) {
  const e = (key: string) => savedEditorText(locale, key)
  const s = (key: string) => serviceText(locale, key)
  const alive = useAlive()
  const [record, setRecord] = useState<ServiceConfigResult>()
  const [draft, setDraft] = useState<DefinitionDraft>({ name: '', direction: mode === 'connect' ? 'forward' : 'share', backend: server.state?.settings?.network === 'none' ? '' : server.state?.settings?.network || '', network: 'tcp', ports: '', excludePorts: mode === 'share' ? '54543-54545' : '', loopbackHost: '127.0.0.1', lifetime: mode === 'connect' ? 'until-stopped' : 'finite', ttlSeconds: mode === 'connect' ? 0 : 3600, purpose: 'generic', discoverable: false })
  const [ids, setIDs] = useState('')
  const [review, setReview] = useState<CommandPayloads['service.save']>()
  const [reload, setReload] = useState(0)
  const [loading, setLoading] = useState(Boolean(source))
  const readID = useRef(crypto.randomUUID())
  useEffect(() => {
    if (!source) return
    let cancelled = false; setLoading(true); setRecord(undefined); setReview(undefined)
    void Promise.resolve().then(() => cancelled ? undefined : server.run('service.config', { id: source.id }, `definition.config:${readID.current}:${reload}`)).then(result => {
      if (cancelled) return
      if (result) {
        try {
          const saved = readServiceConfig(result.result, source.id, mode)
          const configuration: DefinitionDraft = { ...saved.configuration }
          if (source.intent === 'copy') {
            delete configuration.id
            const base = configuration.name
            let suffix = 2
            while (services.some(service => service.name === configuration.name)) configuration.name = `${base.slice(0, 60)}-${suffix++}`
          }
          setRecord(saved); setDraft(configuration); setIDs(mode === 'connect' ? configuration.peerId || '' : (configuration.peerIds || []).join(', '))
        } catch { server.setError({ code: 'invalid_response' }) }
      }
      setLoading(false)
    })
    return () => { cancelled = true }
  }, [source?.id, source?.intent, mode, reload, server.run])
  const selectedIDs = ids.split(',').map(id => id.trim()).filter(Boolean)
  const configuration: DefinitionDraft = { ...draft, peerId: mode === 'connect' ? selectedIDs[0] : undefined, peerIds: mode === 'share' ? selectedIDs : undefined }
  const preview = useMemo(() => { try { return previewPorts(draft.ports, draft.excludePorts || '', draft.localPort ? String(draft.localPort) : '', mode, [], { protocol: draft.network, maxListeners: 65535 }) } catch { return null } }, [draft.ports, draft.excludePorts, draft.localPort, draft.network, mode])
  const duplicateName = services.some(service => service.name === draft.name && service.id !== draft.id)
  const valid = validServiceName(draft.name) && !duplicateName && ['tailnet', 'lan', 'direct-lan', 'mixed'].includes(draft.backend || '') && selectedIDs.length > 0 && (mode !== 'connect' || selectedIDs.length === 1) && selectedIDs.every(id => peerID.test(id)) && new Set(selectedIDs).size === selectedIDs.length && validLifetime(draft.lifetime || 'finite', draft.ttlSeconds, mode) && Boolean(preview)
  const active = source?.intent === 'edit' && (record?.active || [...(server.state?.services || []), ...(server.state?.shares || [])].some(service => service.id === source.id && ['active', 'reconnecting'].includes(service.status)))
  const busy = server.busy.has('service.save') || loading
  const code = (server.error as { code?: string })?.code || ''
  const revisionConflict = ['service_revision_conflict', 'service_not_found', 'service_backend_mismatch'].includes(code)
  const blocked = busy || server.stale || active || revisionConflict
  const change = (patch: Partial<DefinitionDraft>) => { setDraft(current => ({ ...current, ...patch })); setReview(undefined) }
  const prepare = (event: FormEvent) => { event.preventDefault(); if (!valid || blocked || source && !record) return; setReview({ configuration: structuredClone(configuration), ...(source?.intent === 'edit' ? { expectedRevision: record!.revision } : {}) }) }
  const save = async () => {
    if (!review || blocked) return
    const checked = review; setReview(undefined)
    const response = await server.run('service.save', checked)
    if (!response || !alive.current) return
    const value = response.result as unknown as ServiceConfigResult
    if (value?.active !== false || !value.configuration?.id || !/^[0-9a-f]{64}$/.test(value.revision)) { server.setError({ code: 'invalid_response' }); return }
    onSaved()
  }
  return <Modal title={e(source?.intent === 'edit' ? 'edit' : source?.intent === 'copy' ? 'copy' : mode === 'connect' ? 'createForward' : 'createShare')} t={t} onClose={onClose} wide><p>{e('intro')}</p>{server.error != null && <ErrorBanner message={s(code) || errorText(server.error, t)} t={t} />}{source && !record ? <><p role="status">{t(loading ? 'loading' : 'unavailable')}</p>{!loading && <Button onClick={() => { server.setError(null); setReload(value => value + 1) }}>{e('reload')}</Button>}</> : <form className="form-stack" onSubmit={prepare}>
    {active && <p className="scope-note">{e('active')}</p>}<label className="field">{t('ruleName')}<input value={draft.name} maxLength={64} required disabled={busy} onChange={event => change({ name: event.target.value })} /></label><label className="field">{e('backend')}<select value={draft.backend || ''} disabled={busy || source?.intent === 'edit' && Boolean(record?.configuration.backend)} onChange={event => change({ backend: event.target.value as 'tailnet' | 'lan' | 'direct-lan' | 'mixed' | '' })}><option value="">{e('chooseBackend')}</option><option value="tailnet">Tailnet</option><option value="lan">LAN</option><option value="direct-lan">{t('direct-lan')}</option><option value="mixed">{t('mixed')}</option></select><small>{e('backendHint')}</small></label>
    <label className="field">{e('peers')}<input value={ids} required disabled={busy || Boolean(draft.serviceId)} onChange={event => { setIDs(event.target.value); setReview(undefined) }} autoComplete="off" spellCheck={false} /><small>{e('peerHint')}</small></label>{!draft.serviceId && <select aria-label={e('addPeer')} value="" disabled={busy} onChange={event => { setIDs(mode === 'connect' ? event.target.value : [...new Set([...selectedIDs, event.target.value])].join(', ')); setReview(undefined) }}><option value="">{e('addPeer')}</option>{server.state?.peers.filter(peer => peer.networks.includes(draft.backend as 'lan' | 'tailnet' | 'direct-lan' | 'mixed')).map(peer => <option key={peer.id} value={peer.id}>{peer.name} · {peer.id}</option>)}</select>}{selectedIDs.some(id => !server.state?.peers.some(peer => peer.id === id)) && <p className="scope-note">{e('unavailable')}</p>}
    <div className="form-row"><label className="field grow">{t('ports')}<input value={draft.ports} required disabled={busy || Boolean(draft.serviceId)} onChange={event => change({ ports: event.target.value })} spellCheck={false} /></label><label className="field">{t('protocol')}<select value={draft.network} disabled={busy || Boolean(draft.serviceId)} onChange={event => change({ network: event.target.value as 'tcp' | 'udp' })}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label></div><label className="field">{t('excludePorts')}<input value={draft.excludePorts || ''} disabled={busy || Boolean(draft.serviceId)} onChange={event => change({ excludePorts: event.target.value })} /></label><div className="form-row"><label className="field grow">{s('loopbackHost')}<select value={draft.loopbackHost || '127.0.0.1'} disabled={busy} onChange={event => change({ loopbackHost: event.target.value as '127.0.0.1' | '::1' })}><option>127.0.0.1</option><option>::1</option></select></label><label className="field grow">{mode === 'connect' ? t('localStart') : s('shareLocalPort')}<input type="number" min={mode === 'connect' ? 1024 : 1} max="65535" value={draft.localPort || ''} disabled={busy} onChange={event => change({ localPort: event.target.value ? Number(event.target.value) : undefined })} /></label></div>
    <label className="field">{s('lifetime')}<select value={draft.lifetime || 'finite'} disabled={busy} onChange={event => change({ lifetime: event.target.value as DefinitionDraft['lifetime'], ttlSeconds: event.target.value === 'finite' ? 3600 : 0 })}><option value="finite">{s('customDuration')}</option><option value={mode === 'connect' ? 'until-stopped' : 'until-revoked'}>{s(mode === 'connect' ? 'untilStopped' : 'untilRevoked')}</option></select></label>{draft.lifetime === 'finite' && <label className="field">{e('duration')}<input type="number" min="1" max={MAX_SERVICE_TTL_SECONDS} step="1" disabled={busy} required value={draft.ttlSeconds || ''} onChange={event => change({ ttlSeconds: Number(event.target.value) })} /></label>}<label className="field">{e('purpose')}<input value={draft.purpose} disabled={busy || Boolean(draft.serviceId)} onChange={event => change({ purpose: event.target.value })} /></label>{mode === 'share' && <label className="checkbox-field"><input type="checkbox" checked={draft.discoverable} disabled={busy} onChange={event => change({ discoverable: event.target.checked })} /><span>{t('discoverable')}<small>{t('discoverableHint')}</small></span></label>}
    {draft.serviceId && <div className="scope-note"><p>{e('historical')}</p><p className="code-value">{draft.serviceId}</p><p>{e('manualHint')}</p><Button type="button" disabled={busy} onClick={() => change({ serviceId: undefined, serviceRevision: undefined })}>{e('manual')}</Button></div>}{!valid && draft.name && <p className="field-error">{duplicateName ? s('service_name_conflict') : e('invalid')}</p>}
    {review ? <section className="service-preview" aria-label={e('reviewTitle')}><h3>{e('reviewTitle')}</h3><dl><dt>{t('ruleName')}</dt><dd>{configuration.name}</dd><dt>{e('backend')}</dt><dd>{configuration.backend} · {configuration.network.toUpperCase()}</dd><dt>{e('peers')}</dt><dd className="code-value">{ids}</dd><dt>{t('ports')}</dt><dd>{configuration.ports}</dd><dt>{t('excludePorts')}</dt><dd>{configuration.excludePorts || '—'}</dd><dt>{t('mapping')}</dt><dd className="code-value">{mode === 'connect' ? `${loopbackEndpoint(configuration.loopbackHost || '127.0.0.1', preview?.localPorts || '')} → ${preview?.ports}` : `${preview?.ports} → ${loopbackEndpoint(configuration.loopbackHost || '127.0.0.1', preview?.localPorts || '')}`}</dd><dt>{s('lifetime')}</dt><dd>{configuration.lifetime === 'finite' ? `${configuration.ttlSeconds} ${s('customLifetime')}` : s(configuration.lifetime === 'until-revoked' ? 'untilRevoked' : 'untilStopped')}</dd><dt>{e('purpose')}</dt><dd>{configuration.purpose}</dd>{mode === 'share' && <><dt>{t('discoverable')}</dt><dd>{t(configuration.discoverable ? 'enabled' : 'off')}</dd></>}</dl><p>{e('intro')}</p><div className="modal-actions"><Button type="button" onClick={() => setReview(undefined)}>{e('back')}</Button><Button type="button" variant="primary" disabled={blocked || !valid} onClick={save}>{e('confirm')}</Button></div></section> : <div className="modal-actions"><Button type="button" onClick={onClose}>{t('cancel')}</Button>{source && <Button type="button" disabled={busy} onClick={() => { server.setError(null); setReload(value => value + 1) }}>{e('reload')}</Button>}<Button type="submit" variant="primary" disabled={blocked || !valid}>{e('review')}</Button></div>}
    </form>}</Modal>
}
