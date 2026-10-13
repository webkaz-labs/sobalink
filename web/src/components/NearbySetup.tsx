import { useEffect, useImperativeHandle, useRef, useState, type FormEvent, type Ref } from 'react'
import type { CommandPayloads, Locale } from '../api'
import type { Server } from '../useServer'
import { MAX_SETUP_TEXT_BYTES, readSetupContact, readSetupFingerprint, readSetupScopes, readSetupSnapshot, setupEndpoint, setupTerminal, validSetupPort, validSetupAlias, type SetupCommandPayloads, type SetupPlan, type SetupScope, type SetupSnapshot } from '../nearby-setup'
import { nearbyText, nearbyReasonKey, nearbyNextAction, type NearbyTextKey } from '../nearby-i18n'
import { PngCardImporter } from '../qr/png-import'
import { pngPolicy } from '../qr/png-policy'
import { Button, useAlive } from './ui'
import './NearbySetup.css'

export interface NearbySetupHandle { close: () => Promise<boolean> }
export function NearbySetup({ server, locale, disabled = false, ref }: { server: Server; locale: Locale; disabled?: boolean; ref?: Ref<NearbySetupHandle> }) {
  const c = (key: NearbyTextKey) => nearbyText(locale, key)
  const cleanupWaiters = useRef(new Set<(done: boolean) => void>())
  const alive = useAlive(), pending = useRef(false), reading = useRef(false), generation = useRef(0)
  const starting = useRef<(SetupCommandPayloads['setup.start'] & { previousSessionId?: string }) | undefined>(undefined)
  const newFormOwner = useRef<string | undefined>(undefined)
  const [newFormIntent, setNewFormIntent] = useState<string>()
  const current = useRef<SetupSnapshot | undefined>(undefined), latestServer = useRef(server)
  latestServer.current = server
  const [snapshot, setSnapshot] = useState<SetupSnapshot>(), [scopes, setScopes] = useState<SetupScope[]>()
  const [busy, setBusy] = useState(false), [loading, setLoading] = useState(true), [uncertain, setUncertain] = useState(false), uncertainRef = useRef(false)
  const [error, setError] = useState<NearbyTextKey>(), [now, setNow] = useState(Date.now())
  const [alias, setAlias] = useState(''), [scopeId, setScopeId] = useState(''), [bootstrapPort, setBootstrapPort] = useState('48445'), [relayPort, setRelayPort] = useState('48443')
  const [allowHost, setAllowHost] = useState<boolean>(), [consent, setConsent] = useState(false)
  const [retryReview, setRetryReview] = useState<string>()
  const finishCleanupWait = (done: boolean) => { for (const complete of cleanupWaiters.current) complete(done); cleanupWaiters.current.clear() }
  const markUncertain = (key: NearbyTextKey) => { finishCleanupWait(false); uncertainRef.current = true; setUncertain(true); setError(key) }
  const accept = (next: SetupSnapshot) => { if (['cancelled', 'expired', 'failed'].includes(next.state) && (current.current?.sessionId !== next.sessionId || current.current?.state !== next.state)) resetForm(); current.current = next; setSnapshot(next); uncertainRef.current = false; setUncertain(false); setError(undefined); if (setupTerminal(next)) { setConsent(false); const safe = !next.connected && next.state !== 'recovery_required' || next.connected && next.nextAction === 'start_new_setup' && newFormOwner.current === next.sessionId; if (cleanupWaiters.current.size && !safe) setError(next.connected ? 'alreadyConnected' : 'recovery'); finishCleanupWait(safe) } }
  const unavailable = disabled || server.auth !== 'ready' || server.stale
  const scope = scopes?.find(item => item.scopeId === scopeId)
  const expired = Boolean(snapshot?.expiresAt && Date.parse(snapshot.expiresAt) <= now && snapshot.state !== 'idle')
  const active = Boolean(snapshot?.sessionId && !setupTerminal(snapshot))
  const freshForm = Boolean(snapshot?.connected && snapshot.nextAction === 'start_new_setup' && snapshot.process.state === 'ready' && newFormIntent === snapshot.sessionId && !starting.current)
  const retryable = Boolean(snapshot?.sessionId && snapshot.state === 'failed' && snapshot.nextAction === 'try_again' && snapshot.process.state === 'ready' && !snapshot.paired && !snapshot.connected)
  const retryForm = retryable && retryReview === snapshot?.sessionId
  const startable = snapshot && (freshForm || ['idle', 'cancelled', 'expired'].includes(snapshot.state) || retryForm) && snapshot.process.state === 'ready'
  const resetForm = () => { setAlias(''); setScopeId(''); setBootstrapPort('48445'); setRelayPort('48443'); setAllowHost(undefined); setConsent(false) }
  const loadScopes = async () => {
    if (latestServer.current.auth !== 'ready' || pending.current) return
    const result = await latestServer.current.run('setup.scopes', {}, 'setup.scopes')
    if (!alive.current) return
    const choices = result && readSetupScopes(result.result)
    if (!choices) { setError('failed'); return }
    setScopes(choices); setScopeId(old => choices.some(item => item.scopeId === old) ? old : !newFormOwner.current && choices.length === 1 ? choices[0].scopeId : '')
  }
  const status = async (): Promise<SetupSnapshot | undefined> => {
    if (pending.current || reading.current || latestServer.current.auth !== 'ready') return
    reading.current = true
    const sequence = generation.current, sessionId = starting.current ? undefined : current.current?.sessionId
    try {
      const result = await latestServer.current.run('setup.status', sessionId ? { sessionId } : {}, 'setup.status')
      if (!alive.current || sequence !== generation.current) return
      const next = result && readSetupSnapshot(result.result, sessionId)
      if (!next) { markUncertain(result ? 'invalidResponse' : 'uncertain'); return }
      if (starting.current) {
        if (starting.current.previousSessionId && (!next.sessionId || next.sessionId === starting.current.previousSessionId)) { markUncertain('uncertain'); return }
        if (next.sessionId && (next.scope?.scopeId !== starting.current.scopeId || next.contact?.alias !== starting.current.alias || next.contact.bootstrap !== setupEndpoint(next.scope.localAddress, starting.current.bootstrapPort))) { markUncertain('invalidResponse'); return }
        newFormOwner.current = undefined; setNewFormIntent(undefined)
      }
      starting.current = undefined; accept(next); return next
    } catch { if (alive.current && sequence === generation.current) markUncertain('uncertain'); return }
    finally { reading.current = false; if (alive.current) setLoading(false) }
  }
  useEffect(() => {
    let stopped = false
    void Promise.resolve().then(() => { if (!stopped) { void loadScopes(); void status() } })
    const timer = setInterval(() => { setNow(Date.now()); if (current.current && (!setupTerminal(current.current) || newFormOwner.current === current.current.sessionId && current.current.connected && current.current.nextAction !== 'start_new_setup') && document.visibilityState !== 'hidden') void status() }, 1500)
    return () => { stopped = true; ++generation.current; clearInterval(timer); finishCleanupWait(false) }
  }, [])
  useEffect(() => { if (unavailable) { ++generation.current; setConsent(false); if (pending.current || current.current && !setupTerminal(current.current)) markUncertain('uncertain') } }, [unavailable])
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (pending.current || uncertainRef.current || (current.current && !setupTerminal(current.current))) { event.preventDefault(); event.returnValue = '' } }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [])
  async function mutate<N extends Exclude<keyof SetupCommandPayloads, 'setup.scopes' | 'setup.status'>>(name: N, payload: CommandPayloads[N]): Promise<SetupSnapshot | undefined> {
    if (pending.current || unavailable || uncertainRef.current) return
    pending.current = true; setBusy(true); setError(undefined); const sequence = ++generation.current
    const expectedSession = name === 'setup.start' ? undefined : current.current?.sessionId
    if (name === 'setup.start') { starting.current = { ...payload as SetupCommandPayloads['setup.start'], previousSessionId: current.current?.sessionId }; setConsent(false) }
    try {
      const result = await latestServer.current.run(name, payload, 'setup.mutation')
      if (!alive.current || sequence !== generation.current) return
      const next = result && readSetupSnapshot(result.result, expectedSession)
      if (!next) { markUncertain(result ? 'invalidResponse' : 'uncertain'); return }
      if (name === 'setup.start' && next.sessionId) {
        const input = payload as SetupCommandPayloads['setup.start']
        if (next.sessionId === starting.current?.previousSessionId || next.scope?.scopeId !== input.scopeId || next.contact?.alias !== input.alias || next.contact.bootstrap !== setupEndpoint(next.scope.localAddress, input.bootstrapPort)) { markUncertain('invalidResponse'); return }
      }
      if (name === 'setup.manual' && next.selected) {
        const input = payload as SetupCommandPayloads['setup.manual']
        if (JSON.stringify(next.selected) !== JSON.stringify(readSetupContact(input.contactText))) { markUncertain('invalidResponse'); return }
      }
      if (name === 'setup.approve' && next.localApproved) {
        const input = payload as SetupCommandPayloads['setup.approve']
        if (next.plan?.revision !== input.revision || next.plan.fingerprint !== input.fingerprint) { markUncertain('invalidResponse'); return }
      }
      if (name === 'setup.start') { newFormOwner.current = undefined; setNewFormIntent(undefined) }
      starting.current = undefined; accept(next); return next
    } catch { if (alive.current) markUncertain('uncertain'); return }
    finally { pending.current = false; if (alive.current) setBusy(false) }
  }
  const cancel = async (): Promise<boolean> => {
    if (pending.current || unavailable) { setError('closePending'); return false }
    let next = current.current
    if (uncertainRef.current || !next) next = await status()
    if (!next || uncertainRef.current) return false
    if (setupTerminal(next) && !(next.connected && newFormOwner.current === next.sessionId && next.nextAction !== 'start_new_setup')) return true
    if (next.state === 'cancelling') return new Promise(resolve => cleanupWaiters.current.add(resolve))
    const result = await mutate('setup.cancel', { sessionId: next.sessionId! })
    if (result?.state === 'cancelling') return new Promise(resolve => cleanupWaiters.current.add(resolve))
    if (!result || !setupTerminal(result)) return false
    if (result.connected && newFormOwner.current === result.sessionId && result.nextAction === 'start_new_setup') return true
    if (result.state === 'recovery_required' || result.connected) { setError(result.connected ? 'alreadyConnected' : 'recovery'); return false }
    setConsent(false); return true
  }
  const connectAnother = async () => {
    if (pending.current || unavailable || uncertainRef.current) return
    const previous = current.current
    if (!previous?.connected || previous.process.state !== 'ready' || !previous.sessionId) return
    if (!newFormOwner.current) {
      newFormOwner.current = previous.sessionId; setNewFormIntent(previous.sessionId)
      resetForm()
      ++generation.current
      void loadScopes()
    }
    if (newFormOwner.current !== previous.sessionId || previous.nextAction === 'start_new_setup') return
    await mutate('setup.cancel', { sessionId: previous.sessionId })
  }
  useImperativeHandle(ref, () => ({ close: cancel }))
  const start = (event: FormEvent) => {
    event.preventDefault()
    if (!startable || allowHost === undefined || !consent || !scope || !validSetupAlias(alias) || !validSetupPort(Number(bootstrapPort)) || (allowHost && (!validSetupPort(Number(relayPort)) || Number(bootstrapPort) === Number(relayPort)))) { setError('invalidInput'); return }
    void mutate('setup.start', { alias, scopeId: scope.scopeId, bootstrapPort: Number(bootstrapPort), allowHost, relayPort: allowHost ? Number(relayPort) : 0, consent: true })
  }
  const blocked = unavailable || busy || uncertain || snapshot?.state === 'cancelling'
  const errorCode = server.error && typeof server.error === 'object' && 'code' in server.error ? server.error.code : undefined
  const reasonKey = nearbyReasonKey(snapshot?.reasonCode || errorCode)
  return <section className="nearby-setup form-stack" aria-label={c('title')}>
    <h3>{c('title')}</h3><p className="muted">{c('intro')}</p>
    {loading && <p role="status">{c('loading')}</p>}{error && <p role="alert" className="scope-note">{c(error)}</p>}{reasonKey && reasonKey !== error && <p className="scope-note">{c(reasonKey)}</p>}
    {(uncertain || !snapshot) && !loading && !active && <Button disabled={unavailable || busy} onClick={() => void status()}>{c('checkStatus')}</Button>}
    {snapshot?.process.state === 'stop_required' || snapshot?.state === 'stop_required' ? <p className="scope-note" role="status">{c('stopRequired')}</p> : snapshot?.process.state === 'needs_restart' ? <p className="scope-note" role="status">{c('needsRestart')}</p> : null}
    {snapshot?.state === 'cancelling' && <p role="status">{c('cancelling')}</p>}
    {snapshot?.state === 'cancelled' && <p role="status">{c('cancelled')}</p>}{(snapshot?.state === 'expired' || expired && active) && <p role="status">{c('expired')}</p>}
    {newFormIntent && <p className="scope-note" role="status">{c(starting.current ? 'newStartPending' : freshForm ? 'freshForm' : 'finishPrevious')}</p>}
    {snapshot?.connected && !freshForm && !starting.current && <Button variant="primary" disabled={blocked || snapshot.process.state !== 'ready'} onClick={() => void connectAnother()}>{c(newFormIntent ? 'finishPreviousAction' : 'connectAnother')}</Button>}
    {retryable && !retryForm && <Button disabled={blocked} onClick={() => { resetForm(); setRetryReview(snapshot!.sessionId) }}>{c(snapshot?.reasonCode === 'setup_no_permitted_host' ? 'reviewHost' : 'startAgain')}</Button>}
    {retryForm && <p className="scope-note" role="status">{c('retryReview')}</p>}
    {startable && <form onSubmit={start} className="form-stack">
      <label className="field">{c('alias')}<input value={alias} maxLength={80} disabled={blocked} onChange={event => { setAlias(event.target.value); setConsent(false) }} autoComplete="off" required /><small className="muted">{c('aliasHint')}</small></label>
      <label className="field">{c('scope')}<select value={scopeId} disabled={blocked} onChange={event => { setScopeId(event.target.value); setConsent(false) }} required><option value="">{c('chooseScope')}</option>{scopes?.map(item => <option key={item.scopeId} value={item.scopeId}>{item.localAddress} · {item.prefix}</option>)}</select></label>
      {scopes?.length === 0 && <p>{c('noScopes')}</p>}<Button type="button" disabled={blocked} onClick={() => { setConsent(false); void loadScopes() }}>{c('refreshScopes')}</Button>{scope && <ScopeDetails scope={scope} locale={locale} />}
      <details><summary>{c('ports')}</summary><label className="field">{c('bootstrapPort')}<input type="number" min={1024} max={65535} value={bootstrapPort} disabled={blocked} onChange={event => { setBootstrapPort(event.target.value); setConsent(false) }} /></label><label className="field">{c('relayPort')}<input type="number" min={1024} max={65535} value={relayPort} disabled={blocked} onChange={event => { setRelayPort(event.target.value); setConsent(false) }} /></label></details>
      {scope && <p className="code-value">{c('bootstrapPort')}: {setupEndpoint(scope.localAddress, Number(bootstrapPort))}</p>}
      <fieldset className="nearby-host-choice"><legend>{c('hostChoice')}</legend><p className="small muted">{c('hostHint')}</p>
        <label className="checkbox-field"><input type="radio" name="nearby-host-role" checked={allowHost === true} disabled={blocked} onChange={() => { setAllowHost(true); setConsent(false) }} />{c('allowHost')}</label>
        <label className="checkbox-field"><input type="radio" name="nearby-host-role" checked={allowHost === false} disabled={blocked} onChange={() => { setAllowHost(false); setConsent(false) }} />{c('otherHost')}</label>
        {allowHost === false && <p className="scope-note">{c('otherHostHint')}</p>}{allowHost === true && scope && <p className="code-value">{c('relay')}: {setupEndpoint(scope.localAddress, Number(relayPort))}</p>}
      </fieldset>
      <label className="checkbox-field"><input type="checkbox" checked={consent} disabled={blocked || !scope || allowHost === undefined} onChange={event => setConsent(event.target.checked)} />{c('consent')}</label><p className="small muted">{c('consentHint')}</p><p className="small muted">{c('stopImpact')}</p><Button type="submit" variant="primary" disabled={blocked || !consent || !scope || !alias || allowHost === undefined} busy={busy}>{c('start')}</Button>
    </form>}
    {snapshot?.scope && !startable && <ScopeDetails scope={snapshot.scope} locale={locale} />}
    {!freshForm && snapshot?.sessionId && snapshot.expiresAt && <p className="small muted">{c('expires')}: <time dateTime={snapshot.expiresAt}>{new Date(snapshot.expiresAt).toLocaleTimeString(locale)}</time>{active && <> · {c('countdown')}: {Math.max(0, Math.ceil((Date.parse(snapshot.expiresAt) - now) / 1000))}</>}</p>}
    {active && snapshot?.state !== 'cancelling' && !snapshot?.plan && <>
      <p role="status">{c(snapshot?.selected ? 'selectedWaiting' : snapshot?.state === 'discovering' ? 'searching' : 'preparing')}</p><p className="scope-note">{c('unverified')}</p>{snapshot?.discovery.state === 'unavailable' && <p className="scope-note">{c('discoveryUnavailable')}</p>}{!snapshot?.candidates.some(item => Date.parse(item.expiresAt) > now && Date.parse(item.contact.expiresAt) > now) && <p>{c('noCandidates')}</p>}
      <ul className="nearby-candidates">{snapshot?.candidates.filter(item => Date.parse(item.expiresAt) > now && Date.parse(item.contact.expiresAt) > now).map(item => <li key={item.candidateId}><strong>{item.contact.alias}</strong><span className="code-value">{item.contact.bootstrap}</span><Button disabled={blocked || expired || Boolean(snapshot.selected)} onClick={() => { if (Date.parse(item.expiresAt) > Date.now() && Date.parse(item.contact.expiresAt) > Date.now()) void mutate('setup.select', { sessionId: snapshot.sessionId!, candidateId: item.candidateId }) }}>{c('select')}</Button></li>)}</ul>
      {snapshot?.contactText && <ContactExchange key={JSON.stringify([snapshot.sessionId, unavailable, uncertain])} snapshot={snapshot} server={server} locale={locale} disabled={blocked || expired || Boolean(snapshot.selected)} onSelect={text => void mutate('setup.manual', { sessionId: snapshot.sessionId!, contactText: text })} />}
    </>}
    {!newFormIntent && !retryForm && snapshot?.plan && <PlanReview key={JSON.stringify([snapshot.sessionId, snapshot.plan, unavailable, uncertain])} snapshot={snapshot} locale={locale} server={server} disabled={blocked || expired || snapshot.process.state !== 'ready' || snapshot.state !== 'awaiting_approval'} onApprove={() => { if (!current.current || current.current.sessionId !== snapshot.sessionId || JSON.stringify(current.current.plan) !== JSON.stringify(snapshot.plan) || current.current.localApproved || Date.parse(snapshot.plan!.expiresAt) <= Date.now()) return; void mutate('setup.approve', { sessionId: snapshot.sessionId!, revision: snapshot.plan!.revision, fingerprint: snapshot.plan!.fingerprint, verified: true }) }} />}
    {snapshot?.connected ? !newFormIntent && <p role="status" className="scope-note">{c('connected')}</p> : snapshot?.paired ? <p role="status">{c('paired')}</p> : snapshot?.plan && !retryForm && <p role="status">{c(snapshot.state === 'awaiting_approval' ? 'awaiting' : snapshot.state === 'recovery_required' ? 'recovery' : snapshot.state === 'failed' ? 'failed' : snapshot.state === 'cancelling' ? 'cancelling' : snapshot.state === 'cancelled' ? 'cancelled' : snapshot.state === 'expired' ? 'expired' : 'preparing')}</p>}
    {snapshot?.state === 'recovery_required' && !snapshot.plan && <p role="alert">{c('recovery')}</p>}{snapshot?.state === 'failed' && !retryForm && !snapshot.plan && <p role="alert">{c('failed')}</p>}
    {snapshot?.connected && !newFormIntent && <p className="small muted">{c('connectedReceipt')}</p>}
    <p className="small muted">{c('trustSeparate')}</p>{snapshot && <details><summary>{c('details')}</summary><dl><dt>{c('reason')}</dt><dd>{snapshot.reasonCode || snapshot.discovery.reasonCode || '—'}</dd><dt>{c('nextAction')}</dt><dd>{nearbyNextAction(locale, snapshot.nextAction)}</dd></dl></details>}
    {active && <div className="modal-actions"><Button disabled={unavailable || busy} onClick={() => void status()}>{c('checkStatus')}</Button><Button variant="danger" disabled={unavailable || busy || snapshot?.state === 'cancelling'} onClick={() => void cancel()}>{c('cancel')}</Button></div>}
  </section>
}
function ScopeDetails({ scope, locale }: { scope: SetupScope; locale: Locale }) { const c = (key: NearbyTextKey) => nearbyText(locale, key); return <details><summary>{c('scopeDetails')}</summary><dl><dt>{c('localAddress')}</dt><dd className="code-value">{scope.localAddress}</dd><dt>{c('prefix')}</dt><dd className="code-value">{scope.prefix}</dd><dt>{c('interfaceIndex')}</dt><dd>{scope.interfaceIndex} · {scope.interface}</dd></dl></details> }
function QR({ value, label }: { value: boolean[][]; label: string }) { return <figure className="nearby-qr"><svg role="img" aria-label={label} viewBox={`0 0 ${value.length} ${value.length}`} shapeRendering="crispEdges"><rect width="100%" height="100%" fill="#fff" /><path fill="#000" d={value.flatMap((row, y) => row.flatMap((pixel, x) => pixel ? [`M${x} ${y}h1v1h-1z`] : [])).join('')} /></svg></figure> }
function CopyText({ text, label, locale }: { text: string; label: string; locale: Locale }) {
  const [notice, setNotice] = useState<NearbyTextKey>(), field = useRef<HTMLTextAreaElement>(null), alive = useAlive()
  const copy = async () => { try { if (!navigator.clipboard?.writeText) throw new Error(); await navigator.clipboard.writeText(text); if (alive.current) setNotice('copied') } catch { if (alive.current) { setNotice('copyFailed'); field.current?.focus(); field.current?.select() } } }
  return <div className="form-stack"><label className="field">{label}<textarea ref={field} rows={3} readOnly value={text} className="code-value" spellCheck={false} /></label><Button type="button" onClick={() => void copy()}>{nearbyText(locale, 'copy')}</Button>{notice && <p role="status">{nearbyText(locale, notice)}</p>}</div>
}
function TypedInput({ kind, server, locale, label, pngLabel, disabled, value, onChange }: { kind: 'setup-contact' | 'setup-fingerprint'; server: Server; locale: Locale; label: string; pngLabel: string; disabled: boolean; value: string; onChange: (value: string) => void }) {
  const importer = useRef<PngCardImporter | undefined>(undefined), sequence = useRef(0), alive = useAlive(), [reading, setReading] = useState(false), [notice, setNotice] = useState<NearbyTextKey>()
  const c = (key: NearbyTextKey) => nearbyText(locale, key), budget = pngPolicy(server.state?.limits?.effective.resources)
  const invalidate = () => { ++sequence.current; importer.current?.cancel(); setReading(false); setNotice(undefined) }
  const binding = JSON.stringify([kind, disabled, budget, server.auth, server.stale])
  useEffect(() => { invalidate(); return () => { ++sequence.current; importer.current?.dispose(); importer.current = undefined } }, [binding])
  const file = (selected?: File) => {
    if (!selected || disabled) return
    invalidate(); onChange(''); const generation = sequence.current
    if (!budget) { setNotice('pngFailed'); return }
    importer.current ??= new PngCardImporter(); setReading(true)
    void importer.current.start(selected, kind, budget).then(result => {
      if (!alive.current || generation !== sequence.current) return
      setReading(false)
      if (!result.ok) { if (result.error !== 'cancelled') setNotice('pngFailed'); return }
      if (!(kind === 'setup-contact' ? readSetupContact(result.text) : readSetupFingerprint(result.text))) { setNotice('pngFailed'); return }
      onChange(result.text); setNotice('pngReady')
    }).catch(() => { if (alive.current && generation === sequence.current) { setReading(false); setNotice('pngFailed') } })
  }
  return <div className="form-stack"><label className="field">{label}<textarea value={value} rows={3} maxLength={MAX_SETUP_TEXT_BYTES} disabled={disabled} autoComplete="off" spellCheck={false} className="code-value" onChange={event => { invalidate(); onChange(event.target.value.length <= MAX_SETUP_TEXT_BYTES ? event.target.value : '') }} /></label><label className="field">{pngLabel}<input type="file" accept="image/png,.png" disabled={disabled} onChange={event => { const selected = event.target.files?.[0]; event.target.value = ''; file(selected) }} /></label><p className="small muted">{c('pngHint')}</p>{reading && <><p role="status">{c('pngReading')}</p><Button type="button" onClick={invalidate}>{c('pngCancel')}</Button></>}{notice && <p role="status">{c(notice)}</p>}</div>
}
function ContactExchange({ snapshot, server, locale, disabled, onSelect }: { snapshot: SetupSnapshot; server: Server; locale: Locale; disabled: boolean; onSelect: (text: string) => void }) {
  const [text, setText] = useState(''), [error, setError] = useState(false), c = (key: NearbyTextKey) => nearbyText(locale, key)
  const submit = (event: FormEvent) => { event.preventDefault(); const input = text.trim(), contact = readSetupContact(input); if (!contact || contact.sessionId === snapshot.sessionId || Date.parse(contact.expiresAt) <= Date.now() || Date.parse(contact.expiresAt) > Date.now() + 120000) { setError(true); return }; if (!disabled) onSelect(input) }
  return <details open={snapshot.discovery.state === 'unavailable'}><summary>{c('manual')}</summary><div className="form-stack"><p className="small muted">{c('manualHint')}</p><CopyText text={snapshot.contactText!} label={c('localExchange')} locale={locale} />{snapshot.contactQR && <QR value={snapshot.contactQR} label={c('localExchange')} />}<form onSubmit={submit} className="form-stack"><TypedInput kind="setup-contact" server={server} locale={locale} label={c('peerExchange')} pngLabel={c('png')} value={text} onChange={value => { setText(value); setError(false) }} disabled={disabled} />{error && <p role="alert">{c('invalidPeer')}</p>}<Button type="submit" disabled={disabled || !text}>{c('importPeer')}</Button></form></div></details>
}
function PlanReview({ snapshot, locale, server, disabled, onApprove }: { snapshot: SetupSnapshot; locale: Locale; server: Server; disabled: boolean; onApprove: () => void }) {
  const plan = snapshot.plan as SetupPlan, [text, setText] = useState(''), [verified, setVerified] = useState(false), [saveConsent, setSaveConsent] = useState(false), c = (key: NearbyTextKey) => nearbyText(locale, key), matches = readSetupFingerprint(text.trim()) === plan.fingerprint
  return <div className="invitation-card form-stack" role="region" aria-label={c('review')}><h3>{c('review')}</h3><strong>{snapshot.selected?.alias}</strong><p>{c('verifyHint')}</p><CopyText text={plan.verificationText} label={c('fingerprint')} locale={locale} />{plan.verificationQR && <QR value={plan.verificationQR} label={c('fingerprint')} />}<TypedInput kind="setup-fingerprint" server={server} locale={locale} label={c('peerFingerprint')} pngLabel={c('fingerprintPNG')} disabled={disabled || snapshot.localApproved} value={text} onChange={value => { setText(value); setVerified(false); setSaveConsent(false) }} />{text && <p role={matches ? 'status' : 'alert'}>{c(matches ? 'matched' : 'digestMismatch')}</p>}
    <details open><summary>{c('planDetails')}</summary><dl><dt>{c('backend')}</dt><dd>{plan.backend}</dd><dt>{c('participants')}</dt>{plan.participants.map(item => <dd key={item.session_id}>{item.alias}<br /><span className="code-value">{item.key}</span><br /><span className="code-value">{item.bootstrap} · {item.scope.prefix}</span></dd>)}<dt>{c('host')}</dt><dd className="code-value">{plan.hostKey}</dd><dt>{c('relay')}</dt><dd className="code-value">{plan.relay.address}</dd><dt>{c('certificate')}</dt><dd className="code-value">{plan.relay.certificateSHA256}</dd><dt>{c('revision')}</dt><dd>{plan.revision}</dd><dt>{c('expires')}</dt><dd>{new Date(plan.expiresAt).toLocaleTimeString(locale)}</dd></dl></details>
    <div className="scope-note"><p>{c('saveImpact')}</p><p><strong>{c('savedScope')}:</strong> <span className="code-value">{snapshot.scope?.prefix}</span></p><p>{c('stopImpact')}</p></div>
    <label className="checkbox-field"><input type="checkbox" checked={saveConsent} disabled={disabled || !matches || snapshot.localApproved} onChange={event => setSaveConsent(event.target.checked)} />{c('saveConsent')}</label>
    <label className="checkbox-field"><input type="checkbox" checked={verified} disabled={disabled || !matches || snapshot.localApproved} onChange={event => setVerified(event.target.checked)} />{c('verified')}</label><Button variant="primary" disabled={disabled || !matches || !verified || !saveConsent || snapshot.localApproved} onClick={() => { if (!disabled && matches && verified && saveConsent && !snapshot.localApproved) onApprove() }}>{c('approve')}</Button><div className="small muted">{c('localApproved')}: {snapshot.localApproved ? '✓' : '—'} · {c('remoteApproved')}: {snapshot.remoteApproved ? '✓' : '—'}</div>
  </div>
}
