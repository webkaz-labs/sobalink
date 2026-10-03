import { useEffect, useRef, useState } from 'react'
import * as api from '../api'
import { lifecycleText } from '../lifecycle-i18n'
import type { Translate } from '../i18n'
import type { Server } from '../useServer'
import { Button, Modal, useAlive } from './ui'
const prefix = 'soba --state-dir "STATE_DIRECTORY"'
function CommandCopy({ value, label, locale }: { value: string; label: string; locale: api.Locale }) {
  const [status, setStatus] = useState('')
  useEffect(() => setStatus(''), [value])
  const copy = async () => { try { await navigator.clipboard.writeText(value); setStatus('copied') } catch { setStatus('copyFailed') } }
  return <div className="form-stack"><label className="field">{label}<textarea readOnly value={value} rows={2} spellCheck={false} className="code-value" /></label><div><Button type="button" onClick={copy}>{lifecycleText(locale, 'copy')}</Button>{status && <p role="status" className="small muted">{lifecycleText(locale, status)}</p>}</div></div>
}
export function StartupGuide({ locale, t, onClose }: { locale: api.Locale; t: Translate; onClose: () => void }) {
  const l = (key: string) => lifecycleText(locale, key)
  const [mode, setMode] = useState('saved')
  const [action, setAction] = useState('enable')
  const command = `${prefix} autostart ${action} --startup ${mode}`
  return <Modal title={l('startup')} t={t} onClose={onClose} wide><p>{l('startupIntro')}</p><p className="scope-note">{l('registrationStatus')}</p><p className="small muted">{l('registrationHint')}</p><div className="form-stack"><label className="field">{l('action')}<select value={action} onChange={event => setAction(event.target.value)}><option value="enable">{l('enable')}</option><option value="disable">{l('disable')}</option></select></label><label className="field">{l('mode')}<select value={mode} onChange={event => setMode(event.target.value)}><option value="saved">{l('saved')}</option><option value="offline">{l('offline')}</option></select></label><p>{l(mode === 'saved' ? 'savedHint' : 'offlineHint')}</p><p>{l('neverRestart')}</p>{action === 'disable' && <p>{l('disableHint')}</p>}<p className="scope-note">{l('sameDirectory')}</p><CommandCopy label={l('previewCLI')} value={`${command} --json`} locale={locale} /><p>{l('tokenHint')}</p><CommandCopy label={l('applyCLI')} value={`${command} --apply --review REVIEW_TOKEN`} locale={locale} /><details className="advanced"><summary>{t('technicalDetails')}</summary><p>{l('recovery')}</p><CommandCopy label={l('statusCLI')} value={`${prefix} status`} locale={locale} /><CommandCopy label={l('uiCLI')} value={`${prefix} ui`} locale={locale} /></details><div className="modal-actions"><Button onClick={onClose}>{t('close')}</Button></div></div></Modal>
}
export function ServiceOwnership({ service, locale }: { service: Pick<api.Service, 'owner' | 'leaseSeconds' | 'leaseExpiresAt'>; locale: api.Locale }) {
  const l = (key: string) => lifecycleText(locale, key)
  if (!service.owner) return null
  const leased = (service.leaseSeconds || 0) > 0
  return <div className="scope-note service-ownership"><p>{l(leased ? 'taskOwner' : 'operationOwner')}: <span className="code-value">{service.owner}</span></p>{leased && <p>{l('lease')}: {service.leaseSeconds} {l('leaseSeconds')}</p>}{service.leaseExpiresAt && <p>{l('leaseExpires')}: <time dateTime={service.leaseExpiresAt}>{service.leaseExpiresAt}</time></p>}<p className="small">{l(leased ? 'taskHint' : 'ownerHint')}</p></div>
}
export function LogoutControl({ server, locale, t, blocked, onStopping }: { server: Server; locale: api.Locale; t: Translate; blocked: boolean; onStopping: (stopping: boolean) => void }) {
  const l = (key: string) => lifecycleText(locale, key)
  const alive = useAlive()
  const [phase, setPhase] = useState<'idle' | 'review' | 'working' | 'confirmed' | 'unconfirmed' | 'cleanup' | 'unavailable'>('idle')
  const pending = useRef<AbortController | null>(null)
  const state = server.state!
  useEffect(() => () => pending.current?.abort(), [])
  const logout = async () => {
    if (phase !== 'review' || blocked || pending.current) return
    const request = new AbortController(); pending.current = request; setPhase('working'); onStopping(true)
    try {
      const response = await api.command('network.logout', {}, api.requestID(), request.signal)
      if (!alive.current || request.signal.aborted) return
      const value = response.result
      setPhase(value?.logoutConfirmed === true && value.localTrafficStopped === true && value.applicationState === 'stopping' ? 'confirmed' : 'unconfirmed')
    } catch (value) {
      if (!alive.current || request.signal.aborted) return
      const code = value instanceof api.ApiError ? value.code : ''
      if (code === 'unauthenticated') { server.handleError(value); return }
      if (code === 'tailnet_logout_unavailable') { setPhase('unavailable'); onStopping(false) }
      else setPhase(code === 'logout_cleanup_unconfirmed' ? 'cleanup' : 'unconfirmed')
    } finally { if (pending.current === request) pending.current = null }
  }
  if (phase === 'idle') return state.settings?.network === 'tailnet' ? <Button type="button" variant="danger" disabled={blocked} onClick={() => setPhase('review')}>{l('logout')}</Button> : null
  return <section className="invitation-card logout-review" role="region" aria-label={l('reviewLogout')}><h3>{l('reviewLogout')}</h3>{phase === 'review' ? <><p>{l('logoutImpact')}</p><p className="small muted">{l('logoutPreserved')}</p><dl className="details-list"><dt>{l('activeServices')}</dt><dd>{[...state.services, ...state.shares].filter(service => ['active', 'reconnecting'].includes(service.status)).length}</dd><dt>{l('activeProxies')}</dt><dd>{state.proxies?.filter(proxy => proxy.status === 'active').length || 0}</dd><dt>{l('activeTransfers')}</dt><dd>{state.transfers.filter(transfer => ['offered', 'awaiting-acceptance', 'queued', 'transferring', 'saving'].includes(transfer.status)).length}</dd></dl><div className="modal-actions"><Button type="button" onClick={() => setPhase('idle')}>{t('cancel')}</Button><Button type="button" variant="danger" disabled={blocked || state.settings?.network !== 'tailnet'} onClick={() => void logout()}>{l('confirmLogout')}</Button></div></> : <><p role="status">{l(phase === 'working' ? 'submitted' : phase)}</p>{phase === 'unavailable' ? <Button onClick={() => setPhase('idle')}>{t('close')}</Button> : phase !== 'working' && <><p>{l('retryGuide')}</p><CommandCopy label={l('statusCLI')} value={`${prefix} status`} locale={locale} /><CommandCopy label={t('startConnection')} value={`${prefix} start --background`} locale={locale} /><CommandCopy label={l('logout')} value={`${prefix} logout`} locale={locale} /><CommandCopy label={l('uiCLI')} value={`${prefix} ui`} locale={locale} /></>}</>}</section>
}
