import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import type { State } from '../api'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { MixedSetup } from './MixedSetup'
function state(): State { return { csrfToken: 'fixture', self: { name: 'Synthetic device', status: 'ready' }, peers: [], messages: [], transfers: [], services: [], shares: [], settings: { network: 'mixed' }, mixed: { configured: true, backends: ['direct-lan', 'tailnet'], bindings: [], routes: [{ peerId: 'route-one', backend: 'direct-lan', transportId: 'a1'.repeat(32), name: 'Synthetic peer', backendReady: true, expired: false }, { peerId: 'route-two', backend: 'tailnet', transportId: 'synthetic-tailnet-id', name: 'Synthetic peer', backendReady: true, expired: false }], backendStates: [{ backend: 'direct-lan', running: true, state: 'ready' }, { backend: 'tailnet', running: true, state: 'ready' }] } } }
function setup(value = state()) {
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true }), server = { run, busy: new Set(), state: value, auth: 'ready', stale: false, setError: vi.fn() } as unknown as Server
  const view = render(<MixedSetup server={server} state={value} locale="en" t={translator('en')} blocked={false} />)
  return { ...view, run, user: userEvent.setup(), update: (next: State) => view.rerender(<MixedSetup server={server} state={next} locale="en" t={translator('en')} blocked={false} />) }
}
it('does not bind matching display names and requires an explicit proof review', async () => {
  const v = setup(); expect(v.run).not.toHaveBeenCalled()
  const checks = screen.getAllByRole('checkbox'); await v.user.click(checks[0]); await v.user.click(checks[1]); await v.user.click(screen.getByRole('button', { name: 'Review identity binding' }))
  expect(v.run).not.toHaveBeenCalled(); expect(screen.getByText(/Existing route-specific approvals pause/)).toBeInTheDocument()
  await v.user.click(screen.getByRole('button', { name: 'Verify and bind selected identities' })); expect(v.run).toHaveBeenCalledWith('mixed.bind', { peers: ['route-one', 'route-two'] }); expect(v.run.mock.calls.some(c => c[0] === 'peer.trust')).toBe(false)
})
it('invalidates a reviewed binding when a selected backend becomes unavailable', async () => {
  const v = setup(); for (const check of screen.getAllByRole('checkbox')) await v.user.click(check)
  await v.user.click(screen.getByRole('button', { name: 'Review identity binding' })); const next = state(); next.mixed!.routes![0].backendReady = false; v.update(next)
  expect(screen.getByRole('button', { name: 'Verify and bind selected identities' })).toBeDisabled(); expect(v.run).not.toHaveBeenCalled()
})
it('requires reviewed explicit backend order and warns about external contact', async () => {
  const value = state(); value.settings = { network: 'none' }; delete value.mixed
  const v = setup(value), choices = screen.getAllByRole('combobox')
  fireEvent.change(choices[0], { target: { value: 'direct-lan' } }); fireEvent.change(choices[1], { target: { value: 'tailnet' } }); await v.user.click(screen.getByRole('button', { name: 'Review mixed activation' }))
  expect(v.run).not.toHaveBeenCalled(); expect(screen.getAllByText(/may contact external services/).length).toBeGreaterThan(0)
  await v.user.click(screen.getByRole('button', { name: 'Activate selected backends' })); expect(v.run).toHaveBeenCalledWith('network.configure', { mode: 'mixed', mixed: { backends: ['direct-lan', 'tailnet'] } })
})
