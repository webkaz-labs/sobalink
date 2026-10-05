import { useEffect, useRef, useState, type Dispatch, type FormEvent, type SetStateAction } from 'react'
import { messageByteLength, type LanAddress, type LanInvitationPreview, type Locale, type State } from '../api'
import { timestamp, type Translate } from '../i18n'
import { lanTranslator } from '../lan-i18n'
import './LanSetup.css'
import { PreparedLanRoutes } from './LanRoutes'
import { InitialLanPolicy, SavedLanPolicy, selectedLANPolicy, PolicySummary, type LanPolicyDraft, type LanPolicySelection } from './LanPolicy'
import { policyTranslator } from '../lan-policy-i18n'
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
export interface LanDraft extends LanPolicyDraft {
  publicKey?: string
  recipientPublicKey: string
  recipientName: string
  joinInvitation: string
  activeInvitation?: ActiveInvitation
  joinedPeerId?: string
  hostname?: string
  hostAddress?: string
  hostPort?: string
  relayAddress?: string
  relayPin?: string
  section?: 'invite' | 'join'
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
function RelaySummary({ state, t, locale }: { state: State; t: Translate; locale: Locale }) {
  const relay = state.lan?.relay
  const lt = lanTranslator(locale)
  if (!relay) return null
  return <div className="relay-summary"><div className="section-label"><Icon name="shield" />{t(relay.kind === 'host' ? 'relayHostedHere' : 'trustedRelay')}</div><p className="code-value">{relay.address}</p>{relay.certificateSHA256 && <p className="code-value fingerprint-value">{relay.certificateSHA256}</p>}<div className="flex flex-wrap gap-2">{state.lan?.configured && <Badge>{lt('savedRelay')}</Badge>}{relay.kind === 'host' && <Badge tone={state.lan?.relayReady ? 'green' : 'neutral'}>{lt(state.lan?.relayReady === true ? 'relayRunning' : state.lan?.relayReady === false ? 'relayStopped' : 'relayUnknown')}</Badge>}<Badge>{state.lan?.pairingReady ? lt('pairingListenerReady') : t('relayNotReady')}</Badge><Badge>{t(state.lan?.path || 'unknown')}</Badge></div><p className="small muted">{lt('reachabilityUnknown')}</p>{relay.kind === 'host' && state.lan?.certificate && <div className="certificate-summary"><p className="small muted">{lt('certificateExpires')}: <time dateTime={state.lan.certificate.notAfter}>{timestamp(state.lan.certificate.notAfter, locale)}</time></p>{state.lan.certificate.state !== 'valid' && <p role="status" className="scope-note">{lt(state.lan.certificate.state === 'expiring' ? 'certificateExpiring' : state.lan.certificate.state === 'expired' ? 'certificateExpired' : 'certificateNotYetValid')}</p>}</div>}</div>
}
function validPreview(value: unknown, publicKey: string | undefined): value is LanInvitationPreview {
  if (!value || typeof value !== 'object') return false
  const result = value as LanInvitationPreview
  return result.recipientMatches === true && result.recipientPublicKey === publicKey && isPublicKey(result.hostPublicKey) && typeof result.hostName === 'string' && typeof result.expires === 'string' && Number.isFinite(Date.parse(result.expires)) && result.relay?.kind === 'relay' && isRelayAddress(result.relay.address) && isPublicKey(result.relay.certificateSHA256)
}
function sameRelay(state: State, relay: LanInvitationPreview['relay']) { return state.lan?.relay?.address === relay.address && state.lan?.relay?.certificateSHA256?.toLowerCase() === relay.certificateSHA256.toLowerCase() }
function relayEndpoint(address: string, port: number) { return `${address.includes(':') ? `[${address}]` : address}:${port}` }
function eligibleAddress(value: unknown): value is LanAddress {
  if (!value || typeof value !== 'object') return false
  const item = value as LanAddress
  if (typeof item.interface !== 'string' || !item.interface.trim() || typeof item.address !== 'string') return false
  if (!isRelayAddress(relayEndpoint(item.address, 48443))) return false
  if (item.address.includes(':')) return /^(fc|fd)[a-f0-9]{2}:/i.test(item.address)
  const parts = item.address.split('.').map(Number)
  return parts[0] === 10 || (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) || (parts[0] === 192 && parts[1] === 168)
}
function HostRelay({ server, state, t, locale, hostname, setHostname, blocked, draft, setDraft }: { server: Server; state: State; t: Translate; locale: Locale; hostname: string; setHostname: (value: string) => void; blocked: boolean; draft: LanDraft; setDraft: Dispatch<SetStateAction<LanDraft>> }) {
  const lt = lanTranslator(locale)
  const alive = useAlive()
  const [addresses, setAddresses] = useState<LanAddress[]>()
  const selected = draft.hostAddress || ''
  const port = draft.hostPort ?? (state.lan?.relay?.kind === 'host' ? state.lan.relay.address.match(/:(\d+)$/)?.[1] : undefined) ?? '48443'
  const setSelected = (value: string | ((current: string) => string)) => setDraft(current => ({ ...current, hostAddress: typeof value === 'function' ? value(current.hostAddress || '') : value }))
  const setPort = (value: string) => setDraft(current => ({ ...current, hostPort: value }))
  const [validation, setValidation] = useState('')
  const [review, setReview] = useState<{ address: LanAddress; port: number; hostname: string; policy?: LanPolicySelection }>()
  const addressesRequest = useRef(0)
  const startPending = useRef(false)
  const [startAccepted, setStartAccepted] = useState(false)
  const [rotateCertificate, setRotateCertificate] = useState(false)
  const savedHost = state.lan?.relay?.kind === 'host' ? state.lan.relay : undefined
  const reviewedEndpoint = review ? relayEndpoint(review.address.address, review.port) : ''
  const savedHostIP = savedHost?.address.replace(/:\d+$/, '').replace(/^\[|\]$/g, '')
  const rotationRequired = Boolean(savedHost && review && (review.address.address !== savedHostIP || state.lan?.certificate?.state === 'expired' || state.lan?.certificate?.state === 'not-yet-valid'))
  const paired = state.peers.some(peer => peer.networks.includes('lan'))
  const rotationBlocked = Boolean(rotateCertificate && paired)
  useEffect(() => { setReview(undefined); setRotateCertificate(false); setStartAccepted(false) }, [savedHost?.address, savedHost?.certificateSHA256])
  const reviewRegion = useRef<HTMLDivElement>(null)
  useEffect(() => { if (review) reviewRegion.current?.focus() }, [review])
  const findAddresses = async () => {
    const generation = ++addressesRequest.current
    setReview(undefined); setValidation('')
    const response = await server.run('lan.addresses', {})
    if (!alive.current || generation !== addressesRequest.current || !response) return
    const values = response.result?.addresses
    if (!Array.isArray(values) || !values.every(eligibleAddress)) { server.setError({ code: 'invalid_response' }); return }
    setAddresses(values)
    setSelected(current => values.some(item => `${item.interface}\n${item.address}` === current) ? current : '')
  }
  const lookupRef = useRef(findAddresses)
  lookupRef.current = findAddresses
  useEffect(() => { if (!blocked) void lookupRef.current() }, [blocked])
  useEffect(() => { setReview(undefined); setRotateCertificate(false) }, [draft.policyMode, draft.policyPrefixes])
  const reviewHost = (event: FormEvent) => {
    event.preventDefault(); setValidation('')
    if (blocked) return
    const address = addresses?.find(item => `${item.interface}\n${item.address}` === selected)
    if (!address) { setValidation(lt('addressRequired')); return }
    const portNumber = Number(port)
    if (!/^\d+$/.test(port) || !Number.isInteger(portNumber) || portNumber < 1024 || portNumber > 65535 || [54543, 54544, 54545, ...(state.reservedPorts || [])].includes(portNumber)) { setValidation(lt('hostPortInvalid')); return }
    const policy = !state.lan?.configured ? selectedLANPolicy(draft) : undefined
    if (policy?.mode === 'allowed-lan-destinations' && !policy.prefixes.length) { setValidation(policyTranslator(locale)('required')); return }
    setRotateCertificate(false)
    setReview({ address, port: portNumber, hostname: hostname.trim(), policy })
  }
  const start = async () => {
    if (!review || blocked || rotationBlocked || (rotationRequired && !rotateCertificate) || startPending.current || startAccepted || server.busy.has('network.configure')) return
    startPending.current = true
    try {
      const response = await server.run('network.configure', { mode: 'lan', hostname: review.hostname, lan: { kind: 'host', address: reviewedEndpoint }, ...(rotateCertificate ? { rotateCertificate: true } : {}), ...(review.policy ? { lanPolicy: review.policy } : {}) })
      if (response && alive.current) setStartAccepted(true)
    } finally { startPending.current = false }
  }
  return <div className="relay-host form-stack"><p className="small muted">{lt('hostHint')}</p>{validation && <ErrorBanner message={validation} t={t} />}{review ? <div className="invitation-card relay-review" role="region" aria-label={lt('hostReview')} tabIndex={-1} ref={reviewRegion}><strong>{lt('hostReview')}</strong><dl><dt>{t('deviceName')}</dt><dd>{review.hostname}</dd><dt>{lt('selectedInterface')}</dt><dd>{review.address.interface}</dd><dt>{lt('tcpListener')}</dt><dd className="code-value">{relayEndpoint(review.address.address, review.port)}</dd></dl><p className="small muted">{lt('hostImpact')}</p>{review.policy && <PolicySummary policy={review.policy} locale={locale} />}{savedHost && <div className="form-stack"><p className="small muted">{lt('certificateReuse')}</p>{rotationRequired && <p className="scope-note">{lt('certificateRotationRequired')}</p>}<label className="checkbox-field"><input type="checkbox" checked={rotateCertificate} disabled={server.busy.has('network.configure') || startAccepted} onChange={event => setRotateCertificate(event.target.checked)} />{lt('rotateCertificate')}</label>{rotateCertificate && <p className="scope-note">{lt('certificateRotationImpact')}</p>}{rotationBlocked && <p role="alert" className="scope-note">{lt('certificatePairsPresent')}</p>}</div>}<div className="invitation-actions"><Button type="button" disabled={server.busy.has('network.configure') || startAccepted} onClick={() => { setReview(undefined); setStartAccepted(false); setRotateCertificate(false) }}>{t('cancel')}</Button><Button type="button" variant="primary" disabled={blocked || rotationBlocked || (rotationRequired && !rotateCertificate) || startAccepted} busy={server.busy.has('network.configure')} onClick={start}>{lt('startHost')}</Button></div>{startAccepted && <p role="status" className="small muted">{t('actionAccepted')}</p>}</div> : <form className="form-stack" onSubmit={reviewHost}><label className="field">{t('deviceName')}<input value={hostname} onChange={event => setHostname(event.target.value)} maxLength={63} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}]|-){0,62}" title={t('hostnameHint')} required /></label><div><Button type="button" disabled={blocked} busy={server.busy.has('lan.addresses')} onClick={findAddresses}><Icon name="refresh" size={15} />{lt('refreshAddresses')}</Button></div>{addresses && addresses.length === 0 && <p className="scope-note" role="status">{lt('noAddresses')}</p>}<label className="field">{lt('addressChoice')}<select value={selected} onChange={event => { setSelected(event.target.value); setValidation('') }} required disabled={!addresses?.length || blocked}><option value="">{lt('chooseAddress')}</option>{addresses?.map(item => <option key={`${item.interface}\n${item.address}`} value={`${item.interface}\n${item.address}`}>{item.address} · {item.interface}</option>)}</select></label><label className="field">{lt('hostPort')}<input type="number" min="1024" max="65535" step="1" value={port} onChange={event => { setPort(event.target.value); setValidation('') }} required /><small className="muted">{lt('hostPortHint')}</small></label><Button type="submit" variant="primary" disabled={blocked || !selected}>{lt('reviewHost')}<Icon name="arrow" size={15} /></Button></form>}</div>
}
export function StopApplication({ server, state, t, locale, blocked, onStopping }: { server: Server; state: State; t: Translate; locale: Locale; blocked: boolean; onStopping: () => void }) {
  const lt = lanTranslator(locale)
  const alive = useAlive()
  const [review, setReview] = useState(false)
  const reviewRegion = useRef<HTMLDivElement>(null)
  useEffect(() => { if (review) reviewRegion.current?.focus() }, [review])
  const transfers = state.transfers.filter(item => ['offered', 'awaiting-acceptance', 'queued', 'transferring', 'saving'].includes(item.status)).length
  const services = [...state.services, ...state.shares].filter(item => ['active', 'reconnecting'].includes(item.status)).length
  const stop = async () => {
    if (!review || blocked) return
    const response = await server.run('application.stop', {})
    if (!alive.current || !response) return
    if (response.result?.state !== 'stopping') { server.setError({ code: 'invalid_response' }); return }
    onStopping()
  }
  return review ? <div className="invitation-card relay-stop-review" role="region" aria-label={lt('stopReview')} tabIndex={-1} ref={reviewRegion}><strong>{lt('stopReview')}</strong><p className="small muted">{lt('stopImpact')}</p><dl><dt>{lt('activeTransfers')}</dt><dd>{transfers}</dd><dt>{lt('activeServices')}</dt><dd>{services}</dd></dl><div className="invitation-actions"><Button disabled={server.busy.has('application.stop')} onClick={() => setReview(false)}>{t('cancel')}</Button><Button variant="danger" disabled={blocked} busy={server.busy.has('application.stop')} onClick={stop}>{lt('confirmStop')}</Button></div></div> : <div><Button variant="ghost" disabled={blocked} onClick={() => setReview(true)}><Icon name="stop" size={15} />{lt('stopApplication')}</Button></div>
}

export function LanSetup({ server, state, t, locale, hostname, setHostname, draft, setDraft, onViewPeer, showStopControl = true, applicationStopping = false }: { server: Server; state: State; t: Translate; locale: Locale; hostname: string; setHostname: (value: string) => void; draft: LanDraft; setDraft: Dispatch<SetStateAction<LanDraft>>; onViewPeer: (id: string) => void; showStopControl?: boolean; applicationStopping?: boolean }) {
  const lt = lanTranslator(locale)
  const [stopping, setStopping] = useState(false)
  const [preview, setPreview] = useState<{ invitation: string; data: LanInvitationPreview; policy?: LanPolicySelection }>()
  const [joinProgress, setJoinProgress] = useState(false)
  const joinSequence = useRef(0)
  const joinPending = useRef(false)
  const previewRegion = useRef<HTMLDivElement>(null)
  useEffect(() => { if (preview) previewRegion.current?.focus() }, [preview])
  const address = draft.relayAddress ?? state.lan?.relay?.address ?? ''
  const pin = draft.relayPin ?? state.lan?.relay?.certificateSHA256 ?? ''
  const setAddress = (value: string) => setDraft(current => ({ ...current, relayAddress: value }))
  const setPin = (value: string) => setDraft(current => ({ ...current, relayPin: value }))
  const section = draft.section || (draft.joinInvitation ? 'join' : 'invite')
  const setSection = (value: 'invite' | 'join') => setDraft(current => ({ ...current, section: value }))
  const [validation, setValidation] = useState('')
  const [now, setNow] = useState(Date.now)
  const alive = useAlive()
  const blocked = server.auth !== 'ready' || server.stale || stopping || applicationStopping || server.busy.has('application.stop')
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
    if (!active && !preview) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [active, preview])
  const createIdentity = async () => {
    if (blocked) return
    const response = await server.run('lan.identity', {})
    const key = response?.result?.publicKey
    if (typeof key === 'string' && isPublicKey(key)) setDraft(current => ({ ...current, publicKey: key }))
    else if (response) server.setError({ code: 'invalid_response' })
  }
  const configure = async (event: FormEvent) => {
    event.preventDefault(); setValidation('')
    if (blocked || joinProgress) return
    if (!isRelayAddress(address.trim())) { setValidation(t('relayInvalid')); return }
    if (!isPublicKey(pin.trim())) { setValidation(t('pinInvalid')); return }
    const policy = !state.lan?.configured ? selectedLANPolicy(draft) : undefined
    if (policy?.mode === 'allowed-lan-destinations' && !policy.prefixes.length) { setValidation(policyTranslator(locale)('required')); return }
    await server.run('network.configure', { mode: 'lan', hostname: hostname.trim(), lan: { kind: 'relay', address: address.trim(), certificateSHA256: pin.trim().toLowerCase() }, ...(policy ? { lanPolicy: policy } : {}) })
  }
  const invite = async (event: FormEvent) => {
    event.preventDefault(); setValidation('')
    if (!ready || blocked || hasActiveInvite) return
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
    if (!active || consumed || blocked || joinProgress) return
    if (await server.run('lan.cancel', { invitation: active.value })) setDraft(current => current.activeInvitation?.value === active.value ? { ...current, activeInvitation: undefined } : current)
  }
  const finishJoin = async (invitation: string) => {
    const response = await server.run('lan.join', { invitation })
    const result = response?.result
    if (!result) return
    if (result.paired !== true || result.trusted !== false || typeof result.peerId !== 'string') { server.setError({ code: 'invalid_response' }); return }
    setDraft(current => ({ ...current, joinInvitation: current.joinInvitation.trim() === invitation ? '' : current.joinInvitation, joinedPeerId: result.peerId as string }))
    if (alive.current) { setPreview(undefined); setJoinProgress(false) }
    // Keep the acknowledged result, but do not navigate after closing setup.
    if (alive.current && state.peers.some(peer => peer.id === result.peerId)) onViewPeer(result.peerId)
  }
  const join = async (event: FormEvent) => {
    event.preventDefault(); setValidation('')
    if (blocked || hasActiveInvite) return
    const invitation = draft.joinInvitation.trim()
    if (!invitation) return
    if (messageByteLength(invitation) > 65536) { setValidation(t('invitationTooLarge')); return }
    if (ready) { await finishJoin(invitation); return }
    if (!publicKey) { setValidation(lt('joinNeedsIdentity')); return }
    const policy = !state.lan?.configured ? selectedLANPolicy(draft) : undefined
    if (policy?.mode === 'allowed-lan-destinations' && !policy.prefixes.length) { setValidation(policyTranslator(locale)('required')); return }
    const sequence = ++joinSequence.current
    const response = await server.run('lan.inspect', { invitation })
    if (!response || !alive.current || sequence !== joinSequence.current) return
    if (!validPreview(response.result, publicKey)) { server.setError({ code: 'invalid_response' }); return }
    if (Date.parse(response.result.expires) <= Date.now()) { setValidation(lt('invitationExpired')); return }
    setPreview({ invitation, data: response.result, policy })
  }
  const connectAndJoin = async () => {
    if (!preview || blocked || hasActiveInvite || joinPending.current || joinProgress) return
    setValidation('')
    if (preview.invitation !== draft.joinInvitation.trim()) { setPreview(undefined); setValidation(lt('invitationChanged')); return }
    if (Date.parse(preview.data.expires) <= Date.now()) { setValidation(lt('invitationExpired')); return }
    const sequence = ++joinSequence.current
    joinPending.current = true
    setJoinProgress(true)
    try {
      let current = state
      if (current.settings?.network === 'none' || !current.settings?.network) {
        const response = await server.run('network.configure', { mode: 'lan', hostname: hostname.trim(), lan: preview.data.relay, ...(preview.policy ? { lanPolicy: preview.policy } : {}) })
        if (!response || !alive.current || sequence !== joinSequence.current) return
        const refreshed = await server.refresh(true)
        if (!refreshed || !alive.current || sequence !== joinSequence.current) return
        current = refreshed
      }
      if (current.settings?.network !== 'lan' || !sameRelay(current, preview.data.relay)) { setValidation(lt('joinDifferentRelay')); return }
      if (!current.lan?.configured || !current.lan.pairingReady) { setValidation(lt('joinConfiguredHint')); return }
      if (preview.policy && (current.lan.policy?.mode !== preview.policy.mode || JSON.stringify(current.lan.policy?.prefixes) !== JSON.stringify(preview.policy.prefixes))) { server.setError({ code: 'invalid_response' }); return }
      if (Date.parse(preview.data.expires) <= Date.now()) { setValidation(lt('invitationExpired')); return }
      await finishJoin(preview.invitation)
    } finally { joinPending.current = false; if (alive.current && sequence === joinSequence.current) setJoinProgress(false) }
  }
  const changeSection = (value: 'invite' | 'join') => { ++joinSequence.current; setSection(value); setPreview(undefined); setJoinProgress(false); setValidation('') }
  return <div className="lan-setup">
    <section className="lan-step"><h3><span>1</span>{t('deviceIdentity')}</h3><p className="small muted">{t('publicIdHint')}</p>{publicKey ? <><p className="public-id code-value" aria-label={t('publicId')}>{publicKey}</p><CopyValue value={publicKey} label={t('copyPublicId')} t={t} /></> : <Button onClick={createIdentity} disabled={blocked} busy={server.busy.has('lan.identity')}><Icon name="shield" size={15} />{t('createIdentity')}</Button>}</section>
    <section className="lan-step"><h3><span>2</span>{lt('connectLan')}</h3><div className="segmented pairing-tabs" role="group" aria-label={lt('connectLan')}><button type="button" disabled={joinProgress} aria-pressed={section === 'invite'} onClick={() => changeSection('invite')}>{state.lan?.configured ? t('inviteDevice') : lt('hostRelay')}</button><button type="button" disabled={joinProgress} aria-pressed={section === 'join'} onClick={() => changeSection('join')}>{t('joinDevice')}</button></div><RelaySummary state={state} t={t} locale={locale} />
      {!state.lan?.configured ? <InitialLanPolicy server={server} locale={locale} draft={draft} disabled={blocked || joinProgress} onChange={value => { ++joinSequence.current; setPreview(undefined); setValidation(''); setDraft(current => ({ ...current, ...value })) }} /> : <SavedLanPolicy server={server} state={state} locale={locale} t={t} disabled={blocked || joinProgress} />}
      {section === 'invite' && (!state.lan?.configured || state.lan.relay?.kind === 'host' && !state.lan.relayReady) && <HostRelay server={server} state={state} t={t} locale={locale} hostname={hostname} setHostname={setHostname} blocked={blocked || joinProgress} draft={draft} setDraft={setDraft} />}
      {section === 'join' && !ready && <p className="small muted">{lt('joinSetupHint')}</p>}
      <details className="relay-config manual-relay"><summary>{lt('manualRelay')}</summary><p className="small muted">{t('relayScope')}</p>
      <form className="form-stack" onSubmit={configure}><label className="field">{t('deviceName')}<input value={hostname} onChange={event => setHostname(event.target.value)} maxLength={63} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}]|-){0,62}" title={t('hostnameHint')} required /></label><label className="field">{t('relayAddress')}<input value={address} onChange={event => { setAddress(event.target.value); setValidation('') }} placeholder="192.0.2.10:443" autoComplete="off" spellCheck={false} maxLength={80} required /><small className="muted">{t('relayAddressHint')}</small></label><label className="field">{t('certificatePin')}<input className="code-value" value={pin} onChange={event => { setPin(event.target.value); setValidation('') }} autoComplete="off" spellCheck={false} maxLength={64} required /><small className="muted">{t('certificateHint')}</small></label><Button type="submit" variant="primary" disabled={blocked || joinProgress} busy={server.busy.has('network.configure')}>{t('activateRelay')}<Icon name="arrow" size={15} /></Button></form></details>
      {state.lan?.configured && <PreparedLanRoutes server={server} state={state} t={t} locale={locale} disabled={blocked || joinProgress} />}
      {showStopControl && state.settings?.network === 'lan' && <StopApplication server={server} state={state} t={t} locale={locale} blocked={blocked || joinProgress} onStopping={() => setStopping(true)} />}
      {stopping && <p role="status" className="scope-note">{lt('stopping')}</p>}
    </section>
    {validation && <ErrorBanner message={validation} t={t} />}
    {(section === 'join' || state.lan?.configured || active || draft.joinedPeerId) && <section className="lan-step"><h3><span>3</span>{t('pairDevice')}</h3><p className="small muted">{t('joinBeforeTrust')}</p>{section === 'invite' && !ready && <p className="scope-note"><Icon name="info" size={15} />{lt('inviteNeedsRelay')}</p>}
      {active && <div className="invitation-card"><div className="flex items-center justify-between gap-2"><strong>{t(consumed ? 'paired' : 'invitationReady')}</strong><Badge tone={consumed ? 'green' : expired ? 'neutral' : 'purple'}>{t(consumed ? 'paired' : expired ? 'expired' : 'active')}</Badge></div><dl><dt>{t('recipientName')}</dt><dd>{active.recipientName}</dd><dt>{t('recipientPublicId')}</dt><dd className="code-value">{active.recipientPublicKey}</dd><dt>{t('trustedRelay')}</dt><dd className="code-value">{active.relayAddress}</dd><dt>{t('expiresAt')}</dt><dd><time dateTime={active.expires}>{timestamp(active.expires, locale)}</time></dd></dl><p className="small muted">{t(consumed ? 'invitationUsed' : 'invitationScope')}</p>{!consumed && <p className="small muted">{t('invitationExpiry')}</p>}<div className="invitation-actions">{!expired && !consumed && <CopyValue value={active.value} label={t('copyInvitation')} secret t={t} />}{consumed ? <>{pairedInvitePeer && <Button onClick={() => onViewPeer(pairedInvitePeer.id)}>{t('viewDevice')}</Button>}<Button variant="ghost" onClick={() => setDraft(current => ({ ...current, activeInvitation: undefined, recipientPublicKey: '', recipientName: '' }))}>{t('inviteAnother')}</Button></> : <Button variant="ghost" onClick={cancel} disabled={blocked || joinProgress} busy={server.busy.has('lan.cancel')}>{t('cancelInvitation')}</Button>}</div></div>}
      {section === 'invite' && !hasActiveInvite && !consumed && <form className="form-stack" onSubmit={invite}><label className="field">{t('recipientPublicId')}<input className="code-value" value={draft.recipientPublicKey} onChange={event => { setDraft(current => ({ ...current, recipientPublicKey: event.target.value })); setValidation('') }} autoComplete="off" spellCheck={false} maxLength={64} required /></label><label className="field">{t('recipientName')}<input value={draft.recipientName} onChange={event => setDraft(current => ({ ...current, recipientName: event.target.value }))} maxLength={80} placeholder={t('recipientNamePlaceholder')} required /></label><Button variant="primary" type="submit" disabled={!ready || blocked || !publicKey} busy={server.busy.has('lan.invite')}>{t('createInvitation')}</Button></form>}
      {section === 'join' && <>{preview ? <div className="invitation-card relay-review" role="region" aria-label={lt('joinReview')} tabIndex={-1} ref={previewRegion}><strong>{lt('joinReview')}</strong><dl><dt>{lt('invitingDevice')}</dt><dd>{preview.data.hostName}</dd><dt>{lt('invitingPublicId')}</dt><dd className="code-value">{preview.data.hostPublicKey}</dd><dt>{t('trustedRelay')}</dt><dd className="code-value">{preview.data.relay.address}</dd><dt>{t('certificatePin')}</dt><dd className="code-value">{preview.data.relay.certificateSHA256}</dd><dt>{t('expiresAt')}</dt><dd><time dateTime={preview.data.expires}>{timestamp(preview.data.expires, locale)}</time></dd></dl><p className="small muted">{lt('previewLimit')}</p><p className="small muted">{lt('joinRelayImpact')}</p>{preview.policy && <PolicySummary policy={preview.policy} locale={locale} />}<div className="invitation-actions"><Button type="button" disabled={joinProgress} onClick={() => { ++joinSequence.current; setPreview(undefined); setValidation('') }}>{t('cancel')}</Button><Button type="button" variant="primary" disabled={blocked || Date.parse(preview.data.expires) <= now} busy={joinProgress} onClick={connectAndJoin}>{lt('activateAndJoin')}</Button></div>{Date.parse(preview.data.expires) <= now && <p role="status">{lt('invitationExpired')}</p>}</div> : <form className="form-stack" onSubmit={join}>{hasActiveInvite && <p className="scope-note"><Icon name="info" />{t('cancelOwnInvite')}</p>}{!ready && !publicKey && <p className="scope-note">{lt('joinNeedsIdentity')}</p>}<label className="field">{t('invitation')}<input type="password" value={draft.joinInvitation} onChange={event => { ++joinSequence.current; setDraft(current => ({ ...current, joinInvitation: event.target.value })); setValidation('') }} placeholder={t('invitationPlaceholder')} autoComplete="off" spellCheck={false} maxLength={65537} required /><small className="muted">{t('invitationHint')}</small></label><Button variant="primary" type="submit" disabled={blocked || hasActiveInvite || (!ready && !publicKey) || !draft.joinInvitation.trim()} busy={server.busy.has(ready ? 'lan.join' : 'lan.inspect')}>{ready ? t('join') : lt('reviewInvitation')}</Button></form>}</>}

      {draft.joinedPeerId && <div className="paired-result" role="status"><Icon name="check" /><div><strong>{t('paired')}</strong><p>{t('joinBeforeTrust')}</p></div>{state.peers.some(peer => peer.id === draft.joinedPeerId) && <Button onClick={() => onViewPeer(draft.joinedPeerId!)}>{t('viewDevice')}</Button>}</div>}
    </section>}
  </div>
}
