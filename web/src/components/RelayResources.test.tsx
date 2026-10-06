import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { PolicyDialog } from './PolicyDialog'
import { policyLabel, policyText } from '../policy-i18n'
import { translator } from '../i18n'
import type { Locale, PolicyConfig } from '../api'
import type { Server } from '../useServer'

const keys = ['relayPresenceConnections', 'relayCandidateAttempts', 'relayTLSConnections', 'relayAdmissionConnections']
function setup(locale: Locale, editable = true) {
  const config: PolicyConfig = { version: 1, requested: { version: 1, logical: {}, resources: {} }, effective: { version: 1, logical: {}, resources: Object.fromEntries(keys.map((key, index) => [key, { mode: 'limited', value: [4, 4, 64, 16][index] }])) }, catalog: { logical: {}, resources: Object.fromEntries(keys.map((key, index) => [key, { default: [4, 4, 64, 16][index], unit: index === 1 ? 'attempts' : 'connections' }])) }, adjustable: { logical: {}, resources: Object.fromEntries(keys.map(key => [key, true])) }, revision: 'current', usage: {}, relayResourceEditable: editable, restartRequiredResources: keys }
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true, result: config as unknown as Record<string, unknown> })
  const server = { run, busy: new Set(), stale: false, error: null, setError: vi.fn() } as unknown as Server
  const onClose = vi.fn()
  render(<PolicyDialog server={server} locale={locale} t={translator(locale)} onClose={onClose} />)
  return { run, onClose, user: userEvent.setup(), config }
}
describe('relay resource budget review', () => {
  it.each(['en', 'ja'] as const)('exposes finite overrides and reviews next-start impact in %s', async locale => {
    const view = setup(locale)
    await screen.findByText(policyLabel(locale, 'relayTLSConnections'))
    await view.user.click(screen.getByText(policyText(locale, 'advanced')))
    const select = screen.getByRole('combobox', { name: policyLabel(locale, 'relayTLSConnections') })
    expect(Array.from(select.querySelectorAll('option')).map(option => option.value)).toEqual(['default', 'limited'])
    await view.user.selectOptions(select, 'limited')
    fireEvent.change(screen.getByRole('spinbutton', { name: `${policyLabel(locale, 'relayTLSConnections')}: ${policyText(locale, 'value')}` }), { target: { value: '128' } })
    view.run.mockResolvedValueOnce({ ok: true, result: { version: 1, destructive: false, restartRequired: true, revision: 'reviewed', effective: { ...view.config.effective, resources: { ...view.config.effective.resources, relayTLSConnections: { mode: 'limited', value: 128 } } } } })
    await view.user.click(screen.getByRole('button', { name: policyText(locale, 'review') }))
    const review = screen.getByRole('region', { name: policyText(locale, 'preview') })
    expect(review).toHaveTextContent(policyText(locale, 'relayRestart'))
    expect(view.run).not.toHaveBeenCalledWith('policy.apply', expect.anything())
    await view.user.click(screen.getByRole('button', { name: policyText(locale, 'back') }))
    expect(view.run).not.toHaveBeenCalledWith('policy.apply', expect.anything())
  })
  it('shows active budgets without offering live unsupported changes', async () => {
    setup('en', false)
    await screen.findByText(policyLabel('en', 'relayTLSConnections'))
    await userEvent.click(screen.getByText(policyText('en', 'advanced')))
    for (const key of keys) expect(screen.getByRole('combobox', { name: policyLabel('en', key) })).toBeDisabled()
  })
})
