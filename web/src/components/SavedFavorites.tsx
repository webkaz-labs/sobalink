import { useEffect, useRef, useState } from 'react'
import type { FavoriteReference, FavoritesView, Locale, ServiceConfiguration, ServiceGroup } from '../api'
import { favoriteKey, favoriteReference, readFavorites } from '../favorites'
import { favoriteText } from '../favorites-i18n'
import type { Server } from '../useServer'
import { Badge, Button } from './ui'
import './SavedFavorites.css'

interface Props {
  server: Server
  locale: Locale
  services: ServiceConfiguration[]
  groups: ServiceGroup[]
  blocked: boolean
  filterIds: string[] | undefined
  onFilterChange: (ids: string[] | undefined) => void
  onNavigate: (reference: FavoriteReference) => void
}

// Mounted only while the user opens the optional controls. Errors and stale
// responses here must never substitute an empty store or block normal browsing.
export function SavedFavorites({ server, locale, services, groups, blocked, filterIds, onFilterChange, onNavigate }: Props) {
  const f = (key: Parameters<typeof favoriteText>[1]) => favoriteText(locale, key)
  const [view, setView] = useState<FavoritesView>()
  const [target, setTarget] = useState('')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<'loadFailed' | 'changeFailed' | 'stale' | 'saved'>()
  const mounted = useRef(false)
  const initialRead = useRef(false)
  const generation = useRef(0)
  const pending = useRef<number | undefined>(undefined)
  const state = useRef({ blocked, stale: server.stale })
  state.current = { blocked, stale: server.stale }
  const current = (epoch: number) => mounted.current && epoch === generation.current && !state.current.stale
  const externalBusy = [...server.busy].some(key => /^favorites[.:]/.test(key))
  const disabled = blocked || busy || externalBusy

  const run = async (change?: { action: 'favorites.add' | 'favorites.remove'; reference: FavoriteReference }) => {
    if (!mounted.current || state.current.blocked || pending.current !== undefined || externalBusy || change && !view) return
    const epoch = ++generation.current
    pending.current = epoch; setBusy(true); setNotice(undefined); setTarget(''); onFilterChange(undefined)
    const revision = view?.revision
    setView(undefined)
    try {
      if (change) {
        const response = await server.run(change.action, { reference: favoriteReference(change.reference), expectedRevision: revision! }, `favorites:${crypto.randomUUID()}`)
        if (!current(epoch)) return
        if (!response) throw new Error('unconfirmed')
        readFavorites(response.result)
      }
      // Never reuse a request/cache key: a cached list or mutation result is not
      // evidence of current preference state, even after a successful write.
      const response = await server.run('favorites.list', {}, `favorites:${crypto.randomUUID()}`)
      if (!current(epoch)) return
      if (!response) throw new Error('unconfirmed')
      setView(readFavorites(response.result))
      if (change) setNotice('saved')
    } catch {
      if (current(epoch)) { setView(undefined); setNotice(change ? 'changeFailed' : 'loadFailed') }
    } finally {
      if (pending.current === epoch) pending.current = undefined
      if (current(epoch)) setBusy(false)
    }
  }
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false; ++generation.current; pending.current = undefined }
  }, [])
  useEffect(() => {
    if (initialRead.current || blocked || externalBusy) return
    let cancelled = false
    void Promise.resolve().then(() => { if (!cancelled) { initialRead.current = true; void run() } })
    return () => { cancelled = true }
  }, [blocked, externalBusy])
  useEffect(() => {
    if (!server.stale) return
    ++generation.current; pending.current = undefined; setBusy(false); setView(undefined); setNotice('stale'); onFilterChange(undefined)
  }, [server.stale, onFilterChange])

  const marked = new Set(view?.entries.map(favoriteKey))
  const candidates = [
    ...services.map(service => ({ reference: { kind: 'service', serviceId: service.id } as FavoriteReference, name: service.name })),
    ...groups.map(group => ({ reference: { kind: 'group', groupName: group.name } as FavoriteReference, name: group.name })),
  ].filter(item => !marked.has(favoriteKey(item.reference)))
  const selected = candidates.find(item => favoriteKey(item.reference) === target)
  const info = (reference: FavoriteReference) => {
    if (reference.kind === 'service') { const service = services.find(item => item.id === reference.serviceId); return { name: service?.name || reference.serviceId, exists: Boolean(service) } }
    return { name: reference.groupName, exists: groups.some(item => item.name === reference.groupName) }
  }
  const missing = view?.entries.some(entry => !entry.available || !info(entry).exists)
  const noticeKey = notice === 'changeFailed' && (server.error as { code?: string })?.code === 'favorites_revision_conflict' ? 'conflict' : notice
  return <section className="saved-favorites" aria-label={f('title')}>
    <p className="small muted">{f('intro')}</p>
    {(busy || externalBusy) && <p role="status">{f('loading')}</p>}
    {noticeKey && <p role={noticeKey === 'saved' ? 'status' : 'alert'} className={noticeKey === 'saved' ? 'small muted' : 'scope-note'}>{f(noticeKey)}</p>}
    {view && <>
      {view.durabilityUncertain && <p role="alert" className="scope-note">{f('uncertain')}</p>}
      {!view.entries.length ? <p className="small muted">{f('empty')}</p> : <ul className="favorite-list">{view.entries.map(entry => {
        const item = info(entry)
        const available = entry.available && item.exists
        return <li key={favoriteKey(entry)}><Button type="button" variant="ghost" disabled={disabled || !available} aria-label={`${f('open')}: ${f(entry.kind)} · ${item.name}`} onClick={() => { if (!state.current.blocked && pending.current === undefined) onNavigate(favoriteReference(entry)) }}><span className="favorite-label">{item.name}<small>{f(entry.kind)}</small></span></Button>{!available && <Badge>{f('missing')}</Badge>}<Button type="button" variant="ghost" disabled={disabled} aria-label={`${f('remove')}: ${f(entry.kind)} · ${item.name}`} onClick={() => void run({ action: 'favorites.remove', reference: entry })}>{f('remove')}</Button></li>
      })}</ul>}
      {missing && <p className="small muted">{f('missingHint')}</p>}
      <div className="favorite-add"><label className="field">{f('addTarget')}<select disabled={disabled} value={target} onChange={event => setTarget(event.target.value)}><option value="">{f('choose')}</option>{(['service', 'group'] as const).map(kind => <optgroup key={kind} label={f(kind === 'service' ? 'services' : 'groups')}>{candidates.filter(item => item.reference.kind === kind).map(item => <option key={favoriteKey(item.reference)} value={favoriteKey(item.reference)}>{item.name}</option>)}</optgroup>)}</select></label><Button type="button" disabled={disabled || !selected} onClick={() => { if (selected) void run({ action: 'favorites.add', reference: selected.reference }) }}>{f('add')}</Button></div>
      <Button type="button" disabled={disabled} aria-pressed={filterIds !== undefined} onClick={() => onFilterChange(filterIds !== undefined ? undefined : view.entries.flatMap(entry => entry.kind === 'service' && entry.available && info(entry).exists ? [entry.serviceId] : []))}>{f('filter')}</Button>
    </>}
    <Button type="button" variant="ghost" disabled={disabled} onClick={() => void run()}>{f('reload')}</Button>
  </section>
}
