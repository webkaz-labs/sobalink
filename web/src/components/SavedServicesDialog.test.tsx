import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import type { DefinitionBundle, ServiceConfiguration, State } from '../api'
import { definitionsEnglish, definitionsJapanese } from '../definitions-i18n'
import { readDefinitionBundle } from './SavedServicesDialog'

const share: ServiceConfiguration = { id: 'saved-one', name: 'web-share', direction: 'share', backend: 'tailnet', network: 'tcp', ports: '8080', peerIds: ['peer-one'], lifetime: 'finite', ttlSeconds: 3600, purpose: 'web', discoverable: false, loopbackHost: '127.0.0.1' }
const forward: ServiceConfiguration = { id: 'saved-two', name: 'web-connection', direction: 'forward', backend: 'tailnet', network: 'tcp', ports: '8000-8002', excludePorts: '8001', localPort: 9000, peerId: 'peer-one', lifetime: 'until-stopped', ttlSeconds: 0, purpose: 'web', discoverable: false, loopbackHost: '127.0.0.1' }
const profile: DefinitionBundle = { version: 1, services: [share, forward], groups: [{ name: 'daily-tools', serviceIds: ['saved-one', 'saved-two'] }] }
const state: State = { csrfToken: 'fixture', self: { name: 'This device', status: 'online' }, peers: [{ id: 'peer-one', name: 'Studio', networks: ['tailnet'], online: true, bridge: true, trusted: true, verified: true, path: 'direct' }], services: [{ ...forward, peerId: 'peer-one', status: 'saved' }], shares: [{ ...share, peerId: '', status: 'saved' }], transfers: [], messages: [], settings: { network: 'tailnet' } }
function setup(initial = profile, failure = '', selectionStates: unknown[] = []) {
  let current = structuredClone(initial)
  const requests: { name: string; payload: Record<string, unknown> }[] = []
  localStorage.setItem('sobalink.locale', 'en')
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(state))
    const request = JSON.parse(init!.body as string); requests.push(request)
    if (request.name === failure) return new Response(JSON.stringify({ code: failure === 'profile.import' ? 'profile_revision_conflict' : failure === 'services.start' ? 'service_lifetime_conflict' : 'group_revision_conflict' }), { status: 409 })
    let result: unknown
    if (request.name === 'profile.export') result = { profile: current, revision: 'a'.repeat(64), disabled: true }
    if (request.name === 'service.selection') {
      const ids = request.payload.ids || current.groups?.find(item => item.name === request.payload.group)?.serviceIds || []
      result = { services: current.services.filter(item => ids.includes(item.id)), states: selectionStates, revision: 'b'.repeat(64), group: request.payload.group || '', ready: false, application: 'unverified' }
    }
    if (['services.start', 'services.stop'].includes(request.name)) result = { ready: request.name === 'services.start', states: [], services: current.services }
    if (request.name === 'group.save') {
      current = { ...current, groups: [...(current.groups || []).filter(item => item.name !== request.payload.group.name), request.payload.group] }
      result = { group: request.payload.group, revision: 'c'.repeat(64), active: false }
    }
    if (request.name === 'profile.import.preview') result = { profile: request.payload.profile, revision: 'd'.repeat(64), disabled: true, preservesIdentity: true, replacesServices: current.services.length, removesRustDeskMetadata: current.groups?.filter(group => group.rustdesk && !request.payload.profile.groups?.some((next: { name: string }) => next.name === group.name)).map(group => group.name) || [] }
    if (request.name === 'profile.import') { current = request.payload.profile; result = { applied: true } }
    return new Response(JSON.stringify({ ok: true, result }))
  }))
  return requests
}
async function open() {
  render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: 'Saved services' }))
  await waitFor(() => expect(screen.queryByText('Loading…')).not.toBeInTheDocument())
  await screen.findByRole('button', { name: 'Select all' })
}
function definitionFile(profile: unknown) {
  const text = JSON.stringify(profile)
  const file = new File([text], 'definitions.json', { type: 'application/json' })
  Object.defineProperty(file, 'text', { value: async () => text })
  return file
}
describe('saved service and group management', () => {
  it.each(['tailnet', 'lan', 'direct-lan', 'mixed'] as const)('accepts saved %s definitions without changing the backend', backend => {
    const source: DefinitionBundle = { ...profile, services: profile.services.map(service => ({ ...service, backend })) }
    expect(readDefinitionBundle(source)).toEqual(source)
  })
  it.each(['direct-lan', 'mixed'] as const)('lists and reviews saved %s groups without starting them', async backend => {
    const source: DefinitionBundle = { ...profile, services: profile.services.map(service => ({ ...service, backend })) }
    const requests = setup(source); await open()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Saved group' }), 'daily-tools')
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const review = await screen.findByRole('region', { name: 'Review selected services' })
    expect(review).toHaveTextContent(`${backend} · TCP`)
    expect(review).toHaveTextContent('web-share')
    expect(review).toHaveTextContent('web-connection')
    expect(requests.some(item => ['services.start', 'network.configure'].includes(item.name))).toBe(false)
  })
  it.each(['direct-lan', 'mixed'] as const)('reviews a %s definitions import without changing permissions', async backend => {
    const requests = setup(); await open()
    fireEvent.click(screen.getByText('Import and export'))
    const incoming: DefinitionBundle = { version: 1, services: [{ ...forward, backend }], groups: [] }
    await userEvent.upload(screen.getByLabelText('Choose a definitions file'), definitionFile(incoming))
    await userEvent.click(await screen.findByRole('button', { name: 'Review import' }))
    const review = await screen.findByRole('region', { name: 'Review replacement' })
    expect(review).toHaveTextContent(`${backend} · TCP`)
    expect(requests.find(item => item.name === 'profile.import.preview')?.payload).toEqual({ profile: incoming })
    expect(requests.some(item => ['profile.import', 'services.start', 'network.configure'].includes(item.name))).toBe(false)
  })
  it('still rejects an unsupported saved backend', () => {
    expect(() => readDefinitionBundle({ ...profile, services: [{ ...forward, backend: 'unrecognized' }] })).toThrow('invalidBundle')
  })
  it('handles an empty null-valued Core profile and aligns localization', async () => {
    expect(Object.keys(definitionsJapanese)).toEqual(Object.keys(definitionsEnglish))
    expect(readDefinitionBundle({ version: 1, services: null, groups: null })).toEqual({ version: 1, services: [], groups: [] })
    setup({ version: 1, services: [], groups: null }); await open()
    expect(screen.getByText(/No saved services yet/)).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Review start' })).not.toBeInTheDocument()
  })
  it('reviews group scope and exact mappings before starting the reviewed revision', async () => {
    const requests = setup(); await open()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Saved group' }), 'daily-tools')
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const review = await screen.findByRole('region', { name: 'Review selected services' })
    expect(review).toHaveTextContent('Studio')
    expect(review).toHaveTextContent('3,600 seconds')
    expect(review).toHaveTextContent('Until stopped')
    expect(review).toHaveTextContent('127.0.0.1:9000 → 8000')
    expect(review).toHaveTextContent('127.0.0.1:9001 → 8002')
    expect(requests.some(item => item.name === 'services.start')).toBe(false)
    await userEvent.click(within(review).getByRole('button', { name: 'Start reviewed services' }))
    await screen.findByText('Reviewed services started; application use is still unverified.')
    expect(requests.find(item => item.name === 'services.start')?.payload).toEqual({ group: 'daily-tools', expectedRevision: 'b'.repeat(64) })
  })
  it('invalidates a reviewed scope on selection changes and never stops on cancellation', async () => {
    const requests = setup(); await open()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' }))
    await userEvent.click(screen.getByRole('button', { name: 'Review stop' }))
    await screen.findByRole('button', { name: 'Stop reviewed services' })
    await userEvent.click(screen.getByRole('checkbox', { name: /web-share/ }))
    expect(screen.queryByRole('button', { name: 'Stop reviewed services' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Review stop' }))
    const review = await screen.findByRole('region', { name: 'Review selected services' })
    expect(review).not.toHaveTextContent('web-share')
    await userEvent.click(within(review).getByRole('button', { name: 'Cancel' }))
    expect(requests.some(item => item.name === 'services.stop')).toBe(false)
  })
  it('requires explicit existing-group replacement and only saves stopped references', async () => {
    const requests = setup(); await open()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Saved group' }), 'daily-tools')
    fireEvent.click(screen.getByText('Save this selection as a group'))
    expect(screen.getByRole('button', { name: 'Save group' })).toBeDisabled()
    await userEvent.click(screen.getByRole('checkbox', { name: 'Replace the existing group with this selection' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save group' }))
    await screen.findByText('Group saved without starting services')
    expect(requests.find(item => item.name === 'group.save')?.payload).toEqual({ group: { name: 'daily-tools', serviceIds: ['saved-one', 'saved-two'] }, expectedRevision: 'a'.repeat(64) })
    expect(requests.some(item => item.name === 'services.start')).toBe(false)
  })
  it('reviews file-based replacement counts and requires a fresh import review after conflict', async () => {
    const requests = setup(profile, 'profile.import'); await open()
    fireEvent.click(screen.getByText('Import and export'))
    const incoming = { version: 1, services: [forward], groups: [] }
    await userEvent.upload(screen.getByLabelText('Choose a definitions file'), definitionFile(incoming))
    await userEvent.click(await screen.findByRole('button', { name: 'Review import' }))
    const review = await screen.findByRole('region', { name: 'Review replacement' })
    expect(review).toHaveTextContent('Saved services to replace2')
    expect(review).toHaveTextContent('Incoming services1')
    expect(requests.some(item => item.name === 'profile.import')).toBe(false)
    await userEvent.click(within(review).getByRole('button', { name: 'Replace with reviewed definitions' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Saved definitions or the import changed')
    expect(requests.find(item => item.name === 'profile.import')?.payload).toEqual({ profile: incoming, expectedRevision: 'd'.repeat(64) })
    expect(screen.queryByRole('button', { name: 'Replace with reviewed definitions' })).not.toBeInTheDocument()
  })
})

it('keeps invalid and oversized import files local without previewing or applying them', async () => {
  const requests = setup(); await open()
  fireEvent.click(screen.getByText('Import and export'))
  const input = screen.getByLabelText('Choose a definitions file')
  await userEvent.upload(input, definitionFile({ version: 99, services: [], groups: [] }))
  expect(await screen.findByRole('alert')).toHaveTextContent('not a supported definitions file')
  const large = definitionFile(profile)
  Object.defineProperty(large, 'size', { value: 4 * 1024 * 1024 + 1 })
  await userEvent.upload(input, large)
  expect(await screen.findByRole('alert')).toHaveTextContent('exceeds the current saved-configuration storage budget')
  expect(requests.some(item => item.name.startsWith('profile.import'))).toBe(false)
})
it('downloads only the explicit definitions bundle, excluding the control envelope', async () => {
  const requests = setup()
  let downloaded: Blob | undefined
  const createObjectURL = vi.fn((value: Blob) => { downloaded = value; return 'blob:synthetic-local-download' })
  const revokeObjectURL = vi.fn()
  vi.stubGlobal('URL', { createObjectURL, revokeObjectURL })
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  await open(); fireEvent.click(screen.getByText('Import and export'))
  await userEvent.click(screen.getByRole('button', { name: 'Download saved definitions' }))
  await waitFor(() => expect(createObjectURL).toHaveBeenCalledOnce())
  const contents = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(reader.result as string); reader.onerror = reject; reader.readAsText(downloaded!) })
  expect(JSON.parse(contents)).toEqual(profile)
  expect(contents).not.toContain('csrfToken')
  expect(contents).not.toContain('revision')
  expect(requests.filter(item => item.name === 'profile.export')).toHaveLength(2)
  expect(click).toHaveBeenCalledOnce()
})


describe('per-invocation group lifetime', () => {
  it('reviews a custom finite lifetime for mixed directions without saving definitions', async () => {
    const requests = setup(); await open()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' }))
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const region = await screen.findByRole('region', { name: 'Review selected services' })
    const choices = within(region).getByRole('combobox', { name: 'Lifetime for this start' })
    expect(choices).toHaveValue('saved')
    expect(within(choices).queryByRole('option', { name: 'Until stopped' })).not.toBeInTheDocument()
    expect(within(choices).queryByRole('option', { name: 'Until revoked' })).not.toBeInTheDocument()
    await userEvent.selectOptions(choices, 'custom')
    fireEvent.change(within(region).getByLabelText('Duration in seconds'), { target: { value: '120' } })
    expect(within(region).getAllByText('120 seconds')).toHaveLength(2)
    await userEvent.click(within(region).getByRole('button', { name: 'Start reviewed services' }))
    await screen.findByText('Reviewed services started; application use is still unverified.')
    expect(requests.find(request => request.name === 'services.start')?.payload).toEqual({ ids: ['saved-one', 'saved-two'], expectedRevision: 'b'.repeat(64), lifetime: 'finite', ttlSeconds: 120 })
    expect(requests.some(request => ['service.save', 'group.save'].includes(request.name))).toBe(false)
  })
  it.each([['forward', 'until-stopped'], ['share', 'until-revoked']] as const)('offers the explicit no-expiry mode only for %s selections', async (direction, lifetime) => {
    const selected = direction === 'forward' ? forward : share
    const requests = setup({ version: 1, services: [selected], groups: [] }); await open()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' })); await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const region = await screen.findByRole('region', { name: 'Review selected services' })
    await userEvent.selectOptions(within(region).getByLabelText('Lifetime for this start'), lifetime)
    await userEvent.click(within(region).getByRole('button', { name: 'Start reviewed services' }))
    await waitFor(() => expect(requests.find(request => request.name === 'services.start')?.payload).toMatchObject({ lifetime, ttlSeconds: 0 }))
  })
  it('rejects invalid custom duration and cancels without a start', async () => {
    const requests = setup(); await open(); await userEvent.click(screen.getByRole('button', { name: 'Select all' })); await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const region = await screen.findByRole('region', { name: 'Review selected services' })
    await userEvent.selectOptions(within(region).getByLabelText('Lifetime for this start'), 'custom')
    fireEvent.change(within(region).getByLabelText('Duration in seconds'), { target: { value: '0' } })
    expect(within(region).getByRole('button', { name: 'Start reviewed services' })).toBeDisabled()
    await userEvent.click(within(region).getByRole('button', { name: 'Cancel' }))
    expect(requests.some(request => request.name === 'services.start')).toBe(false)
  })
  it('never extends an observed active grant and explains a server lifetime conflict', async () => {
    const requests = setup({ version: 1, services: [share], groups: [] }, '', [{ id: share.id, status: 'active', lifetime: 'finite', ttlSeconds: 60 }]); await open()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' })); await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const region = await screen.findByRole('region', { name: 'Review selected services' })
    expect(region).toHaveTextContent('Stop it explicitly')
    expect(within(region).getByRole('button', { name: 'Start reviewed services' })).toBeDisabled()
    expect(requests.some(request => request.name === 'services.start')).toBe(false)
  })
  it('keeps server lifetime conflicts recoverable through a new reviewed stop/start', async () => {
    setup(profile, 'services.start'); await open()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' })); await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Start reviewed services' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('already active with a different lifetime')
    expect(screen.getByRole('button', { name: 'Review stop' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: 'Start reviewed services' })).not.toBeInTheDocument()
  })
  it('shows helper metadata lost by an import before cancellation or commit', async () => {
    const original = { ...profile, groups: [{ name: 'desktop-group', serviceIds: [forward.id], rustdesk: { publicKey: 'fictional-public-key', idServiceId: 'id', natServiceId: 'nat', heartbeatServiceId: 'heartbeat', relayServiceId: 'relay' } }] }
    const requests = setup(original); await open(); fireEvent.click(screen.getByText('Import and export'))
    await userEvent.upload(screen.getByLabelText('Choose a definitions file'), definitionFile({ version: 1, services: [forward], groups: [] }))
    await userEvent.click(screen.getByRole('button', { name: 'Review import' }))
    const review = await screen.findByRole('region', { name: 'Review replacement' })
    expect(review).toHaveTextContent('removes or changes the RustDesk public key and role metadata')
    expect(review).toHaveTextContent('desktop-group')
    await userEvent.click(within(review).getByRole('button', { name: 'Cancel' }))
    expect(requests.some(request => request.name === 'profile.import')).toBe(false)
  })
})

it('reviews and cancels one-start lifetime in Japanese', async () => {
  const requests = setup(); localStorage.setItem('sobalink.locale', 'ja'); render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: '保存済みサービス' }))
  await userEvent.click(await screen.findByRole('button', { name: 'すべて選択' }))
  await userEvent.click(screen.getByRole('button', { name: '開始内容を確認' }))
  const region = await screen.findByRole('region', { name: '選択したサービスを確認' })
  await userEvent.selectOptions(within(region).getByLabelText('今回の開始に使う有効期間'), '3600')
  expect(region).toHaveTextContent('保存済み定義は変更しません')
  expect(within(region).getAllByText('3,600 秒')).toHaveLength(2)
  await userEvent.click(within(region).getByRole('button', { name: 'キャンセル' }))
  expect(requests.some(request => request.name === 'services.start')).toBe(false)
})

it('reviews the actual finite runtime when stopping a saved indefinite definition', async () => {
  const requests = setup({ version: 1, services: [forward], groups: [] }, '', [{ id: forward.id, status: 'active', lifetime: 'finite', ttlSeconds: 120, expiresAt: '2026-10-03T06:00:00Z' }]); await open()
  await userEvent.click(screen.getByRole('button', { name: 'Select all' })); await userEvent.click(screen.getByRole('button', { name: 'Review stop' }))
  const region = await screen.findByRole('region', { name: 'Review selected services' })
  expect(region).toHaveTextContent('120 seconds'); expect(region).toHaveTextContent('2026-10-03T06:00:00Z')
  expect(region).not.toHaveTextContent('Until stopped')
  await userEvent.click(within(region).getByRole('button', { name: 'Cancel' }))
  expect(requests.some(request => request.name === 'services.stop')).toBe(false)
})


it('keeps service selection visible while management opens without changing the selection', async () => {
  const requests = setup(); await open()
  const selection = screen.getByRole('checkbox', { name: /web-share/ })
  const management = screen.getAllByText('Manage service')[0]
  expect(selection).toBeVisible()
  expect(management.closest('details')).not.toHaveAttribute('open')
  await userEvent.click(selection)
  const count = requests.length
  await userEvent.click(management)
  expect(management.closest('details')).toHaveAttribute('open')
  expect(selection).toBeChecked()
  await userEvent.click(management)
  expect(selection).toBeChecked()
  expect(requests).toHaveLength(count)
})
