import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Peer, State, Transfer } from '../api'
import { NetworkGraph, type NetworkGraphProps } from './NetworkGraph'

function peer(id: string, extra: Partial<Peer> = {}): Peer {
  return { id, name: id, networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'unknown', ...extra }
}
function state(extra: Partial<State> = {}): State {
  return { csrfToken: '', self: { name: 'Local device', status: 'online', networks: ['tailnet'] }, peers: [peer('Studio')], services: [], shares: [], messages: [], transfers: [], ...extra }
}
function transfer(extra: Partial<Transfer> = {}): Transfer {
  return { id: 'transfer-one', peerId: 'Studio', direction: 'outgoing', name: 'Files', entries: [], totalBytes: 400, completedBytes: 100, status: 'transferring', createdAt: '2026-10-02T10:00:00Z', ...extra }
}
function props(extra: Partial<NetworkGraphProps> = {}): NetworkGraphProps {
  return { state: state(), locale: 'en', view: 'diagram', onViewChange: vi.fn(), onSelectPeer: vi.fn(), onSelectSelf: vi.fn(), ...extra }
}
function diagram() { return within(screen.getByRole('group', { name: 'Known connections from this device' })) }

describe('network diagram evidence and interaction', () => {
  it('keeps known, online, permitted app readiness, and transfer state distinct', () => {
    render(<NetworkGraph {...props({ state: state({ peers: [
      peer('Known offline', { online: false }), peer('Needs permission', { trusted: false }), peer('Ready'),
      peer('Identity unknown', { verified: false }), peer('Service unknown', { bridge: false }),
    ] }) })} />)
    const graph = diagram()
    expect(graph.getByRole('button', { name: /^Open device: Known offline;/ })).toHaveTextContent('Known deviceKnown offlineNetwork offlineTailnetApp offline')
    expect(graph.getByRole('button', { name: /^Open device: Needs permission;/ })).toHaveTextContent('Network onlineTailnetPermission needed')
    expect(graph.getByRole('button', { name: /^Open device: Ready;/ })).toHaveTextContent('Allowed here')
    expect(graph.getByRole('button', { name: /^Open device: Identity unknown;/ })).toHaveTextContent('Identity unverified')
    expect(graph.getByRole('button', { name: /^Open device: Service unknown;/ })).toHaveTextContent('sobalink service not confirmed')
    expect(graph.getAllByText('No file transfer in progress')).toHaveLength(5)
    expect(graph.queryByText(/Ordinary|not installed/i)).not.toBeInTheDocument()
  })

  it.each([
    { locale: 'en' as const, group: 'Known connections from this device', open: /^Open device: Studio;/, online: 'Network online', unconfirmed: 'sobalink service not confirmed', ready: 'Allowed here', explanation: /Network online is reported by the network or an authenticated sobalink reply/ },
    { locale: 'ja' as const, group: 'この端末から確認できる接続', open: /^デバイスを開く: Studio;/, online: 'ネットワーク上でオンライン', unconfirmed: 'sobalink サービス未確認', ready: 'この端末で許可', explanation: /ネットワーク側の状態通知または認証済みの sobalink 応答/ },
  ])('does not treat network presence as a confirmed sobalink reply in $locale', ({ locale, group, open, online, unconfirmed, ready, explanation }) => {
    const { container } = render(<NetworkGraph {...props({ locale, state: state({ peers: [peer('Studio', { online: true, bridge: false })] }) })} />)
    const node = within(screen.getByRole('group', { name: group })).getByRole('button', { name: open })
    expect(node).toHaveTextContent(online)
    expect(node).toHaveTextContent(unconfirmed)
    expect(node).not.toHaveTextContent(ready)
    expect(screen.getByText(explanation)).toBeInTheDocument()
    expect(container.querySelectorAll('[data-direction], .has-transfer')).toHaveLength(0)
  })

  it('does not turn ready services, acknowledgements, or configuration into active traffic', () => {
    const { container } = render(<NetworkGraph {...props({ state: state({
      peers: [peer('Studio', { autosave: { enabled: true, paused: true } }), peer('Other')],
      services: [
        { id: 'connection', peerId: 'Studio', name: 'HTTP', network: 'tcp', status: 'active' },
        { id: 'saved', peerId: 'Studio', name: 'Saved', network: 'tcp', status: 'saved' },
      ],
      shares: [
        { id: 'scoped', peerId: 'Other', peerIds: ['Studio'], name: 'Scoped share', network: 'tcp', status: 'active' },
        { id: 'unscoped', peerId: '', name: 'No scope', network: 'tcp', status: 'active' },
        { id: 'stopped', peerId: 'Studio', name: 'Stopped share', network: 'tcp', status: 'stopped' },
      ],
      availableServices: [{ id: 'advertised', peerId: 'Studio', name: 'Advertised', network: 'tcp', status: 'active' }],
      messages: [{ id: 'message', peerId: 'Studio', direction: 'outgoing', text: 'Hello', status: 'sent', createdAt: '2026-10-02T10:00:00Z' }],
    }) })} />)
    const edge = diagram().getByRole('button', { name: /^Open connection: Local device — Studio;/ })
    expect(edge).toHaveTextContent('Ready connection services: 1')
    expect(edge).toHaveTextContent('Active sharing rules: 1')
    expect(edge).toHaveTextContent('No file transfer in progress')
    expect(edge).toHaveAccessibleName(/App paused/)
    expect(diagram().getByRole('button', { name: /^Open connection: Local device — Other;/ })).toHaveTextContent('No active connections or sharing rules')
    expect(container.querySelectorAll('[data-direction]')).toHaveLength(0)
    expect(container.querySelectorAll('.has-transfer, animate, animateMotion')).toHaveLength(0)
    expect(screen.getByText(/Network traffic is not measured/)).toBeInTheDocument()
    expect(container.textContent).not.toMatch(/\b(?:Mbps|MB\/s|KB\/s|throughput)\b/)
  })

  it('uses authoritative active transfer counters and explicitly labels logical direction', () => {
    const { container } = render(<NetworkGraph {...props({ state: state({ transfers: [
      transfer(), transfer({ id: 'second', totalBytes: 600, completedBytes: 200 }),
      transfer({ id: 'incoming', direction: 'incoming', totalBytes: 100, completedBytes: 50 }),
      transfer({ id: 'old', status: 'completed', totalBytes: 9000, completedBytes: 9000 }),
      transfer({ id: 'unknown-peer', peerId: 'Not known' }),
    ] }) })} />)
    const edge = diagram().getByRole('button', { name: /^Open connection:/ })
    expect(edge).toHaveTextContent('File transfer: Sending 30% · Receiving 50%')
    expect(container.querySelectorAll('[data-direction="incoming"]')).toHaveLength(1)
    expect(container.querySelectorAll('[data-direction="outgoing"]')).toHaveLength(1)
    expect(container.querySelectorAll('g[data-peer-id]')).toHaveLength(1)
    expect(screen.getByText(/Arrows show file-transfer direction/)).toBeInTheDocument()
  })

  it.each(['offered', 'awaiting-acceptance', 'queued', 'saving', 'completed', 'cancelled', 'failed', 'declined'] as const)('does not draw transfer arrows for %s', status => {
    const { container } = render(<NetworkGraph {...props({ state: state({ transfers: [transfer({ status })] }) })} />)
    expect(container.querySelectorAll('[data-direction], .has-transfer')).toHaveLength(0)
    expect(diagram().getByRole('button', { name: /^Open connection:/ })).not.toHaveTextContent(/Sending|Receiving/)
  })

  it.each([{ totalBytes: 0 }, { totalBytes: Number.NaN }, { completedBytes: -1 }, { completedBytes: 401 }])('labels invalid transfer progress as unknown (%j)', invalid => {
    render(<NetworkGraph {...props({ state: state({ transfers: [transfer(invalid)] }) })} />)
    expect(diagram().getByRole('button', { name: /^Open connection:/ })).toHaveTextContent('Sending progress unknown')
  })

  it('keeps unknown paths unknown even when LAN configuration mentions a relay', () => {
    render(<NetworkGraph {...props({ state: state({
      peers: [peer('Studio', { networks: ['lan'], path: 'unknown' })],
      lan: { configured: true, pairingReady: true, path: 'relay', relay: { kind: 'relay', address: 'relay.example.invalid:443' } },
    }) })} />)
    const graph = diagram()
    expect(graph.getByRole('button', { name: /^Open connection:/ })).toHaveTextContent('Path unknown')
    expect(graph.queryByText('Direct path')).not.toBeInTheDocument()
    expect(graph.queryByText('Relay path')).not.toBeInTheDocument()
  })

  it('shows reported direct and relay paths without making peer-to-peer connections', () => {
    const { container } = render(<NetworkGraph {...props({ state: state({ peers: [peer('Direct', { path: 'direct' }), peer('Relay', { path: 'relay' })] }) })} />)
    expect(diagram().getByText('Direct path')).toBeInTheDocument()
    expect(diagram().getByText('Relay path')).toBeInTheDocument()
    expect(diagram().getAllByRole('button', { name: /^Open connection: Local device —/ })).toHaveLength(2)
    expect(container.querySelectorAll('.network-graph-line')).toHaveLength(2)
  })

  it('opens only the requested peer or local details with mouse, Enter, and Space', async () => {
    const handlers = props()
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    render(<NetworkGraph {...handlers} />)
    const graph = diagram()
    const node = graph.getByRole('button', { name: /^Open device: Studio;/ })
    node.focus()
    await userEvent.keyboard('{Enter}')
    const edge = graph.getByRole('button', { name: /^Open connection:/ })
    edge.focus()
    await userEvent.keyboard(' ')
    expect(handlers.onSelectPeer).toHaveBeenCalledTimes(2)
    expect(handlers.onSelectPeer).toHaveBeenNthCalledWith(1, 'Studio')
    expect(handlers.onSelectPeer).toHaveBeenNthCalledWith(2, 'Studio')
    await userEvent.click(graph.getByRole('button', { name: /^Open device: Local device;/ }))
    expect(handlers.onSelectSelf).toHaveBeenCalledOnce()
    expect(fetch).not.toHaveBeenCalled()
  })

  it.each([
    { locale: 'en' as const, graphHeading: 'Network graph', listHeading: 'Device list', showList: 'Show device list', showGraph: 'Show network diagram', open: /^Open device: Studio;/ },
    { locale: 'ja' as const, graphHeading: 'ネットワーク図', listHeading: 'デバイス一覧', showList: 'デバイス一覧を表示', showGraph: 'ネットワーク図を表示', open: /^デバイスを開く: Studio;/ },
  ])('renders only the parent-selected mode with its truthful heading in $locale', async ({ locale, graphHeading, listHeading, showList, showGraph, open }) => {
    const handlers = props({ locale, selectedPeerId: 'Studio' })
    const { container, rerender } = render(<NetworkGraph {...handlers} />)
    const toggle = screen.getByRole('button', { name: showList })
    toggle.focus()
    await userEvent.keyboard('{Enter}')
    expect(handlers.onViewChange).toHaveBeenCalledExactlyOnceWith('list')
    // Parent navigation owns the mode. Requesting a change never silently diverges from it.
    expect(container.querySelector('.network-graph')).toHaveAttribute('data-view', 'diagram')
    expect(screen.getByRole('heading', { name: graphHeading })).toBeInTheDocument()
    expect(container.querySelector('.network-graph-list')).not.toBeInTheDocument()
    rerender(<NetworkGraph {...handlers} view="list" />)
    expect(screen.getByRole('heading', { name: listHeading })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: graphHeading })).not.toBeInTheDocument()
    expect(container.querySelector('.network-graph-diagram')).not.toBeInTheDocument()
    const list = within(screen.getByRole('group', { name: listHeading }))
    const item = list.getByRole('button', { name: open })
    expect(item).toHaveAttribute('aria-pressed', 'true')
    expect(item).toHaveAccessibleName(locale === 'en' ? /Path unknown; No active connections or sharing rules; No file transfer in progress/ : /経路不明; 有効な接続・共有許可なし; 進行中のファイル転送なし/)
    item.focus()
    await userEvent.keyboard(' ')
    expect(handlers.onSelectPeer).toHaveBeenCalledWith('Studio')
    await userEvent.click(screen.getByRole('button', { name: showGraph }))
    expect(handlers.onViewChange).toHaveBeenLastCalledWith('diagram')
    rerender(<NetworkGraph {...handlers} view="diagram" />)
    expect(screen.getByRole('heading', { name: graphHeading })).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: open })).toHaveLength(1)
  })

  it('anchors branches to illustrated device glyphs outside their captions and redraws when the panel narrows', () => {
    let compact = false
    let notifyResize = () => {}
    const observe = vi.fn()
    const disconnect = vi.fn()
    vi.stubGlobal('ResizeObserver', class {
      constructor(callback: () => void) { notifyResize = callback }
      observe = observe
      disconnect = disconnect
    })
    const rect = (x: number, y: number, width: number, height: number) => ({ x, y, left: x, top: y, right: x + width, bottom: y + height, width, height, toJSON: () => ({}) })
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const second = this.closest('.network-graph-branch')?.getAttribute('data-peer-id') === 'B'
      if (this.classList.contains('network-graph-canvas')) return rect(100, 100, compact ? 320 : 820, compact ? 800 : 360)
      if (this.classList.contains('network-graph-device-glyph')) {
        if (this.closest('.network-graph-self')) return compact ? rect(128, 128, 96, 96) : rect(144, 128, 112, 112)
        return compact ? rect(160, second ? 720 : 420, 72, 72) : rect(526, second ? 288 : 128, 72, 72)
      }
      if (this.classList.contains('network-graph-self')) return rect(120, 120, compact ? 260 : 160, 132)
      if (this.classList.contains('network-graph-edge')) return compact ? rect(152, second ? 600 : 300, 248, 90) : rect(324, second ? 292 : 132, 170, 108)
      if (this.classList.contains('network-graph-node')) return compact ? rect(152, second ? 712 : 412, 248, 132) : rect(518, second ? 280 : 120, 202, 132)
      return rect(0, 0, 0, 0)
    })
    const { container, unmount } = render(<NetworkGraph {...props({ selectedPeerId: 'A', state: state({ peers: [peer('B'), peer('A')], transfers: [transfer({ peerId: 'A' })] }) })} />)
    const lines = () => Array.from(container.querySelectorAll('.network-graph-line')).map(line => line.getAttribute('d'))
    expect(lines()).toEqual([
      'M 154 84 H 192 C 208 84, 208 86, 224 86 M 394 86 C 411 86, 411 64, 428 64',
      'M 154 84 H 192 C 208 84, 208 246, 224 246 M 394 246 C 411 246, 411 224, 428 224',
    ])
    expect(container.querySelector('g[data-peer-id="A"]')).toHaveClass('is-selected')
    expect(diagram().getByRole('button', { name: /^Open connection: Local device — A;/ })).toHaveClass('is-selected')
    expect(observe).toHaveBeenCalledTimes(6)
    compact = true
    act(() => notifyResize())
    expect(lines()).toEqual([
      'M 30 76 Q 20 76 20 88 V 235 Q 20 245 30 245 H 52 M 176 290 C 176 306, 96 306, 96 322',
      'M 30 76 Q 20 76 20 88 V 535 Q 20 545 30 545 H 52 M 176 590 C 176 606, 96 606, 96 622',
    ])
    expect(container.querySelector('[data-direction="outgoing"]')).toHaveAttribute('d', 'M 92 311 l 4 5 4 -5')
    expect(container.querySelector('.network-graph')).toHaveAttribute('data-view', 'diagram')
    expect(screen.getByRole('button', { name: 'Show device list' })).toBeInTheDocument()
    expect(container.querySelector('.network-graph-list')).not.toBeInTheDocument()
    expect(container.querySelectorAll('foreignObject, text, [style]')).toHaveLength(0)
    expect(diagram().getAllByRole('button')).toHaveLength(5)
    unmount()
    expect(disconnect).toHaveBeenCalledOnce()
  })

  it('keeps coordinates and keyboard nodes stable as polling reorders or updates peers', () => {
    const first = props({ state: state({ peers: [peer('B'), peer('A')] }) })
    const { container, rerender } = render(<NetworkGraph {...first} />)
    const a = diagram().getByRole('button', { name: /^Open device: A;/ })
    const before = Array.from(container.querySelectorAll('.network-graph-branch')).map(group => group.getAttribute('data-peer-id'))
    a.focus()
    rerender(<NetworkGraph {...first} state={state({ peers: [peer('A', { online: false, name: 'Renamed' }), peer('B', { trusted: false })] })} />)
    const after = Array.from(container.querySelectorAll('.network-graph-branch')).map(group => group.getAttribute('data-peer-id'))
    expect(before).toEqual(['A', 'B'])
    expect(after).toEqual(before)
    expect(diagram().getByRole('button', { name: /^Open device: Renamed;/ })).toBe(a)
    expect(a).toHaveFocus()
  })

  it('has an honest actionable empty state and aligned Japanese labels', () => {
    const initial = props({ state: state({ peers: [] }), locale: 'ja' })
    const { container } = render(<NetworkGraph {...initial} />)
    expect(screen.getByRole('heading', { name: 'ネットワーク図' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'まだデバイスがありません' })).toBeInTheDocument()
    expect(screen.getByText(/この端末を開いて、ネットワークの設定/)).toBeInTheDocument()
    expect(container.querySelectorAll('.network-graph-line')).toHaveLength(0)
    expect(screen.getByText(/通信量は計測していません/)).toBeInTheDocument()
    expect(within(screen.getByRole('group', { name: 'この端末から確認できる接続' })).getByRole('button', { name: /^デバイスを開く: Local device;/ })).toBeInTheDocument()
  })
})
