import { TextOutboxSection } from './TextOutbox'
import { newOutboxRequestID } from '../outbox-id'
import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import { bytes, type Translate } from '../i18n'
import { outboxDeadline, outboxReason, outboxText, queueLabel } from '../outbox-i18n'
import { outboxTerminal, pendingOutboxLocators, readOutbox, readOutboxSnapshot, resolveOutboxLocator, retainOutboxLocator, type OutboxRecord, type PendingOutbox } from '../outbox'
import type { Server } from '../useServer'
import { Button, Modal } from './ui'

export function OutboxReview({ record, locale, peers }: { record: OutboxRecord; locale: api.Locale; peers: api.Peer[] }) {
  const t = (key: Parameters<typeof outboxText>[1]) => outboxText(locale, key)
  return <div className="outbox-review">
    <p>{t('recipients')}: {record.peerIds.map(id => peers.find(p => p.id === id)?.name || id).join(', ')}</p>
    <p>{record.entries.length} · {bytes(record.totalBytes, locale)}</p>
    <p><strong>{t('deadline')}: <time dateTime={record.expiresAt}>{outboxDeadline(record.expiresAt, locale)}</time></strong></p>
    <p>{t('consent')}</p>
    <details><summary>{t('details')}</summary><ul>{record.peerIds.map(id => <li key={id}>{peers.find(p => p.id === id)?.name || id}<small className="code-value">{id}</small></li>)}</ul><ul>{record.entries.map(e => <li key={e.id}>{e.path} · {bytes(e.size, locale)}<small className="code-value">{e.sha256}</small></li>)}</ul><p className="small">{t('request')}: <span className="code-value">{record.requestId}</span></p></details>
  </div>
}

export function QueuePreparation({ selection, peerIds, pending, onPending, onFinished, onReset, server, state, locale, enabled, onViewOutbox }: {
  selection: api.UploadSelection; peerIds: string[]; pending?: PendingOutbox; onPending: (value: PendingOutbox) => void; onFinished: (record: OutboxRecord) => void; onReset: () => void; server: Server; state: api.State; locale: api.Locale; enabled: boolean; onViewOutbox?: () => void
}) {
  const t = (key: Parameters<typeof outboxText>[1]) => outboxText(locale, key)
  const [absent, setAbsent] = useState(false), [reasonCode, setReasonCode] = useState<string | undefined>()
  const [hours, setHours] = useState(24), [busy, setBusy] = useState(false), [failed, setFailed] = useState(false)
  const working = useRef(false), alive = useRef(true), controller = useRef<AbortController | null>(null)
  const latest = useRef({ server, state, enabled }); latest.current = { server, state, enabled }
  useEffect(() => { alive.current = true; return () => { alive.current = false; controller.current?.abort() } }, [])
  const canReview = enabled && server.auth === 'ready' && !server.stale
  const liveReview = pending?.review && pending.phase === 'prepared' && pending.processId === state.resourceCatalogProcessId
  const run = async (action: 'prepare' | 'commit' | 'status' | 'discard') => {
    if (action !== 'prepare' && !pending) return
    if (working.current || server.auth !== 'ready' || server.stale || action === 'prepare' && (!canReview || pending) || action === 'commit' && (!canReview || !liveReview) || action === 'discard' && !liveReview) return
    const processId = pending?.processId || state.resourceCatalogProcessId
    if (!processId || !/^[a-f0-9]{64}$/.test(processId)) return
    const now = Date.now()
    // The draft's immediate request ID is deliberately unrelated. Allocate
    // this durable key only when the user begins its first preparation.
    let next: PendingOutbox = pending || { requestId: newOutboxRequestID(now), processId, expiresAt: new Date(now + hours * 3600000).toISOString(), phase: 'preparing' }
    const requestId = next.requestId
    working.current = true; setBusy(true); setFailed(false); setReasonCode(undefined); retainOutboxLocator(requestId)
    if (action === 'prepare' || action === 'commit') { next = { ...next, phase: action === 'prepare' ? 'preparing' : 'committing' }; onPending(next) }
    try {
      let response: api.CommandResult
      if (action === 'prepare') {
        const abort = new AbortController(); controller.current = abort
        response = await api.uploadOutbox(peerIds, processId, next.expiresAt, selection, requestId, () => {}, abort.signal)
      } else if (action === 'commit') {
        if (Date.parse(next.expiresAt) <= Date.now()) throw new api.ApiError('outbox_expired', '')
        response = await api.command('outbox.commit', { processId, outboxId: requestId, reviewRevision: next.review!.reviewRevision, confirm: true })
      } else if (action === 'discard') response = await api.command('outbox.discard', { processId, outboxId: requestId })
      else response = await api.command('outbox.status', { requestId })
      const record = readOutbox(response.result, { requestId, peerIds, selection, expiresAt: next.expiresAt, ...(action !== 'status' ? { processId } : {}), ...(next.review ? { review: next.review } : {}) })
      if (action === 'commit' && record.admission !== 'enqueued' || action === 'discard' && record.status !== 'discarded') throw new api.ApiError('outbox_invalid_response', '')
      if (!alive.current) return
      if (record.admission === 'enqueued' || record.status === 'discarded') { resolveOutboxLocator(requestId); onFinished(record); await latest.current.server.refresh(true); return }
      onPending({ ...next, phase: 'prepared', review: record })
      // Keep a prepared locator through reload: it is not a durable admission.
    } catch (error) {
      if (alive.current) {
        onPending({ ...next, phase: 'unknown' }); setFailed(true)
        if (error instanceof api.ApiError) {
          if (/^outbox_[a-z0-9_]+$/.test(error.code)) setReasonCode(error.code)
          if (action === 'status' && error.code === 'outbox_not_found') { setAbsent(true); resolveOutboxLocator(requestId) }
          if (error.code === 'unauthenticated') latest.current.server.handleError(error)
        }
      }
    } finally { working.current = false; controller.current = null; if (alive.current) setBusy(false) }
  }
  return <section className="outbox-preparation" aria-label={t('queue')}>
    {!pending && <><label className="field">{t('deadline')}<select value={hours} disabled={busy} onChange={event => setHours(Number(event.target.value))}><option value={1}>{t('hour')}</option><option value={24}>{t('day')}</option><option value={168}>{t('week')}</option></select></label><p className="small muted">{t('prepareOnly')}</p><Button variant="primary" disabled={!canReview || busy} busy={busy} onClick={() => void run('prepare')}>{t('review')}</Button></>}
    {pending && <>
      {pending.review && <OutboxReview record={pending.review} locale={locale} peers={state.peers} />}
      <p role="status">{t(absent ? 'absent' : pending.phase === 'unknown' || failed ? 'uncertain' : pending.phase === 'preparing' ? 'staging' : pending.phase === 'committing' ? 'awaiting_receipt' : pending.processId !== state.resourceCatalogProcessId ? 'expiredReview' : 'prepareOnly')}</p>
      {reasonCode && <p className="small">{outboxReason(locale, reasonCode)}</p>}
      <p className="small">{t('request')}: <span className="code-value">{pending.requestId}</span></p>
      <div className="outbox-actions">{absent && <Button disabled={busy} onClick={onReset}>{t('newReview')}</Button>}{liveReview && <><Button variant="primary" disabled={!canReview || busy} busy={busy} onClick={() => void run('commit')}>{queueLabel(pending.expiresAt, locale)}</Button><Button disabled={!canReview || busy} onClick={() => void run('discard')}>{t('discard')}</Button></>}
        <Button disabled={busy || server.auth !== 'ready' || server.stale} onClick={() => void run('status')}>{t('check')}</Button>{onViewOutbox && <Button onClick={onViewOutbox}>{t('view')}</Button>}
      </div>
    </>}
  </section>
}

export function OutboxDialog({ server, state, locale, t, onClose, focusTextRequest }: { server: Server; state: api.State; locale: api.Locale; t: Translate; onClose: () => void; focusTextRequest?: string }) {
  const ot = (key: Parameters<typeof outboxText>[1]) => outboxText(locale, key)
  const [absent, setAbsent] = useState<string[]>([])
  const [availability, setAvailability] = useState<{ status: string; reasonCode?: string } | null>(null)
  const [records, setRecords] = useState<OutboxRecord[] | null>(null), [locators, setLocators] = useState<string[]>(pendingOutboxLocators), [failure, setFailure] = useState(false), [busy, setBusy] = useState<string | null>(null)
  const alive = useRef(true), operation = useRef(false), fetchGeneration = useRef(0)
  const latest = useRef({ server, state }); latest.current = { server, state }
  useEffect(() => { alive.current = true; return () => { alive.current = false; ++fetchGeneration.current } }, [])
  const refresh = useCallback(async () => {
    const { server } = latest.current
    if (server.auth !== 'ready' || server.stale || operation.current) return
    const generation = ++fetchGeneration.current
    try {
      const result = await api.command('outbox.list', {}), snapshot = readOutboxSnapshot(result.result), next = snapshot.items
      const unresolved: string[] = [], missing: string[] = []
      for (const requestId of pendingOutboxLocators()) {
        const listed = next.find(r => r.requestId === requestId)
        if (listed) { if (listed.admission === 'enqueued' || listed.status === 'discarded') resolveOutboxLocator(requestId); continue }
        try { const response = await api.command('outbox.status', { requestId }); const recovered = readOutbox(response.result, { requestId }); next.push(recovered); if (recovered.admission === 'enqueued' || recovered.status === 'discarded') resolveOutboxLocator(requestId) } catch (error) { if (error instanceof api.ApiError && error.code === 'outbox_not_found') { missing.push(requestId); resolveOutboxLocator(requestId) } else unresolved.push(requestId) }
      }
      if (!alive.current || generation !== fetchGeneration.current) return
      setAvailability(snapshot); setAbsent(missing); setRecords(next); setLocators(unresolved); setFailure(false)
    } catch (error) {
      if (alive.current && generation === fetchGeneration.current) { setFailure(true); if (error instanceof api.ApiError && error.code === 'unauthenticated') latest.current.server.handleError(error) }
    }
  }, [])
  useEffect(() => { void refresh() }, [refresh, server.auth, server.stale, server.updatedAt, state.resourceCatalogProcessId])
  const act = async (record: OutboxRecord, action: 'cancel' | 'commit' | 'discard') => {
    if (operation.current || server.auth !== 'ready' || server.stale || failure || availability?.status !== 'ready') return
    const prepared = record.admission === 'prepared' && record.status === 'prepared'
    if (action !== 'cancel' && (!prepared || record.processId !== state.resourceCatalogProcessId) || action === 'commit' && (Date.parse(record.expiresAt) <= Date.now() || !record.peerIds.every(id => state.peers.some(p => p.id === id && api.canQueue(p))))) return
    operation.current = true; ++fetchGeneration.current; setBusy(record.outboxId); retainOutboxLocator(record.requestId)
    try {
      const result = action === 'cancel' ? await api.command('outbox.cancel', { outboxId: record.outboxId }) : action === 'discard' ? await api.command('outbox.discard', { processId: record.processId!, outboxId: record.outboxId }) : await api.command('outbox.commit', { processId: record.processId!, outboxId: record.outboxId, reviewRevision: record.reviewRevision, confirm: true })
      const updated = readOutbox(result.result, { requestId: record.requestId, review: record })
      if (action === 'commit' && updated.admission !== 'enqueued' || action === 'discard' && updated.status !== 'discarded' || action === 'cancel' && (!['cancel_requested', 'cancelled', 'completed', 'expired'].includes(updated.status) || updated.children.some(c => !['cancel_requested', 'cancelled', 'completed', 'expired'].includes(c.status)))) throw new api.ApiError('outbox_invalid_response', '')
      resolveOutboxLocator(record.requestId)
      if (alive.current) setRecords(current => current?.map(r => r.outboxId === updated.outboxId ? updated : r) || [updated])
      await latest.current.server.refresh(true)
    } catch (error) { if (alive.current) { setFailure(true); setLocators(pendingOutboxLocators()); if (error instanceof api.ApiError && error.code === 'unauthenticated') latest.current.server.handleError(error) } }
    finally { operation.current = false; if (alive.current) setBusy(null) }
  }
  return <Modal title={ot('title')} t={t} onClose={onClose} wide><div className="outbox-view">
    <h2>{ot('filesTitle')}</h2>
    {availability?.status === 'blocked_recovery' && <p role="status">{ot('blocked_recovery')}: {outboxReason(locale, availability.reasonCode)}</p>}
    <p>{ot('autoRetry')}</p><Button disabled={server.stale || !!busy} onClick={() => void refresh()}>{ot('refresh')}</Button>
    {absent.length > 0 && <div role="status"><p>{ot('absent')}</p>{absent.map(id => <p className="small code-value" key={id}>{id}</p>)}</div>}
    {(failure || locators.length > 0) && <div role="status"><p>{ot('uncertain')}</p>{locators.map(id => <p key={id} className="small code-value">{id}</p>)}</div>}
    {records?.length === 0 && locators.length === 0 && <p>{ot('empty')}</p>}
    {records?.map(record => {
      const prepared = record.admission === 'prepared' && record.status === 'prepared', sameProcess = record.processId === state.resourceCatalogProcessId
      const disabled = server.auth !== 'ready' || server.stale || !!busy || failure || availability?.status !== 'ready'
      const counts = { saved: record.children.filter(c => c.deliveryOutcome === 'confirmed').length, unknown: record.children.filter(c => c.deliveryOutcome === 'unknown' || c.deliveryOutcome === 'partial').length, waiting: record.children.filter(c => c.deliveryOutcome === 'not_attempted').length }
      return <article className="outbox-card" key={record.outboxId} aria-label={record.peerIds.map(id => state.peers.find(p => p.id === id)?.name || id).join(', ')}>
        <h3>{record.peerIds.map(id => state.peers.find(p => p.id === id)?.name || id).join(', ')}</h3>
        <p role="status">{ot('work')}: {ot(record.status)} · {ot('delivery')}: {ot(record.deliveryOutcome)}</p>
        {record.admission === 'enqueued' && record.deliveryOutcome === 'not_attempted' && <p>{ot('localOnly')}</p>}
        {record.reasonCode && <p>{outboxReason(locale, record.reasonCode)}</p>}
        <p>{ot('saved')}: {counts.saved} · {ot('unknownCount')}: {counts.unknown} · {ot('waiting')}: {counts.waiting}</p>
        {prepared ? <OutboxReview record={record} locale={locale} peers={state.peers} /> : <><p>{record.entries.length} · {bytes(record.totalBytes, locale)}</p><p>{ot('deadline')}: <time dateTime={record.expiresAt}>{outboxDeadline(record.expiresAt, locale)}</time></p></>}
        <details><summary>{ot('details')}</summary><p>{ot('receipt')}</p><p className="small">{ot('request')}: <span className="code-value">{record.requestId}</span></p>
          <ul>{record.children.map(child => <li key={child.deliveryId}><strong>{state.peers.find(p => p.id === child.peerId)?.name || child.peerId}</strong><p>{ot(child.status)} · {ot(child.deliveryOutcome)}</p>{child.reasonCode && <p>{outboxReason(locale, child.reasonCode)}</p>}<small className="code-value">{child.peerId} / {child.deliveryId}</small><ul>{child.files?.map(file => <li key={file.fileId}>{record.entries.find(e => e.id === file.fileId)?.path || file.fileId} · {ot('confirmed')}</li>)}</ul></li>)}</ul><ul>{record.entries.map(entry => <li key={entry.id}>{entry.path} · {bytes(entry.size, locale)}</li>)}</ul>
        </details>
        {prepared && !sameProcess && <p>{ot('expiredReview')}</p>}
        {record.admission === 'enqueued' && !outboxTerminal(record) && <p className="small muted">{ot('stopHint')}</p>}
        <div className="outbox-actions">{prepared && sameProcess && <><Button variant="primary" disabled={disabled || !record.peerIds.every(id => state.peers.some(p => p.id === id && api.canQueue(p)))} busy={busy === record.outboxId} onClick={() => void act(record, 'commit')}>{queueLabel(record.expiresAt, locale)}</Button><Button disabled={disabled} onClick={() => void act(record, 'discard')}>{ot('discard')}</Button></>}
          {record.admission === 'enqueued' && !outboxTerminal(record) && <Button disabled={disabled || record.status === 'cancel_requested'} busy={busy === record.outboxId} onClick={() => void act(record, 'cancel')}>{ot('stop')}</Button>}
        </div>
      </article>
    })}
    {/^[a-f0-9]{64}$/.test(state.resourceCatalogProcessId || '') && <TextOutboxSection state={state} server={server} locale={locale} focusRequest={focusTextRequest} />}
  </div></Modal>
}
