import { useEffect, useRef, useState } from 'react'
import * as api from '../api'
import { bytes, errorText, type Translate } from '../i18n'
import { recoveryText } from '../receive-recovery-i18n'
import type { Server } from '../useServer'
import { Button, ErrorBanner, Icon, Modal, useAlive } from './ui'

export function ReceiveRecoveryNotice({ state, locale, onReview }: { state: api.State; locale: api.Locale; onReview: () => void }) {
  if (!api.receivingBlocked(state)) return null
  return <div className="peer-notice receive-recovery-notice" role="status"><Icon name="alert" /><p><strong>{recoveryText(locale, 'blocked')}</strong><br />{recoveryText(locale, 'blockedHint')}</p><Button type="button" onClick={onReview}>{recoveryText(locale, 'open')}</Button></div>
}

function signature(view: api.ReceiveRecovery | null | undefined) {
  // Reserved bytes can change as an allowed autosave starts immediately after
  // recovery. They are live usage, not part of the review's identity.
  return view && JSON.stringify([view.state, view.code, view.review])
}
export function ReceiveRecoveryDialog({ server, locale, t, onClose, onBack }: { server: Server; locale: api.Locale; t: Translate; onClose: () => void; onBack: () => void }) {
  const r = (key: Parameters<typeof recoveryText>[1]) => recoveryText(locale, key)
  const alive = useAlive()
  const [view, setView] = useState<api.ReceiveRecovery | null>(null)
  const [reviewed, setReviewed] = useState(false)
  const [working, setWorking] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [status, setStatus] = useState<'applied' | 'changed' | 'uncertain' | ''>('')
  const pending = useRef<AbortController | null>(null)
  const serverRef = useRef(server); serverRef.current = server
  const current = api.readReceiveRecovery(server.state?.receiveRecovery)
  const currentSignature = signature(current)
  const completeReview = view?.review.length === api.RECEIVE_RECOVERY_REVIEW.length && api.RECEIVE_RECOVERY_REVIEW.every(key => view.review.includes(key))
  const canConfirm = view?.state === 'blocked' && view.code === 'legacy_review_required' && completeReview
  const matchesCurrent = Boolean(view && signature(view) === currentSignature)
  const displayed = matchesCurrent ? current : view
  const blocked = working || server.auth !== 'ready' || server.stale

  const check = async (confirm: boolean) => {
    const latest = serverRef.current
    if (pending.current || latest.auth !== 'ready' || (confirm && (latest.stale || !reviewed || !canConfirm || !matchesCurrent))) return
    const request = new AbortController(); pending.current = request
    setWorking(true); setError(null); setStatus(''); setReviewed(false)
    if (!confirm) setView(null)
    try {
      // Refreshing is read-only and may recover a stale connection. Never send
      // an acknowledgment while the last known server state is stale.
      if (!confirm && latest.stale && !await latest.refresh(true)) throw new api.ApiError('network_error', '')
      if (!alive.current || request.signal.aborted) return
      const response = await api.command('receive.recovery.confirm', { reviewed: confirm }, api.requestID(), request.signal)
      if (!alive.current || request.signal.aborted) return
      const next = api.readReceiveRecovery(response.result)
      if (!next || (!confirm && next.applied)) throw new api.ApiError('invalid_response', '')
      const snapshot = await serverRef.current.refresh(true)
      if (!alive.current || request.signal.aborted) return
      const verified = api.readReceiveRecovery(snapshot?.receiveRecovery)
      if (!verified || signature(next) !== signature(verified)) { setView(null); setStatus('uncertain'); return }
      setView(verified)
      if (confirm) setStatus(next.applied && verified.state === 'ready' ? 'applied' : verified.state === 'ready' ? '' : 'uncertain')
    } catch (value) {
      if (!alive.current || request.signal.aborted) return
      setView(null); setError(value)
      if (value instanceof api.ApiError && value.code === 'unauthenticated') serverRef.current.handleError(value)
    } finally {
      if (pending.current === request) { pending.current = null; if (alive.current) setWorking(false) }
    }
  }
  useEffect(() => {
    void check(false)
    return () => { pending.current?.abort(); pending.current = null }
  // The dialog owns one preview per opening, including StrictMode remounts.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => {
    if (server.stale || (!working && view && !matchesCurrent)) {
      setReviewed(false); setView(null); setStatus('changed')
    }
  }, [server.stale, currentSignature, working, view, matchesCurrent])

  return <Modal title={r('title')} t={t} onClose={onClose}>
    <div className="form-stack receive-recovery-review">
      {working && <p role="status">{r('pending')}</p>}
      {error != null && <ErrorBanner message={errorText(error, t)} t={t} />}
      {status && <p role="status" className="scope-note">{r(status)}</p>}
      {server.stale && <p className="scope-note">{t('stale')}</p>}
      {view && <>
        <p><strong>{r(view.state === 'ready' ? 'ready' : 'blocked')}</strong></p>
        <p className="small muted">{r('retained')}: {displayed?.reservedBytes == null ? r('unknown') : bytes(displayed.reservedBytes, locale)}</p>
        {canConfirm ? <>
          <p>{r('legacy')}</p>
          <ul>{api.RECEIVE_RECOVERY_REVIEW.map(key => <li key={key}>{r(key)}</li>)}</ul>
          <p className="scope-note">{r('unavailable')}</p>
          <label className="checkbox-field"><input type="checkbox" checked={reviewed} onChange={event => setReviewed(event.target.checked)} disabled={blocked || !matchesCurrent} /><span>{r('acknowledge')}</span></label>
        </> : view.state === 'blocked' && <p>{r('repair')}</p>}
      </>}
      <div className="modal-actions"><Button type="button" onClick={onBack}>{r('back')}</Button><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button type="button" onClick={() => void check(false)} disabled={working || server.auth !== 'ready'}>{r('reload')}</Button>{canConfirm && <Button type="button" variant="primary" onClick={() => void check(true)} disabled={blocked || !reviewed || !matchesCurrent} busy={working}>{r('confirm')}</Button>}</div>
    </div>
  </Modal>
}
