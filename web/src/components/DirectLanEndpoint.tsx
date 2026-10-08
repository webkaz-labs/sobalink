import { useEffect, useRef, useState } from 'react'
import type { Locale, State } from '../api'
import type { Server } from '../useServer'
import { endpointCommands, endpointDeadlineValid, endpointObject, endpointOperations, endpointReviewMatches, endpointReviewDeadlineValid, validEndpointStatus, validEndpointResult, nextEndpointRevision, type EndpointData, type EndpointOperation, type EndpointPayload } from '../direct-lan-endpoint'
import { endpointFieldLabel, endpointStateText, endpointText } from '../endpoint-i18n'
import { Button, useAlive } from './ui'

function Details({ data, locale }: { data: EndpointData; locale: Locale }) {
  // Values stay exact; no status field is promoted to transport/application success.
  return <dl className="endpoint-details">{Object.entries(data).filter(([key]) => key !== 'update').map(([key, value]) => <div key={key}><dt>{endpointFieldLabel(locale, key)}</dt><dd className="code-value">{endpointObject(value) ? <Details data={value} locale={locale} /> : Array.isArray(value) ? value.map((item, index) => <div key={index}>{endpointObject(item) ? <Details data={item} locale={locale} /> : String(item)}</div>) : key === 'state' || key === 'outcome' ? endpointStateText(locale, value) : String(value ?? '')}</dd></div>)}</dl>
}
export function DirectLanEndpoint({ server, state, locale, blocked }: { server: Server; state: State; locale: Locale; blocked: boolean }) {
  const e = (key: Parameters<typeof endpointText>[1]) => endpointText(locale, key)
  const [operation, setOperation] = useState<EndpointOperation>('move'), [peerId, setPeer] = useState('')
  const [endpoint, setEndpoint] = useState(''), [update, setUpdate] = useState(''), [lifetime, setLifetime] = useState('finite')
  const [expires, setExpires] = useState(''), [granted, setGranted] = useState(''), [deliver, setDeliver] = useState(false)
  const [approve, setApprove] = useState(false), [withdraw, setWithdraw] = useState(false), [cancelRecovery, setCancelRecovery] = useState(false)
  const [status, setStatus] = useState<EndpointData>(), [proof, setProof] = useState<EndpointData>()
  const [review, setReview] = useState<{ operation: EndpointOperation; input: EndpointPayload; data: EndpointData & { revision: string }; binding: string }>()
  const [result, setResult] = useState<EndpointData>(), [message, setMessage] = useState<'unknown' | 'invalid'>(), [consent, setConsent] = useState(false)
  const [uncertain, setUncertain] = useState(false), [pending, setPending] = useState(false), [now, setNow] = useState(Date.now)
  const alive = useAlive(), busy = useRef(false), epoch = useRef(0), selectionEpoch = useRef(0)
  const unavailable = blocked || server.stale || server.auth !== 'ready'
  const peers = state.directLAN?.peers?.map(peer => ({ id: peer.key, name: peer.name || peer.key })) ?? state.peers.filter(peer => peer.networks.includes('direct-lan')).map(peer => ({ id: peer.id, name: peer.name }))
  const binding = JSON.stringify([state.settings?.network, state.directLAN, server.auth, server.stale])
  const currentBinding = useRef(binding); currentBinding.current = binding
  const invalidate = () => { ++epoch.current; ++selectionEpoch.current; setReview(undefined); setConsent(false); setMessage(undefined); setResult(undefined) }
  useEffect(() => { ++epoch.current; setReview(undefined); setConsent(false); setProof(undefined) }, [binding])
  useEffect(() => {
    const discard = () => { ++epoch.current; ++selectionEpoch.current; setReview(undefined); setConsent(false); setProof(undefined) }
    window.addEventListener('popstate', discard); window.addEventListener('pagehide', discard)
    return () => { ++epoch.current; window.removeEventListener('popstate', discard); window.removeEventListener('pagehide', discard) }
  }, [])
  useEffect(() => { if (!review) return; const timer = setInterval(() => setNow(Date.now()), 1000); return () => clearInterval(timer) }, [review])
  const savedPeers = Array.isArray(status?.peers) ? status.peers.filter(endpointObject) : []
  const saved = savedPeers.find(peer => peer.peerId === peerId)
  const doRequest = async (action: () => Promise<void>, readOnly = false) => {
    if (busy.current || blocked || server.auth !== 'ready' || server.stale && !readOnly) return
    busy.current = true; setPending(true)
    try { await action() } finally { busy.current = false; if (alive.current) setPending(false) }
  }
  const refresh = () => doRequest(async () => {
    invalidate(); setProof(undefined)
    const generation = epoch.current
    const response = await server.run('direct-lan.endpoint.status', {})
    if (!alive.current || generation !== epoch.current) return
    if (!response || !validEndpointStatus(response.result)) { setStatus(undefined); setUncertain(true); setMessage('unknown'); return }
    setStatus(response.result); setUncertain(false)
  }, true)
  const inputFor = (): EndpointPayload | undefined => {
    const bounds = { lifetime, expires: lifetime === 'finite' ? expires : '' }
    if (operation === 'move') return { endpoint: endpoint.trim(), deliveries: deliver ? [{ peerId, lifetime, ...(lifetime === 'finite' ? { expires } : {}) }] : [] }
    if (operation === 'recover') return typeof status?.pendingTransactionId === 'string' ? { transactionId: status.pendingTransactionId, cancel: cancelRecovery } : undefined
    if (!peers.some(peer => peer.id === peerId)) return undefined
    if (operation === 'export') return { peerId, operation: withdraw ? 'withdraw' : 'set', ...bounds }
    if (operation === 'reexport' || operation === 'delivery') return { peerId }
    if (operation === 'revoke' || operation === 'disable-follow') return { peerId, action: operation }
    if (operation === 'follow') {
      const revision = nextEndpointRevision(saved?.authorityRevision)
      if (!revision || typeof saved?.scopeDigest !== 'string') return undefined
      return { peerId, action: 'grant-follow', follow: { scope_digest: saved.scopeDigest, revision, granted, ...bounds, active: true } }
    }
    const source = operation === 'import' ? proof : saved
    const signedEndpoint = operation === 'import' ? source?.endpoint : source?.signedEndpoint
    const input: EndpointPayload = { peerId, action: operation === 'import' ? 'receive' : 'reapprove', ...(operation === 'import' ? { update } : {}) }
    if (approve || operation === 'reapprove') {
      if (typeof source?.proofDigest !== 'string' || typeof signedEndpoint !== 'string' || !signedEndpoint) return undefined
      input.approval = { kind: 'exact', proof_digest: source.proofDigest, endpoint: signedEndpoint, follow_revision: '', granted, ...bounds }
    }
    return input
  }
  const inspect = () => doRequest(async () => {
    if (uncertain) return
    invalidate()
    const input = inputFor(), generation = epoch.current, localBinding = binding
    if (!input || !endpointDeadlineValid(input, Date.now())) { setMessage('invalid'); return }
    const response = await server.run(endpointCommands[operation][0], input)
    if (!alive.current || generation !== epoch.current || currentBinding.current !== localBinding) return
    if (!response) { setUncertain(true); setMessage('unknown'); return }
    if (!endpointReviewMatches(response.result, operation, input)) { setMessage('invalid'); return }
    if (operation === 'import') setProof(response.result)
    setReview({ operation, input, data: response.result, binding: localBinding }); setNow(Date.now())
  })
  const apply = () => doRequest(async () => {
    if (!review || !consent || review.binding !== currentBinding.current || !endpointReviewDeadlineValid(review.operation, review.input, review.data, Date.now())) { invalidate(); setMessage('invalid'); return }
    const approved = review, selection = selectionEpoch.current
    ++epoch.current
    // Record unresolved completion before dispatch, including navigation while awaiting.
    setUncertain(true)
    setReview(undefined); setConsent(false); setProof(undefined); setStatus(undefined); setResult(undefined)
    const response = await server.run(endpointCommands[approved.operation][1], { ...approved.input, expectedRevision: approved.data.revision })
    if (!alive.current) return
    if (selection !== selectionEpoch.current) { setMessage('unknown'); return }
    if (!response || !validEndpointResult(response.result, approved.operation, approved.input)) { setUncertain(true); setMessage('unknown'); return }
    setResult(response.result); setUncertain(false)
  })
  const needsLifetime = operation === 'export' || operation === 'follow' || operation === 'reapprove' || operation === 'import' && approve || operation === 'move' && deliver
  const needsGranted = operation === 'follow' || operation === 'reapprove' || operation === 'import' && approve
  return <details className="subsection endpoint-controls"><summary>{e('title')}</summary><div className="form-stack">
    <p className="small muted">{e('intro')}</p>
    <div><Button disabled={blocked || server.auth !== 'ready' || pending} onClick={() => void refresh()}>{e('refresh')}</Button></div>
    {unavailable && <p role="status">{e('stale')}</p>}
    {status ? <details><summary>{e('status')}: {endpointStateText(locale, status.state ?? 'saved_only')}</summary><Details data={status} locale={locale} /></details> : <p className="small muted">{e('noStatus')}</p>}
    <fieldset disabled={pending || unavailable} className="form-stack">
      <label className="field">{e('action')}<select value={operation} onChange={event => { invalidate(); setProof(undefined); setApprove(false); setOperation(event.target.value as EndpointOperation) }}>{endpointOperations.map(action => <option key={action} value={action}>{e(action)}</option>)}</select></label>
      {operation !== 'recover' && <label className="field">{e('peer')}<select value={peerId} onChange={event => { invalidate(); setProof(undefined); setApprove(false); setPeer(event.target.value) }}><option value="">{e('choose')}</option>{peers.map(peer => <option key={peer.id} value={peer.id}>{peer.name} · {peer.id}</option>)}</select></label>}
      {operation === 'move' && <><label className="field">{e('endpoint')}<input value={endpoint} onChange={event => { invalidate(); setEndpoint(event.target.value) }} autoComplete="off" spellCheck={false} /></label><label className="checkbox-field"><input type="checkbox" checked={deliver} onChange={event => { invalidate(); setDeliver(event.target.checked) }} />{e('deliveryChoice')}</label><p className="small muted">{e('noDelivery')}</p></>}
      {operation === 'import' && <><label className="field">{e('update')}<textarea value={update} maxLength={65536} rows={4} className="private-copy code-value" autoComplete="off" spellCheck={false} onChange={event => { invalidate(); setProof(undefined); setApprove(false); setUpdate(event.target.value) }} /></label><p className="small muted">{e('inspectHint')}</p>{proof && <label className="checkbox-field"><input type="checkbox" checked={approve} onChange={event => { invalidate(); setApprove(event.target.checked) }} />{e('approval')}</label>}</>}
      {operation === 'export' && <label className="field">{e('operation')}<select value={withdraw ? 'withdraw' : 'set'} onChange={event => { invalidate(); setWithdraw(event.target.value === 'withdraw') }}><option value="set">{e('set')}</option><option value="withdraw">{e('withdraw')}</option></select></label>}
      {needsLifetime && <><label className="field">{e('lifetime')}<select value={lifetime} onChange={event => { invalidate(); setLifetime(event.target.value) }}><option value="finite">{e('finite')}</option><option value="until-revoked">{e('permanent')}</option></select></label>{lifetime === 'finite' && <label className="field">{e('expires')}<input value={expires} placeholder="2030-01-01T12:00:00Z" onChange={event => { invalidate(); setExpires(event.target.value) }} /></label>}</>}
      {needsGranted && <label className="field">{e('granted')}<input value={granted} placeholder="2030-01-01T11:00:00Z" onChange={event => { invalidate(); setGranted(event.target.value) }} /></label>}
      {operation === 'recover' && <><p className="scope-note">{e('recovery')}</p><label className="checkbox-field"><input type="checkbox" checked={cancelRecovery} onChange={event => { invalidate(); setCancelRecovery(event.target.checked) }} />{e('cancelRecovery')}</label></>}
      <div><Button disabled={uncertain} onClick={() => void inspect()}>{operation === 'import' && !proof ? e('inspect') : e('review')}</Button></div>
    </fieldset>
    {review && <div className="invitation-card" role="region" aria-label={e('review')}><h4>{e(review.operation)}</h4><Details data={review.data} locale={locale} /><p className="scope-note">{e('impact')}</p><label className="checkbox-field"><input type="checkbox" checked={consent} onChange={event => setConsent(event.target.checked)} />{e('consent')}</label><div className="modal-actions"><Button onClick={invalidate}>{e('back')}</Button><Button variant="primary" disabled={pending || unavailable || !consent || review.binding !== binding || !endpointReviewDeadlineValid(review.operation, review.input, review.data, now) || review.data.outcome === 'review_required'} onClick={() => void apply()}>{e('apply')}</Button></div></div>}
    {pending && <p role="status">{e('waiting')}</p>}
    {message && <p role="alert">{e(message)}</p>}
    {result && <div role="status"><h4>{e('result')}</h4><Details data={result} locale={locale} />{typeof result.update === 'string' && <><p className="small muted">{e('private')}</p><label className="field">{e('update')}<textarea readOnly rows={4} className="private-copy code-value" value={result.update} autoComplete="off" spellCheck={false} /></label></>}</div>}
  </div></details>
}
