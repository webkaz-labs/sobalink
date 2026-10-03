import { ServiceOwnership } from './LifecycleControls'
import { useEffect, useRef, useState } from 'react'
import type { GroupList, Locale, ServiceConfigResult } from '../api'
import { errorDetail, errorText, type Translate } from '../i18n'
import { readServiceConfig, type ServiceMode } from '../service-form'
import { serviceText } from '../service-i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, Icon, Modal, useAlive } from './ui'

export function RemoveDefinitionDialog({ id, mode, server, locale, t, onClose }: { id: string; mode: ServiceMode; server: Server; locale: Locale; t: Translate; onClose: () => void }) {
  const s = (key: string) => serviceText(locale, key)
  const alive = useAlive()
  const [record, setRecord] = useState<ServiceConfigResult>()
  const [groups, setGroups] = useState<GroupList>()
  const [stop, setStop] = useState(false)
  const [removeGroups, setRemoveGroups] = useState(false)
  const [reload, setReload] = useState(0)
  const readID = useRef(crypto.randomUUID())
  useEffect(() => {
    let cancelled = false
    setRecord(undefined); setGroups(undefined); setStop(false); setRemoveGroups(false)
    void Promise.resolve().then(async () => {
      if (cancelled) return
      const loaded = await server.run('service.config', { id }, `remove.config:${readID.current}:${reload}`)
      if (cancelled || !loaded) return
      const listed = await server.run('group.list', {}, `remove.groups:${readID.current}:${reload}`)
      if (cancelled || !listed) return
      try {
        const record = readServiceConfig(loaded.result, id, mode)
        const list = listed.result as unknown as GroupList
        if (!/^[0-9a-f]{64}$/.test(list?.revision || '') || list.groups !== null && (!Array.isArray(list.groups) || list.groups.some(group => typeof group.name !== 'string' || !Array.isArray(group.serviceIds) || group.serviceIds.some(value => typeof value !== 'string')))) throw new Error('invalid_response')
        setRecord(record); setGroups({ ...list, groups: list.groups || [] })
      } catch { server.setError({ code: 'invalid_response' }) }
    })
    return () => { cancelled = true }
  }, [id, mode, reload, server.run])
  const affectedGroups = (groups?.groups || []).filter(group => group.serviceIds.includes(id))
  const active = Boolean(record?.active || [...(server.state?.services || []), ...(server.state?.shares || [])].some(service => service.id === id && ['active', 'reconnecting'].includes(service.status)))
  const runtime = [...(server.state?.services || []), ...(server.state?.shares || [])].find(service => service.id === id)
  const code = (server.error as { code?: string })?.code || ''
  const conflict = ['service_revision_conflict', 'profile_revision_conflict', 'service_active', 'service_in_group', 'service_not_found'].includes(code)
  const busy = server.busy.has(`remove:${id}`)
  const remove = async () => {
    if (!record || !groups || runtime?.owner || conflict || server.stale || busy || active && !stop || affectedGroups.length > 0 && !removeGroups) return
    const result = await server.run('service.delete', { id, expectedRevision: record.revision, expectedProfileRevision: groups.revision, stopActive: stop, removeFromGroups: removeGroups }, `remove:${id}`)
    if (result && alive.current) onClose()
  }
  return <Modal title={s('removeTitle')} t={t} onClose={onClose}><p className="muted">{s('removeHint')}</p>{server.error != null && <ErrorBanner message={s(code) || errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}{record && groups ? <><div className="target-pill"><Icon name="link" /><strong>{record.configuration.name}</strong><Badge>{t(active ? 'active' : 'saved')}</Badge></div><ServiceOwnership service={runtime || {}} locale={locale} /><p className="code-value">{record.configuration.network.toUpperCase()} {record.configuration.ports}</p>{active && <label className="checkbox-field"><input type="checkbox" checked={stop} disabled={busy} onChange={event => setStop(event.target.checked)} /><span>{s('stopActive')}</span></label>}{affectedGroups.length > 0 && <><h3>{s('affectedGroups')}</h3><ul>{affectedGroups.map(group => <li key={group.name}>{group.name}</li>)}</ul><label className="checkbox-field"><input type="checkbox" checked={removeGroups} disabled={busy} onChange={event => setRemoveGroups(event.target.checked)} /><span>{s('removeGroups')}</span></label></>}<div className="modal-actions"><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button type="button" variant="danger" onClick={remove} busy={busy} disabled={server.stale || Boolean(runtime?.owner) || conflict || active && !stop || affectedGroups.length > 0 && !removeGroups}>{s('removeConfirm')}</Button></div></> : <p role="status">{t('loading')}</p>}{(conflict || !record && server.error != null) && <Button type="button" onClick={() => setReload(value => value + 1)}>{s('reloadSaved')}</Button>}</Modal>
}

export function StopSharesDialog({ server, locale, t, onClose }: { server: Server; locale: Locale; t: Translate; onClose: () => void }) {
  const s = (key: string) => serviceText(locale, key)
  const alive = useAlive()
  const active = (server.state?.shares || []).filter(service => ['active', 'reconnecting'].includes(service.status))
  const stop = async () => { if (server.stale || !active.length) return; if (await server.run('service.stop-shares', {}) && alive.current) onClose() }
  return <Modal title={s('stopShares')} t={t} onClose={onClose}><p className="muted">{s('stopSharesHint')}</p>{active.length ? <ul>{active.map(service => <li key={service.id}>{service.name} · {service.network.toUpperCase()} {service.ports}</li>)}</ul> : <p>{s('stopSharesEmpty')}</p>}{server.error != null && <ErrorBanner message={errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}<div className="modal-actions"><Button type="button" onClick={onClose}>{t('cancel')}</Button><Button type="button" variant="danger" disabled={server.stale || !active.length} busy={server.busy.has('service.stop-shares')} onClick={stop}>{s('stopSharesConfirm')}</Button></div></Modal>
}
