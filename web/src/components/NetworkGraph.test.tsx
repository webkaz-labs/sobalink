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
      peer('Known offline', { online: false, bridge: false }), peer('Needs permission', { trusted: false }), peer('Ready'),
      peer('Identity unknown', { verified: false }), peer('Service unknown', { bridge: false }),
    ] }) })} />)
    const graph = diagram()
    expect(graph.getByRole('button', { name: /^Open device: Known offline;/ })).toHaveTextContent('Known deviceKnown offlineOfflinesobalink service not confirmed')
    expect(graph.getByRole('button', { name: /^Open device: Needs permission;/ })).toHaveAccessibleName(/Permission needed/)
    expect(graph.getByRole('button', { name: /^Open device: Ready;/ })).toHaveClass('is-ready')
    expect(graph.getByRole('button', { name: /^Open device: Identity unknown;/ })).toHaveAccessibleName(/Identity unverified/)
    expect(graph.getByRole('button', { name: /^Open device: Service unknown;/ })).toHaveTextContent('sobalink service not confirmed')
    expect(graph.queryByText('No file transfer in progress')).not.toBeInTheDocument()
    expect(graph.getAllByRole('button', { name: /^Open connection:/ }).every(button => button.getAttribute('aria-label')?.includes('No file transfer in progress'))).toBe(true)
    expect(graph.queryByText(/Ordinary|not installed/i)).not.toBeInTheDocument()
  })

  it.each([
    { locale: 'en' as const, group: 'Known connections from this device', open: /^Open device: Studio;/, online: 'Online', unconfirmed: 'sobalink service not confirmed', ready: 'Allowed here', explanation: /Network online is reported by the network or an authenticated sobalink reply/ },
    { locale: 'ja' as const, group: 'この端末から確認できる接続', open: /^デバイスを開く: Studio;/, online: 'オンライン', unconfirmed: 'sobalink サービス未確認', ready: 'この端末で許可', explanation: /ネットワーク側の状態通知または認証済みの sobalink 応答/ },
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
    expect(edge).toHaveAccessibleName(/Ready connection services: 1/)
    expect(edge).toHaveAccessibleName(/Active sharing rules: 1/)
    expect(edge).not.toHaveTextContent('Ready connection services')
    expect(diagram().getByText('Listeners 1 · Sharing rules 1')).toBeInTheDocument()
    expect(diagram().queryByText('Listeners 0 · Sharing rules 0')).not.toBeInTheDocument()
    expect(edge).toHaveAccessibleName(/No file transfer in progress/)
    expect(edge).toHaveAccessibleName(/App paused/)
    expect(diagram().getByRole('button', { name: /^Open connection: Local device — Other;/ })).toHaveAccessibleName(/No active connections or sharing rules/)
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
    expect(edge).toHaveAccessibleName(/File transfer: Sending 30% · Receiving 50%/)
    expect(diagram().getByText('File transfer: Sending 30% · Receiving 50%')).toBeInTheDocument()
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
    expect(diagram().getByRole('button', { name: /^Open connection:/ })).toHaveAccessibleName(/Sending progress unknown/)
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
      if (this.classList.contains('network-graph-edge')) return compact ? rect(232, second ? 812 : 512, 130, 44) : rect(324, second ? 268 : 108, 130, 44)
      if (this.classList.contains('network-graph-wire-slot')) return compact ? rect(0, 0, 0, 0) : rect(314, second ? 312 : 152, 170, 24)
      if (this.classList.contains('network-graph-node')) return compact ? rect(152, second ? 712 : 412, 248, 132) : rect(518, second ? 280 : 120, 202, 132)
      return rect(0, 0, 0, 0)
    })
    const { container, unmount } = render(<NetworkGraph {...props({ selectedPeerId: 'A', state: state({ peers: [peer('B'), peer('A')], transfers: [transfer({ peerId: 'A' })] }) })} />)
    const lines = () => ['A', 'B'].map(peerId => container.querySelector(`g[data-peer-id="${peerId}"] .network-graph-line`)?.getAttribute('d'))
    expect(lines()).toHaveLength(2)
    expect(lines()[0]).toMatch(/^M 154 84 H 190 Q 200 84 200 74 V 74 Q 200 64 210 64 H 368 C 398 64, 398 64, 428 64$/)
    expect(lines()[1]).toMatch(/^M 154 84 H 186 Q 200 84 200 98 V 210 Q 200 224 214 224 H 368 C 398 224, 398 224, 428 224$/)
    // No second move command: route badges cannot break the device-to-device line.
    expect(lines().every(line => line?.match(/M /g)?.length === 1)).toBe(true)
    expect(container.querySelectorAll('.network-graph-terminal')).toHaveLength(4)
    expect(container.querySelectorAll('.network-graph-junction')).toHaveLength(2)
    expect(container.querySelector('g[data-peer-id="A"]')).toHaveClass('is-selected')
    expect(diagram().getByRole('button', { name: /^Open connection: Local device — A;/ })).toHaveClass('is-selected')
    expect(observe).toHaveBeenCalledTimes(8)
    compact = true
    act(() => notifyResize())
    expect(lines()).toEqual([
      'M 30 76 H 27 Q 24 76 24 79 V 353 Q 24 356 27 356 H 46 C 54 356, 54 356, 62 356',
      'M 30 76 H 27 Q 24 76 24 79 V 653 Q 24 656 27 656 H 46 C 54 656, 54 656, 62 656',
    ])
    expect(container.querySelector('[data-direction="outgoing"]')).toHaveAttribute('d', 'M 48 351 l 6 5 -6 5')
    expect(container.querySelector('.network-graph')).toHaveAttribute('data-view', 'diagram')
    expect(screen.getByRole('button', { name: 'Show device list' })).toBeInTheDocument()
    expect(container.querySelector('.network-graph-list')).not.toBeInTheDocument()
    expect(container.querySelectorAll('foreignObject, text')).toHaveLength(0)
    expect(Array.from(container.querySelectorAll('[style]')).every(element => !element.getAttribute('style')?.includes('transform'))).toBe(true)
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

  it('keeps measured routes stable through repeated selection, switching, and closing details', () => {
    // A regression to inline selected facts would move all following rows.
    // ResizeObserver deliberately never fires in this selection regression.
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
    const rect = (x: number, y: number, width: number, height: number) => ({ x, y, left: x, top: y, right: x + width, bottom: y + height, width, height, toJSON: () => ({}) })
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const branch = this.closest('.network-graph-branch')
      const branches = [...(branch?.parentElement?.children || [])]
      const index = branches.indexOf(branch!)
      const hasDetailsBefore = branches.slice(0, index).some(item => item.querySelector('.network-graph-selected-facts'))
      const row = 24 + index * 160 + (hasDetailsBefore ? 64 : 0)
      if (this.classList.contains('network-graph-canvas')) return rect(0, 0, 860, 700)
      if (this.classList.contains('network-graph-self')) return rect(24, 56, 152, 240)
      if (this.classList.contains('network-graph-device-glyph')) return this.closest('.network-graph-self') ? rect(44, 80, 112, 112) : rect(548, row + 16, 80, 80)
      if (this.classList.contains('network-graph-node')) return rect(540, row, 296, 150)
      if (this.classList.contains('network-graph-edge')) return rect(270, row, 144, 44)
      if (this.classList.contains('network-graph-wire-slot')) return rect(228, row + 44, 220, 24)
      return rect(0, 0, 0, 0)
    })
    const handlers = props({ state: state({ peers: [peer('A'), peer('B'), peer('C')] }), selectedPeerId: 'A' })
    const { container, rerender } = render(<NetworkGraph {...handlers} />)
    const line = (id: string) => container.querySelector(`g[data-peer-id="${id}"] .network-graph-line`)!.getAttribute('d')!
    const before = ['A', 'B', 'C'].map(line)
    const nodes = [...container.querySelectorAll('.network-graph-branch > .network-graph-node')]
    expect(line('B')).toMatch(/, 550 240$/)
    for (const selection of ['B', 'B', 'C', null, 'A', null, 'A']) {
      rerender(<NetworkGraph {...handlers} selectedPeerId={selection} />)
      expect(['A', 'B', 'C'].map(line)).toEqual(before)
      expect([...container.querySelectorAll('.network-graph-branch > .network-graph-node')]).toEqual(nodes)
      expect(container.querySelectorAll('.network-graph-branches .network-graph-selected-facts')).toHaveLength(0)
      expect(container.querySelectorAll('.network-graph-node[aria-pressed="true"]')).toHaveLength(selection ? 1 : 0)
      expect(container.querySelectorAll('.network-graph-edge[aria-pressed="true"]')).toHaveLength(selection ? 1 : 0)
      const summary = container.querySelector('.network-graph-selection')!
      if (selection) expect(summary.querySelector('strong')).toHaveTextContent(selection)
      else expect(summary).toHaveTextContent('Select a device for service and transfer details.')
    }
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

it('uses route markers only for reported paths and documents dashed uncertainty without idle arrows', () => {
  const { container } = render(<NetworkGraph {...props({ state: state({ peers: [peer('Direct', { path: 'direct' }), peer('Relay', { path: 'relay' }), peer('Unknown'), peer('Offline', { online: false, path: 'direct' })] }) })} />)
  expect(container.querySelectorAll('.network-graph-line.is-reported')).toHaveLength(2)
  expect(container.querySelectorAll('.network-graph-line.is-unconfirmed')).toHaveLength(2)
  expect(container.querySelector('.network-graph-edge[data-path="unknown"] .network-graph-route-glyph')).not.toBeInTheDocument()
  expect(container.querySelectorAll('.network-graph-edge[data-path="relay"] .network-graph-route-glyph rect')).toHaveLength(1)
  expect(screen.getByText('Solid: reported path to an online device. Dashed: other known relationships.')).toBeInTheDocument()
  expect(container.querySelectorAll('[data-direction], animate, animateMotion')).toHaveLength(0)
})
it('keeps waiting offers visible while omitting empty transfer and listener summaries', () => {
  render(<NetworkGraph {...props({ state: state({ transfers: [transfer({ status: 'offered' })] }) })} />)
  const edge = diagram().getByRole('button', { name: /^Open connection:/ })
  expect(edge).toHaveAccessibleName(/Transfers waiting: 1/)
  expect(diagram().getByText('Transfers waiting: 1')).toBeInTheDocument()
  expect(edge).not.toHaveTextContent('No active connections or sharing rules')
  expect(edge).toHaveAccessibleName(/No active connections or sharing rules/)
})


it('coordinates an entire branch on node, route, and path hover or keyboard focus', async () => {
  const handlers = props({ state: state({ peers: [peer('A'), peer('B')] }) })
  const { container } = render(<NetworkGraph {...handlers} />)
  const node = diagram().getByRole('button', { name: /^Open device: A;/ })
  const edge = diagram().getByRole('button', { name: /^Open connection: Local device — A;/ })
  const branch = container.querySelector('.network-graph-branch[data-peer-id="A"]')!
  const path = container.querySelector('g[data-peer-id="A"]')!
  await userEvent.hover(node)
  expect(branch).toHaveClass('is-emphasized')
  expect(path).toHaveClass('is-emphasized')
  expect(path.parentElement?.lastElementChild).toBe(path)
  expect(container.querySelector('g[data-peer-id="B"]')).not.toHaveClass('is-emphasized')
  await userEvent.unhover(node)
  expect(path).not.toHaveClass('is-emphasized')
  act(() => edge.focus())
  expect(path).toHaveClass('is-emphasized')
  act(() => node.focus())
  expect(path).toHaveClass('is-emphasized')
  act(() => screen.getByRole('button', { name: 'Show device list' }).focus())
  expect(path).not.toHaveClass('is-emphasized')
  await userEvent.hover(path.querySelector('.network-graph-line-hit')!)
  expect(branch).toHaveClass('is-emphasized')
  await userEvent.click(path.querySelector('.network-graph-line-hit')!)
  expect(handlers.onSelectPeer).toHaveBeenCalledExactlyOnceWith('A')
})


it.each([1180, 960, 375].flatMap(viewport => [3, 6, 12].map(count => ({ viewport, count }))))('reserves independent measured lanes for $count long Japanese names at $viewport', ({ viewport, count }) => {
  const narrow = viewport === 375
  const compact = viewport < 1180
  const width = narrow ? 351 : compact ? 660 : 860
  const rowHeight = narrow ? 310 : 180
  const rowStart = compact ? 180 : 28
  const rect = (x: number, y: number, width: number, height: number) => ({ x, y, left: x, top: y, right: x + width, bottom: y + height, width, height, toJSON: () => ({}) })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const peerId = this.closest('.network-graph-branch')?.getAttribute('data-peer-id')
    const index = peerId ? Number(peerId.slice(1)) : 0
    const row = rowStart + index * rowHeight
    if (this.classList.contains('network-graph-canvas')) return rect(0, 0, width, rowStart + count * rowHeight)
    if (this.classList.contains('network-graph-self')) return compact ? rect(10, 16, width - 20, 120) : rect(24, 60, 152, 240)
    if (this.classList.contains('network-graph-device-glyph')) {
      if (this.closest('.network-graph-self')) return compact ? rect(18, 24, 96, 96) : rect(44, 80, 112, 112)
      return narrow ? rect(46, row + 8, 72, 72) : rect(compact ? 308 : 548, row + 16, 80, 80)
    }
    if (this.classList.contains('network-graph-node')) return narrow ? rect(38, row, 303, 142) : rect(compact ? 300 : 540, row, compact ? 324 : 296, 150)
    if (this.classList.contains('network-graph-edge')) return narrow ? rect(126, row + 150, 144, 44) : rect(compact ? 90 : 270, row, 144, 44)
    if (this.classList.contains('network-graph-wire-slot')) return narrow ? rect(0, 0, 0, 0) : rect(compact ? 60 : 228, row + 44, 220, 24)
    return rect(0, 0, 0, 0)
  })
  const peers = Array.from({ length: count }, (_, index) => peer(`p${index}`, { name: `制作スタジオの共有デバイス・長い表示名その${index + 1}`, path: index % 3 === 0 ? 'direct' : index % 3 === 1 ? 'relay' : 'unknown' }))
  const { container } = render(<NetworkGraph {...props({ locale: 'ja', state: state({ peers }) })} />)
  expect(container.querySelectorAll('.network-graph-line')).toHaveLength(count)
  expect(container.querySelectorAll('.network-graph-edge')).toHaveLength(count)
  const laneYs: number[] = []
  peers.forEach((peer, index) => {
    const group = container.querySelector(`g[data-peer-id="${peer.id}"]`)!
    const line = group.querySelector('.network-graph-line')!.getAttribute('d')!
    const y = rowStart + index * rowHeight + (narrow ? 44 : 56)
    const x = narrow ? 48 : compact ? 310 : 550
    expect(line.match(/M /g)).toHaveLength(1)
    expect(line).not.toMatch(/NaN|Infinity|undefined/)
    expect(line).toMatch(new RegExp(`, ${x} ${y}$`))
    expect(Number(group.querySelector('.network-graph-junction')!.getAttribute('cy'))).toBe(y)
    const finalCurve = line.split(' C ')[1].split(/[ ,]+/).map(Number)
    const curveYs = finalCurve.filter((_, index) => index % 2 === 1)
    expect(Math.max(...curveYs) - Math.min(...curveYs)).toBeLessThanOrEqual(24)
    const annotation = group.querySelector('.network-graph-annotation')!.getAttribute('d')
    if (narrow) expect(annotation).toBe('')
    else expect(annotation).toBe(`M ${compact ? 162 : 342} ${y - 9} V ${y - 4}`)
    laneYs.push(y)
    expect(screen.getByRole('button', { name: new RegExp(`^デバイスを開く: ${peer.name};`) })).toHaveTextContent(peer.name)
  })
  expect(laneYs.every((y, index) => index === 0 || y - laneYs[index - 1] >= rowHeight)).toBe(true)
  expect(container.querySelectorAll('foreignObject, text, [style]')).toHaveLength(0)
})

it.each(['en', 'ja'] as const)('shows factual lane counts and richer selected service details in %s', locale => {
  const data = state({ peers: [peer('Studio'), peer('Other')],
    services: [{ id: 'a', peerId: 'Studio', name: 'Web preview', network: 'tcp', status: 'active' }, { id: 'b', peerId: 'Studio', name: 'Saved rule', network: 'tcp', status: 'saved' }],
    shares: [{ id: 'c', peerId: 'Studio', name: 'Local service', network: 'udp', status: 'active' }, { id: 'd', peerId: 'Other', name: 'Other scope', network: 'tcp', status: 'active' }],
  })
  const { container, rerender } = render(<NetworkGraph {...props({ locale, state: data })} />)
  const lane = container.querySelector('.network-graph-branch[data-peer-id="Studio"]')!
  expect(lane.querySelector('.network-graph-lane-meta')).toHaveTextContent(locale === 'ja' ? 'Tailnet接続待受 1 · 共有許可 1' : 'TailnetListeners 1 · Sharing rules 1')
  expect(lane.querySelector('.network-graph-selected-facts')).not.toBeInTheDocument()
  rerender(<NetworkGraph {...props({ locale, state: data, selectedPeerId: 'Studio' })} />)
  const details = container.querySelector('.network-graph-selection .network-graph-selected-facts')!
  expect(details).toHaveTextContent('Web preview (TCP) · Local service (UDP)')
  expect(details).not.toHaveTextContent(/Saved rule|Other scope/)
  expect(container.querySelectorAll('[data-direction], animate, animateMotion')).toHaveLength(0)
})

it.each(['en', 'ja'] as const)('separates Tailcat response uncertainty, bridge observation, and stored permission in %s', locale => {
  const data = state({ self: { name: 'Local device', status: 'running', networks: ['lan'] }, settings: { network: 'lan' }, peers: [
    peer('Unknown', { networks: ['lan'], online: false, bridge: false, trusted: true }),
    peer('Paused', { networks: ['lan'], online: false, bridge: false, autosave: { enabled: false, paused: true } }),
    peer('Confirmed', { networks: ['lan'], online: true, bridge: true, trusted: false, discovery: { state: 'confirmed', services: 0 } }),
  ] })
  const { container } = render(<NetworkGraph {...props({ locale, state: data, selectedPeerId: 'Paused' })} />)
  const unknown = container.querySelector('.network-graph-branch[data-peer-id="Unknown"] .network-graph-node')!
  const paused = container.querySelector('.network-graph-branch[data-peer-id="Paused"] .network-graph-node')!
  expect(unknown).toHaveTextContent(locale === 'ja' ? '応答未確認' : 'Response not confirmed')
  expect(unknown).toHaveTextContent(locale === 'ja' ? 'sobalink サービス未確認' : 'sobalink service not confirmed')
  expect(unknown).toHaveAccessibleName(locale === 'ja' ? /この端末で許可/ : /Allowed here/)
  expect(unknown).not.toHaveClass('is-ready')
  expect(paused).toHaveAccessibleName(locale === 'ja' ? /アプリ通信を一時停止中/ : /App paused/)
  expect(paused).toHaveAccessibleName(locale === 'ja' ? /sobalink サービス未確認/ : /sobalink service not confirmed/)
  expect(container.querySelector('.network-graph-selection')).toHaveTextContent(locale === 'ja' ? 'アプリ通信を一時停止中' : 'App paused')
  expect(container.querySelector('.network-graph-branch[data-peer-id="Confirmed"] .network-graph-node')).toHaveTextContent(locale === 'ja' ? 'sobalink 確認済み' : 'sobalink confirmed')
  expect(container.querySelector('.network-graph-canvas')).not.toHaveTextContent(locale === 'ja' ? 'オフライン' : 'Offline')
})

it.each(['en', 'ja'] as const)('describes relay configuration outside the graph without claiming an observed route in %s', locale => {
  const { container } = render(<NetworkGraph {...props({ locale, state: state({
    settings: { network: 'lan' }, self: { name: 'Local device', status: 'running', networks: ['lan'] },
    peers: [peer('Peer', { networks: ['lan'], path: 'unknown' })],
    lan: { configured: true, pairingReady: true, relayReady: true, path: 'relay', relay: { kind: 'relay', address: 'relay.example.invalid:443' } },
  }) })} />)
  const configuration = container.querySelector('.network-graph-configuration')!
  expect(configuration).toHaveTextContent(locale === 'ja' ? '設定済みの中継先' : 'Configured relay')
  expect(configuration).toHaveTextContent(locale === 'ja' ? '実際の経路は報告されていません' : 'the route is not reported')
  expect(configuration).toHaveTextContent('relay.example.invalid:443')
  expect(container.querySelector('.network-graph-diagram')).not.toContainElement(configuration as HTMLElement)
  expect(container.querySelectorAll('.network-graph-branches .network-graph-route-glyph')).toHaveLength(0)
  expect(container.querySelectorAll('.network-graph-line.is-unconfirmed')).toHaveLength(1)
  expect(container.querySelector('.network-graph-network-label')).toHaveTextContent('Tailcat')
  expect(container.querySelector('.network-graph-self')).not.toHaveTextContent('Tailnet')
})

it('uses the selected backend without suggesting simultaneous networks or reusing inactive relay settings', () => {
  const { container } = render(<NetworkGraph {...props({ state: state({
    settings: { network: 'tailnet' }, self: { name: 'Local device', status: 'running', networks: ['lan', 'tailnet'] },
    lan: { configured: true, pairingReady: true, path: 'unknown', relay: { kind: 'relay', address: 'relay.example.invalid:443' } },
  }) })} />)
  expect(container.querySelector('.network-graph-network-label')).toHaveTextContent('Tailscale · Tailnet')
  expect(container.querySelector('.network-graph-self')).not.toHaveTextContent(/Tailcat|LAN/)
  expect(container.querySelector('.network-graph-configuration')).not.toBeInTheDocument()
})

it('keeps filtered peers, counts, selection, and reset action consistent in diagram and list views', async () => {
  const data = state({ peers: [peer('A'), peer('B', { bridge: false }), peer('C', { online: false, bridge: false })] })
  const handlers = props({ state: data, visiblePeers: [data.peers[1]], selectedPeerId: 'A', onClearFilters: vi.fn() })
  const { container, rerender } = render(<NetworkGraph {...handlers} />)
  expect(container.querySelectorAll('.network-graph-branch')).toHaveLength(1)
  expect([...container.querySelectorAll('.network-graph-legend strong')].map(node => node.textContent)).toEqual(['1', '1', '0'])
  expect(container.querySelector('.network-graph-selection')).not.toHaveTextContent('Selected device')
  expect(container.querySelectorAll('[aria-pressed="true"]')).toHaveLength(0)
  rerender(<NetworkGraph {...handlers} view="list" />)
  expect(container.querySelectorAll('.network-graph-list-peer')).toHaveLength(1)
  expect(screen.getByRole('button', { name: /^Open device: B;/ })).toBeInTheDocument()
  rerender(<NetworkGraph {...handlers} visiblePeers={[]} />)
  expect(screen.getByRole('heading', { name: 'No matching devices' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'No known devices yet' })).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
  expect(handlers.onClearFilters).toHaveBeenCalledOnce()
})
