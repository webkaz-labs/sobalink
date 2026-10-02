import { useEffect, useRef, useState, type Dispatch, type FormEvent, type SetStateAction } from 'react'
import { messageByteLength, type Locale, type State } from '../api'
import { timestamp, type Translate } from '../i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Icon, useAlive } from './ui'

export interface ActiveInvitation {
  value: string
  expires: string
  recipientPublicKey: string
  recipientName: string
  relayAddress: string
  certificateSHA256: string
  consumed?: boolean
}
export interface LanDraft {
  publicKey?: string
  recipientPublicKey: string
  recipientName: string
  joinInvitation: string
  activeInvitation?: ActiveInvitation
  joinedPeerId?: string
}
export function emptyLanDraft(): LanDraft { return { recipientPublicKey: '', recipientName: '', joinInvitation: '' } }
export function isPublicKey(value: string) { return /^[a-fA-F0-9]{64}$/.test(value) }
export function isRelayAddress(value: string) {
  const match = /^(?:\[([a-fA-F0-9:.]+)\]|([0-9.]+)):(\d{1,5})$/.exec(value)
  if (!match || Number(match[3]) < 1 || Number(match[3]) > 65535) return false
  if (match[2]) {
    const parts = match[2].split('.')
    return parts.length === 4 && parts.every(part => /^(0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255)
  }
  try { return new URL(`https://${value}`).hostname.startsWith('[') } catch { return false }
}
function CopyValue({ value, label, t, secret = false }: { value: string; label: string; t: Translate; secret?: boolean }) {
  const [copied, setCopied] = useState(false)
  const [manual, setManual] = useState(false)
  const field = useRef<HTMLTextAreaElement>(null)
  const alive = useAlive()
  useEffect(() => { setCopied(false); setManual(false) }, [value])
  useEffect(() => { if (manual) { field.current?.focus(); field.current?.select() } }, [manual])
  const copy = async () => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error('unavailable')
      await navigator.clipboard.writeText(value)
      if (alive.current) { setCopied(true); setManual(false) }
    } catch { if (alive.current) setManual(true) }
  }
  return <div className="copy-value"><Button type="button" onClick={copy}><Icon name={copied ? 'check' : 'copy'} size={15} />{copied ? t('copied') : label}</Button>{manual && <><p className="small muted" role="status">{t('copyFailed')}</p><textarea ref={field} className={`code-value ${secret ? 'private-copy' : ''}`} aria-label={label} value={value} readOnly rows={secret ? 4 : 2} autoComplete="off" spellCheck={false} /></>}</div>
}
function RelaySummary({ state, t }: { state: State; t: Translate }) {
  const relay = state.lan?.relay
  if (!relay) return null
  return <div className="relay-summary"><div className="section-label"><Icon name="shield" />{t(relay.kind === 'host' ? 'relayHostedHere' : 'trustedRelay')}</div><p className="code-value">{relay.address}</p>{relay.certificateSHA256 && <p className="code-value fingerprint-value">{relay.certificateSHA256}</p>}<div className="flex flex-wrap gap-2"><Badge>{t(state.lan?.pairingReady ? 'networkReady' : 'relayNotReady')}</Badge><Badge>{t(state.lan?.path || 'unknown')}</Badge></div></div>
}

export function LanSetup({ server, state, t, locale, hostname, setHostname, draft, setDraft, onViewPeer }: { server: Server; state: State; t: Translate; locale: Locale; hostname: string; setHostname: (value: string) => void; draft: LanDraft; setDraft: Dispatch<SetStateAction<LanDraft>>; onViewPeer: (id: string) => void }) {
  const [address, setAddress] = useState(state.lan?.relay?.address || '')
  const [pin, setPin] = useState(state.lan?.relay?.certificateSHA256 || '')
  const [section, setSection] = useState<'invite' | 'join'>(draft.joinInvitation ? 'join' : 'invite')
  const [validation, setValidation] = useState('')
  const [now, setNow] = useState(Date.now)
  const alive = useAlive()
  const publicKey = state.lan?.publicKey || draft.publicKey
  const active = draft.activeInvitation
  const pairedInvitePeer = active && state.peers.find(peer => peer.networks.includes('lan') && peer.id === active.recipientPublicKey)
  const consumed = Boolean(active?.consumed || pairedInvitePeer)
  useEffect(() => {
    if (pairedInvitePeer && active && !active.consumed) setDraft(current => current.activeInvitation === active ? { ...current, activeInvitation: { ...active, value: '', consumed: true } } : current)
  }, [pairedInvitePeer, active, setDraft])
  const expired = Boolean(active && Date.parse(active.expires) <= now)
  const hasActiveInvite = Boolean(active && !expired && !consumed)
  const ready = state.settings?.network === 'lan' && Boolean(state.lan?.configured && state.lan.pairingReady) && !server.stale
  useEffect(() => {
    if (!active) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [active])
  const createIdentity = async () => {
    const response = await server.run('lan.identity', {})
    const key = response?.result?.publicKey
    if (typeof key === 'string' && isPublicKey(key)) setDraft(current => ({ ...current, publicKey: key }))
    else if (response) server.setError({ code: 'invalid_response' })
  }
  const configure = async (event: FormEvent) => {
    event.preventDefault(); setValidation('')
    if (!isRelayAddress(address.trim())) { setValidation(t('relayInvalid')); return }
    if (!isPublicKey(pin.trim())) { setValidation(t('pinInvalid')); return }
    await server.run('network.configure', { mode: 'lan', hostname: hostname.trim(), lan: { kind: 'relay', address: address.trim(), certificateSHA256: pin.trim().toLowerCase() } })
  }
  const invite = async (event: FormEvent) => {
    event.preventDefault(); setValidation('')
    if (!ready || hasActiveInvite) return
    const recipientPublicKey = draft.recipientPublicKey.trim().toLowerCase()
    if (!isPublicKey(recipientPublicKey)) { setValidation(t('publicIdInvalid')); return }
    const recipientName = draft.recipientName.trim()
    const relay = state.lan?.relay
    if (!recipientName || !relay?.address) return
    if (messageByteLength(recipientName) > 80) { setValidation(t('recipientNameTooLong')); return }
    const response = await server.run('lan.invite', { recipientPublicKey, name: recipientName, ttlSeconds: 300 })
    const result = response?.result
    if (!result) return
    if (typeof result.invitation !== 'string' || typeof result.expires !== 'string' || !Number.isFinite(Date.parse(result.expires)) || result.recipientPublicKey !== recipientPublicKey) { server.setError({ code: 'invalid_response' }); return }
    // Keep the acknowledged capability in parent memory so closing/reopening
    // setup still allows cancellation. It never enters ordinary status or storage.
    const invitation: ActiveInvitation = { value: result.invitation, expires: result.expires, recipientPublicKey, recipientName, relayAddress: relay.address, certificateSHA256: relay.certificateSHA256 || '' }
    setDraft(current => ({ ...current, activeInvitation: invitation, joinedPeerId: undefined }))
  }
  const cancel = async () => {
    if (!active || consumed) return
    if (await server.run('lan.cancel', { invitation: active.value })) setDraft(current => current.activeInvitation?.value === active.value ? { ...current, activeInvitation: undefined } : current)
  }
  const join = async (event: FormEvent) => {
    event.preventDefault(); setValidation('')
    if (!ready || hasActiveInvite) return
    const invitation = draft.joinInvitation.trim()
    if (!invitation) return
    if (messageByteLength(invitation) > 65536) { setValidation(t('invitationTooLarge')); return }
    const response = await server.run('lan.join', { invitation })
    const result = response?.result
    if (!result) return
    if (result.paired !== true || result.trusted !== false || typeof result.peerId !== 'string') { server.setError({ code: 'invalid_response' }); return }
    setDraft(current => ({ ...current, joinInvitation: current.joinInvitation.trim() === invitation ? '' : current.joinInvitation, joinedPeerId: result.peerId as string }))
    // Do not move the user out of a newer screen after an interrupted request.
    if (alive.current && state.peers.some(peer => peer.id === result.peerId)) onViewPeer(result.peerId)
  }
  return <div className="lan-setup">
    <section className="lan-step"><h3><span>1</span>{t('deviceIdentity')}</h3><p className="small muted">{t('publicIdHint')}</p>{publicKey ? <><p className="public-id code-value" aria-label={t('publicId')}>{publicKey}</p><CopyValue value={publicKey} label={t('copyPublicId')} t={t} /></> : <Button onClick={createIdentity} busy={server.busy.has('lan.identity')}><Icon name="shield" size={15} />{t('createIdentity')}</Button>}</section>
    <section className="lan-step"><h3><span>2</span>{t('trustedRelay')}</h3><p className="small muted">{t('relayScope')}</p><RelaySummary state={state} t={t} /><details className="relay-config" open={!state.lan?.configured}><summary>{t('changeRelay')}</summary>
      <form className="form-stack" onSubmit={configure}><label className="field">{t('deviceName')}<input value={hostname} onChange={event => setHostname(event.target.value)} maxLength={63} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}]|-){0,62}" title={t('hostnameHint')} required /></label><label className="field">{t('relayAddress')}<input value={address} onChange={event => { setAddress(event.target.value); setValidation('') }} placeholder="192.0.2.10:443" autoComplete="off" spellCheck={false} maxLength={80} required /><small className="muted">{t('relayAddressHint')}</small></label><label className="field">{t('certificatePin')}<input className="code-value" value={pin} onChange={event => { setPin(event.target.value); setValidation('') }} autoComplete="off" spellCheck={false} maxLength={64} required /><small className="muted">{t('certificateHint')}</small></label><Button type="submit" variant="primary" busy={server.busy.has('network.configure')}>{t('activateRelay')}<Icon name="arrow" size={15} /></Button></form></details>
    </section>
    {validation && <ErrorBanner message={validation} t={t} />}
    <section className="lan-step"><h3><span>3</span>{t('pairDevice')}</h3><p className="small muted">{t('joinBeforeTrust')}</p>{!ready && <p className="scope-note"><Icon name="info" size={15} />{t('relayRequired')}</p>}<div className="segmented pairing-tabs" role="group" aria-label={t('pairDevice')}><button type="button" aria-pressed={section === 'invite'} onClick={() => { setSection('invite'); setValidation('') }}>{t('inviteDevice')}</button><button type="button" aria-pressed={section === 'join'} onClick={() => { setSection('join'); setValidation('') }}>{t('joinDevice')}</button></div>
      {active && <div className="invitation-card"><div className="flex items-center justify-between gap-2"><strong>{t(consumed ? 'paired' : 'invitationReady')}</strong><Badge tone={consumed ? 'green' : expired ? 'neutral' : 'purple'}>{t(consumed ? 'paired' : expired ? 'expired' : 'active')}</Badge></div><dl><dt>{t('recipientName')}</dt><dd>{active.recipientName}</dd><dt>{t('recipientPublicId')}</dt><dd className="code-value">{active.recipientPublicKey}</dd><dt>{t('trustedRelay')}</dt><dd className="code-value">{active.relayAddress}</dd><dt>{t('expiresAt')}</dt><dd><time dateTime={active.expires}>{timestamp(active.expires, locale)}</time></dd></dl><p className="small muted">{t(consumed ? 'invitationUsed' : 'invitationScope')}</p>{!consumed && <p className="small muted">{t('invitationExpiry')}</p>}<div className="invitation-actions">{!expired && !consumed && <CopyValue value={active.value} label={t('copyInvitation')} secret t={t} />}{consumed ? <>{pairedInvitePeer && <Button onClick={() => onViewPeer(pairedInvitePeer.id)}>{t('viewDevice')}</Button>}<Button variant="ghost" onClick={() => setDraft(current => ({ ...current, activeInvitation: undefined, recipientPublicKey: '', recipientName: '' }))}>{t('inviteAnother')}</Button></> : <Button variant="ghost" onClick={cancel} busy={server.busy.has('lan.cancel')}>{t('cancelInvitation')}</Button>}</div></div>}
      {section === 'invite' && !hasActiveInvite && !consumed && <form className="form-stack" onSubmit={invite}><label className="field">{t('recipientPublicId')}<input className="code-value" value={draft.recipientPublicKey} onChange={event => { setDraft(current => ({ ...current, recipientPublicKey: event.target.value })); setValidation('') }} autoComplete="off" spellCheck={false} maxLength={64} required /></label><label className="field">{t('recipientName')}<input value={draft.recipientName} onChange={event => setDraft(current => ({ ...current, recipientName: event.target.value }))} maxLength={80} placeholder={t('recipientNamePlaceholder')} required /></label><Button variant="primary" type="submit" disabled={!ready || !publicKey} busy={server.busy.has('lan.invite')}>{t('createInvitation')}</Button></form>}
      {section === 'join' && <form className="form-stack" onSubmit={join}>{hasActiveInvite && <p className="scope-note"><Icon name="info" />{t('cancelOwnInvite')}</p>}<label className="field">{t('invitation')}<input type="password" value={draft.joinInvitation} onChange={event => { setDraft(current => ({ ...current, joinInvitation: event.target.value })); setValidation('') }} placeholder={t('invitationPlaceholder')} autoComplete="off" spellCheck={false} maxLength={65537} required /><small className="muted">{t('invitationHint')}</small></label><Button variant="primary" type="submit" disabled={!ready || hasActiveInvite || !draft.joinInvitation.trim()} busy={server.busy.has('lan.join')}>{t('join')}</Button></form>}
      {draft.joinedPeerId && <div className="paired-result" role="status"><Icon name="check" /><div><strong>{t('paired')}</strong><p>{t('joinBeforeTrust')}</p></div>{state.peers.some(peer => peer.id === draft.joinedPeerId) && <Button onClick={() => onViewPeer(draft.joinedPeerId!)}>{t('viewDevice')}</Button>}</div>}
    </section>
  </div>
}
