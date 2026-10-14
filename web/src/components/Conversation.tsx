import { useTextQueue } from './TextOutbox'
import { textQueueText } from '../text-outbox-i18n'
import { textOutboxStatuses, textRequestTime, type TextOutboxStatus } from '../text-outbox'
import { QueuePreparation } from './Outbox'
import { outboxReason, outboxText } from '../outbox-i18n'
import { pendingOutboxLocators, type PendingOutbox } from '../outbox'
import { DispatchTransfers } from './DispatchTransfers'
import { readDispatchResult, samePeers, type DispatchCommitted, type PendingDispatch } from '../dispatch'
import { PeerTransfers } from './PeerTransfers'
import { retryExpected } from '../peer-transfers'
import type { TransferFocus } from '../catalog/types'
import { currentTransfer } from '../catalog/workflow'
import { ReceiveRecoveryNotice } from './ReceiveRecovery'
import { peerPresenceKey } from '../peer-status'
import { useEffect, useId, useRef, useState, type Dispatch, type SetStateAction, type DragEvent, type KeyboardEvent } from 'react'
import * as api from '../api'
import { appendSelection, fromClipboardImages, fromDrop, fromFiles, removeSelection, validateSelection } from '../files'
import { bytes, errorDetail, errorText, timestamp, transferFailureText, type Translate, type TextKey } from '../i18n'
import { transferTranslator } from '../transfer-i18n'
import type { MessageBlock, Server } from '../useServer'
import { Badge, Button, ErrorBanner, Icon, IconButton, Modal } from './ui'

function TransferCard({ transfer, peer, t, locale, server, destination, setDestination }: { transfer: api.Transfer; peer: api.Peer; t: Translate; locale: api.Locale; server: Server; destination: string | undefined; setDestination: (value: string | undefined) => void }) {
  const ft = transferTranslator(locale)
  const incoming = transfer.direction === 'incoming'
  const offered = transfer.status === 'offered' || transfer.status === 'awaiting-acceptance'
  const active = ['queued', 'transferring', 'saving'].includes(transfer.status)
  const progress = transfer.totalBytes ? Math.min(100, Math.max(0, transfer.completedBytes / transfer.totalBytes * 100)) : 0
  const busy = server.busy.has(`transfer:${transfer.id}`)
  const entries = transfer.entries || []
  const entryStatus = (value: string) => {
    const statuses: Record<string, TextKey> = { pending: 'pending', receiving: 'transferring', failed: 'failed', saved: 'completed', cancelled: 'cancelled' }
    return statuses[value] ? t(statuses[value]) : value
  }
  const configuredDirectory = server.state?.settings?.receiveDirectory || server.state?.self.receiveDirectory || ''
  const [editingDestination, setEditingDestination] = useState(false)
  const [reviewingDiscard, setReviewingDiscard] = useState(false)
  const discardsSendingCopies = !incoming && transfer.status === 'failed'
  useEffect(() => { setReviewingDiscard(false) }, [transfer.id, transfer.direction, transfer.status])
  const selectedDestination = destination ?? configuredDirectory
  const expectedRetry = server.state && retryExpected(server.state, peer, transfer, server.stale, server.busy)
  const action = (name: 'transfer.accept' | 'transfer.decline' | 'transfer.cancel' | 'transfer.retry' | 'transfer.forget') => {
    if (name === 'transfer.retry' && (!expectedRetry || server.auth !== 'ready')) return
    // Keep the reviewed default explicit across refresh/navigation and manual
    // acceptance retries. A later default change must not retarget this batch.
    if (name === 'transfer.accept' && destination === undefined) setDestination(selectedDestination)
    return server.run(name, { transferId: transfer.id, ...(name === 'transfer.retry' ? { expected: expectedRetry } : {}), ...(name === 'transfer.accept' ? { destination: selectedDestination.trim() } : {}) }, `transfer:${transfer.id}`)
  }
  const discard = async () => { if (await action('transfer.forget')) setReviewingDiscard(false) }
  return <article className={`transfer-card ${incoming && offered ? 'incoming-offer' : ''}`} aria-label={`${transfer.name} · ${t(transfer.status)}`}>
    <div className="transfer-top"><div className="transfer-icon"><Icon name={incoming ? 'download' : 'upload'} size={22} /></div><div className="grow"><p className="eyebrow">{t(incoming ? offered ? 'incomingOffer' : 'incomingBatch' : 'outgoingOffer')}</p><h3>{transfer.name || `${entries.length} ${t(entries.length === 1 ? 'item' : 'items')}`}</h3><p className="small muted">{entries.length} {t(entries.length === 1 ? 'item' : 'items')}<span className="dot-separator">·</span>{bytes(transfer.totalBytes, locale)}</p></div><Badge tone={transfer.status === 'completed' ? 'green' : transfer.status === 'failed' ? 'red' : offered ? 'purple' : 'neutral'}>{t(transfer.status)}</Badge></div>
    {active && <div className="transfer-progress"><progress value={transfer.completedBytes} max={Math.max(1, transfer.totalBytes)} aria-label={t(transfer.status)} /><div><span>{bytes(transfer.completedBytes, locale)} / {bytes(transfer.totalBytes, locale)}</span><span>{Math.floor(progress)}%</span></div></div>}
    {transfer.error && <p className="field-error" role="status">{transferFailureText(transfer.error, t)}</p>}
    {entries.length > 0 && <details className="file-details"><summary>{t('showFiles')}<span>{entries.length}</span></summary><ul>{entries.map((entry, index) => <li key={entry.id || `${entry.path}:${index}`}><Icon name={entry.kind === 'directory' ? 'folder' : 'file'} size={15} /><div><span className="entry-path">{entry.path}</span>{entry.status && <small>{entryStatus(entry.status)}</small>}{entry.storedPath && <small>{t('storedAt')}: {entry.storedPath}</small>}{entry.error && <small className="field-error">{transferFailureText(entry.error, t)}</small>}</div><span>{entry.kind === 'file' ? bytes(entry.size, locale) : t('folders')}</span></li>)}</ul></details>}
    {incoming && offered && <><p className="small muted accept-hint">{t('acceptHint')}</p>{editingDestination || destination !== undefined || !configuredDirectory ? <div className="destination-field"><label className="field">{t('receiveDirectory')}<input value={selectedDestination} onChange={event => setDestination(event.target.value)} placeholder={t('directoryPlaceholder')} spellCheck={false} autoComplete="off" autoFocus={editingDestination} disabled={busy} /><small className="muted">{ft('batchDestinationHint')}</small></label>{configuredDirectory && <Button variant="ghost" disabled={busy} onClick={() => { setDestination(undefined); setEditingDestination(false) }}>{ft('useDefaultFolder')}</Button>}</div> : <div className="accept-destination"><p className="small muted">{t('receiveDirectory')}: <span className="code-value">{selectedDestination}</span></p><Button variant="ghost" disabled={busy} onClick={() => { setDestination(selectedDestination); setEditingDestination(true) }}>{ft('changeFolder')}</Button></div>}<div className="transfer-actions"><Button variant="ghost" onClick={() => action('transfer.decline')} disabled={busy}>{t('decline')}</Button><Button variant="primary" onClick={() => action('transfer.accept')} busy={busy} disabled={!api.canExchange(peer) || server.stale || api.receivingBlocked(server.state || {}) || !selectedDestination.trim()}><Icon name="download" size={16} />{t('acceptBatch')}</Button></div></>}
    {(active || (!incoming && offered)) && <div className="transfer-actions"><Button variant="ghost" onClick={() => action('transfer.cancel')} busy={busy}>{t('cancelTransfer')}</Button></div>}
    {transfer.status === 'failed' && <div className="transfer-actions"><Button onClick={() => action('transfer.retry')} busy={busy} disabled={!expectedRetry || server.auth !== 'ready'}><Icon name="refresh" size={16} />{t('retryTransfer')}</Button></div>}
    {['completed', 'cancelled', 'failed', 'declined'].includes(transfer.status) && <div className="forget-transfer"><small>{discardsSendingCopies ? ft('discardHint') : t('forgetHint')}</small><Button variant="ghost" busy={busy} onClick={() => { if (discardsSendingCopies) { server.setError(null); setReviewingDiscard(true) } else void action('transfer.forget') }}>{discardsSendingCopies ? ft('discardBatch') : t('forgetTransfer')}</Button></div>}
    {reviewingDiscard && discardsSendingCopies && <Modal title={ft('discardTitle')} t={t} onClose={() => { if (!busy) setReviewingDiscard(false) }}><div className="target-pill"><Icon name="upload" /><strong>{transfer.name || `${entries.length} ${t(entries.length === 1 ? 'item' : 'items')}`}</strong><span>{peer.name}</span></div><p>{ft('discardImpact')}</p><p className="small muted">{ft('discardPreservedFiles')}</p>{server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}<div className="modal-actions"><Button onClick={() => setReviewingDiscard(false)} disabled={busy}>{t('cancel')}</Button><Button variant="danger" busy={busy} onClick={() => void discard()}>{ft('discardConfirm')}</Button></div></Modal>}
  </article>
}

export interface DraftBatch { selection: api.UploadSelection; requestId: string; peerIds?: string[]; dispatch?: PendingDispatch; mode?: 'immediate' | 'queue'; outbox?: PendingOutbox }
export interface UploadActivity { peerId: string; loaded: number; total: number; cancel: () => void }
export function Conversation({ peer, state, t, locale, server, onTrust, drafts, setDrafts, batches, setBatches, receiveDestinations, setReceiveDestinations, onUploadChange, onReviewReceiving, onViewOutbox, focusTarget, retryResetToken = '' }: { onViewOutbox?: (requestId?: string) => void; retryResetToken?: string; focusTarget?: TransferFocus | null; onReviewReceiving?: () => void; peer: api.Peer; state: api.State; t: Translate; locale: api.Locale; server: Server; onTrust: () => void; drafts: Record<string, string>; setDrafts: Dispatch<SetStateAction<Record<string, string>>>; batches: Record<string, DraftBatch | undefined>; setBatches: Dispatch<SetStateAction<Record<string, DraftBatch | undefined>>>; receiveDestinations: Record<string, string | undefined>; setReceiveDestinations: Dispatch<SetStateAction<Record<string, string | undefined>>>; onUploadChange?: (activity: UploadActivity | null) => void }) {
  const ft = transferTranslator(locale)
  const ot = (key: Parameters<typeof outboxText>[1]) => outboxText(locale, key)
  const [modes, setModes] = useState<Record<string, 'immediate' | 'queue'>>({})
  const [unresolvedOutbox, setUnresolvedOutbox] = useState<string[]>(pendingOutboxLocators)
  useEffect(() => { const refresh = () => setUnresolvedOutbox(pendingOutboxLocators()); window.addEventListener('sobalink-outbox-locators', refresh); return () => window.removeEventListener('sobalink-outbox-locators', refresh) }, [])
  const [queuedNotice, setQueuedNotice] = useState<string | null>(null)
  const [collecting, setCollecting] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [uploading, setUploading] = useState<{ peerId: string; loaded: number; total: number } | null>(null)
  const [dispatchBusy, setDispatchBusy] = useState(false)
  const [abandonConfirmed, setAbandonConfirmed] = useState(false)
  const [dispatchReceipts, setDispatchReceipts] = useState<Record<string, DispatchCommitted | undefined>>({})
  const dispatchOperation = useRef<'preparing' | 'committing' | 'checking' | null>(null)
  const [messageBlocked, setMessageBlocked] = useState<MessageBlock | null>(null)
  const uploadController = useRef<AbortController | null>(null)
  const dragDepth = useRef(0)
  const filesRef = useRef<HTMLInputElement>(null)
  const folderRef = useRef<HTMLInputElement>(null)
  const composerId = useId()
  const composerRef = useRef<HTMLTextAreaElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const nearBottom = useRef(true)
  const pendingSend = useRef(new Set<string>())
  const collectionGeneration = useRef(0)
  const collectingRef = useRef(false)
  const alive = useRef(true)
  const batchesRef = useRef(batches)
  batchesRef.current = batches
  const stateRef = useRef(state)
  stateRef.current = state
  const focusedCatalogTarget = useRef<TransferFocus | null>(null)
  useEffect(() => {
    if (focusedCatalogTarget.current === focusTarget) return
    if (!focusTarget || focusTarget.peerId !== peer.id || !currentTransfer(state, server.stale, focusTarget)) return
    const card = Array.from(listRef.current?.querySelectorAll<HTMLElement>('[data-transfer-id]') || [])
      .find(node => node.dataset.transferId === focusTarget.id && node.dataset.transferDirection === focusTarget.direction)
    if (card) { card.focus(); focusedCatalogTarget.current = focusTarget }
  }, [focusTarget, peer.id, state, server.stale])
  const message = drafts[peer.id] || ''
  const batch = batches[peer.id]
  const budgets = api.exchangeBudgets(state)
  const maxFiles = budgets.batchEntries
  const maxBytes = budgets.batchBytes
  const pathLimits = { pathDepth: budgets.pathDepth, pathBytes: budgets.pathBytes }
  const allowed = api.canExchange(peer) && !server.stale
  const mode = batch?.mode || modes[peer.id] || 'immediate'
  const queueMode = mode === 'queue'
  const filesAllowed = !server.stale && (queueMode ? state.outboxReady === true && api.canQueue(peer) : api.canExchange(peer))
  const eligibleRecipient = (value: api.Peer) => queueMode ? state.outboxReady === true && api.canQueue(value) : api.canExchange(value)
  const recipientIds = batch?.peerIds || [peer.id]
  const dispatchMayHaveSent = batch?.dispatch?.phase === 'committing' || batch?.dispatch?.phase === 'commit-unknown'
  useEffect(() => { setAbandonConfirmed(false) }, [batch?.dispatch?.preparationId, batch?.dispatch?.phase])
  const recipientsReady = recipientIds.every(id => state.peers.some(value => value.id === id && eligibleRecipient(value)))
  const dispatchEpoch = `${peer.id}:${state.resourceCatalogProcessId}:${server.auth}:${server.stale}:${retryResetToken}`
  const dispatchLatest = useRef({ epoch: dispatchEpoch, recipientIds, state, server })
  dispatchLatest.current = { epoch: dispatchEpoch, recipientIds, state, server }
  useEffect(() => () => { if (dispatchOperation.current === 'preparing') uploadController.current?.abort() }, [dispatchEpoch])
  const sending = server.busy.has(`message:${peer.id}`)
  const messages = state.messages.filter(item => item.peerId === peer.id)
  const transfers = state.transfers.filter(item => item.peerId === peer.id)
  const timeline = [...messages.map(value => ({ type: 'message' as const, value })), ...transfers.map(value => ({ type: 'transfer' as const, value }))].sort((a, b) => a.value.createdAt.localeCompare(b.value.createdAt))
  useEffect(() => {
    let current = true
    void server.messageBlock(peer.id, message).then(code => { if (current) setMessageBlocked(code) })
    return () => { current = false }
  }, [peer.id, message, server.messageBlock, server.messageGuardRevision])
  useEffect(() => { alive.current = true; return () => { alive.current = false; uploadController.current?.abort(); ++collectionGeneration.current } }, [])
  useEffect(() => {
    const controller = uploadController.current
    onUploadChange?.(uploading ? { ...uploading, cancel: () => { if (uploadController.current === controller) controller?.abort() } } : null)
  }, [uploading, onUploadChange])
  useEffect(() => () => onUploadChange?.(null), [onUploadChange])
  useEffect(() => { nearBottom.current = true; setDragging(false); dragDepth.current = 0 }, [peer.id])
  useEffect(() => { if (nearBottom.current && listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight }, [timeline.length, peer.id])
  useEffect(() => {
    const list = listRef.current
    if (!list) return
    let frame = 0
    const resize = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => { if (nearBottom.current) list.scrollTop = list.scrollHeight })
    }
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(resize)
    observer?.observe(list)
    for (const child of Array.from(list.children)) observer?.observe(child)
    window.addEventListener('resize', resize)
    return () => { observer?.disconnect(); cancelAnimationFrame(frame); window.removeEventListener('resize', resize) }
  }, [timeline.length, peer.id])
  const setMessage = (value: string) => setDrafts(current => ({ ...current, [peer.id]: value }))
  const textQueue = useTextQueue({ peer, state, server, locale, text: message, onViewOutbox, onAccepted: (target, original) => setDrafts(current => current[target] === original ? { ...current, [target]: '' } : current) })
  const textAllowed = textQueue.queueMode ? textQueue.supported && !server.stale && peer.verified && peer.trusted && !peer.autosave?.paused : allowed
  const send = async () => {
    if (textQueue.locked || textQueue.busy) return
    if (textQueue.queueMode) { if (!messageBlocked) await textQueue.prepare(); return }
    if (!allowed || !message.trim() || sending || messageBlocked || pendingSend.current.has(peer.id)) return
    if (api.messageByteLength(message) > budgets.messageBytes) { server.setError({ code: 'messageLimit' }); return }
    const target = peer.id
    const text = message
    nearBottom.current = true
    pendingSend.current.add(target)
    try {
      const response = await server.run('message.send', { peerId: target, text }, `message:${target}`)
      if (response) { setDrafts(current => current[target] === text ? { ...current, [target]: '' } : current); nearBottom.current = true }
    } finally { pendingSend.current.delete(target) }
  }
  const keyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter always inserts a newline; composition confirmation must never send.
    if (event.key === 'Enter' && (event.ctrlKey || event.metaKey) && !event.nativeEvent.isComposing && event.keyCode !== 229) { event.preventDefault(); void send() }
  }
  const updateBatch = (target: string, next: DraftBatch | undefined) => {
    batchesRef.current = { ...batchesRef.current, [target]: next }
    setBatches(current => ({ ...current, [target]: next }))
  }
  const select = async (collect: () => Promise<api.UploadSelection> | api.UploadSelection) => {
    if (collectingRef.current || uploadController.current || (batchesRef.current[peer.id]?.dispatch || batchesRef.current[peer.id]?.outbox) || !filesAllowed) return
    const target = peer.id
    const generation = ++collectionGeneration.current
    collectingRef.current = true
    setCollecting(true); server.setError(null)
    try {
      const addition = await collect()
      if (generation !== collectionGeneration.current) return
      const currentState = stateRef.current
      const targetPeer = currentState.peers.find(item => item.id === target)
      // A pause or revoked permission may clear this draft while a folder reader is pending.
      if (!targetPeer || targetPeer.autosave?.paused || !targetPeer.verified || !targetPeer.trusted) return
      if (!addition.entries.length) { server.setError({ code: 'noSelection' }); return }
      const previous = batchesRef.current[target]
      const currentBudgets = api.exchangeBudgets(currentState)
      const selection = appendSelection(previous?.selection, addition, currentBudgets.batchEntries, currentBudgets.batchBytes, currentBudgets)
      validateSelection(selection, currentBudgets.batchEntries, currentBudgets.batchBytes, currentBudgets.fileBytes, currentBudgets)
      if (selection !== previous?.selection) updateBatch(target, { selection, requestId: api.requestID(), peerIds: previous?.peerIds, mode })
    } catch (error) { if (generation === collectionGeneration.current) server.setError(error) }
    finally { if (generation === collectionGeneration.current) { collectingRef.current = false; setCollecting(false) } }
  }
  const pickFiles = (list: FileList | File[] | null) => { if (!list?.length) return; const files = Array.from(list); void select(() => fromFiles(files, maxFiles, maxBytes, pathLimits)) }
  const removeEntry = (path: string) => {
    if (uploadController.current || collectingRef.current || (batchesRef.current[peer.id]?.dispatch || batchesRef.current[peer.id]?.outbox)) return
    const previous = batchesRef.current[peer.id]
    if (!previous) return
    const selection = removeSelection(previous.selection, path)
    if (selection !== previous.selection) updateBatch(peer.id, selection.entries.length ? { selection, requestId: api.requestID(), peerIds: previous.peerIds, mode: previous.mode } : undefined)
  }
  const drop = (event: DragEvent) => {
    event.preventDefault(); dragDepth.current = 0; setDragging(false)
    if (filesAllowed && !uploadController.current && !collectingRef.current) void select(() => fromDrop(event.dataTransfer, maxFiles, maxBytes, pathLimits))
  }
  const changeRecipient = (id: string, checked: boolean) => {
    const previous = batchesRef.current[peer.id]
    if (!previous || previous.dispatch || previous.outbox || uploadController.current || collectingRef.current || id === peer.id) return
    const current = previous.peerIds || [peer.id]
    const next = checked ? [...current, id] : current.filter(value => value !== id)
    if (new Set(next).size !== next.length) return
    updateBatch(peer.id, { ...previous, peerIds: next, requestId: api.requestID() })
  }
  const dispatchContext = (draft: DraftBatch, pending: PendingDispatch) => ({ processId: pending.processId, preparationId: pending.preparationId, peerIds: pending.peerIds, selection: draft.selection })
  const retainDispatch = (target: string, draft: DraftBatch, pending: PendingDispatch) => {
    const current = batchesRef.current[target]
    if (alive.current && current?.requestId === draft.requestId && current.selection === draft.selection) updateBatch(target, { ...current, dispatch: pending })
  }
  const receivedDispatch = (target: string, draft: DraftBatch, receipt: DispatchCommitted) => {
    if (!alive.current) return
    setDispatchReceipts(current => ({ ...current, [target]: receipt }))
    if (batchesRef.current[target]?.requestId === draft.requestId) updateBatch(target, undefined)
  }
  const inspectDispatch = async (discard = false) => {
    const draft = batchesRef.current[peer.id], pending = draft?.dispatch
    if (!draft || !pending || dispatchOperation.current || server.auth !== 'ready' || server.stale || pending.processId !== state.resourceCatalogProcessId) return
    const target = peer.id
    dispatchOperation.current = 'checking'; setDispatchBusy(true); server.setError(null)
    try {
      const response = await api.command(discard ? 'transfer.dispatch.discard' : 'transfer.dispatch.status', { processId: pending.processId, preparationId: pending.preparationId })
      const result = readDispatchResult(response.result, dispatchContext(draft, pending))
      if (result.status === 'committed') receivedDispatch(target, draft, result)
      else if (result.status === 'prepared') retainDispatch(target, draft, { ...pending, phase: 'prepared', review: result })
      else if (alive.current && batchesRef.current[target]?.requestId === draft.requestId) updateBatch(target, { selection: draft.selection, peerIds: pending.peerIds, requestId: api.requestID() })
      await server.refresh(true)
    } catch (error) { if (alive.current) server.handleError(error) }
    finally { dispatchOperation.current = null; if (alive.current) setDispatchBusy(false) }
  }
  const abandonDispatchDraft = () => {
    const draft = batchesRef.current[peer.id]
    if (!draft?.dispatch || dispatchOperation.current || dispatchMayHaveSent && !abandonConfirmed) return
    // This explicit local action never discards an unknown committed operation
    // or creates a new send. The user may inspect the retained Core receipt.
    updateBatch(peer.id, undefined)
    setAbandonConfirmed(false)
  }
  const sendToRecipients = async (draft: DraftBatch) => {
    const target = peer.id, selected = [...(draft.peerIds || [target])], started = dispatchEpoch
    const processId = state.resourceCatalogProcessId
    if (!processId || !/^[a-f0-9]{64}$/.test(processId) || dispatchOperation.current || !recipientsReady || server.auth !== 'ready') return
    const controller = new AbortController()
    let pending: PendingDispatch = draft.dispatch || { processId, preparationId: draft.requestId, peerIds: selected, phase: 'preparing' }
    if (pending.processId !== processId || !samePeers(pending.peerIds, selected) || draft.dispatch && pending.phase !== 'prepared') return
    uploadController.current = controller; setDispatchBusy(true); server.setError(null)
    const current = () => {
      const latest = dispatchLatest.current, saved = batchesRef.current[target]
      return alive.current && !controller.signal.aborted && latest.epoch === started && latest.server.auth === 'ready' && !latest.server.stale
        && saved?.requestId === draft.requestId && saved.selection === draft.selection && samePeers(saved.peerIds || [target], selected)
        && selected.every(id => latest.state.peers.some(value => value.id === id && api.canExchange(value)))
    }
    try {
      if (!pending.review) {
        dispatchOperation.current = 'preparing'; retainDispatch(target, draft, pending)
        setUploading({ peerId: target, loaded: 0, total: 0 })
        const response = await api.uploadDispatch(selected, processId, draft.selection, draft.requestId, (loaded, total) => { if (alive.current) setUploading({ peerId: target, loaded, total }) }, controller.signal)
        const prepared = readDispatchResult(response.result, dispatchContext(draft, pending))
        if (prepared.status === 'committed') { receivedDispatch(target, draft, prepared); return }
        if (prepared.status !== 'prepared') throw new api.ApiError('invalid_response', '')
        pending = { ...pending, phase: 'prepared', review: prepared }
        retainDispatch(target, draft, pending)
      }
      if (!current()) {
        // Navigation or changed authentication cannot commit a delayed preparation.
        try {
          const discarded = await api.command('transfer.dispatch.discard', { processId, preparationId: pending.preparationId })
          if (readDispatchResult(discarded.result, dispatchContext(draft, pending)).status === 'discarded' && alive.current && batchesRef.current[target]?.requestId === draft.requestId) updateBatch(target, { selection: draft.selection, peerIds: selected, requestId: api.requestID() })
        } catch { /* Keep the original reference available for explicit status. */ }
        return
      }
      setUploading(null); dispatchOperation.current = 'committing'
      pending = { ...pending, phase: 'committing' }; retainDispatch(target, draft, pending)
      const response = await api.command('transfer.dispatch.commit', { processId, preparationId: pending.preparationId, reviewRevision: pending.review!.reviewRevision, confirm: true })
      const committed = readDispatchResult(response.result, dispatchContext(draft, pending))
      if (committed.status !== 'committed') throw new api.ApiError('invalid_response', '')
      receivedDispatch(target, draft, committed)
      await server.refresh(true)
    } catch (error) {
      retainDispatch(target, draft, { ...pending, phase: dispatchOperation.current === 'committing' ? 'commit-unknown' : pending.review ? 'prepared' : 'prepare-unknown' })
      if (alive.current && !controller.signal.aborted) server.handleError(error)
      if (alive.current) await server.refresh()
    } finally { dispatchOperation.current = null; uploadController.current = null; if (alive.current) { setUploading(null); setDispatchBusy(false) } }
  }
  const sendBatch = async () => {
    if (queueMode || !allowed || !batch || uploadController.current || collectingRef.current || dispatchOperation.current) return
    try { validateSelection(batch.selection, maxFiles, maxBytes, budgets.fileBytes, pathLimits) } catch (error) { server.setError(error); return }
    if (batch.dispatch || (batch.peerIds?.length || 1) > 1) { await sendToRecipients(batch); return }
    const target = peer.id
    const controller = new AbortController()
    uploadController.current = controller
    setUploading({ peerId: target, loaded: 0, total: 0 }); server.setError(null)
    try {
      await api.upload(target, batch.selection, batch.requestId, (loaded, total) => setUploading({ peerId: target, loaded, total }), controller.signal)
      if (!alive.current) return
      setBatches(current => current[target] === batch ? { ...current, [target]: undefined } : current)
      nearBottom.current = true
      await server.refresh(true)
    } catch (error) {
      if (!alive.current) return
      if (controller.signal.aborted) server.setError({ code: 'cancelledUpload' })
      else server.handleError(error)
      await server.refresh()
    } finally { uploadController.current = null; if (alive.current) setUploading(null) }
  }
  const reason = peer.autosave?.paused ? 'peerPaused' : !peer.bridge ? 'appNotConfirmed' : !peer.online ? peerPresenceKey(peer) === 'offline' ? 'needsOnline' : 'awaitingResponse' : !peer.verified ? 'needsVerification' : !peer.trusted ? 'needsTrust' : null
  return <div className={`conversation ${dragging ? 'dragging' : ''}`} onDragEnter={event => { if (Array.from(event.dataTransfer.types).includes('Files')) { event.preventDefault(); dragDepth.current++; if (filesAllowed) setDragging(true) } }} onDragOver={event => { if (Array.from(event.dataTransfer.types).includes('Files')) event.preventDefault() }} onDragLeave={event => { event.preventDefault(); if (--dragDepth.current <= 0) setDragging(false) }} onDrop={drop}>
    {dragging && <div className="drop-overlay"><Icon name="upload" size={36} /><strong>{t('dropHere')}</strong></div>}
    {onReviewReceiving && <ReceiveRecoveryNotice state={state} locale={locale} onReview={onReviewReceiving} />}
    <PeerTransfers peer={peer} state={state} server={server} locale={locale} resetToken={retryResetToken} />
    <div className="timeline" ref={listRef} role="log" aria-label={peer.name} aria-live="polite" aria-relevant="additions text" onScroll={() => { const el = listRef.current!; nearBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100 }}>
      {timeline.length === 0 && <div className="conversation-empty"><div className="empty-illustration"><div className="hello-bubble"><Icon name={peer.bridge ? 'message' : 'link'} size={32} /></div><span className="tiny-spark">✦</span></div><h2>{t(peer.bridge ? 'noConversation' : 'connectService')}</h2><p>{t(peer.bridge ? 'noConversationHint' : 'appNotConfirmed')}</p></div>}
      {timeline.map(item => <div key={`${item.type}:${item.value.direction}:${item.value.id}`} className={`timeline-item ${item.value.direction}`} tabIndex={item.type === 'transfer' ? -1 : undefined} data-transfer-id={item.type === 'transfer' ? item.value.id : undefined} data-transfer-direction={item.type === 'transfer' ? item.value.direction : undefined}>
        {item.type === 'message' ? <div className="message-bubble"><p>{item.value.text}</p>{item.value.error && <small className="field-error">{item.value.error}</small>}</div> : <TransferCard transfer={item.value} peer={peer} locale={locale} t={t} server={server} destination={receiveDestinations[item.value.id]} setDestination={value => setReceiveDestinations(current => ({ ...current, [item.value.id]: value }))} />}
        <div className="message-meta"><time dateTime={item.value.createdAt}>{timestamp(item.value.createdAt, locale)}</time>{item.type === 'message' && <><span>·</span><span className={item.value.status === 'failed' ? 'field-error' : ''}>{textOutboxStatuses.includes(item.value.status as TextOutboxStatus) && textRequestTime(item.value.id) !== undefined ? textQueueText(locale, item.value.status as TextOutboxStatus) : t(item.value.status as TextKey)}</span>{item.value.direction === 'outgoing' && textRequestTime(item.value.id) !== undefined && onViewOutbox && <Button variant="ghost" onClick={() => onViewOutbox(item.value.id)}>{textQueueText(locale, 'view')}</Button>}</>}</div>
      </div>)}
    </div>
    <div className="composer-area">
      {messageBlocked && <div id={`${composerId}-safety`}><ErrorBanner message={t(messageBlocked)} t={t} /></div>}
      {reason && (!textQueue.queueMode || !['needsOnline', 'awaitingResponse', 'appNotConfirmed'].includes(reason)) && <div className="peer-notice"><Icon name={reason === 'needsTrust' ? 'shield' : 'info'} /><p>{t(reason)}</p>{reason === 'needsTrust' && <Button variant="primary" onClick={onTrust} busy={server.busy.has(`trust:${peer.id}`)}>{t('trustDevice')}</Button>}{reason === 'peerPaused' && <Button disabled={!peer.verified || !peer.trusted || server.stale} busy={server.busy.has(`autosave:${peer.id}`)} onClick={() => server.run('peer.autosave', { peerId: peer.id, paused: false }, `autosave:${peer.id}`)}>{t('resume')}</Button>}</div>}
      {state.outboxSupported && <section className="outbox-mode" aria-label={ot('mode')}><fieldset disabled={Boolean(uploading) || collecting || !!batch?.dispatch || !!batch?.outbox}><legend>{ot('mode')}</legend><label><input type="radio" name={`${composerId}-mode`} checked={!queueMode} onChange={() => { setModes(current => ({ ...current, [peer.id]: 'immediate' })); if (batch) updateBatch(peer.id, { ...batch, mode: 'immediate', requestId: api.requestID() }) }} />{ot('immediate')}</label><label><input type="radio" name={`${composerId}-mode`} checked={queueMode} disabled={!state.outboxReady || !api.canQueue(peer)} onChange={() => { setModes(current => ({ ...current, [peer.id]: 'queue' })); if (batch) updateBatch(peer.id, { ...batch, mode: 'queue', requestId: api.requestID() }) }} />{ot('queue')}</label></fieldset>{!api.canQueue(peer) ? <p className="small">{peer.outboxEligibility?.reasonCode?.includes('upgrade') ? ot('unsupported') : ot('unavailable')}</p> : queueMode && peer.outboxEligibility?.reasonCode && <p className="small">{outboxReason(locale, peer.outboxEligibility.reasonCode)}</p>}{onViewOutbox && <Button variant="ghost" onClick={() => onViewOutbox()}>{ot('view')}</Button>}</section>}
      {unresolvedOutbox.length > 0 && !batch?.outbox && <p role="status">{ot('uncertain')}</p>}
      {queuedNotice === peer.id && <p role="status">{ot('localOnly')}</p>}
      {batch && <section className="batch-preview" aria-label={t('preview')}><div className="batch-heading"><div><p className="eyebrow">{t('preview')}</p><h3>{t('to')}: {peer.name}</h3></div><IconButton icon="close" label={t('cancel')} disabled={Boolean(uploading) || collecting || !!batch.dispatch || !!batch.outbox} onClick={() => updateBatch(peer.id, undefined)} /></div><fieldset className="dispatch-recipients" disabled={Boolean(uploading) || collecting || dispatchBusy || !!batch.dispatch || !!batch.outbox}><legend>{ft('recipients')}</legend><p className="small muted">{ft('recipientHint')}</p><ul>{[peer, ...state.peers.filter(value => value.id !== peer.id && (eligibleRecipient(value) || recipientIds.includes(value.id))), ...recipientIds.filter(id => id !== peer.id && !state.peers.some(value => value.id === id)).map(id => ({ id, name: id, online: false, verified: false, trusted: false, bridge: false } as api.Peer))].map(recipient => <li key={recipient.id}><label><input type="checkbox" checked={recipientIds.includes(recipient.id)} disabled={recipient.id === peer.id || !eligibleRecipient(recipient) && !recipientIds.includes(recipient.id)} onChange={event => changeRecipient(recipient.id, event.target.checked)} /><span>{recipient.name}<small className="muted">{recipient.id}</small>{!eligibleRecipient(recipient) && <small className="field-error">{queueMode ? outboxReason(locale, recipient.outboxEligibility?.reasonCode) || ot('unavailable') : ft('recipientUnavailable')}</small>}</span></label></li>)}</ul></fieldset><p className="small dispatch-selected">{ft('selectedRecipients')}: {recipientIds.map(id => state.peers.find(value => value.id === id)?.name || id).join(', ')}</p><ul className="preview-files">{batch.selection.entries.map(entry => <li key={entry.path}><Icon name={entry.kind === 'directory' ? 'folder' : 'file'} size={15} /><span>{entry.path}</span><small>{entry.kind === 'file' ? bytes(entry.size, locale) : t('folders')}</small><IconButton icon="close" label={`${ft('removeEntry')}: ${entry.path}`} disabled={Boolean(uploading) || collecting || !!batch.dispatch || !!batch.outbox} onClick={() => removeEntry(entry.path)} /></li>)}</ul><div className="batch-footer"><span>{batch.selection.entries.length} {t(batch.selection.entries.length === 1 ? 'item' : 'items')} · {bytes(batch.selection.entries.reduce((sum, item) => sum + item.size, 0), locale)}</span>{!queueMode && <Button variant="primary" onClick={sendBatch} busy={dispatchBusy} disabled={!allowed || !recipientsReady || Boolean(uploading) || collecting || dispatchBusy || !!batch.outbox || !!batch.dispatch && batch.dispatch.phase !== 'prepared' || !!batch.dispatch && batch.dispatch.processId !== state.resourceCatalogProcessId}><Icon name="send" size={15} />{batch.dispatch?.phase === 'prepared' ? ft('sendPrepared') : t('sendBatch')}</Button>}</div>{queueMode && <QueuePreparation key={batch.requestId} selection={batch.selection} peerIds={recipientIds} pending={batch.outbox} onReset={() => updateBatch(peer.id, { selection: batch.selection, peerIds: recipientIds, mode: 'queue', requestId: api.requestID() })} onPending={value => { const current = batchesRef.current[peer.id]; if (current?.requestId === batch.requestId) updateBatch(peer.id, { ...current, outbox: value }) }} onFinished={record => { if (record.admission === 'enqueued') setQueuedNotice(peer.id); updateBatch(peer.id, undefined) }} server={server} state={state} locale={locale} enabled={filesAllowed && recipientsReady && !collecting && !unresolvedOutbox.some(id => id !== batch.outbox?.requestId)} onViewOutbox={onViewOutbox} />}</section>}
      {batch?.dispatch && <section className="dispatch-pending" role="status"><p>{ft(batch.dispatch.processId !== state.resourceCatalogProcessId ? 'dispatchExpired' : batch.dispatch.phase === 'preparing' ? 'preparingRecipients' : batch.dispatch.phase === 'committing' ? 'committingRecipients' : batch.dispatch.phase === 'prepared' ? 'dispatchPrepared' : batch.dispatch.phase === 'prepare-unknown' ? 'prepareUnknown' : 'dispatchUnknown')}</p><div className="transfer-actions"><Button disabled={dispatchBusy || server.auth !== 'ready' || server.stale || batch.dispatch.processId !== state.resourceCatalogProcessId} onClick={() => void inspectDispatch()}>{ft('checkDispatch')}</Button>{batch.dispatch.phase === 'prepared' && <Button disabled={dispatchBusy || server.auth !== 'ready' || server.stale || batch.dispatch.processId !== state.resourceCatalogProcessId} onClick={() => void inspectDispatch(true)}>{ft('discardPrepared')}</Button>}</div>{!dispatchBusy && <><p className="small muted">{ft('requestReference')}: <span className="code-value">{batch.dispatch.preparationId}</span></p><p className="small muted">{ft(dispatchMayHaveSent ? 'abandonUnknownHint' : 'abandonPreparedHint')}</p>{dispatchMayHaveSent && <label className="small"><input type="checkbox" checked={abandonConfirmed} onChange={event => setAbandonConfirmed(event.target.checked)} /> {ft('acknowledgeUnknown')}</label>}<Button variant="ghost" disabled={dispatchMayHaveSent && !abandonConfirmed} onClick={abandonDispatchDraft}>{ft('abandonDraft')}</Button></>}</section>}
      {dispatchReceipts[peer.id] && <DispatchTransfers receipt={dispatchReceipts[peer.id]!} state={state} server={server} locale={locale} resetToken={retryResetToken} />}
      {uploading?.peerId === peer.id && <section className="upload-progress" aria-live="polite"><div><Icon name="upload" /><strong>{t('staging')}</strong><Button variant="ghost" onClick={() => uploadController.current?.abort()}>{t('cancel')}</Button></div><progress {...(uploading.total ? { value: uploading.loaded, max: uploading.total } : {})} aria-label={t('staging')} /><p className="small muted">{t('stagingHint')}</p></section>}
      {textQueue.panel}
      {(peer.bridge || api.canQueue(peer) || textQueue.supported && peer.verified && peer.trusted) && <><div className={`composer ${!textAllowed ? 'composer-disabled' : ''}`}><label className="composer-label" htmlFor={composerId}>{t('composerLabel')}</label><textarea id={composerId} ref={composerRef} value={message} onChange={event => setMessage(event.target.value)} onKeyDown={keyDown} onPaste={event => { const images = Array.from(event.clipboardData.files).filter(file => file.type.startsWith('image/')); if (images.length && filesAllowed && !uploadController.current && !collectingRef.current) { event.preventDefault(); void select(() => fromClipboardImages(images, batchesRef.current[peer.id]?.selection, maxFiles, maxBytes, pathLimits)) } }} placeholder={t('messagePlaceholder')} aria-label={t('messagePlaceholder')} aria-describedby={`composer-hint${messageBlocked ? ` ${composerId}-safety` : ''}`} rows={2} disabled={!textAllowed || textQueue.locked}  /><div className="composer-tools"><div className="flex items-center gap-1"><IconButton icon="clip" label={t('attach')} disabled={!filesAllowed || collecting || Boolean(uploading) || !!batch?.dispatch || !!batch?.outbox} onClick={() => filesRef.current?.click()} /><IconButton icon="folder" label={t('attachFolder')} disabled={!filesAllowed || collecting || Boolean(uploading) || !!batch?.dispatch || !!batch?.outbox} onClick={() => folderRef.current?.click()} />{collecting && <span className="small muted">{t('loading')}</span>}</div><Button variant="primary" className="send-button" aria-label={textQueue.queueMode ? textQueueText(locale, 'review') : t('sendMessage')} disabled={textQueue.locked || (textQueue.queueMode ? !textQueue.canPrepare : !allowed || !message.trim()) || Boolean(messageBlocked)} busy={sending || textQueue.busy} onClick={send}>{textQueue.queueMode ? textQueueText(locale, 'review') : t('send')}<Icon name="send" size={15} /></Button></div></div><div className="composer-hints"><span id="composer-hint">{t('composerHint')} · {t('effectiveMessageLimit')}: {bytes(budgets.messageBytes, locale)}</span><span title={`${t('batchLimit')}: ${maxFiles} ${t('items')} · ${bytes(maxBytes, locale)}`}>{maxFiles} {t('items')} / {bytes(maxBytes, locale)}</span></div><details className="input-help"><summary><Icon name="info" size={13} />{t('inputTips')}</summary><p>{t('pasteHint')}</p><p>{t('folderLimit')}</p><p>{t('effectiveMessageLimit')}: {bytes(budgets.messageBytes, locale)}</p><p>{t('effectiveFileLimit')}: {bytes(budgets.fileBytes, locale)}</p><p>{t('effectivePathLimit')}: {budgets.pathDepth} {t('pathLevels')} / {bytes(budgets.pathBytes, locale)}</p><p>{t('finiteResourceHint')}</p></details><input hidden ref={filesRef} type="file" multiple aria-label={t('attach')} onChange={event => { pickFiles(event.target.files); event.target.value = '' }} /><input hidden ref={folderRef} type="file" multiple aria-label={t('attachFolder')} {...{ webkitdirectory: '', directory: '' }} onChange={event => { pickFiles(event.target.files); event.target.value = '' }} /></>}
    </div>
  </div>
}
