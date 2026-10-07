import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from '../api'
import { translator } from '../i18n'
import { portProposalText } from '../port-proposals-i18n'
import { proposal, source } from '../port-proposals-fixture'
import type { Server } from '../useServer'
import { SavedServicesDialog } from './SavedServicesDialog'
import { PortProposalsDialog } from './PortProposalsDialog'
const state: api.State = { csrfToken: 'synthetic', self: { name: 'Example', status: 'online' }, settings: { network: 'tailnet' }, peers: [], messages: [], transfers: [], services: [], shares: [], limits: { effective: { resources: { portProposalResults: { mode: 'limited', value: 3 }, portProposalAttempts: { mode: 'limited', value: 32 }, portProposalBinds: { mode: 'limited', value: 4096 }, portProposalSeconds: { mode: 'limited', value: 5 } } }, usage: { materializedListeners: 0 } } }
function setup(locale: api.Locale = 'en') {
  const command = vi.spyOn(api, 'command').mockImplementation(async name => ({ ok: true, result: (name === 'service.config' ? source : proposal) as any }))
  const server = { state, stale: false, busy: new Set(), setError: vi.fn(), handleError: vi.fn(), run: vi.fn() } as unknown as Server
  return { command, props: { id: source.configuration.id, server, locale, t: translator(locale), onClose: vi.fn(), onChoose: vi.fn() }, p: (key: string) => portProposalText(locale, key) }
}
function pending() { let resolve!: (result: api.CommandResult) => void; return { promise: new Promise<api.CommandResult>(done => { resolve = done }), resolve: (result: api.CommandResult) => resolve(result) } }
afterEach(() => vi.restoreAllMocks())
describe('explicit stopped-service port checks', () => {
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: reads without probing, displays complete observation, and requires candidate choice`, async () => {
      const { props, command, p } = setup(locale); render(<PortProposalsDialog {...props} />)
      const check = await screen.findByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
      expect(command.mock.calls.map(([name]) => name)).toEqual(['service.config'])
      const scope = screen.getByRole('region', { name: p('source') })
      for (const value of [source.revision, 'example-peer', 'TCP', 'IPv4', '8000-8002', '8001', '127.0.0.1:18080 → 8000; 127.0.0.1:18081 → 8002', '7200']) expect(scope).toHaveTextContent(value)
      await userEvent.click(check)
      const results = await screen.findByRole('region', { name: p('results') })
      expect(results).toHaveTextContent(p('warning')); expect(results).toHaveTextContent(proposal.checkedAt)
      expect(screen.getByRole('button', { name: p('use') })).toBeDisabled()
      expect(screen.getAllByRole('radio')).toHaveLength(3)
      await userEvent.click(within(results).getByRole('radio', { name: '127.0.0.1:49152-49153' }))
      await userEvent.click(screen.getByRole('button', { name: p('use') }))
      expect(props.onChoose).toHaveBeenCalledExactlyOnceWith({ source, localPort: 49152, checkedAt: proposal.checkedAt })
      expect(command.mock.calls[1].slice(0, 2)).toEqual(['service.ports', { id: source.configuration.id, expectedRevision: source.revision, fromPort: 49152, count: 0, attempts: 0 }])
      expect(props.server.run).not.toHaveBeenCalled()
    })
    it.each(['listener_no_conflict', 'listener_capacity', 'listener_probe_timeout', 'listener_probe_canceled', 'listener_probe_capacity', 'listener_probe_invalid', 'listener_permission_denied', 'listener_address_unavailable', 'listener_mapping_invalid', 'listener_unavailable', 'listener_proposals_exhausted', 'listener_proposal_unsupported'])(`${locale}: provides actionable %s without partial proposals`, async code => {
      const { props, command, p } = setup(locale); render(<PortProposalsDialog {...props} />)
      const check = screen.getByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
      command.mockRejectedValueOnce(new api.ApiError(code, 'Untrusted response text'))
      await userEvent.click(check)
      expect(await screen.findByText(p(code))).toBeVisible(); expect(screen.queryByText('Untrusted response text')).not.toBeInTheDocument()
      expect(screen.queryByRole('radio')).not.toBeInTheDocument(); expect(props.onChoose).not.toHaveBeenCalled()
    })
  }
  it('retries observations with fresh request identities after failures, without using mutating retry guards', async () => {
    const { props, command, p } = setup(); render(<PortProposalsDialog {...props} />)
    const check = screen.getByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
    command.mockRejectedValueOnce(new api.ApiError('network_error', ''))
    await userEvent.click(check); await userEvent.click(check)
    expect(await screen.findByRole('region', { name: p('results') })).toBeVisible()
    expect(command.mock.calls[1][2]).not.toBe(command.mock.calls[2][2]); expect(props.server.run).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText(p('count')), { target: { value: '2' } })
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    command.mockResolvedValueOnce({ ok: true, result: { ...proposal, requestedCount: 2, proposals: proposal.proposals.slice(0, 2) } })
    await userEvent.click(check); expect(command.mock.calls[3][1]).toMatchObject({ count: 2 })
  })
  it.each(['close', 'source', 'stale', 'active', 'unmount'] as const)('ignores and aborts a late check after %s', async reason => {
    const { props, command, p } = setup(); const view = render(<PortProposalsDialog {...props} />)
    const check = screen.getByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
    const late = pending(); command.mockReturnValueOnce(late.promise)
    fireEvent.click(check); fireEvent.click(check); expect(command.mock.calls.filter(([name]) => name === 'service.ports')).toHaveLength(1)
    const signal = command.mock.calls[1][3]
    if (reason === 'close') await userEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    if (reason === 'source') view.rerender(<PortProposalsDialog {...props} id="another-source" />)
    if (reason === 'stale') { view.rerender(<PortProposalsDialog {...props} server={{ ...props.server, stale: true }} />); view.rerender(<PortProposalsDialog {...props} />) }
    if (reason === 'active') view.rerender(<PortProposalsDialog {...props} server={{ ...props.server, state: { ...state, services: [{ ...source.configuration, peerId: 'example-peer', status: 'active' }] } }} />)
    if (reason === 'unmount') view.unmount()
    expect(signal?.aborted).toBe(true)
    await act(async () => late.resolve({ ok: true, result: proposal as any }))
    expect(screen.queryByRole('radio')).not.toBeInTheDocument(); expect(props.onChoose).not.toHaveBeenCalled(); expect(props.server.setError).not.toHaveBeenCalled()
  })
  it('rejects a changed revision and requires explicit reload even after input changes', async () => {
    const { props, command, p } = setup(); render(<PortProposalsDialog {...props} />)
    const check = screen.getByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
    command.mockRejectedValueOnce(new api.ApiError('service_revision_conflict', ''))
    await userEvent.click(check); expect(check).toBeDisabled()
    fireEvent.change(screen.getByLabelText(p('count')), { target: { value: '1' } }); expect(check).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: p('reload') })); await waitFor(() => expect(check).toBeEnabled())
    expect(command.mock.calls.map(([name]) => name)).toEqual(['service.config', 'service.ports', 'service.config'])
  })
  it('never presents malformed or source-mismatched results as usable candidates', async () => {
    const { props, command, p } = setup(); render(<PortProposalsDialog {...props} />)
    const check = screen.getByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
    command.mockResolvedValueOnce({ ok: true, result: { ...proposal, revision: 'b'.repeat(64) } })
    await userEvent.click(check); expect(await screen.findByRole('alert')).toHaveTextContent('invalid_response')
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
  })
})

it('shows Core bounded exhaustion as an observation, not an invalid response or whole-system availability claim', async () => {
  const { props, command, p } = setup(); render(<PortProposalsDialog {...props} />)
  const check = screen.getByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
  command.mockResolvedValueOnce({ ok: true, result: { ...proposal, code: 'listener_proposals_exhausted', attempts: 0, stopReason: 'port_range', proposals: [] } })
  await userEvent.click(check)
  const results = await screen.findByRole('region', { name: p('results') })
  expect(results).toHaveTextContent(p('listener_proposals_exhausted')); expect(results).toHaveTextContent(proposal.checkedAt)
  expect(screen.queryByRole('radio')).not.toBeInTheDocument(); expect(screen.queryByRole('button', { name: p('use') })).not.toBeInTheDocument()
})
it('renders raised budgets in bounded pages and requires a new visible choice after paging', async () => {
  const { props, command, p } = setup(); render(<PortProposalsDialog {...props} />)
  const check = screen.getByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
  command.mockResolvedValueOnce({ ok: true, result: { ...proposal, attempts: 25, requestedCount: 25, proposals: Array.from({ length: 25 }, (_, i) => ({ localPort: 49152 + i * 2, localEnd: 49153 + i * 2 })) } })
  await userEvent.click(check); expect(await screen.findAllByRole('radio')).toHaveLength(20)
  await userEvent.click(screen.getAllByRole('radio')[0]); expect(screen.getByRole('button', { name: p('use') })).toBeEnabled()
  await userEvent.click(screen.getByRole('button', { name: p('next') })); expect(screen.getAllByRole('radio')).toHaveLength(5)
  expect(screen.getByRole('button', { name: p('use') })).toBeDisabled()
  expect(command.mock.calls.map(([name]) => name)).toEqual(['service.config', 'service.ports'])
})

it('exposes the action only for saved forwards and disables active forwards', async () => {
  const { props, command } = setup()
  const active = { ...source.configuration, id: 'active-forward', name: 'active-forward' }
  const share = { ...source.configuration, id: 'example-share', name: 'example-share', direction: 'share' as const, peerIds: ['example-peer'], peerId: undefined }
  const server = { ...props.server, state: { ...state, services: [{ ...active, peerId: 'example-peer', status: 'active' as const }] }, run: vi.fn().mockResolvedValue({ ok: true, result: { disabled: true, revision: source.revision, profile: { version: 1, services: [source.configuration, active, share], groups: [] } } }) }
  render(<SavedServicesDialog server={server} locale="en" t={translator('en')} onClose={vi.fn()} />)
  await screen.findByText('example-forward', { exact: true })
  for (const name of ['example-forward', 'active-forward', 'example-share']) {
    const entry = screen.getByText(name, { exact: true }).closest('.definition-entry') as HTMLElement
    await userEvent.click(within(entry).getByText('Manage service', { exact: true }))
    const action = within(entry).queryByRole('button', { name: 'Check alternate ports' })
    if (name === 'example-share') expect(action).not.toBeInTheDocument()
    else if (name === 'active-forward') expect(action).toBeDisabled()
    else expect(action).toBeEnabled()
  }
  expect(command).not.toHaveBeenCalled()
})
it.each(['tcp', 'udp'] as const)('keeps exact IPv6 %s source and candidate family', async network => {
  const { props, command, p } = setup()
  const configuration = { ...source.configuration, network, loopbackHost: '::1' as const }
  command.mockImplementation(async name => ({ ok: true, result: (name === 'service.config' ? { ...source, configuration } : { ...proposal, configuration }) as any }))
  render(<PortProposalsDialog {...props} />)
  const check = screen.getByRole('button', { name: p('check') }); await waitFor(() => expect(check).toBeEnabled())
  expect(screen.getByRole('region', { name: p('source') })).toHaveTextContent(`tailnet · ${network.toUpperCase()}`)
  expect(screen.getByRole('region', { name: p('source') })).toHaveTextContent('IPv6 · ::1')
  await userEvent.click(check); await userEvent.click(await screen.findByRole('radio', { name: '[::1]:49152-49153' }))
  expect(screen.getByLabelText(p('selectedMapping'))).toHaveTextContent('[::1]:49152 → 8000; [::1]:49153 → 8002')
})
