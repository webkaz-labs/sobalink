import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import * as api from '../api'
import { proxyScopeKey, readProxyReview } from '../advanced-connections'
import { readSavedProxies, readSavedProxy } from '../startup-proxy'
import { startupText } from '../startup-i18n'
import { advancedText } from '../advanced-i18n'
import { loopbackEndpoint } from '../service-form'
import type { Translate } from '../i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Modal, useAlive } from './ui'
export function ProxyScopeDetails({ scope, locale, t }: { scope: api.ProxyScope; locale: api.Locale; t: Translate }) {
  const p = (key: string) => advancedText(locale, key)
  return <div className="proxy-review"><dl className="details-list"><dt>{p('name')}</dt><dd>{scope.name}</dd><dt>{p('backend')}</dt><dd>{t(scope.backend)}</dd><dt>{p('endpoint')}</dt><dd className="code-value">{loopbackEndpoint(scope.loopbackHost, String(scope.localPort))}</dd><dt>{p('lifetime')}</dt><dd>{scope.lifetime === 'finite' ? `${scope.ttlSeconds} ${p('seconds')}` : p('untilStopped')}</dd></dl><ul>{scope.targets.map(target => <li className="code-value" key={`${target.peerId}:${target.port}`}>{target.peerId} · TCP {target.port}</li>)}</ul><p className="small muted">{p('application')}</p></div>
}
function failureCode(value: unknown) { return value instanceof api.ApiError ? value.code : value instanceof Error && value.message === 'invalid_response' ? 'invalid_response' : 'failedRequest' }
function credentialValid(value: string) { const length = api.messageByteLength(value); return length > 0 && length <= 255 }
export function SaveProxyReview({ review, server, locale, t, onBack, onSaved }: { review: api.ProxyReview; server: Server; locale: api.Locale; t: Translate; onBack: () => void; onSaved: () => void }) {
  const s = (key: string) => startupText(locale, key)
  const p = (key: string) => advancedText(locale, key)
  const alive = useAlive()
  const [list, setList] = useState<api.SavedProxyList>()
  const [method, setMethod] = useState<'generate' | 'supplied'>('generate')
  const [launch, setLaunch] = useState(false)
  const [replace, setReplace] = useState(false)
  const [busy, setBusy] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState('')
  const [needsReview, setNeedsReview] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const username = useRef<HTMLInputElement>(null); const password = useRef<HTMLInputElement>(null)
  const erase = () => { if (username.current) username.current.value = ''; if (password.current) password.current.value = '' }
  useLayoutEffect(() => () => { erase(); pending.current?.abort() }, [])
  useEffect(() => {
    const request = new AbortController(); pending.current = request; setBusy(true)
    void api.command('proxy.saved.list', {}, api.requestID(), request.signal).then(response => { if (alive.current && !request.signal.aborted) setList(readSavedProxies(response.result)) }).catch(value => { if (alive.current && !request.signal.aborted) { if (value instanceof api.ApiError && value.code === 'unauthenticated') server.handleError(new api.ApiError(value.code, '', value.status)); else setError(failureCode(value)) } }).finally(() => { if (pending.current === request) { pending.current = null; if (alive.current) setBusy(false) } })
    return () => request.abort()
  }, [review.revision])
  useLayoutEffect(() => {
    if (list && server.state?.savedProxies && server.state.savedProxies.revision !== list.revision) { erase(); setNeedsReview(true); setError('scopeChanged') }
  }, [server.state?.savedProxies?.revision])
  const existing = list?.entries.find(entry => entry.name === review.scope.name)
  const active = server.state?.proxies?.some(proxy => proxy.name === review.scope.name && proxy.status === 'active')
  const blocked = busy || server.stale || needsReview || Boolean(active)
  const save = async (event: FormEvent) => {
    event.preventDefault()
    if (blocked || pending.current || !list || existing && !replace) return
    const input = { scope: review.scope, expectedRevision: review.revision, expectedStoreRevision: list.revision, startOnLaunch: launch, username: username.current?.value || '', password: password.current?.value || '' }
    erase()
    if (method === 'supplied' && (!credentialValid(input.username) || !credentialValid(input.password))) { input.username = ''; input.password = ''; setError('credentialInvalid'); return }
    const request = new AbortController(); pending.current = request; setBusy(true); setSubmitted(true); setError('')
    // Private input and reveal never use useServer.run's retained retry signature.
    const response = method === 'generate' ? api.command('proxy.generate', { scope: input.scope, expectedRevision: input.expectedRevision, expectedStoreRevision: input.expectedStoreRevision, startOnLaunch: input.startOnLaunch }, api.requestID(), request.signal) : api.command('proxy.save', input, api.requestID(), request.signal)
    input.username = ''; input.password = ''
    try {
      const result = await response
      if (!alive.current || request.signal.aborted) return
      const value = readSavedProxy(result.result)
      if (value.name !== review.scope.name || value.startOnLaunch !== launch || proxyScopeKey(value.scope) !== proxyScopeKey(review.scope)) throw new Error('invalid_response')
      onSaved(); await server.refresh(true)
    } catch (value) {
      if (alive.current && !request.signal.aborted) {
        if (value instanceof api.ApiError && value.code === 'unauthenticated') server.handleError(new api.ApiError(value.code, '', value.status))
        else { setError(['network_error', 'invalid_response'].includes(failureCode(value)) ? 'saveUncertain' : failureCode(value)); setNeedsReview(true) }
      }
    } finally { erase(); if (pending.current === request) { pending.current = null; if (alive.current) { setBusy(false); setSubmitted(false) } } }
  }
  return <section className="form-stack" aria-label={s('saveTitle')}><h4>{s('saveTitle')}</h4><p>{s('storageHint')}</p>{error && <ErrorBanner message={s(error) || p(error) || s('failedRequest')} t={t} />}{submitted && <p role="status">{s('operationPending')}</p>}{active && <p className="field-error">{p('proxy_name_conflict')}</p>}{needsReview && <p>{s('reviewAgain')}</p>}<form className="form-stack" onSubmit={save}><label className="field">{s('saveTitle')}<select value={method} disabled={blocked} onChange={event => { erase(); setMethod(event.target.value as typeof method); setError('') }}><option value="generate">{s('generated')}</option><option value="supplied">{s('supplied')}</option></select></label><label className="checkbox-field"><input type="checkbox" checked={launch} disabled={blocked} onChange={event => { erase(); setLaunch(event.target.checked) }} /><span>{s('startOnLaunch')}</span></label><p className="small muted">{s('launchHint')}</p>{existing && <><p>{s('replaceProxyHint')}</p><label className="checkbox-field"><input type="checkbox" checked={replace} disabled={blocked} onChange={event => { erase(); setReplace(event.target.checked) }} /><span>{s('replaceProxy')}</span></label></>}{method === 'supplied' && <div className="proxy-credentials" data-private="proxy-credentials"><label className="field">{p('username')}<input ref={username} type="password" autoComplete="off" spellCheck={false} data-private="proxy-credential" disabled={blocked} required /></label><label className="field">{p('password')}<input ref={password} type="password" autoComplete="new-password" spellCheck={false} data-private="proxy-credential" disabled={blocked} required /></label></div>}<div className="modal-actions"><Button type="button" onClick={() => { erase(); pending.current?.abort(); onBack() }}>{p('back')}</Button><Button type="submit" variant="primary" disabled={blocked || !list || Boolean(existing && !replace)}>{s(method === 'generate' ? 'generate' : 'saveCredentials')}</Button></div></form></section>
}

type SavedAction = { kind: 'start' | 'disable' | 'delete' | 'reveal'; entry: api.SavedProxyView; preview?: api.ProxyReview }
export function SavedProxiesDialog({ server, locale, t, onClose }: { server: Server; locale: api.Locale; t: Translate; onClose: () => void }) {
  const s = (key: string) => startupText(locale, key); const p = (key: string) => advancedText(locale, key)
  const alive = useAlive()
  const [list, setList] = useState<api.SavedProxyList>()
  const [action, setAction] = useState<SavedAction>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [revealed, setRevealed] = useState(false)
  const [copyStatus, setCopyStatus] = useState('')
  const pending = useRef<AbortController | null>(null)
  const epoch = useRef(0)
  const username = useRef<HTMLInputElement>(null); const password = useRef<HTMLInputElement>(null)
  const erase = () => { if (username.current) username.current.value = ''; if (password.current) password.current.value = '' }
  const actionRef = useRef(action); actionRef.current = action
  const snapshot = useRef(server.state?.savedProxies); snapshot.current = server.state?.savedProxies
  const scopeKey = JSON.stringify([server.state?.settings?.network, server.state?.self.status, server.stale])
  const scopeRef = useRef(scopeKey); scopeRef.current = scopeKey
  const clear = () => { ++epoch.current; erase(); setRevealed(false); setCopyStatus(''); setAction(undefined) }
  const close = () => { erase(); pending.current?.abort(); onClose() }
  useLayoutEffect(() => () => { erase(); pending.current?.abort() }, [])
  useLayoutEffect(() => { ++epoch.current; erase(); setRevealed(false); setAction(undefined); setCopyStatus('') }, [scopeKey])
  useLayoutEffect(() => {
    if (!server.state?.savedProxies) return
    try {
      const next = readSavedProxies(server.state.savedProxies); setList(next)
      if (action && !next.entries.some(entry => entry.name === action.entry.name && entry.revision === action.entry.revision && entry.valid === action.entry.valid)) { clear(); setStatus('scopeChanged') }
    } catch { /* A malformed poll never replaces a valid reviewed list. */ }
  }, [server.state?.savedProxies])
  const fail = (value: unknown) => { if (value instanceof api.ApiError && value.code === 'unauthenticated') server.handleError(new api.ApiError(value.code, '', value.status)); else setError(failureCode(value)) }
  const load = async () => {
    if (pending.current) return
    const request = new AbortController(); pending.current = request; setBusy(true); setError(''); clear()
    const observed = snapshot.current
    try { const response = await api.command('proxy.saved.list', {}, api.requestID(), request.signal); if (alive.current && !request.signal.aborted && observed === snapshot.current) setList(readSavedProxies(response.result)) }
    catch (value) { if (alive.current && !request.signal.aborted) fail(value) }
    finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy(false) } }
  }
  useEffect(() => { let cancelled = false; void Promise.resolve().then(() => { if (!cancelled) return load() }); return () => { cancelled = true } }, [])
  const review = async (kind: SavedAction['kind'], entry: api.SavedProxyView) => {
    if (pending.current || server.stale) return
    clear(); setError(''); setStatus(''); setAction({ kind, entry })
    if (kind !== 'start' && kind !== 'reveal') return
    const request = new AbortController(); pending.current = request; setBusy(true)
    const originalScope = scopeRef.current
    const operationEpoch = epoch.current
    try {
      if (kind === 'start') {
        const response = await api.command('proxy.preview', { scope: entry.scope }, api.requestID(), request.signal)
        if (alive.current && !request.signal.aborted && operationEpoch === epoch.current && originalScope === scopeRef.current && (!snapshot.current || snapshot.current.entries.some(item => item.name === entry.name && item.revision === entry.revision))) setAction({ kind, entry, preview: readProxyReview(response.result, entry.scope) })
      } else {
        const response = await api.command('proxy.reveal', { name: entry.name, expectedRevision: entry.revision }, api.requestID(), request.signal)
        const secret = response.result as unknown as { username?: string; password?: string }
        try {
          if (!alive.current || request.signal.aborted || operationEpoch !== epoch.current || originalScope !== scopeRef.current || !actionRef.current || actionRef.current.kind !== 'reveal' || actionRef.current.entry.revision !== entry.revision) return
          if (typeof secret?.username !== 'string' || typeof secret.password !== 'string' || !credentialValid(secret.username) || !credentialValid(secret.password)) throw new Error('invalid_response')
          if (username.current && password.current) { username.current.value = secret.username; password.current.value = secret.password; setRevealed(true) }
        } finally { if (secret && typeof secret === 'object') { secret.username = ''; secret.password = '' } }
      }
    } catch (value) { if (alive.current && !request.signal.aborted) { clear(); fail(value) } }
    finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy(false) } }
  }
  const apply = async () => {
    if (!action || action.kind === 'reveal' || pending.current || server.stale || action.kind === 'start' && !action.preview) return
    const checked = action; clear(); setError(''); setStatus('operationPending')
    const request = new AbortController(); pending.current = request; setBusy(true)
    let success = false
    try {
      const result = await api.command(checked.kind === 'start' ? 'proxy.saved.start' : checked.kind === 'disable' ? 'proxy.saved.disable' : 'proxy.saved.delete', { name: checked.entry.name, expectedRevision: checked.entry.revision }, api.requestID(), request.signal)
      if (!alive.current || request.signal.aborted) return
      if (checked.kind === 'start') { const value = result.result as unknown as api.ProxyView; if (value?.status !== 'active' || value.name !== checked.entry.name || value.application !== 'unverified') throw new Error('invalid_response') }
      else { const value = readSavedProxies(result.result); if (checked.kind === 'delete' ? value.entries.some(entry => entry.name === checked.entry.name) : value.entries.find(entry => entry.name === checked.entry.name)?.startOnLaunch !== false) throw new Error('invalid_response'); setList(value) }
      success = true; setStatus(checked.kind === 'start' ? 'startedProxy' : checked.kind === 'disable' ? 'proxyDisabled' : 'deleted')
    } catch (value) { if (alive.current && !request.signal.aborted) { setStatus(''); fail(value) } }
    finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy(false) } }
    if (success && alive.current && !request.signal.aborted) { await load(); await server.refresh(true) }
  }
  const copy = async (field: 'username' | 'password') => {
    const input = field === 'username' ? username.current : password.current
    if (!revealed || !input?.value) return
    try { await navigator.clipboard.writeText(input.value); if (alive.current && actionRef.current?.kind === 'reveal') setCopyStatus('copied') }
    catch { if (alive.current && actionRef.current?.kind === 'reveal') setCopyStatus('copyFailed') }
  }
  return <Modal title={s('savedProxies')} onClose={close} t={t} wide><p>{s('storageHint')}</p><p>{s('savedLifecycle')}</p>{list?.suppressed && <p className="scope-note">{s('suppressed')}</p>}{error && <ErrorBanner message={s(error) || p(error) || s('failedRequest')} t={t} />}{status && <p role="status" className="scope-note">{s(status)}</p>}
    {action ? <section aria-label={s(action.kind === 'start' ? 'startTitle' : action.kind === 'reveal' ? 'revealTitle' : action.kind === 'delete' ? 'deleteProxy' : 'disableProxy')}><h3>{action.entry.name}</h3><ProxyScopeDetails scope={action.entry.scope} locale={locale} t={t} />{action.kind === 'reveal' ? <div className="proxy-credentials" data-private="proxy-credentials"><h4>{s('revealTitle')}</h4><p>{s('revealHint')}</p><label className="field">{p('username')}<input ref={username} readOnly type="text" autoComplete="off" data-private="proxy-credential" /></label><label className="field">{p('password')}<input ref={password} readOnly type="text" autoComplete="off" data-private="proxy-credential" /></label>{busy && <p>{t('loading')}</p>}<div className="modal-actions"><Button disabled={!revealed} onClick={() => void copy('username')}>{s('copyUsername')}</Button><Button disabled={!revealed} onClick={() => void copy('password')}>{s('copyPassword')}</Button><Button onClick={() => { pending.current?.abort(); clear(); setStatus('hidden') }}>{s('hide')}</Button></div>{copyStatus && <p role="status">{s(copyStatus)}</p>}</div> : <><p>{s(action.kind === 'start' ? 'startHint' : action.kind === 'delete' ? 'deleteProxyHint' : 'disableProxyHint')}</p>{action.preview && <ul>{action.preview.targets.map(target => <li className="code-value" key={`${target.peerId}:${target.port}`}>{target.peerId} · {target.host} · TCP {target.port}</li>)}</ul>}<div className="modal-actions"><Button onClick={() => { pending.current?.abort(); clear() }}>{t('cancel')}</Button><Button variant={action.kind === 'start' ? 'primary' : 'danger'} disabled={busy || server.stale || action.kind === 'start' && !action.preview} onClick={() => void apply()}>{s(action.kind === 'start' ? 'startConfirm' : action.kind === 'delete' ? 'deleteProxyConfirm' : 'disableProxyConfirm')}</Button></div></>}</section> : !list ? <p>{t(busy ? 'loading' : 'unavailable')}</p> : list.entries.length === 0 ? <p>{s('savedEmpty')}</p> : list.entries.map(entry => <article key={entry.name} className="definition-scope"><div className="flex justify-between gap-2"><strong>{entry.name}</strong><Badge>{s(entry.startOnLaunch ? 'futureOn' : 'futureOff')}</Badge></div><p>{s('stored')} · {s(entry.valid ? 'valid' : 'invalid')}</p><p>{s('proxyState')}: {s(entry.state === 'saved' ? 'savedState' : entry.state) || t(entry.state as api.Service['status']) || entry.state}</p><ProxyScopeDetails scope={entry.scope} locale={locale} t={t} /><div className="flex flex-wrap gap-2"><Button disabled={busy || server.stale || !entry.valid || server.state?.proxies?.some(proxy => proxy.name === entry.name && proxy.status === 'active')} onClick={() => void review('start', entry)}>{s('reviewStart')}</Button><Button disabled={busy || server.stale || !entry.startOnLaunch} onClick={() => void review('disable', entry)}>{s('disableProxy')}</Button><Button disabled={busy || server.stale} onClick={() => void review('delete', entry)}>{s('deleteProxy')}</Button><Button disabled={busy || server.stale} onClick={() => void review('reveal', entry)}>{s('reveal')}</Button></div></article>)}<div className="modal-actions"><Button type="button" disabled={busy} onClick={() => void load()}>{s('reload')}</Button><Button type="button" onClick={close}>{t('close')}</Button></div></Modal>
}
