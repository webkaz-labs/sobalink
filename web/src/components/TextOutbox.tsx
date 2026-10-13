import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import { bytes } from '../i18n'
import { outboxDeadline } from '../outbox-i18n'
import { textQueueReason, textQueueText } from '../text-outbox-i18n'
import { canQueueText, newTextRequestID, pendingTextLocators, readTextPage, resolveTextLocator, retainTextLocator, textLocatorEvent, textTerminal, verifiedTextOutbox, type PendingText, type TextOutboxRecord } from '../text-outbox'
import type { Server } from '../useServer'
import { Button } from './ui'

function TextReview({ record, state, locale }: { record: TextOutboxRecord; state: api.State; locale: api.Locale }) {
  const t = (key: Parameters<typeof textQueueText>[1]) => textQueueText(locale, key)
  return <div className="text-queue-review"><p>{t('recipient')}: <strong>{state.peers.find(peer => peer.id === record.peerId)?.name || record.peerId}</strong><small className="code-value">{record.peerId}</small></p>
    <p><strong>{t('deadline')}: <time dateTime={record.expiresAt}>{outboxDeadline(record.expiresAt, locale)}</time></strong></p>
    {record.text !== undefined ? <div><strong>{t('body')}</strong><p className="text-queue-body">{record.text}</p></div> : <p>{t('bodyRemoved')}</p>}
    <p className="small">{bytes(record.textBytes, locale)}</p><p className="small">{t('request')}: <span className="code-value">{record.requestId}</span></p>
  </div>
}

// The original immutable request is kept separately from the editable composer.
// Neither a render, a network error, navigation nor a process restart allocates
// another identity. Only an explicit first/new preparation can do so.
export function useTextQueue({ peer, state, server, locale, text, onAccepted, onViewOutbox }: {
  peer: api.Peer; state: api.State; server: Server; locale: api.Locale; text: string
  onAccepted: (peerId: string, text: string) => void; onViewOutbox?: (requestId?: string) => void
}) {
  const t = (key: Parameters<typeof textQueueText>[1]) => textQueueText(locale, key)
  const [modes, setModes] = useState<Record<string, boolean>>({}), [pendingByPeer, setPendingByPeer] = useState<Record<string, PendingText | undefined>>({})
  const pendingRef = useRef(pendingByPeer); pendingRef.current = pendingByPeer
  const [hours, setHours] = useState(24), [consent, setConsent] = useState(false), [busy, setBusy] = useState(false), [accepted, setAccepted] = useState<Record<string, TextOutboxRecord | undefined>>({})
  const [locators, setLocators] = useState(pendingTextLocators), working = useRef(false), alive = useRef(true)
  const latest = useRef({ state, server, onAccepted }); latest.current = { state, server, onAccepted }
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  useEffect(() => { const refresh = () => setLocators(pendingTextLocators()); window.addEventListener(textLocatorEvent, refresh); return () => window.removeEventListener(textLocatorEvent, refresh) }, [])
  const pending = pendingByPeer[peer.id], queueMode = !!modes[peer.id]
  useEffect(() => { setConsent(false) }, [peer.id, pending?.phase, pending?.review?.reviewRevision, state.resourceCatalogProcessId])
  const supported = /^[a-f0-9]{64}$/.test(state.resourceCatalogProcessId || '')
  const ready = server.auth === 'ready' && !server.stale
  const enabled = ready && canQueueText(peer, state)
  const unresolved = locators.some(id => id !== pending?.requestId)
  const update = (value: PendingText | undefined, id = peer.id) => { pendingRef.current = { ...pendingRef.current, [id]: value }; if (alive.current) setPendingByPeer(pendingRef.current) }
  const liveReview = pending?.phase === 'prepared' && pending.review?.status === 'prepared' && pending.processId === state.resourceCatalogProcessId && Date.parse(pending.expiresAt) > Date.now() && Date.now() < Date.parse(pending.review.createdAt) + 15 * 60000
  const run = async (action: 'prepare' | 'commit' | 'status' | 'discard', retry = false) => {
    const current = pendingRef.current[peer.id]
    if (working.current || !ready || action !== 'prepare' && !current) return
    if (action === 'prepare' && (!enabled || unresolved || !text.trim() || api.messageByteLength(text) > api.exchangeBudgets(state).messageBytes || current && (!retry || current.failedAction !== 'prepare' || !['storage_busy', 'message_storage_busy', 'message_store_busy'].includes(current.reasonCode || '') || current.processId !== state.resourceCatalogProcessId))) return
    if (action === 'commit' && (!enabled || !liveReview || !consent) || action === 'discard' && (!current?.review || current.review.admission !== 'prepared' || current.processId !== state.resourceCatalogProcessId)) return
    const now = Date.now()
    let next: PendingText = current || { requestId: newTextRequestID(now), processId: state.resourceCatalogProcessId!, peerId: peer.id, text, expiresAt: new Date(now + hours * 3600000).toISOString(), phase: 'preparing' }
    working.current = true; setBusy(true); setConsent(false); retainTextLocator(next.requestId)
    next = { ...next, phase: action === 'prepare' ? 'preparing' : action === 'commit' ? 'committing' : next.phase, reasonCode: undefined }; update(next)
    try {
      let result: api.CommandResult
      if (action === 'prepare') result = await api.command('message.queue.prepare', { processId: next.processId, peerId: next.peerId, text: next.text, expiresAt: next.expiresAt }, next.requestId)
      else if (action === 'commit') result = await api.command('message.queue.commit', { outboxId: next.requestId, processId: next.processId, reviewRevision: next.review!.reviewRevision, confirm: true })
      else if (action === 'discard') result = await api.command('message.queue.discard', { outboxId: next.requestId, processId: next.processId })
      else result = await api.command('message.queue.status', { outboxId: next.requestId })
      const record = await verifiedTextOutbox(result.result, { requestId: next.requestId, peerId: next.peerId, processId: next.processId, text: next.text, expiresAt: next.expiresAt, review: next.review, detail: true })
      if (action === 'commit' && record.admission !== 'enqueued' || action === 'discard' && record.status !== 'discarded') throw new api.ApiError('message_invalid_response', '')
      if (record.admission === 'enqueued' || record.status === 'discarded') {
        resolveTextLocator(next.requestId)
        if (alive.current) {
          update(undefined, next.peerId)
          if (record.admission === 'enqueued') { setAccepted(values => ({ ...values, [next.peerId]: record })); latest.current.onAccepted(next.peerId, next.text) }
        }
        await latest.current.server.refresh(true)
      } else if (alive.current) update({ ...next, phase: 'prepared', review: record, failedAction: undefined }, next.peerId)
    } catch (error) {
      if (alive.current) {
        const code = error instanceof api.ApiError ? error.code : 'message_invalid_response'
        // Only an authoritative healthy not-found allows a fresh review. A
        // retired/uncertain key never gains that affordance.
        update({ ...next, phase: action === 'status' && code === 'message_not_found' ? 'absent' : 'unknown', failedAction: action === 'status' ? next.failedAction : action, reasonCode: code }, next.peerId)
        if (code === 'unauthenticated') latest.current.server.handleError(error)
      }
    } finally { working.current = false; if (alive.current) setBusy(false) }
  }
  const panel = <>
    {supported && <section className="outbox-mode" aria-label={t('mode')}><fieldset disabled={busy || !!pending}><legend>{t('mode')}</legend>
      <label><input type="radio" name={`text-mode-${peer.id}`} checked={!queueMode} onChange={() => setModes(values => ({ ...values, [peer.id]: false }))} />{t('immediate')}</label>
      <label><input type="radio" name={`text-mode-${peer.id}`} checked={queueMode} disabled={!enabled} onChange={() => setModes(values => ({ ...values, [peer.id]: true }))} />{t('queue')}</label>
    </fieldset>{!canQueueText(peer, state) && <p className="small">{t('unavailable')}</p>}
    {queueMode && !pending && <><label className="field">{t('deadline')}<select value={hours} disabled={busy} onChange={event => setHours(Number(event.target.value))}><option value={1}>{t('hour')}</option><option value={24}>{t('day')}</option><option value={168}>{t('week')}</option></select></label><p className="small">{t('prepareOnly')}</p></>}
    {queueMode && unresolved && !pending && <p role="status">{t('unresolved')}</p>}
    {onViewOutbox && (queueMode || pending || locators.length > 0) && <Button variant="ghost" onClick={() => onViewOutbox(pending?.requestId || accepted[peer.id]?.requestId)}>{t('view')}</Button>}</section>}
    {pending && <section className="outbox-preparation" aria-label={t('review')}>
      {pending.review ? <TextReview record={pending.review} state={state} locale={locale} /> : <><p>{t('recipient')}: {peer.name}<small className="code-value">{pending.peerId}</small></p><p className="text-queue-body">{pending.text}</p><p>{t('deadline')}: {outboxDeadline(pending.expiresAt, locale)}</p><p className="small code-value">{pending.requestId}</p></>}
      <p role="status">{t(pending.phase === 'absent' ? 'absent' : pending.phase === 'unknown' ? 'uncertain' : pending.phase === 'preparing' ? 'preparing' : pending.phase === 'committing' ? 'committing' : liveReview ? 'prepareOnly' : 'expiredReview')}</p>
      {pending.reasonCode && <p>{textQueueReason(locale, pending.reasonCode)}</p>}
      {liveReview && <label className="text-queue-consent"><input type="checkbox" checked={consent} disabled={busy || !enabled} onChange={event => setConsent(event.target.checked)} />{t('consent')}</label>}
      <div className="outbox-actions">
        {liveReview && <Button variant="primary" disabled={busy || !enabled || !consent} busy={busy} onClick={() => void run('commit')}>{t('commit')}</Button>}
        {pending.review?.admission === 'prepared' && pending.processId === state.resourceCatalogProcessId && <Button disabled={busy || !ready} onClick={() => void run('discard')}>{t('discard')}</Button>}
        <Button disabled={busy || !ready} onClick={() => void run('status')}>{t('check')}</Button>
        {pending.failedAction === 'prepare' && ['storage_busy', 'message_storage_busy', 'message_store_busy'].includes(pending.reasonCode || '') && pending.processId === state.resourceCatalogProcessId && <Button disabled={busy || !enabled} onClick={() => void run('prepare', true)}>{t('retryPrepare')}</Button>}
        {pending.phase === 'absent' && <Button disabled={busy} onClick={() => { resolveTextLocator(pending.requestId); update(undefined); setConsent(false) }}>{t('newReview')}</Button>}
      </div>
    </section>}
    {accepted[peer.id] && <div role="status"><p>{t('work')}: {t(accepted[peer.id]!.status)} · {t('outcome')}: {t(accepted[peer.id]!.deliveryOutcome)}</p>{accepted[peer.id]!.deliveryOutcome === 'not_attempted' && !textTerminal(accepted[peer.id]!) && <p>{t('localOnly')}</p>}</div>}
  </>
  return { panel, queueMode, locked: !!pending, busy, supported, canPrepare: enabled && !unresolved && !pending && !busy && !!text.trim() && api.messageByteLength(text) <= api.exchangeBudgets(state).messageBytes, prepare: () => run('prepare') }
}

// Both list and detail readers return normalized metadata. A body is valid
// only for that exact metadata revision and the Core process that verified it.
function textRecordRevision(record: TextOutboxRecord) {
  const { text: _text, ...metadata } = record
  return JSON.stringify(metadata)
}

function TextQueueCard({ initial, state, server, locale, onChange }: { initial: TextOutboxRecord; state: api.State; server: Server; locale: api.Locale; onChange: (record: TextOutboxRecord) => void }) {
  const t = (key: Parameters<typeof textQueueText>[1]) => textQueueText(locale, key)
  const [inspection, setInspection] = useState<{ record: TextOutboxRecord; processId?: string } | null>(null), [code, setCode] = useState(''), [busy, setBusy] = useState(false), [consent, setConsent] = useState(false)
  const working = useRef(false), alive = useRef(true)
  const revision = textRecordRevision(initial), processId = state.resourceCatalogProcessId
  const current = JSON.stringify([processId, revision]), latest = useRef({ current })
  if (latest.current.current !== current) latest.current = { current }
  const observed = latest.current
  const detail = inspection?.processId === processId && inspection && textRecordRevision(inspection.record) === revision ? inspection.record : null
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  useEffect(() => {
    setConsent(false)
    setInspection(value => value?.processId === processId && value && textRecordRevision(value.record) === revision ? value : null)
  }, [revision, processId])
  const record = detail || initial, ready = server.auth === 'ready' && !server.stale
  const prepared = record.status === 'prepared' && record.admission === 'prepared'
  const peer = state.peers.find(p => p.id === record.peerId)
  const live = record.processId === state.resourceCatalogProcessId && Date.parse(record.expiresAt) > Date.now() && Date.now() < Date.parse(record.createdAt) + 15 * 60000
  const act = async (action: 'status' | 'commit' | 'discard' | 'cancel') => {
    if (working.current || !ready || latest.current !== observed || action === 'commit' && (!detail || !consent || !live || !peer || !canQueueText(peer, state)) || action === 'discard' && (!prepared || record.processId !== state.resourceCatalogProcessId)) return
    const started = observed
    working.current = true; setBusy(true); setConsent(false); setCode(''); retainTextLocator(record.requestId)
    try {
      const result = action === 'status' ? await api.command('message.queue.status', { outboxId: record.requestId }) : action === 'cancel' ? await api.command('message.queue.cancel', { outboxId: record.requestId }) : action === 'discard' ? await api.command('message.queue.discard', { outboxId: record.requestId, processId: record.processId! }) : await api.command('message.queue.commit', { outboxId: record.requestId, processId: record.processId!, reviewRevision: record.reviewRevision, confirm: true })
      const value = await verifiedTextOutbox(result.result, { requestId: record.requestId, review: record, detail: true })
      if (action === 'commit' && value.admission !== 'enqueued' || action === 'discard' && value.status !== 'discarded' || action === 'cancel' && !['cancel_requested', 'cancelled', 'completed', 'expired'].includes(value.status)) throw new api.ApiError('message_invalid_response', '')
      if (!alive.current || latest.current !== started) return
      if (value.admission === 'enqueued' || value.status === 'discarded') resolveTextLocator(record.requestId)
      onChange(value); setInspection({ record: value, processId })
      if (action !== 'status') await server.refresh(true)
    } catch (error) { if (alive.current && latest.current === started) { setCode(error instanceof api.ApiError ? error.code : 'message_invalid_response'); if (error instanceof api.ApiError && error.code === 'unauthenticated') server.handleError(error) } }
    finally { working.current = false; if (alive.current) setBusy(false) }
  }
  return <article className="outbox-card" tabIndex={-1} data-text-request-id={record.requestId} aria-label={`${t('title')}: ${peer?.name || record.peerId}`}>
    <h3>{peer?.name || record.peerId}</h3><p role="status">{t('work')}: {t(record.status)} · {t('outcome')}: {t(record.deliveryOutcome)}</p>
    {record.admission === 'enqueued' && record.deliveryOutcome === 'not_attempted' && <p>{t('localOnly')}</p>}
    {record.reasonCode && <p>{textQueueReason(locale, record.reasonCode)}</p>}
    <p>{t('deadline')}: <time dateTime={record.expiresAt}>{outboxDeadline(record.expiresAt, locale)}</time></p><p className="small code-value">{record.requestId}</p>
    {detail && <TextReview record={detail} state={state} locale={locale} />}
    {record.deliveryOutcome === 'confirmed' && <p>{t('receipt')}</p>}
    {record.receipt && <time dateTime={record.receipt.savedAt}>{outboxDeadline(record.receipt.savedAt, locale)}</time>}
    {code && <div role="status"><p>{t('uncertain')}</p><p>{textQueueReason(locale, code)}</p></div>}
    {prepared && !live && <p>{t('expiredReview')}</p>}
    {prepared && live && detail?.text !== undefined && <label className="text-queue-consent"><input type="checkbox" checked={consent} disabled={busy || !ready || !!code} onChange={event => setConsent(event.target.checked)} />{t('consent')}</label>}
    {record.admission === 'enqueued' && !textTerminal(record) && <p>{t('stopHint')}</p>}
    <div className="outbox-actions"><Button disabled={busy || !ready} onClick={() => void act('status')}>{t(detail ? 'check' : 'details')}</Button>
      {prepared && live && detail?.text !== undefined && <Button variant="primary" disabled={busy || !ready || !consent || !!code || !peer || !canQueueText(peer, state)} onClick={() => void act('commit')}>{t('commit')}</Button>}
      {prepared && record.processId === state.resourceCatalogProcessId && <Button disabled={busy || !ready || !!code} onClick={() => void act('discard')}>{t('discard')}</Button>}
      {record.admission === 'enqueued' && !textTerminal(record) && <Button disabled={busy || !ready || record.status === 'cancel_requested'} onClick={() => void act('cancel')}>{t('stop')}</Button>}
    </div>
  </article>
}

export function TextOutboxSection({ state, server, locale, focusRequest }: { state: api.State; server: Server; locale: api.Locale; focusRequest?: string }) {
  const t = (key: Parameters<typeof textQueueText>[1]) => textQueueText(locale, key)
  const [records, setRecords] = useState<TextOutboxRecord[]>([]), [page, setPage] = useState<ReturnType<typeof readTextPage> | null>(null), [code, setCode] = useState(''), [missing, setMissing] = useState<string[]>([]), [unresolved, setUnresolved] = useState<{ id: string; code: string }[]>([]), [busy, setBusy] = useState(false)
  const working = useRef(false), alive = useRef(true), latest = useRef(server), root = useRef<HTMLElement>(null), focused = useRef(''); latest.current = server
  useEffect(() => { if (focusRequest && focused.current !== focusRequest) { const card = Array.from(root.current?.querySelectorAll<HTMLElement>('[data-text-request-id]') || []).find(node => node.dataset.textRequestId === focusRequest); if (card) { card.focus(); focused.current = focusRequest } } }, [focusRequest, records])
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const refresh = useCallback(async (previous?: ReturnType<typeof readTextPage>) => {
    if (working.current || latest.current.auth !== 'ready' || latest.current.stale) return
    working.current = true; setBusy(true); setCode('')
    try {
      const result = await api.command('message.queue.list', previous?.nextCursor ? { cursor: previous.nextCursor, revision: previous.revision } : {}), next = readTextPage(result.result)
      if (previous && (next.revision !== previous.revision || next.total !== previous.total || next.status !== previous.status || next.items.some(r => r.requestId <= previous.nextCursor!))) throw new api.ApiError('message_invalid_response', '')
      if (!previous && !next.nextCursor && next.items.length !== next.total) throw new api.ApiError('message_invalid_response', '')
      const items = [...next.items], pending: { id: string; code: string }[] = [], absent: string[] = []
      if (!previous) for (const id of [...new Set([...pendingTextLocators(), ...(focusRequest ? [focusRequest] : [])])]) {
        const listed = items.find(r => r.requestId === id)
        if (listed?.admission === 'enqueued' || listed?.status === 'discarded') { resolveTextLocator(id); continue }
        try {
          const reply = await api.command('message.queue.status', { outboxId: id }), record = await verifiedTextOutbox(reply.result, { requestId: id, detail: true })
          if (!listed) items.push(record)
          if (record.admission === 'enqueued' || record.status === 'discarded') resolveTextLocator(id)
        } catch (error) { if (error instanceof api.ApiError && error.code === 'message_not_found') absent.push(id); else pending.push({ id, code: error instanceof api.ApiError ? error.code : 'message_invalid_response' }) }
      }
      if (alive.current) { setPage(next); setRecords(current => previous ? [...new Map([...current, ...items].map(r => [r.requestId, r])).values()] : items); if (!previous) { setMissing(absent); setUnresolved(pending) } }
    } catch (error) { if (alive.current) { setCode(error instanceof api.ApiError ? error.code : 'message_invalid_response'); if (error instanceof api.ApiError && error.code === 'unauthenticated') latest.current.handleError(error) } }
    finally { working.current = false; if (alive.current) setBusy(false) }
  }, [focusRequest])
  useEffect(() => { void refresh() }, [refresh, server.auth, server.stale, state.resourceCatalogProcessId])
  return <section ref={root} className="text-outbox-view" aria-label={t('title')}><h2>{t('title')}</h2><p>{t('receipt')}</p><p>{t('cleanup')}</p>
    <Button disabled={busy || server.stale || server.auth !== 'ready'} onClick={() => void refresh()}>{t('refresh')}</Button>
    {code && <p role="status">{textQueueReason(locale, code)}</p>}{page?.status === 'blocked' && <p role="status">{textQueueReason(locale, page.reasonCode)}</p>}
    {missing.map(id => <div key={id} role="status"><p>{t('absent')}</p><p className="small code-value">{id}</p><Button onClick={() => { resolveTextLocator(id); setMissing(values => values.filter(value => value !== id)) }}>{t('newReview')}</Button></div>)}
    {unresolved.map(({ id, code }) => <div key={id} role="status"><p>{t('uncertain')}</p><p>{textQueueReason(locale, code)}</p><p className="small code-value">{id}</p></div>)}
    {page && !records.length && !missing.length && !unresolved.length && <p>{t('empty')}</p>}
    {records.map(record => <TextQueueCard key={record.requestId} initial={record} state={state} server={server} locale={locale} onChange={value => setRecords(current => current.map(item => item.requestId === value.requestId ? value : item))} />)}
    {page?.nextCursor && <Button disabled={busy || server.stale || !!code} onClick={() => void refresh(page)}>{t('more')}</Button>}
  </section>
}
