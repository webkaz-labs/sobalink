import { useEffect, useRef, useState } from 'react'
import type { Locale, State } from '../api'
import type { Translate } from '../i18n'
import { lanTranslator } from '../lan-i18n'
import type { Server } from '../useServer'
import { PolicySummary } from './LanPolicy'
import { Button, useAlive } from './ui'

// Review is bound to the complete server-issued snapshot. The server also
// compares its revision atomically with configuration mutations before start.
export function SavedLanHost({ server, state, locale, t, blocked }: { server: Server; state: State; locale: Locale; t: Translate; blocked: boolean }) {
  const d = lanTranslator(locale)
  const saved = state.lan?.savedStart
  const alive = useAlive()
  const binding = JSON.stringify([saved, server.auth, server.stale, state.csrfToken, blocked])
  const current = useRef({ binding, generation: 0 })
  if (current.current.binding !== binding) current.current = { binding, generation: current.current.generation + 1 }
  const [prepared, setPrepared] = useState<number>()
  const [accepted, setAccepted] = useState(false)
  const pending = useRef(false)
  const [starting, setStarting] = useState(false)
  const review = prepared !== undefined && prepared === current.current.generation && saved
  const region = useRef<HTMLDivElement>(null)
  useEffect(() => { if (review) region.current?.focus() }, [prepared, current.current.generation])
  const start = async () => {
    if (!review || blocked || pending.current || accepted || server.busy.has('network.configure')) return
    const generation = current.current.generation
    pending.current = true; setStarting(true)
    try {
      const result = await server.run('network.configure', { mode: 'lan', expectedLANStartRevision: review.revision })
      if (result && alive.current && generation === current.current.generation) setAccepted(true)
    } finally { pending.current = false; if (alive.current) setStarting(false) }
  }
  if (!saved) return null
  return review ? <div className="invitation-card relay-review" role="region" aria-label={d('savedHostReview')} tabIndex={-1} ref={region}>
    <strong>{d('savedHostReview')}</strong>
    <dl><dt>{t('deviceName')}</dt><dd>{review.hostname}</dd><dt>{t('publicId')}</dt><dd className="code-value">{review.publicKey}</dd><dt>{d('tcpListener')}</dt><dd className="code-value">{review.relay.address}</dd><dt>{t('certificatePin')}</dt><dd className="code-value">{review.relay.certificateSHA256}</dd></dl>
    <PolicySummary policy={review.policy} locale={locale} />
    <p className="small muted">{d('savedHostImpact')}</p>
    <details className="relay-config"><summary>{d('existingScope')}</summary>
      <dl><dt>{d('savedPairs')}</dt><dd>{review.pairedDevices}</dd><dt>{d('trustedDevices')}</dt><dd>{review.trustedDevices}</dd><dt>{d('automaticReceivers')}</dt><dd>{review.automaticReceivers}</dd></dl>
      <p className="small muted">{d('existingScopeHint')}</p>
      <strong>{d('preparedRelays')}</strong>{review.preparedRelays.length ? <ul>{review.preparedRelays.map(relay => <li key={`${relay.address}:${relay.certificateSHA256}`}><span className="code-value">{relay.address}</span><br /><span className="code-value">{relay.certificateSHA256}</span></li>)}</ul> : <p>{d('noneSaved')}</p>}
      <strong>{d('wanScope')}</strong>{review.wanCandidates ? <><ul>{review.wanCandidates.stunEndpoints.map(endpoint => <li key={endpoint} className="code-value">{endpoint}</li>)}</ul><p>{d('advertiseIPv6')}: {d(review.wanCandidates.advertiseIPv6 ? 'enabled' : 'disabled')} · {d('probeBudget')}: {review.wanCandidates.probeBudget}</p></> : <p>{d('noneSaved')}</p>}
      <strong>{d('pendingStartup')}</strong>{review.pendingStartup.length ? <ul>{review.pendingStartup.map((name, index) => <li key={`${index}:${name}`}>{name}</li>)}</ul> : <p>{d('noneSaved')}</p>}
    </details>
    <div className="invitation-actions"><Button disabled={starting || accepted || server.busy.has('network.configure')} onClick={() => { setPrepared(undefined); setAccepted(false) }}>{t('cancel')}</Button><Button variant="primary" disabled={blocked || accepted} busy={starting || server.busy.has('network.configure')} onClick={start}>{d('startSavedHost')}</Button></div>
    {accepted && <p role="status" className="small muted">{t('actionAccepted')}</p>}
  </div> : <div className="form-stack"><p className="small muted">{d('savedHostHint')}</p><div><Button variant="primary" disabled={blocked || starting || server.busy.has('network.configure')} onClick={() => { setAccepted(false); setPrepared(current.current.generation) }}>{d('reviewSavedHost')}</Button></div></div>
}
