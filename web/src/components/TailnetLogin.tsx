import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import { errorDetail, errorText, type Translate } from '../i18n'
import { loginText } from '../login-i18n'
import type { Server } from '../useServer'
import { Button, ErrorBanner, Icon } from './ui'

export interface LoginView { state: 'waiting' | 'connected' | 'approval-required'; authUrl?: string; qr?: boolean[][] }
export function readLoginView(value: unknown): LoginView {
  const view = value as LoginView
  if (!view || !['waiting', 'connected', 'approval-required'].includes(view.state)) throw new api.ApiError('invalid_response', '')
  if (view.state !== 'waiting') return { state: view.state }
  if (view.authUrl !== undefined && (typeof view.authUrl !== 'string' || !api.safeAuthURL(view.authUrl))) throw new api.ApiError('login_url_invalid', '')
  if (view.qr !== undefined && (!view.authUrl || !Array.isArray(view.qr) || view.qr.length < 21 || view.qr.length > 185 || view.qr.some(row => !Array.isArray(row) || row.length !== view.qr!.length || row.some(pixel => typeof pixel !== 'boolean')))) throw new api.ApiError('invalid_response', '')
  return { state: view.state, authUrl: view.authUrl, qr: view.qr }
}
export function TailnetLogin({ server, locale, t, disabled }: { server: Server; locale: api.Locale; t: Translate; disabled: boolean }) {
  const l = (key: string) => loginText(locale, key)
  const [view, setView] = useState<LoginView>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [wantQR, setWantQR] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)
  const mounted = useRef(true)
  const clear = useCallback(() => { ++generation.current; pending.current?.abort(); pending.current = null; setView(undefined); setWantQR(false); setBusy(false); setError(null) }, [])
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; ++generation.current; pending.current?.abort() } }, [])
  useEffect(() => { if (disabled) clear() }, [disabled, clear])
  const request = useCallback(async (start: boolean, refresh = false, qr = false) => {
    if (disabled || pending.current) return
    const current = ++generation.current
    const controller = new AbortController(); pending.current = controller
    setBusy(true); setError(null)
    if (start) { setView(undefined); setWantQR(qr) }
    try {
      const result = start ? await api.command('network.login', { ...(refresh ? { refresh: true } : {}), ...(qr ? { qr: true } : {}) }, api.requestID(), controller.signal) : await api.command('network.login.status', qr ? { qr: true } : {}, api.requestID(), controller.signal)
      if (!mounted.current || current !== generation.current) return
      const next = readLoginView(result.result)
      setView(next)
      if (next.state !== 'waiting') { setWantQR(false); void server.refresh() }
    } catch (value) {
      if (!mounted.current || controller.signal.aborted || current !== generation.current) return
      setView(undefined); setWantQR(false)
      if (value instanceof api.ApiError && value.code === 'unauthenticated') server.handleError(value)
      else setError(value)
    } finally { if (pending.current === controller) pending.current = null; if (mounted.current && current === generation.current) setBusy(false) }
  }, [disabled, server.refresh, server.handleError])
  useEffect(() => {
    if (view?.state !== 'waiting' || disabled) return
    const timer = setInterval(() => { if (document.visibilityState !== 'hidden') void request(false, false, wantQR) }, 2000)
    return () => clearInterval(timer)
  }, [view?.state, disabled, wantQR, request])
  const qrPath = view?.qr?.flatMap((row, y) => row.flatMap((pixel, x) => pixel ? [`M${x} ${y}h1v1h-1z`] : [])).join('')
  const code = (error as { code?: string })?.code || ''
  return <div className="subsection tailnet-login">{error != null && <ErrorBanner message={l(code) || errorText(error, t)} detail={errorDetail(error, t)} t={t} />}<div className="login-actions">{!view && <Button type="button" onClick={() => request(true)} disabled={disabled} busy={busy}><Icon name="globe" />{t('signInTailscale')}</Button>}<Button type="button" onClick={() => request(false, false, wantQR)} disabled={disabled || busy}>{l('check')}</Button></div>
    {view?.state === 'connected' && <p className="scope-note" role="status">{l('connected')}</p>}{view?.state === 'approval-required' && <div role="status"><p className="scope-note">{l('approval')}</p><p className="small muted">{l('approvalHint')}</p></div>}
    {view?.state === 'waiting' && <div><p role="status">{l('waiting')}</p>{view.authUrl ? <div className="signin-link auth-private"><p className="muted">{t('signInLinkHint')}</p><a href={view.authUrl} target="_blank" rel="noreferrer noopener" className="button button-primary">{t('continueSignIn')}<Icon name="arrow" /></a><div className="login-actions"><Button type="button" disabled={busy || disabled} onClick={() => { if (view.qr) { setWantQR(false); setView({ ...view, qr: undefined }) } else { setWantQR(true); void request(false, false, true) } }}>{l(view.qr ? 'hideQR' : 'qr')}</Button><Button type="button" disabled={busy || disabled} onClick={() => request(true, true, wantQR)}>{l('refresh')}</Button></div>{view.qr && <figure className="auth-qr auth-private"><svg role="img" aria-label={l('qrAlt')} viewBox={`0 0 ${view.qr.length} ${view.qr.length}`}><rect width="100%" height="100%" className="qr-background" /><path d={qrPath} className="qr-modules" /></svg><figcaption className="small muted">{l('qrHint')}</figcaption></figure>}</div> : <p className="small muted">{l('noLink')}</p>}<Button type="button" onClick={clear}>{l('dismiss')}</Button></div>}
  </div>
}
