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
it('shows effective worker budgets and a pending restart separately', () => {
  const value = state(); value.mixed!.workerResources = { frameBytes: 1048576, requests: 128, handles: 1024 }; value.mixed!.resourceRestartRequired = true
  setup(value)
  expect(screen.getByText(/Restart mixed mode/)).toBeInTheDocument()
  expect(screen.getByText('Effective worker budgets')).toBeInTheDocument()
  expect(screen.getByText('1048576')).toBeInTheDocument()
})

it.each(['en', 'ja'] as const)('shows unavailable saved state and blocks changes in %s', async locale => {
  const value = state(); value.settings = { network: 'none' }; value.mixed = { configured: false, error: 'saved mixed configuration unavailable' }
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true, result: { ...value.mixed } }), server = { run, busy: new Set(), state: value, auth: 'ready', stale: false, setError: vi.fn() } as unknown as Server
  render(<MixedSetup server={server} state={value} locale={locale} t={translator(locale)} blocked={false} />)
  expect(screen.getByText(locale === 'ja' ? /保存済みの複合接続設定を確認できません/ : /Saved mixed configuration is unavailable/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: locale === 'ja' ? '複合接続の開始を確認' : 'Review mixed activation' })).toBeDisabled()
  const refresh = screen.getByRole('button', { name: locale === 'ja' ? '接続方式の識別情報を更新' : 'Refresh backend identities' })
  expect(refresh).not.toBeDisabled(); await userEvent.click(refresh)
  expect(run).toHaveBeenCalledWith('mixed.status', {})
  expect(run.mock.calls.some(c => c[0] === 'network.configure')).toBe(false)
})
