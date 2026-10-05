import { useRef, useState } from 'react'
import type { Locale } from '../api'
import type { Translate } from '../i18n'
import type { Server } from '../useServer'
import { Button, ErrorBanner, useAlive } from './ui'
import { isRelayAddress } from './LanSetup'
const en = {
  budget: 'Maximum probes per discovery pass',
  title: 'Advanced: WAN direct candidates', read: 'Review WAN settings', enabled: 'Explicit discovery enabled', disabled: 'Discovery disabled',
  intro: 'Optionally probe only the numeric STUN endpoints you choose and advertise eligible global IPv6 addresses. This may send UDP outside the LAN. It does not guarantee direct connectivity.',
  addresses: 'Exact STUN IP:port endpoints', ipv6: 'Advertise eligible global IPv6 addresses',
  hint: 'Numeric endpoints, separated by commas. The probe budget bounds each discovery pass. Leave endpoints empty for IPv6-only candidates. No server is selected automatically. The existing approved encrypted relay remains the fallback.',
  review: 'Review candidate changes', save: 'Save explicit WAN discovery', disable: 'Disable WAN discovery', cancel: 'Cancel',
  restart: 'Saved configuration applies after restart. No discovery is started by saving here.', locked: 'Stop sobalink and reopen offline before changing WAN discovery. A selected pinned relay and trusted-relay destination policy are required.',
  invalid: 'Enter distinct numeric IP:port endpoints and a positive finite probe budget, without hostnames or wildcard addresses.', saved: 'WAN configuration saved. Restart to apply.',
} as const
const ja: Record<keyof typeof en, string> = {
  budget: '1回の探索の最大送信先数',
  title: '詳細設定：WAN直接経路候補', read: 'WAN設定を確認', enabled: '明示した探索が有効', disabled: '探索は無効',
  intro: '指定した数値STUN接続先への探索と、利用可能なグローバルIPv6アドレスの通知を任意で有効にします。LAN外へUDPを送信する場合があります。直接接続を保証するものではありません。',
  addresses: '正確なSTUN IP:port接続先', ipv6: '利用可能なグローバルIPv6アドレスを通知する',
  hint: '数値接続先をカンマ区切りで指定します。1回の探索数は探索予算で制限します。IPv6候補だけを使う場合、接続先は空欄にできます。サーバーは自動選択しません。既存の許可済み暗号化リレーは代替経路として残ります。',
  review: '経路候補の変更を確認', save: '明示したWAN探索を保存', disable: 'WAN探索を無効化', cancel: 'キャンセル',
  restart: '保存した設定は再起動後に適用されます。ここでの保存では探索を開始しません。', locked: 'WAN探索の変更前にsobalinkを停止し、オフラインで開き直してください。固定証明書付きリレーの選択と、trusted-relay送信先ポリシーが必要です。',
  invalid: 'ホスト名やワイルドカードではなく、重複しない数値IP:port接続先と正の有限な探索予算を入力してください。', saved: 'WAN設定を保存しました。再起動すると適用されます。',
}
interface Settings { probeBudget: number; enabled: boolean; stunEndpoints: string[]; advertiseIPv6: boolean; editable: boolean; restartRequired: true }
export function WanCandidates({ server, locale, t, blocked }: { server: Server; locale: Locale; t: Translate; blocked: boolean }) {
  const d = locale === 'ja' ? ja : en, alive = useAlive(), pending = useRef(false)
  const [settings, setSettings] = useState<Settings>(), [endpoints, setEndpoints] = useState(''), [ipv6, setIPv6] = useState(false), [review, setReview] = useState<string[]>(), [error, setError] = useState(''), [saved, setSaved] = useState(false), [budget, setBudget] = useState('4')
  const read = async () => {
    if (blocked || pending.current) return
    pending.current = true
    try {
      const result = await server.run('wan.candidates.get', {})
      if (!result || !alive.current) return
      const value = result.result as unknown as Settings
      if (typeof value?.enabled !== 'boolean' || typeof value.editable !== 'boolean' || typeof value.advertiseIPv6 !== 'boolean' || !Array.isArray(value.stunEndpoints) || !value.stunEndpoints.every(p => typeof p === 'string') || value.restartRequired !== true) { server.setError({ code: 'invalid_response' }); return }
      setBudget(String(value.probeBudget || 4)); setSettings(value); setEndpoints(value.stunEndpoints.join(', ')); setIPv6(value.advertiseIPv6); setReview(undefined); setSaved(false)
    } finally { pending.current = false }
  }
  const prepare = () => { const values = endpoints.split(/[\s,]+/).filter(Boolean); if ((!values.length && !ipv6) || !Number.isSafeInteger(Number(budget)) || Number(budget)<1 || Number(budget)>65535 || values.some(p => !isRelayAddress(p) || /^(0\.0\.0\.0|\[::\]):/.test(p)) || new Set(values).size !== values.length) { setError(d.invalid); return }; setError(''); setReview(values) }
  const write = async (enable: boolean) => {
    if (blocked || pending.current || !settings?.editable || (enable && !review)) return
    pending.current = true
    try { const result = await server.run('wan.candidates.set', enable ? { enabled: true, stunEndpoints: review!, advertiseIPv6: ipv6, probeBudget: Number(budget) } : { enabled: false }); if (result && alive.current) { setSettings({ ...settings, enabled: enable, stunEndpoints: enable ? review! : [], advertiseIPv6: enable && ipv6 }); setReview(undefined); setSaved(true) } }
    finally { pending.current = false }
  }
  return <details className="form-section"><summary>{d.title}</summary><div className="form-stack"><p className="small muted">{d.intro}</p><Button disabled={blocked} busy={server.busy.has('wan.candidates.get')} onClick={read}>{d.read}</Button>{error && <ErrorBanner message={error} t={t} />}{settings && <><p role="status">{settings.enabled ? d.enabled : d.disabled}</p>{!settings.editable && <p className="scope-note">{d.locked}</p>}<label className="field">{d.addresses}<textarea value={endpoints} disabled={blocked || !settings.editable || Boolean(review)} onChange={e => { setEndpoints(e.target.value); setSaved(false) }} autoComplete="off" spellCheck={false} rows={2} /><small className="muted">{d.hint}</small></label><label className="field">{d.budget}<input type="number" min="1" step="1" value={budget} onChange={e => { setBudget(e.target.value); setSaved(false) }} disabled={blocked || !settings.editable || Boolean(review)} /></label><label className="checkbox-field"><input type="checkbox" checked={ipv6} disabled={blocked || !settings.editable || Boolean(review)} onChange={e => { setIPv6(e.target.checked); setSaved(false) }} />{d.ipv6}</label>{review ? <div className="invitation-card"><p className="code-value">{review.join(', ')}</p><p>{d.intro}</p><p>{d.restart}</p><div className="modal-actions"><Button disabled={server.busy.has('wan.candidates.set')} onClick={() => setReview(undefined)}>{d.cancel}</Button><Button variant="primary" disabled={blocked} busy={server.busy.has('wan.candidates.set')} onClick={() => write(true)}>{d.save}</Button></div></div> : <div className="modal-actions"><Button disabled={blocked || !settings.editable} onClick={prepare}>{d.review}</Button>{settings.enabled && <Button variant="danger" disabled={blocked || !settings.editable} onClick={() => write(false)}>{d.disable}</Button>}</div>}<p className="small muted">{d.restart}</p>{saved && <p role="status">{d.saved}</p>}</>}</div></details>
}
