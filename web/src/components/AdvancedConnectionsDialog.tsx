import { SaveProxyReview, SavedProxiesDialog } from './SavedProxyControls'
import { startupText } from '../startup-i18n'
import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import * as api from '../api'
import { advancedText } from '../advanced-i18n'
import { readProxyReview, validProxyScope } from '../advanced-connections'
import { MAX_SERVICE_TTL_SECONDS } from '../service-form'
import type { Translate } from '../i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Modal, useAlive } from './ui'

export function AdvancedConnectionsDialog({ server, locale, t, onClose }: { server: Server; locale: api.Locale; t: Translate; onClose: () => void }) {
  const p = (key: string) => advancedText(locale, key) || startupText(locale, key)
  const alive = useAlive()
  const state = server.state!
  const backend = state.settings?.network
  const networkReady = (backend === 'lan' || backend === 'tailnet') && ['running', 'online', 'ready'].includes(state.self.status.toLowerCase())
  const peers = state.peers.filter(peer => peer.networks.includes(backend as api.Network))
  const [draft, setDraft] = useState<api.ProxyScope>({ name: '', backend: backend === 'lan' ? 'lan' : 'tailnet', loopbackHost: '127.0.0.1', localPort: 1080, lifetime: 'until-stopped', ttlSeconds: 0, targets: [{ peerId: '', port: 443 }] })
  const [review, setReview] = useState<api.ProxyReview>()
  const [savedOpen, setSavedOpen] = useState(false)
  const [saveReviewed, setSaveReviewed] = useState(false)
  const [credentials, setCredentials] = useState(false)
  const [proxies, setProxies] = useState<api.ProxyView[] | undefined>(state.proxies)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [uncertain, setUncertain] = useState(false)
  const startedID = useRef('')
  const pending = useRef<AbortController | null>(null)
  const username = useRef<HTMLInputElement>(null)
  const password = useRef<HTMLInputElement>(null)
  const erase = () => { if (username.current) username.current.value = ''; if (password.current) password.current.value = '' }
  const scopeKey = JSON.stringify([backend, state.self.status, server.stale, state.reservedPorts, draft.targets.map(target => peers.filter(peer => peer.id === target.peerId).map(peer => [peer.id, peer.name, peer.address, peer.online, peer.verified, peer.networks]))])
  const networkKey = JSON.stringify([backend, state.self.status, server.stale, draft.targets.map(target => peers.filter(peer => peer.id === target.peerId).map(peer => [peer.id, peer.name, peer.address, peer.online, peer.verified, peer.networks]))])
  const currentNetwork = useRef(networkKey); currentNetwork.current = networkKey
  const authoritativeProxies = useRef(state.proxies); authoritativeProxies.current = state.proxies
  const currentScope = useRef(scopeKey); currentScope.current = scopeKey
  const previousScope = useRef(scopeKey)
  const close = () => { erase(); pending.current?.abort(); onClose() }
  useLayoutEffect(() => () => { erase(); pending.current?.abort(); pending.current = null }, [])
  useEffect(() => {
    if (previousScope.current === scopeKey) return
    previousScope.current = scopeKey
    erase(); setCredentials(false); setSaveReviewed(false); setReview(undefined)
    setDraft(current => ({ ...current, backend: backend === 'lan' ? 'lan' : 'tailnet' }))
    if (review || credentials || busy === 'preview') setStatus('changed')
  }, [scopeKey, backend])
  useEffect(() => {
    if (state.proxies === undefined) return
    setProxies(state.proxies)
    if (startedID.current && !state.proxies.some(proxy => proxy.id === startedID.current && proxy.status === 'active')) { startedID.current = ''; setStatus('stopped') }
  }, [state.proxies])
  useEffect(() => { if (state.proxies === undefined) setProxies(undefined) }, [networkKey])
  const fail = (value: unknown) => {
    if (value instanceof api.ApiError && value.code === 'unauthenticated') { server.handleError(new api.ApiError(value.code, '', value.status)); return }
    // Only stable codes reach the UI; response messages must never echo private input.
    setError(value instanceof api.ApiError ? value.code : 'failed')
  }
  const load = async (clearError = true) => {
    if (pending.current) return
    const request = new AbortController(); pending.current = request; setBusy('list'); if (clearError) setError('')
    const observedNetwork = currentNetwork.current
    const observedProxies = authoritativeProxies.current
    try {
      const result = await api.command('proxy.list', {}, api.requestID(), request.signal)
      if (!alive.current || request.signal.aborted || observedNetwork !== currentNetwork.current || observedProxies !== authoritativeProxies.current) return
      const values = result.result as unknown
      if (!Array.isArray(values) || values.some(value => !value?.id || !value?.name || !value?.endpoint || !Array.isArray(value?.targets) || value.protocol !== 'socks5-tcp-connect' || value.authentication !== 'required')) throw new api.ApiError('invalid_response', '')
      setProxies(values); setUncertain(false)
    } catch (value) { if (alive.current && !request.signal.aborted) fail(value) }
    finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy('') } }
  }
  useEffect(() => { let cancelled = false; void Promise.resolve().then(() => { if (!cancelled) return load() }); return () => { cancelled = true } }, [])
  const update = (next: api.ProxyScope) => { erase(); setCredentials(false); setSaveReviewed(false); setReview(undefined); setDraft(next); setError(''); setStatus('') }
  const invalid = !validProxyScope(draft, peers.map(peer => peer.id), state.reservedPorts)
  const blocked = Boolean(busy) || server.stale
  const preview = async (event: FormEvent) => {
    event.preventDefault()
    if (pending.current || server.stale || invalid || !networkReady || uncertain) return
    erase(); setCredentials(false); setSaveReviewed(false); setReview(undefined); setError(''); setStatus('')
    const request = new AbortController(); pending.current = request; setBusy('preview')
    const reviewedScope = currentScope.current
    try {
      const response = await api.command('proxy.preview', { scope: draft }, api.requestID(), request.signal)
      if (!alive.current || request.signal.aborted || reviewedScope !== currentScope.current) return
      setReview(readProxyReview(response.result, draft))
    } catch (value) { if (alive.current && !request.signal.aborted) fail(value) }
    finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy('') } }
  }
  const start = async (event: FormEvent) => {
    event.preventDefault()
    if (pending.current || !review || !credentials || blocked || !networkReady || uncertain) return
    const input = { scope: review.scope, expectedRevision: review.revision, username: username.current?.value || '', password: password.current?.value || '' }
    erase()
    if ([input.username, input.password].some(value => { const bytes = api.messageByteLength(value); return bytes < 1 || bytes > 255 })) { input.username = ''; input.password = ''; setError('credentialInvalid'); return }
    const request = new AbortController(); pending.current = request; setBusy('start'); setError(''); setStatus(''); setCredentials(false); setReview(undefined)
    // Do not pass credentials through useServer.run: its retry signature retains payloads.
    // api.command serializes immediately. Drop our private values before awaiting I/O.
    const startingNetwork = currentNetwork.current
    const response = api.command('proxy.start', input, api.requestID(), request.signal)
    input.username = ''; input.password = ''
    let started = false
    try {
      const result = await response
      if (!alive.current || request.signal.aborted) return
      const value = result.result as unknown as api.ProxyView
      if (!value?.id || value.status !== 'active' || value.application !== 'unverified') throw new api.ApiError('invalid_response', '')
      started = true; startedID.current = value.id
      setStatus(startingNetwork === currentNetwork.current ? 'started' : 'changed')
    } catch (value) {
      if (alive.current && !request.signal.aborted) {
        if (!(value instanceof api.ApiError) || ['network_error', 'invalid_response'].includes(value.code)) { setUncertain(true); setStatus('startUncertain') }
        else fail(value)
      }
    } finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy('') } }
    if (started && alive.current && !request.signal.aborted) { await load(false); await server.refresh(true) }
  }
  const stop = async (id: string) => {
    if (pending.current || server.stale) return
    const request = new AbortController(); pending.current = request; setBusy(id); setError(''); setStatus('')
    let stopped = false
    try { await api.command('proxy.stop', { id }, api.requestID(), request.signal); if (alive.current && !request.signal.aborted) { stopped = true; setStatus('stopped') } }
    catch (value) { if (alive.current && !request.signal.aborted) fail(value) }
    finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy('') } }
    if (stopped && alive.current && !request.signal.aborted) { await load(false); await server.refresh(true) }
  }
  if (savedOpen) return <SavedProxiesDialog server={server} locale={locale} t={t} onClose={() => { setSavedOpen(false); void load() }} />
  return <Modal title={p('title')} t={t} onClose={close} wide><p className="muted">{p('intro')}</p>{error && <ErrorBanner message={p(error) || p('failed')} t={t} />}{status && <p className="scope-note" role="status">{p(status)}</p>}
    <section className="proxy-list"><div className="flex justify-between items-center"><h3>{p('proxies')}</h3><Button type="button" onClick={() => void load()} disabled={Boolean(busy)}>{t('refresh')}</Button></div>{!proxies ? <p>{busy === 'list' ? t('loading') : t('unavailable')}</p> : !proxies.length ? <p className="muted">{p('none')}</p> : proxies.map(proxy => <article className="service-row" key={proxy.id}><div className="flex justify-between gap-2"><strong>{proxy.name}</strong><Badge>{t(proxy.status)}</Badge></div><p className="code-value">{proxy.endpoint}</p><p>{t(proxy.backend)} · {proxy.expiresAt || p('untilStopped')}</p><p className="small muted">{p('application')}</p>{state.savedProxies?.entries.some(entry => entry.name === proxy.name && entry.startOnLaunch) && <p className="scope-note">{p('stopStartupHint')}</p>}<ul>{proxy.targets.map(target => <li className="code-value" key={`${target.peerId}:${target.port}`}>{state.peers.find(peer => peer.id === target.peerId)?.name || target.peerId} · {target.peerId} · TCP {target.port}</li>)}</ul><Button type="button" variant="danger" disabled={blocked} onClick={() => void stop(proxy.id)}>{p('stop')}</Button></article>)}<p className="small muted">{p('stopHint')}</p></section>
    <details className="advanced"><summary>{p('savedProxies')}</summary><Button type="button" disabled={blocked} onClick={() => { erase(); setCredentials(false); setSaveReviewed(false); setReview(undefined); setSavedOpen(true) }}>{p('openSaved')}</Button></details>
    <section className="subsection"><h3>{p('create')}</h3><p className="small muted">{p('protocol')}</p><p className="scope-note">{p('lifecycle')}</p>{!networkReady && <p role="status">{p('noNetwork')}</p>}
    {!review ? <form className="form-stack" onSubmit={preview}><label className="field">{p('name')}<input autoComplete="off" spellCheck={false} value={draft.name} disabled={blocked} maxLength={64} onChange={event => update({ ...draft, name: event.target.value })} required /></label><p>{p('backend')}: <strong>{backend === 'lan' || backend === 'tailnet' ? t(backend) : t('disabledNetwork')}</strong></p><div className="proxy-fields"><label className="field">{p('listener')}<select value={draft.loopbackHost} disabled={blocked} onChange={event => update({ ...draft, loopbackHost: event.target.value as api.ProxyScope['loopbackHost'] })}><option>127.0.0.1</option><option>::1</option></select></label><label className="field">{p('localPort')}<input type="number" min="1024" max="65535" step="1" value={draft.localPort || ''} disabled={blocked} onChange={event => update({ ...draft, localPort: Number(event.target.value) })} required /></label></div><fieldset className="proxy-targets"><legend>{p('targets')}</legend>{draft.targets.map((target, index) => <div className="proxy-target" key={index}><label className="field">{p('target')} {index + 1}<select value={target.peerId} disabled={blocked} onChange={event => update({ ...draft, targets: draft.targets.map((item, i) => i === index ? { ...item, peerId: event.target.value } : item) })} required><option value="">{p('choosePeer')}</option>{peers.map(peer => <option key={peer.id} value={peer.id}>{peer.name} · {peer.id}</option>)}</select></label><label className="field">{p('port')} {index + 1}<input type="number" min="1" max="65535" step="1" value={target.port || ''} disabled={blocked} onChange={event => update({ ...draft, targets: draft.targets.map((item, i) => i === index ? { ...item, port: Number(event.target.value) } : item) })} required /></label><Button type="button" variant="ghost" disabled={blocked || draft.targets.length === 1} onClick={() => update({ ...draft, targets: draft.targets.filter((_, i) => i !== index) })} aria-label={`${p('removeTarget')} ${index + 1}`}>{p('removeTarget')}</Button></div>)}<Button type="button" onClick={() => update({ ...draft, targets: [...draft.targets, { peerId: '', port: 443 }] })} disabled={blocked}>{p('addTarget')}</Button></fieldset><label className="field">{p('lifetime')}<select disabled={blocked} value={draft.lifetime} onChange={event => update({ ...draft, lifetime: event.target.value as api.ProxyScope['lifetime'], ttlSeconds: event.target.value === 'finite' ? 3600 : 0 })}><option value="until-stopped">{p('untilStopped')}</option><option value="finite">{p('finite')}</option></select></label>{draft.lifetime === 'finite' && <label className="field">{p('seconds')}<input type="number" min="1" max={MAX_SERVICE_TTL_SECONDS} step="1" disabled={blocked} value={draft.ttlSeconds || ''} onChange={event => update({ ...draft, ttlSeconds: Number(event.target.value) })} required /></label>}{invalid && draft.name && <p className="field-error">{p('invalid')}</p>}<div className="modal-actions"><Button type="button" onClick={close}>{t('cancel')}</Button><Button type="submit" variant="primary" disabled={blocked || invalid || !networkReady || uncertain}>{p('review')}</Button></div></form>
    : <section className="proxy-review" aria-label={p('reviewTitle')}><h4>{p('reviewTitle')}</h4><p>{p('reviewHint')}</p><dl className="details-list"><dt>{p('name')}</dt><dd>{review.scope.name}</dd><dt>{p('backend')}</dt><dd>{t(review.scope.backend)}</dd><dt>{p('endpoint')}</dt><dd className="code-value">{review.endpoint}</dd><dt>{p('lifetime')}</dt><dd>{review.scope.lifetime === 'finite' ? `${review.scope.ttlSeconds} ${p('seconds')}` : p('untilStopped')}</dd></dl><h4>{p('targets')}</h4><ul>{review.targets.map(target => <li key={`${target.peerId}:${target.port}`}><p>{state.peers.find(peer => peer.id === target.peerId)?.name || target.peerId}</p><p className="code-value">{p('peerID')}: {target.peerId}</p><p className="code-value">{p('host')}: {target.host} · TCP {target.port}</p></li>)}</ul><p>{p('application')}</p>{saveReviewed ? <SaveProxyReview review={review} server={server} locale={locale} t={t} onBack={() => update(draft)} onSaved={() => { update(draft); setStatus('savedProxyDone') }} /> : credentials ? <form className="form-stack proxy-credentials" data-private="proxy-credentials" onSubmit={start} autoComplete="off"><p>{p('credentials')}</p><label className="field">{p('username')}<input ref={username} type="password" autoComplete="off" spellCheck={false} autoCapitalize="none" data-private="proxy-credential" required /></label><label className="field">{p('password')}<input ref={password} type="password" autoComplete="new-password" spellCheck={false} data-private="proxy-credential" required /></label><div className="modal-actions"><Button type="button" onClick={() => update(draft)}>{p('back')}</Button><Button type="submit" variant="primary" disabled={blocked}>{p('start')}</Button></div></form> : <div className="modal-actions"><Button type="button" onClick={() => update(draft)}>{p('back')}</Button><Button type="button" variant="primary" disabled={blocked} onClick={() => { erase(); setCredentials(true); setError('') }}>{p('authenticate')}</Button><Button type="button" disabled={blocked} onClick={() => { erase(); setCredentials(false); setSaveReviewed(true); setError('') }}>{p('saveScope')}</Button></div>}</section>}
    </section></Modal>
}
