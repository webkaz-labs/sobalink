import { useCallback, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { type Locale, type Peer, type State, type Transfer } from '../api'
import { peerCommunicationAllowed, peerPermissionKey, peerPresenceKey } from '../peer-status'
import './NetworkGraph.css'

export interface NetworkGraphProps {
  state: State
  locale: Locale
  onSelectPeer: (peerId: string) => void
  onSelectSelf: () => void
  view: 'diagram' | 'list'
  onViewChange?: (view: 'diagram' | 'list') => void
  selectedPeerId?: string | null
  visiblePeers?: Peer[]
  onClearFilters?: () => void
}

const en = {
  heading: 'Network graph', intro: 'Select a device or connection to see its details.',
  diagram: 'Known connections from this device', list: 'Device list', showList: 'Show device list', showDiagram: 'Show network diagram',
  onlineCompact: 'Online', offlineCompact: 'Offline', self: 'This device', known: 'Known device', online: 'Network online', offline: 'Network offline', ready: 'Allowed here',
  appUnknown: 'sobalink service not confirmed', identityNeeded: 'Identity unverified', permissionNeeded: 'Permission needed', paused: 'App paused', localPermission: 'Files & messages',
  bridgeConfirmed: 'sobalink confirmed', presenceUnknown: 'Response not confirmed', presenceUnknownCompact: 'Response not confirmed',
  network: 'Selected network', tailnet: 'Tailscale · Tailnet', lan: 'Tailcat', configuredRelay: 'Configured relay', configuredHost: 'Configured host', configurationOnly: 'Configuration only; the route is not reported.',
  selected: 'Selected device', selectHint: 'Select a device for service and transfer details.', knownCount: 'Known devices', confirmedCount: 'sobalink confirmed',
  filteredEmpty: 'No matching devices', filteredEmptyHint: 'Try a different search or network filter.', clearFilters: 'Clear filters',
  transferring: 'Transferring', direct: 'Direct path', relay: 'Relay path', unknown: 'Path unknown',
  listenersCompact: 'Listeners', sharesCompact: 'Sharing rules', activeServices: 'Active services',
  connections: 'Ready connection services', shares: 'Active sharing rules', noListeners: 'No active connections or sharing rules',
  sending: 'Sending', receiving: 'Receiving', progressUnknown: 'progress unknown', transfer: 'File transfer',
  noTransfer: 'No file transfer in progress', saving: 'Saving files', waiting: 'Transfers waiting',
  traffic: 'Network traffic is not measured. Arrows show file-transfer direction. Ready connections have listeners. Sharing rules grant scoped access. Neither confirms application success or traffic.',
  explain: 'About these states', explanation: 'Network online is reported by the network or an authenticated sobalink reply; it does not by itself confirm the sobalink service. Tailcat does not report a separate offline state. An unconfirmed service may still be installed. Allowed here is this device’s stored permission for files and messages. Exchange also needs a confirmed sobalink service, verified network identity, and no pause. The receiving device must also allow communication; its permission is not reported here. Manual service connections have separate requirements. Lines show this device’s known relationships, not traffic or connections between other devices. Paths are shown only when reported; direct does not imply LAN and relay does not imply Internet.',
  empty: 'No known devices yet', emptyHint: 'Open this device to set up a network or pair a device.',
  open: 'Open device', openConnection: 'Open connection', status: 'Status', networkReady: 'Network ready', login: 'Sign-in required', starting: 'Connecting', disabled: 'Network not connected',
  networkUnknown: 'Network unknown', networkUnavailable: 'Network unavailable', routeLegend: 'Solid: reported path to an online device. Dashed: other known relationships.',
} as const
type Labels = { [Key in keyof typeof en]: string }
const ja: Labels = {
  heading: 'ネットワーク図', intro: 'デバイスや接続を選ぶと詳細を確認できます。',
  diagram: 'この端末から確認できる接続', list: 'デバイス一覧', showList: 'デバイス一覧を表示', showDiagram: 'ネットワーク図を表示',
  onlineCompact: 'オンライン', offlineCompact: 'オフライン', self: 'この端末', known: '検出・登録済み', online: 'ネットワーク上でオンライン', offline: 'ネットワーク上でオフライン', ready: 'この端末で許可',
  appUnknown: 'sobalink サービス未確認', identityNeeded: '識別情報が未確認', permissionNeeded: '通信の許可が必要', paused: 'アプリ通信を一時停止中', localPermission: 'ファイルとメッセージ',
  bridgeConfirmed: 'sobalink 確認済み', presenceUnknown: '応答未確認', presenceUnknownCompact: '応答未確認',
  network: '選択中のネットワーク', tailnet: 'Tailscale · Tailnet', lan: 'Tailcat', configuredRelay: '設定済みの中継先', configuredHost: '設定済みのホスト', configurationOnly: '設定情報です。実際の経路は報告されていません。',
  selected: '選択中のデバイス', selectHint: 'デバイスを選ぶとサービスや転送の詳細を確認できます。', knownCount: '登録・検出済み', confirmedCount: 'sobalink 確認済み',
  filteredEmpty: '一致するデバイスがありません', filteredEmptyHint: '検索語やネットワークの絞り込みを変更してください。', clearFilters: '絞り込みを解除',
  transferring: 'ファイル転送中', direct: '直接接続', relay: '中継接続', unknown: '経路不明',
  listenersCompact: '接続待受', sharesCompact: '共有許可', activeServices: '有効なサービス',
  connections: '接続サービスの待受準備完了', shares: '共有の許可', noListeners: '有効な接続・共有許可なし',
  sending: '送信', receiving: '受信', progressUnknown: '進捗不明', transfer: 'ファイル転送',
  noTransfer: '進行中のファイル転送なし', saving: 'ファイルを保存中', waiting: '転送待ち',
  traffic: '通信量は計測していません。矢印はファイル転送の方向を示します。接続の待受準備と共有の許可は、アプリの動作確認や通信中を意味しません。',
  explain: '状態の見方', explanation: 'ネットワーク上でオンラインという表示は、ネットワーク側の状態通知または認証済みの sobalink 応答に基づきます。オンライン表示だけでは sobalink サービスを確認できません。Tailcat は独立したオフライン状態を報告しません。サービス未確認でも未導入とは限りません。「この端末で許可」はファイルとメッセージに対する、この端末に保存された許可です。通信には sobalink サービスとネットワークの識別情報の確認、一時停止されていないことも必要です。受信側の許可も必要ですが、相手側の許可はここでは確認できません。手動のサービス接続は別の条件です。線はこの端末との既知の関係を示し、通信中や他の端末どうしの接続を示しません。経路は報告された場合のみ表示します。直接接続は LAN、中継接続はインターネット経由とは限りません。',
  empty: 'まだデバイスがありません', emptyHint: 'この端末を開いて、ネットワークの設定やデバイスのペアリングを行えます。',
  open: 'デバイスを開く', openConnection: '接続の詳細を開く', status: '状態', networkReady: 'ネットワーク利用準備完了', login: 'ログインが必要', starting: '接続中', disabled: 'ネットワーク未接続',
  networkUnknown: 'ネットワーク不明', networkUnavailable: 'ネットワークを利用できません', routeLegend: '実線：オンライン端末への報告済み経路。破線：その他の既知の関係。',
}

function appLabel(peer: Peer, labels: Labels) {
  const permission = peerPermissionKey(peer)
  return permission === 'paused' ? labels.paused : permission === 'trusted' ? labels.ready : labels.permissionNeeded
}

function presenceLabel(peer: Peer, labels: Labels, compact = false) {
  const presence = peerPresenceKey(peer)
  if (presence === 'online') return compact ? labels.onlineCompact : labels.online
  // Only Tailnet supplies a network-level online bit. Tailcat's false value
  // means no fresh authenticated discovery, not evidence of an offline host.
  if (presence === 'responseUnconfirmed') return compact ? labels.presenceUnknownCompact : labels.presenceUnknown
  return compact ? labels.offlineCompact : labels.offline
}

function selfStatus(status: string, labels: Labels) {
  const value = status.toLowerCase()
  if (['running', 'online', 'ready'].includes(value)) return labels.networkReady
  if (['needslogin', 'needs-login', 'needsmachineauth'].includes(value)) return labels.login
  if (['starting', 'connecting'].includes(value)) return labels.starting
  if (['none', 'stopped', 'idle', 'offline', ''].includes(value)) return labels.disabled
  if (['error', 'unavailable'].includes(value)) return labels.networkUnavailable
  return status
}

function transferProgress(transfers: Transfer[], locale: Locale, labels: Labels) {
  // These counters describe the backend's file-transfer state, never link throughput.
  if (transfers.some(item => !Number.isFinite(item.totalBytes) || item.totalBytes <= 0 ||
    !Number.isFinite(item.completedBytes) || item.completedBytes < 0 || item.completedBytes > item.totalBytes)) return labels.progressUnknown
  const total = transfers.reduce((sum, item) => sum + item.totalBytes, 0)
  const completed = transfers.reduce((sum, item) => sum + item.completedBytes, 0)
  if (!Number.isFinite(total) || !Number.isFinite(completed) || total <= 0) return labels.progressUnknown
  return new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 0 }).format(Math.floor(completed / total * 100) / 100)
}

function peerFacts(peer: Peer, state: State, locale: Locale, labels: Labels) {
  const transfers = state.transfers.filter(item => item.peerId === peer.id)
  const active = transfers.filter(item => item.status === 'transferring')
  const incoming = active.filter(item => item.direction === 'incoming')
  const outgoing = active.filter(item => item.direction === 'outgoing')
  const direction = [outgoing.length ? `${labels.sending} ${transferProgress(outgoing, locale, labels)}` : '',
    incoming.length ? `${labels.receiving} ${transferProgress(incoming, locale, labels)}` : ''].filter(Boolean).join(' · ')
  const pending = transfers.filter(item => ['offered', 'awaiting-acceptance', 'queued'].includes(item.status)).length
  const saving = transfers.filter(item => item.status === 'saving').length
  const transferLabel = active.length ? `${labels.transfer}: ${direction}` : saving ? `${labels.saving}: ${saving}` : pending ? `${labels.waiting}: ${pending}` : labels.noTransfer
  const activeConnections = state.services.filter(item => item.peerId === peer.id && item.status === 'active')
  const connections = activeConnections.length
  // A share applies only to its explicit peer scope, never to every visible device.
  const activeShares = state.shares.filter(item => item.status === 'active' && (item.peerIds ? item.peerIds.includes(peer.id) : item.peerId === peer.id))
  const shares = activeShares.length
  const listeners = [connections ? `${labels.connections}: ${connections}` : '', shares ? `${labels.shares}: ${shares}` : ''].filter(Boolean)
  const path = labels[peer.path === 'direct' ? 'direct' : peer.path === 'relay' ? 'relay' : 'unknown']
  const app = appLabel(peer, labels)
  const networks = peer.networks.map(network => network === 'lan' ? labels.lan : 'Tailnet').join(' · ') || labels.networkUnknown
  return { incoming: incoming.length > 0, outgoing: outgoing.length > 0, active: active.length > 0, hasTransferWork: active.length > 0 || pending > 0 || saving > 0, transferLabel, listeners, path, app,
    networks, serviceSummary: `${labels.listenersCompact} ${connections} · ${labels.sharesCompact} ${shares}`,
    serviceNames: [...activeConnections, ...activeShares].map(item => `${item.name} (${item.network.toUpperCase()})`), description: `${labels.known}; ${presenceLabel(peer, labels)}; ${app}; ${networks}; ${path}; ${listeners.join('; ') || labels.noListeners}; ${transferLabel}; ${peer.bridge ? labels.bridgeConfirmed : labels.appUnknown}${peer.verified ? '' : `; ${labels.identityNeeded}`}` }
}

type Point = { x: number; y: number }
type Connector = {
  line: string; outgoing: string; incoming: string
  source: Point; target: Point; junction: Point; lane: Point; annotation: string; compact: boolean
}

// Every path is continuous from the measured local device port to one peer port.
// SVG is decoration in CSS pixels; labels and keyboard controls remain native HTML.
function useConnectors(peerIds: string, view: NetworkGraphProps['view']) {
  const canvas = useRef<HTMLDivElement>(null)
  const self = useRef<HTMLButtonElement>(null)
  const edges = useRef(new Map<string, HTMLButtonElement>())
  const nodes = useRef(new Map<string, HTMLButtonElement>())
  const lanes = useRef(new Map<string, HTMLSpanElement>())
  const [connectors, setConnectors] = useState<Record<string, Connector>>({})

  const measure = useCallback(() => {
    const bounds = canvas.current?.getBoundingClientRect()
    const local = self.current?.getBoundingClientRect()
    const localGlyph = self.current?.querySelector('.network-graph-device-glyph')?.getBoundingClientRect()
    if (!bounds?.width || !local?.width || !localGlyph?.width) return
    const next: Record<string, Connector> = {}
    nodes.current.forEach((element, peerId) => {
      const node = element.getBoundingClientRect()
      const glyph = element.querySelector('.network-graph-device-glyph')?.getBoundingClientRect()
      const edge = edges.current.get(peerId)?.getBoundingClientRect()
      if (!node.width || !glyph?.width) return
      const wire = lanes.current.get(peerId)?.getBoundingClientRect()
      if (!wire || !edge?.width) return
      const compact = local.right > node.left
      const stacked = edge.top >= glyph.bottom
      const sx = (compact ? localGlyph.left + 2 : localGlyph.right - 2) - bounds.left
      const sy = localGlyph.top + localGlyph.height / 2 - bounds.top
      const tx = glyph.left + 2 - bounds.left
      const ty = glyph.top + glyph.height / 2 - bounds.top
      // The native row reserves a label, a wire slot, and facts. A shared local
      // rail reaches each row; only the final, shallow section bends to its port.
      const ly = stacked ? ty : wire.top + wire.height / 2 - bounds.top
      const jx = compact ? Math.max(12, local.left - bounds.left + 4) : local.right - bounds.left + 20
      const direction = Math.sign(ly - sy)
      const radius = Math.min(14, Math.abs(ly - sy) / 2, Math.abs(sx - jx) / 2)
      const towardRail = compact ? 1 : -1
      const lead = direction === 0 ? `M ${sx} ${sy} H ${jx}`
        : `M ${sx} ${sy} H ${jx + towardRail * radius} Q ${jx} ${sy} ${jx} ${sy + direction * radius} V ${ly - direction * radius} Q ${jx} ${ly} ${jx + radius} ${ly}`
      const curveStart = Math.max(jx + radius, tx - Math.min(60, Math.max(16, (tx - jx) * .3)))
      const curveMiddle = (curveStart + tx) / 2
      const line = `${lead} H ${curveStart} C ${curveMiddle} ${ly}, ${curveMiddle} ${ty}, ${tx} ${ty}`
      const labelX = edge.left + edge.width / 2 - bounds.left
      next[peerId] = {
        line, source: { x: sx, y: sy }, target: { x: tx, y: ty }, junction: { x: jx, y: ly }, lane: { x: labelX, y: ly }, compact,
        annotation: stacked ? '' : `M ${labelX} ${edge.bottom - bounds.top + 3} V ${ly - 4}`,
        outgoing: `M ${tx - 14} ${ty - 5} l 6 5 -6 5`,
        incoming: `M ${Math.max(jx + 22, curveStart - 16)} ${ly - 5} l -6 5 6 5`,
      }

    })
    setConnectors(previous => JSON.stringify(previous) === JSON.stringify(next) ? previous : next)
  }, [])

  // Selection and polling can move rows without changing any observed size.
  // Measure after the DOM commits, before paint, as well as on actual resizes.
  useLayoutEffect(() => { measure() })
  useLayoutEffect(() => {
    if (view !== 'diagram' || !canvas.current || !self.current) return
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    for (const element of [canvas.current, self.current, ...edges.current.values(), ...nodes.current.values(), ...lanes.current.values()]) observer?.observe(element)
    window.addEventListener('resize', measure)
    return () => { observer?.disconnect(); window.removeEventListener('resize', measure) }
  }, [measure, peerIds, view])

  return { canvas, self, edges, nodes, lanes, connectors }
}

function RouteGlyph({ path }: { path: Peer['path'] }) {
  if (path !== 'direct' && path !== 'relay') return null
  return <svg className="network-graph-route-glyph" width="34" height="20" viewBox="0 0 34 20" fill="none" aria-hidden="true"><path d="M7 10H27" /><circle cx="4" cy="10" r="3" /><circle cx="30" cy="10" r="3" />{path === 'relay' && <rect x="13" y="6" width="8" height="8" rx="2" />}</svg>
}
function DeviceGlyph({ local = false }: { local?: boolean }) {
  return <span className={`network-graph-device-glyph ${local ? 'is-local' : 'is-peer'}`} aria-hidden="true"><svg width="120" height="120" viewBox="0 0 120 120" fill="none">
    <circle className="network-graph-glyph-orbit" cx="60" cy="60" r="58" />
    <circle className="network-graph-glyph-halo" cx="60" cy="60" r="49" />
    <circle className="network-graph-glyph-core" cx="60" cy="60" r="40" />
    <path className="network-graph-glyph-detail" d="M 27 34 A 42 42 0 0 1 45 21 M 75 99 A 42 42 0 0 0 93 86" />
    <rect className="network-graph-glyph-device" x="36" y="37" width="48" height="35" rx="5" />
    <path className="network-graph-glyph-screen" d="M 42 43 H 78 V 64 H 42 Z" />
    <path className="network-graph-glyph-device" d="M 54 73 V 81 M 66 73 V 81 M 47 83 H 73" />
    <path className="network-graph-glyph-reflection" d="M 47 49 H 60 M 47 54 H 54" />
  </svg></span>
}

export function NetworkGraph({ state, locale, onSelectPeer, onSelectSelf, view, onViewChange, selectedPeerId, visiblePeers, onClearFilters }: NetworkGraphProps) {
  const labels = locale === 'ja' ? ja : en
  const id = useId()
  const [hoveredPeerId, setHoveredPeerId] = useState<string | null>(null)
  const [focusedPeerId, setFocusedPeerId] = useState<string | null>(null)
  const emphasisRank = (peerId: string) => hoveredPeerId === peerId || focusedPeerId === peerId ? 2 : selectedPeerId === peerId ? 1 : 0
  const emphasized = (peerId: string) => emphasisRank(peerId) > 0
  // Identity ordering keeps positions stable when polling changes names, statuses, or API order.
  const peers = useMemo(() => [...(visiblePeers ?? state.peers)].sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0), [state.peers, visiblePeers])
  const geometry = useConnectors(JSON.stringify(peers.map(peer => peer.id)), view)
  const facts = peers.map(peer => ({ peer, facts: peerFacts(peer, state, locale, labels) }))
  const selfName = state.self.name || labels.self
  const localStatus = selfStatus(state.self.status, labels)
  const selectedNetwork = state.settings?.network ?? (state.self.networks?.length === 1 ? state.self.networks[0] : undefined)
  const selfNetworks = selectedNetwork === 'lan' ? labels.lan : selectedNetwork === 'tailnet' ? labels.tailnet : labels.networkUnknown
  const relayConfiguration = selectedNetwork === 'lan' && state.lan?.configured ? state.lan.relay : undefined
  const selected = facts.find(item => item.peer.id === selectedPeerId)
  const selfButton = <button ref={geometry.self} type="button" className="network-graph-node network-graph-self" onClick={onSelectSelf} aria-label={`${labels.open}: ${selfName}; ${labels.self}; ${localStatus}`}>
    <span className="network-graph-device-heading"><DeviceGlyph local /><span><span className="network-graph-node-kind">{labels.self}</span><strong title={selfName}>{selfName}</strong></span></span>
    <span className="network-graph-local-status">{localStatus}</span><span className="network-graph-node-meta">{selfNetworks}</span>
  </button>

  return <section className="network-graph" data-view={view} aria-labelledby={`${id}-heading`}>
    <header className="network-graph-header"><div><h2 id={`${id}-heading`}>{view === 'diagram' ? labels.heading : labels.list}</h2><p>{labels.intro}</p></div>
      {onViewChange && <button className="button button-secondary network-graph-view-toggle" type="button" onClick={() => onViewChange(view === 'diagram' ? 'list' : 'diagram')} aria-controls={`${id}-content`}>{view === 'diagram' ? labels.showList : labels.showDiagram}</button>}
    </header>
    <ul className="network-graph-legend" aria-label={labels.explain}>
      <li><span className="network-graph-key known" aria-hidden="true" />{labels.knownCount}<strong>{peers.length}</strong></li>
      <li><span className="network-graph-key online" aria-hidden="true" />{labels.onlineCompact}<strong>{peers.filter(peer => peer.online).length}</strong></li>
      <li><span className="network-graph-key confirmed" aria-hidden="true" />{labels.confirmedCount}<strong>{peers.filter(peer => peer.bridge).length}</strong></li>
    </ul>
    <div className="network-graph-context">
      <span className="network-graph-network-label"><span>{labels.network}</span><strong>{selfNetworks}</strong></span>
      {relayConfiguration && <details className="network-graph-configuration"><summary>{relayConfiguration.kind === 'relay' ? labels.configuredRelay : labels.configuredHost}</summary><p>{relayConfiguration.address}</p><p>{labels.configurationOnly}</p></details>}
    </div>
    <div id={`${id}-content`}>
      {view === 'diagram' ? <div className="network-graph-diagram" role="group" aria-label={labels.diagram} aria-describedby={`${id}-traffic`}>
        <div className="network-graph-canvas" ref={geometry.canvas}>
          <svg className="network-graph-connectors" aria-hidden="true">
            {[...facts].sort((a, b) => emphasisRank(a.peer.id) - emphasisRank(b.peer.id)).map(({ peer, facts }) => {
              const connector = geometry.connectors[peer.id]
              return <g key={peer.id} className={`${selectedPeerId === peer.id ? 'is-selected' : ''} ${emphasized(peer.id) ? 'is-emphasized' : ''}`} data-peer-id={peer.id}>
                <path className="network-graph-line-glow" d={connector?.line} />
                <path className="network-graph-line-track" d={connector?.line} />
                <path className={`network-graph-line ${!peer.online || !['direct', 'relay'].includes(peer.path) ? 'is-unconfirmed' : 'is-reported'} ${facts.active ? 'has-transfer' : ''}`} d={connector?.line} />
                <path className="network-graph-annotation" d={connector?.annotation} />
                <path className="network-graph-line-hit" d={connector?.line} onMouseEnter={() => setHoveredPeerId(peer.id)} onMouseLeave={() => setHoveredPeerId(null)} onClick={() => onSelectPeer(peer.id)} />
                {connector && <><circle className="network-graph-junction" cx={connector.junction.x} cy={connector.junction.y} r="4" /><circle className="network-graph-terminal-halo" cx={connector.target.x} cy={connector.target.y} r="8" /><circle className="network-graph-terminal" cx={connector.source.x} cy={connector.source.y} r="4" /><circle className="network-graph-terminal" cx={connector.target.x} cy={connector.target.y} r="4" /></>}
                {facts.outgoing && <path className="network-graph-arrow" d={connector?.outgoing} fill="none" data-direction="outgoing" />}
                {facts.incoming && <path className="network-graph-arrow" d={connector?.incoming} fill="none" data-direction="incoming" />}
              </g>
            })}
          </svg>
          <div className="network-graph-origin">{selfButton}</div>
          <div className="network-graph-branches">
            {facts.map(({ peer, facts }) => <div className={`network-graph-branch ${emphasized(peer.id) ? 'is-emphasized' : ''}`} key={peer.id} data-peer-id={peer.id}
              onMouseEnter={() => setHoveredPeerId(peer.id)} onMouseLeave={() => setHoveredPeerId(null)}
              onFocusCapture={() => setFocusedPeerId(peer.id)} onBlurCapture={event => { if (!event.currentTarget.contains(event.relatedTarget)) setFocusedPeerId(null) }}>
              <div className="network-graph-route-group">
                <button ref={element => { if (element) geometry.edges.current.set(peer.id, element); else geometry.edges.current.delete(peer.id) }} type="button" data-path={peer.path}
                  className={`network-graph-edge ${facts.active ? 'has-transfer' : ''} ${selectedPeerId === peer.id ? 'is-selected' : ''}`} aria-pressed={selectedPeerId === peer.id} aria-label={`${labels.openConnection}: ${selfName} — ${peer.name}; ${facts.description}`} onClick={() => onSelectPeer(peer.id)}>
                  <span className="network-graph-route"><RouteGlyph path={peer.path} /><span className="network-graph-path">{facts.path}</span></span>
                </button>
                <span className="network-graph-wire-slot" aria-hidden="true" ref={element => { if (element) geometry.lanes.current.set(peer.id, element); else geometry.lanes.current.delete(peer.id) }} />
                <span className="network-graph-lane-meta"><span>{facts.networks}</span>{facts.listeners.length > 0 && <span className="network-graph-lane-counts">{facts.serviceSummary}</span>}</span>
                {facts.hasTransferWork && <span className={`network-graph-transfer ${facts.active ? 'has-transfer' : ''}`}>{facts.transferLabel}</span>}
              </div>
              <button ref={element => { if (element) geometry.nodes.current.set(peer.id, element); else geometry.nodes.current.delete(peer.id) }} type="button" className={`network-graph-node ${peerCommunicationAllowed(peer) ? 'is-ready' : ''} ${selectedPeerId === peer.id ? 'is-selected' : ''}`} aria-label={`${labels.open}: ${peer.name}; ${facts.description}`} aria-pressed={selectedPeerId === peer.id} onClick={() => onSelectPeer(peer.id)}>
                <span className="network-graph-device-heading"><DeviceGlyph /><span><span className="network-graph-node-kind">{labels.known}</span><strong title={peer.name}>{peer.name}</strong></span></span>
                <span className="network-graph-node-meta"><span className="network-graph-online-status"><span className={`network-graph-key ${peer.online ? 'online' : 'offline'}`} aria-hidden="true" /><span>{presenceLabel(peer, labels, true)}</span></span></span>
                <span className={`network-graph-app ${peer.bridge ? 'is-confirmed' : ''}`}>{peer.bridge ? labels.bridgeConfirmed : labels.appUnknown}</span>
              </button>
            </div>)}
          </div>
        </div>
        <div className="network-graph-selection" aria-live="polite" aria-atomic="true">
          {selected ? <><span className="network-graph-selection-title"><span>{labels.selected}</span><strong>{selected.peer.name}</strong><span>{labels.localPermission}: {selected.facts.app}</span></span><div className="network-graph-selected-facts"><span>{selected.facts.listeners.join(' · ') || labels.noListeners}</span>{selected.facts.serviceNames.length > 0 && <span><span className="network-graph-facts-label">{labels.activeServices}: </span>{selected.facts.serviceNames.join(' · ')}</span>}<span>{selected.facts.transferLabel}</span></div></> : <p>{labels.selectHint}</p>}
        </div>
      </div> : <div className="network-graph-list" role="group" aria-label={labels.list}>
        {selfButton}
        <ul>{facts.map(({ peer, facts }) => <li key={peer.id}>
          <button type="button" className={`network-graph-list-peer ${selectedPeerId === peer.id ? 'is-selected' : ''}`} onClick={() => onSelectPeer(peer.id)} aria-pressed={selectedPeerId === peer.id} aria-label={`${labels.open}: ${peer.name}; ${facts.description}`}>
            <span className="network-graph-list-title"><strong>{peer.name}</strong><span className="network-graph-node-kind">{labels.known}</span></span>
            <span className="network-graph-list-state"><span className={`network-graph-key ${peer.online ? 'online' : 'offline'}`} aria-hidden="true" />{presenceLabel(peer, labels)}<span>·</span><span className={`network-graph-app ${peer.bridge ? 'is-confirmed' : ''}`}>{peer.bridge ? labels.bridgeConfirmed : labels.appUnknown}</span><span>·</span>{facts.path}<span className="network-graph-networks">{facts.networks}</span></span>
            <span className="network-graph-list-permission">{labels.localPermission}: {facts.app}</span>
            <span className="network-graph-listeners">{facts.listeners.join(' · ') || labels.noListeners}</span>
            <span className={`network-graph-transfer ${facts.active ? 'has-transfer' : ''}`}>{facts.transferLabel}</span>
          </button>
        </li>)}</ul>
      </div>}
    </div>
    {!peers.length && <div className="network-graph-empty"><h3>{state.peers.length ? labels.filteredEmpty : labels.empty}</h3><p>{state.peers.length ? labels.filteredEmptyHint : labels.emptyHint}</p>{state.peers.length > 0 && onClearFilters && <button type="button" className="button button-secondary" onClick={onClearFilters}>{labels.clearFilters}</button>}</div>}
    <footer className="network-graph-footer"><p className="network-graph-route-legend"><span className="network-graph-line-sample" aria-hidden="true" />{labels.routeLegend}</p><p id={`${id}-traffic`}>{labels.traffic}</p><details><summary>{labels.explain}</summary><p>{labels.explanation}</p></details></footer>
  </section>
}
