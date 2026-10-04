import { DiscoveryObservation } from './DiscoveryObservation'
import { ReceiveRecoveryNotice } from './ReceiveRecovery'
import { recoveryText } from '../receive-recovery-i18n'
import { receivingBlocked } from '../api'
import { LogoutControl } from './LifecycleControls'
import { useEffect, useMemo, useRef, useState, type Dispatch, type FormEvent, type SetStateAction } from 'react'
import { type Locale, type Peer, type State, type Theme, type ServiceConfigResult, type DiscoveryRefresh } from '../api'
import { errorText, errorDetail, networkLabel, type Translate } from '../i18n'
import { previewPorts } from '../ports'
import { advertisedDraft, matchesAdvertisedDraft, freshAdvertisedDraft, draftFromConfig, newServiceDraft, readServiceConfig, remainingListeners, loopbackEndpoint, MAX_SERVICE_TTL_SECONDS, serviceDraftIssue, servicePayload, serviceDefinitionPayload, uniqueServiceName, type SavedServiceAction, type ServiceDraft } from '../service-form'
import { serviceText } from '../service-i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Icon, Modal, useAlive } from './ui'
import { LanSetup, StopApplication, type LanDraft } from './LanSetup'
import { TailnetLogin } from './TailnetLogin'
import { lanTranslator } from '../lan-i18n'

interface Base { t: Translate; onClose: () => void; server: Server }
interface DirectoryDraft { directoryDraft?: string; onDirectoryDraft: (value: string | undefined) => void }
export function Preferences({ t, onClose, locale, theme, setLocale, setTheme, server, directoryDraft, onDirectoryDraft, onPolicy, policyLabel, onAdvancedConnections, advancedConnectionsLabel, onStartup, startupLabel, onStopSharing, stopSharingLabel, onReviewReceiving, recoveryLocale }: Base & DirectoryDraft & { onReviewReceiving: () => void; recoveryLocale: Locale; onPolicy: () => void; policyLabel: string; onAdvancedConnections: () => void; advancedConnectionsLabel: string; onStartup: () => void; startupLabel: string; onStopSharing: () => void; stopSharingLabel: string; locale: 'auto' | Locale; theme: Theme; setLocale: (value: 'auto' | Locale) => void; setTheme: (value: Theme) => void }) {
  const alive = useAlive()
  const directory = directoryDraft ?? server.state?.settings?.receiveDirectory ?? server.state?.self.receiveDirectory ?? ''
  const [saved, setSaved] = useState(false)
  const save = async (event: FormEvent) => { event.preventDefault(); if (await server.run('settings.update', { locale, theme, receiveDirectory: directory.trim() }) && alive.current) { onDirectoryDraft(undefined); setSaved(true) } }
  return <Modal title={t('settings')} t={t} onClose={onClose}><p className="muted">{t('preferencesHint')}</p>
    {server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}
    <fieldset className="form-section settings-section"><legend>{t('displayPreferences')}</legend><label className="field">{t('language')}<select value={locale} onChange={event => setLocale(event.target.value as 'auto' | Locale)}><option value="auto">{t('automatic')}</option><option value="ja">日本語</option><option value="en">English</option></select></label>
    <fieldset className="field"><legend>{t('appearance')}</legend><div className="segmented theme-options">{(['system', 'light', 'dark'] as const).map(value => <button key={value} type="button" aria-pressed={theme === value} onClick={() => setTheme(value)}><Icon name={value === 'system' ? 'monitor' : value === 'light' ? 'sun' : 'moon'} />{t(value)}</button>)}</div></fieldset></fieldset>
    {server.auth === 'ready' && <form className="form-stack" onSubmit={save}><fieldset className="form-section settings-section"><legend>{t('receivingPreferences')}</legend>{server.state && <ReceiveRecoveryNotice state={server.state} locale={recoveryLocale} onReview={onReviewReceiving} />}{!receivingBlocked(server.state || {}) && <Button type="button" onClick={onReviewReceiving}>{recoveryText(recoveryLocale, 'open')}</Button>}<label className="field">{t('receiveDirectory')}<input value={directory} onChange={event => { onDirectoryDraft(event.target.value); setSaved(false) }} placeholder={t('directoryPlaceholder')} spellCheck={false} autoComplete="off" /></label><div className="modal-actions">{saved && <span className="small muted" role="status">{t('settingsSaved')}</span>}<Button type="submit" variant="primary" busy={server.busy.has('settings.update')}>{t('save')}</Button></div></fieldset></form>}
    {server.auth === 'ready' && <fieldset className="form-section settings-section settings-links"><legend>{t('managementPreferences')}</legend><Button className="full-width" onClick={onPolicy}>{policyLabel}<Icon name="chevron" /></Button><Button className="full-width" onClick={onAdvancedConnections}>{advancedConnectionsLabel}<Icon name="chevron" /></Button><Button className="full-width" onClick={onStartup}>{startupLabel}<Icon name="chevron" /></Button>{server.state?.shares.some(service => ['active', 'reconnecting'].includes(service.status)) && <Button className="full-width" variant="danger" onClick={onStopSharing}>{stopSharingLabel}</Button>}</fieldset>}
  </Modal>
}
export function NetworkDialog({ t, onClose, server, locale, lanDraft, setLanDraft, onViewPeer }: Base & { locale: Locale; lanDraft: LanDraft; setLanDraft: Dispatch<SetStateAction<LanDraft>>; onViewPeer: (id: string) => void }) {
  const lt = lanTranslator(locale)
  const [hostname, updateHostname] = useState(lanDraft.hostname ?? server.state?.self.name ?? 'sobalink')
  const setHostname = (value: string) => { updateHostname(value); setLanDraft(current => ({ ...current, hostname: value })) }
  const [networkStopping, setNetworkStopping] = useState(false)
  const blocked = networkStopping || server.auth !== 'ready' || server.stale || server.busy.has('application.stop')
  const [mode, setMode] = useState<'tailnet' | 'lan'>(server.state?.settings?.network === 'lan' ? 'lan' : 'tailnet')
  const activeMode = server.state?.settings?.network
  const engineActive = !['idle', 'offline', 'none', 'stopped', ''].includes((server.state?.self.status || '').toLowerCase())
  const switchingActive = Boolean(engineActive && activeMode && activeMode !== 'none' && activeMode !== mode)
  const configure = async (event: FormEvent) => { event.preventDefault(); if (blocked) return; await server.run('network.configure', { mode, hostname: hostname.trim() }) }
  return <Modal title={t('chooseNetwork')} t={t} onClose={onClose}><p className="muted">{t('networkHint')}</p>
    {server.state && <div className="network-current"><span>{t(server.state.settings?.network === 'lan' ? 'lan' : server.state.settings?.network === 'tailnet' ? 'tailnet' : 'disabledNetwork')}</span><Badge>{networkLabel(server.state.self.status, t)}</Badge>{server.state.self.error && <div className="error-copy field-error"><p>{server.state.self.errorCode ? errorText({ code: server.state.self.errorCode }, t) : t('networkProblem')}</p><details><summary>{t('technicalDetails')}</summary><p>{server.state.self.error}</p></details></div>}</div>}
    {server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}
      <div className="network-options">{(['tailnet', 'lan'] as const).map(value => <label key={value} className={`network-option ${mode === value ? 'selected' : ''}`}><input type="radio" name="network" disabled={blocked} value={value} checked={mode === value} onChange={() => setMode(value)} /><Icon name={value === 'tailnet' ? 'globe' : 'wifi'} size={23} /><span><strong>{t(value)}</strong><small>{t(value === 'tailnet' ? 'tailnetHint' : 'lanHint')}</small></span></label>)}</div>
    {switchingActive && <p className="scope-note network-restart">{t('networkSwitchRestart')}</p>}
    {mode === 'tailnet' && !switchingActive && <form onSubmit={configure} className="form-stack subsection">
      <label className="field">{t('deviceName')}<input value={hostname} onChange={event => setHostname(event.target.value)} maxLength={63} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}]|-){0,62}" title={t('hostnameHint')} autoComplete="off" required /><small className="muted">{t('hostnameHint')}</small></label>
      <div className="modal-actions"><Button onClick={onClose} type="button">{t('close')}</Button><Button type="submit" variant="primary" disabled={blocked} busy={server.busy.has('network.configure')}>{t('activate')}<Icon name="arrow" /></Button></div>
    </form>}
    {mode === 'lan' && !switchingActive && server.state && <LanSetup server={server} state={server.state} t={t} locale={locale} hostname={hostname} setHostname={setHostname} draft={lanDraft} setDraft={setLanDraft} onViewPeer={onViewPeer} showStopControl={false} applicationStopping={networkStopping} />}
    {mode === 'tailnet' && !switchingActive && <TailnetLogin server={server} locale={locale} t={t} disabled={blocked || activeMode !== 'tailnet' || !engineActive} />}
    {server.state && <div className="network-restart"><LogoutControl server={server} locale={locale} t={t} blocked={blocked} onStopping={setNetworkStopping} />{activeMode && activeMode !== 'none' && <p className="small muted">{t('networkRestart')}</p>}<StopApplication server={server} state={server.state} t={t} locale={locale} blocked={blocked} onStopping={() => setNetworkStopping(true)} />{networkStopping && <p className="scope-note" role="status">{lt('stopping')}</p>}</div>}
  </Modal>
}
export function AutosaveDialog({ t, onClose, server, peer, locale, edit = false, directoryDraft, onDirectoryDraft }: Base & DirectoryDraft & { peer: Peer; locale: Locale; edit?: boolean }) {
  const alive = useAlive()
  const directory = directoryDraft ?? (peer.autosave?.directory || server.state?.settings?.receiveDirectory || server.state?.self.receiveDirectory || '')
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
  const [applying, setApplying] = useState(false)
  const [mutating, setMutating] = useState(false)
  const applyPending = useRef(false)
  const formVersion = useRef(0)
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
  const { name, ports, exclusions, localPort, protocol, ttl, lifetime, loopbackHost, purpose, discovery, peers, serviceId } = draft
  const change = (patch: Partial<ServiceDraft>) => { ++formVersion.current; onDraft({ ...draft, ...patch }); setValidation('') }
  const [customDuration, setCustomDuration] = useState(false)
  const listenerBudget = remainingListeners(state)
  const available = (state.availableServices || []).filter(item => item.peerId === peers[0])
  const scopeNetwork = draft.backend
  const currentAdvertisement = available.find(item => item.id === serviceId)
  const savedReview = source?.intent === 'edit' && Boolean(draft.serviceRevision)
  const serviceMissing = Boolean(serviceId && !savedReview && !matchesAdvertisedDraft(draft, currentAdvertisement))
  const serviceNeedsReview = Boolean(serviceId && !savedReview && !draft.serviceCheckedAt)
  const serviceChanged = Boolean(serviceId && !savedReview && currentAdvertisement && !matchesAdvertisedDraft(draft, currentAdvertisement))
  const preview = useMemo(() => { try { return { value: previewPorts(ports, exclusions, localPort, mode, state.reservedPorts, { protocol, maxListeners: 65535 }), error: '' } } catch (error) { const code = (error as { code?: string }).code || ''; return { value: null, error: ports ? serviceText(locale, code) || errorText(error, t) : '' } } }, [ports, exclusions, localPort, mode, t, locale, state.reservedPorts, protocol, listenerBudget])
  const exceedsListeners = Boolean(preview.value && (mode === 'connect' || protocol === 'udp') && preview.value.count > (listenerBudget ?? 64))
  const localIssue = serviceDraftIssue(draft, state, mode, loaded)
  const chosen = peers.map(id => state.peers.find(item => item.id === id)?.name || id)
  const serverCode = (server.error as { code?: string } | null)?.code
  const discoveryConflict = serverCode === 'discovery_review_changed'
  const issue = localIssue || (source && ['service_revision_conflict', 'service_active', 'service_backend_mismatch'].includes(serverCode || '') ? serverCode! : null)
  const error = validation ? s(validation) || errorText({ code: validation }, t) : server.error != null ? s(serverCode || '') || errorText(server.error, t) : ''
  const reset = () => { ++formVersion.current; onDraft(undefined); setCustomDuration(false); setValidation(''); server.setError(null); if (source) setReload(value => value + 1) }
  const apply = async (saveOnly = false) => {
    if (applyPending.current || source && (!loaded || loading)) return
    if (issue) { setValidation(issue); return }
    if (!saveOnly && (serviceChanged || serviceNeedsReview || discoveryConflict)) { setValidation('discovery_review_changed'); return }
    if (!preview.value || server.stale || (!saveOnly && exceedsListeners)) return
    const version = formVersion.current
    applyPending.current = true; setApplying(true)
    try {
      let submission = draft
      if (!saveOnly && mode === 'connect' && serviceId && !savedReview) {
        const response = await server.run('discovery.refresh', { peerId: peers[0] }, `discovery:${peers[0]}`)
        if (!alive.current || version !== formVersion.current || !response) return
        const refreshed = response.result as unknown as DiscoveryRefresh
        if (!Array.isArray(refreshed?.services) || !Array.isArray(refreshed.observations)) { setValidation('invalid_response'); return }
        const advertised = refreshed.services.find(service => service.id === serviceId && service.peerId === peers[0])
        if (!advertised) {
          const observation = refreshed.observations.find(item => item.peerId === peers[0])
          setValidation(observation?.code && ['discovery_network_unavailable', 'discovery_unsupported', 'discovery_capacity'].includes(observation.code) ? observation.code : 'discovery_review_changed'); return
        }
        const candidate = { ...draft, ...advertisedDraft(advertised) }
        if (!matchesAdvertisedDraft(draft, advertised) || !freshAdvertisedDraft(candidate)) { setValidation('discovery_review_changed'); return }
        if (latest.current.state.settings?.network !== draft.backend) { setValidation('service_backend_mismatch'); return }
        submission = candidate
      }
      if (!alive.current || version !== formVersion.current) return
      setMutating(true)
      const result = saveOnly ? await server.run('service.save', serviceDefinitionPayload(submission, mode)) : await server.run(mode === 'connect' ? 'service.connect' : 'service.share', servicePayload(submission, mode))
      if (result && alive.current) { onDraft(undefined); (onStarted || onClose)() }
    } finally { applyPending.current = false; if (alive.current) { setApplying(false); setMutating(false) } }
  }
  const submit = async (event: FormEvent) => { event.preventDefault(); await apply() }
  const lifetimeLabel = lifetime === 'finite' ? ([900, 3600, 14400, 86400].includes(ttl) ? t(ttl === 900 ? 'minutes15' : ttl === 3600 ? 'hour1' : ttl === 14400 ? 'hours4' : 'hours24') : `${ttl} ${s('customLifetime')}`) : s(lifetime === 'until-stopped' ? 'untilStopped' : 'untilRevoked')
  return <Modal title={source ? s(source.intent === 'copy' ? 'copyTitle' : 'editTitle') : t(mode === 'connect' ? 'connectService' : 'shareService')} onClose={onClose} t={t} wide>
    <p className="muted">{source ? s('reviewHint') : t(mode === 'connect' ? 'serviceIntro' : 'shareIntro')}</p>{mode === 'connect' && <DiscoveryObservation peer={peer} locale={locale} t={t} server={server} />}
    {error && <ErrorBanner message={error} detail={!validation ? errorDetail(server.error, t) : undefined} t={t} />}
    {source && (loading || !loaded) ? <><p role="status">{loading ? t('loading') : t('invalid_response')}</p>{!loading && <Button onClick={reset}>{t('retry')}</Button>}</> : <form className="form-stack" onSubmit={submit}><fieldset className="form-stack service-inputs" disabled={mutating}>
      <fieldset className="form-section"><legend>{s(mode === 'connect' ? 'connectionTarget' : 'sharingAccess')}</legend>
      {mode === 'connect' && (available.length > 0 || serviceId) && <label className="field">{t('availableServices')}<select value={serviceId} onChange={event => { const id = event.target.value; const service = available.find(item => item.id === id); server.setError(null); change({ ...advertisedDraft(service), ...(service && !draft.nameEdited ? { name: uniqueServiceName(service.name, state) } : {}) }) }}><option value="">{t('manualPorts')}</option>{serviceMissing && <option value={serviceId} disabled>{t('serviceUnavailableChoice')}</option>}{available.map(item => <option key={item.id} value={item.id}>{item.name} · {item.network.toUpperCase()} {item.ports || item.remotePort}</option>)}</select></label>}
      {serviceMissing && <p className="field-error" role="status">{s('advertisedUnavailable')}</p>}{serviceId && <div className="scope-note advertised-review"><p>{s(savedReview ? 'advertisedSaved' : 'advertisedStale')}</p><p>{s('purpose')}: {purpose}</p><p>{s('advertisedChecked')}: {draft.serviceCheckedAt || currentAdvertisement?.checkedAt || t('unknown')}</p><p>{s('advertisedExpiry')}: {draft.serviceExpiresAt || currentAdvertisement?.expiresAt || (draft.serviceLifetime === 'until-revoked' || currentAdvertisement?.lifetime === 'until-revoked' ? s('untilRevoked') : t('unknown'))}</p>{(!savedReview || discoveryConflict) && <Button type="button" disabled={!currentAdvertisement || server.stale} onClick={() => { server.setError(null); change(advertisedDraft(currentAdvertisement)) }}>{s('advertisedReview')}</Button>}</div>}
      {mode === 'share' ? <fieldset className="field"><legend>{t('selectedDevices')}</legend><div className="peer-checkboxes">{state.peers.filter(item => item.networks.includes(scopeNetwork)).map(item => <label key={item.id}><input type="checkbox" checked={peers.includes(item.id)} onChange={event => change({ peers: event.target.checked ? [...peers, item.id] : peers.filter(id => id !== item.id) })} /><span>{item.name}</span><small>{t(item.online ? 'online' : 'offline')}</small></label>)}{peers.filter(id => !state.peers.some(item => item.id === id && item.networks.includes(scopeNetwork))).map(id => <label key={id}><input type="checkbox" checked onChange={() => change({ peers: peers.filter(value => value !== id) })} /><span>{state.peers.find(item => item.id === id)?.name || id}</span><small>{t('unavailable')}</small></label>)}</div></fieldset> : <div className="target-pill"><Icon name="monitor" /><strong>{chosen.join(', ')}</strong><Badge>{t(scopeNetwork)}</Badge></div>}
      </fieldset>
      <fieldset className="form-section"><legend>{s('serviceSettings')}</legend>
      {Boolean(state.servicePresets?.length) && <label className="field">{s('examples')}<select value="" disabled={Boolean(serviceId)} onChange={event => { const preset = state.servicePresets?.find(item => item.id === event.target.value); if (preset) change({ ports: String(preset.port), protocol: preset.network, localPort: mode === 'connect' ? String(preset.localPort) : '', exclusions: mode === 'share' ? '54543-54545' : '', purpose: preset.purpose }) }}><option value="">{s('chooseExample')}</option>{state.servicePresets!.map(item => <option key={item.id} value={item.id}>{item.label[locale]} · {item.network.toUpperCase()} {item.port}</option>)}</select><small className="muted">{s('exampleHint')}</small></label>}
      <label className="field">{t('ruleName')}<input value={name} onChange={event => change({ name: event.target.value, nameEdited: true })} required maxLength={64} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}_]|-){0,63}" title={t('nameHint')} placeholder={t('ruleNamePlaceholder')} /><small className="muted">{s('nameSuggested')}</small></label>
      {issue && <div className="field-error" role="status">{s(issue)}{issue === 'service_name_conflict' && <Button type="button" onClick={() => change({ name: uniqueServiceName(name, state), nameEdited: false })}>{s('useUniqueName')}</Button>}{issue === 'service_revision_conflict' && <Button type="button" onClick={reset}>{s('reloadSaved')}</Button>}</div>}
      {draft.source && !draft.source.backend && <label className="checkbox-field"><input type="checkbox" checked={draft.legacyReviewed} onChange={event => change({ legacyReviewed: event.target.checked })} /><span>{s('reviewLegacy')} · {t(scopeNetwork)}</span></label>}
      <div className="form-row"><label className="field grow">{t('ports')}<input value={ports} onChange={event => change({ ports: event.target.value })} placeholder={t('portsPlaceholder')} required inputMode="text" autoComplete="off" spellCheck={false} disabled={Boolean(serviceId)} aria-invalid={Boolean(preview.error)} aria-describedby="port-help" /></label><label className="field protocol-field">{t('protocol')}<select value={protocol} disabled={Boolean(serviceId)} onChange={event => change({ protocol: event.target.value as 'tcp' | 'udp' })}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label></div>
      {(preview.error || exceedsListeners) && <p id="port-help" className="field-error">{preview.error || s('too_many_ports')}</p>}
      <div className="form-row service-mapping-row"><label className="field grow">{mode === 'connect' ? t('localStart') : s('shareLocalPort')}<input value={localPort} onChange={event => change({ localPort: event.target.value })} placeholder={t('samePorts')} inputMode="numeric" /></label><label className="field grow">{s('lifetime')}<select value={lifetime === 'finite' ? (customDuration || ![900, 3600, 14400, 86400].includes(ttl) ? 'custom' : String(ttl)) : lifetime} onChange={event => { const value = event.target.value; if (value === 'until-stopped' || value === 'until-revoked') { setCustomDuration(false); change({ lifetime: value }) } else { setCustomDuration(value === 'custom'); change({ lifetime: 'finite', ...(value !== 'custom' ? { ttl: Number(value) } : {}) }) } }}><option value={mode === 'connect' ? 'until-stopped' : 'until-revoked'}>{s(mode === 'connect' ? 'untilStopped' : 'untilRevoked')}</option><option value="900">{t('minutes15')}</option><option value="3600">{t('hour1')}</option><option value="14400">{t('hours4')}</option><option value="86400">{t('hours24')}</option><option value="custom">{s('customDuration')}</option></select></label></div>
      {lifetime === 'finite' && (customDuration || ![900, 3600, 14400, 86400].includes(ttl)) && <label className="field">{s('durationSeconds')}<input type="number" min="1" max={MAX_SERVICE_TTL_SECONDS} step="1" required value={Number.isFinite(ttl) ? ttl : ''} onChange={event => change({ ttl: event.target.value === '' ? NaN : Number(event.target.value) })} /></label>}
      <p className="small muted">{s(lifetime === 'finite' ? 'finiteHint' : lifetime === 'until-stopped' ? 'untilStoppedHint' : 'untilRevokedHint')}</p>
      {mode === 'share' && <p className="small muted">{s('shareMappingHint')}</p>}
      </fieldset>
      <details className="advanced"><summary>{t('advanced')}</summary><div className="form-stack"><label className="field">{s('loopbackHost')}<select value={loopbackHost} onChange={event => change({ loopbackHost: event.target.value as '127.0.0.1' | '::1' })}><option value="127.0.0.1">127.0.0.1 (IPv4)</option><option value="::1">::1 (IPv6)</option></select></label><label className="field">{t('excludePorts')}<input value={exclusions} disabled={Boolean(serviceId)} onChange={event => change({ exclusions: event.target.value })} placeholder="22, 54543" spellCheck={false} /></label><label className="field">{t('purpose')}<select value={purpose} disabled={Boolean(serviceId)} onChange={event => change({ purpose: event.target.value })}>{!['generic', 'web', 'ssh', 'desktop'].includes(purpose) && <option value={purpose}>{purpose}</option>}{(['generic', 'web', 'ssh', 'desktop'] as const).map(value => <option key={value} value={value}>{t(value)}</option>)}</select></label>{mode === 'share' && <p className="small muted">{t('reservedPorts')}</p>}{mode === 'share' && <label className="checkbox-field"><input type="checkbox" checked={discovery} onChange={event => change({ discovery: event.target.checked })} /><span>{t('discoverable')}<small>{t('discoverableHint')}</small></span></label>}</div></details>
      <div className="service-preview" role="group" aria-label={t('previewScope')}><div className="section-label"><Icon name="shield" />{t('previewScope')}</div><dl><dt>{t('selectedDevices')}</dt><dd>{chosen.join(', ') || '—'}</dd><dt>{t('networks')}</dt><dd>{t(scopeNetwork)} · {protocol.toUpperCase()}</dd><dt>{t('scope')}</dt><dd>{t(mode === 'connect' ? 'loopbackOnly' : 'selectedPeersOnly')}</dd><dt>{t('mapping')}</dt><dd><span className="code-value">{mode === 'connect' ? loopbackEndpoint(loopbackHost, preview.value?.localPorts || '…') : `${t(scopeNetwork)}:${preview.value?.ports || '…'}`}</span><Icon name="arrow" size={14} /><span className="code-value">{mode === 'connect' ? `${chosen.join(', ')}:${preview.value?.ports || '…'}` : loopbackEndpoint(loopbackHost, preview.value?.localPorts || '…')}</span></dd><dt>{s('lifetime')}</dt><dd>{lifetimeLabel}</dd><dt>{s('exclusions')}</dt><dd className="code-value">{exclusions || '—'}</dd><dt>{s('purpose')}</dt><dd>{['generic', 'web', 'ssh', 'desktop'].includes(purpose) ? t(purpose as 'generic') : purpose}</dd>{mode === 'share' && <><dt>{s('discovery')}</dt><dd>{t(discovery ? 'enabled' : 'off')}</dd></>}</dl>{mode === 'connect' && preview.value && <><Badge>{preview.value.count} {t('listeners')}</Badge>{preview.value.mappings.length > 1 && <div><p className="small muted">{s('exactMappings')}</p>{preview.value.mappings.map(mapping => <p key={mapping.remote} className="code-value">{loopbackEndpoint(loopbackHost, mapping.local)} → {chosen.join(', ')}:{mapping.remote}</p>)}</div>}</>}<p className="small muted">{t('appUnverified')}</p><p className="small muted">{listenerBudget === undefined ? s('capacityUnknown') : `${s('capacity')}: ${listenerBudget}`}</p><p className="small muted">{s('capacityHint')}</p>{mode === 'share' && <p className="small muted">{t('reservedPorts')}</p>}</div>
      {mode === 'connect' && scopeNetwork === 'tailnet' && !peer.bridge && <p className="inline-note"><Icon name="info" />{t('ordinaryConnection')}</p>}
      </fieldset>{mutating && <p role="status" className="scope-note">{s('serviceSubmitting')}</p>}<p className="small muted">{s('saveOnlyHint')}</p><div className="modal-actions"><Button type="button" variant="ghost" disabled={mutating} onClick={reset}>{s(source ? 'reloadSaved' : 'newDraft')}</Button><Button type="button" onClick={() => apply(true)} busy={server.busy.has('service.save')} disabled={applying || !preview.value || Boolean(issue) || server.stale || server.busy.has(`service.${mode}`)}>{s('saveOnly')}</Button><Button type="button" onClick={onClose}>{t(mutating ? 'close' : 'cancel')}</Button><Button variant="primary" type="submit" busy={applying || server.busy.has(`service.${mode}`)} disabled={!preview.value || Boolean(issue) || serviceChanged || serviceNeedsReview || discoveryConflict || exceedsListeners || server.stale || server.busy.has('service.save')}>{source?.intent === 'edit' ? s('applyStart') : t(mode === 'connect' ? 'startConnection' : 'startSharing')}<Icon name="arrow" /></Button></div>
    </form>}
  </Modal>
}
