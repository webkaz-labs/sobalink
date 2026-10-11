import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import type { Peer, State, Transfer } from '../api'
import type { Server } from '../useServer'
import { PeerTransfers } from './PeerTransfers'
import { peerTransferCounts, retryExpected } from '../peer-transfers'
const peer: Peer = { id: 'p', name: 'Example', online: true, verified: true, trusted: true, bridge: true, networks: ['direct-lan'], path: 'direct' }
const transfer = (id: string, status: Transfer['status'] = 'failed'): Transfer => ({ id, peerId: 'p', direction: 'outgoing', name: id, status, retryable: true, entries: [], totalBytes: 2, completedBytes: 1, createdAt: '2026-10-11' })
const state: State = { resourceCatalogProcessId: 'a'.repeat(64), csrfToken: '', self: { name: '', status: 'online', networks: [] }, peers: [peer], transfers: [transfer('a'), transfer('b')], messages: [], services: [], shares: [] }
const server = () => ({ auth: 'ready', stale: false, busy: new Set<string>(), run: vi.fn().mockResolvedValue({ ok: true }) } as unknown as Server)
it('counts terminal states separately', () => expect(peerTransferCounts(['completed', 'failed', 'offered', 'saving', 'cancelled', 'declined'].map((s, i) => transfer(String(i), s as Transfer['status'])))).toEqual({ succeeded: 1, failed: 1, inProgress: 2, cancelled: 1, declined: 1 }))
it.each(['completed', 'cancelled', 'declined', 'offered', 'queued', 'transferring', 'saving'] as const)('never retries %s', status => expect(retryExpected(state, peer, transfer('a', status), false, new Set())).toBeUndefined())
it('requires boot identity and explicit eligibility', () => {
  expect(retryExpected({ ...state, resourceCatalogProcessId: undefined }, peer, transfer('a'), false, new Set())).toBeUndefined()
  expect(retryExpected(state, peer, { ...transfer('a'), retryable: undefined }, false, new Set())).toBeUndefined()
  expect(retryExpected(state, peer, transfer('a'), true, new Set())).toBeUndefined()
})
it.each(['en', 'ja'] as const)('only retries checked failed targets in %s', async locale => {
  const s = server(); render(<PeerTransfers state={state} peer={peer} server={s} locale={locale} resetToken="1" />)
  fireEvent.click(screen.getAllByRole('checkbox')[0]); fireEvent.click(screen.getByRole('button'))
  await waitFor(() => expect(s.run).toHaveBeenCalledTimes(1))
  expect(s.run).toHaveBeenCalledWith('transfer.retry', { transferId: 'a', expected: { processId: 'a'.repeat(64), peerId: 'p', direction: 'outgoing' } }, 'transfer:a')
  await screen.findByText(locale === 'en' ? /Retry requested. Follow/ : /再試行を要求しました/)
  expect(screen.getByRole('button')).toBeDisabled()
})
it.each(['navigation', 'process', 'auth', 'status'])('clears selection on %s', reason => {
  const s = server(); const view = render(<PeerTransfers state={state} peer={peer} server={s} locale="en" resetToken="1" />)
  fireEvent.click(screen.getAllByRole('checkbox')[0])
  view.rerender(<PeerTransfers state={reason === 'process' ? { ...state, resourceCatalogProcessId: 'b'.repeat(64) } : reason === 'status' ? { ...state, transfers: [transfer('a', 'completed'), transfer('b')] } : state} peer={peer} server={reason === 'auth' ? { ...s, auth: 'locked' } : s} locale="en" resetToken={reason === 'navigation' ? '2' : '1'} />)
  expect(screen.getAllByRole('checkbox').every(input => !(input as HTMLInputElement).checked)).toBe(true)
  expect(screen.getByRole('button')).toBeDisabled()
})
it('does not duplicate pending requests or send remaining targets after navigation', async () => {
  let finish!: (result: undefined) => void
  const s = server(); s.run = vi.fn(() => new Promise<undefined>(resolve => { finish = resolve })) as Server['run']
  const view = render(<PeerTransfers state={state} peer={peer} server={s} locale="en" resetToken="1" />)
  screen.getAllByRole('checkbox').forEach(input => fireEvent.click(input)); const button = screen.getByRole('button'); fireEvent.click(button); fireEvent.click(button)
  view.rerender(<PeerTransfers state={state} peer={peer} server={s} locale="en" resetToken="2" />)
  finish(undefined); await waitFor(() => expect(screen.getByRole('button')).toBeDisabled()); expect(s.run).toHaveBeenCalledTimes(1)
})
it('reports unknown acknowledgement without claiming completion', async () => {
  const s = server(); s.run = vi.fn().mockResolvedValue(undefined)
  render(<PeerTransfers state={state} peer={peer} server={s} locale="en" resetToken="1" />)
  fireEvent.click(screen.getAllByRole('checkbox')[0]); fireEvent.click(screen.getByRole('button'))
  await screen.findByText(/Retry was not confirmed/); expect(screen.getByRole('button')).toBeDisabled()
})
it('keeps per-target outcomes for mixed retry acknowledgements', async () => {
  const s = server(); s.run = vi.fn().mockResolvedValueOnce({ ok: true }).mockResolvedValueOnce(undefined)
  render(<PeerTransfers state={state} peer={peer} server={s} locale="en" resetToken="1" />)
  screen.getAllByRole('checkbox').forEach(input => fireEvent.click(input)); fireEvent.click(screen.getByRole('button'))
  await screen.findByText(/b: Retry was not confirmed/)
  expect(screen.getByText(/a: Retry requested/)).toBeInTheDocument(); expect(s.run).toHaveBeenCalledTimes(2)
})
it('clears selection when Core withdraws eligibility', () => {
  const s = server(); const view = render(<PeerTransfers state={state} peer={peer} server={s} locale="en" resetToken="1" />)
  fireEvent.click(screen.getAllByRole('checkbox')[0])
  view.rerender(<PeerTransfers state={{ ...state, transfers: [{ ...transfer('a'), retryable: false, retryBlockedReason: 'busy' }, transfer('b')] }} peer={peer} server={s} locale="en" resetToken="1" />)
  expect(screen.getAllByRole('checkbox')[0]).not.toBeChecked(); expect(screen.getAllByRole('checkbox')[0]).toBeDisabled(); expect(screen.getByRole('button')).toBeDisabled()
})
