import { useEffect, useRef, useState, type FormEvent } from 'react'
import * as api from '../api'
import { errorText, type Translate } from '../i18n'
import { serviceText } from '../service-i18n'
import { portProposalText } from '../port-proposals-i18n'
import { readPortProposals, type PortProposalChoice } from '../port-proposals'
import { loopbackEndpoint, readServiceConfig } from '../service-form'
import { previewPorts } from '../ports'
import type { Server } from '../useServer'
import { Button, ErrorBanner, Modal, useAlive } from './ui'

export function PortProposalsDialog({ id, server, locale, t, onClose, onChoose }: { id: string; server: Server; locale: api.Locale; t: Translate; onClose: () => void; onChoose: (choice: PortProposalChoice) => void }) {
  const p = (key: string) => portProposalText(locale, key)
  const alive = useAlive()
  const [source, setSource] = useState<api.ServiceConfigResult>()
  const [result, setResult] = useState<api.ServicePortProposals>()
  const [selected, setSelected] = useState<number>()
  const [page, setPage] = useState(0)
  const [fromPort, setFromPort] = useState('49152')
  const [count, setCount] = useState('0')
  const [attempts, setAttempts] = useState('0')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)
  const sourceID = useRef(id); sourceID.current = id
  const stale = useRef(server.stale); stale.current = server.stale
  const active = Boolean(source?.active || server.state?.services.some(service => service.id === id && ['active', 'reconnecting'].includes(service.status)))
  const runtimeActive = useRef(active); runtimeActive.current = active
  const invalidate = () => { ++generation.current; pending.current?.abort(); pending.current = null; setBusy(false); setResult(undefined); setSelected(undefined); setPage(0) }
  const close = () => { invalidate(); onClose() }
  const report = (value: unknown) => {
    if (value instanceof api.ApiError && value.code === 'unauthenticated') server.handleError(value)
    else {
      const code = value instanceof api.ApiError ? value.code : 'invalid_response'
      if (['service_revision_conflict', 'service_not_found', 'service_backend_mismatch', 'service_active'].includes(code)) setSource(undefined)
      setError(code)
    }
  }
  const load = async () => {
    if (stale.current) return
    invalidate(); setSource(undefined); setError('')
    const current = generation.current; const target = id
    const request = new AbortController(); pending.current = request; setBusy(true)
    try {
      const response = await api.command('service.config', { id: target }, api.requestID(), request.signal)
      if (!alive.current || request.signal.aborted || current !== generation.current || sourceID.current !== target || stale.current) return
      setSource(readServiceConfig(response.result, target, 'connect'))
    } catch (value) { if (alive.current && !request.signal.aborted && current === generation.current && sourceID.current === target && !stale.current) report(value) }
    finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy(false) } }
  }
  useEffect(() => { void load(); return () => { ++generation.current; pending.current?.abort(); pending.current = null } }, [id])
  useEffect(() => { if (server.stale || active) { invalidate(); setSource(undefined); setError(server.stale ? 'stale' : 'service_active') } }, [server.stale, active])
  const valid = /^\d+$/.test(fromPort) && Number(fromPort) >= 1024 && Number(fromPort) <= 65535 && [count, attempts].every(value => /^\d+$/.test(value) && Number.isSafeInteger(Number(value)) && Number(value) <= 64512)
  const invalidSource = ['stale', 'service_revision_conflict', 'service_not_found', 'service_backend_mismatch', 'service_active'].includes(error)
  const blocked = busy || server.stale || active || invalidSource
  const check = async (event: FormEvent) => {
    event.preventDefault()
    if (!source || blocked || !valid || pending.current) return
    invalidate(); setError('')
    const current = generation.current; const target = id; const checked = source
    const request = new AbortController(); pending.current = request; setBusy(true)
    const payload = { id: target, expectedRevision: checked.revision, fromPort: Number(fromPort), count: Number(count), attempts: Number(attempts) }
    try {
      // Every observation is fresh and outside mutation retry guards.
      const response = await api.command('service.ports', payload, api.requestID(), request.signal)
      if (!alive.current || request.signal.aborted || current !== generation.current || sourceID.current !== target || stale.current || runtimeActive.current) return
      const value = readPortProposals(response.result, checked)
      if (value.fromPort !== payload.fromPort || payload.count > 0 && value.requestedCount !== payload.count || payload.attempts > 0 && value.attemptBudget !== payload.attempts) throw new api.ApiError('invalid_response', '')
      setResult(value)
    } catch (value) { if (alive.current && !request.signal.aborted && current === generation.current && sourceID.current === target && !stale.current && !runtimeActive.current) report(value) }
    finally { if (pending.current === request) { pending.current = null; if (alive.current) setBusy(false) } }
  }
  const choose = () => {
    if (!source || !result || blocked || !result.proposals.some(proposal => proposal.localPort === selected)) return
    const choice = { source: structuredClone(source), localPort: selected!, checkedAt: result.checkedAt }
    invalidate(); onChoose(choice)
  }
  const change = (set: (value: string) => void, value: string) => { invalidate(); setError(''); set(value) }
  const c = source?.configuration
  let mapping: ReturnType<typeof previewPorts> | undefined
  try { if (c) mapping = previewPorts(c.ports, c.excludePorts || '', c.localPort ? String(c.localPort) : '', 'connect', [], { maxListeners: 65535 }) } catch { /* Core returns the authoritative typed mapping error. */ }
  const mappingText = (localPort?: number) => c ? previewPorts(c.ports, c.excludePorts || '', localPort ? String(localPort) : '', 'connect', [], { maxListeners: 65535 }).mappings.map(item => `${loopbackEndpoint(c.loopbackHost || '127.0.0.1', item.local)} → ${item.remote}`).join('; ') : ''
  return <Modal title={p('title')} t={t} onClose={close} wide><p>{p('intro')}</p>{error && <ErrorBanner message={p(error) || serviceText(locale, error) || errorText({ code: error }, t)} detail={error} t={t} />}
    {busy && <p role="status">{t('loading')}</p>}
    {c && <section className="service-preview" aria-label={p('source')}><h3>{p('source')}</h3><dl><dt>{t('ruleName')}</dt><dd>{c.name} · <span className="code-value">{c.id}</span></dd><dt>{p('revision')}</dt><dd className="code-value">{source.revision}</dd><dt>{t('protocol')}</dt><dd>{c.backend} · {c.network.toUpperCase()}</dd><dt>{p('family')}</dt><dd>{c.loopbackHost === '::1' ? 'IPv6 · ::1' : 'IPv4 · 127.0.0.1'}</dd><dt>{p('peer')}</dt><dd className="code-value">{c.peerId}</dd><dt>{t('ports')}</dt><dd>{c.ports}</dd><dt>{t('excludePorts')}</dt><dd>{c.excludePorts || '—'}</dd><dt>{p('effective')}</dt><dd>{mapping?.ports || t('unavailable')}</dd><dt>{p('original')}</dt><dd className="code-value">{mapping ? mappingText(c.localPort) : t('unavailable')}</dd><dt>{serviceText(locale, 'lifetime')}</dt><dd>{c.lifetime === 'until-stopped' ? serviceText(locale, 'untilStopped') : `${c.ttlSeconds} ${serviceText(locale, 'customLifetime')}`}</dd></dl></section>}
    <form className="form-stack" onSubmit={check}><details className="advanced"><summary>{p('policy')}</summary><dl>{['portProposalResults', 'portProposalAttempts', 'portProposalBinds', 'portProposalSeconds', 'materializedListeners'].map(key => <div key={key}><dt>{p(key)}</dt><dd>{server.state?.limits?.effective.resources[key]?.value ?? p('unknown')}</dd></div>)}</dl><p className="small muted">{p('defaults')}</p></details><label className="field">{p('from')}<input type="number" min="1024" max="65535" step="1" disabled={busy} value={fromPort} onChange={event => change(setFromPort, event.target.value)} /></label><div className="form-row"><label className="field grow">{p('count')}<input type="number" min="0" max="64512" step="1" disabled={busy} value={count} onChange={event => change(setCount, event.target.value)} /></label><label className="field grow">{p('attempts')}<input type="number" min="0" max="64512" step="1" disabled={busy} value={attempts} onChange={event => change(setAttempts, event.target.value)} /></label></div><p className="small muted">{p('defaults')}</p>{!valid && <p className="field-error">{p('invalid')}</p>}<Button type="submit" disabled={blocked || !source || !valid}>{p('check')}</Button></form>
    {result && <section className="service-preview" aria-label={p('results')}><h3>{p('results')}</h3><p className="code-value">{result.code}</p>{result.code === 'listener_proposals_exhausted' && <p role="status">{p(result.code)}</p>}<p>{p('warning')}</p><p>{p('mapping')}</p><dl><dt>{p('checkedAt')}</dt><dd><time dateTime={result.checkedAt}>{result.checkedAt}</time></dd><dt>{p('conflict')}</dt><dd>{result.conflictPort}</dd><dt>{p('from')}</dt><dd>{result.fromPort}</dd><dt>{p('count')} / {p('attempts')}</dt><dd>{result.requestedCount} / {result.attemptBudget}</dd><dt>{p('effective')}</dt><dd>{result.effectivePorts}</dd><dt>{p('performed')}</dt><dd>{result.attempts} / {result.bindChecks}</dd><dt>{p('stop')}</dt><dd>{p(result.stopReason)}</dd></dl><p>{p('bounded')}</p>{result.proposals.length > 0 && <fieldset><legend>{p('choose')}</legend>{result.proposals.slice(page * 20, (page + 1) * 20).map(proposal => <label className="checkbox-field" key={proposal.localPort}><input type="radio" name="port-proposal" value={proposal.localPort} checked={selected === proposal.localPort} disabled={blocked} onChange={() => setSelected(proposal.localPort)} /><span className="code-value">{loopbackEndpoint(c?.loopbackHost || '127.0.0.1', proposal.localPort === proposal.localEnd ? String(proposal.localPort) : `${proposal.localPort}-${proposal.localEnd}`)}</span></label>)}</fieldset>}{result.proposals.length > 20 && <div className="modal-actions"><Button type="button" disabled={page === 0} onClick={() => { setPage(value => value - 1); setSelected(undefined) }}>{p('previous')}</Button><span>{page + 1} / {Math.ceil(result.proposals.length / 20)}</span><Button type="button" disabled={(page + 1) * 20 >= result.proposals.length} onClick={() => { setPage(value => value + 1); setSelected(undefined) }}>{p('next')}</Button></div>}{selected !== undefined && <p className="code-value" aria-label={p('selectedMapping')}>{p('selectedMapping')}: {mappingText(selected)}</p>}<p>{p('draft')}</p>{result.proposals.length > 0 && <Button type="button" variant="primary" disabled={blocked || selected === undefined} onClick={choose}>{p('use')}</Button>}</section>}
    <div className="modal-actions"><Button type="button" disabled={busy || server.stale} onClick={() => void load()}>{p('reload')}</Button><Button type="button" onClick={close}>{t('close')}</Button></div>
  </Modal>
}
