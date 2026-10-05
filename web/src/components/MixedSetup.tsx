import { useRef, useState } from 'react'
import type { Locale, MixedStatus, State, TransportBackend } from '../api'
import type { Translate } from '../i18n'
import type { Server } from '../useServer'
import { Badge, Button, ErrorBanner, useAlive } from './ui'
const en = {
  title: 'Mixed connections', intro: 'Set up and pair each network separately first. Then choose two or three backends in the order to try for new connections.',
  external: 'Tailnet and trusted-relay backends may contact external services. Mixed mode is incompatible with strict LAN-only permission.',
  lifetime: 'Only new connections and reconnects can choose another approved route. Established TCP streams are not migrated or replayed.',
  review: 'Review mixed activation', activate: 'Activate selected backends', cancel: 'Cancel', order: 'Backend order', optional: 'No additional backend',
  refresh: 'Refresh backend identities', backendReady: 'Backend ready', backendNotReady: 'Backend not ready', readiness: 'Backend readiness is not proof of application health or the path currently carrying traffic.',
  identity: 'This device’s mixed public ID', binding: 'Bind routes to one device', bindHint: 'Select current unbound identities from different backends that should represent the same remote device. The backend requires a fresh signed proof on every selected route. Matching names or IP addresses are insufficient.',
  reviewBind: 'Review identity binding', proveBind: 'Verify and bind selected identities', bindImpact: 'Existing route-specific approvals pause. The new logical identity needs separate application permission. No trust, service scope or file-receive permission is copied.',
  existing: 'Saved identity bindings', unbind: 'Review removal', remove: 'Remove this binding', removeImpact: 'Removing a binding closes its current work and does not reactivate old approvals.',
  select: 'Select two or three distinct backends or current identities from different backends.', changed: 'The selected routes changed or became unavailable. Refresh and review them again.', applied: 'Change accepted. Refreshing current state.', noRoutes: 'No eligible unbound routes are reported. Set up the underlying networks and refresh their status.',
} as const
const ja: Record<keyof typeof en, string> = {
  title: '複合接続', intro: '先に各ネットワークを個別に設定・ペアリングします。その後、新しい接続で試す順序に2〜3個の接続方式を選びます。',
  external: 'Tailnetや信頼したリレー方式は外部サービスへ通信する場合があります。複合接続はLAN内限定の許可とは併用できません。',
  lifetime: '別の許可済み経路を選べるのは新規接続と再接続だけです。既存のTCPストリームは移行・再送しません。',
  review: '複合接続の開始を確認', activate: '選択した方式を有効化', cancel: 'キャンセル', order: '接続方式の順序', optional: '追加の方式なし',
  refresh: '接続方式の識別情報を更新', backendReady: '接続方式の準備完了', backendNotReady: '接続方式の準備未完了', readiness: '接続方式の準備完了は、アプリの動作や現在の通信経路の確認を意味しません。',
  identity: 'この端末の複合接続公開ID', binding: '経路を同じ端末に関連付ける', bindHint: '同じ相手端末を表す、異なる接続方式の未関連付けIDを選びます。選択した各経路で新しい署名証明を確認します。名前やIPアドレスの一致だけでは関連付けません。',
  reviewBind: '識別情報の関連付けを確認', proveBind: '選択したIDを検証して関連付け', bindImpact: '既存の経路別許可を停止します。新しい論理IDには別途アプリ利用許可が必要です。信頼・サービス範囲・ファイル受信許可はコピーしません。',
  existing: '保存済みの識別情報の関連付け', unbind: '解除内容を確認', remove: 'この関連付けを解除', removeImpact: '解除すると現在の処理を閉じます。以前の許可は再有効化しません。',
  select: '異なる2〜3個の接続方式、または異なる方式で確認済みのIDを選択してください。', changed: '選択した経路が変わったか利用できなくなりました。更新して再確認してください。', applied: '変更を受け付けました。現在の状態を更新します。', noRoutes: '関連付け可能な経路は報告されていません。個別の接続方式を設定し、状態を更新してください。',
}
const names: TransportBackend[] = ['direct-lan', 'tailnet', 'lan']
function usableStatus(v: unknown): v is MixedStatus {
  if (!v || typeof v !== 'object') return false
  const s = v as MixedStatus
  return typeof s.configured === 'boolean' && (!s.backends || s.backends.every(n => names.includes(n))) && (!s.bindings || Array.isArray(s.bindings)) && (!s.routes || Array.isArray(s.routes)) && (!s.backendStates || Array.isArray(s.backendStates))
}
export function MixedSetup({ server, state, locale, t, blocked }: { server: Server; state: State; locale: Locale; t: Translate; blocked: boolean }) {
  const d = locale === 'ja' ? ja : en, alive = useAlive(), pending = useRef(false)
  const [order, setOrder] = useState<string[]>([...(state.mixed?.backends || []), '', '', ''].slice(0, 3))
  const [review, setReview] = useState<TransportBackend[]>(), [selected, setSelected] = useState<string[]>([]), [bindReview, setBindReview] = useState<string>(), [remove, setRemove] = useState<string>(), [error, setError] = useState(''), [applied, setApplied] = useState(false)
  const [readStatus, setReadStatus] = useState<MixedStatus>()
  const status = state.mixed || readStatus
  const bindingIDs = new Set((status?.bindings || []).map(b => b.peerId))
  const routes = (status?.routes || []).filter(r => r.backendReady && !r.expired && !bindingIDs.has(r.peerId))
  const currentSelection = routes.filter(r => selected.includes(r.peerId))
  const signature = JSON.stringify(currentSelection.map(r => [r.peerId, r.backend, r.transportId]).sort())
  const disabled = blocked || server.stale || server.auth !== 'ready'
  const execute = async (fn: () => Promise<void>) => { if (disabled || pending.current) return; pending.current = true; try { await fn() } finally { pending.current = false } }
  const refresh = () => execute(async () => { const result = await server.run('mixed.status', {}); if (result && alive.current) { if (!usableStatus(result.result)) { server.setError({ code: 'invalid_response' }); return }; setReadStatus(result.result); setBindReview(undefined); setSelected([]) } })
  const prepare = () => { const chosen = order.filter(Boolean) as TransportBackend[]; if (chosen.length < 2 || chosen.length > 3 || new Set(chosen).size !== chosen.length) { setError(d.select); return }; setError(''); setReview(chosen) }
  const activate = () => execute(async () => { if (!review) return; const result = await server.run('network.configure', { mode: 'mixed', mixed: { backends: review } }); if (result && alive.current) { setReview(undefined); setApplied(true) } })
  const prepareBind = () => { if (currentSelection.length < 2 || currentSelection.length > 3 || currentSelection.length !== selected.length || new Set(currentSelection.map(r => r.backend)).size !== selected.length) { setError(d.select); return }; setError(''); setBindReview(signature) }
  const bind = () => execute(async () => { if (!bindReview || bindReview !== signature) { setError(d.changed); setBindReview(undefined); return }; const result = await server.run('mixed.bind', { peers: [...selected] }); if (result && alive.current) { setBindReview(undefined); setSelected([]); setApplied(true) } })
  const unbind = () => execute(async () => { if (!remove || !bindingIDs.has(remove)) { setRemove(undefined); setError(d.changed); return }; const result = await server.run('mixed.unbind', { peerId: remove }); if (result && alive.current) { setRemove(undefined); setApplied(true) } })
  return <section className="form-stack subsection" aria-label={d.title}><p className="muted">{d.intro}</p><p className="scope-note">{d.external}</p><p className="small muted">{d.lifetime}</p>{error && <ErrorBanner message={error} t={t} />}
    {state.settings?.network !== 'mixed' && (review ? <div className="invitation-card"><h3>{d.review}</h3><ol>{review.map(n => <li key={n}>{t(n)}</li>)}</ol><p>{d.external}</p><p>{d.lifetime}</p><div className="modal-actions"><Button disabled={server.busy.has('network.configure')} onClick={() => setReview(undefined)}>{d.cancel}</Button><Button variant="primary" onClick={activate} disabled={disabled} busy={server.busy.has('network.configure')}>{d.activate}</Button></div></div> : <><fieldset className="form-section"><legend>{d.order}</legend>{order.map((value, index) => <label className="field" key={index}>{index + 1}<select value={value} disabled={disabled} onChange={e => { setOrder(old => old.map((v, i) => i === index ? e.target.value : v)); setApplied(false) }}><option value="">{d.optional}</option>{names.map(n => <option key={n} value={n}>{t(n)}</option>)}</select></label>)}</fieldset><Button variant="primary" onClick={prepare} disabled={disabled}>{d.review}</Button></>)}
    <Button disabled={disabled} onClick={refresh} busy={server.busy.has('mixed.status')}>{d.refresh}</Button>
    {status?.publicKey && <label className="field">{d.identity}<textarea readOnly rows={2} value={status.publicKey} className="code-value" /></label>}
    {status?.backendStates?.map(b => <p key={b.backend}>{t(b.backend)} <Badge>{b.running ? d.backendReady : d.backendNotReady}</Badge></p>)}<p className="small muted">{d.readiness}</p>
    {state.settings?.network === 'mixed' && <><fieldset className="form-section"><legend>{d.binding}</legend><p className="small muted">{d.bindHint}</p>{routes.map(r => <label className="checkbox-field" key={r.peerId}><input type="checkbox" disabled={disabled || Boolean(bindReview)} checked={selected.includes(r.peerId)} onChange={e => { setSelected(old => e.target.checked ? [...old, r.peerId] : old.filter(id => id !== r.peerId)); setApplied(false) }} /><span>{r.name || r.peerId} · {t(r.backend)}<span className="code-value"> {r.transportId}</span></span></label>)}{!routes.length && <p>{d.noRoutes}</p>}{bindReview ? <div className="invitation-card"><p>{d.bindImpact}</p><p className="small muted">{d.lifetime}</p><div className="modal-actions"><Button onClick={() => setBindReview(undefined)} disabled={server.busy.has('mixed.bind')}>{d.cancel}</Button><Button variant="primary" onClick={bind} disabled={disabled || signature !== bindReview} busy={server.busy.has('mixed.bind')}>{d.proveBind}</Button></div></div> : <Button onClick={prepareBind} disabled={disabled || selected.length < 2}>{d.reviewBind}</Button>}</fieldset>
      {Boolean(status?.bindings?.length) && <section><h3>{d.existing}</h3>{status!.bindings!.map(b => <div className="invitation-card" key={b.peerId}><p className="code-value">{b.peerId}</p><ul>{b.identities.map(i => <li key={`${i.backend}:${i.id}`}>{t(i.backend)} <span className="code-value">{i.id}</span></li>)}</ul>{remove === b.peerId ? <><p>{d.removeImpact}</p><div className="modal-actions"><Button onClick={() => setRemove(undefined)} disabled={server.busy.has('mixed.unbind')}>{d.cancel}</Button><Button variant="danger" onClick={unbind} disabled={disabled} busy={server.busy.has('mixed.unbind')}>{d.remove}</Button></div></> : <Button variant="danger" onClick={() => setRemove(b.peerId)} disabled={disabled}>{d.unbind}</Button>}</div>)}</section>}</>}
    {applied && <p role="status">{d.applied}</p>}
  </section>
}
