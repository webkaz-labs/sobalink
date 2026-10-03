import { useEffect, useRef, useState } from 'react'
import type { Locale, ServiceConfiguration, StartupEntry, StartupList, StartupReview } from '../api'
import { startupText } from '../startup-i18n'
import { readStartupList, readStartupReview } from '../startup-proxy'
import { definitionText } from '../definitions-i18n'
import { serviceText } from '../service-i18n'
import { errorText, type Translate } from '../i18n'
import { loopbackEndpoint, validServiceName } from '../service-form'
import { previewPorts } from '../ports'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Modal, useAlive } from './ui'
export function StartupScope({ services, locale, t }: { services: ServiceConfiguration[]; locale: Locale; t: Translate }) {
  const d = (key: string) => definitionText(locale, key)
  return <div>{services.map(service => {
    let mappings: { local: string; remote: string }[] = []
    try { mappings = previewPorts(service.ports, service.excludePorts || '', service.localPort ? String(service.localPort) : '', 'connect', [], { protocol: service.network, maxListeners: 65535 }).mappings } catch { /* Core will reject invalid definitions at start. */ }
    return <article className="definition-scope" key={service.id}><strong>{service.name}</strong><dl><div><dt>{d('backend')}</dt><dd>{service.backend || t('unknown')} · {service.network.toUpperCase()}</dd></div><div><dt>{d('peers')}</dt><dd className="code-value">{service.peerId}</dd></div><div><dt>{t('ports')}</dt><dd className="code-value">{service.ports}</dd></div>{service.excludePorts && <div><dt>{t('excludePorts')}</dt><dd className="code-value">{service.excludePorts}</dd></div>}<div><dt>{t('purpose')}</dt><dd>{service.purpose}</dd></div><div><dt>{t('mapping')}</dt><dd>{mappings.length ? mappings.map(mapping => <p className="code-value" key={mapping.remote}>{loopbackEndpoint(service.loopbackHost || '127.0.0.1', mapping.local)} → {mapping.remote}</p>) : d('mappingInvalid')}</dd></div><div><dt>{d('lifetime')}</dt><dd>{!service.lifetime || service.lifetime === 'finite' ? `${service.ttlSeconds.toLocaleString(locale)} ${d('seconds')}` : serviceText(locale, 'untilStopped')}</dd></div>{service.serviceId && <div><dt>{d('remoteReference')}</dt><dd className="code-value">{service.serviceId}</dd></div>}</dl></article>
  })}</div>
}
export function StartupProfilesDialog({ server, locale, t, onClose, target, selected }: { server: Server; locale: Locale; t: Translate; onClose: () => void; target: { ids?: string[]; group?: string }; selected: ServiceConfiguration[] }) {
  const s = (key: string) => startupText(locale, key)
  const alive = useAlive()
  const [list, setList] = useState<StartupList>()
  const [name, setName] = useState(target.group || '')
  const [review, setReview] = useState<StartupReview>()
  const [disabling, setDisabling] = useState<{ entry: StartupEntry; revision: string }>()
  const [replace, setReplace] = useState(false)
  const [working, setWorking] = useState(false)
  const [mutating, setMutating] = useState(false)
  const [status, setStatus] = useState('')
  const [reload, setReload] = useState(0)
  const pending = useRef(false)
  const generation = useRef(0)
  const readID = useRef(crypto.randomUUID())
  const targetKey = JSON.stringify(target)
  const currentTarget = useRef(targetKey); currentTarget.current = targetKey
  useEffect(() => { setReview(undefined); setReplace(false); ++generation.current }, [targetKey])
  useEffect(() => {
    let cancelled = false; setWorking(true); setList(undefined); setReview(undefined); setDisabling(undefined)
    void Promise.resolve().then(() => cancelled ? undefined : server.run('startup.list', {}, `startup.list:${readID.current}:${reload}`)).then(result => {
      if (cancelled) return
      if (result) { try { setList(readStartupList(result.result)) } catch { server.setError({ code: 'invalid_response' }) } }
      setWorking(false)
    })
    return () => { cancelled = true }
  }, [reload, server.run])
  useEffect(() => {
    const snapshot = server.state?.startup
    if (!snapshot) return
    try { setList(readStartupList(snapshot)) } catch { return }
    if (review && review.storeRevision !== snapshot.revision || disabling && disabling.revision !== snapshot.revision) { ++generation.current; setReview(undefined); setDisabling(undefined); setStatus('scopeChanged') }
  }, [server.state?.startup])
  const blocked = working || server.stale
  const canApprove = selected.length > 0 && selected.every(service => service.direction === 'forward')
  const prepare = async () => {
    if (blocked || pending.current || !canApprove || !validServiceName(name) || !list) return
    const selection = { ...target, name }; const scope = currentTarget.current; const version = ++generation.current
    setStatus(''); setReview(undefined); setReplace(false); setWorking(true); pending.current = true
    const result = await server.run('startup.preview', selection)
    if (alive.current && scope === currentTarget.current && version === generation.current && result) { try { setReview(readStartupReview(result.result, selection)) } catch { server.setError({ code: 'invalid_response' }) } }
    pending.current = false; if (alive.current) { setWorking(false); setMutating(false) }
  }
  const apply = async () => {
    if (blocked || pending.current || !review || list?.entries.some(entry => entry.name === review.name) && !replace) return
    const checked = review; setReview(undefined); setWorking(true); setMutating(true); pending.current = true
    const result = await server.run('startup.save', { name: checked.name, ...(checked.group ? { group: checked.group } : { ids: checked.ids }), expectedRevision: checked.revision, expectedStoreRevision: checked.storeRevision })
    if (alive.current && result) { try { const next = readStartupList(result.result); if (!next.entries.some(entry => entry.name === checked.name && entry.revision === checked.revision && entry.enabled && entry.valid)) throw new Error('invalid_response'); setList(next); setStatus('saved') } catch { server.setError({ code: 'invalid_response' }) } }
    pending.current = false; if (alive.current) { setWorking(false); setMutating(false) }
  }
  const disable = async () => {
    if (!disabling || blocked || pending.current) return
    const checked = disabling; setDisabling(undefined); setWorking(true); setMutating(true); pending.current = true
    const result = await server.run('startup.disable', { name: checked.entry.name, expectedStoreRevision: checked.revision })
    if (alive.current && result) { try { const next = readStartupList(result.result); if (next.entries.find(entry => entry.name === checked.entry.name)?.enabled !== false) throw new Error('invalid_response'); setList(next); setStatus('disabledDone') } catch { server.setError({ code: 'invalid_response' }) } }
    pending.current = false; if (alive.current) { setWorking(false); setMutating(false) }
  }
  const code = (server.error as { code?: string })?.code || ''
  return <Modal title={s('title')} t={t} onClose={onClose} wide><p>{s('intro')}</p>{server.error != null && <ErrorBanner message={s(code) || serviceText(locale, code) || errorText(server.error, t)} t={t} />}{status && <p role="status" className="scope-note">{s(status)}</p>}{list?.suppressed && <p className="scope-note">{s('suppressed')}</p>}{mutating && <p role="status">{s('operationPending')}</p>}
    {disabling ? <section aria-label={s('disableTitle')}><h3>{disabling.entry.name}</h3><p>{s('disableHint')}</p><StartupScope services={disabling.entry.services} locale={locale} t={t} /><div className="modal-actions"><Button type="button" onClick={() => setDisabling(undefined)}>{t('cancel')}</Button><Button type="button" variant="danger" disabled={blocked} onClick={() => void disable()}>{s('disableConfirm')}</Button></div></section> : review ? <section aria-label={s('scope')}><h3>{review.name}</h3><p>{s('effect')}</p><p>{s('frozen')}</p><p>{s('currentNetwork')}: {review.network} · {review.hostname}</p><StartupScope services={review.services} locale={locale} t={t} />{list?.entries.some(entry => entry.name === review.name) && <label className="checkbox-field"><input type="checkbox" checked={replace} onChange={event => setReplace(event.target.checked)} /><span>{s('replace')}</span></label>}<div className="modal-actions"><Button type="button" onClick={() => { ++generation.current; setReview(undefined) }}>{s('cancelReview')}</Button><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button type="button" variant="primary" disabled={blocked || Boolean(list?.entries.some(entry => entry.name === review.name) && !replace)} onClick={() => void apply()}>{s('confirm')}</Button></div></section> : <><section className="form-stack"><h3>{s('selection')}</h3>{!canApprove ? <p>{s('selectionRequired')}</p> : <><p>{selected.map(service => service.name).join(', ')}</p><label className="field">{s('name')}<input maxLength={64} value={name} disabled={blocked} onChange={event => { ++generation.current; setName(event.target.value); setReview(undefined); setReplace(false) }} /></label><Button type="button" disabled={blocked || !list || !validServiceName(name)} onClick={() => void prepare()}>{s('review')}</Button></>}<p className="small muted">{s('noAutostart')}</p></section><section className="subsection"><h3>{s('list')}</h3>{!list ? <p>{t(working ? 'loading' : 'unavailable')}</p> : list.entries.length === 0 ? <p>{s('none')}</p> : list.entries.map(entry => <article className="definition-scope" key={entry.name}><strong>{entry.name}</strong><p><Badge>{s(entry.enabled ? 'enabled' : 'disabled')}</Badge> · {s(entry.valid ? 'valid' : 'invalid')}</p><p>{s('lastAttempt')}: {s(entry.state === 'saved' ? 'savedState' : entry.state) || entry.state}</p><details><summary>{s('selection')}</summary><StartupScope services={entry.services} locale={locale} t={t} /></details><Button type="button" disabled={blocked || !entry.enabled} onClick={() => { server.setError(null); setStatus(''); setDisabling({ entry, revision: list.revision }) }}>{s('disable')}</Button></article>)}</section></>}
    <div className="modal-actions"><Button type="button" disabled={blocked} onClick={() => { server.setError(null); setStatus(''); ++generation.current; setReload(value => value + 1) }}>{s('reload')}</Button><Button type="button" onClick={onClose}>{t('close')}</Button></div></Modal>
}
