import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { WanCandidates } from './WanCandidates'
function setup(editable = true) {
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true, result: { enabled: false, stunEndpoints: [], advertiseIPv6: false, editable, restartRequired: true, probeBudget: 4 } })
  const server = { run, busy: new Set(), setError: vi.fn() } as unknown as Server
  render(<WanCandidates server={server} locale="en" t={translator('en')} blocked={false} />)
  return { run, user: userEvent.setup() }
}
it('does no probe or read before an explicit request and reviews exact candidates before saving', async () => {
  const v = setup(); expect(v.run).not.toHaveBeenCalled(); await v.user.click(screen.getByText('Advanced: WAN direct candidates')); await v.user.click(screen.getByRole('button', { name: 'Review WAN settings' }))
  fireEvent.change(screen.getByLabelText('Exact STUN IP:port endpoints', { exact: false }), { target: { value: '192.0.2.20:3478' } })
  await v.user.click(screen.getByRole('button', { name: 'Review candidate changes' })); expect(v.run).toHaveBeenCalledTimes(1)
  await v.user.click(screen.getByRole('button', { name: 'Save explicit WAN discovery' })); expect(v.run).toHaveBeenLastCalledWith('wan.candidates.set', { enabled: true, stunEndpoints: ['192.0.2.20:3478'], advertiseIPv6: false, probeBudget: 4 })
})
it('blocks edits while the engine is active and rejects hostname candidates', async () => {
  const v = setup(false); await v.user.click(screen.getByText('Advanced: WAN direct candidates')); await v.user.click(screen.getByRole('button', { name: 'Review WAN settings' })); expect(screen.getByRole('button', { name: 'Review candidate changes' })).toBeDisabled()
})
it('rejects hostname candidates before issuing a mutation', async () => {
  const v = setup(); await v.user.click(screen.getByText('Advanced: WAN direct candidates')); await v.user.click(screen.getByRole('button', { name: 'Review WAN settings' }))
  fireEvent.change(screen.getByLabelText('Exact STUN IP:port endpoints', { exact: false }), { target: { value: 'stun.example.test:3478' } }); await v.user.click(screen.getByRole('button', { name: 'Review candidate changes' })); expect(v.run).toHaveBeenCalledTimes(1); expect(screen.getByRole('alert')).toBeInTheDocument()
})
