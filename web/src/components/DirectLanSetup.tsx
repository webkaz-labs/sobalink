import { useEffect, useRef, useState, type Dispatch, type FormEvent, type SetStateAction } from 'react'
import type { DirectLanInvitationPreview, LanAddress, Locale, State } from '../api'
import type { Translate } from '../i18n'
import { timestamp } from '../i18n'
import { directLanText } from '../direct-lan-i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, useAlive } from './ui'
import { isPublicKey, isRelayAddress } from './LanSetup'

export interface DirectLanDraft {
  listen?: string; prefixes?: string; recipient: string; name: string; invitation: string
  active?: { value: string; expires: string; recipient: string; consumed?: boolean; qr?: boolean[][] }
  joinedPeerId?: string
}
export const emptyDirectLanDraft = (): DirectLanDraft => ({ recipient: '', name: '', invitation: '' })
function privateEndpoint(endpoint: string) {
  if (!isRelayAddress(endpoint)) return false
  const port = Number(endpoint.match(/:(\d+)$/)?.[1]); if (port < 1024 || port > 65535) return false
  const ip = endpoint.replace(/:\d+$/, '').replace(/^\[|\]$/g, '').toLowerCase()
  if (ip.includes(':')) return ip === '::1' || /^(fc|fd)[0-9a-f]{2}:/.test(ip)
  const p = ip.split('.').map(Number)
  return p[0] === 10 || p[0] === 127 || (p[0] === 172 && p[1] >= 16 && p[1] <= 31) || (p[0] === 192 && p[1] === 168)
}
function validPreview(value: unknown, publicKey?: string): value is DirectLanInvitationPreview {
  if (!value || typeof value !== 'object') return false
  const p = value as DirectLanInvitationPreview
  return p.recipientMatches === true && p.recipientPublicKey === publicKey && isPublicKey(p.hostPublicKey) && typeof p.hostName === 'string' && privateEndpoint(p.endpoint) && Number.isFinite(Date.parse(p.expires))
}
function CopyField({ value, label, locale, secret = false }: { value: string; label: string; locale: Locale; secret?: boolean }) {
  const [copied, setCopied] = useState(false)
  const field = useRef<HTMLTextAreaElement>(null)
  const alive = useAlive()
  useEffect(() => { setCopied(false) }, [value])
  const copy = async () => {
    try { if (!navigator.clipboard?.writeText) throw new Error(); await navigator.clipboard.writeText(value); if (alive.current) setCopied(true) }
    catch { field.current?.focus(); field.current?.select() }
  }
  return <div className="form-stack"><label className="field">{label}<textarea ref={field} className={`code-value ${secret ? 'private-copy' : ''}`} value={value} readOnly rows={secret ? 5 : 2} autoComplete="off" spellCheck={false} /></label><div><Button onClick={copy}>{directLanText(locale, copied ? 'copied' : 'copy')}</Button></div><p className="small muted">{directLanText(locale, 'copyHint')}</p></div>
}
export function DirectLanSetup({ server, state, locale, t, hostname, setHostname, draft, setDraft, onViewPeer, blocked = false }: {
  server: Server; state: State; locale: Locale; t: Translate; hostname: string; setHostname: (value: string) => void
  draft: DirectLanDraft; setDraft: Dispatch<SetStateAction<DirectLanDraft>>; onViewPeer: (id: string) => void; blocked?: boolean
}) {
  const d = (key: Parameters<typeof directLanText>[1]) => directLanText(locale, key)
  const alive = useAlive(), pending = useRef(false), sequence = useRef(0)
  const [error, setError] = useState(''), [now, setNow] = useState(Date.now)
  const [review, setReview] = useState<{ listen: string; prefixes: string[]; hostname: string }>()
  const [preview, setPreview] = useState<{ value: string; data: DirectLanInvitationPreview; binding: string }>()
  const [wantQR, setWantQR] = useState(false)
  const [addresses, setAddresses] = useState<LanAddress[]>()
  const [section, setSection] = useState<'invite' | 'join'>(draft.invitation ? 'join' : 'invite')
  const status = state.directLAN
  const ready = state.settings?.network === 'direct-lan' && Boolean(status?.configured && status.listenerReady && !status.recoveryRequired)
  const disabled = blocked || server.stale || server.auth !== 'ready' || Boolean(status?.recoveryRequired)
  const listen = draft.listen ?? status?.endpoint ?? ''
  const prefixes = draft.prefixes ?? status?.prefixes?.join(', ') ?? ''
  const binding = JSON.stringify([status?.publicKey, status?.endpoint, status?.prefixes])
  const active = draft.active
  const pairedInvite = active && state.peers.find(p => p.networks.includes('direct-lan') && p.id === active.recipient)
  const consumed = Boolean(active?.consumed || pairedInvite)
  const expired = Boolean(active && Date.parse(active.expires) <= now)
  useEffect(() => {
    if (!active && !preview) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [active, preview])
  useEffect(() => {
    if (active && (consumed || expired) && active.value) setDraft(current => current.active === active ? { ...current, active: { ...active, value: '', consumed, qr: undefined } } : current)
  }, [active, consumed, expired, setDraft])
  const change = (patch: Partial<DirectLanDraft>) => { sequence.current++; setDraft(current => ({ ...current, ...patch })); setError(''); setPreview(undefined) }
  const run = async (action: () => Promise<void>) => { if (disabled || pending.current) return; pending.current = true; try { await action() } finally { pending.current = false } }
  const findAddresses = () => run(async () => {
    const result = await server.run('lan.addresses', {})
    if (!result || !alive.current) return
    const values = result.result?.addresses
    if (!Array.isArray(values) || !values.every(v => typeof v?.address === 'string' && typeof v.interface === 'string' && typeof v.prefix === 'string' && privateEndpoint(`${v.address.includes(':') ? `[${v.address}]` : v.address}:48444`))) { server.setError({ code: 'invalid_response' }); return }
    setAddresses(values as LanAddress[])
  })
  const reviewConfig = (event: FormEvent) => {
    event.preventDefault(); if (disabled) return
    const selected = prefixes.split(/[\s,]+/).filter(Boolean)
    if (!privateEndpoint(listen.trim()) || [54543, 54544, 54545, ...(state.reservedPorts || [])].includes(Number(listen.match(/:(\d+)$/)?.[1])) || !selected.length) { setError(d('invalid')); return }
    setError(''); setReview({ listen: listen.trim(), prefixes: selected, hostname: hostname.trim() })
  }
  const configure = () => run(async () => { if (!review) return; const result = await server.run('network.configure', { mode: 'direct-lan', hostname: review.hostname, directLAN: { listen: review.listen, prefixes: review.prefixes } }); if (result && alive.current) setReview(undefined) })
  const invite = (event: FormEvent) => { event.preventDefault(); void run(async () => {
    if (!ready || (active && !consumed && !expired) || !isPublicKey(draft.recipient.trim()) || !draft.name.trim()) { setError(d('invalid')); return }
    const recipient = draft.recipient.trim().toLowerCase()
    const result = await server.run('direct-lan.invite', { recipientPublicKey: recipient, name: draft.name.trim(), ttlSeconds: 300, ...(wantQR ? { qr: true } : {}) })
    if (!result) return
    if (!alive.current) { if (typeof result.result?.invitation === 'string') await server.run('direct-lan.cancel', { invitation: result.result.invitation }); return }
    if (typeof result.result?.invitation !== 'string' || typeof result.result.expires !== 'string' || !Number.isFinite(Date.parse(result.result.expires)) || result.result.recipientPublicKey !== recipient) { server.setError({ code: 'invalid_response' }); return }
    const qr = result.result.qr as boolean[][] | undefined
    if (qr !== undefined && (!Array.isArray(qr) || qr.length<21 || qr.length>185 || qr.some(row => !Array.isArray(row)||row.length!==qr.length||row.some(p => typeof p !== 'boolean')))) {server.setError({code:'invalid_response'});return}
    setDraft(current => ({ ...current, active: { qr, value: result.result!.invitation as string, expires: result.result!.expires as string, recipient } }))
  }) }
  const cancel = () => run(async () => { if (!active?.value) return; const result = await server.run('direct-lan.cancel', { invitation: active.value }); if (result && alive.current) setDraft(current => current.active === active ? { ...current, active: undefined } : current) })
  const inspect = (event: FormEvent) => { event.preventDefault(); void run(async () => {
    if (!ready || !draft.invitation) return
    const value = draft.invitation, generation = sequence.current, localBinding = binding
    const result = await server.run('direct-lan.inspect', { invitation: value })
    if (!result || !alive.current || generation !== sequence.current) return
    if (!validPreview(result.result, status?.publicKey)) { server.setError({ code: 'invalid_response' }); return }
    setPreview({ value, data: result.result, binding: localBinding })
  }) }
  const join = () => run(async () => {
    if (!ready || !preview || preview.value !== draft.invitation || preview.binding !== binding || Date.parse(preview.data.expires) <= Date.now()) { setPreview(undefined); setError(d('changed')); return }
    const result = await server.run('direct-lan.join', { invitation: preview.value })
    if (!result || !alive.current) return
    if (result.result?.paired !== true || typeof result.result.peerId !== 'string') { server.setError({ code: 'invalid_response' }); return }
    setDraft(current => ({ ...current, invitation: '', joinedPeerId: result.result!.peerId as string })); setPreview(undefined)
  })
  return <section className="form-stack subsection direct-lan-setup" aria-label={d('title')}>
    <p className="muted">{d('intro')}</p>
    {error && <ErrorBanner message={error} t={t} />}
    {status?.resourceRestartRequired && <p className="scope-note" role="status">{d('resourceRestart')}</p>}
    {status?.recoveryRequired && <p className="scope-note" role="alert">{d('recovery')}</p>}
    {status?.configured && <div className="relay-summary"><p className="code-value">{status.endpoint}</p><p className="code-value">{status.prefixes?.join(', ')}</p><div className="flex flex-wrap gap-2"><Badge>{d('configured')}</Badge><Badge tone={ready ? 'green' : 'neutral'}>{d(ready ? 'ready' : 'notReady')}</Badge></div><p className="small muted">{d('reachability')}</p></div>}
    {!ready && (review ? <div className="invitation-card" role="region" aria-label={d('reviewTitle')}><h3>{d('reviewTitle')}</h3><dl><dt>{t('deviceName')}</dt><dd>{review.hostname}</dd><dt>{d('endpoint')}</dt><dd className="code-value">{review.listen}</dd><dt>{d('prefixes')}</dt><dd className="code-value">{review.prefixes.join(', ')}</dd></dl><p className="muted">{d('impact')}</p><div className="modal-actions"><Button disabled={server.busy.has('network.configure')} onClick={() => setReview(undefined)}>{t('cancel')}</Button><Button variant="primary" disabled={disabled} busy={server.busy.has('network.configure')} onClick={configure}>{d('start')}</Button></div></div> : <form className="form-stack" onSubmit={reviewConfig}><label className="field">{t('deviceName')}<input required value={hostname} maxLength={63} onChange={e => setHostname(e.target.value)} autoComplete="off" /></label><div><Button type="button" disabled={disabled} busy={server.busy.has('lan.addresses')} onClick={findAddresses}>{d('addresses')}</Button></div>{addresses && <label className="field">{d('chooseAddress')}<select value="" disabled={disabled} onChange={e => { const address=addresses[Number(e.target.value)]; if (!address) return; const port=listen.match(/:(\d+)$/)?.[1] || '48444'; change({listen:`${address.address.includes(':') ? `[${address.address}]` : address.address}:${port}`,prefixes:address.prefix}) }}><option value="">{d('chooseAddress')}</option>{addresses.map((address,index) => <option key={`${address.interface}:${address.address}`} value={index}>{address.address} · {address.interface}</option>)}</select></label>}{addresses?.length === 0 && <p role="status">{d('noAddresses')}</p>}<label className="field">{d('listen')}<input required value={listen} onChange={e => change({ listen: e.target.value })} placeholder="192.168.50.10:48444" autoComplete="off" spellCheck={false} /><small className="muted">{d('listenHint')}</small></label><label className="field">{d('prefixes')}<input required value={prefixes} onChange={e => change({ prefixes: e.target.value })} placeholder="192.168.50.0/24" autoComplete="off" spellCheck={false} /><small className="muted">{d('prefixesHint')}</small></label><Button type="submit" variant="primary" disabled={disabled}>{d('review')}</Button></form>)}
    {status?.publicKey && <><CopyField value={status.publicKey} label={d('key')} locale={locale} /><p className="small muted">{d('keyHint')}</p></>}
    <p className="small muted">{d('udp')}</p>
    {!ready ? <p className="scope-note">{d('configureFirst')}</p> : <>
      <div className="segmented" role="group" aria-label={t('pairing')}><button type="button" aria-pressed={section === 'invite'} onClick={() => setSection('invite')}>{d('invite')}</button><button type="button" aria-pressed={section === 'join'} onClick={() => setSection('join')}>{d('join')}</button></div>
      {section === 'invite' && (active && !consumed && !expired ? <div className="invitation-card"><h3>{d('invitation')}</h3><p className="small muted">{d('secret')}</p><p>{d('expires')}: {timestamp(active.expires, locale)}</p><p className="code-value">{active.recipient}</p><CopyField value={active.value} label={d('invitation')} locale={locale} secret />{active.qr && <figure className="auth-qr auth-private"><svg role="img" aria-label={d('qrAlt')} viewBox={`0 0 ${active.qr.length} ${active.qr.length}`}><rect width="100%" height="100%" className="qr-background" /><path d={active.qr.flatMap((row,y) => row.flatMap((pixel,x) => pixel ? [`M${x} ${y}h1v1h-1z`] : [])).join('')} className="qr-modules" /></svg></figure>}<Button variant="danger" onClick={cancel} disabled={disabled} busy={server.busy.has('direct-lan.cancel')}>{d('cancelInvite')}</Button></div> : <><form className="form-stack" onSubmit={invite}><label className="field">{d('recipient')}<input value={draft.recipient} onChange={e => change({ recipient: e.target.value })} required pattern="[a-fA-F0-9]{64}" maxLength={64} spellCheck={false} autoComplete="off" /></label><label className="field">{d('recipientName')}<input value={draft.name} onChange={e => change({ name: e.target.value })} required maxLength={128} autoComplete="off" /></label><label className="checkbox-field"><input type="checkbox" checked={wantQR} onChange={e => setWantQR(e.target.checked)} disabled={disabled} />{d('qr')}</label><Button type="submit" variant="primary" disabled={disabled} busy={server.busy.has('direct-lan.invite')}>{d('inviteAction')}</Button></form>{expired && !consumed && <p role="status">{d('expired')}</p>}{consumed && <p role="status">{d('consumed')}</p>}</>)}
      {section === 'join' && <><form className="form-stack" onSubmit={inspect}><label className="field">{d('paste')}<textarea value={draft.invitation} onChange={e => change({ invitation: e.target.value })} required maxLength={16384} rows={5} autoComplete="off" spellCheck={false} className="private-copy code-value" /></label><Button type="submit" disabled={disabled || !draft.invitation} busy={server.busy.has('direct-lan.inspect')}>{d('inspect')}</Button></form>{preview && <div className="invitation-card" role="region" aria-label={d('joinTitle')}><h3>{d('joinTitle')}</h3><dl><dt>{d('host')}</dt><dd>{preview.data.hostName}</dd><dt>{d('hostKey')}</dt><dd className="code-value">{preview.data.hostPublicKey}</dd><dt>{d('endpoint')}</dt><dd className="code-value">{preview.data.endpoint}</dd><dt>{d('expires')}</dt><dd>{timestamp(preview.data.expires, locale)}</dd></dl><p className="small muted">{d('joinImpact')}</p><div className="modal-actions"><Button onClick={() => setPreview(undefined)} disabled={server.busy.has('direct-lan.join')}>{t('cancel')}</Button><Button variant="primary" onClick={join} disabled={disabled || preview.binding !== binding || Date.parse(preview.data.expires) <= now} busy={server.busy.has('direct-lan.join')}>{d('pair')}</Button></div></div>}{draft.joinedPeerId && <div><p role="status">{d('consumed')}</p><Button onClick={() => onViewPeer(draft.joinedPeerId!)}>{d('openPeer')}</Button></div>}</>}
    </>}
  </section>
}
