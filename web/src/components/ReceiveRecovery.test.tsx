import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { StrictMode } from 'react'
import * as api from '../api'
import { errorText, translator, transferFailureText } from '../i18n'
import { recoveryText } from '../receive-recovery-i18n'
import type { Server } from '../useServer'
import { ReceiveRecoveryDialog } from './ReceiveRecovery'

const legacy: api.ReceiveRecovery = { state: 'blocked', code: 'legacy_review_required', reservedBytes: null, applied: false, review: [...api.RECEIVE_RECOVERY_REVIEW] }
const ready: api.ReceiveRecovery = { ...legacy, state: 'ready', code: '', reservedBytes: 0 }
const result = (view: unknown) => ({ ok: true, result: view } as api.CommandResult)
function setup(locale: api.Locale = 'en', recovery = legacy) {
  const state = { csrfToken: 'fixture-csrf', self: { name: 'Notebook', status: 'online' }, peers: [], messages: [], transfers: [], services: [], shares: [], receiveRecovery: recovery } satisfies api.State
  const server = { state, auth: 'ready', stale: false, error: null, busy: new Set(), refresh: vi.fn(async () => server.state), handleError: vi.fn(), setError: vi.fn(), run: vi.fn() } as unknown as Server
  const command = vi.spyOn(api, 'command').mockImplementation(async () => result(server.state!.receiveRecovery))
  const close = vi.fn(), back = vi.fn()
  const element = () => <ReceiveRecoveryDialog server={server} locale={locale} t={translator(locale)} onClose={close} onBack={back} />
  return { server, command, close, back, element }
}
beforeEach(() => vi.restoreAllMocks())

describe('receive recovery review', () => {
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: previews unknown bytes and requires a complete explicit review`, async () => {
      const { server, command, element } = setup(locale)
      render(element())
      const confirm = await screen.findByRole('button', { name: recoveryText(locale, 'confirm') })
      expect(command).toHaveBeenCalledWith('receive.recovery.confirm', { reviewed: false }, expect.any(String), expect.any(AbortSignal))
      expect(confirm).toBeDisabled()
      expect(screen.getByText(new RegExp(recoveryText(locale, 'unknown').replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeInTheDocument()
      expect(screen.queryByText(/: 0 B/)).not.toBeInTheDocument()
      for (const item of api.RECEIVE_RECOVERY_REVIEW) expect(screen.getByText(recoveryText(locale, item))).toBeInTheDocument()
      await userEvent.click(screen.getByRole('checkbox'))
      command.mockImplementation(async () => { server.state = { ...server.state!, receiveRecovery: ready }; return result({ ...ready, applied: true }) })
      await userEvent.click(confirm)
      expect(await screen.findByText(recoveryText(locale, 'applied'))).toBeInTheDocument()
      expect(command.mock.calls.filter(([, payload]) => 'reviewed' in payload && payload.reviewed)).toHaveLength(1)
      expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
      expect(server.run).not.toHaveBeenCalled()
    })
  }
  it('keeps damaged and future codes blocked with repair guidance and no acknowledgment', async () => {
    for (const code of ['index_unavailable', 'future_code']) {
      const { element, command } = setup('en', { ...legacy, code })
      const ui = render(element())
      expect(await screen.findByText(recoveryText('en', 'repair'))).toBeInTheDocument()
      expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
      expect(screen.queryByRole('button', { name: recoveryText('en', 'confirm') })).not.toBeInTheDocument()
      expect(command.mock.calls.every(([, payload]) => 'reviewed' in payload && payload.reviewed === false)).toBe(true)
      ui.unmount(); vi.restoreAllMocks()
    }
  })
  it('does not acknowledge incomplete or additional review requirements', async () => {
    const { element } = setup('en', { ...legacy, review: [...legacy.review, 'future_requirement'] })
    render(element())
    expect(await screen.findByText(recoveryText('en', 'repair'))).toBeInTheDocument()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  })
  it('discards the checkbox when the server becomes stale or the state changes', async () => {
    const { server, element } = setup(); const ui = render(element())
    await userEvent.click(await screen.findByRole('checkbox'))
    server.stale = true; ui.rerender(element())
    expect(await screen.findByText(recoveryText('en', 'changed'))).toBeInTheDocument()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    server.stale = false; ui.rerender(element())
    await userEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(await screen.findByRole('checkbox')).not.toBeChecked()
    server.state = { ...server.state!, receiveRecovery: { ...legacy, code: 'index_unavailable' } }; ui.rerender(element())
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  })
  it('rejects malformed responses and offers a read-only retry', async () => {
    const { command, element } = setup(); command.mockResolvedValueOnce(result({ ...legacy, reservedBytes: undefined }))
    render(element())
    expect(await screen.findByRole('alert')).toHaveTextContent(translator('en')('invalid_response'))
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(await screen.findByRole('checkbox')).not.toBeChecked()
    expect(command.mock.calls.every(([, payload]) => 'reviewed' in payload && payload.reviewed === false)).toBe(true)
  })
  it('requires a fresh matching snapshot before claiming applied success', async () => {
    const { command, server, element } = setup(); render(element())
    await userEvent.click(await screen.findByRole('checkbox'))
    command.mockResolvedValueOnce(result({ ...ready, applied: true }))
    vi.mocked(server.refresh).mockResolvedValueOnce(null)
    await userEvent.click(screen.getByRole('button', { name: recoveryText('en', 'confirm') }))
    expect(await screen.findByText(recoveryText('en', 'uncertain'))).toBeInTheDocument()
    expect(screen.queryByText(recoveryText('en', 'applied'))).not.toBeInTheDocument()
  })
  it('does not turn unapplied ready state into a claim that this dialog applied a review', async () => {
    const { command, server, element } = setup(); render(element())
    await userEvent.click(await screen.findByRole('checkbox'))
    command.mockImplementationOnce(async () => { server.state = { ...server.state!, receiveRecovery: ready }; return result(ready) })
    await userEvent.click(screen.getByRole('button', { name: recoveryText('en', 'confirm') }))
    expect(await screen.findByText(recoveryText('en', 'ready'))).toBeInTheDocument()
    expect(screen.queryByText(recoveryText('en', 'applied'))).not.toBeInTheDocument()
  })
  it('keeps confirmation valid when allowed autosave changes reserved bytes and displays live usage', async () => {
    const { command, server, element } = setup(); const ui = render(element())
    await userEvent.click(await screen.findByRole('checkbox'))
    command.mockImplementationOnce(async () => { server.state = { ...server.state!, receiveRecovery: { ...ready, reservedBytes: 256 } }; return result({ ...ready, applied: true }) })
    await userEvent.click(screen.getByRole('button', { name: recoveryText('en', 'confirm') }))
    expect(await screen.findByText(recoveryText('en', 'applied'))).toBeInTheDocument()
    expect(screen.getByText('Reserved receive storage: 256 B')).toBeInTheDocument()
    server.state = { ...server.state!, receiveRecovery: { ...ready, reservedBytes: 512 } }; ui.rerender(element())
    expect(screen.getByText('Reserved receive storage: 512 B')).toBeInTheDocument()
    expect(screen.getByText(recoveryText('en', 'applied'))).toBeInTheDocument()
  })
  it('keeps a failed confirmation unconfirmed and clears consent before retry', async () => {
    const { command, element } = setup(); render(element())
    await userEvent.click(await screen.findByRole('checkbox'))
    command.mockRejectedValueOnce(new api.ApiError('receive_recovery_required', 'private-path-must-not-display'))
    await userEvent.click(screen.getByRole('button', { name: recoveryText('en', 'confirm') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(translator('en')('receive_recovery_required'))
    expect(screen.queryByText('private-path-must-not-display')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(await screen.findByRole('checkbox')).not.toBeChecked()
  })
  it('prevents duplicate confirmations while busy and ignores late results after dismissal', async () => {
    const { command, element } = setup(); const ui = render(element())
    await userEvent.click(await screen.findByRole('checkbox'))
    let resolve!: (value: api.CommandResult) => void
    command.mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const confirm = screen.getByRole('button', { name: recoveryText('en', 'confirm') })
    fireEvent.click(confirm); fireEvent.click(confirm)
    expect(command).toHaveBeenCalledTimes(2)
    expect(confirm).toBeDisabled()
    ui.unmount()
    expect(command.mock.calls[1][3]?.aborted).toBe(true)
    await act(async () => resolve(result({ ...ready, applied: true })))
    expect(screen.queryByText(recoveryText('en', 'applied'))).not.toBeInTheDocument()
  })
  it('cancels and goes back without acknowledgment, including StrictMode preview cleanup', async () => {
    const { command, close, back, element } = setup()
    render(<StrictMode>{element()}</StrictMode>)
    await screen.findByRole('checkbox')
    await userEvent.click(screen.getByRole('button', { name: 'Back to preferences' }))
    expect(back).toHaveBeenCalledOnce()
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(close).toHaveBeenCalledOnce()
    expect(command.mock.calls.every(([, payload]) => 'reviewed' in payload && payload.reviewed === false)).toBe(true)
  })
  it('translates recovery errors and fails closed for invalid present state', () => {
    for (const locale of ['en', 'ja'] as const) {
      const t = translator(locale)
      expect(errorText({ code: 'receive_recovery_required' }, t)).toBe(t('receive_recovery_required'))
      expect(transferFailureText('receive_recovery_required', t)).toBe(t('receive_recovery_required'))
    }
    expect(api.receivingBlocked({})).toBe(false)
    expect(api.receivingBlocked({ receiveRecovery: ready })).toBe(false)
    expect(api.receivingBlocked({ receiveRecovery: { ...legacy, state: 'future' } })).toBe(true)
    expect(api.readReceiveRecovery({ ...ready, reservedBytes: null })).toBeNull()
    expect(api.readReceiveRecovery({ ...legacy, applied: true })).toBeNull()
  })
})
