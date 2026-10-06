import { useEffect, useId, useRef, useState } from 'react'
import type { LanAddress, LanPolicy, Locale, State } from '../api'
import type { Translate } from '../i18n'
import { policyTranslator } from '../lan-policy-i18n'
import type { Server } from '../useServer'
import { Button, ErrorBanner, useAlive } from './ui'

export type LanPolicySelection = Pick<LanPolicy, 'mode' | 'prefixes'>
export interface LanPolicyDraft { policyMode?: LanPolicy['mode']; policyPrefixes?: string }
export function selectedLANPolicy(draft: LanPolicyDraft): LanPolicySelection | undefined {
  if (!draft.policyMode) return undefined
  return { mode: draft.policyMode, prefixes: draft.policyMode === 'trusted-relay' ? [] : Array.from(new Set((draft.policyPrefixes || '').split(/[\n,]+/).map(value => value.trim()).filter(Boolean))).sort() }
}
function validPolicy(value: unknown): value is LanPolicy {
  if (!value || typeof value !== 'object') return false
  const policy = value as LanPolicy
  return ['trusted-relay', 'allowed-lan-destinations'].includes(policy.mode) && Array.isArray(policy.prefixes) && policy.prefixes.every(prefix => typeof prefix === 'string' && /^[0-9a-fA-F:.]+\/\d{1,3}$/.test(prefix)) && (policy.mode === 'trusted-relay' ? policy.prefixes.length === 0 : policy.prefixes.length > 0) && typeof policy.editable === 'boolean' && typeof policy.restartRequired === 'boolean'
}
export function PolicySummary({ policy, locale }: { policy: LanPolicySelection; locale: Locale }) {
  const p = policyTranslator(locale)
  return <div className="form-stack"><strong>{p('mode')}: {p(policy.mode === 'trusted-relay' ? 'trusted' : 'allowed')}</strong>{policy.prefixes.length > 0 && <ul>{policy.prefixes.map(prefix => <li key={prefix} className="code-value">{prefix}</li>)}</ul>}<p className="small muted">{p('scope')}</p>{policy.mode === 'trusted-relay' && <p className="scope-note">{p('expansion')}</p>}</div>
}
function PolicyFields({ server, locale, value, onChange, disabled }: { server: Server; locale: Locale; value: LanPolicyDraft; onChange: (value: LanPolicyDraft) => void; disabled: boolean }) {
  const p = policyTranslator(locale)
  const alive = useAlive()
  const sequence = useRef(0)
  const [suggestions, setSuggestions] = useState<LanAddress[]>()
  const prefixHint = useId()
  const mode = value.policyMode || 'trusted-relay'
  const lookup = async () => {
    const current = ++sequence.current
    const response = await server.run('lan.addresses', {})
    if (!response || !alive.current || current !== sequence.current) return
    const addresses = response.result?.addresses
    if (!Array.isArray(addresses) || addresses.some(item => !item || typeof item !== 'object' || typeof item.interface !== 'string' || typeof item.address !== 'string' || item.prefix !== undefined && typeof item.prefix !== 'string')) { server.setError({ code: 'invalid_response' }); return }
    setSuggestions(addresses.filter((item: LanAddress) => item.prefix && /^[0-9a-fA-F:.]+\/\d{1,3}$/.test(item.prefix)))
  }
  return <div className="form-stack"><label className="field">{p('mode')}<select disabled={disabled} value={mode} onChange={event => onChange({ ...value, policyMode: event.target.value as LanPolicy['mode'] })}><option value="trusted-relay">{p('trusted')}</option><option value="allowed-lan-destinations">{p('allowed')}</option></select></label>{mode === 'allowed-lan-destinations' && <><label className="field">{p('prefixes')}<textarea aria-label={p('prefixes')} aria-describedby={prefixHint} value={value.policyPrefixes || ''} onChange={event => onChange({ ...value, policyPrefixes: event.target.value })} disabled={disabled} rows={3} spellCheck={false} autoComplete="off" placeholder="192.168.50.0/24" /><small id={prefixHint} className="muted">{p('prefixesHint')}</small></label><div><Button disabled={disabled} busy={server.busy.has('lan.addresses')} onClick={lookup}>{p('suggestions')}</Button></div>{suggestions?.length === 0 && <p className="small muted">{p('noSuggestions')}</p>}{suggestions && <div className="flex flex-wrap gap-2">{suggestions.map(item => <Button key={`${item.interface}:${item.prefix}`} disabled={disabled} onClick={() => onChange({ ...value, policyPrefixes: Array.from(new Set([...(value.policyPrefixes || '').split(/[\n,]+/).map(value => value.trim()).filter(Boolean), item.prefix!])).join('\n') })}>{p('choose')}: {item.prefix} · {item.interface}</Button>)}</div>}</>}<p className="small muted">{p('scope')}</p>{mode === 'trusted-relay' && <p className="small muted">{p('expansion')}</p>}</div>
}
export function InitialLanPolicy({ server, locale, draft, onChange, disabled }: { server: Server; locale: Locale; draft: LanPolicyDraft; onChange: (value: LanPolicyDraft) => void; disabled: boolean }) {
  const p = policyTranslator(locale)
  return <details className="relay-config lan-policy"><summary>{p('title')}</summary><p className="small muted">{p('initial')}</p><PolicyFields server={server} locale={locale} value={{ policyMode: draft.policyMode, policyPrefixes: draft.policyPrefixes }} onChange={onChange} disabled={disabled} /></details>
}
export function SavedLanPolicy({ server, state, locale, t, disabled }: { server: Server; state: State; locale: Locale; t: Translate; disabled: boolean }) {
  const p = policyTranslator(locale)
  const alive = useAlive()
  const [policy, setPolicy] = useState(state.lan?.policy)
  const [draft, setDraft] = useState<LanPolicyDraft>({ policyMode: state.lan?.policy?.mode, policyPrefixes: state.lan?.policy?.prefixes.join('\n') })
  const [review, setReview] = useState<LanPolicySelection>()
  const [saved, setSaved] = useState(false)
  const [validation, setValidation] = useState('')
  const pending = useRef(false)
  const sequence = useRef(0)
  const reviewRegion = useRef<HTMLDivElement>(null)
  useEffect(() => { if (review) reviewRegion.current?.focus() }, [review])
  useEffect(() => {
    if (!state.lan?.policy) return
    setPolicy(state.lan.policy); setDraft({ policyMode: state.lan.policy.mode, policyPrefixes: state.lan.policy.prefixes.join('\n') }); setReview(undefined)
  }, [state.lan?.policy?.mode, state.lan?.policy?.prefixes.join(','), state.lan?.policy?.editable])
  const refresh = async () => {
    const current = ++sequence.current
    const response = await server.run('lan.policy.get', {})
    if (!response || !alive.current || current !== sequence.current) return
    if (!validPolicy(response.result)) { server.setError({ code: 'invalid_response' }); return }
    setPolicy(response.result); setDraft({ policyMode: response.result.mode, policyPrefixes: response.result.prefixes.join('\n') }); setReview(undefined); setSaved(false)
  }
  const blocked = disabled || !policy?.editable || server.busy.has('lan.policy.set')
  const apply = async () => {
    if (!review || blocked || pending.current) return
    pending.current = true
    try {
      const response = await server.run('lan.policy.set', review)
      if (!response || !alive.current) return
      if (!validPolicy(response.result) || response.result.mode !== review.mode || JSON.stringify(response.result.prefixes) !== JSON.stringify(review.prefixes)) { server.setError({ code: 'invalid_response' }); return }
      setPolicy(response.result); setDraft({ policyMode: response.result.mode, policyPrefixes: response.result.prefixes.join('\n') }); setReview(undefined); setSaved(true)
    } finally { pending.current = false }
  }
  return <details className="relay-config lan-policy" onToggle={event => { if (event.currentTarget.open && !policy && !server.busy.has('lan.policy.get')) void refresh() }}><summary>{p('title')}</summary>{validation && <ErrorBanner message={validation} t={t} />}{saved && <p role="status" className="scope-note">{p('saved')}</p>}{!policy ? <p>{p('unknown')}</p> : review ? <div className="relay-review invitation-card" role="region" aria-label={p('review')} tabIndex={-1} ref={reviewRegion}><PolicySummary policy={review} locale={locale} /><p className="small muted">{p('impact')}</p><div className="invitation-actions"><Button disabled={server.busy.has('lan.policy.set')} onClick={() => setReview(undefined)}>{t('cancel')}</Button><Button variant="primary" disabled={blocked} busy={server.busy.has('lan.policy.set')} onClick={apply}>{p('apply')}</Button></div></div> : <><PolicyFields server={server} locale={locale} value={draft} disabled={blocked} onChange={value => { ++sequence.current; setDraft(value); setValidation(''); setSaved(false) }} />{!policy.editable && <p className="scope-note">{p('stopped')}</p>}<Button disabled={blocked} onClick={() => { ++sequence.current; const choice = selectedLANPolicy(draft); if (!choice || choice.mode === 'allowed-lan-destinations' && !choice.prefixes.length) { setValidation(p('required')); return } setValidation(''); setReview(choice); setSaved(false) }}>{p('review')}</Button></>}<div><Button disabled={disabled || server.busy.has('lan.policy.set')} busy={server.busy.has('lan.policy.get')} onClick={refresh}>{p('refresh')}</Button></div></details>
}
