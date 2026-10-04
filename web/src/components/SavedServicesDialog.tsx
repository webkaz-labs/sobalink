import { StartupProfilesDialog } from './StartupProfilesDialog'
import { startupText } from '../startup-i18n'
import { ClientSettingsDialog, RustDeskSetupDialog } from './ClientHelpers'
import { clientText } from '../client-i18n'
import { serviceText } from '../service-i18n'
import { MAX_SERVICE_TTL_SECONDS } from '../service-form'
import { SavedDefinitionEditor } from './SavedDefinitionEditor'
import { RemoveDefinitionDialog } from './DefinitionDialogs'
import { ServiceOwnership } from './LifecycleControls'
import { savedEditorText } from '../saved-editor-i18n'
import type { SavedServiceAction, ServiceMode } from '../service-form'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import type { DefinitionBundle, DefinitionExport, DefinitionImport, Locale, ServiceConfiguration, ServiceSelection, ServiceLifetime } from '../api'
import { definitionText } from '../definitions-i18n'
import { errorDetail, errorText, type Translate } from '../i18n'
import { previewPorts } from '../ports'
import { loopbackEndpoint, validServiceName } from '../service-form'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Modal, useAlive } from './ui'

export function readDefinitionBundle(value: unknown): DefinitionBundle {
  const raw = value as DefinitionBundle
  const bundle = raw && { ...raw, services: raw.services === null ? [] : raw.services, groups: raw.groups === null ? [] : raw.groups }
  if (bundle?.version !== 1 || !Array.isArray(bundle.services) || !Array.isArray(bundle.groups)) throw new Error('invalidBundle')
  if (bundle.services.some(service => typeof service.id !== 'string' || !service.id || typeof service.name !== 'string' || !['share', 'forward'].includes(service.direction) || !['', 'lan', 'tailnet'].includes(service.backend || '') || !['tcp', 'udp'].includes(service.network) || typeof service.ports !== 'string' || service.ttlSeconds !== undefined && !Number.isSafeInteger(service.ttlSeconds) || service.peerIds !== undefined && (!Array.isArray(service.peerIds) || service.peerIds.some(id => typeof id !== 'string')))) throw new Error('invalidBundle')
  if ((bundle.groups || []).some(group => typeof group.name !== 'string' || !Array.isArray(group.serviceIds) || group.serviceIds.some(id => typeof id !== 'string'))) throw new Error('invalidBundle')
  return bundle
}
function readExport(value: unknown): DefinitionExport {
  const data = value as DefinitionExport
  if (!data || data.disabled !== true || !/^[0-9a-f]{64}$/.test(data.revision)) throw new Error('invalid_response')
  return { ...data, profile: readDefinitionBundle(data.profile) }
}
export function SavedServicesDialog({ server, locale, t, onClose }: { server: Server; locale: Locale; t: Translate; onClose: () => void }) {
  const d = (key: string) => definitionText(locale, key)
  const e = (key: string) => savedEditorText(locale, key)
  const c = (key: string) => clientText(locale, key)
  const [startupOpen, setStartupOpen] = useState(false)
  const [helper, setHelper] = useState<'rustdesk' | { ids?: string[]; group?: string }>()
  const [pendingGroup, setPendingGroup] = useState('')
  const [lifetimeChoice, setLifetimeChoice] = useState('saved')
  const [runtimeSeconds, setRuntimeSeconds] = useState(3600)
  const [editor, setEditor] = useState<{ mode: ServiceMode; source?: SavedServiceAction }>()
  const [removing, setRemoving] = useState<{ id: string; mode: ServiceMode }>()
  const alive = useAlive()
  const [bundle, setBundle] = useState<DefinitionExport>()
  const [selected, setSelected] = useState<string[]>([])
  const [group, setGroup] = useState('')
  const [groupName, setGroupName] = useState('')
  const [replaceGroup, setReplaceGroup] = useState(false)
  const [review, setReview] = useState<{ action: 'start' | 'stop'; target: { ids?: string[]; group?: string }; value: ServiceSelection }>()
  const [incoming, setIncoming] = useState<DefinitionBundle>()
  const [imported, setImported] = useState<DefinitionImport>()
  const [status, setStatus] = useState('')
  const [validation, setValidation] = useState('')
  const [reload, setReload] = useState(0)
  const reading = useRef(crypto.randomUUID())
  const fileGeneration = useRef(0)
  const importInput = useRef<HTMLInputElement>(null)
  useEffect(() => {
    let cancelled = false
    setBundle(undefined); setReview(undefined); setImported(undefined); setReplaceGroup(false)
    void Promise.resolve().then(() => cancelled ? undefined : server.run('profile.export', {}, `definitions:${reading.current}:${reload}`)).then(result => {
      if (cancelled || !result) return
      try { const next = readExport(result.result); setBundle(next); setSelected(current => pendingGroup ? next.profile.groups?.find(item => item.name === pendingGroup)?.serviceIds || [] : current.filter(id => next.profile.services.some(service => service.id === id))); if (pendingGroup) { setGroup(pendingGroup); setGroupName(pendingGroup); setPendingGroup('') } } catch { server.setError({ code: 'invalid_response' }) }
    })
    return () => { cancelled = true }
  }, [server.run, reload])
  const services = bundle?.profile.services || []
  const groups = bundle?.profile.groups || []
  const working = [...server.busy].some(key => /^(definitions:|profile\.|group\.|services\.|service\.selection)/.test(key))
  const blocked = server.stale || working
  const active = [...(server.state?.services || []), ...(server.state?.shares || [])].some(service => ['active', 'reconnecting'].includes(service.status))
  const updateSelection = (ids: string[], nextGroup = '') => { setSelected(ids); setGroup(nextGroup); setReview(undefined); setLifetimeChoice('saved'); setStatus(''); setReplaceGroup(false) }
  const peerName = (id: string) => server.state?.peers.find(peer => peer.id === id)?.name || id
  const mappings = (service: ServiceConfiguration) => {
    try {
      const value = previewPorts(service.ports, service.excludePorts || '', service.localPort ? String(service.localPort) : '', service.direction === 'forward' ? 'connect' : 'share', server.state?.reservedPorts, { protocol: service.network, maxListeners: 65535 })
      return value.mappings.map(mapping => <span className="definition-mapping" key={mapping.remote}>{service.direction === 'forward' ? `${loopbackEndpoint(service.loopbackHost || '127.0.0.1', mapping.local)} → ${mapping.remote}` : `${mapping.remote} → ${loopbackEndpoint(service.loopbackHost || '127.0.0.1', mapping.local)}`}</span>)
    } catch { return <span>{d('mappingInvalid')}</span> }
  }
  const scope = (service: ServiceConfiguration) => <article className="definition-scope" key={service.id}><div className="flex justify-between gap-2"><strong>{service.name}</strong><Badge>{d(service.direction)}</Badge></div><dl><div><dt>{d('backend')}</dt><dd>{service.backend || t('unknown')} · {service.network.toUpperCase()}</dd></div><div><dt>{d('peers')}</dt><dd>{(service.peerIds || (service.peerId ? [service.peerId] : [])).map(peerName).join(', ')}</dd></div><div><dt>{t('ports')}</dt><dd className="code-value">{service.ports}</dd></div>{service.excludePorts && <div><dt>{t('excludePorts')}</dt><dd className="code-value">{service.excludePorts}</dd></div>}<div><dt>{t('mapping')}</dt><dd className="code-value">{mappings(service)}</dd></div><div><dt>{d('lifetime')}</dt><dd>{service.lifetime === 'until-stopped' ? d('untilStopped') : service.lifetime === 'until-revoked' ? d('untilRevoked') : `${(service.ttlSeconds || 0).toLocaleString(locale)} ${d('seconds')}`}</dd></div>{service.serviceId && <div><dt>{d('remoteReference')}</dt><dd className="code-value">{service.serviceId}</dd></div>}{service.direction === 'share' && <div><dt>{t('discoverable')}</dt><dd>{t(service.discoverable ? 'enabled' : 'off')}</dd></div>}</dl></article>
  const reviewSelection = async (action: 'start' | 'stop') => {
    if (!selected.length || blocked) return
    setReview(undefined); setValidation(''); setStatus('')
    const target = group ? { group } : { ids: [...selected] }
    const result = await server.run('service.selection', target)
    if (!alive.current || !result) return
    const value = result.result as unknown as ServiceSelection
    try {
      readDefinitionBundle({ version: 1, services: value?.services, groups: [] })
      if (!/^[0-9a-f]{64}$/.test(value.revision) || !Array.isArray(value.states) || typeof value.ready !== 'boolean') throw new Error('invalid_response')
      setLifetimeChoice('saved'); setRuntimeSeconds(3600); setReview({ action, target, value })
    } catch { server.setError({ code: 'invalid_response' }) }
  }
  const runtimeLifetime: { lifetime?: ServiceLifetime; ttlSeconds?: number } = lifetimeChoice === 'saved' ? {} : lifetimeChoice === '3600' || lifetimeChoice === 'custom' ? { lifetime: 'finite', ttlSeconds: lifetimeChoice === '3600' ? 3600 : runtimeSeconds } : { lifetime: lifetimeChoice as ServiceLifetime, ttlSeconds: 0 }
  const runtimeValid = lifetimeChoice !== 'custom' || Number.isSafeInteger(runtimeSeconds) && runtimeSeconds > 0 && runtimeSeconds <= MAX_SERVICE_TTL_SECONDS
  const activeLifetimeConflict = review?.action === 'start' && review.value.services.some(service => { const state = review.value.states.find(item => item.id === service.id); return state && ['active', 'reconnecting'].includes(state.status) && state.lifetime && (state.lifetime !== (runtimeLifetime.lifetime || service.lifetime || 'finite') || state.ttlSeconds !== (runtimeLifetime.ttlSeconds ?? service.ttlSeconds)) })
  const scopeForReview = (service: ServiceConfiguration) => {
    if (review?.action === 'start') return { ...service, ...runtimeLifetime }
    const runtime = review?.value.states.find(state => state.id === service.id)
    return runtime?.lifetime ? { ...service, lifetime: runtime.lifetime, ttlSeconds: runtime.ttlSeconds ?? service.ttlSeconds } : service
  }
  const applySelection = async () => {
    if (!review || blocked || !runtimeValid || activeLifetimeConflict) return
    const checked = review; setReview(undefined)
    const result = await server.run(checked.action === 'start' ? 'services.start' : 'services.stop', { ...checked.target, expectedRevision: checked.value.revision, ...(checked.action === 'start' ? runtimeLifetime : {}) })
    if (alive.current && result) {
      const value = result.result as unknown as ServiceSelection
      if (!Array.isArray(value?.states) || (checked.action === 'start' ? value.ready !== true : value.ready !== false)) { server.setError({ code: 'invalid_response' }); return }
      setStatus(checked.action === 'start' ? 'started' : 'stopped'); setReload(value => value + 1)
    }
  }
  const saveGroup = async (event: FormEvent) => {
    event.preventDefault()
    const name = groupName.trim()
    if (!bundle || blocked || groups.some(item => item.name === name && item.rustdesk) || (server.error as { code?: string })?.code === 'group_revision_conflict' || !selected.length || !validServiceName(name) || groups.some(item => item.name === name) && !replaceGroup) return
    const result = await server.run('group.save', { group: { name, serviceIds: [...selected] }, expectedRevision: bundle.revision })
    if (result && alive.current) { if (result.result?.active !== false) { server.setError({ code: 'invalid_response' }); return }; setStatus('groupSaved'); setReplaceGroup(false); setReload(value => value + 1) }
  }
  const download = async () => {
    const result = await server.run('profile.export', {})
    if (!alive.current || !result) return
    try {
      const value = readExport(result.result)
      const url = URL.createObjectURL(new Blob([JSON.stringify(value.profile, null, 2) + '\n'], { type: 'application/json' }))
      const anchor = document.createElement('a'); anchor.href = url; anchor.download = 'sobalink-services.json'; document.body.append(anchor); anchor.click(); anchor.remove(); setTimeout(() => URL.revokeObjectURL(url), 0)
    } catch { server.setError({ code: 'invalid_response' }) }
  }
  const chooseFile = async (file?: File) => {
    const generation = ++fileGeneration.current
    setIncoming(undefined); setImported(undefined); setValidation(''); setStatus('')
    if (!file) return
    const budget = server.state?.limits?.effective.resources.profileBytes
    const maximum = budget?.mode === 'limited' && Number.isSafeInteger(budget.value) && budget.value! > 0 ? budget.value! : 4 * 1024 * 1024
    if (file.size > maximum) { setValidation('fileTooLarge'); return }
    try {
      const parsed = readDefinitionBundle(JSON.parse(await file.text()))
      if (alive.current && generation === fileGeneration.current) setIncoming(parsed)
    } catch { if (alive.current && generation === fileGeneration.current) setValidation('invalidBundle') }
  }
  const reviewImport = async () => {
    if (!incoming || blocked) return
    setImported(undefined); setStatus('')
    const result = await server.run('profile.import.preview', { profile: incoming })
    if (!alive.current || !result) return
    try {
      const value = readExport(result.result) as DefinitionImport
      if (value.removesRustDeskMetadata !== undefined && (!Array.isArray(value.removesRustDeskMetadata) || value.removesRustDeskMetadata.some(name => typeof name !== 'string')) || value.preservesIdentity !== true || !Number.isSafeInteger(value.replacesServices) || value.replacesServices < 0) throw new Error('invalid_response')
      setImported(value)
    } catch { server.setError({ code: 'invalid_response' }) }
  }
  const applyImport = async () => {
    if (!imported || !incoming || blocked || active) return
    const checked = imported; setImported(undefined)
    const result = await server.run('profile.import', { profile: incoming, expectedRevision: checked.revision })
    if (result && alive.current) { if (result.result?.applied !== true) { server.setError({ code: 'invalid_response' }); return }; setStatus('importDone'); setIncoming(undefined); if (importInput.current) importInput.current.value = ''; updateSelection([]); setReload(value => value + 1); setStatus('importDone') }
  }
  const openEditor = (mode: ServiceMode, source?: SavedServiceAction) => { server.setError(null); setReview(undefined); setEditor({ mode, source }) }
  if (startupOpen) return <StartupProfilesDialog server={server} locale={locale} t={t} target={group ? { group } : { ids: selected }} selected={services.filter(service => selected.includes(service.id))} onClose={() => setStartupOpen(false)} />
  if (helper === 'rustdesk') return <RustDeskSetupDialog server={server} locale={locale} t={t} onClose={() => { setHelper(undefined); setReload(value => value + 1) }} onSaved={name => { setHelper(undefined); setPendingGroup(name); setReload(value => value + 1) }} />
  if (helper) return <ClientSettingsDialog target={helper} server={server} locale={locale} t={t} onClose={() => setHelper(undefined)} />
  if (editor) return <SavedDefinitionEditor {...editor} services={services} server={server} locale={locale} t={t} onClose={() => setEditor(undefined)} onSaved={() => { setEditor(undefined); setStatus('savedDefinition'); setReload(value => value + 1) }} />
  if (removing) return <RemoveDefinitionDialog {...removing} server={server} locale={locale} t={t} onClose={() => { setRemoving(undefined); setReload(value => value + 1) }} />
  const code = (server.error as { code?: string })?.code || ''
  return <Modal title={d('title')} onClose={onClose} t={t} wide><p className="muted">{d('intro')}</p>{server.error != null && <ErrorBanner message={d(code) || serviceText(locale, code) || c(code) || errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}{validation && <ErrorBanner message={d(validation)} t={t} />}{status && <p role="status" className="scope-note">{d(status)}</p>}
    {!bundle ? <p role="status">{working ? t('loading') : t('unavailable')}</p> : <><div className="definition-toolbar"><Button type="button" disabled={blocked} onClick={() => openEditor('connect')}>{e('createForward')}</Button><Button type="button" disabled={blocked} onClick={() => openEditor('share')}>{e('createShare')}</Button>{groups.length > 0 && <label className="field grow">{d('group')}<select value={group} disabled={blocked} onChange={event => { const name = event.target.value; updateSelection(groups.find(item => item.name === name)?.serviceIds || [], name); setGroupName(name) }}><option value="">{d('manual')}</option>{groups.map(item => <option key={item.name} value={item.name}>{item.name}</option>)}</select></label>}<Button type="button" disabled={blocked || !services.length} onClick={() => updateSelection(services.map(service => service.id))}>{d('selectAll')}</Button><Button type="button" disabled={blocked || !selected.length} onClick={() => updateSelection([])}>{d('clear')}</Button></div>
      <details className="advanced"><summary>{c('advanced')}</summary><div className="subsection form-stack"><Button disabled={blocked} onClick={() => { server.setError(null); setReview(undefined); setStartupOpen(true) }}>{startupText(locale, 'open')}</Button><Button disabled={blocked} onClick={() => { server.setError(null); setReview(undefined); setHelper('rustdesk') }}>{c('setup')}</Button>{selected.length > 0 && <Button disabled={blocked} onClick={() => { server.setError(null); setReview(undefined); setHelper(group ? { group } : { ids: [...selected] }) }}>{c('groupSettings')}</Button>}</div></details>
      {!services.length ? <p className="muted">{d('empty')}</p> : <fieldset className="definition-list"><legend>{d('selected')} ({selected.length} / {services.length})</legend>{services.map(service => <div className="definition-entry" key={service.id}><label><input type="checkbox" disabled={blocked} checked={selected.includes(service.id)} onChange={event => updateSelection(event.target.checked ? [...selected, service.id] : selected.filter(id => id !== service.id))} /><span><strong>{service.name}</strong><small>{d(service.direction)} · {service.network.toUpperCase()} {service.ports}</small></span><Badge>{t([...server.state!.services, ...server.state!.shares].find(item => item.id === service.id)?.status || 'saved')}</Badge></label><ServiceOwnership service={[...server.state!.services, ...server.state!.shares].find(item => item.id === service.id) || {}} locale={locale} /><details className="definition-actions"><summary>{t('serviceActions')}</summary><div className="flex flex-wrap gap-2"><Button type="button" variant="ghost" disabled={blocked} onClick={() => { server.setError(null); setReview(undefined); setHelper({ ids: [service.id] }) }}>{c('settings')}</Button><Button type="button" variant="ghost" disabled={blocked} onClick={() => openEditor(service.direction === 'share' ? 'share' : 'connect', { id: service.id, intent: 'edit' })}>{e('edit')}</Button><Button type="button" variant="ghost" disabled={blocked} onClick={() => openEditor(service.direction === 'share' ? 'share' : 'connect', { id: service.id, intent: 'copy' })}>{e('copy')}</Button><Button type="button" variant="ghost" disabled={blocked} onClick={() => { server.setError(null); setRemoving({ id: service.id, mode: service.direction === 'share' ? 'share' : 'connect' }) }}>{e('remove')}</Button></div></details></div>)}</fieldset>}
      {services.length > 0 && <><div className="modal-actions"><Button type="button" disabled={blocked || !selected.length} onClick={() => reviewSelection('stop')}>{d('reviewStop')}</Button><Button type="button" variant="primary" disabled={blocked || !selected.length} onClick={() => reviewSelection('start')}>{d('reviewStart')}</Button></div>{review && <section className="policy-review" aria-label={d('scope')}><h3>{d('scope')}</h3><p>{d(review.action === 'start' ? 'startHint' : 'stopHint')}</p>{review.action === 'stop' && server.state?.startup?.entries.some(entry => entry.enabled && entry.services.some(item => review.value.services.some(service => service.id === item.id))) && <p className="scope-note">{startupText(locale, 'stopStartupHint')}</p>}{review.action === 'start' && <div className="form-stack"><label className="field">{d('runtimeLifetime')}<select aria-label={d('runtimeLifetime')} value={lifetimeChoice} disabled={blocked} onChange={event => setLifetimeChoice(event.target.value)}><option value="saved">{d('useSavedLifetime')}</option><option value="3600">{d('oneHour')}</option><option value="custom">{d('customLifetime')}</option>{review.value.services.every(service => service.direction === 'forward') && <option value="until-stopped">{d('untilStopped')}</option>}{review.value.services.every(service => service.direction === 'share') && <option value="until-revoked">{d('untilRevoked')}</option>}</select></label>{lifetimeChoice === 'custom' && <label className="field">{d('durationSeconds')}<input type="number" min={1} max={MAX_SERVICE_TTL_SECONDS} step={1} required value={runtimeSeconds || ''} disabled={blocked} onChange={event => setRuntimeSeconds(Number(event.target.value))} /></label>}<p className="scope-note">{d('runtimeHint')}</p>{!runtimeValid && <p className="field-error">{serviceText(locale, 'invalidLifetime')}</p>}{activeLifetimeConflict && <p className="field-error">{serviceText(locale, 'service_lifetime_conflict')}</p>}</div>}{review.value.services.map(service => <div key={service.id}>{scope(scopeForReview(service))}<p>{t(review.value.states.find(item => item.id === service.id)?.status || 'saved')}</p>{review.value.states.find(item => item.id === service.id)?.expiresAt && <p>{t('expiresAt')}: {review.value.states.find(item => item.id === service.id)!.expiresAt}</p>}<ServiceOwnership service={review.value.states.find(item => item.id === service.id) || {}} locale={locale} /></div>)}<p className="small muted">{d('readiness')}</p><div className="modal-actions"><Button type="button" onClick={() => setReview(undefined)}>{t('cancel')}</Button><Button type="button" variant={review.action === 'start' ? 'primary' : 'danger'} disabled={blocked || !runtimeValid || Boolean(activeLifetimeConflict) || review.value.states.some(state => Boolean(state.owner))} onClick={applySelection}>{d(review.action)}</Button></div></section>}
      <details className="advanced"><summary>{d('groupEditor')}</summary><form className="form-stack subsection" onSubmit={saveGroup}><p className="small muted">{d('groupHint')}</p>{groups.some(item => item.name === groupName.trim() && item.rustdesk) && <p className="scope-note">{c('protectedGroup')}</p>}<label className="field">{d('groupName')}<input value={groupName} disabled={blocked} maxLength={64} required pattern="[\p{L}\p{N}](?:[\p{L}\p{N}_]|-){0,63}" onChange={event => { setGroupName(event.target.value); setReplaceGroup(false) }} /></label>{groups.some(item => item.name === groupName.trim()) && <label className="checkbox-field"><input type="checkbox" disabled={blocked} checked={replaceGroup} onChange={event => setReplaceGroup(event.target.checked)} /><span>{d('replaceGroup')}</span></label>}<Button type="submit" disabled={blocked || groups.some(item => item.name === groupName.trim() && item.rustdesk) || code === 'group_revision_conflict' || !selected.length || !validServiceName(groupName.trim()) || groups.some(item => item.name === groupName.trim()) && !replaceGroup}>{d('saveGroup')}</Button></form></details></>}
      <details className="advanced definition-portable"><summary>{d('portable')}</summary><p className="small muted">{d('exportHint')}</p><Button type="button" disabled={blocked} onClick={download}>{d('export')}</Button><p className="small muted">{d('importHint')}</p><label className="field">{d('importFile')}<input ref={importInput} type="file" accept=".json,application/json" disabled={blocked} onChange={event => void chooseFile(event.target.files?.[0])} /></label>{incoming && <Button type="button" disabled={blocked} onClick={reviewImport}>{d('importReview')}</Button>}{imported && <section className="policy-review" aria-label={d('importTitle')}><h3>{d('importTitle')}</h3><dl><div><dt>{d('currentCount')}</dt><dd>{imported.replacesServices}</dd></div><div><dt>{d('incomingCount')}</dt><dd>{imported.profile.services.length}</dd></div><div><dt>{d('incomingGroups')}</dt><dd>{imported.profile.groups?.length || 0}</dd></div></dl>{imported.profile.services.map(scope)}{Boolean(imported.profile.groups?.length) && <ul>{imported.profile.groups!.map(group => <li key={group.name}><strong>{group.name}</strong>: {group.serviceIds.map(id => imported.profile.services.find(service => service.id === id)?.name || id).join(', ')}</li>)}</ul>}{Boolean(imported.removesRustDeskMetadata?.length) && <div className="scope-note"><p>{d('removesRustDeskMetadata')}</p><ul>{imported.removesRustDeskMetadata!.map(name => <li key={name}>{name}</li>)}</ul></div>}{active && <p className="field-error">{d('stopBeforeImport')}</p>}<div className="modal-actions"><Button type="button" onClick={() => setImported(undefined)}>{t('cancel')}</Button><Button type="button" variant="danger" disabled={blocked || active} onClick={applyImport}>{d('importApply')}</Button></div></section>}</details>
    </>}<div className="modal-actions"><Button type="button" disabled={blocked} onClick={() => { setValidation(''); setReload(value => value + 1) }}>{d('reload')}</Button><Button type="button" onClick={onClose}>{t('close')}</Button></div>
  </Modal>
}
