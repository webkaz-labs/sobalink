import { useEffect, useRef, useState } from 'react'
import type { Locale, State } from '../api'
import type { Translate } from '../i18n'
import type { Server } from '../useServer'
import { resumeText, type NetworkResumeReview } from '../network-resume'
import { PolicySummary } from './LanPolicy'
import { Button, useAlive } from './ui'

export function ResumeConnection({ server, state, locale, t, blocked }: { server: Server; state: State; locale: Locale; t: Translate; blocked: boolean }) {
  const d = (key: Parameters<typeof resumeText>[1]) => resumeText(locale, key)
  const alive = useAlive()
  const available = state.self.guidance?.code === 'network_stopped' && state.settings?.network && state.settings.network !== 'none'
  const binding = JSON.stringify([server.auth, server.stale, blocked, state.csrfToken, state.processId, state.self.status, state.self.guidance?.code, state.settings, state.peers.map(peer => [peer.id, peer.name, peer.trusted, peer.autosave]), state.lan?.savedStart?.revision, state.lan?.relay, state.lan?.policy, state.directLAN?.endpoint, state.directLAN?.prefixes, state.mixed?.backends, state.startup?.revision, state.savedProxies?.revision])
  const current = useRef({ binding, generation: 0 })
  if (current.current.binding !== binding) current.current = { binding, generation: current.current.generation + 1 }
  const [prepared, setPrepared] = useState<{ review: NetworkResumeReview; generation: number }>()
  const [busy, setBusy] = useState(false)
  const pending = useRef(false)
  const identity = JSON.stringify([state.csrfToken, state.processId])
  const [acceptedOwner, setAcceptedOwner] = useState<string>()
  const accepted = acceptedOwner === identity
  const [invalid, setInvalid] = useState(false)
  const region = useRef<HTMLDivElement>(null)
  const review = prepared?.generation === current.current.generation ? prepared.review : undefined
  useEffect(() => { if (review) region.current?.focus() }, [review])
  const execute = async (apply: boolean) => {
    if (blocked || pending.current || accepted || !available || (apply && !review)) return
    const generation = current.current.generation
    pending.current = true; setBusy(true); setInvalid(false)
    try {
      const result = apply ? await server.run('network.resume', { expectedRevision: review!.revision }) : await server.run('network.resume.preview', {})
      if (!alive.current || generation !== current.current.generation) return
      if (apply) {
        setPrepared(undefined)
        if (result) setAcceptedOwner(identity)
      } else if (result) {
        const next = result.result as unknown as NetworkResumeReview
        if (next && typeof next.revision === 'string' && next.revision && next.mode === state.settings?.network && typeof next.hostname === 'string' && Array.isArray(next.backends) && Array.isArray(next.automaticReceivers) && Array.isArray(next.pendingStartup)) setPrepared({ review: next, generation })
        else setInvalid(true)
      }
    } finally { pending.current = false; if (alive.current) setBusy(false) }
  }
  if (accepted) return <p role="status">{d('accepted')}</p>
  if (!available) return null
  return <section className="form-section form-stack">
    <h3>{d('title')}</h3><p className="muted">{d('hint')}</p>
    {invalid && <p role="alert">{d('failed')}</p>}
    {review ? <div className="invitation-card relay-review" role="region" aria-label={d('review')} tabIndex={-1} ref={region}>
      <dl><dt>{t('deviceName')}</dt><dd>{review.hostname}</dd><dt>{d('backends')}</dt><dd>{review.backends.map(mode => t(mode)).join(' · ')}</dd></dl>
      <p>{d('impact')}</p>{(review.startupSuppressed || review.savedProxiesSuppressed) && <p>{d('suppressed')}</p>}
      {review.externalControlTraffic && <p className="scope-note">{d('external')}</p>}
      {review.directLAN && <><dl><dt>{d('listener')}</dt><dd className="code-value">{review.directLAN.listen}</dd><dt>{d('prefixes')}</dt><dd className="code-value">{review.directLAN.prefixes.join(', ')}</dd></dl></>}
      {review.lan && <><dl><dt>{d('relay')}</dt><dd className="code-value">{review.lan.relay.address}</dd><dt>{t('certificatePin')}</dt><dd className="code-value">{review.lan.relay.certificateSHA256}</dd></dl><PolicySummary policy={review.lan.policy} locale={locale} /></>}
      <dl><dt>{d('trusted')}</dt><dd>{review.trustedDevices}</dd><dt>{d('paired')}</dt><dd>{review.pairedDevices}</dd></dl><h4>{d('receivers')}</h4>{review.automaticReceivers.length ? <ul>{review.automaticReceivers.map(receiver => <li key={receiver.peerId}>{receiver.name}<div className="code-value">{receiver.peerId}</div><div className="code-value">{receiver.directory}</div></li>)}</ul> : <p>{d('none')}</p>}
      <details><summary>{d('details')}</summary>
        {review.lan && <><h4>{d('preparedRelays')}</h4>{review.lan.preparedRelays.length ? <ul>{review.lan.preparedRelays.map((relay, i) => <li key={i}><span className="code-value">{relay.address}</span><br /><span className="code-value">{relay.certificateSHA256}</span></li>)}</ul> : <p>{d('none')}</p>}{review.lan.wanCandidates && <pre className="code-value">{JSON.stringify(review.lan.wanCandidates, null, 2)}</pre>}</>}
        <h4>{d('pending')}</h4>{review.pendingStartup.length ? <ul>{review.pendingStartup.map((name, i) => <li key={i}>{name}</li>)}</ul> : <p>{d('none')}</p>}
      </details>
      <div className="invitation-actions"><Button disabled={busy} onClick={() => setPrepared(undefined)}>{t('cancel')}</Button><Button variant="primary" disabled={blocked} busy={busy} onClick={() => void execute(true)}>{d('start')}</Button></div>
    </div> : <Button variant="primary" disabled={blocked} busy={busy} onClick={() => void execute(false)}>{d('review')}</Button>}
  </section>
}
