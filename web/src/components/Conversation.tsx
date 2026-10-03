import { useEffect, useRef, useState, type Dispatch, type SetStateAction, type DragEvent, type KeyboardEvent } from 'react'
import * as api from '../api'
import { appendSelection, fromClipboardImages, fromDrop, fromFiles, removeSelection, validateSelection } from '../files'
import { bytes, errorDetail, errorText, timestamp, transferFailureText, type Translate, type TextKey } from '../i18n'
import { transferTranslator } from '../transfer-i18n'
import type { Server } from '../useServer'
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
  const action = (name: 'transfer.accept' | 'transfer.decline' | 'transfer.cancel' | 'transfer.retry' | 'transfer.forget') => server.run(name, { transferId: transfer.id, ...(name === 'transfer.accept' ? { destination: selectedDestination.trim() } : {}) }, `transfer:${transfer.id}`)
  const discard = async () => { if (await action('transfer.forget')) setReviewingDiscard(false) }
  return <article className={`transfer-card ${incoming && offered ? 'incoming-offer' : ''}`} aria-label={`${transfer.name} · ${t(transfer.status)}`}>
    <div className="transfer-top"><div className="transfer-icon"><Icon name={incoming ? 'download' : 'upload'} size={22} /></div><div className="grow"><p className="eyebrow">{t(incoming ? offered ? 'incomingOffer' : 'incomingBatch' : 'outgoingOffer')}</p><h3>{transfer.name || `${entries.length} ${t(entries.length === 1 ? 'item' : 'items')}`}</h3><p className="small muted">{entries.length} {t(entries.length === 1 ? 'item' : 'items')}<span className="dot-separator">·</span>{bytes(transfer.totalBytes, locale)}</p></div><Badge tone={transfer.status === 'completed' ? 'green' : transfer.status === 'failed' ? 'red' : offered ? 'purple' : 'neutral'}>{t(transfer.status)}</Badge></div>
    {active && <div className="transfer-progress"><progress value={transfer.completedBytes} max={Math.max(1, transfer.totalBytes)} aria-label={t(transfer.status)} /><div><span>{bytes(transfer.completedBytes, locale)} / {bytes(transfer.totalBytes, locale)}</span><span>{Math.floor(progress)}%</span></div></div>}
    {transfer.error && <p className="field-error" role="status">{transferFailureText(transfer.error, t)}</p>}
    {entries.length > 0 && <details className="file-details"><summary>{t('showFiles')}<span>{entries.length}</span></summary><ul>{entries.map((entry, index) => <li key={entry.id || `${entry.path}:${index}`}><Icon name={entry.kind === 'directory' ? 'folder' : 'file'} size={15} /><div><span className="entry-path">{entry.path}</span>{entry.status && <small>{entryStatus(entry.status)}</small>}{entry.storedPath && <small>{t('storedAt')}: {entry.storedPath}</small>}{entry.error && <small className="field-error">{transferFailureText(entry.error, t)}</small>}</div><span>{entry.kind === 'file' ? bytes(entry.size, locale) : t('folders')}</span></li>)}</ul></details>}
    {incoming && offered && <><p className="small muted accept-hint">{t('acceptHint')}</p>{editingDestination || destination !== undefined || !configuredDirectory ? <div className="destination-field"><label className="field">{t('receiveDirectory')}<input value={selectedDestination} onChange={event => setDestination(event.target.value)} placeholder={t('directoryPlaceholder')} spellCheck={false} autoComplete="off" autoFocus={editingDestination} disabled={busy} /><small className="muted">{ft('batchDestinationHint')}</small></label>{configuredDirectory && <Button variant="ghost" disabled={busy} onClick={() => { setDestination(undefined); setEditingDestination(false) }}>{ft('useDefaultFolder')}</Button>}</div> : <div className="accept-destination"><p className="small muted">{t('receiveDirectory')}: <span className="code-value">{selectedDestination}</span></p><Button variant="ghost" disabled={busy} onClick={() => { setDestination(selectedDestination); setEditingDestination(true) }}>{ft('changeFolder')}</Button></div>}<div className="transfer-actions"><Button variant="ghost" onClick={() => action('transfer.decline')} disabled={busy}>{t('decline')}</Button><Button variant="primary" onClick={() => action('transfer.accept')} busy={busy} disabled={!api.canExchange(peer) || server.stale || !selectedDestination.trim()}><Icon name="download" size={16} />{t('acceptBatch')}</Button></div></>}
    {(active || (!incoming && offered)) && <div className="transfer-actions"><Button variant="ghost" onClick={() => action('transfer.cancel')} busy={busy}>{t('cancelTransfer')}</Button></div>}
    {transfer.status === 'failed' && <div className="transfer-actions"><Button onClick={() => action('transfer.retry')} busy={busy} disabled={!api.canExchange(peer) || server.stale}><Icon name="refresh" size={16} />{t('retryTransfer')}</Button></div>}
    {['completed', 'cancelled', 'failed', 'declined'].includes(transfer.status) && <div className="forget-transfer"><small>{discardsSendingCopies ? ft('discardHint') : t('forgetHint')}</small><Button variant="ghost" busy={busy} onClick={() => { if (discardsSendingCopies) { server.setError(null); setReviewingDiscard(true) } else void action('transfer.forget') }}>{discardsSendingCopies ? ft('discardBatch') : t('forgetTransfer')}</Button></div>}
    {reviewingDiscard && discardsSendingCopies && <Modal title={ft('discardTitle')} t={t} onClose={() => { if (!busy) setReviewingDiscard(false) }}><div className="target-pill"><Icon name="upload" /><strong>{transfer.name || `${entries.length} ${t(entries.length === 1 ? 'item' : 'items')}`}</strong><span>{peer.name}</span></div><p>{ft('discardImpact')}</p><p className="small muted">{ft('discardPreservedFiles')}</p>{server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}<div className="modal-actions"><Button onClick={() => setReviewingDiscard(false)} disabled={busy}>{t('cancel')}</Button><Button variant="danger" busy={busy} onClick={() => void discard()}>{ft('discardConfirm')}</Button></div></Modal>}
  </article>
}

export interface DraftBatch { selection: api.UploadSelection; requestId: string }
export interface UploadActivity { peerId: string; loaded: number; total: number; cancel: () => void }
export function Conversation({ peer, state, t, locale, server, onTrust, drafts, setDrafts, batches, setBatches, receiveDestinations, setReceiveDestinations, onUploadChange }: { peer: api.Peer; state: api.State; t: Translate; locale: api.Locale; server: Server; onTrust: () => void; drafts: Record<string, string>; setDrafts: Dispatch<SetStateAction<Record<string, string>>>; batches: Record<string, DraftBatch | undefined>; setBatches: Dispatch<SetStateAction<Record<string, DraftBatch | undefined>>>; receiveDestinations: Record<string, string | undefined>; setReceiveDestinations: Dispatch<SetStateAction<Record<string, string | undefined>>>; onUploadChange?: (activity: UploadActivity | null) => void }) {
  const ft = transferTranslator(locale)
  const [collecting, setCollecting] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [uploading, setUploading] = useState<{ peerId: string; loaded: number; total: number } | null>(null)
  const uploadController = useRef<AbortController | null>(null)
  const dragDepth = useRef(0)
  const filesRef = useRef<HTMLInputElement>(null)
  const folderRef = useRef<HTMLInputElement>(null)
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
  const message = drafts[peer.id] || ''
  const batch = batches[peer.id]
  const budgets = api.exchangeBudgets(state)
  const maxFiles = budgets.batchEntries
  const maxBytes = budgets.batchBytes
  const pathLimits = { pathDepth: budgets.pathDepth, pathBytes: budgets.pathBytes }
  const allowed = api.canExchange(peer) && !server.stale
  const sending = server.busy.has(`message:${peer.id}`)
  const messages = state.messages.filter(item => item.peerId === peer.id)
  const transfers = state.transfers.filter(item => item.peerId === peer.id)
  const timeline = [...messages.map(value => ({ type: 'message' as const, value })), ...transfers.map(value => ({ type: 'transfer' as const, value }))].sort((a, b) => a.value.createdAt.localeCompare(b.value.createdAt))
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
  const send = async () => {
    if (!allowed || !message.trim() || sending || pendingSend.current.has(peer.id)) return
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
    if (collectingRef.current || uploadController.current || !allowed) return
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
      if (selection !== previous?.selection) updateBatch(target, { selection, requestId: api.requestID() })
    } catch (error) { if (generation === collectionGeneration.current) server.setError(error) }
    finally { if (generation === collectionGeneration.current) { collectingRef.current = false; setCollecting(false) } }
  }
  const pickFiles = (list: FileList | File[] | null) => { if (!list?.length) return; const files = Array.from(list); void select(() => fromFiles(files, maxFiles, maxBytes, pathLimits)) }
  const removeEntry = (path: string) => {
    if (uploadController.current || collectingRef.current) return
    const previous = batchesRef.current[peer.id]
    if (!previous) return
    const selection = removeSelection(previous.selection, path)
    if (selection !== previous.selection) updateBatch(peer.id, selection.entries.length ? { selection, requestId: api.requestID() } : undefined)
  }
  const drop = (event: DragEvent) => {
    event.preventDefault(); dragDepth.current = 0; setDragging(false)
    if (allowed && !uploadController.current && !collectingRef.current) void select(() => fromDrop(event.dataTransfer, maxFiles, maxBytes, pathLimits))
  }
  const sendBatch = async () => {
    if (!allowed || !batch || uploadController.current || collectingRef.current) return
    try { validateSelection(batch.selection, maxFiles, maxBytes, budgets.fileBytes, pathLimits) } catch (error) { server.setError(error); return }
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
  const reason = peer.autosave?.paused ? 'peerPaused' : !peer.bridge ? 'appNotConfirmed' : !peer.online ? 'needsOnline' : !peer.verified ? 'needsVerification' : !peer.trusted ? 'needsTrust' : null
  return <div className={`conversation ${dragging ? 'dragging' : ''}`} onDragEnter={event => { if (Array.from(event.dataTransfer.types).includes('Files')) { event.preventDefault(); dragDepth.current++; if (allowed) setDragging(true) } }} onDragOver={event => { if (Array.from(event.dataTransfer.types).includes('Files')) event.preventDefault() }} onDragLeave={event => { event.preventDefault(); if (--dragDepth.current <= 0) setDragging(false) }} onDrop={drop}>
    {dragging && <div className="drop-overlay"><Icon name="upload" size={36} /><strong>{t('dropHere')}</strong></div>}
    <div className="timeline" ref={listRef} role="log" aria-label={peer.name} aria-live="polite" aria-relevant="additions text" onScroll={() => { const el = listRef.current!; nearBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100 }}>
      {timeline.length === 0 && <div className="conversation-empty"><div className="empty-illustration"><div className="hello-bubble"><Icon name={peer.bridge ? 'message' : 'link'} size={32} /></div><span className="tiny-spark">✦</span></div><h2>{t(peer.bridge ? 'noConversation' : 'connectService')}</h2><p>{t(peer.bridge ? 'noConversationHint' : 'appNotConfirmed')}</p></div>}
      {timeline.map(item => <div key={`${item.type}:${item.value.id}`} className={`timeline-item ${item.value.direction}`}>
        {item.type === 'message' ? <div className="message-bubble"><p>{item.value.text}</p>{item.value.error && <small className="field-error">{item.value.error}</small>}</div> : <TransferCard transfer={item.value} peer={peer} locale={locale} t={t} server={server} destination={receiveDestinations[item.value.id]} setDestination={value => setReceiveDestinations(current => ({ ...current, [item.value.id]: value }))} />}
        <div className="message-meta"><time dateTime={item.value.createdAt}>{timestamp(item.value.createdAt, locale)}</time>{item.type === 'message' && <><span>·</span><span className={item.value.status === 'failed' ? 'field-error' : ''}>{t(item.value.status)}</span></>}</div>
      </div>)}
    </div>
    <div className="composer-area">
      {reason && <div className="peer-notice"><Icon name={reason === 'needsTrust' ? 'shield' : 'info'} /><p>{t(reason)}</p>{reason === 'needsTrust' && <Button variant="primary" onClick={onTrust} busy={server.busy.has(`trust:${peer.id}`)}>{t('trustDevice')}</Button>}{reason === 'peerPaused' && <Button disabled={!peer.verified || !peer.trusted || server.stale} busy={server.busy.has(`autosave:${peer.id}`)} onClick={() => server.run('peer.autosave', { peerId: peer.id, paused: false }, `autosave:${peer.id}`)}>{t('resume')}</Button>}</div>}
      {batch && <section className="batch-preview" aria-label={t('preview')}><div className="batch-heading"><div><p className="eyebrow">{t('preview')}</p><h3>{t('to')}: {peer.name}</h3></div><IconButton icon="close" label={t('cancel')} disabled={Boolean(uploading) || collecting} onClick={() => updateBatch(peer.id, undefined)} /></div><ul className="preview-files">{batch.selection.entries.map(entry => <li key={entry.path}><Icon name={entry.kind === 'directory' ? 'folder' : 'file'} size={15} /><span>{entry.path}</span><small>{entry.kind === 'file' ? bytes(entry.size, locale) : t('folders')}</small><IconButton icon="close" label={`${ft('removeEntry')}: ${entry.path}`} disabled={Boolean(uploading) || collecting} onClick={() => removeEntry(entry.path)} /></li>)}</ul><div className="batch-footer"><span>{batch.selection.entries.length} {t(batch.selection.entries.length === 1 ? 'item' : 'items')} · {bytes(batch.selection.entries.reduce((sum, item) => sum + item.size, 0), locale)}</span><Button variant="primary" onClick={sendBatch} disabled={!allowed || Boolean(uploading) || collecting}><Icon name="send" size={15} />{t('sendBatch')}</Button></div></section>}
      {uploading?.peerId === peer.id && <section className="upload-progress" aria-live="polite"><div><Icon name="upload" /><strong>{t('staging')}</strong><Button variant="ghost" onClick={() => uploadController.current?.abort()}>{t('cancel')}</Button></div><progress {...(uploading.total ? { value: uploading.loaded, max: uploading.total } : {})} aria-label={t('staging')} /><p className="small muted">{t('stagingHint')}</p></section>}
      {peer.bridge && <><div className={`composer ${!allowed ? 'composer-disabled' : ''}`}><textarea ref={composerRef} value={message} onChange={event => setMessage(event.target.value)} onKeyDown={keyDown} onPaste={event => { const images = Array.from(event.clipboardData.files).filter(file => file.type.startsWith('image/')); if (images.length && allowed && !uploadController.current && !collectingRef.current) { event.preventDefault(); void select(() => fromClipboardImages(images, batchesRef.current[peer.id]?.selection, maxFiles, maxBytes, pathLimits)) } }} placeholder={t('messagePlaceholder')} aria-label={t('messagePlaceholder')} aria-describedby="composer-hint" rows={2} disabled={!allowed}  /><div className="composer-tools"><div className="flex items-center gap-1"><IconButton icon="clip" label={t('attach')} disabled={!allowed || collecting || Boolean(uploading)} onClick={() => filesRef.current?.click()} /><IconButton icon="folder" label={t('attachFolder')} disabled={!allowed || collecting || Boolean(uploading)} onClick={() => folderRef.current?.click()} />{collecting && <span className="small muted">{t('loading')}</span>}</div><Button variant="primary" className="send-button" aria-label={t('sendMessage')} disabled={!allowed || !message.trim()} busy={sending} onClick={send}>{t('send')}<Icon name="send" size={15} /></Button></div></div><div className="composer-hints"><span id="composer-hint">{t('composerHint')} · {t('effectiveMessageLimit')}: {bytes(budgets.messageBytes, locale)}</span><span title={`${t('batchLimit')}: ${maxFiles} ${t('items')} · ${bytes(maxBytes, locale)}`}>{maxFiles} {t('items')} / {bytes(maxBytes, locale)}</span></div><details className="input-help"><summary><Icon name="info" size={13} />{t('inputTips')}</summary><p>{t('pasteHint')}</p><p>{t('folderLimit')}</p><p>{t('effectiveMessageLimit')}: {bytes(budgets.messageBytes, locale)}</p><p>{t('effectiveFileLimit')}: {bytes(budgets.fileBytes, locale)}</p><p>{t('effectivePathLimit')}: {budgets.pathDepth} {t('pathLevels')} / {bytes(budgets.pathBytes, locale)}</p><p>{t('finiteResourceHint')}</p></details><input hidden ref={filesRef} type="file" multiple aria-label={t('attach')} onChange={event => { pickFiles(event.target.files); event.target.value = '' }} /><input hidden ref={folderRef} type="file" multiple aria-label={t('attachFolder')} {...{ webkitdirectory: '', directory: '' }} onChange={event => { pickFiles(event.target.files); event.target.value = '' }} /></>}
    </div>
  </div>
}
