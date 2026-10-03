import { useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { canExchange, type Locale, type Peer, type State, type Transfer } from '../api'
import './NetworkGraph.css'

export interface NetworkGraphProps {
  state: State
  locale: Locale
  onSelectPeer: (peerId: string) => void
  onSelectSelf: () => void
  view: 'diagram' | 'list'
  onViewChange?: (view: 'diagram' | 'list') => void
  selectedPeerId?: string | null
}

const en = {
  heading: 'Network graph', intro: 'Select a device or connection to see its details.',
  diagram: 'Known connections from this device', list: 'Device list', showList: 'Show device list', showDiagram: 'Show network diagram',
  self: 'This device', known: 'Known device', online: 'Network online', offline: 'Network offline', ready: 'Allowed here',
  appUnknown: 'sobalink service not confirmed', identityNeeded: 'Identity unverified', permissionNeeded: 'Permission needed', paused: 'App paused', appOffline: 'App offline',
  transferring: 'Transferring', direct: 'Direct path', relay: 'Relay path', unknown: 'Path unknown',
  connections: 'Ready connection services', shares: 'Active sharing rules', noListeners: 'No active connections or sharing rules',
  sending: 'Sending', receiving: 'Receiving', progressUnknown: 'progress unknown', transfer: 'File transfer',
  noTransfer: 'No file transfer in progress', saving: 'Saving files', waiting: 'Transfers waiting',
  traffic: 'Network traffic is not measured. Arrows show file-transfer direction. Ready connections have listeners. Sharing rules grant scoped access. Neither confirms application success or traffic.',
  explain: 'About these states', explanation: 'Known devices may be offline. Network online is reported by the network or an authenticated sobalink reply; it does not by itself confirm the sobalink service. Allowed here means a confirmed sobalink service, verified identity, local communication permission, and no pause. The receiving device must also allow communication; its permission is not reported here. Lines show this device’s known relationships, not connections between other devices. Paths are shown only when reported.',
  empty: 'No known devices yet', emptyHint: 'Open this device to set up a network or pair a device.',
  open: 'Open device', openConnection: 'Open connection', status: 'Status', networkReady: 'Network ready', login: 'Sign-in required', starting: 'Connecting', disabled: 'Network not connected',
  networkUnknown: 'Network unknown', networkUnavailable: 'Network unavailable',
} as const
type Labels = { [Key in keyof typeof en]: string }
const ja: Labels = {
  heading: 'ネットワーク図', intro: 'デバイスや接続を選ぶと詳細を確認できます。',
  diagram: 'この端末から確認できる接続', list: 'デバイス一覧', showList: 'デバイス一覧を表示', showDiagram: 'ネットワーク図を表示',
  self: 'この端末', known: '検出・登録済み', online: 'ネットワーク上でオンライン', offline: 'ネットワーク上でオフライン', ready: 'この端末で許可',
  appUnknown: 'sobalink サービス未確認', identityNeeded: '識別情報が未確認', permissionNeeded: '通信の許可が必要', paused: 'アプリ通信を一時停止中', appOffline: 'アプリはオフライン',
  transferring: 'ファイル転送中', direct: '直接接続', relay: '中継接続', unknown: '経路不明',
  connections: '接続サービスの待受準備完了', shares: '共有の許可', noListeners: '有効な接続・共有許可なし',
  sending: '送信', receiving: '受信', progressUnknown: '進捗不明', transfer: 'ファイル転送',
  noTransfer: '進行中のファイル転送なし', saving: 'ファイルを保存中', waiting: '転送待ち',
  traffic: '通信量は計測していません。矢印はファイル転送の方向を示します。接続の待受準備と共有の許可は、アプリの動作確認や通信中を意味しません。',
  explain: '状態の見方', explanation: '検出・登録済みの端末がオンラインとは限りません。ネットワーク上でオンラインという表示は、ネットワーク側の状態通知または認証済みの sobalink 応答に基づきます。オンライン表示だけでは sobalink サービスの応答を確認できません。「この端末で許可」は sobalink サービスと識別情報の確認、この端末での通信許可、一時停止されていないことを表します。受信側でも通信の許可が必要です。相手側の許可状態はここでは確認できません。線はこの端末との既知の関係を示し、他の端末どうしの接続は示しません。経路は報告された場合のみ表示します。',
  empty: 'まだデバイスがありません', emptyHint: 'この端末を開いて、ネットワークの設定やデバイスのペアリングを行えます。',
  open: 'デバイスを開く', openConnection: '接続の詳細を開く', status: '状態', networkReady: 'ネットワーク利用準備完了', login: 'ログインが必要', starting: '接続中', disabled: 'ネットワーク未接続',
  networkUnknown: 'ネットワーク不明', networkUnavailable: 'ネットワークを利用できません',
}

function appLabel(peer: Peer, labels: Labels) {
  if (!peer.bridge) return labels.appUnknown
  if (!peer.verified) return labels.identityNeeded
  if (!peer.trusted) return labels.permissionNeeded
  if (peer.autosave?.paused) return labels.paused
  return canExchange(peer) ? labels.ready : labels.appOffline
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
  const connections = state.services.filter(item => item.peerId === peer.id && item.status === 'active').length
  // A share applies only to its explicit peer scope, never to every visible device.
  const shares = state.shares.filter(item => item.status === 'active' && (item.peerIds ? item.peerIds.includes(peer.id) : item.peerId === peer.id)).length
  const listeners = [connections ? `${labels.connections}: ${connections}` : '', shares ? `${labels.shares}: ${shares}` : ''].filter(Boolean)
  const path = labels[peer.path === 'direct' ? 'direct' : peer.path === 'relay' ? 'relay' : 'unknown']
  const app = appLabel(peer, labels)
  const networks = peer.networks.map(network => network === 'lan' ? 'LAN' : 'Tailnet').join(' · ') || labels.networkUnknown
  return { incoming: incoming.length > 0, outgoing: outgoing.length > 0, active: active.length > 0, transferLabel, listeners, path, app,
    networks, description: `${labels.known}; ${peer.online ? labels.online : labels.offline}; ${app}; ${networks}; ${path}; ${listeners.join('; ') || labels.noListeners}; ${transferLabel}` }
}

type Connector = { line: string; outgoing: string; incoming: string }

// SVG draws only decoration in CSS pixels. Native HTML controls determine the layout
// and keep text, focus rings, and hit targets readable at every panel width.
function useConnectors(peerIds: string, view: NetworkGraphProps['view']) {
  const canvas = useRef<HTMLDivElement>(null)
  const self = useRef<HTMLButtonElement>(null)
  const edges = useRef(new Map<string, HTMLButtonElement>())
  const nodes = useRef(new Map<string, HTMLButtonElement>())
  const [connectors, setConnectors] = useState<Record<string, Connector>>({})

  useLayoutEffect(() => {
    if (view !== 'diagram' || !canvas.current || !self.current) return
    const measure = () => {
      const bounds = canvas.current?.getBoundingClientRect()
      const local = self.current?.getBoundingClientRect()
      const localGlyph = self.current?.querySelector('.network-graph-device-glyph')?.getBoundingClientRect()
      if (!bounds?.width || !local?.width || !localGlyph?.width) return
      const next: Record<string, Connector> = {}
      edges.current.forEach((element, peerId) => {
        const edge = element.getBoundingClientRect()
        const node = nodes.current.get(peerId)?.getBoundingClientRect()
        const nodeGlyph = nodes.current.get(peerId)?.querySelector('.network-graph-device-glyph')?.getBoundingClientRect()
        if (!edge.width || !node?.width || !nodeGlyph?.width) return
        const ex = edge.left - bounds.left
        const ey = edge.top + edge.height / 2 - bounds.top
        const compact = local.right > edge.left
        const sx = (compact ? localGlyph.left + 2 : localGlyph.right - 2) - bounds.left
        const sy = localGlyph.top + localGlyph.height / 2 - bounds.top
        const shoulder = local.right + 12 - bounds.left
        const spine = local.left - bounds.left
        const bend = (shoulder + ex) / 2
        // Curves attach to the illustrated device, then route outside its caption.
        // Every branch starts at this device; no peer-to-peer relationship is inferred.
        const source = compact
          ? `M ${sx} ${sy} Q ${spine} ${sy} ${spine} ${sy + 12} V ${ey - 10} Q ${spine} ${ey} ${spine + 10} ${ey} H ${ex}`
          : `M ${sx} ${sy} H ${shoulder} C ${bend} ${sy}, ${bend} ${ey}, ${ex} ${ey}`
        const stacked = edge.bottom <= node.top
        const tx = (stacked ? nodeGlyph.left + nodeGlyph.width / 2 : nodeGlyph.left + 2) - bounds.left
        const ty = (stacked ? nodeGlyph.top + 2 : nodeGlyph.top + nodeGlyph.height / 2) - bounds.top
        const ox = (stacked ? edge.left + edge.width / 2 : edge.right) - bounds.left
        const oy = (stacked ? edge.bottom : edge.top + edge.height / 2) - bounds.top
        const middle = (ox + tx) / 2
        const target = stacked
          ? `M ${ox} ${oy} C ${ox} ${(oy + ty) / 2}, ${tx} ${(oy + ty) / 2}, ${tx} ${ty}`
          : `M ${ox} ${oy} C ${middle} ${oy}, ${middle} ${ty}, ${tx} ${ty}`
        next[peerId] = {
          line: `${source} ${target}`,
          outgoing: stacked ? `M ${tx - 4} ${ty - 11} l 4 5 4 -5` : `M ${tx - 11} ${ty - 4} l 5 4 -5 4`,
          incoming: `M ${ex - 5} ${ey - 4} l -5 4 5 4`,
        }
      })
      setConnectors(previous => JSON.stringify(previous) === JSON.stringify(next) ? previous : next)
    }
    measure()
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    for (const element of [canvas.current, self.current, ...edges.current.values(), ...nodes.current.values()]) observer?.observe(element)
    window.addEventListener('resize', measure)
    return () => { observer?.disconnect(); window.removeEventListener('resize', measure) }
  }, [peerIds, view])

  return { canvas, self, edges, nodes, connectors }
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

export function NetworkGraph({ state, locale, onSelectPeer, onSelectSelf, view, onViewChange, selectedPeerId }: NetworkGraphProps) {
  const labels = locale === 'ja' ? ja : en
  const id = useId()
  // Identity ordering keeps positions stable when polling changes names, statuses, or API order.
  const peers = useMemo(() => [...state.peers].sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0), [state.peers])
  const geometry = useConnectors(JSON.stringify(peers.map(peer => peer.id)), view)
  const facts = peers.map(peer => ({ peer, facts: peerFacts(peer, state, locale, labels) }))
  const selfName = state.self.name || labels.self
  const localStatus = selfStatus(state.self.status, labels)
  const selectedNetworks = state.self.networks || (state.settings?.network && state.settings.network !== 'none' ? [state.settings.network] : [])
  const selfNetworks = selectedNetworks.map(network => network === 'lan' ? 'LAN' : 'Tailnet').join(' · ') || labels.networkUnknown
  const selfButton = <button ref={geometry.self} type="button" className="network-graph-node network-graph-self" onClick={onSelectSelf} aria-label={`${labels.open}: ${selfName}; ${labels.self}; ${localStatus}`}>
    <span className="network-graph-device-heading"><DeviceGlyph local /><span><span className="network-graph-node-kind">{labels.self}</span><strong title={selfName}>{selfName}</strong></span></span>
    <span className="network-graph-local-status">{localStatus}</span><span className="network-graph-node-meta">{selfNetworks}</span>
  </button>

  return <section className="network-graph" data-view={view} aria-labelledby={`${id}-heading`}>
    <header className="network-graph-header"><div><h2 id={`${id}-heading`}>{view === 'diagram' ? labels.heading : labels.list}</h2><p>{labels.intro}</p></div>
      {onViewChange && <button className="button button-secondary network-graph-view-toggle" type="button" onClick={() => onViewChange(view === 'diagram' ? 'list' : 'diagram')} aria-controls={`${id}-content`}>{view === 'diagram' ? labels.showList : labels.showDiagram}</button>}
    </header>
    <ul className="network-graph-legend" aria-label={labels.explain}>
      <li><span className="network-graph-key known" aria-hidden="true" />{labels.known}</li>
      <li><span className="network-graph-key online" aria-hidden="true" />{labels.online}</li>
      <li><span className="network-graph-key ready" aria-hidden="true" />{labels.ready}</li>
      <li><span className="network-graph-key transferring" aria-hidden="true">→</span>{labels.transferring}</li>
    </ul>
    <div id={`${id}-content`}>
      {view === 'diagram' ? <div className="network-graph-diagram" role="group" aria-label={labels.diagram} aria-describedby={`${id}-traffic`}>
        <div className="network-graph-canvas" ref={geometry.canvas}>
          <svg className="network-graph-connectors" aria-hidden="true">
            {facts.map(({ peer, facts }) => <g key={peer.id} className={selectedPeerId === peer.id ? 'is-selected' : ''} data-peer-id={peer.id}>
              <path className={`network-graph-line ${facts.active ? 'has-transfer' : ''}`} d={geometry.connectors[peer.id]?.line} />
              {facts.outgoing && <path className="network-graph-arrow" d={geometry.connectors[peer.id]?.outgoing} fill="none" data-direction="outgoing" />}
              {facts.incoming && <path className="network-graph-arrow" d={geometry.connectors[peer.id]?.incoming} fill="none" data-direction="incoming" />}
            </g>)}
          </svg>
          <div className="network-graph-origin">{selfButton}</div>
          <div className="network-graph-branches">
            {facts.map(({ peer, facts }) => <div className="network-graph-branch" key={peer.id} data-peer-id={peer.id}>
              <button ref={element => { if (element) geometry.edges.current.set(peer.id, element); else geometry.edges.current.delete(peer.id) }} type="button" className={`network-graph-edge ${facts.active ? 'has-transfer' : ''} ${selectedPeerId === peer.id ? 'is-selected' : ''}`} aria-label={`${labels.openConnection}: ${selfName} — ${peer.name}; ${facts.description}`} onClick={() => onSelectPeer(peer.id)}>
                <span className="network-graph-path">{facts.path}</span>
                <span className="network-graph-listeners">{facts.listeners.length ? facts.listeners.map(text => <span key={text}>{text}</span>) : labels.noListeners}</span>
                <span className="network-graph-transfer">{facts.transferLabel}</span>
              </button>
              <button ref={element => { if (element) geometry.nodes.current.set(peer.id, element); else geometry.nodes.current.delete(peer.id) }} type="button" className={`network-graph-node ${canExchange(peer) ? 'is-ready' : ''} ${selectedPeerId === peer.id ? 'is-selected' : ''}`} aria-label={`${labels.open}: ${peer.name}; ${facts.description}`} aria-pressed={selectedPeerId === peer.id} onClick={() => onSelectPeer(peer.id)}>
                <span className="network-graph-device-heading"><DeviceGlyph /><span><span className="network-graph-node-kind">{labels.known}</span><strong title={peer.name}>{peer.name}</strong></span></span>
                <span className="network-graph-node-meta"><span className={`network-graph-key ${peer.online ? 'online' : 'offline'}`} aria-hidden="true" />{peer.online ? labels.online : labels.offline}<span className="network-graph-networks">{facts.networks}</span></span>
                <span className={`network-graph-app ${canExchange(peer) ? 'is-ready' : ''}`}>{facts.app}</span>
              </button>
            </div>)}
          </div>
        </div>
      </div> : <div className="network-graph-list" role="group" aria-label={labels.list}>
        {selfButton}
        <ul>{facts.map(({ peer, facts }) => <li key={peer.id}>
          <button type="button" className={`network-graph-list-peer ${selectedPeerId === peer.id ? 'is-selected' : ''}`} onClick={() => onSelectPeer(peer.id)} aria-pressed={selectedPeerId === peer.id} aria-label={`${labels.open}: ${peer.name}; ${facts.description}`}>
            <span className="network-graph-list-title"><strong>{peer.name}</strong><span className="network-graph-node-kind">{labels.known}</span></span>
            <span className="network-graph-list-state"><span className={`network-graph-key ${peer.online ? 'online' : 'offline'}`} aria-hidden="true" />{peer.online ? labels.online : labels.offline}<span>·</span><span className={`network-graph-app ${canExchange(peer) ? 'is-ready' : ''}`}>{facts.app}</span><span>·</span>{facts.path}<span className="network-graph-networks">{facts.networks}</span></span>
            <span className="network-graph-listeners">{facts.listeners.join(' · ') || labels.noListeners}</span>
            <span className={`network-graph-transfer ${facts.active ? 'has-transfer' : ''}`}>{facts.transferLabel}</span>
          </button>
        </li>)}</ul>
      </div>}
    </div>
    {!peers.length && <div className="network-graph-empty"><h3>{labels.empty}</h3><p>{labels.emptyHint}</p></div>}
    <footer className="network-graph-footer"><p id={`${id}-traffic`}>{labels.traffic}</p><details><summary>{labels.explain}</summary><p>{labels.explanation}</p></details></footer>
  </section>
}
