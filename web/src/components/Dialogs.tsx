import { useMemo, useState, type FormEvent } from 'react'
import { safeAuthURL, type Locale, type Peer, type State, type Theme } from '../api'
import { errorText, networkLabel, type Translate } from '../i18n'
import { previewPorts } from '../ports'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Icon, Modal } from './ui'

interface Base { t: Translate; onClose: () => void; server: Server }
export function Preferences({ t, onClose, locale, theme, setLocale, setTheme, server }: Base & { locale: 'auto' | Locale; theme: Theme; setLocale: (value: 'auto' | Locale) => void; setTheme: (value: Theme) => void }) {
  const [directory, setDirectory] = useState(server.state?.settings?.receiveDirectory || server.state?.self.receiveDirectory || '')
  const [saved, setSaved] = useState(false)
  const save = async (event: FormEvent) => { event.preventDefault(); if (await server.run('settings.update', { locale, theme, receiveDirectory: directory.trim() })) setSaved(true) }
  return <Modal title={t('settings')} t={t} onClose={onClose}><p className="muted">{t('preferencesHint')}</p>
    {server.error != null && <ErrorBanner message={errorText(server.error, t)} t={t} />}
    <label className="field">{t('language')}<select value={locale} onChange={event => setLocale(event.target.value as 'auto' | Locale)}><option value="auto">{t('automatic')}</option><option value="ja">日本語</option><option value="en">English</option></select></label>
    <fieldset className="field"><legend>{t('appearance')}</legend><div className="segmented theme-options">{(['system', 'light', 'dark'] as const).map(value => <button key={value} type="button" aria-pressed={theme === value} onClick={() => setTheme(value)}><Icon name={value === 'system' ? 'monitor' : value === 'light' ? 'sun' : 'moon'} />{t(value)}</button>)}</div></fieldset>
    {server.auth === 'ready' && <form className="form-stack subsection" onSubmit={save}><label className="field">{t('receiveDirectory')}<input value={directory} onChange={event => { setDirectory(event.target.value); setSaved(false) }} placeholder={t('directoryPlaceholder')} spellCheck={false} autoComplete="off" /></label><div className="modal-actions">{saved && <span className="small muted" role="status">{t('settingsSaved')}</span>}<Button type="submit" variant="primary" busy={server.busy.has('settings.update')}>{t('save')}</Button></div></form>}
  </Modal>
}
export function NetworkDialog({ t, onClose, server }: Base) {
  const [hostname, setHostname] = useState(server.state?.self.name || 'sobalink')
  const [mode, setMode] = useState<'tailnet' | 'lan'>('tailnet')
  const [authUrl, setAuthUrl] = useState('')
  const configure = async (event: FormEvent) => { event.preventDefault(); await server.run('network.configure', { mode, hostname: hostname.trim() }) }
  const signIn = async () => {
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
    {server.state && <p className="network-current"><Badge>{networkLabel(server.state.self.status, t)}</Badge>{server.state.self.error && <span className="field-error">{server.state.self.error}</span>}</p>}
    {server.error != null && <ErrorBanner message={errorText(server.error, t)} t={t} />}
    <form onSubmit={configure} className="form-stack">
      <div className="network-options">{(['tailnet', 'lan'] as const).map(value => <label key={value} className={`network-option ${mode === value ? 'selected' : ''}`}><input type="radio" name="network" value={value} checked={mode === value} onChange={() => { setMode(value); setAuthUrl('') }} /><Icon name={value === 'tailnet' ? 'globe' : 'wifi'} size={23} /><span><strong>{t(value)}</strong><small>{t(value === 'tailnet' ? 'tailnetHint' : 'lanHint')}</small></span></label>)}</div>
      <label className="field">{t('deviceName')}<input value={hostname} onChange={event => setHostname(event.target.value)} maxLength={63} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}]|-){0,62}" title={t('hostnameHint')} autoComplete="off" required /><small className="muted">{t('hostnameHint')}</small></label>
      <div className="modal-actions"><Button onClick={onClose} type="button">{t('close')}</Button><Button type="submit" variant="primary" busy={server.busy.has('network.configure')}>{t('activate')}<Icon name="arrow" /></Button></div>
    </form>
    {mode === 'tailnet' && <div className="subsection"><Button onClick={signIn} busy={server.busy.has('network.login')}><Icon name="globe" />{t('signInTailscale')}</Button>{authUrl && <div className="signin-link"><p className="muted">{t('signInLinkHint')}</p><a href={authUrl} target="_blank" rel="noreferrer noopener" className="button button-primary">{t('continueSignIn')}<Icon name="arrow" /></a></div>}</div>}
    {server.state?.settings?.network && server.state.settings.network !== 'none' && <Button className="network-disconnect" variant="ghost" busy={server.busy.has('network.configure')} onClick={() => server.run('network.configure', { mode: 'none' })}>{t('disconnect')}</Button>}
  </Modal>
}
export function AutosaveDialog({ t, onClose, server, peer }: Base & { peer: Peer }) {
  const [directory, setDirectory] = useState(peer.autosave?.directory || server.state?.settings?.receiveDirectory || server.state?.self.receiveDirectory || '')
  const [validation, setValidation] = useState('')
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!directory.trim()) { setValidation(t('directoryRequired')); return }
    const result = await server.run('peer.autosave', { peerId: peer.id, enabled: true, paused: false, directory: directory.trim() }, `autosave:${peer.id}`)
    if (result) onClose()
  }
  return <Modal title={t('autosave')} onClose={onClose} t={t}><div className="target-pill"><Icon name="monitor" /><strong>{peer.name}</strong><Badge tone="green">{t('trusted')}</Badge></div><p className="muted">{t('autosaveHint')}</p>
    {(validation || server.error != null) && <ErrorBanner message={validation || errorText(server.error, t)} t={t} />}
    <form onSubmit={submit} className="form-stack"><label className="field">{t('receiveDirectory')}<input value={directory} onChange={event => { setDirectory(event.target.value); setValidation('') }} placeholder={t('directoryPlaceholder')} autoComplete="off" spellCheck={false} required /></label><div className="scope-note"><Icon name="shield" />{t('autosaveScope')}</div><div className="modal-actions"><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button variant="primary" type="submit" busy={server.busy.has(`autosave:${peer.id}`)} disabled={!peer.trusted || !peer.verified || !peer.bridge}>{t('enableAutosave')}</Button></div></form>
  </Modal>
}
export function ServiceDialog({ t, onClose, onStarted, server, peer, mode, state }: Base & { peer: Peer; mode: 'connect' | 'share'; state: State; onStarted?: () => void }) {
  const [name, setName] = useState('')
  const [ports, setPorts] = useState('')
  const [exclusions, setExclusions] = useState(mode === 'share' ? '54543-54545' : '')
  const [localPort, setLocalPort] = useState('')
  const [protocol, setProtocol] = useState<'tcp' | 'udp'>('tcp')
  const [ttl, setTtl] = useState(3600)
  const [purpose, setPurpose] = useState('generic')
  const [discovery, setDiscovery] = useState(false)
  const [peers, setPeers] = useState<string[]>([peer.id])
  const [validation, setValidation] = useState('')
  const [serviceId, setServiceId] = useState('')
  const available = (state.availableServices || []).filter(item => item.peerId === peer.id)
  const serviceMissing = Boolean(serviceId && !available.some(item => item.id === serviceId))
  const preview = useMemo(() => { try { return { value: previewPorts(ports, exclusions, localPort, mode, state.reservedPorts), error: '' } } catch (error) { return { value: null, error: ports ? errorText(error, t) : '' } } }, [ports, exclusions, localPort, mode, t, state.reservedPorts])
  const chosen = state.peers.filter(item => peers.includes(item.id))
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (serviceMissing) { setValidation(t('serviceUnavailable')); return }
    if (!preview.value) { setValidation(preview.error || t('empty_ports')); return }
    if (!peers.length) { setValidation(t('choosePeers')); return }
    const result = await server.run(mode === 'connect' ? 'service.connect' : 'service.share', {
      name: name.trim(), ...(mode === 'connect' ? { peerId: peer.id, serviceId: serviceId || undefined } : { peerIds: peers }), network: protocol,
      ports, excludePorts: exclusions || undefined, localPort: mode === 'connect' ? preview.value.localPort : undefined,
      ttlSeconds: ttl, purpose, discoverable: mode === 'share' && discovery,
    })
    if (result) (onStarted || onClose)()
  }
  return <Modal title={t(mode === 'connect' ? 'connectService' : 'shareService')} onClose={onClose} t={t} wide>
    <p className="muted">{t(mode === 'connect' ? 'serviceIntro' : 'shareIntro')}</p>
    {(validation || server.error != null) && <ErrorBanner message={validation || errorText(server.error, t)} t={t} />}
    <form className="form-stack" onSubmit={submit}>
      {mode === 'connect' && (available.length > 0 || serviceId) && <label className="field">{t('availableServices')}<select value={serviceId} onChange={event => { const id = event.target.value; setServiceId(id); setValidation(''); const service = available.find(item => item.id === id); if (service) { setPorts(service.ports || String(service.remotePort || '')); setProtocol(service.network); setName(service.name.replace(/[^a-zA-Z0-9_-]+/g, '-').slice(0, 64)); setExclusions('') } }}><option value="">{t('manualPorts')}</option>{serviceMissing && <option value={serviceId} disabled>{t('serviceUnavailableChoice')}</option>}{available.map(item => <option key={item.id} value={item.id}>{item.name} · {item.network.toUpperCase()} {item.ports || item.remotePort}</option>)}</select></label>}
      {serviceMissing && <p className="field-error" role="status">{t('serviceUnavailable')}</p>}
      {mode === 'share' ? <fieldset className="field"><legend>{t('selectedDevices')}</legend><div className="peer-checkboxes">{state.peers.filter(item => item.networks.includes('tailnet')).map(item => <label key={item.id}><input type="checkbox" checked={peers.includes(item.id)} onChange={event => setPeers(current => event.target.checked ? [...current, item.id] : current.filter(id => id !== item.id))} /><span>{item.name}</span><small>{t(item.online ? 'online' : 'offline')}</small></label>)}</div></fieldset> : <div className="target-pill"><Icon name="monitor" /><strong>{peer.name}</strong><Badge>{t('tailnet')}</Badge></div>}
      <label className="field">{t('ruleName')}<input value={name} onChange={event => setName(event.target.value)} required maxLength={64} pattern="[\p{L}\p{N}](?:[\p{L}\p{N}_]|-){0,63}" title={t('nameHint')} placeholder={t('ruleNamePlaceholder')} /><small className="muted">{t('nameHint')}</small></label>
      <div className="form-row"><label className="field grow">{t('ports')}<input value={ports} onChange={event => { setPorts(event.target.value); setValidation('') }} placeholder={t('portsPlaceholder')} required inputMode="text" autoComplete="off" spellCheck={false} disabled={Boolean(serviceId)} aria-invalid={Boolean(preview.error)} aria-describedby="port-help" /></label><label className="field protocol-field">{t('protocol')}<select value={protocol} disabled={Boolean(serviceId)} onChange={event => setProtocol(event.target.value as 'tcp' | 'udp')}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label></div>
      {preview.error && <p id="port-help" className="field-error">{preview.error}</p>}
      <div className="form-row">{mode === 'connect' && <label className="field grow">{t('localStart')}<input value={localPort} onChange={event => setLocalPort(event.target.value)} placeholder={t('samePorts')} inputMode="numeric" /></label>}<label className="field grow">{t('expiry')}<select value={ttl} onChange={event => setTtl(Number(event.target.value))}><option value={900}>{t('minutes15')}</option><option value={3600}>{t('hour1')}</option><option value={14400}>{t('hours4')}</option><option value={86400}>{t('hours24')}</option></select></label></div>
      {mode === 'share' && <p className="small muted">{t('samePortShare')}</p>}
      <details className="advanced"><summary>{t('advanced')}</summary><div className="form-stack"><label className="field">{t('excludePorts')}<input value={exclusions} onChange={event => setExclusions(event.target.value)} placeholder="22, 54543" spellCheck={false} /></label><label className="field">{t('purpose')}<select value={purpose} onChange={event => setPurpose(event.target.value)}>{(['generic', 'web', 'ssh', 'desktop'] as const).map(value => <option key={value} value={value}>{t(value)}</option>)}</select></label>{mode === 'share' && <p className="small muted">{t('reservedPorts')}</p>}{mode === 'share' && <label className="checkbox-field"><input type="checkbox" checked={discovery} onChange={event => setDiscovery(event.target.checked)} /><span>{t('discoverable')}<small>{t('discoverableHint')}</small></span></label>}</div></details>
      <div className="service-preview"><div className="section-label"><Icon name="shield" />{t('previewScope')}</div><dl><dt>{t('selectedDevices')}</dt><dd>{mode === 'connect' ? peer.name : chosen.map(item => item.name).join(', ') || '—'}</dd><dt>{t('networks')}</dt><dd>Tailnet · {protocol.toUpperCase()}</dd><dt>{t('scope')}</dt><dd>{t(mode === 'connect' ? 'loopbackOnly' : 'selectedPeersOnly')}</dd><dt>{t('mapping')}</dt><dd><span className="code-value">127.0.0.1:{preview.value?.localPorts || '…'}</span><Icon name="arrow" size={14} /><span className="code-value">{mode === 'connect' ? peer.name : 'Tailnet'}:{preview.value?.ports || '…'}</span></dd><dt>{t('expiry')}</dt><dd>{t(ttl === 900 ? 'minutes15' : ttl === 3600 ? 'hour1' : ttl === 14400 ? 'hours4' : 'hours24')}</dd></dl>{mode === 'connect' && preview.value && <Badge>{preview.value.count} {t('listeners')}</Badge>}<p className="small muted">{t('appUnverified')}</p>{mode === 'share' && <p className="small muted">{t('reservedPorts')}</p>}</div>
      {mode === 'connect' && !peer.bridge && <p className="inline-note"><Icon name="info" />{t('ordinaryConnection')}</p>}
      <div className="modal-actions"><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button variant="primary" type="submit" busy={server.busy.has(`service.${mode}`)} disabled={!preview.value || !peers.length || !name.trim() || serviceMissing}>{t(mode === 'connect' ? 'startConnection' : 'startSharing')}<Icon name="arrow" /></Button></div>
    </form>
  </Modal>
}
