import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import type { ClientNotice, ClientSettingsView, Locale, RustDeskClientSettings, RustDeskSetup, RustDeskSetupReview, ServiceLifetime } from '../api'
import { clientText } from '../client-i18n'
import { readClientSettings, readRustDeskReview, validRustDeskSetup } from '../client-helpers'
import { errorText, type Translate } from '../i18n'
import { serviceText } from '../service-i18n'
import { MAX_SERVICE_TTL_SECONDS } from '../service-form'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Modal, useAlive } from './ui'
function lifetimeText(locale: Locale, lifetime: ServiceLifetime, seconds: number) { return lifetime === 'finite' ? `${seconds.toLocaleString(locale)} ${serviceText(locale, 'customLifetime')}` : serviceText(locale, lifetime === 'until-revoked' ? 'untilRevoked' : 'untilStopped') }
function Notices({ notices, locale }: { notices: ClientNotice[]; locale: Locale }) { return <ul className="client-notices">{notices.map((notice, index) => <li key={`${notice.code}:${index}`}>{locale === 'ja' ? notice.messageJa : notice.message}</li>)}</ul> }
export function CopyValue({ label, value, locale }: { label: string; value: string; locale: Locale }) {
  const [status, setStatus] = useState('')
  const alive = useAlive()
  const current = useRef(value); current.current = value
  useEffect(() => setStatus(''), [value])
  const copy = async () => { const selected = value; try { await navigator.clipboard.writeText(selected); if (alive.current && current.current === selected) setStatus('copied') } catch { if (alive.current && current.current === selected) setStatus('copyFailed') } }
  return <div className="client-copy"><label className="field">{label}<textarea readOnly value={value} rows={value.length > 100 ? 3 : 2} spellCheck={false} className="code-value" /></label><Button type="button" onClick={() => void copy()} aria-label={`${clientText(locale, 'copy')}: ${label}`}>{clientText(locale, 'copy')}</Button>{status && <p role="status" className="small muted">{clientText(locale, status)}</p>}</div>
}
export function RustDeskSettings({ settings, locale, t }: { settings: RustDeskClientSettings; locale: Locale; t: Translate }) {
  const c = (key: string) => clientText(locale, key)
  return <section className="form-stack rustdesk-settings"><h3>{settings.group}</h3><p className="scope-note">{c('unverified')}</p><CopyValue label={c('idServer')} value={settings.idServer} locale={locale} /><CopyValue label={c('relayServer')} value={settings.relayServer} locale={locale} /><CopyValue label={c('publicKey')} value={settings.publicKey} locale={locale} /><dl className="details-list"><dt>{c('proxy')}</dt><dd>{c('blank')}</dd><dt>{c('udp')}</dt><dd>{c('enabled')}</dd><dt>{c('suffix')}</dt><dd className="code-value">{settings.remoteIdSuffix}</dd></dl><h4>{c('flows')}</h4><div className="client-flows">{settings.roles.map(role => <article key={role.role} className="definition-scope"><strong>{c(role.role)} · {role.network.toUpperCase()}</strong><dl><div><dt>{c('peer')}</dt><dd className="code-value">{role.peerId}</dd></div><div><dt>{c('endpoint')}</dt><dd className="code-value">{role.localEndpoint} → {role.remotePort}</dd></div><div><dt>{c('lifetime')}</dt><dd>{lifetimeText(locale, role.lifetime, role.ttlSeconds)}</dd></div><div><dt>{c('readiness')}</dt><dd>{c(role.listenerReady ? 'ready' : 'notReady')} · {role.status === 'planned' ? c('planned') : t(role.status)}</dd></div></dl></article>)}</div><Notices notices={settings.notices} locale={locale} /></section>
}
export function ClientSettingsDialog({ server, locale, t, onClose, target }: { server: Server; locale: Locale; t: Translate; onClose: () => void; target: { ids?: string[]; group?: string } }) {
  const c = (key: string) => clientText(locale, key)
  const [value, setValue] = useState<ClientSettingsView>()
  const [loading, setLoading] = useState(true)
  const [reload, setReload] = useState(0)
  const readID = useRef(crypto.randomUUID())
  const targetKey = JSON.stringify(target)
  useEffect(() => {
    let cancelled = false; setValue(undefined); setLoading(true)
    void Promise.resolve().then(() => cancelled ? undefined : server.run('client.settings', JSON.parse(targetKey), `client.settings:${readID.current}:${targetKey}:${reload}`)).then(result => {
      if (cancelled) return
      if (result) { try { const settings = readClientSettings(result.result); if (target.ids && (settings.services.length !== target.ids.length || settings.services.some(service => !target.ids!.includes(service.id))) || target.group && settings.rustdesk.some(group => group.group !== target.group)) throw new Error('invalid_response'); setValue(settings) } catch { server.setError({ code: 'invalid_response' }) } }
      setLoading(false)
    })
    return () => { cancelled = true }
  }, [targetKey, reload, server.run])
  const code = (server.error as { code?: string })?.code || ''
  const span = (first: number, last: number) => first === last ? String(first) : `${first}–${last}`
  return <Modal title={c('settings')} t={t} onClose={onClose} wide><p>{c('hintsIntro')}</p>{server.error != null && <ErrorBanner message={c(code) || serviceText(locale, code) || errorText(server.error, t)} t={t} />}{loading ? <p role="status">{t('loading')}</p> : value ? <div className="form-stack"><Notices notices={value.notices} locale={locale} />{value.rustdesk.map(settings => <RustDeskSettings key={settings.group} settings={settings} locale={locale} t={t} />)}{value.services.map(service => <section className="definition-scope form-stack" key={service.id}><div className="flex justify-between gap-2"><h3>{service.name}</h3><Badge>{t(service.status)}</Badge></div><p>{service.backend} · {service.network.toUpperCase()} · <span className="code-value">{service.peerId || service.allowedPeerIds?.join(', ')}</span></p><p>{c('readiness')}: {c(service.listenerReady ? 'ready' : 'notReady')} · {c('lifetime')}: {lifetimeText(locale, service.lifetime, service.ttlSeconds)}</p>{service.localEndpoint && <CopyValue label={c('endpoint')} value={service.localEndpoint} locale={locale} />}<p>{c('mappings')}: <span className="code-value">{service.localHost}</span></p><ul>{service.mappings.map((mapping, index) => <li className="code-value" key={index}>{span(mapping.localFirst, mapping.localLast)} → {span(mapping.remoteFirst, mapping.remoteLast)}</li>)}</ul>{service.remoteHosts?.length ? <CopyValue label={c('remoteHosts')} value={service.remoteHosts.join('\n')} locale={locale} /> : null}{service.remoteEndpoints.length ? <CopyValue label={c('remoteEndpoints')} value={service.remoteEndpoints.join('\n')} locale={locale} /> : <p className="small muted">{c('noEndpoints')}</p>}{service.ssh && <><CopyValue label={c('ssh')} value={service.ssh.command} locale={locale} /><CopyValue label={c('hostKey')} value={service.ssh.hostKeyAlias} locale={locale} /></>}{service.httpCandidate && <CopyValue label={c('http')} value={service.httpCandidate} locale={locale} />}<Notices notices={service.notices} locale={locale} /></section>)}{!value.services.length && !value.rustdesk.length && <p>{c('empty')}</p>}</div> : <p>{t('unavailable')}</p>}<div className="modal-actions"><Button disabled={loading || server.stale} onClick={() => { server.setError(null); setReload(value => value + 1) }}>{c('refresh')}</Button><Button onClick={onClose}>{t('close')}</Button></div></Modal>
}
export function ClientSettingsButton({ server, locale, t, id }: { server: Server; locale: Locale; t: Translate; id: string }) {
  const [open, setOpen] = useState(false)
  return <><Button type="button" variant="ghost" disabled={server.stale} onClick={() => { server.setError(null); setOpen(true) }}>{clientText(locale, 'settings')}</Button>{open && <ClientSettingsDialog target={{ ids: [id] }} server={server} locale={locale} t={t} onClose={() => setOpen(false)} />}</>
}
export function RustDeskSetupDialog({ server, locale, t, onClose, onSaved }: { server: Server; locale: Locale; t: Translate; onClose: () => void; onSaved: (group: string) => void }) {
  const c = (key: string) => clientText(locale, key)
  const keyHelpId = useId()
  const s = (key: string) => serviceText(locale, key)
  const [draft, setDraft] = useState<RustDeskSetup>({ name: 'rustdesk', backend: server.state?.settings?.network === 'lan' ? 'lan' : 'tailnet', idPeerId: '', relayPeerId: '', publicKey: '', idPort: 21116, relayPort: 21117, localIdPort: 32116, localRelayPort: 32117, loopbackHost: '127.0.0.1', lifetime: 'until-stopped', ttlSeconds: 0 })
  const [review, setReview] = useState<RustDeskSetupReview>()
  const [saved, setSaved] = useState<RustDeskSetupReview>()
  const [working, setWorking] = useState(false)
  const pending = useRef(false)
  const generation = useRef(0)
  const alive = useAlive()
  const blocked = server.stale || working
  const valid = validRustDeskSetup(draft)
  const change = (patch: Partial<RustDeskSetup>) => { ++generation.current; setReview(undefined); setSaved(undefined); server.setError(null); setDraft(current => ({ ...current, ...patch })) }
  const prepare = async (event: FormEvent) => {
    event.preventDefault(); if (blocked || pending.current || !valid) return
    const current = ++generation.current; const configuration = structuredClone(draft)
    pending.current = true; setWorking(true); setReview(undefined)
    const response = await server.run('rustdesk.preview', { configuration })
    if (alive.current && current === generation.current && response) { try { setReview(readRustDeskReview(response.result, configuration)) } catch { server.setError({ code: 'invalid_response' }) } }
    pending.current = false; if (alive.current) setWorking(false)
  }
  const save = async () => {
    if (!review || blocked || pending.current) return
    const checked = review; const current = ++generation.current
    pending.current = true; setWorking(true); setReview(undefined)
    const response = await server.run('rustdesk.save', { configuration: checked.configuration, expectedRevision: checked.revision })
    if (alive.current && current === generation.current && response) { try { const result = readRustDeskReview(response.result, checked.configuration); if (!result.saved) throw new Error('invalid_response'); setSaved(result) } catch { server.setError({ code: 'invalid_response' }) } }
    pending.current = false; if (alive.current) setWorking(false)
  }
  const code = (server.error as { code?: string })?.code || ''
  const peerField = (field: 'idPeerId' | 'relayPeerId', label: string) => <fieldset className="client-peer"><legend>{label}</legend><label className="field">{c('pick')}<select value={server.state?.peers.some(peer => peer.id === draft[field] && peer.networks.includes(draft.backend)) ? draft[field] : ''} disabled={blocked} onChange={event => change({ [field]: event.target.value })}><option value="">{c('pick')}</option>{server.state?.peers.filter(peer => peer.networks.includes(draft.backend)).map(peer => <option key={peer.id} value={peer.id}>{peer.name} · {peer.id}</option>)}</select></label><label className="field">{c('exactID')}<input value={draft[field]} required disabled={blocked} maxLength={128} autoComplete="off" spellCheck={false} onChange={event => change({ [field]: event.target.value })} /></label></fieldset>
  return <Modal title={c('setup')} t={t} onClose={onClose} wide><p>{c('intro')}</p>{server.error != null && <ErrorBanner message={c(code) || s(code) || errorText(server.error, t)} t={t} />}{saved ? <><p role="status">{c(saved.applied ? 'saved' : 'alreadySaved')}</p><RustDeskSettings settings={saved.clientSettings} locale={locale} t={t} /><div className="modal-actions"><Button onClick={onClose}>{t('close')}</Button><Button variant="primary" onClick={() => onSaved(saved.group.name)}>{c('manage')}</Button></div></> : review ? <section className="form-stack" aria-label={c('reviewTitle')}><h3>{c('reviewTitle')}</h3><p>{c('backend')}: {review.configuration.backend}</p><p>{c('savedLifetime')}: {lifetimeText(locale, review.configuration.lifetime, review.configuration.ttlSeconds)}</p><RustDeskSettings settings={review.clientSettings} locale={locale} t={t} /><div className="modal-actions"><Button onClick={() => { ++generation.current; setReview(undefined) }}>{c('back')}</Button><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button variant="primary" disabled={blocked} onClick={() => void save()}>{c('save')}</Button></div></section> : <form className="form-stack" onSubmit={prepare}><label className="field">{c('name')}<input value={draft.name} required maxLength={54} disabled={blocked} onChange={event => change({ name: event.target.value })} /></label><label className="field">{c('backend')}<select value={draft.backend} disabled={blocked} onChange={event => change({ backend: event.target.value as RustDeskSetup['backend'], idPeerId: '', relayPeerId: '' })}><option value="tailnet">Tailnet</option><option value="lan">LAN</option></select></label><div className="client-peers">{peerField('idPeerId', c('idPeer'))}{peerField('relayPeerId', c('relayPeer'))}</div><p className="small muted">{c('offline')}</p><label className="field">{c('publicKey')}<textarea aria-label={c('publicKey')} aria-describedby={keyHelpId} value={draft.publicKey} required rows={2} maxLength={44} spellCheck={false} autoComplete="off" disabled={blocked} onChange={event => change({ publicKey: event.target.value.trim() })} /><small id={keyHelpId}>{c('keyHint')}</small></label><p className="small muted">{c('portHint')}</p><div className="client-ports">{(['idPort', 'relayPort', 'localIdPort', 'localRelayPort'] as const).map(field => <label className="field" key={field}>{c(field)}<input type="number" required min={field === 'idPort' || field === 'localIdPort' ? 1025 : 1024} max={65535} value={draft[field] || ''} disabled={blocked} onChange={event => change({ [field]: Number(event.target.value) })} /></label>)}</div><label className="field">{s('loopbackHost')}<select value={draft.loopbackHost} disabled={blocked} onChange={event => change({ loopbackHost: event.target.value as RustDeskSetup['loopbackHost'] })}><option>127.0.0.1</option><option>::1</option></select></label><label className="field">{s('lifetime')}<select value={draft.lifetime} disabled={blocked} onChange={event => change({ lifetime: event.target.value as RustDeskSetup['lifetime'], ttlSeconds: event.target.value === 'finite' ? 3600 : 0 })}><option value="until-stopped">{s('untilStopped')}</option><option value="finite">{s('customDuration')}</option></select></label>{draft.lifetime === 'finite' && <label className="field">{s('durationSeconds')}<input type="number" min={1} max={MAX_SERVICE_TTL_SECONDS} step={1} required value={draft.ttlSeconds || ''} disabled={blocked} onChange={event => change({ ttlSeconds: Number(event.target.value) })} /></label>}<p className="small muted">{s(draft.lifetime === 'finite' ? 'finiteHint' : 'untilStoppedHint')}</p>{!valid && draft.publicKey && <p className="field-error">{c('invalid')}</p>}<div className="modal-actions"><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button type="submit" variant="primary" disabled={blocked || !valid}>{working ? t('loading') : c('review')}</Button></div></form>}</Modal>
}
