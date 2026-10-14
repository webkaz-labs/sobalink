import { useLayoutEffect, useRef, useState } from 'react'
import type { Locale } from '../api'
import type { Server } from '../useServer'
import { readSetupReenroll, readSetupRevoked, type SetupRevokedRecord, type SetupReenrollResult } from '../nearby-setup'
import { nearbyText, type NearbyTextKey } from '../nearby-i18n'
import { Button, useAlive } from './ui'

/** Reenrollment changes only a durable tombstone. It grants no pairing or application trust. */
export function RemovedSetupDevices({ server, locale, disabled, onGuardChange }: { server: Server; locale: Locale; disabled: boolean; onGuardChange: (blocked: boolean) => void }) {
  const c = (key: NearbyTextKey) => nearbyText(locale, key), alive = useAlive()
  const latest = useRef({ server, disabled, onGuardChange }); latest.current = { server, disabled, onGuardChange }
  const operation = useRef<'read' | 'reenroll' | undefined>(undefined), epoch = useRef(0), unknown = useRef<SetupRevokedRecord | undefined>(undefined)
  const current = useRef<SetupRevokedRecord[] | undefined>(undefined), selection = useRef<SetupRevokedRecord | undefined>(undefined)
  const [records, setRecords] = useState<SetupRevokedRecord[]>(), [selected, setSelected] = useState<SetupRevokedRecord>()
  const [receipt, setReceipt] = useState<SetupReenrollResult>(), [consent, setConsent] = useState(false)
  const [busy, setBusy] = useState(false), [uncertain, setUncertain] = useState(false), [notice, setNotice] = useState<NearbyTextKey>()
  const clearSelection = () => { selection.current = undefined; setSelected(undefined); setConsent(false) }
  const guard = (value: boolean) => latest.current.onGuardChange(value)
  const usable = () => !latest.current.disabled && latest.current.server.auth === 'ready' && !latest.current.server.stale
  const binding = JSON.stringify([disabled, server.auth, server.stale, server.state?.processId])
  useLayoutEffect(() => {
    ++epoch.current; clearSelection(); setReceipt(undefined)
    if (operation.current === 'reenroll') { setUncertain(true); setNotice('reenrollUncertain'); guard(true) }
    return () => { ++epoch.current }
  }, [binding])
  const read = async () => {
    if (operation.current || !usable()) return
    operation.current = 'read'; setBusy(true); guard(true); clearSelection(); setReceipt(undefined)
    const sequence = ++epoch.current, prior = unknown.current
    try {
      const response = await latest.current.server.run('setup.revoked', {}, 'setup.revoked')
      if (!alive.current || sequence !== epoch.current) return
      const next = response?.ok && readSetupRevoked(response.result)
      if (!next) { setNotice('reenrollReadFailed'); setUncertain(true); return }
      current.current = next; setRecords(next); setUncertain(false); unknown.current = undefined
      const record = prior && next.find(item => item.peerKey === prior.peerKey)
      setNotice(prior ? !record ? 'reenrollNoLongerRemoved' : JSON.stringify(record) === JSON.stringify(prior) ? 'reenrollStillRemoved' : 'reenrollChanged' : undefined)
      guard(false)
    } catch { if (alive.current && sequence === epoch.current) { setNotice('reenrollReadFailed'); setUncertain(true) } }
    finally { operation.current = undefined; if (alive.current) { setBusy(false); if (sequence !== epoch.current && !unknown.current) guard(false) } }
  }
  const reenroll = async () => {
    const expected = selection.current
    if (operation.current || !usable() || uncertain || !consent || !expected || !current.current?.some(item => JSON.stringify(item) === JSON.stringify(expected))) return
    operation.current = 'reenroll'; unknown.current = expected; setBusy(true); setConsent(false); setReceipt(undefined); setNotice(undefined); guard(true)
    const sequence = ++epoch.current
    try {
      const response = await latest.current.server.run('setup.reenroll', { peerKey: expected.peerKey, expectedGeneration: expected.generation, consent: true }, 'setup.reenroll')
      if (!alive.current || sequence !== epoch.current) return
      const result = response?.ok && readSetupReenroll(response.result, expected)
      if (!result) { setUncertain(true); setNotice('reenrollUncertain'); return }
      unknown.current = undefined; setUncertain(false); clearSelection(); setReceipt(result); setNotice('reenrollSuccess')
      current.current = current.current?.filter(item => item.peerKey !== expected.peerKey); setRecords(current.current); guard(false)
    } catch { if (alive.current) { setUncertain(true); setNotice('reenrollUncertain') } }
    finally { operation.current = undefined; if (alive.current) setBusy(false) }
  }
  const locked = disabled || server.auth !== 'ready' || server.stale || busy
  const details = (record: SetupRevokedRecord) => <dl><dt>{c('peerKey')}</dt><dd className="code-value">{record.peerKey}</dd><dt>{c('removedGeneration')}</dt><dd>{record.generation}</dd><dt>{c('removedBasis')}</dt><dd>{c(record.verification === 'full-sha256' ? 'fullMode' : record.verification === 'tofu' ? 'tofuMode' : 'basisUnknown')}</dd></dl>
  return <details className="nearby-removed"><summary>{c('removedDevices')}</summary><div className="form-stack">
    <p>{c('removedHint')}</p><p className="scope-note">{c('removedPrerequisites')}</p>
    <Button type="button" disabled={locked} busy={busy} onClick={() => void read()}>{c('removedLoad')}</Button>
    {notice && <p role={uncertain ? 'alert' : 'status'}>{c(notice)}</p>}
    {receipt && <section aria-label={c('reenrollSuccess')}>{details(receipt)}</section>}
    {records?.length === 0 && <p>{c('removedEmpty')}</p>}
    <ul className="nearby-candidates">{records?.map(record => <li key={record.peerKey} className="invitation-card">{details(record)}<Button type="button" disabled={locked || uncertain} aria-pressed={selected?.peerKey === record.peerKey} onClick={() => { selection.current = record; setSelected(record); setConsent(false); setReceipt(undefined); setNotice(undefined) }}>{c('removedSelect')}</Button></li>)}</ul>
    {selected && <section className="invitation-card form-stack" aria-label={c('reenroll')}>{details(selected)}
      <label className="checkbox-field"><input type="checkbox" checked={consent} disabled={locked || uncertain} onChange={event => setConsent(event.target.checked)} />{c('removedConsent')}</label>
      <Button type="button" disabled={locked || uncertain || !consent} onClick={() => void reenroll()}>{c('reenroll')}</Button>
    </section>}
  </div></details>
}
