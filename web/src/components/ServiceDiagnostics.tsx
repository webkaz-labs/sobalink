import { useEffect, useRef, useState } from 'react'
import * as api from '../api'
import { advancedText } from '../advanced-i18n'
import { includesServicePort, servicePorts, singleServicePort } from '../advanced-connections'
import type { Translate } from '../i18n'
import type { Server } from '../useServer'
import { Button, ErrorBanner, useAlive } from './ui'

export function ServiceDiagnostics({ service, locale, t, server }: { service: api.Service; locale: api.Locale; t: Translate; server: Server }) {
  const p = (key: string) => advancedText(locale, key)
  const alive = useAlive()
  const [port, setPort] = useState(() => singleServicePort(service))
  const [result, setResult] = useState<api.ServiceDiagnostic>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const scope = `${service.id}:${service.status}:${service.network}:${servicePorts(service)}`
  const currentScope = useRef(scope); currentScope.current = scope
  useEffect(() => { setPort(singleServicePort(service)); setResult(undefined); setError(''); pending.current?.abort(); pending.current = null; setBusy(false) }, [scope])
  useEffect(() => () => pending.current?.abort(), [])
  const diagnostic = result && (!service.diagnostic || Date.parse(result.checkedAt) > Date.parse(service.diagnostic.checkedAt)) ? result : service.diagnostic
  const active = service.status === 'active' && service.network === 'tcp'
  if (!active && !diagnostic && !service.lastFailure) return null
  const check = async () => {
    if (!active || server.stale || pending.current || !includesServicePort(service, Number(port))) return
    const request = new AbortController(); pending.current = request
    const checkedScope = scope
    setBusy(true); setError(''); setResult(undefined)
    try {
      const response = await api.command('diagnostics.run', { serviceId: service.id, port: Number(port), probeTCP: true }, api.requestID(), request.signal)
      if (!alive.current || request.signal.aborted || currentScope.current !== checkedScope) return
      const value = response.result as unknown as api.ServiceDiagnostic
      if (!value || value.serviceId !== service.id || value.port !== Number(port) || !['reachable', 'unreachable'].includes(value.transport) || value.application !== 'unverified' || !value.checkedAt || !value.code || !value.nextSteps?.en || !value.nextSteps?.ja) throw new api.ApiError('invalid_response', '')
      setResult(value)
      await server.refresh(true)
    } catch (value) {
      if (!alive.current || request.signal.aborted || currentScope.current !== checkedScope) return
      if (value instanceof api.ApiError && value.code === 'unauthenticated') server.handleError(value)
      else setError(value instanceof api.ApiError ? value.code : 'failed')
    } finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy(false) } }
  }
  return <details className="advanced service-diagnostics"><summary>{p('diagnosticTitle')}</summary><p className="small muted">{p('checkHint')}</p>{active && <><p className="small">{p('effectivePorts')}: <span className="code-value">{servicePorts(service)}</span></p>{!singleServicePort(service) && <label className="field">{p('selectPort')}<input type="number" min="1" max="65535" step="1" value={port} disabled={busy} onChange={event => { setPort(event.target.value); setResult(undefined); setError('') }} /></label>}{port && !includesServicePort(service, Number(port)) && <p className="field-error">{p('invalidPort')}</p>}<Button type="button" onClick={check} busy={busy} disabled={server.stale || !includesServicePort(service, Number(port))}>{p('check')}</Button></>}{error && <ErrorBanner message={p(error) || p('failed')} t={t} />}{diagnostic && <section className="diagnostic-result" role="status"><strong>{p('transport')}: {p(diagnostic.transport)}</strong><p>{p('application')}</p><p className="code-value">{diagnostic.code} · TCP {diagnostic.port}</p><p>{p('checkedAt')}: <time dateTime={diagnostic.checkedAt}>{diagnostic.checkedAt}</time></p><p>{diagnostic.nextSteps[locale]}</p></section>}{service.lastFailure && <section className="diagnostic-result"><h4>{p('lastFailure')}</h4><p className="code-value">{service.lastFailure.code}</p><p>{p('failureAt')}: <time dateTime={service.lastFailure.at}>{service.lastFailure.at}</time></p><p>{service.lastFailure.nextSteps[locale]}</p><p className="small muted">{p('historyHint')}</p></section>}</details>
}
