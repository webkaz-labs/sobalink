import { useEffect, useRef, useState } from 'react'
import type { Locale, State } from '../api'
import type { Server } from '../useServer'
import { upgradeText } from '../upgrade-i18n'
import { upgradeDeadline, validUpgradeDeadline, validUpgradeReview, validUpgradeStatus, upgradeRunning, type UpgradeReview, type UpgradeStatus } from '../direct-lan-upgrade'
import { Button, useAlive } from './ui'

export function DirectLanUpgrade({ server, state, locale, blocked }: { server: Server; state: State; locale: Locale; blocked: boolean }) {
  const u = (key: Parameters<typeof upgradeText>[1]) => upgradeText(locale, key)
  const [peerId, setPeerId] = useState('')
  const [review, setReview] = useState<UpgradeReview>()
  const [status, setStatus] = useState<UpgradeStatus>()
  const [message, setMessage] = useState<'invalid' | 'expired' | 'unknown'>()
  const [pending, setPending] = useState(false)
  const [now, setNow] = useState(Date.now)
  const alive = useAlive(), writing = useRef(false), checking = useRef(false), cancelling = useRef(false)
  const epoch = useRef(0)
  const peers = state.directLAN?.peers?.map(peer => ({ id: peer.key, name: peer.name || peer.key, endpoint: peer.endpoint })) ?? state.peers.filter(peer => peer.networks.includes('direct-lan')).map(peer => ({ id: peer.id, name: peer.name, endpoint: peer.address }))
  const binding = JSON.stringify([state.settings?.network, state.directLAN?.publicKey, state.directLAN?.endpoint, state.directLAN?.prefixes, peers.map(peer => [peer.id, peer.endpoint])])
  useEffect(() => { ++epoch.current; setReview(undefined) }, [binding])
  useEffect(() => {
    if (!review) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [review])
  const unavailable = blocked || server.stale || server.auth !== 'ready'
  const running = pending || upgradeRunning(status?.state)
  const readStatus = async () => {
    if (checking.current || unavailable) return
    checking.current = true
    try {
      const result = await server.run('direct-lan.upgrade.status', {})
      if (!alive.current) return
      if (!result) { setMessage('unknown'); return }
      if (!validUpgradeStatus(result.result)) { setMessage('invalid'); return }
      setStatus(result.result); setMessage(undefined)
    } finally { checking.current = false }
  }
  useEffect(() => {
    if (unavailable || message || !upgradeRunning(status?.state)) return
    const timer = setInterval(() => { void readStatus() }, 1500)
    return () => clearInterval(timer)
  }, [unavailable, status?.state, message, server.run])
  const inspect = async () => {
    if (writing.current || unavailable || running || !peers.some(peer => peer.id === peerId)) return
    writing.current = true; setPending(true); setReview(undefined); setMessage(undefined)
    const generation = epoch.current, deadline = upgradeDeadline()
    try {
      const result = await server.run('direct-lan.upgrade.review', { peerId, deadline })
      if (!alive.current || generation !== epoch.current) return
      if (!result) { setMessage('unknown'); return }
      if (!validUpgradeReview(result.result, peerId, deadline)) { setMessage('invalid'); return }
      setReview(result.result); setNow(Date.now())
    } finally { writing.current = false; if (alive.current) setPending(false) }
  }
  const apply = async () => {
    if (writing.current || unavailable || !review || review.restartRequired) return
    if (review.peerId !== peerId || !validUpgradeDeadline(review.deadline)) { setReview(undefined); setMessage('expired'); return }
    writing.current = true; setPending(true); setMessage(undefined)
    const approved = review
    setReview(undefined)
    setStatus({ state: 'preparing', peerId: approved.peerId, deadline: approved.deadline })
    try {
      const result = await server.run('direct-lan.upgrade.run', { peerId: approved.peerId, deadline: approved.deadline, expectedRevision: approved.revision })
      if (!alive.current) return
      if (!result) { setMessage('unknown'); return }
      if (!validUpgradeStatus(result.result) || result.result.peerId !== approved.peerId || result.result.deadline !== approved.deadline) { setMessage('invalid'); return }
      setStatus(result.result)
    } finally { writing.current = false; if (alive.current) setPending(false) }
  }
  const cancel = async () => {
    if (cancelling.current || unavailable || !upgradeRunning(status?.state)) return
    cancelling.current = true
    try {
      const result = await server.run('direct-lan.upgrade.cancel', {})
      if (!alive.current) return
      if (!result) { setMessage('unknown'); return }
      if (!validUpgradeStatus(result.result)) { setMessage('invalid'); return }
      setStatus(result.result); setMessage(undefined)
    } finally { cancelling.current = false }
  }
  const restart = review?.restartRequired || status?.restartRequired || status?.state === 'restart-required'
  return <section className="form-stack subsection" aria-label={u('title')}>
    <h3>{u('title')}</h3><p className="small muted">{u('intro')}</p>
    <label className="field">{u('peer')}<select value={peerId} disabled={unavailable || running} onChange={event => { ++epoch.current; setPeerId(event.target.value); setReview(undefined); setMessage(undefined) }}><option value="">{u('choose')}</option>{peers.map(peer => <option value={peer.id} key={peer.id}>{peer.name} · {peer.id}</option>)}</select></label>
    {!review && <div><Button disabled={unavailable || running || !peerId} onClick={() => void inspect()}>{u('review')}</Button></div>}
    {review && <div className="invitation-card" role="region" aria-label={u('review')}><dl>
      <dt>{u('peer')}</dt><dd className="code-value">{review.peerId}</dd>
      <dt>{u('local')}</dt><dd className="code-value">{review.localEndpoint}</dd>
      <dt>{u('remote')}</dt><dd className="code-value">{review.peerEndpoint}</dd>
      <dt>{u('scope')}</dt><dd>{review.scope.family}<ul>{review.scope.prefixes.map(prefix => <li className="code-value" key={prefix}>{prefix}</li>)}</ul></dd>
      <dt>{u('deadline')}</dt><dd><time dateTime={review.deadline}>{review.deadline}</time></dd>
      <dt>{u('revision')}</dt><dd className="code-value">{review.revision}</dd>
    </dl>{review.resumePreparation && <p className="scope-note">{u('resume')}: {u('previous')} <time dateTime={review.previousDeadline}>{review.previousDeadline}</time> → <time dateTime={review.deadline}>{review.deadline}</time></p>}{Date.parse(review.deadline) <= now && <p role="alert">{u('expired')}</p>}
      <div className="modal-actions"><Button disabled={pending} onClick={() => setReview(undefined)}>{u('back')}</Button><Button variant="primary" disabled={unavailable || pending || review.restartRequired || Date.parse(review.deadline) <= now} onClick={() => void apply()}>{u('apply')}</Button></div>
    </div>}
    {status && <div role="status" aria-label={u('status')}><p>{u(status.state)}</p>{status.peerId && <p>{u('currentPeer')}: <span className="code-value">{status.peerId}</span></p>}{status.deadline && <p>{u('deadline')}: <time dateTime={status.deadline}>{status.deadline}</time></p>}{status.errorCode && <p>{u('code')}: <span className="code-value">{status.errorCode}</span></p>}</div>}
    {restart && <p className="scope-note" role="alert">{u('restart')}</p>}
    {message && <p role="alert">{u(message)}</p>}
    <div className="modal-actions"><Button disabled={unavailable || server.busy.has('direct-lan.upgrade.status')} onClick={() => void readStatus()}>{u('refresh')}</Button><Button variant="danger" disabled={unavailable || !upgradeRunning(status?.state) || server.busy.has('direct-lan.upgrade.cancel')} onClick={() => void cancel()}>{u('cancel')}</Button></div>
    <p className="small muted">{u('cancelHint')}</p>
  </section>
}
