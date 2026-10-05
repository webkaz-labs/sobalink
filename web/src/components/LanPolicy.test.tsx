import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, LanPolicy, Locale, State } from '../api'
import { translator } from '../i18n'
import { policyTranslator } from '../lan-policy-i18n'
import type { Server } from '../useServer'
import { SavedLanPolicy, selectedLANPolicy } from './LanPolicy'

const current: LanPolicy = { mode: 'allowed-lan-destinations', prefixes: ['192.168.50.0/24'], editable: true, restartRequired: true }
function setup(policy: LanPolicy = current, locale: Locale = 'en') {
  const state = { lan: { policy } } as State
  const run = vi.fn<Server['run']>()
  const setError = vi.fn()
  const server = { state, busy: new Set(), run, setError } as unknown as Server
  const view = render(<SavedLanPolicy server={server} state={state} locale={locale} t={translator(locale)} disabled={false} />)
  screen.getByText(policyTranslator(locale)('title')).closest('details')!.open = true
  return { ...view, user: userEvent.setup(), run, setError, update: (next: LanPolicy) => view.rerender(<SavedLanPolicy server={server} state={{ lan: { policy: next } } as State} locale={locale} t={translator(locale)} disabled={false} />) }
}
describe('Saved destination policy review', () => {
  it.each(['en', 'ja'] as const)('requires review, explains widening, and cancels without mutation in %s', async locale => {
    const view = setup(current, locale); const p = policyTranslator(locale)
    await view.user.selectOptions(screen.getByRole('combobox', { name: p('mode') }), 'trusted-relay')
    await view.user.click(screen.getByRole('button', { name: p('review') }))
    expect(screen.getByRole('region', { name: p('review') })).toHaveTextContent(p('expansion'))
    await view.user.click(screen.getByRole('button', { name: translator(locale)('cancel') }))
    expect(view.run).not.toHaveBeenCalled()
  })
  it('shows suggestions without selecting or saving them', async () => {
    const view = setup(); const p = policyTranslator('en')
    view.run.mockResolvedValueOnce({ ok: true, result: { addresses: [{ interface: 'fixture0', address: '192.168.60.10', prefix: '192.168.60.0/24' }] } })
    await view.user.click(screen.getByRole('button', { name: p('suggestions') }))
    expect(screen.getByRole('textbox', { name: p('prefixes') })).toHaveValue('192.168.50.0/24')
    expect(view.run.mock.calls).toEqual([['lan.addresses', {}]])
    await view.user.click(screen.getByRole('button', { name: `${p('choose')}: 192.168.60.0/24 · fixture0` }))
    expect(screen.getByRole('textbox', { name: p('prefixes') })).toHaveValue('192.168.50.0/24\n192.168.60.0/24')
    expect(view.run).not.toHaveBeenCalledWith('lan.policy.set', expect.anything())
  })
  it('saves only the reviewed exact prefixes and does not start a network', async () => {
    const view = setup(); const p = policyTranslator('en')
    fireEvent.change(screen.getByRole('textbox', { name: p('prefixes') }), { target: { value: '192.168.50.0/24\nfd00::/64' } })
    await view.user.click(screen.getByRole('button', { name: p('review') }))
    view.run.mockResolvedValueOnce({ ok: true, result: { ...current, prefixes: ['192.168.50.0/24', 'fd00::/64'] } })
    await view.user.click(screen.getByRole('button', { name: p('apply') }))
    expect(view.run.mock.calls).toEqual([['lan.policy.set', { mode: 'allowed-lan-destinations', prefixes: ['192.168.50.0/24', 'fd00::/64'] }]])
    expect(screen.getByRole('status')).toHaveTextContent(p('saved'))
  })
  it('rejects empty scope and blocks active engines', async () => {
    const view = setup(); const p = policyTranslator('en')
    fireEvent.change(screen.getByRole('textbox', { name: p('prefixes') }), { target: { value: ' \n ' } })
    await view.user.click(screen.getByRole('button', { name: p('review') }))
    expect(screen.getByRole('alert')).toHaveTextContent(p('required'))
    view.update({ ...current, editable: false })
    expect(screen.getByRole('button', { name: p('review') })).toBeDisabled()
    expect(screen.getByText(p('stopped'))).toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
  })
  it('does not claim success for an unacknowledged save and permits the same review to retry', async () => {
    const view = setup(); const p = policyTranslator('en')
    await view.user.click(screen.getByRole('button', { name: p('review') }))
    view.run.mockResolvedValueOnce(undefined)
    await view.user.click(screen.getByRole('button', { name: p('apply') }))
    expect(screen.queryByText(p('saved'))).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: p('review') })).toBeInTheDocument()
  })
  it('discards an obsolete review when live policy changes', async () => {
    const view = setup(); const p = policyTranslator('en')
    await view.user.click(screen.getByRole('button', { name: p('review') }))
    view.update({ ...current, prefixes: ['192.168.50.0/25'] })
    expect(screen.queryByRole('region', { name: p('review') })).not.toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
  })
  it('does not follow an unmounted request with a mutation', async () => {
    const view = setup(); const p = policyTranslator('en')
    let resolve!: (value: CommandResult) => void
    const pending = new Promise<CommandResult>(done => { resolve = done })
    view.run.mockReturnValueOnce(pending)
    await view.user.click(screen.getByRole('button', { name: p('refresh') }))
    view.unmount()
    await act(async () => { resolve({ ok: true, result: { ...current } }); await pending })
    expect(view.run.mock.calls).toEqual([['lan.policy.get', {}]])
  })
  it('rejects a mismatched save response instead of claiming the requested restriction', async () => {
    const view = setup(); const p = policyTranslator('en')
    await view.user.click(screen.getByRole('button', { name: p('review') }))
    view.run.mockResolvedValueOnce({ ok: true, result: { ...current, mode: 'trusted-relay', prefixes: [] } })
    await view.user.click(screen.getByRole('button', { name: p('apply') }))
    expect(view.setError).toHaveBeenCalledWith({ code: 'invalid_response' })
    expect(screen.queryByText(p('saved'))).not.toBeInTheDocument()
  })
  it('does not overwrite newer edits when an older refresh returns', async () => {
    const view = setup(); const p = policyTranslator('en')
    let resolve!: (value: CommandResult) => void
    const pending = new Promise<CommandResult>(done => { resolve = done })
    view.run.mockReturnValueOnce(pending)
    await view.user.click(screen.getByRole('button', { name: p('refresh') }))
    fireEvent.change(screen.getByRole('textbox', { name: p('prefixes') }), { target: { value: '192.168.50.0/25' } })
    await act(async () => { resolve({ ok: true, result: { ...current } }); await pending })
    expect(screen.getByRole('textbox', { name: p('prefixes') })).toHaveValue('192.168.50.0/25')
  })
  it('keeps unselected initial policy absent and canonicalizes only selected text', () => {
    expect(selectedLANPolicy({})).toBeUndefined()
    expect(selectedLANPolicy({ policyMode: 'allowed-lan-destinations', policyPrefixes: 'fd00::/64,192.168.50.0/24\nfd00::/64' })).toEqual({ mode: 'allowed-lan-destinations', prefixes: ['192.168.50.0/24', 'fd00::/64'] })
  })
})
