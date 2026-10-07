import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, Service, ServiceConfiguration, State } from '../api'
import { translator } from '../i18n'
import { selectionAttemptEnglish, selectionAttemptJapanese } from '../selection-attempt-i18n'
import { useServer } from '../useServer'
import { SavedServicesDialog } from './SavedServicesDialog'

const definitions: ServiceConfiguration[] = ['already-running', 'prepared-member', 'failing-member', 'later-member'].map((name, index) => ({ id: `service-${index}`, name, direction: index === 0 ? 'share' : 'forward', backend: 'tailnet', network: 'tcp', ports: String(8000 + index), peerId: 'peer-one', peerIds: ['peer-one'], lifetime: 'finite', ttlSeconds: 60, loopbackHost: '127.0.0.1', discoverable: false, purpose: 'web' }))
function snapshot(statuses: Service['status'][]): State {
  const services = definitions.map((service, index) => ({ ...service, peerId: 'peer-one', status: statuses[index] }))
  return { csrfToken: 'fixture', self: { name: 'This device', status: 'online' }, peers: [], services: services.slice(1), shares: services.slice(0, 1), messages: [], transfers: [], settings: { network: 'tailnet' } }
}
const before = snapshot(['active', 'saved', 'saved', 'saved'])
const after = snapshot(['active', 'stopped', 'failed', 'saved'])
const aggregateError = 'multi-service start failed; newly started services were stopped: listener conflict'
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const failure = () => json({ code: 'command_failed', error: aggregateError }, 409)
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
function Harness({ locale }: { locale: Locale }) {
  const server = useServer()
  const [open, setOpen] = useState(true)
  return server.state && open ? <SavedServicesDialog server={server} locale={locale} t={translator(locale)} onClose={() => setOpen(false)} /> : null
}
function setup({ locale = 'en', mutate = async () => failure(), refresh = async () => json(after) }: { locale?: Locale; mutate?: () => Promise<Response>; refresh?: () => Promise<Response> } = {}) {
  let attempted = false
  const requests: { name: string; payload: Record<string, unknown> }[] = []
  const readAfterAttempt = vi.fn(refresh)
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return attempted ? readAfterAttempt() : json(before)
    const request = JSON.parse(init!.body as string); requests.push(request)
    if (request.name === 'profile.export') return json({ ok: true, result: { profile: { version: 1, services: definitions, groups: [{ name: 'reviewed-group', serviceIds: definitions.map(service => service.id) }] }, revision: 'a'.repeat(64), disabled: true } })
    if (request.name === 'service.selection') {
      const services = request.payload.ids ? definitions.filter(service => request.payload.ids.includes(service.id)) : definitions
      return json({ ok: true, result: { services, states: [...before.shares, ...before.services].filter(state => services.some(service => service.id === state.id)), revision: 'b'.repeat(64), ready: false, group: request.payload.group || '', application: 'unverified' } })
    }
    if (request.name === 'services.start' || request.name === 'services.stop') { attempted = true; return mutate() }
    throw new Error(`unexpected command: ${request.name}`)
  }))
  render(<Harness locale={locale} />)
  return { requests, readAfterAttempt }
}
async function review(action: 'start' | 'stop' = 'start') {
  await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Saved group' }), 'reviewed-group')
  await userEvent.click(screen.getByRole('button', { name: action === 'start' ? 'Review start' : 'Review stop' }))
  return screen.findByRole('button', { name: action === 'start' ? 'Start reviewed services' : 'Stop reviewed services' })
}
async function result(action: 'start' | 'stop' = 'start') {
  const region = await screen.findByRole('region', { name: action === 'start' ? 'Start not confirmed' : 'Stop not confirmed' })
  await waitFor(() => expect(within(region).getByRole('button', { name: 'Refresh member states' })).toBeEnabled())
  return region
}
function expectStates(region: HTMLElement, name: string, before: string, current: string) {
  const member = within(region).getByRole('article', { name })
  expect(within(member).getAllByRole('definition').map(item => item.textContent)).toEqual([before, current])
}

describe('reviewed selection failure states', () => {
  it('retains every reviewed identity and shows observed states with the aggregate error', async () => {
    const { requests, readAfterAttempt } = setup()
    await userEvent.click(await review())
    const region = await result()
    expectStates(region, 'already-running', 'Active', 'Active')
    expectStates(region, 'prepared-member', 'Saved configuration', 'Stopped')
    expectStates(region, 'failing-member', 'Saved configuration', 'Failed')
    expectStates(region, 'later-member', 'Saved configuration', 'Saved configuration')
    for (const service of definitions) expect(region).toHaveTextContent(service.id)
    expect(region).toHaveTextContent('A stopped state alone does not tell us')
    expect(screen.getByRole('alert')).toHaveTextContent(aggregateError)
    expect(requests.filter(request => request.name === 'service.selection')).toHaveLength(1)
    expect(requests.filter(request => request.name === 'services.start')).toEqual([{ name: 'services.start', payload: { group: 'reviewed-group', expectedRevision: 'b'.repeat(64) }, requestId: expect.any(String) }])
    expect(readAfterAttempt).toHaveBeenCalledOnce()
    expect(screen.queryByRole('button', { name: 'Start reviewed services' })).not.toBeInTheDocument()
  })

  it('keeps missing, duplicate and unrecognized observations unknown instead of assigning an execution outcome', async () => {
    const changed = snapshot(['active', 'stopped', 'failed', 'saved'])
    changed.services = [changed.services[0], { ...changed.services[0] }, { ...changed.services[2], status: 'unexpected' as Service['status'] }]
    changed.shares = [{ ...changed.shares[0], name: 'renamed-after-review' }]
    setup({ refresh: async () => json(changed) })
    await userEvent.click(await review())
    const region = await result()
    expectStates(region, 'already-running', 'Active', 'Active')
    for (const name of ['prepared-member', 'failing-member', 'later-member']) expectStates(region, name, 'Saved configuration', 'Unknown')
    expect(region).not.toHaveTextContent('renamed-after-review')
  })

  it('retains the error and identities when refresh fails, then allows a read-only refresh without retrying the start', async () => {
    const read = vi.fn<() => Promise<Response>>().mockRejectedValueOnce(new TypeError('network unavailable')).mockResolvedValueOnce(json(after))
    const { requests } = setup({ refresh: read })
    await userEvent.click(await review())
    const region = await result()
    expect(region).toHaveTextContent('Current member states are unknown')
    expectStates(region, 'already-running', 'Active', 'Unknown')
    expectStates(region, 'prepared-member', 'Saved configuration', 'Unknown')
    expect(screen.getByRole('alert')).toHaveTextContent(aggregateError)
    await userEvent.click(within(region).getByRole('button', { name: 'Refresh member states' }))
    await waitFor(() => expectStates(region, 'already-running', 'Active', 'Active'))
    expect(screen.getByRole('alert')).toHaveTextContent(aggregateError)
    expect(requests.filter(request => request.name === 'services.start')).toHaveLength(1)
    expect(requests.some(request => request.name === 'services.stop')).toBe(false)
  })

  it.each(['network', 'malformed'] as const)('shows refreshed state when the %s mutation response leaves the result uncertain', async kind => {
    const { requests } = setup({ mutate: async () => { if (kind === 'network') throw new TypeError('connection lost'); return json({ ok: true, result: { states: [], ready: false } }) }, refresh: async () => json(snapshot(['active', 'active', 'active', 'active'])) })
    await userEvent.click(await review())
    const region = await result()
    expectStates(region, 'prepared-member', 'Saved configuration', 'Active')
    expect(screen.queryByText('Reviewed services started; application use is still unverified.')).not.toBeInTheDocument()
    expect(requests.filter(request => request.name === 'services.start')).toHaveLength(1)
  })

  it('also preserves reviewed stop results without claiming that all members stopped', async () => {
    const { requests } = setup()
    await userEvent.click(await review('stop'))
    expectStates(await result('stop'), 'already-running', 'Active', 'Active')
    expect(requests.filter(request => request.name === 'services.stop')).toHaveLength(1)
    expect(requests.some(request => request.name === 'services.start')).toBe(false)
  })

  it('requires a new scope review after an outcome is dismissed or the selection changes', async () => {
    const { requests } = setup()
    await userEvent.click(await review())
    await result()
    await userEvent.click(screen.getByRole('checkbox', { name: /already-running/ }))
    expect(screen.queryByRole('region', { name: 'Start not confirmed' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Review stop' }))
    const region = await screen.findByRole('region', { name: 'Review selected services' })
    expect(region).not.toHaveTextContent('already-running')
    await userEvent.click(within(region).getByRole('button', { name: 'Cancel' }))
    expect(requests.filter(request => request.name.startsWith('services.'))).toHaveLength(1)
  })

  it('ignores an old refresh after dismissal and a newer review', async () => {
    const waiting = deferred<Response>()
    const read = vi.fn<() => Promise<Response>>().mockImplementationOnce(() => waiting.promise).mockImplementation(async () => json(after))
    const { requests } = setup({ refresh: read })
    await userEvent.click(await review())
    const old = await screen.findByRole('region', { name: 'Start not confirmed' })
    expect(within(old).getByRole('button', { name: 'Refresh member states' })).toBeDisabled()
    await userEvent.click(within(old).getByRole('button', { name: 'Dismiss result' }))
    await userEvent.click(screen.getByRole('checkbox', { name: /already-running/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Review stop' }))
    await screen.findByRole('region', { name: 'Review selected services' })
    await act(async () => { waiting.resolve(json(before)); await waiting.promise })
    expect(screen.queryByRole('region', { name: 'Start not confirmed' })).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Review selected services' })).not.toHaveTextContent('already-running')
    expect(requests.filter(request => request.name.startsWith('services.'))).toHaveLength(1)
  })

  it('guards repeated submission and does not refresh a dismissed dialog when a late mutation fails', async () => {
    const waiting = deferred<Response>()
    const { requests, readAfterAttempt } = setup({ mutate: () => waiting.promise })
    const button = await review()
    act(() => { fireEvent.click(button); fireEvent.click(button) })
    await waitFor(() => expect(requests.filter(request => request.name === 'services.start')).toHaveLength(1))
    await userEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    await act(async () => { waiting.resolve(failure()); await waiting.promise })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(readAfterAttempt).not.toHaveBeenCalled()
  })

  it('keeps Japanese results aligned and dismisses without another command', async () => {
    expect(Object.keys(selectionAttemptJapanese)).toEqual(Object.keys(selectionAttemptEnglish))
    const { requests } = setup({ locale: 'ja' })
    await userEvent.selectOptions(await screen.findByRole('combobox', { name: '保存済みグループ' }), 'reviewed-group')
    await userEvent.click(screen.getByRole('button', { name: '開始内容を確認' }))
    await userEvent.click(await screen.findByRole('button', { name: '確認したサービスを開始' }))
    const region = await screen.findByRole('region', { name: '開始結果を確認できません' })
    await waitFor(() => expectStates(region, 'already-running', '有効', '有効'))
    expectStates(region, 'prepared-member', '設定を保存済み', '停止済み')
    expect(region).toHaveTextContent('失敗・開始後の取り消し・未実行のどれかは分かりません')
    await userEvent.click(within(region).getByRole('button', { name: '結果を閉じる' }))
    expect(screen.queryByRole('region', { name: '開始結果を確認できません' })).not.toBeInTheDocument()
    expect(requests.filter(request => request.name.startsWith('services.'))).toHaveLength(1)
  })
})
