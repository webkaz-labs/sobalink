import { useEffect, useRef, useState } from 'react'
import * as api from '../api'
import { peerTransferCounts, retryExpected, transferKey } from '../peer-transfers'
import { transferTranslator } from '../transfer-i18n'
import type { Server } from '../useServer'
import { Button } from './ui'
export function PeerTransfers({ peer, state, server, locale, resetToken }: { peer: api.Peer; state: api.State; server: Server; locale: api.Locale; resetToken: string }) {
  const ft = transferTranslator(locale)
  const transfers = state.transfers.filter(item => item.peerId === peer.id)
  const counts = peerTransferCounts(transfers)
  const [selected, setSelected] = useState<string[]>([])
  const [results, setResults] = useState<{ key: string; name: string; outcome: 'retryAcknowledged' | 'retryUnconfirmed' | 'retrySkipped' }[]>([])
  const [running, setRunning] = useState(false)
  const pending = useRef(false)
  const epoch = `${peer.id}:${state.resourceCatalogProcessId}:${server.auth}:${server.stale}:${resetToken}`
  const latest = useRef({ peer, state, server, epoch }); latest.current = { peer, state, server, epoch }
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  useEffect(() => { setSelected([]); setResults([]) }, [epoch])
  useEffect(() => { setSelected(current => current.filter(key => transfers.some(item => transferKey(item) === key && retryExpected(state, peer, item, server.stale, server.busy)))) }, [state, peer, server.stale, server.busy])
  const eligible = (item: api.Transfer) => server.auth === 'ready' && !!retryExpected(state, peer, item, server.stale, server.busy)
  const chosen = transfers.filter(item => selected.includes(transferKey(item)) && eligible(item))
  const retry = async () => {
    if (pending.current || !chosen.length) return
    const captured = chosen.map(item => ({ ...item })); const started = epoch
    pending.current = true; setRunning(true); setSelected([]); setResults([])
    try {
      for (const item of captured) {
        const current = latest.current
        if (!alive.current || current.epoch !== started) break
        const target = current.state.transfers.find(t => transferKey(t) === transferKey(item) && t.peerId === item.peerId)
        const expected = target && current.server.auth === 'ready' ? retryExpected(current.state, current.peer, target, current.server.stale, current.server.busy) : undefined
        let outcome: 'retryAcknowledged' | 'retryUnconfirmed' | 'retrySkipped' = 'retrySkipped'
        if (expected) {
          try { outcome = await current.server.run('transfer.retry', { transferId: item.id, expected }, `transfer:${item.id}`) ? 'retryAcknowledged' : 'retryUnconfirmed' } catch { outcome = 'retryUnconfirmed' }
        }
        if (alive.current && latest.current.epoch === started) setResults(current => [...current, { key: transferKey(item), name: item.name, outcome }])
      }
    } finally { pending.current = false; if (alive.current) setRunning(false) }
  }
  if (!transfers.length) return null
  return <section className="batch-preview" aria-label={ft('peerTransfers')}>
    <h3>{ft('peerTransfers')}: {peer.name}</h3>
    <p className="small">{ft('succeededCount')}: {counts.succeeded} · {ft('failedCount')}: {counts.failed} · {ft('progressCount')}: {counts.inProgress} · {ft('cancelledCount')}: {counts.cancelled} · {ft('declinedCount')}: {counts.declined}</p>
    {transfers.some(item => item.status === 'failed') && <><p className="small muted">{ft('retryHint')}</p><ul className="preview-files" style={{ maxHeight: '10rem', overflowY: 'auto' }}>{transfers.filter(item => item.status === 'failed').map(item => <li key={transferKey(item)}><label><input type="checkbox" checked={selected.includes(transferKey(item)) && eligible(item)} disabled={running || !eligible(item)} onChange={event => setSelected(current => event.target.checked ? [...current, transferKey(item)] : current.filter(key => key !== transferKey(item)))} /> {ft('selectRetry')}: {item.name} ({item.direction === 'incoming' ? '↓' : '↑'})</label>{!eligible(item) && <small>{ft('retryUnavailable')}</small>}</li>)}</ul><Button onClick={() => void retry()} disabled={!chosen.length || running} busy={running}>{ft('retrySelected')} ({chosen.length})</Button></>}
    {!!results.length && <ul aria-live="polite">{results.map(result => <li key={result.key}>{result.name}: {ft(result.outcome)}</li>)}</ul>}
  </section>
}
