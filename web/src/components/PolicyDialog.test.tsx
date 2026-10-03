import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import type { CapacityPolicy, PolicyConfig, State } from '../api'
import { policyEnglish, policyJapanese } from '../policy-i18n'

const state: State = { csrfToken: 'fixture-token', self: { name: 'This device', status: 'online' }, peers: [], messages: [], transfers: [], services: [], shares: [] }
const config: PolicyConfig = {
  version: 1, revision: 'saved-revision', requested: { version: 1, logical: {}, resources: {} },
  effective: { version: 1, logical: { messageBytes: { mode: 'limited', value: 16384 }, batchEntries: { mode: 'limited', value: 256 }, messageHistoryEntries: { mode: 'limited', value: 128 } }, resources: { messageTextBytes: { mode: 'limited', value: 16384 } } },
  catalog: { logical: { messageBytes: { default: 16384, unit: 'bytes' }, batchEntries: { default: 256, unit: 'entries' }, messageHistoryEntries: { default: 128, unit: 'entries' }, groups: { default: 32, unit: 'entries' } }, resources: { messageTextBytes: { default: 16384, unit: 'bytes' } } },
  adjustable: { logical: { messageBytes: true, batchEntries: true, messageHistoryEntries: true }, resources: { messageTextBytes: true } }, usage: {},
}
function setup(failure?: 'apply' | 'cleanup') {
  const requests: { name: string; payload: { policy?: CapacityPolicy; expectedRevision?: string } }[] = []
  let cleanupReviews = 0
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(state))
    const request = JSON.parse(init!.body as string); requests.push(request)
    let result: unknown
    if (request.name === 'policy.config') result = config
    else if (request.name === 'policy.preview') result = { ...config, requested: request.payload.policy, effective: { ...config.effective, logical: { ...config.effective.logical, ...request.payload.policy.logical } }, revision: 'reviewed-candidate', destructive: false }
    else if (request.name === 'policy.apply') {
      if (failure === 'apply') return new Response(JSON.stringify({ code: 'policy_revision_conflict' }), { status: 409 })
      result = { ...config, requested: request.payload.policy, revision: 'applied-revision' }
    } else if (request.name === 'message.history.preview') result = { version: 1, revision: `history-review-${++cleanupReviews}`, messageIds: ['old-one', 'old-two'], remove: 2, retained: 3, destructive: true }
    else if (request.name === 'message.history.cleanup') {
      if (failure === 'cleanup') return new Response(JSON.stringify({ code: 'history_revision_conflict' }), { status: 409 })
      result = { version: 1 }
    }
    return new Response(JSON.stringify({ ok: true, result }))
  }))
  localStorage.setItem('sobalink.locale', 'en')
  return requests
}
async function open() {
  render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: 'Preferences' }))
  await userEvent.click(screen.getByRole('button', { name: 'Capacity and history' }))
  await screen.findByRole('combobox', { name: 'Message size' })
}
describe('reviewed policy controls', () => {
  it('localizes every shared instruction and displays only supported controls', async () => {
    expect(Object.keys(policyJapanese)).toEqual(Object.keys(policyEnglish))
    setup(); await open()
    expect(screen.queryByRole('combobox', { name: 'Service groups' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('More limits and resource budgets'))
    const resource = screen.getByRole('combobox', { name: 'Message text memory' })
    expect(within(resource).queryByRole('option', { name: 'No policy limit' })).not.toBeInTheDocument()
    expect(screen.getByText(/No policy limit still uses finite resource budgets/)).toBeVisible()
  })
  it('reviews a candidate before applying its exact revision and invalidates review after edits', async () => {
    const requests = setup(); await open()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Message size' }), 'unlimited')
    expect(requests.filter(item => item.name === 'policy.apply')).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Review changes' }))
    await screen.findByRole('button', { name: 'Apply reviewed settings' })
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Items per batch' }), 'limited')
    expect(screen.queryByRole('button', { name: 'Apply reviewed settings' })).not.toBeInTheDocument()
    fireEvent.change(screen.getByRole('spinbutton', { name: 'Items per batch: Limit' }), { target: { value: '512' } })
    await userEvent.click(screen.getByRole('button', { name: 'Review changes' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Apply reviewed settings' }))
    await screen.findByText('Capacity settings saved')
    expect(requests.find(item => item.name === 'policy.apply')?.payload).toEqual({ policy: { version: 1, logical: { messageBytes: { mode: 'unlimited' }, batchEntries: { mode: 'limited', value: 512 } }, resources: {} }, expectedRevision: 'reviewed-candidate' })
    expect(requests.some(item => item.name === 'message.history.cleanup')).toBe(false)
  })
  it('keeps invalid input local and stale application requires a new review', async () => {
    const requests = setup('apply'); await open()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Items per batch' }), 'limited')
    fireEvent.change(screen.getByRole('spinbutton', { name: 'Items per batch: Limit' }), { target: { value: '0' } })
    expect(screen.getByRole('button', { name: 'Review changes' })).toBeDisabled()
    expect(requests.filter(item => item.name === 'policy.preview')).toHaveLength(0)
    fireEvent.change(screen.getByRole('spinbutton', { name: 'Items per batch: Limit' }), { target: { value: '512' } })
    await userEvent.click(screen.getByRole('button', { name: 'Review changes' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Apply reviewed settings' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Settings or saved configuration changed')
    expect(screen.queryByRole('button', { name: 'Apply reviewed settings' })).not.toBeInTheDocument()
    expect(screen.getByRole('spinbutton', { name: 'Items per batch: Limit' })).toHaveValue(512)
  })
  it('shows exact cleanup counts and cancels without deleting or reusing a stale review', async () => {
    const requests = setup('cleanup'); await open()
    await userEvent.click(screen.getByRole('button', { name: 'Review history cleanup' }))
    await screen.findByRole('button', { name: 'Permanently remove reviewed messages' })
    const review = screen.getByLabelText('Review permanent removal')
    expect(review).toHaveTextContent('Messages to remove2')
    expect(review).toHaveTextContent('Messages to keep3')
    expect(review).toHaveTextContent('cannot be undone')
    await userEvent.click(within(review).getByRole('button', { name: 'Cancel' }))
    expect(requests.some(item => item.name === 'message.history.cleanup')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Review history cleanup' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Permanently remove reviewed messages' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('History or retention settings changed'))
    expect(requests.find(item => item.name === 'message.history.cleanup')?.payload).toEqual({ expectedRevision: 'history-review-2' })
    expect(screen.queryByRole('button', { name: 'Permanently remove reviewed messages' })).not.toBeInTheDocument()
  })
})
