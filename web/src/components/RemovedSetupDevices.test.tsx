import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { Locale } from '../api'
import type { Server } from '../useServer'
import { nearbyText } from '../nearby-i18n'
import { RemovedSetupDevices } from './RemovedSetupDevices'
const record = { peerKey: '4'.repeat(64), generation: 7, verification: 'full-sha256' as const }
const removed = (records = [record]) => ({ ok: true, result: { kind: 'sobalink-setup-revoked', version: 1, records } })
const success = () => ({ ok: true, result: { kind: 'sobalink-setup-reenroll', version: 1, ...record, generation: 8, paired: false, trusted: false } })
const deferred = () => { let resolve!: (value: any) => void; const promise = new Promise<any>(done => { resolve = done }); return { promise, resolve } }
function setup(locale: Locale = 'en') {
  const run = vi.fn<Server['run']>(), guard = vi.fn(), server = { auth: 'ready', stale: false, state: null, run } as unknown as Server
  const view = render(<RemovedSetupDevices server={server} locale={locale} disabled={false} onGuardChange={guard} />), c = (key: Parameters<typeof nearbyText>[1]) => nearbyText(locale, key)
  const open = () => fireEvent.click(screen.getByText(c('removedDevices')))
  const read = async (response = removed()) => { run.mockResolvedValueOnce(response); fireEvent.click(screen.getByRole('button', { name: c('removedLoad') })); await waitFor(() => expect(screen.getByRole('button', { name: c('removedLoad') })).toBeEnabled()) }
  const select = () => { fireEvent.click(screen.getAllByRole('button', { name: c('removedSelect') })[0]); fireEvent.click(screen.getByLabelText(c('removedConsent'))) }
  const submit = () => fireEvent.click(screen.getByRole('button', { name: c('reenroll') }))
  return { ...view, server, run, guard, c, open, read, select, submit }
}
describe('explicit removed-key reenrollment', () => {
  it.each(['en', 'ja'] as const)('reviews full key/generation/basis and requires fresh consent in %s', async locale => {
    const v = setup(locale)
    expect(v.run).not.toHaveBeenCalled(); expect(screen.getByText(v.c('removedDevices')).closest('details')).not.toHaveAttribute('open')
    v.open(); await v.read()
    expect(screen.getByText(record.peerKey)).toBeInTheDocument(); expect(screen.getByText('7')).toBeInTheDocument(); expect(screen.getByText(v.c('fullMode'))).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: v.c('removedSelect') }))
    expect(screen.getByLabelText(v.c('removedConsent'))).not.toBeChecked(); expect(screen.getByRole('button', { name: v.c('reenroll') })).toBeDisabled()
    fireEvent.click(screen.getByLabelText(v.c('removedConsent')))
    const pending = deferred(); v.run.mockReturnValueOnce(pending.promise); v.submit(); v.submit()
    expect(v.run).toHaveBeenCalledTimes(2)
    expect(v.run).toHaveBeenLastCalledWith('setup.reenroll', { peerKey: record.peerKey, expectedGeneration: 7, consent: true }, 'setup.reenroll')
    await act(async () => pending.resolve(success()))
    expect(screen.getByRole('status')).toHaveTextContent(v.c('reenrollSuccess')); expect(screen.getByText('8')).toBeInTheDocument()
    expect(screen.queryByLabelText(v.c('removedConsent'))).not.toBeInTheDocument(); expect(v.guard).toHaveBeenLastCalledWith(false)
    expect(v.run.mock.calls.some(([name]) => ['peer.trust', 'setup.start', 'setup.approve'].includes(name))).toBe(false)
  })
  it.each(['unchanged', 'changed', 'absent'] as const)('recovers a lost response from current revoked state (%s), without replay or inferred success', async state => {
    const v = setup(); v.open(); await v.read(); v.select(); v.run.mockResolvedValueOnce(undefined); v.submit()
    expect(await screen.findByRole('alert')).toHaveTextContent(v.c('reenrollUncertain'))
    expect(screen.getByRole('button', { name: v.c('reenroll') })).toBeDisabled(); expect(v.guard).toHaveBeenLastCalledWith(true)
    await v.read(removed(state === 'absent' ? [] : [{ ...record, generation: state === 'changed' ? 9 : 7 }]))
    expect(screen.getByRole('status')).toHaveTextContent(v.c(state === 'absent' ? 'reenrollNoLongerRemoved' : state === 'changed' ? 'reenrollChanged' : 'reenrollStillRemoved'))
    expect(screen.queryByLabelText(v.c('removedConsent'))).not.toBeInTheDocument(); expect(v.guard).toHaveBeenLastCalledWith(false)
    expect(v.run.mock.calls.filter(([name]) => name === 'setup.reenroll')).toHaveLength(1)
    expect(screen.queryByText(v.c('reenrollSuccess'))).not.toBeInTheDocument()
    if (state !== 'absent') { fireEvent.click(screen.getByRole('button', { name: v.c('removedSelect') })); expect(screen.getByLabelText(v.c('removedConsent'))).not.toBeChecked() }
  })
  it.each([{ generation: 7 }, { generation: 9 }, { peerKey: '5'.repeat(64) }, { verification: 'tofu' }, { paired: true }, { trusted: true }])('rejects a mismatched or authority-granting response: %j', async change => {
    const v = setup(); v.open(); await v.read(); v.select(); const result = success(); v.run.mockResolvedValueOnce({ ...result, result: { ...result.result, ...change } }); v.submit()
    expect(await screen.findByRole('alert')).toHaveTextContent(v.c('reenrollUncertain'))
    expect(v.guard).toHaveBeenLastCalledWith(true)
  })
  it('clears exact selection and consent when the list is re-read', async () => {
    const v = setup(); v.open(); await v.read(); v.select(); await v.read(removed([{ ...record, generation: 9 }]))
    expect(screen.queryByLabelText(v.c('removedConsent'))).not.toBeInTheDocument(); expect(screen.getByText('9')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: v.c('removedSelect') })); expect(screen.getByLabelText(v.c('removedConsent'))).not.toBeChecked()
  })
  it('does not replay pending operations or restore consent after reload', async () => {
    const v = setup(); v.open(); await v.read(); v.select(); v.run.mockResolvedValueOnce(undefined); v.submit(); await screen.findByRole('alert'); v.unmount()
    const next = setup(); expect(next.run).not.toHaveBeenCalled(); next.open(); await next.read(); fireEvent.click(screen.getByRole('button', { name: next.c('removedSelect') }))
    expect(screen.getByLabelText(next.c('removedConsent'))).not.toBeChecked(); expect(next.run).toHaveBeenCalledTimes(1)
  })
  it('discards late mutation responses after auth or process availability changes', async () => {
    const v = setup(); v.open(); await v.read(); v.select(); const pending = deferred(); v.run.mockReturnValueOnce(pending.promise); v.submit()
    v.rerender(<RemovedSetupDevices server={{ ...v.server, stale: true }} locale="en" disabled={false} onGuardChange={v.guard} />)
    await act(async () => pending.resolve(success()))
    expect(screen.getByRole('alert')).toHaveTextContent(v.c('reenrollUncertain')); expect(screen.queryByText(v.c('reenrollSuccess'))).not.toBeInTheDocument(); expect(v.guard).toHaveBeenLastCalledWith(true)
  })
  it('blocks changes while another setup or process prerequisite is unresolved', () => {
    const v = setup(); v.open(); v.rerender(<RemovedSetupDevices server={v.server} locale="en" disabled onGuardChange={v.guard} />)
    expect(screen.getByRole('button', { name: v.c('removedLoad') })).toBeDisabled(); expect(v.run).not.toHaveBeenCalled()
  })
})
