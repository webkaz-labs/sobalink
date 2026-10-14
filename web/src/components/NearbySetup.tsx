import { useEffect, useLayoutEffect, useImperativeHandle, useRef, useState, type FormEvent, type Ref } from 'react'
import type { CommandPayloads, Locale } from '../api'
import type { Server } from '../useServer'
import { MAX_SETUP_TEXT_BYTES, readSetupContact, readSetupFingerprint, readSetupScopes, readSetupSnapshot, setupEndpoint, setupTerminal, validSetupPort, validSetupAlias, validSetupTargetAddress, type SetupMode, type SetupVerification, type SetupCommandPayloads, type SetupPlan, type SetupScope, type SetupSnapshot } from '../nearby-setup'
import { nearbyText, nearbyReasonKey, nearbyNextAction, type NearbyTextKey } from '../nearby-i18n'
import { PngCardImporter } from '../qr/png-import'
import { pngPolicy } from '../qr/png-policy'
import { Button, useAlive } from './ui'
import { RemovedSetupDevices } from './RemovedSetupDevices'
import './NearbySetup.css'

export interface NearbySetupHandle { close: () => Promise<boolean> }
export function NearbySetup({ server, locale, disabled = false, ref }: { server: Server; locale: Locale; disabled?: boolean; ref?: Ref<NearbySetupHandle> }) {
  const c = (key: NearbyTextKey) => nearbyText(locale, key)
  const removedGuard = useRef(false), [removedBlocked, setRemovedBlocked] = useState(false)
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
  const [mode, setMode] = useState<SetupMode | 'legacy'>('request'), [verification, setVerification] = useState<SetupVerification>('tofu'), [duration, setDuration] = useState('120')
  const [allowHost, setAllowHost] = useState<boolean>(), [consent, setConsent] = useState(false)
  const [retryReview, setRetryReview] = useState<string>()
  const finishCleanupWait = (done: boolean) => { for (const complete of cleanupWaiters.current) complete(done); cleanupWaiters.current.clear() }
  const markUncertain = (key: NearbyTextKey) => { finishCleanupWait(false); uncertainRef.current = true; setUncertain(true); setError(key) }
  const accept = (next: SetupSnapshot) => { const previous = current.current; if (previous?.sessionId === next.sessionId && previous?.target && next.target && (next.target.revision < previous.target.revision || (previous.requestId || next.target.revision === previous.target.revision) && targetBinding(previous.target) !== targetBinding(next.target))) { markUncertain('invalidResponse'); return false }; if (current.current && current.current.sessionId === next.sessionId && next.sessionId && (current.current.version !== next.version || current.current.mode !== next.mode || current.current.verification !== next.verification || current.current.requestId && next.requestId && current.current.requestId !== next.requestId || current.current.request && next.request && JSON.stringify({ ...current.current.request, keyStatus: undefined }) !== JSON.stringify({ ...next.request, keyStatus: undefined }))) { markUncertain('invalidResponse'); return false }; if (['cancelled', 'expired', 'failed'].includes(next.state) && (current.current?.sessionId !== next.sessionId || current.current?.state !== next.state)) resetForm(); current.current = next; setSnapshot(next); uncertainRef.current = false; setUncertain(false); setError(undefined); if (setupTerminal(next)) { setConsent(false); const safe = !next.connected && next.state !== 'recovery_required' || next.connected && next.nextAction === 'start_new_setup' && newFormOwner.current === next.sessionId; if (cleanupWaiters.current.size && !safe) setError(next.connected ? 'alreadyConnected' : 'recovery'); finishCleanupWait(safe) }; return true }
  const unavailable = disabled || server.auth !== 'ready' || server.stale
  const scope = scopes?.find(item => item.scopeId === scopeId)
  const expired = Boolean(snapshot?.expiresAt && Date.parse(snapshot.expiresAt) <= now && snapshot.state !== 'idle')
  const active = Boolean(snapshot?.sessionId && !setupTerminal(snapshot))
  const freshForm = Boolean(snapshot?.connected && snapshot.nextAction === 'start_new_setup' && snapshot.process.state === 'ready' && newFormIntent === snapshot.sessionId && !starting.current)
  const retryable = Boolean(snapshot?.sessionId && snapshot.state === 'failed' && snapshot.nextAction === 'try_again' && snapshot.process.state === 'ready' && !snapshot.paired && !snapshot.connected)
  const retryForm = retryable && retryReview === snapshot?.sessionId
  const startable = snapshot && (freshForm || ['idle', 'cancelled', 'expired'].includes(snapshot.state) || retryForm) && snapshot.process.state === 'ready'
  const resetForm = () => { setAlias(''); setScopeId(''); setBootstrapPort('48445'); setRelayPort('48443'); setAllowHost(undefined); setMode('request'); setVerification('tofu'); setDuration('120'); setConsent(false) }
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
        if (next.sessionId && (next.scope?.scopeId !== starting.current.scopeId || !matchesStart(next, starting.current))) { markUncertain('invalidResponse'); return }
        newFormOwner.current = undefined; setNewFormIntent(undefined)
      }
      starting.current = undefined; if (!accept(next)) return; return next
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
    const warn = (event: BeforeUnloadEvent) => { if (pending.current || removedGuard.current || uncertainRef.current || (current.current && !setupTerminal(current.current))) { event.preventDefault(); event.returnValue = '' } }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [])
  async function mutate<N extends Exclude<keyof SetupCommandPayloads, 'setup.scopes' | 'setup.status' | 'setup.revoked' | 'setup.reenroll'>>(name: N, payload: CommandPayloads[N]): Promise<SetupSnapshot | undefined> {
    if (pending.current || removedGuard.current || unavailable || uncertainRef.current) return
    pending.current = true; setBusy(true); setError(undefined); const sequence = ++generation.current
    const expectedSession = name === 'setup.start' ? undefined : current.current?.sessionId, reviewedTarget = current.current?.target
    if (name === 'setup.start') { starting.current = { ...payload as SetupCommandPayloads['setup.start'], previousSessionId: current.current?.sessionId }; setConsent(false) }
    try {
      const result = await latestServer.current.run(name, payload, 'setup.mutation')
      if (!alive.current || sequence !== generation.current) return
      const next = result && readSetupSnapshot(result.result, expectedSession)
      if (!next) { markUncertain(result ? 'invalidResponse' : 'uncertain'); return }
      if (name === 'setup.start' && next.sessionId) {
        const input = payload as SetupCommandPayloads['setup.start']
        if (next.sessionId === starting.current?.previousSessionId || next.scope?.scopeId !== input.scopeId || !matchesStart(next, input)) { markUncertain('invalidResponse'); return }
      }
      if (name === 'setup.inspect-target' && next.target) {
        const input = payload as SetupCommandPayloads['setup.inspect-target'], candidate = current.current?.candidates.find(item => item.candidateId === input.candidateId)
        if (reviewedTarget && next.target.revision <= reviewedTarget.revision || input.address && next.target.address !== input.address || input.candidateId && (!candidate || next.target.sessionId !== candidate.contact.sessionId || next.target.address !== candidate.contact.bootstrap || next.target.alias !== candidate.contact.alias)) { markUncertain('invalidResponse'); return }
      }
      if (name === 'setup.manual' && next.selected) {
        const input = payload as SetupCommandPayloads['setup.manual']
        if (JSON.stringify(next.selected) !== JSON.stringify(readSetupContact(input.contactText))) { markUncertain('invalidResponse'); return }
      }
      if (name === 'setup.approve' && next.localApproved) {
        const input = payload as SetupCommandPayloads['setup.approve']
        if (next.plan?.revision !== input.revision || next.plan.fingerprint !== input.fingerprint || next.requestId !== input.requestId) { markUncertain('invalidResponse'); return }
      }
      if (name === 'setup.request') { const input = payload as SetupCommandPayloads['setup.request']; if (next.target?.sessionId !== input.targetSessionId || next.target.revision !== input.targetRevision || next.target.peerKey !== input.expectedPeerKey || !reviewedTarget || targetBinding(next.target) !== targetBinding(reviewedTarget)) { markUncertain('invalidResponse'); return } }
      if (name === 'setup.start') { newFormOwner.current = undefined; setNewFormIntent(undefined) }
      starting.current = undefined; if (!accept(next)) return; return next
    } catch { if (alive.current) markUncertain('uncertain'); return }
    finally { pending.current = false; if (alive.current) setBusy(false) }
  }
  const cancel = async (): Promise<boolean> => {
    if (pending.current || removedGuard.current || unavailable) { setError('closePending'); return false }
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
    if (pending.current || removedGuard.current || unavailable || uncertainRef.current) return
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
    const hosting = mode === 'legacy' ? allowHost : mode === 'receive'
    const port = mode === 'outbound' ? 0 : Number(bootstrapPort)
    if (!startable || hosting === undefined || !consent || !scope || !validSetupAlias(alias) || (mode !== 'outbound' && !validSetupPort(port)) || (hosting && (!validSetupPort(Number(relayPort)) || port === Number(relayPort))) || mode !== 'legacy' && (!Number.isInteger(Number(duration)) || Number(duration) < 120 || Number(duration) > 1200)) { setError('invalidInput'); return }
    void mutate('setup.start', { alias, scopeId: scope.scopeId, bootstrapPort: port, allowHost: hosting, relayPort: hosting ? Number(relayPort) : 0, consent: true, ...(mode === 'legacy' ? {} : { mode, verification, expiresInSeconds: Number(duration) }) })
  }

  const blocked = unavailable || busy || removedBlocked || uncertain || snapshot?.state === 'cancelling'
  const errorCode = server.error && typeof server.error === 'object' && 'code' in server.error ? server.error.code : undefined
  const reasonKey = nearbyReasonKey(snapshot?.reasonCode || errorCode)
  return <section className="nearby-setup form-stack" aria-label={c('title')}>
    <h3>{c('title')}</h3><p className="muted">{c(snapshot?.version === 1 && active || startable && mode === 'legacy' ? 'intro' : 'v2Intro')}</p>
    {loading && <p role="status">{c('loading')}</p>}{error && <p role="alert" className="scope-note">{c(error)}</p>}{reasonKey && reasonKey !== error && <p className="scope-note">{c(reasonKey)}</p>}
    {(uncertain || removedBlocked || !snapshot) && !loading && !active && <Button disabled={unavailable || busy} onClick={() => void status()}>{c('checkStatus')}</Button>}
    {snapshot?.process.state === 'stop_required' || snapshot?.state === 'stop_required' ? <p className="scope-note" role="status">{c('stopRequired')}</p> : snapshot?.process.state === 'needs_restart' ? <p className="scope-note" role="status">{c('needsRestart')}</p> : null}
    {snapshot?.state === 'cancelling' && <p role="status">{c('cancelling')}</p>}
    {snapshot?.state === 'cancelled' && <p role="status">{c('cancelled')}</p>}{(snapshot?.state === 'expired' || expired && active) && <p role="status">{c('expired')}</p>}
    {newFormIntent && <p className="scope-note" role="status">{c(starting.current ? 'newStartPending' : freshForm ? 'freshForm' : 'finishPrevious')}</p>}
    {snapshot?.connected && !freshForm && !starting.current && <Button variant="primary" disabled={blocked || snapshot.process.state !== 'ready'} onClick={() => void connectAnother()}>{c(newFormIntent ? 'finishPreviousAction' : 'connectAnother')}</Button>}
    {retryable && !retryForm && <Button disabled={blocked} onClick={() => { resetForm(); setRetryReview(snapshot!.sessionId) }}>{c(snapshot?.reasonCode === 'setup_no_permitted_host' ? 'reviewHost' : 'startAgain')}</Button>}
    {retryForm && <p className="scope-note" role="status">{c('retryReview')}</p>}
    {startable && <form onSubmit={start} className="form-stack">
      <fieldset className="nearby-host-choice"><legend>{c('chooseMode')}</legend>{(['request', 'receive'] as const).map(value => <label key={value} className="checkbox-field"><input type="radio" name="nearby-mode" checked={mode === value} disabled={blocked} onChange={() => { setMode(value); setConsent(false) }} />{c(value === 'request' ? 'requestMode' : 'receiveMode')}</label>)}
        <details><summary>{c('moreOptions')}</summary>{(['outbound', 'legacy'] as const).map(value => <label key={value} className="checkbox-field"><input type="radio" name="nearby-mode" checked={mode === value} disabled={blocked} onChange={() => { setMode(value); setConsent(false) }} />{c(value === 'outbound' ? 'outboundMode' : 'legacyMode')}</label>)}</details>
      </fieldset>
      {mode === 'outbound' && <p className="scope-note">{c('outboundHint')}</p>}
      {mode !== 'legacy' && <><label className="field">{c('duration')}<input type="number" min={120} max={1200} step={1} value={duration} disabled={blocked} onChange={event => { setDuration(event.target.value); setConsent(false) }} /></label><p className="small muted">{c('durationHint')}</p>
        <details><summary>{c('verificationChoice')}</summary><label className="field">{c('verificationChoice')}<select value={verification} disabled={blocked} onChange={event => { setVerification(event.target.value as SetupVerification); setConsent(false) }}><option value="tofu">{c('tofuMode')}</option><option value="full-sha256">{c('fullMode')}</option></select></label><p>{c('tofuLimit')}</p></details></>}

      <label className="field">{c('alias')}<input value={alias} maxLength={80} disabled={blocked} onChange={event => { setAlias(event.target.value); setConsent(false) }} autoComplete="off" required /><small className="muted">{c('aliasHint')}</small></label>
      <label className="field">{c('scope')}<select value={scopeId} disabled={blocked} onChange={event => { setScopeId(event.target.value); setConsent(false) }} required><option value="">{c('chooseScope')}</option>{scopes?.map(item => <option key={item.scopeId} value={item.scopeId}>{item.localAddress} · {item.prefix}</option>)}</select></label>
      {scopes?.length === 0 && <p>{c('noScopes')}</p>}<Button type="button" disabled={blocked} onClick={() => { setConsent(false); void loadScopes() }}>{c('refreshScopes')}</Button>{scope && <ScopeDetails scope={scope} locale={locale} />}
      {mode !== 'outbound' && <details><summary>{c('ports')}</summary><label className="field">{c('bootstrapPort')}<input type="number" min={1024} max={65535} value={bootstrapPort} disabled={blocked} onChange={event => { setBootstrapPort(event.target.value); setConsent(false) }} /></label><label className="field">{c('relayPort')}<input type="number" min={1024} max={65535} value={relayPort} disabled={blocked} onChange={event => { setRelayPort(event.target.value); setConsent(false) }} /></label></details>}
      {scope && mode !== 'outbound' && <p className="code-value">{c('bootstrapPort')}: {setupEndpoint(scope.localAddress, Number(bootstrapPort))}</p>}
      {mode === 'legacy' && <fieldset className="nearby-host-choice"><legend>{c('hostChoice')}</legend><p className="small muted">{c('hostHint')}</p>
        <label className="checkbox-field"><input type="radio" name="nearby-host-role" checked={allowHost === true} disabled={blocked} onChange={() => { setAllowHost(true); setConsent(false) }} />{c('allowHost')}</label>
        <label className="checkbox-field"><input type="radio" name="nearby-host-role" checked={allowHost === false} disabled={blocked} onChange={() => { setAllowHost(false); setConsent(false) }} />{c('otherHost')}</label>
        {allowHost === false && <p className="scope-note">{c('otherHostHint')}</p>}{allowHost === true && scope && <p className="code-value">{c('relay')}: {setupEndpoint(scope.localAddress, Number(relayPort))}</p>}
      </fieldset>}
      {mode === 'receive' && scope && <p className="code-value">{c('relay')}: {setupEndpoint(scope.localAddress, Number(relayPort))}</p>}
      <label className="checkbox-field"><input type="checkbox" checked={consent} disabled={blocked || !scope || mode === 'legacy' && allowHost === undefined} onChange={event => setConsent(event.target.checked)} />{c(mode === 'legacy' ? 'consent' : 'v2Consent')}</label><p className="small muted">{c(mode === 'legacy' ? 'consentHint' : mode === 'outbound' ? 'outboundHint' : 'v2ConsentHint')}</p><p className="small muted">{c('stopImpact')}</p><Button type="submit" variant="primary" disabled={blocked || !consent || !scope || !alias || mode === 'legacy' && allowHost === undefined} busy={busy}>{c(mode === 'receive' ? 'startReceive' : mode === 'outbound' ? 'startOutbound' : 'start')}</Button>
    </form>}
    {snapshot?.scope && !startable && <ScopeDetails scope={snapshot.scope} locale={locale} />}
    {!freshForm && snapshot?.sessionId && snapshot.expiresAt && <p className="small muted">{c('expires')}: <time dateTime={snapshot.expiresAt}>{new Date(snapshot.expiresAt).toLocaleTimeString(locale)}</time>{active && <> · {c('countdown')}: {Math.max(0, Math.ceil((Date.parse(snapshot.expiresAt) - now) / 1000))}</>}</p>}
    {active && snapshot?.version === 1 && snapshot?.state !== 'cancelling' && !snapshot?.plan && <>
      <p role="status">{c(snapshot?.selected ? 'selectedWaiting' : snapshot?.state === 'discovering' ? 'searching' : 'preparing')}</p><p className="scope-note">{c('unverified')}</p>{snapshot?.discovery.state === 'unavailable' && <p className="scope-note">{c('discoveryUnavailable')}</p>}{!snapshot?.candidates.some(item => Date.parse(item.expiresAt) > now && Date.parse(item.contact.expiresAt) > now) && <p>{c('noCandidates')}</p>}
      <ul className="nearby-candidates">{snapshot?.candidates.filter(item => Date.parse(item.expiresAt) > now && Date.parse(item.contact.expiresAt) > now).map(item => <li key={item.candidateId}><strong>{item.contact.alias}</strong><span className="code-value">{item.contact.bootstrap}</span><Button disabled={blocked || expired || Boolean(snapshot.selected)} onClick={() => { if (Date.parse(item.expiresAt) > Date.now() && Date.parse(item.contact.expiresAt) > Date.now()) void mutate('setup.select', { sessionId: snapshot.sessionId!, candidateId: item.candidateId }) }}>{c('select')}</Button></li>)}</ul>
      {snapshot?.contactText && <ContactExchange key={JSON.stringify([snapshot.sessionId, unavailable, uncertain])} snapshot={snapshot} server={server} locale={locale} disabled={blocked || expired || Boolean(snapshot.selected)} onSelect={text => void mutate('setup.manual', { sessionId: snapshot.sessionId!, contactText: text })} />}
    </>}
    {active && snapshot?.version === 2 && !newFormIntent && <RequestFlow snapshot={snapshot} locale={locale} now={now} disabled={blocked || expired} onInspect={target => void mutate('setup.inspect-target', { sessionId: snapshot.sessionId!, ...target })} onRequest={() => { const target = snapshot.target; if (!target || !current.current || current.current.sessionId !== snapshot.sessionId || JSON.stringify(current.current.target) !== JSON.stringify(target) || current.current.requestId || Date.parse(target.expiresAt) <= Date.now()) return; void mutate('setup.request', { sessionId: snapshot.sessionId!, targetSessionId: target.sessionId, targetRevision: target.revision, expectedPeerKey: target.peerKey, tofuAcknowledged: snapshot.verification === 'tofu' }) }} onReject={() => { if (!current.current || current.current.requestId !== snapshot.requestId || !snapshot.requestId || current.current.localApproved) return; void mutate('setup.reject', { sessionId: snapshot.sessionId!, requestId: snapshot.requestId }) }} />}
    {!newFormIntent && !retryForm && snapshot?.plan && <PlanReview key={JSON.stringify([snapshot.sessionId, snapshot.requestId])} snapshot={snapshot} locale={locale} server={server} disabled={blocked || expired || snapshot.request?.keyStatus === 'revoked' || snapshot.process.state !== 'ready' || !['awaiting_approval', 'request_received'].includes(snapshot.state)} onApprove={() => { if (!current.current || current.current.sessionId !== snapshot.sessionId || current.current.requestId !== snapshot.requestId || JSON.stringify(current.current.plan) !== JSON.stringify(snapshot.plan) || current.current.localApproved || Date.parse(snapshot.plan!.expiresAt) <= Date.now()) return; void mutate('setup.approve', { sessionId: snapshot.sessionId!, revision: snapshot.plan!.revision, fingerprint: snapshot.plan!.fingerprint, verified: snapshot.plan!.verification === 'full-sha256', ...(snapshot.version === 2 ? { requestId: snapshot.requestId, tofuAcknowledged: snapshot.verification === 'tofu' } : {}) }) }} />}
    {snapshot?.connected ? !newFormIntent && <p role="status" className="scope-note">{c('connected')}</p> : snapshot?.paired ? <p role="status">{c('paired')}</p> : snapshot?.plan && !retryForm && <p role="status">{c(snapshot.state === 'awaiting_approval' ? 'awaiting' : snapshot.state === 'recovery_required' ? 'recovery' : snapshot.state === 'failed' ? 'failed' : snapshot.state === 'cancelling' ? 'cancelling' : snapshot.state === 'cancelled' ? 'cancelled' : snapshot.state === 'expired' ? 'expired' : 'preparing')}</p>}
    {snapshot?.state === 'recovery_required' && !snapshot.plan && <p role="alert">{c('recovery')}</p>}{snapshot?.state === 'failed' && !retryForm && !snapshot.plan && <p role="alert">{c('failed')}</p>}
    {snapshot?.connected && !newFormIntent && <p className="small muted">{c('connectedReceipt')}</p>}
    <p className="small muted">{c('trustSeparate')}</p>{snapshot && <details><summary>{c('details')}</summary><dl><dt>{c('reason')}</dt><dd>{snapshot.reasonCode || snapshot.discovery.reasonCode || '—'}</dd><dt>{c('nextAction')}</dt><dd>{nearbyNextAction(locale, snapshot.nextAction)}</dd></dl></details>}
    <RemovedSetupDevices server={server} locale={locale} disabled={unavailable || busy || uncertain || !snapshot || active || snapshot.process.state !== 'ready' || snapshot.connected} onGuardChange={value => { removedGuard.current = value; setRemovedBlocked(value); if (value) setConsent(false) }} />
    {active && <div className="modal-actions"><Button disabled={unavailable || busy} onClick={() => void status()}>{c('checkStatus')}</Button><Button variant="danger" disabled={unavailable || busy || snapshot?.state === 'cancelling'} onClick={() => void cancel()}>{c(snapshot?.mode === 'receive' ? 'receiveStop' : 'cancel')}</Button></div>}
  </section>
}
function targetBinding(target: NonNullable<SetupSnapshot['target']>) { return JSON.stringify({ ...target, keyStatus: undefined }) }
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
  // Apply binding invalidation before newly committed controls can receive input.
  useLayoutEffect(() => { invalidate(); return () => { ++sequence.current; importer.current?.dispose(); importer.current = undefined } }, [binding])
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
  const plan = snapshot.plan as SetupPlan, [text, setText] = useState(''), [verified, setVerified] = useState(false), [saveConsent, setSaveConsent] = useState(false), c = (key: NearbyTextKey) => nearbyText(locale, key), tofu = snapshot.version === 2 && plan.verification === 'tofu', matches = tofu || readSetupFingerprint(text.trim()) === plan.fingerprint
  const binding = JSON.stringify([snapshot.sessionId, snapshot.requestId, plan, disabled, server.auth, server.stale])
  useLayoutEffect(() => { setText(''); setVerified(false); setSaveConsent(false) }, [binding])
  return <div className="invitation-card form-stack" role="region" aria-label={c(tofu ? 'v2Review' : 'review')}><h3>{c(tofu ? 'v2Review' : 'review')}</h3><strong>{snapshot.selected?.alias}</strong>{tofu ? <p className="scope-note">{c('tofuLimit')}</p> : <><p>{c('verifyHint')}</p><CopyText text={plan.verificationText} label={c('fingerprint')} locale={locale} />{plan.verificationQR && <QR value={plan.verificationQR} label={c('fingerprint')} />}<TypedInput kind="setup-fingerprint" server={server} locale={locale} label={c('peerFingerprint')} pngLabel={c('fingerprintPNG')} disabled={disabled || snapshot.localApproved} value={text} onChange={value => { setText(value); setVerified(false); setSaveConsent(false) }} />{text && <p role={matches ? 'status' : 'alert'}>{c(matches ? 'matched' : 'digestMismatch')}</p>}</>}
    <details open><summary>{c('planDetails')}</summary><dl><dt>{c('backend')}</dt><dd>{plan.backend}</dd><dt>{c('participants')}</dt>{plan.participants.map(item => <dd key={item.session_id}>{item.alias}<br /><span className="code-value">{item.key}</span><br /><span className="code-value">{item.bootstrap} · {item.scope.prefix}</span></dd>)}<dt>{c('host')}</dt><dd className="code-value">{plan.hostKey}</dd><dt>{c('relay')}</dt><dd className="code-value">{plan.relay.address}</dd><dt>{c('certificate')}</dt><dd className="code-value">{plan.relay.certificateSHA256}</dd><dt>{c('revision')}</dt><dd>{plan.revision}</dd><dt>{c('expires')}</dt><dd>{new Date(plan.expiresAt).toLocaleTimeString(locale)}</dd></dl></details>
    <div className="scope-note"><p>{c(snapshot.version === 2 ? 'v2SaveImpact' : 'saveImpact')}</p><p><strong>{c('savedScope')}:</strong> <span className="code-value">{snapshot.scope?.prefix}</span></p><p>{c('stopImpact')}</p></div>
    <label className="checkbox-field"><input type="checkbox" checked={saveConsent} disabled={disabled || !matches || snapshot.localApproved} onChange={event => setSaveConsent(event.target.checked)} />{c('saveConsent')}</label>
    <label className="checkbox-field"><input type="checkbox" checked={verified} disabled={disabled || !matches || snapshot.localApproved} onChange={event => setVerified(event.target.checked)} />{c(tofu ? 'tofuAcknowledge' : 'verified')}</label><Button variant="primary" disabled={disabled || !matches || !verified || !saveConsent || snapshot.localApproved} onClick={() => { if (!disabled && matches && verified && saveConsent && !snapshot.localApproved) onApprove() }}>{c('approve')}</Button><div className="small muted">{c('localApproved')}: {snapshot.localApproved ? '✓' : '—'} · {c('remoteApproved')}: {snapshot.remoteApproved ? '✓' : '—'}</div>
  </div>
}

function matchesStart(snapshot: SetupSnapshot, input: SetupCommandPayloads['setup.start']): boolean {
  if (input.mode) return snapshot.version === 2 && snapshot.mode === input.mode && snapshot.verification === input.verification && snapshot.alias === input.alias && (input.mode === 'outbound' ? !snapshot.contact && snapshot.discovery.state !== 'active' : snapshot.contact?.bootstrap === setupEndpoint(snapshot.scope!.localAddress, input.bootstrapPort))
  return snapshot.version === 1 && snapshot.contact?.alias === input.alias && snapshot.contact.bootstrap === setupEndpoint(snapshot.scope!.localAddress, input.bootstrapPort)
}

function RequestFlow({ snapshot, locale, now, disabled, onInspect, onRequest, onReject }: { snapshot: SetupSnapshot; locale: Locale; now: number; disabled: boolean; onInspect: (target: { address?: string; candidateId?: string }) => void; onRequest: () => void; onReject: () => void }) {
  const c = (key: NearbyTextKey) => nearbyText(locale, key), [address, setAddress] = useState(''), [invalid, setInvalid] = useState(false), [acknowledged, setAcknowledged] = useState(false), [save, setSave] = useState(false)
  const target = snapshot.target, request = snapshot.request, receiving = snapshot.mode === 'receive', waiting = Boolean(snapshot.requestId)
  const binding = JSON.stringify([snapshot.sessionId, target, disabled])
  useLayoutEffect(() => { setAcknowledged(false); setSave(false) }, [binding])
  const effectiveExpiry = Math.min(Date.parse(snapshot.expiresAt!), Date.parse(target?.expiresAt || request?.expiresAt || snapshot.expiresAt!), Date.parse(snapshot.plan?.expiresAt || snapshot.expiresAt!))
  const locked = disabled || waiting || snapshot.state === 'rejected', usable = snapshot.candidates.filter(item => Date.parse(item.expiresAt) > now && Date.parse(item.contact.expiresAt) > now)
  const submit = (event: FormEvent) => { event.preventDefault(); if (!validSetupTargetAddress(address)) { setInvalid(true); return }; if (!locked) onInspect({ address }) }
  return <div className="form-stack nearby-request-flow">
    {effectiveExpiry - now < 30000 && effectiveExpiry > now && <p role="status">{c('expiringSoon')}</p>}
    {snapshot.state === 'rejected' && <p role="status">{c('requestRejected')}</p>}
    {receiving ? <>
      {!request && <p role="status">{c('receiving')}</p>}
      {snapshot.contact && <><CopyText text={snapshot.contact.bootstrap} label={c('receiveAddress')} locale={locale} /><p className="small muted">{c('addressHint')}</p></>}
      {request && <section aria-label={c('incomingRequest')} className="invitation-card form-stack">
        <h3>{c('incomingRequest')}</h3><strong>{request.alias}</strong><p>{c(request.keyStatus === 'saved' ? 'keySaved' : request.keyStatus === 'revoked' ? 'keyRevoked' : 'keyNew')}</p>
        {request.keyStatus === 'saved' && <p>{c('keySavedHint')}</p>}
        <dl><dt>{c('peerKey')}</dt><dd className="code-value">{request.peerKey}</dd><dt>{c('requestId')}</dt><dd className="code-value">{request.requestId}</dd><dt>{c('peerAddress')}</dt><dd className="code-value">{request.address}</dd><dt>{c('effectiveExpiry')}</dt><dd><time dateTime={new Date(effectiveExpiry).toISOString()}>{new Date(effectiveExpiry).toLocaleTimeString(locale)}</time></dd></dl>
        <Button variant="danger" disabled={disabled || snapshot.localApproved || !['authenticating', 'request_received', 'awaiting_approval'].includes(snapshot.state)} onClick={onReject}>{c('rejectRequest')}</Button>
      </section>}
    </> : <>
      {waiting ? <p role="status">{c('requesting')}</p> : <>
        {snapshot.mode === 'outbound' ? <p className="scope-note">{c('outboundHint')}</p> : <><p role="status">{c(snapshot.discovery.state === 'active' ? 'searching' : snapshot.discovery.state === 'stopped' ? 'discoveryStopped' : 'discoveryUnavailableV2')}</p><p>{c('unverified')}</p>{!usable.length && <p>{c('v2NoCandidates')}</p>}</>}
        <ul className="nearby-candidates">{usable.map(item => <li key={item.candidateId}><strong>{item.contact.alias}</strong><span className="code-value">{item.contact.bootstrap}</span><span className="small muted">{c('keyPending')}</span><details><summary>{c('details')}</summary><span className="code-value">{item.contact.sessionId}</span></details><Button disabled={locked} onClick={() => { if (Date.parse(item.expiresAt) > Date.now() && Date.parse(item.contact.expiresAt) > Date.now()) onInspect({ candidateId: item.candidateId }) }}>{c('select')}</Button></li>)}</ul>
        <details open={snapshot.mode === 'outbound' || snapshot.discovery.state !== 'active' || !usable.length}><summary>{c('addressEntry')}</summary><form onSubmit={submit} className="form-stack"><label className="field">{c('peerAddress')}<input value={address} maxLength={54} disabled={locked} autoComplete="off" autoCapitalize="none" spellCheck={false} onChange={event => { setAddress(event.target.value); setInvalid(false) }} /></label><p className="small muted">{c('addressHint')}</p>{invalid && <p role="alert">{c('invalidAddress')}</p>}<Button type="submit" disabled={locked || !address}>{c('reviewAddress')}</Button></form></details>
      </>}
      {target && !waiting && <section className="invitation-card form-stack" aria-label={c('requestReview')}><h3>{c('requestReview')}</h3><strong>{target.alias}</strong><p>{c(target.keyStatus === 'saved' ? 'keySaved' : target.keyStatus === 'revoked' ? 'keyRevoked' : 'keyNew')}</p>
        {target.keyStatus === 'saved' && <p>{c('keySavedHint')}</p>}
        <dl><dt>{c('peerKey')}</dt><dd className="code-value">{target.peerKey}</dd><dt>{c('revision')}</dt><dd>{target.revision}</dd><dt>{c('sessionId')}</dt><dd className="code-value">{target.sessionId}</dd><dt>{c('peerAddress')}</dt><dd className="code-value">{target.address}</dd><dt>{c('savedScope')}</dt><dd className="code-value">{snapshot.scope?.prefix}</dd><dt>{c('peerScope')}</dt><dd className="code-value">{target.scopePrefix}</dd><dt>{c('relay')}</dt><dd className="code-value">{target.relayAddress}</dd><dt>{c('effectiveExpiry')}</dt><dd><time dateTime={new Date(effectiveExpiry).toISOString()}>{new Date(effectiveExpiry).toLocaleTimeString(locale)}</time></dd></dl>
        <details><summary>{c('certificate')}</summary><p className="code-value">{target.relayCertificateSHA256}</p></details><p>{c('v2SaveImpact')}</p><p className="small muted">{c('stopImpact')}</p>
        {snapshot.verification === 'tofu' ? <><p className="scope-note">{c('tofuLimit')}</p><label className="checkbox-field"><input type="checkbox" checked={acknowledged} disabled={locked} onChange={event => setAcknowledged(event.target.checked)} />{c('tofuAcknowledge')}</label></> : <p>{c('verifyHint')}</p>}
        <label className="checkbox-field"><input type="checkbox" checked={save} disabled={locked} onChange={event => setSave(event.target.checked)} />{c('saveConsent')}</label>
        <Button variant="primary" disabled={locked || target.keyStatus === 'revoked' || !save || snapshot.verification === 'tofu' && !acknowledged || effectiveExpiry <= now} onClick={() => { if (!locked && save && (snapshot.verification !== 'tofu' || acknowledged) && target.keyStatus !== 'revoked' && effectiveExpiry > Date.now()) onRequest() }}>{c('requestConnection')}</Button>
      </section>}
    </>}
  </div>
}
