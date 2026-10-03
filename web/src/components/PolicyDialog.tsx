import { useEffect, useRef, useState, type FormEvent } from 'react'
import type { CapacityChoice, CapacityPolicy, HistoryPreview, Locale, PolicyConfig, PolicyPreview } from '../api'
import { bytes, errorDetail, errorText, type Translate } from '../i18n'
import { policyLabel, policyText } from '../policy-i18n'
import type { Server } from '../useServer'
import { Button, ErrorBanner, Modal, useAlive } from './ui'

const common = ['messageBytes', 'batchEntries', 'fileBytes', 'batchBytes']
const retention = ['messageHistoryEntries', 'messageHistoryBytes', 'messageHistoryAgeSeconds']
function readConfig(value: unknown): PolicyConfig {
  const data = value as PolicyConfig
  if (data?.version !== 1 || !data.requested?.logical || !data.requested.resources || !data.effective?.logical || !data.effective.resources || !data.catalog?.logical || !data.catalog.resources || !data.adjustable?.logical || !data.adjustable.resources || !data.revision) throw new Error('invalid_response')
  return data
}
export function PolicyDialog({ server, locale, t, onClose }: { server: Server; locale: Locale; t: Translate; onClose: () => void }) {
  const p = (key: string) => policyText(locale, key)
  const alive = useAlive()
  const [config, setConfig] = useState<PolicyConfig>()
  const [draft, setDraft] = useState<CapacityPolicy>()
  const [preview, setPreview] = useState<PolicyPreview>()
  const [cleanup, setCleanup] = useState<HistoryPreview>()
  const [status, setStatus] = useState('')
  const [reload, setReload] = useState(0)
  const readID = useRef(crypto.randomUUID())
  const localError = () => server.setError({ code: 'invalid_response' })
  useEffect(() => {
    let cancelled = false
    setConfig(undefined); setDraft(undefined); setPreview(undefined); setCleanup(undefined)
    void Promise.resolve().then(() => cancelled ? undefined : server.run('policy.config', {}, `policy.config:${readID.current}:${reload}`)).then(result => {
      if (!result || cancelled) return
      try { const value = readConfig(result.result); setConfig(value); setDraft(structuredClone(value.requested)) } catch { server.setError({ code: 'invalid_response' }) }
    })
    return () => { cancelled = true }
  }, [server.run, reload])
  const keys = (group: 'logical' | 'resources') => Object.keys(config?.catalog[group] || {}).filter(key => config?.adjustable[group][key])
  const invalid = Boolean(draft && config && (['logical', 'resources'] as const).some(group => keys(group).some(key => {
    const value = draft[group][key]
    return value?.mode === 'limited' && (!Number.isSafeInteger(value.value) || value.value! < 1 || value.value! > (config.catalog[group][key].unit === 'seconds' ? 9223372036 : Number.MAX_SAFE_INTEGER))
  })))
  const working = [...server.busy].some(key => key.startsWith('policy.') || key.startsWith('message.history.'))
  const blocked = server.stale || working
  const update = (group: 'logical' | 'resources', key: string, value: CapacityChoice) => {
    setDraft(current => current && ({ ...current, [group]: { ...current[group], [key]: value } })); setPreview(undefined); setCleanup(undefined); setStatus('')
  }
  const format = (choice: CapacityChoice | undefined, unit: string) => choice?.mode === 'unlimited' ? p('unlimited') : choice?.value === undefined ? '—' : unit === 'bytes' ? bytes(choice.value, locale) : `${choice.value.toLocaleString(locale)} ${p(unit) || unit}`
  const field = (group: 'logical' | 'resources', key: string) => {
    const definition = config!.catalog[group][key]
    const choice = draft![group][key] || { mode: 'default' }
    const label = policyLabel(locale, key)
    return <div className="policy-field" key={`${group}:${key}`}><label className="field">{label}<select value={choice.mode} disabled={blocked} onChange={event => { const mode = event.target.value as CapacityChoice['mode']; update(group, key, mode === 'limited' ? { mode, value: config!.effective[group][key]?.value || definition.default } : { mode }) }}><option value="default">{p('default')} ({format({ mode: 'limited', value: definition.default }, definition.unit)})</option><option value="limited">{p('limited')}</option>{group === 'logical' && <option value="unlimited">{p('unlimited')}</option>}</select></label>{choice.mode === 'limited' && <label className="field policy-value">{p('value')} · {p(definition.unit) || definition.unit}<input type="number" aria-label={`${label}: ${p('value')}`} min="1" max={definition.unit === 'seconds' ? 9223372036 : Number.MAX_SAFE_INTEGER} step="1" required disabled={blocked} value={Number.isFinite(choice.value) ? choice.value : ''} onChange={event => update(group, key, { mode: 'limited', value: event.target.value === '' ? NaN : Number(event.target.value) })} /></label>}<small className="muted">{p('effective')}: {format(config!.effective[group][key], definition.unit)}</small></div>
  }
  const review = async (event: FormEvent) => {
    event.preventDefault()
    if (!draft || blocked || invalid) return
    setPreview(undefined); setCleanup(undefined); setStatus('')
    const result = await server.run('policy.preview', { policy: draft })
    if (!alive.current || !result) return
    const value = result.result as unknown as PolicyPreview
    if (value?.version !== 1 || value.destructive !== false || !value.revision || !value.effective?.logical || !value.effective.resources) { localError(); return }
    setPreview(value)
  }
  const apply = async () => {
    if (!draft || !preview || blocked) return
    const reviewed = preview; setPreview(undefined); setCleanup(undefined)
    const result = await server.run('policy.apply', { policy: draft, expectedRevision: reviewed.revision })
    if (!alive.current || !result) return
    try { const value = readConfig(result.result); setConfig(value); setDraft(structuredClone(value.requested)); setStatus('applied') } catch { localError() }
  }
  const reviewCleanup = async () => {
    setCleanup(undefined); setStatus('')
    const result = await server.run('message.history.preview', {})
    if (!alive.current || !result) return
    const value = result.result as unknown as HistoryPreview
    if (value?.version !== 1 || value.destructive !== true || !value.revision || !Array.isArray(value.messageIds) || !Number.isSafeInteger(value.remove) || value.remove < 0 || !Number.isSafeInteger(value.retained) || value.retained < 0 || value.messageIds.length !== value.remove) { localError(); return }
    setCleanup(value)
  }
  const clean = async () => {
    if (!cleanup || !cleanup.remove || blocked) return
    const reviewed = cleanup; setCleanup(undefined)
    if (await server.run('message.history.cleanup', { expectedRevision: reviewed.revision }) && alive.current) setStatus('cleanupDone')
  }
  const errorCode = (server.error as { code?: string })?.code || ''
  return <Modal title={p('title')} t={t} onClose={onClose} wide><p className="muted">{p('intro')}</p>{server.error != null && <ErrorBanner message={p(errorCode) || errorText(server.error, t)} detail={errorDetail(server.error, t)} t={t} />}{status && <p role="status" className="scope-note">{p(status)}</p>}
    {!config || !draft ? <><p role="status">{working ? t('loading') : t('unavailable')}</p>{!working && <Button type="button" onClick={() => setReload(value => value + 1)}>{t('retry')}</Button>}</> : <>
      <form className="form-stack policy-form" onSubmit={review}>
        <h3>{p('common')}</h3><div className="policy-fields">{common.filter(key => keys('logical').includes(key)).map(key => field('logical', key))}</div>
        <details className="advanced"><summary>{p('history')}</summary><p className="small muted">{p('retentionHint')}</p><div className="policy-fields">{retention.filter(key => keys('logical').includes(key)).map(key => field('logical', key))}</div></details>
        <details className="advanced"><summary>{p('advanced')}</summary><h3>{p('logical')}</h3><div className="policy-fields">{keys('logical').filter(key => !common.includes(key) && !retention.includes(key)).map(key => field('logical', key))}</div><h3>{p('resources')}</h3><div className="policy-fields">{keys('resources').map(key => field('resources', key))}</div></details>
        <p className="small muted">{p('resourceHint')}</p>{invalid && <p className="field-error" role="status">{p('invalid')}</p>}
        {preview ? <section className="policy-review" aria-label={p('preview')}><h3>{p('preview')}</h3><p>{p('previewHint')}</p><dl>{(['logical', 'resources'] as const).flatMap(group => keys(group).filter(key => JSON.stringify(draft[group][key] || { mode: 'default' }) !== JSON.stringify(config.requested[group][key] || { mode: 'default' })).map(key => <div key={`${group}:${key}`}><dt>{policyLabel(locale, key)}</dt><dd>{format(preview.effective[group][key], config.catalog[group][key].unit)}</dd></div>))}</dl><div className="modal-actions"><Button type="button" onClick={() => setPreview(undefined)}>{p('back')}</Button><Button type="button" variant="primary" onClick={apply} disabled={blocked}>{p('apply')}</Button></div></section> : <div className="modal-actions"><Button type="button" disabled={blocked} onClick={() => { setStatus(''); setReload(value => value + 1) }}>{p('reload')}</Button><Button type="submit" variant="primary" disabled={blocked || invalid}>{p('review')}</Button></div>}
      </form>
      <section className="policy-cleanup"><h3>{p('history')}</h3><p className="small muted">{p('cleanupHint')}</p><Button type="button" onClick={reviewCleanup} disabled={blocked}>{p('cleanupReview')}</Button>{cleanup && <div className="policy-review" aria-label={p('cleanupTitle')}><h4>{p('cleanupTitle')}</h4><dl><div><dt>{p('remove')}</dt><dd>{cleanup.remove.toLocaleString(locale)}</dd></div><div><dt>{p('retained')}</dt><dd>{cleanup.retained.toLocaleString(locale)}</dd></div></dl>{cleanup.remove ? <><p>{p('cleanupWarning')}</p><div className="modal-actions"><Button type="button" onClick={() => setCleanup(undefined)}>{t('cancel')}</Button><Button type="button" variant="danger" disabled={blocked} onClick={clean}>{p('cleanupApply')}</Button></div></> : <p>{p('nothing')}</p>}</div>}</section>
    </>}
  </Modal>
}
