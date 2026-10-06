import { useEffect, useLayoutEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import * as api from '../api'
import { localRouteExpiry, readOwnRoutes, readPeerRoutes, readRouteReview, routeAddress, routeKey, routeTimestamp, currentRouteObservation, readRouteLifetime, currentRoutePermission } from '../lan-routes'
import { routeTranslator, type RouteTextKey } from '../route-i18n'
import { type Translate } from '../i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner } from './ui'
import './LanRoutes.css'

type Props = { server: Server; state: api.State; locale: api.Locale; t: Translate; disabled?: boolean }
type RouteName = Extract<api.CommandName, `lan.routes.${string}`>
function useRouteRequest(server: Server) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<RouteTextKey>()
  const pending = useRef<AbortController | null>(null)
  const cancel = () => { pending.current?.abort(); pending.current = null; setBusy(false) }
  useLayoutEffect(() => () => { pending.current?.abort(); pending.current = null }, [])
  const call = async <N extends RouteName, R>(name: N, payload: api.CommandPayloads[N], read: (value: unknown) => R): Promise<R | undefined> => {
    if (pending.current || server.auth !== 'ready' || server.stale || server.busy.has('application.stop')) return
    const request = new AbortController(); pending.current = request; setBusy(true); setError(undefined)
    try {
      // Never retain private route text in the shared command-retry cache.
      const response = await api.command(name, payload, api.requestID(), request.signal)
      if (request.signal.aborted || pending.current !== request) return
      return read(response.result)
    } catch (value) {
      if (request.signal.aborted || pending.current !== request) return
      if (value instanceof api.ApiError && value.code === 'unauthenticated') server.handleError(new api.ApiError(value.code, '', value.status))
      else setError(value instanceof api.ApiError ? ({ lan_routes_invalid: 'invalid', lan_routes_unavailable: 'unavailable', lan_routes_recovery: 'recovery', network_restart_required: 'offline' } as Record<string, RouteTextKey>)[value.code] || 'failed' : 'failed')
    } finally { if (pending.current === request) { pending.current = null; setBusy(false) } }
  }
  return { busy, error, setError, pending, cancel, call }
}
function Candidate({ candidate, locale }: { candidate: Omit<api.LanRouteCandidate, 'candidateId'>; locale: api.Locale }) {
  const r = routeTranslator(locale)
  return <div className="route-candidate"><p className="code-value">{candidate.address}</p><dl><dt>{r('pin')}</dt><dd className="code-value">{candidate.certificateSHA256}</dd><dt>{r('scope')}</dt><dd><Badge tone={candidate.scope === 'external' ? 'amber' : 'neutral'}>{r(candidate.scope)}</Badge></dd></dl>{candidate.scope === 'external' && <p className="small muted">{r('externalWarning')}</p>}</div>
}
function RouteDisclosure({ title, children }: { title: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  return <details className="route-disclosure" onToggle={event => setOpen(event.currentTarget.open)}><summary>{title}</summary>{open && children}</details>
}
export function PreparedLanRoutes(props: Props) {
  return <RouteDisclosure title={routeTranslator(props.locale)('prepared')}><OwnRoutes key={`${props.state.lan?.publicKey}:${props.state.settings?.network}`} {...props} /></RouteDisclosure>
}
function OwnRoutes({ server, state, locale, t, disabled: externallyDisabled = false }: Props) {
  const r = routeTranslator(locale), request = useRouteRequest(server)
  const [list, setList] = useState<api.LanOwnRoutes>()
  const [mode, setMode] = useState<'home' | 'edit' | 'review'>('home')
  const [address, setAddress] = useState(''), [pin, setPin] = useState('')
  const [scope, setScope] = useState<'local' | 'external'>('local')
  const [review, setReview] = useState<{ kind: 'add' | 'remove'; candidate: Omit<api.LanRouteCandidate, 'candidateId'> & { candidateId?: string } }>()
  const [status, setStatus] = useState<RouteTextKey>()
  const heading = useRef<HTMLHeadingElement>(null), firstField = useRef<HTMLInputElement>(null), home = useRef<HTMLHeadingElement>(null)
  const blocked = server.stale || server.auth !== 'ready' || request.busy || server.busy.has('application.stop')
  const editable = list?.editable === true && Boolean(state.lan?.configured) && !blocked && !externallyDisabled
  const load = async () => { const next = await request.call('lan.routes.list', {}, readOwnRoutes); if (next) setList(next) }
  useEffect(() => { void load() }, [])
  useLayoutEffect(() => { if (mode === 'review') heading.current?.focus(); else if (mode === 'edit') firstField.current?.focus() }, [mode])
  useEffect(() => { if (server.stale || server.auth !== 'ready' || externallyDisabled) { request.cancel(); setReview(undefined); setMode('home') } }, [server.stale, server.auth, externallyDisabled])
  const back = () => { request.setError(undefined); setReview(undefined); setMode('home'); requestAnimationFrame(() => home.current?.focus()) }
  const prepare = (event: FormEvent) => {
    event.preventDefault(); request.setError(undefined)
    if (!editable) return
    if (!routeAddress(address.trim()) || !routeKey(pin.trim().toLowerCase())) { request.setError('invalidCandidate'); return }
    setReview({ kind: 'add', candidate: { address: address.trim(), certificateSHA256: pin.trim().toLowerCase(), scope } }); setMode('review')
  }
  const commit = async () => {
    if (!review || !editable || request.pending.current) return
    const read = (value: unknown) => { const result = value as { saved?: boolean; restartRequired?: boolean }; if (result?.saved !== true || result.restartRequired !== true) throw new Error('invalid_response'); return true }
    const result = review.kind === 'add'
      ? await request.call('lan.routes.add', { address: review.candidate.address, certificateSHA256: review.candidate.certificateSHA256, scope: review.candidate.scope }, read)
      : await request.call('lan.routes.remove', { candidateId: review.candidate.candidateId! }, read)
    if (result) { back(); setStatus('saved'); setAddress(''); setPin(''); await load() }
  }
  return <div className="route-panel form-stack"><p className="small muted">{r('purpose')}</p><p className="small muted">{r('offline')}</p>{request.error && <ErrorBanner message={r(request.error)} t={t} />}{status && <p role="status">{r(status)}</p>}
    {mode === 'home' && <><h4 ref={home} tabIndex={-1}>{r('candidates')}</h4>{!list ? <Button onClick={load} busy={request.busy} disabled={server.stale}>{r('retry')}</Button> : <><ul className="route-list">{list.candidates.map(candidate => <li key={candidate.candidateId}><Candidate candidate={candidate} locale={locale} />{candidate.candidateId === list.primaryCandidateId ? <Badge>{r('primary')}</Badge> : <Button disabled={!editable} onClick={() => { setStatus(undefined); setReview({ kind: 'remove', candidate }); setMode('review') }}>{r('remove')}</Button>}</li>)}</ul>{!list.candidates.length && <p>{r('empty')}</p>}<p className="small muted">{r('maximum')}</p><div className="route-actions"><Button disabled={!editable || !list.candidates.length} onClick={() => { setStatus(undefined); setMode('edit') }}>{r('add')}</Button><Button disabled={blocked} onClick={load}>{r('refresh')}</Button></div></>}</>}
    {mode === 'edit' && <form className="form-stack" onSubmit={prepare}><label className="field">{r('address')}<input ref={firstField} value={address} onChange={event => setAddress(event.target.value)} autoComplete="off" spellCheck={false} maxLength={80} required /></label><label className="field">{r('pin')}<input className="code-value" value={pin} onChange={event => setPin(event.target.value)} autoComplete="off" spellCheck={false} maxLength={64} required /></label><label className="field">{r('scope')}<select value={scope} onChange={event => setScope(event.target.value as typeof scope)}><option value="local">{r('local')}</option><option value="external">{r('external')}</option></select></label><p className="small muted">{r('scopeHint')}</p><div className="route-actions"><Button type="button" onClick={back}>{r('cancel')}</Button><Button type="submit" disabled={!editable}>{r('candidateReview')}</Button></div></form>}
    {mode === 'review' && review && <section className="route-review" aria-label={r('candidateReview')}><h4 ref={heading} tabIndex={-1}>{r('candidateReview')}</h4><Candidate candidate={review.candidate} locale={locale} /><p className="small muted">{r(review.kind === 'add' ? 'addImpact' : 'removeImpact')}</p><div className="route-actions"><Button disabled={request.busy} onClick={back}>{r('cancel')}</Button>{review.kind === 'add' && <Button disabled={request.busy} onClick={() => { setReview(undefined); setMode('edit') }}>{r('back')}</Button>}<Button variant={review.kind === 'add' ? 'primary' : 'danger'} disabled={!editable} busy={request.busy} onClick={commit}>{r(review.kind === 'add' ? 'save' : 'confirmRemove')}</Button></div></section>}
  </div>
}
export function PeerLanRoutes(props: Props & { peer: api.Peer }) {
  return <RouteDisclosure title={routeTranslator(props.locale)('routes')}><PeerRoutes key={`${props.peer.id}:${props.state.lan?.publicKey}:${props.state.settings?.network}`} {...props} /></RouteDisclosure>
}
function PeerRoutes({ server, state, peer, locale, t }: Props & { peer: api.Peer }) {
  const r = routeTranslator(locale), request = useRouteRequest(server)
  const [list, setList] = useState<api.LanPeerRoutes>()
  const [mode, setMode] = useState<'home' | 'input' | 'review' | 'export' | 'revoke'>('home')
  const [review, setReview] = useState<api.LanRouteReview>()
  const [selected, setSelected] = useState<string[]>([]), [expiry, setExpiry] = useState('')
  const [status, setStatus] = useState<RouteTextKey>()
  const [approvalLifetime, setApprovalLifetime] = useState<api.LanRouteLifetime>('until-revoked')
  const [exportLifetime, setExportLifetime] = useState<api.LanRouteLifetime>('until-revoked')
  const [exportUntil, setExportUntil] = useState(() => localRouteExpiry(new Date(Date.now() + 7 * 86400000).toISOString()))
  const [exported, setExported] = useState<{ lifetime: api.LanRouteLifetime; expires: string | null }>()
  const [withdraw, setWithdraw] = useState(false)
  const [copyStatus, setCopyStatus] = useState<RouteTextKey>()
  const [now, setNow] = useState(Date.now)
  const secret = useRef(''), input = useRef<HTMLInputElement>(null), output = useRef<HTMLTextAreaElement>(null)
  const heading = useRef<HTMLHeadingElement>(null), home = useRef<HTMLHeadingElement>(null)
  const erase = () => { secret.current = ''; if (input.current) input.current.value = ''; if (output.current) output.current.value = '' }
  useLayoutEffect(() => () => erase(), [])
  const blocked = server.stale || server.auth !== 'ready' || request.busy || server.busy.has('application.stop')
  const disabled = blocked || list?.recoveryRequired === true
  const load = async () => { const next = await request.call('lan.routes.list', { peerId: peer.id }, readPeerRoutes); if (next) setList(next) }
  useEffect(() => { void load() }, [])
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 1000); return () => clearInterval(timer) }, [])
  useEffect(() => { if (mode !== 'home') return; const timer = setInterval(() => { if (document.visibilityState !== 'hidden' && !request.pending.current) void load() }, 10000); return () => clearInterval(timer) }, [mode, server.stale, server.auth])
  useLayoutEffect(() => { if (mode === 'input') input.current?.focus(); else if (mode !== 'home') heading.current?.focus() }, [mode, review])
  const clear = () => { request.cancel(); erase(); setReview(undefined); setSelected([]); setExported(undefined); setExportLifetime('until-revoked'); setWithdraw(false); setCopyStatus(undefined); request.setError(undefined) }
  const back = () => { clear(); setMode('home'); requestAnimationFrame(() => home.current?.focus()) }
  useEffect(() => { if (server.stale || server.auth !== 'ready') { clear(); setMode('home') } }, [server.stale, server.auth])
  useEffect(() => { if (exported && !currentRoutePermission(exported, now)) { erase(); setExported(undefined); setStatus('expired') } }, [now, exported])
  const beginReview = (next: api.LanRouteReview) => {
    if (!currentRoutePermission(next, Date.now())) { erase(); request.setError('expired'); return }
    setReview(next); setSelected([]); setApprovalLifetime(next.lifetime === 'finite' ? 'finite' : 'until-revoked'); setExpiry(localRouteExpiry(new Date(Math.min(next.expires ? Date.parse(next.expires) : Infinity, Date.now() + 7 * 86400000)).toISOString())); setMode('review')
  }
  const inspect = async (event: FormEvent) => {
    event.preventDefault()
    if (disabled || request.pending.current) return
    const update = input.current?.value.trim() || ''
    if (!update || api.messageByteLength(update) > 65536) { request.setError('invalid'); return }
    secret.current = update
    const next = await request.call('lan.routes.inspect', { peerId: peer.id, update }, value => readRouteReview(value, peer.id, state.lan?.publicKey))
    if (next) beginReview(next)
  }
  const savedReview = async () => {
    if (disabled) return
    clear(); setStatus(undefined)
    const next = await request.call('lan.routes.review', { peerId: peer.id }, value => readRouteReview(value, peer.id, state.lan?.publicKey))
    if (next) beginReview(next)
  }
  const approve = async (event: FormEvent) => {
    event.preventDefault()
    if (!review || disabled || request.pending.current) return
    if (review.candidates.length && !selected.length) { request.setError('selectionRequired'); return }
    const lifetime = review.candidates.length ? approvalLifetime : review.lifetime
    if (!['finite', 'until-revoked'].includes(lifetime) || !currentRoutePermission(review, Date.now()) || lifetime === 'until-revoked' && review.lifetime !== 'until-revoked') { request.setError('expiryInvalid'); return }
    const expires = lifetime === 'finite' ? new Date(review.candidates.length ? expiry : review.expires!).getTime() : undefined
    if (expires !== undefined && (!Number.isFinite(expires) || expires <= Date.now() || review.expires !== null && expires > Date.parse(review.expires))) { request.setError('expiryInvalid'); return }
    const payload = { peerId: peer.id, digest: review.digest, candidateIds: selected, lifetime, ...(expires !== undefined ? { expires: new Date(expires).toISOString() } : {}) }
    const update = secret.current; erase(); setReview(undefined); setMode('home')
    const read = (value: unknown) => {
      const next = readPeerRoutes(value)
      if (!review.candidates.length && (next.legacy || next.candidates.length || next.approvals.length || next.permittedIds.length || next.receivedSequence !== review.sequence)) throw new Error('invalid_response')
      return next
    }
    const next = update ? await request.call('lan.routes.apply', { ...payload, update }, read) : await request.call('lan.routes.approve', payload, read)
    if (next) { setList(next); setStatus(review.candidates.length ? 'approvedResult' : 'withdrawalResult'); home.current?.focus(); void server.refresh(true) }
    else { setList(undefined); setStatus(undefined) }
  }
  const createExport = async () => {
    if (disabled || request.pending.current || exported) return
    setCopyStatus(undefined)
    if (!['finite', 'until-revoked'].includes(exportLifetime)) { request.setError('exportExpiryInvalid'); return }
    const ttlSeconds = exportLifetime === 'finite' ? Math.floor((new Date(exportUntil).getTime() - Date.now()) / 1000) : undefined
    if (ttlSeconds !== undefined && (!Number.isSafeInteger(ttlSeconds) || ttlSeconds < 1)) { request.setError('exportExpiryInvalid'); return }
    const next = await request.call('lan.routes.export', { peerId: peer.id, lifetime: exportLifetime, ...(ttlSeconds !== undefined ? { ttlSeconds } : {}), ...(withdraw ? { withdraw: true } : {}) }, value => {
      const result = value as { update?: string; lifetime?: api.LanRouteLifetime; expires?: string | null; peerId?: string }
      try {
        if (!result || result.peerId !== peer.id || result.lifetime !== exportLifetime || typeof result.update !== 'string' || !result.update || api.messageByteLength(result.update) > 65536 || !output.current) throw new Error('invalid_response')
        const lifetime = readRouteLifetime(result)
        if (!currentRoutePermission(lifetime, Date.now())) throw new Error('invalid_response')
        output.current.value = result.update
        return lifetime
      } finally { if (result) result.update = '' }
    })
    if (next) setExported(next)
  }
  const copy = async () => {
    if (!output.current?.value || !exported || !currentRoutePermission(exported, Date.now())) return
    try { await navigator.clipboard.writeText(output.current.value); setCopyStatus('copied') }
    catch { output.current?.focus(); output.current?.select(); setCopyStatus('copyManual') }
  }
  const revoke = async () => {
    if (disabled || mode !== 'revoke') return
    const next = await request.call('lan.routes.revoke', { peerId: peer.id, candidateIds: [] }, readPeerRoutes)
    if (next) { setList(next); back(); setStatus('revoked'); void server.refresh(true) }
  }
  const permitted = list?.permittedIds.filter(id => currentRoutePermission(list, now) && list.approvals.some(item => item.candidateId === id && currentRoutePermission(item, now))) || []
  const observed = peer.route || list?.observation
  const removedObservation = observed?.state === 'ready' && list && !list.legacy && (!observed.candidateId || !permitted.includes(observed.candidateId))
  const observation = currentRouteObservation(removedObservation ? undefined : observed, now, server.stale)
  const deadlines = list ? [list.expires, ...list.approvals.filter(item => permitted.includes(item.candidateId)).map(item => item.expires)].filter((value): value is string => value !== null) : []
  const nextExpiry = permitted.length && deadlines.length ? new Date(Math.min(...deadlines.map(value => Date.parse(value)))).toISOString() : ''
  return <div className="route-panel form-stack"><p className="small muted">{r('purpose')}</p><p className="small muted">{r('limits')}</p><p className="small muted">{r('certificateAvailability')}</p>{request.error && <ErrorBanner message={r(request.error)} t={t} />}{status && <p role="status">{r(status)}</p>}{list?.recoveryRequired && <p role="alert">{r('recovery')}</p>}
    {mode === 'home' && <><h4 ref={home} tabIndex={-1}>{r('approved')}</h4><dl className="route-observation"><dt>{r('observedPath')}</dt><dd>{t(observation.path)} · {r(`state_${observation.state}`)}{observation.state === 'ready' && observation.observedAt && <p className="small muted">{r('observedAt')}: <time dateTime={observation.observedAt}>{routeTimestamp(observation.observedAt, locale)}</time></p>}</dd></dl>{list ? <><Badge tone={permitted.length ? 'green' : 'neutral'}>{list.legacy ? r('legacy') : `${r('approved')}: ${permitted.length}`}</Badge>{list.legacy ? <p className="small muted">{r('legacyHint')}</p> : !permitted.length && <p>{r('none')}</p>}{permitted.length > 0 && <><p className="small">{nextExpiry ? <>{r('nextExpiry')}: <time dateTime={nextExpiry}>{routeTimestamp(nextExpiry, locale)}</time></> : r('noExpiry')}</p><ul className="route-list">{list.candidates.filter(item => permitted.includes(item.candidateId)).map(candidate => { const approval = list.approvals.find(item => item.candidateId === candidate.candidateId)!; return <li key={candidate.candidateId}><Candidate candidate={candidate} locale={locale} /><p className="small">{r('approvalLifetime')}: {approval.expires ? <time dateTime={approval.expires}>{routeTimestamp(approval.expires, locale)}</time> : r('untilRevoked')}</p></li> })}</ul></>}</> : <Button disabled={blocked} onClick={load}>{r('retry')}</Button>}<div className="route-actions"><Button disabled={disabled || !list} onClick={() => { clear(); setStatus(undefined); setMode('input') }}>{r('receive')}</Button><Button disabled={blocked} onClick={load}>{r('refresh')}</Button></div>{list && <details className="route-more"><summary>{r('export')}</summary><p className="small muted">{r('exportHint')}</p><Button disabled={disabled} onClick={() => { clear(); setStatus(undefined); setMode('export') }}>{r('export')}</Button></details>}{list && !list.legacy && <div className="route-actions"><Button disabled={disabled} onClick={savedReview}>{r('savedReview')}</Button><Button variant="ghost" disabled={disabled || !list.approvals.length} onClick={() => { clear(); setStatus(undefined); setMode('revoke') }}>{r('revoke')}</Button></div>}</>}
    {mode === 'input' && <form className="form-stack" onSubmit={inspect}><label className="field">{r('input')}<input ref={input} data-private="route-update" type="password" autoComplete="off" spellCheck={false} maxLength={65537} required onChange={() => { request.cancel(); secret.current = ''; request.setError(undefined) }} /><small className="muted">{r('inputHint')}</small></label><div className="route-actions"><Button type="button" onClick={back}>{r('cancel')}</Button><Button type="submit" disabled={disabled} busy={request.busy}>{r('inspect')}</Button></div></form>}
    {mode === 'review' && review && <form className="route-review form-stack" onSubmit={approve}><h4 ref={heading} tabIndex={-1}>{r(review.candidates.length ? 'review' : 'withdrawalReview')}</h4><dl><dt>{r('peer')}</dt><dd>{peer.name}<p className="code-value">{review.issuer}</p></dd><dt>{r('recipient')}</dt><dd className="code-value">{review.recipient}</dd><dt>{r('sequence')}</dt><dd>{review.sequence}</dd><dt>{r('exportLifetime')}</dt><dd>{review.expires !== null ? <time dateTime={review.expires}>{routeTimestamp(review.expires, locale)}</time> : r('untilRevoked')}</dd></dl><p className="small muted">{r(review.candidates.length ? 'authenticated' : 'withdrawalHint')}</p>{review.candidates.length > 0 && <><ul className="route-list">{review.candidates.map(candidate => <li key={candidate.candidateId}><label className="checkbox-field"><input type="checkbox" checked={selected.includes(candidate.candidateId)} disabled={disabled} onChange={event => setSelected(values => event.target.checked ? [...values, candidate.candidateId] : values.filter(id => id !== candidate.candidateId))} /><span>{r('select')} · <span className="code-value">{candidate.address}</span></span></label><Candidate candidate={candidate} locale={locale} /></li>)}</ul><label className="field">{r('approvalLifetime')}<select value={approvalLifetime} disabled={disabled} onChange={event => { setApprovalLifetime(event.target.value as api.LanRouteLifetime); setSelected([]); request.setError(undefined) }}>{review.lifetime === 'until-revoked' && <option value="until-revoked">{r('untilRevoked')}</option>}<option value="finite">{r('finite')}</option></select></label>{approvalLifetime === 'finite' ? <label className="field">{r('approvalExpiry')}<input type="datetime-local" value={expiry} max={review.expires ? localRouteExpiry(review.expires) : undefined} onChange={event => setExpiry(event.target.value)} required disabled={disabled} /><small className="muted">{r('expiryHint')}</small></label> : <p className="scope-note">{r('permanentHint')}</p>}{review.lifetime === 'finite' && <p className="small muted">{r('finiteOfferHint')}</p>}<p className="small muted">{r('scopeHint')}</p><p className="small muted">{r('unchanged')}</p></>}<p className="small muted">{r('approvalImpact')}</p>{!currentRoutePermission(review, now) && <p role="status">{r('expired')}</p>}<div className="route-actions"><Button type="button" onClick={back}>{r('cancel')}</Button><Button type="button" onClick={() => { clear(); setMode('input') }}>{r('back')}</Button><Button type="submit" variant="primary" disabled={disabled || Boolean(review.candidates.length && !selected.length) || !currentRoutePermission(review, now)}>{r(review.candidates.length ? approvalLifetime === 'until-revoked' ? 'applyPermanent' : 'apply' : 'applyWithdrawal')}</Button></div></form>}
    {mode === 'export' && <section className="form-stack"><h4 ref={heading} tabIndex={-1}>{r('export')}</h4><p>{peer.name}</p><p className="code-value">{peer.id}</p><p className="small muted">{r('exportHint')}</p><label className="field">{r('exportLifetime')}<select value={exportLifetime} disabled={blocked || Boolean(exported)} onChange={event => { erase(); setExportLifetime(event.target.value as api.LanRouteLifetime); request.setError(undefined) }}><option value="until-revoked">{r('untilRevoked')}</option><option value="finite">{r('finite')}</option></select></label>{exportLifetime === 'finite' ? <label className="field">{r('offerExpiry')}<input type="datetime-local" value={exportUntil} onChange={event => { erase(); setExportUntil(event.target.value) }} disabled={blocked || Boolean(exported)} required /></label> : <p className="scope-note">{r('permanentExportHint')}</p>}<label className="checkbox-field"><input type="checkbox" checked={withdraw} disabled={blocked || Boolean(exported)} onChange={event => { erase(); setWithdraw(event.target.checked) }} /><span>{r('withdrawOption')}</span></label>{withdraw && <p className="small muted">{r('withdrawExportHint')}</p>}<label className="field">{r('input')}<textarea ref={output} data-private="route-update" className="code-value private-copy" readOnly autoComplete="off" spellCheck={false} rows={4} /></label>{exported && <p>{r('exportLifetime')}: {exported.expires ? <time dateTime={exported.expires}>{routeTimestamp(exported.expires, locale)}</time> : r('untilRevoked')}</p>}{copyStatus && <p role="status">{r(copyStatus)}</p>}<div className="route-actions"><Button onClick={back}>{r('hide')}</Button>{exported ? <Button onClick={copy}>{r('copy')}</Button> : <Button disabled={disabled} busy={request.busy} onClick={createExport}>{r(withdraw ? 'exportWithdrawal' : 'export')}</Button>}</div></section>}
    {mode === 'revoke' && <section className="route-review form-stack"><h4 ref={heading} tabIndex={-1}>{r('revokeReview')}</h4><p>{peer.name}</p><p className="code-value">{peer.id}</p><p>{r('revokeImpact')}</p><div className="route-actions"><Button disabled={request.busy} onClick={back}>{r('cancel')}</Button><Button variant="danger" disabled={disabled} busy={request.busy} onClick={revoke}>{r('confirmRevoke')}</Button></div></section>}
  </div>
}
