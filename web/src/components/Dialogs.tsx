import { useEffect, useMemo, useRef, useState, type Dispatch, type FormEvent, type SetStateAction } from 'react'
import { safeAuthURL, type Locale, type Peer, type State, type Theme, type ServiceConfigResult } from '../api'
import { errorText, errorDetail, networkLabel, type Translate } from '../i18n'
import { previewPorts } from '../ports'
import { draftFromConfig, newServiceDraft, readServiceConfig, serviceDraftIssue, servicePayload, uniqueServiceName, type SavedServiceAction, type ServiceDraft } from '../service-form'
import { serviceText } from '../service-i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Icon, Modal, useAlive } from './ui'
import { LanSetup, StopApplication, type LanDraft } from './LanSetup'
import { lanTranslator } from '../lan-i18n'

interface Base { t: Translate; onClose: () => void; server: Server }
interface DirectoryDraft { directoryDraft?: string; onDirectoryDraft: (value: string | undefined) => void }
export function Preferences({ t, onClose, locale, theme, setLocale, setTheme, server, directoryDraft, onDirectoryDraft }: Base & DirectoryDraft & { locale: 'auto' | Locale; theme: Theme; setLocale: (value: 'auto' | Locale) => void; setTheme: (value: Theme) => void }) {
  const alive = useAlive()
  const directory = directoryDraft ?? server.state?.settings?.receiveDirectory ?? server.state?.self.receiveDirectory ?? ''
  const [saved, setSaved] = useState(false)
  const save = async (event: FormEvent) => { event.preventDefault(); if (await server.run('settings.update', { locale, theme, receiveDirectory: directory.trim() }) && alive.current) { onDirectoryDraft(undefined); setSaved(true) } }
  return <Modal title={t('settings')} t={t} onClose={onClose}><p className="muted">{t('preferencesHint')}</p>
    {server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}
    <label className="field">{t('language')}<select value={locale} onChange={event => setLocale(event.target.value as 'auto' | Locale)}><option value="auto">{t('automatic')}</option><option value="ja">日本語</option><option value="en">English</option></select></label>
    <fieldset className="field"><legend>{t('appearance')}</legend><div className="segmented theme-options">{(['system', 'light', 'dark'] as const).map(value => <button key={value} type="button" aria-pressed={theme === value} onClick={() => setTheme(value)}><Icon name={value === 'system' ? 'monitor' : value === 'light' ? 'sun' : 'moon'} />{t(value)}</button>)}</div></fieldset>
    {server.auth === 'ready' && <form className="form-stack subsection" onSubmit={save}><label className="field">{t('receiveDirectory')}<input value={directory} onChange={event => { onDirectoryDraft(event.target.value); setSaved(false) }} placeholder={t('directoryPlaceholder')} spellCheck={false} autoComplete="off" /></label><div className="modal-actions">{saved && <span className="small muted" role="status">{t('settingsSaved')}</span>}<Button type="submit" variant="primary" busy={server.busy.has('settings.update')}>{t('save')}</Button></div></form>}
  </Modal>
}
export function NetworkDialog({ t, onClose, server, locale, lanDraft, setLanDraft, onViewPeer }: Base & { locale: Locale; lanDraft: LanDraft; setLanDraft: Dispatch<SetStateAction<LanDraft>>; onViewPeer: (id: string) => void }) {
  const lt = lanTranslator(locale)
  const [hostname, updateHostname] = useState(lanDraft.hostname ?? server.state?.self.name ?? 'sobalink')
  const setHostname = (value: string) => { updateHostname(value); setLanDraft(current => ({ ...current, hostname: value })) }
  const [networkStopping, setNetworkStopping] = useState(false)
  const blocked = networkStopping || server.auth !== 'ready' || server.stale || server.busy.has('application.stop')
  const [mode, setMode] = useState<'tailnet' | 'lan'>(server.state?.settings?.network === 'lan' ? 'lan' : 'tailnet')
  const [authUrl, setAuthUrl] = useState('')
  const activeMode = server.state?.settings?.network
  const engineActive = !['idle', 'offline', 'none', 'stopped', ''].includes((server.state?.self.status || '').toLowerCase())
  const switchingActive = Boolean(engineActive && activeMode && activeMode !== 'none' && activeMode !== mode)
  const configure = async (event: FormEvent) => { event.preventDefault(); if (blocked) return; await server.run('network.configure', { mode, hostname: hostname.trim() }) }
  const signIn = async () => {
    if (blocked) return
    setAuthUrl('')
    const response = await server.run('network.login', {})
    const value = response?.result?.authUrl
    if (typeof value === 'string') {
      const safe = safeAuthURL(value)
      if (safe) setAuthUrl(safe)
      else server.setError({ code: 'invalid_response' })
    }
  }
  return <Modal title={t('chooseNetwork')} t={t} onClose={onClose}><p className="muted">{t('networkHint')}</p>
    {server.state && <div className="network-current"><span>{t(server.state.settings?.network === 'lan' ? 'lan' : server.state.settings?.network === 'tailnet' ? 'tailnet' : 'disabledNetwork')}</span><Badge>{networkLabel(server.state.self.status, t)}</Badge>{server.state.self.error && <div className="error-copy field-error"><p>{server.state.self.errorCode ? errorText({ code: server.state.self.errorCode }, t) : t('networkProblem')}</p><details><summary>{t('technicalDetails')}</summary><p>{server.state.self.error}</p></details></div>}</div>}
    {server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}
      <div className="network-options">{(['tailnet', 'lan'] as const).map(value => <label key={value} className={`network-option ${mode === value ? 'selected' : ''}`}><input type="radio" name="network" disabled={blocked} value={value} checked={mode === value} onChange={() => { setMode(value); setAuthUrl('') }} /><Icon name={value === 'tailnet' ? 'globe' : 'wifi'} size={23} /><span><strong>{t(value)}</strong><small>{t(value === 'tailnet' ? 'tailnetHint' : 'lanHint')}</small></span></label>)}</div>
    {switchingActive && <p className="scope-note network-restart">{t('networkSwitchRestart')}</p>}
    {mode === 'tailnet' && !switchingActive && <form onSubmit={configure} className="form-stack subsection">
      <label className="field">{t('deviceName')}<input value={hostname} onChange={event => setHostname(event.target.value)} maxLength={63} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}]|-){0,62}" title={t('hostnameHint')} autoComplete="off" required /><small className="muted">{t('hostnameHint')}</small></label>
      <div className="modal-actions"><Button onClick={onClose} type="button">{t('close')}</Button><Button type="submit" variant="primary" disabled={blocked} busy={server.busy.has('network.configure')}>{t('activate')}<Icon name="arrow" /></Button></div>
    </form>}
    {mode === 'lan' && !switchingActive && server.state && <LanSetup server={server} state={server.state} t={t} locale={locale} hostname={hostname} setHostname={setHostname} draft={lanDraft} setDraft={setLanDraft} onViewPeer={onViewPeer} showStopControl={false} applicationStopping={networkStopping} />}
    {mode === 'tailnet' && !switchingActive && <div className="subsection"><Button onClick={signIn} disabled={blocked || activeMode !== 'tailnet' || !engineActive} busy={server.busy.has('network.login')}><Icon name="globe" />{t('signInTailscale')}</Button>{authUrl && <div className="signin-link"><p className="muted">{t('signInLinkHint')}</p><a href={authUrl} target="_blank" rel="noreferrer noopener" className="button button-primary">{t('continueSignIn')}<Icon name="arrow" /></a></div>}</div>}
    {server.state && <div className="network-restart">{activeMode && activeMode !== 'none' && <p className="small muted">{t('networkRestart')}</p>}<StopApplication server={server} state={server.state} t={t} locale={locale} blocked={blocked} onStopping={() => setNetworkStopping(true)} />{networkStopping && <p className="scope-note" role="status">{lt('stopping')}</p>}</div>}
  </Modal>
}
export function AutosaveDialog({ t, onClose, server, peer, locale, edit = false, directoryDraft, onDirectoryDraft }: Base & DirectoryDraft & { peer: Peer; locale: Locale; edit?: boolean }) {
  const alive = useAlive()
  const directory = directoryDraft ?? peer.autosave?.directory ?? server.state?.settings?.receiveDirectory ?? server.state?.self.receiveDirectory ?? ''
  const [validation, setValidation] = useState('')
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!directory.trim()) { setValidation(t('directoryRequired')); return }
    const result = await server.run('peer.autosave', { peerId: peer.id, ...(edit ? {} : { enabled: true }), directory: directory.trim() }, `autosave:${peer.id}`)
    if (result && alive.current) { onDirectoryDraft(undefined); onClose() }
  }
  return <Modal title={t('autosave')} onClose={onClose} t={t}><div className="target-pill"><Icon name="monitor" /><strong>{peer.name}</strong><Badge tone="green">{t('trusted')}</Badge></div><p className="muted">{serviceText(locale, edit ? 'folderOnly' : 'folderEnable')}</p>
    {(validation || server.error != null) && <ErrorBanner message={validation || errorText(server.error, t)} detail={!validation ? errorDetail(server.error, t) : undefined} t={t} />}
    <form onSubmit={submit} className="form-stack"><label className="field">{t('receiveDirectory')}<input value={directory} onChange={event => { onDirectoryDraft(event.target.value); setValidation('') }} placeholder={t('directoryPlaceholder')} autoComplete="off" spellCheck={false} required /></label><div className="scope-note"><Icon name="shield" />{t('autosaveScope')}</div><div className="modal-actions"><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button variant="primary" type="submit" busy={server.busy.has(`autosave:${peer.id}`)} disabled={server.stale || (!edit && (!peer.trusted || !peer.verified || !peer.bridge))}>{edit ? serviceText(locale, 'saveFolder') : t('enableAutosave')}</Button></div></form>
  </Modal>
}
export function PausePeerDialog({ t, onClose, server, peer }: Base & { peer: Peer }) {
  const alive = useAlive()
  const affected = (server.state?.transfers || []).filter(item => item.peerId === peer.id && item.direction === 'outgoing' && ['offered', 'awaiting-acceptance', 'queued', 'transferring', 'saving'].includes(item.status))
  const pause = async () => {
    const result = await server.run('peer.autosave', { peerId: peer.id, paused: true }, `autosave:${peer.id}`)
    if (result && alive.current) onClose()
  }
  return <Modal title={t('pausePeer')} t={t} onClose={onClose}><div className="target-pill"><Icon name="monitor" /><strong>{peer.name}</strong></div><p className="muted">{t('pauseImpact')}</p><div className="scope-note"><Icon name="info" /><span>{t('pauseScope')}</span></div>{affected.length > 0 && <p className="pause-affected"><strong>{t('pausedBatches')}: {affected.length}</strong></p>}{server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}<div className="modal-actions"><Button onClick={onClose}>{t('cancel')}</Button><Button variant="primary" onClick={pause} busy={server.busy.has(`autosave:${peer.id}`)}>{t('pausePeer')}</Button></div></Modal>
}
export function RevokePairingDialog({ t, onClose, server, peer }: Base & { peer: Peer }) {
  const alive = useAlive()
  const revoke = async () => {
    const result = await server.run('lan.revoke', { peerId: peer.id }, `lan-revoke:${peer.id}`)
    if (result && alive.current) onClose()
  }
  return <Modal title={t('revokePairing')} t={t} onClose={onClose}><div className="target-pill"><Icon name="monitor" /><strong>{peer.name}</strong></div><p className="code-value">{peer.id}</p><p className="muted">{t('revokePairingHint')}</p>{server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}<div className="modal-actions"><Button onClick={onClose}>{t('cancel')}</Button><Button variant="danger" onClick={revoke} busy={server.busy.has(`lan-revoke:${peer.id}`)}>{t('revokePairing')}</Button></div></Modal>
}
export function ServiceDialog({ t, locale, onClose, onStarted, server, peer, mode, state, draft: storedDraft, onDraft, source }: Base & { peer: Peer; mode: 'connect' | 'share'; state: State; locale: Locale; draft?: ServiceDraft; onDraft: (draft: ServiceDraft | undefined) => void; source?: SavedServiceAction; onStarted?: () => void }) {
  const alive = useAlive()
  const s = (key: string) => serviceText(locale, key)
  const [loaded, setLoaded] = useState<ServiceConfigResult>()
  const [loading, setLoading] = useState(Boolean(source))
  const [reload, setReload] = useState(0)
  const [validation, setValidation] = useState('')
  const configReadId = useRef(crypto.randomUUID())
  const latest = useRef({ storedDraft, onDraft, state })
  latest.current = { storedDraft, onDraft, state }
  useEffect(() => {
    if (!source) return
    let cancelled = false
    setLoading(true); setLoaded(undefined); setValidation('')
    void Promise.resolve().then(() => cancelled ? undefined : server.run('service.config', { id: source.id }, `config:${source.id}:${configReadId.current}:${reload}`)).then(result => {
      if (cancelled) return
      if (result) {
        try {
          const record = readServiceConfig(result.result, source.id, mode)
          setLoaded(record)
          if (!latest.current.storedDraft) latest.current.onDraft(draftFromConfig(record, source, latest.current.state))
        } catch { setValidation('invalid_response') }
      }
      setLoading(false)
    })
    return () => { cancelled = true }
  }, [source?.id, source?.intent, mode, reload, server.run])
  const draft = storedDraft || newServiceDraft(peer, mode, state)
  const { name, ports, exclusions, localPort, protocol, ttl, purpose, discovery, peers, serviceId } = draft
  const change = (patch: Partial<ServiceDraft>) => { onDraft({ ...draft, ...patch }); setValidation('') }
  const available = (state.availableServices || []).filter(item => item.peerId === peers[0])
  const scopeNetwork = draft.backend
  const serviceMissing = Boolean(serviceId && !available.some(item => item.id === serviceId && item.network === protocol && (item.ports || String(item.remotePort || '')) === ports))
  const preview = useMemo(() => { try { return { value: previewPorts(ports, exclusions, localPort, mode, state.reservedPorts), error: '' } } catch (error) { const code = (error as { code?: string }).code || ''; return { value: null, error: ports ? serviceText(locale, code) || errorText(error, t) : '' } } }, [ports, exclusions, localPort, mode, t, locale, state.reservedPorts])
  const localIssue = serviceDraftIssue(draft, state, mode, loaded)
  const chosen = peers.map(id => state.peers.find(item => item.id === id)?.name || id)
  const serverCode = (server.error as { code?: string } | null)?.code
  const issue = localIssue || (source && ['service_revision_conflict', 'service_active', 'service_backend_mismatch'].includes(serverCode || '') ? serverCode! : null)
  const error = validation ? s(validation) || errorText({ code: validation }, t) : server.error != null ? s(serverCode || '') || errorText(server.error, t) : ''
  const reset = () => { onDraft(undefined); setValidation(''); server.setError(null); if (source) setReload(value => value + 1) }
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (source && (!loaded || loading)) return
    if (issue) { setValidation(issue); return }
    if (serviceMissing) { setValidation('serviceUnavailable'); return }
    if (!preview.value || server.stale) return
    const result = await server.run(mode === 'connect' ? 'service.connect' : 'service.share', servicePayload(draft, mode))
    if (result && alive.current) { onDraft(undefined); (onStarted || onClose)() }
  }
  return <Modal title={source ? s(source.intent === 'copy' ? 'copyTitle' : 'editTitle') : t(mode === 'connect' ? 'connectService' : 'shareService')} onClose={onClose} t={t} wide>
    <p className="muted">{source ? s('reviewHint') : t(mode === 'connect' ? 'serviceIntro' : 'shareIntro')}</p>
    {error && <ErrorBanner message={error} detail={!validation ? errorDetail(server.error, t) : undefined} t={t} />}
    {source && (loading || !loaded) ? <><p role="status">{loading ? t('loading') : t('invalid_response')}</p>{!loading && <Button onClick={reset}>{t('retry')}</Button>}</> : <form className="form-stack" onSubmit={submit}>
      {mode === 'connect' && (available.length > 0 || serviceId) && <label className="field">{t('availableServices')}<select value={serviceId} onChange={event => { const id = event.target.value; const service = available.find(item => item.id === id); change({ serviceId: id, ...(service ? { ports: service.ports || String(service.remotePort || ''), protocol: service.network, ...(!draft.nameEdited ? { name: uniqueServiceName(service.name, state) } : {}), exclusions: '' } : {}) }) }}><option value="">{t('manualPorts')}</option>{serviceMissing && <option value={serviceId} disabled>{t('serviceUnavailableChoice')}</option>}{available.map(item => <option key={item.id} value={item.id}>{item.name} · {item.network.toUpperCase()} {item.ports || item.remotePort}</option>)}</select></label>}
      {serviceMissing && <p className="field-error" role="status">{t('serviceUnavailable')}</p>}
      {mode === 'share' ? <fieldset className="field"><legend>{t('selectedDevices')}</legend><div className="peer-checkboxes">{state.peers.filter(item => item.networks.includes(scopeNetwork)).map(item => <label key={item.id}><input type="checkbox" checked={peers.includes(item.id)} onChange={event => change({ peers: event.target.checked ? [...peers, item.id] : peers.filter(id => id !== item.id) })} /><span>{item.name}</span><small>{t(item.online ? 'online' : 'offline')}</small></label>)}{peers.filter(id => !state.peers.some(item => item.id === id && item.networks.includes(scopeNetwork))).map(id => <label key={id}><input type="checkbox" checked onChange={() => change({ peers: peers.filter(value => value !== id) })} /><span>{state.peers.find(item => item.id === id)?.name || id}</span><small>{t('unavailable')}</small></label>)}</div></fieldset> : <div className="target-pill"><Icon name="monitor" /><strong>{chosen.join(', ')}</strong><Badge>{t(scopeNetwork)}</Badge></div>}
      <label className="field">{t('ruleName')}<input value={name} onChange={event => change({ name: event.target.value, nameEdited: true })} required maxLength={64} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}_]|-){0,63}" title={t('nameHint')} placeholder={t('ruleNamePlaceholder')} /><small className="muted">{s('nameSuggested')}</small></label>
      {issue && <div className="field-error" role="status">{s(issue)}{issue === 'service_name_conflict' && <Button type="button" onClick={() => change({ name: uniqueServiceName(name, state), nameEdited: false })}>{s('useUniqueName')}</Button>}{issue === 'service_revision_conflict' && <Button type="button" onClick={reset}>{s('reloadSaved')}</Button>}</div>}
      {draft.source && !draft.source.backend && <label className="checkbox-field"><input type="checkbox" checked={draft.legacyReviewed} onChange={event => change({ legacyReviewed: event.target.checked })} /><span>{s('reviewLegacy')} · {t(scopeNetwork)}</span></label>}
      <div className="form-row"><label className="field grow">{t('ports')}<input value={ports} onChange={event => change({ ports: event.target.value })} placeholder={t('portsPlaceholder')} required inputMode="text" autoComplete="off" spellCheck={false} disabled={Boolean(serviceId)} aria-invalid={Boolean(preview.error)} aria-describedby="port-help" /></label><label className="field protocol-field">{t('protocol')}<select value={protocol} disabled={Boolean(serviceId)} onChange={event => change({ protocol: event.target.value as 'tcp' | 'udp' })}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label></div>
      {preview.error && <p id="port-help" className="field-error">{preview.error}</p>}
      <div className="form-row">{mode === 'connect' && <label className="field grow">{t('localStart')}<input value={localPort} onChange={event => change({ localPort: event.target.value })} placeholder={t('samePorts')} inputMode="numeric" /></label>}<label className="field grow">{t('expiry')}<select value={ttl} onChange={event => change({ ttl: Number(event.target.value) })}>{![900, 3600, 14400, 86400].includes(ttl) && <option value={ttl}>{ttl} {s('customLifetime')}</option>}<option value={900}>{t('minutes15')}</option><option value={3600}>{t('hour1')}</option><option value={14400}>{t('hours4')}</option><option value={86400}>{t('hours24')}</option></select></label></div>
      {mode === 'share' && <p className="small muted">{t('samePortShare')}</p>}
      <details className="advanced"><summary>{t('advanced')}</summary><div className="form-stack"><label className="field">{t('excludePorts')}<input value={exclusions} disabled={Boolean(serviceId)} onChange={event => change({ exclusions: event.target.value })} placeholder="22, 54543" spellCheck={false} /></label><label className="field">{t('purpose')}<select value={purpose} onChange={event => change({ purpose: event.target.value })}>{!['generic', 'web', 'ssh', 'desktop'].includes(purpose) && <option value={purpose}>{purpose}</option>}{(['generic', 'web', 'ssh', 'desktop'] as const).map(value => <option key={value} value={value}>{t(value)}</option>)}</select></label>{mode === 'share' && <p className="small muted">{t('reservedPorts')}</p>}{mode === 'share' && <label className="checkbox-field"><input type="checkbox" checked={discovery} onChange={event => change({ discovery: event.target.checked })} /><span>{t('discoverable')}<small>{t('discoverableHint')}</small></span></label>}</div></details>
      <div className="service-preview"><div className="section-label"><Icon name="shield" />{t('previewScope')}</div><dl><dt>{t('selectedDevices')}</dt><dd>{chosen.join(', ') || '—'}</dd><dt>{t('networks')}</dt><dd>{t(scopeNetwork)} · {protocol.toUpperCase()}</dd><dt>{t('scope')}</dt><dd>{t(mode === 'connect' ? 'loopbackOnly' : 'selectedPeersOnly')}</dd><dt>{t('mapping')}</dt><dd><span className="code-value">127.0.0.1:{preview.value?.localPorts || '…'}</span><Icon name="arrow" size={14} /><span className="code-value">{mode === 'connect' ? chosen.join(', ') : t(scopeNetwork)}:{preview.value?.ports || '…'}</span></dd><dt>{t('expiry')}</dt><dd>{[900, 3600, 14400, 86400].includes(ttl) ? t(ttl === 900 ? 'minutes15' : ttl === 3600 ? 'hour1' : ttl === 14400 ? 'hours4' : 'hours24') : `${ttl} ${s('customLifetime')}`}</dd><dt>{s('exclusions')}</dt><dd className="code-value">{exclusions || '—'}</dd><dt>{s('purpose')}</dt><dd>{['generic', 'web', 'ssh', 'desktop'].includes(purpose) ? t(purpose as 'generic') : purpose}</dd>{mode === 'share' && <><dt>{s('discovery')}</dt><dd>{t(discovery ? 'enabled' : 'off')}</dd></>}</dl>{mode === 'connect' && preview.value && <><Badge>{preview.value.count} {t('listeners')}</Badge>{preview.value.mappings.length > 1 && <div><p className="small muted">{s('exactMappings')}</p>{preview.value.mappings.map(mapping => <p key={mapping.remote} className="code-value">127.0.0.1:{mapping.local} → {chosen.join(', ')}:{mapping.remote}</p>)}</div>}</>}<p className="small muted">{t('appUnverified')}</p>{mode === 'share' && <p className="small muted">{t('reservedPorts')}</p>}</div>
      {mode === 'connect' && scopeNetwork === 'tailnet' && !peer.bridge && <p className="inline-note"><Icon name="info" />{t('ordinaryConnection')}</p>}
      <div className="modal-actions"><Button type="button" variant="ghost" onClick={reset}>{s(source ? 'reloadSaved' : 'newDraft')}</Button><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button variant="primary" type="submit" busy={server.busy.has(`service.${mode}`)} disabled={!preview.value || Boolean(issue) || serviceMissing || server.stale}>{source?.intent === 'edit' ? s('applyStart') : t(mode === 'connect' ? 'startConnection' : 'startSharing')}<Icon name="arrow" /></Button></div>
    </form>}
  </Modal>
}
