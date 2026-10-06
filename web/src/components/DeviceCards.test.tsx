import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, Locale } from '../api'
import { cardDigest, type DeviceCard, type DeviceCardMode } from '../device-cards'
import { deviceCardText } from '../device-card-i18n'
import type { Server } from '../useServer'
import { DeviceCards } from './DeviceCards'
const key = 'a1'.repeat(32), remote = 'b2'.repeat(32), pin = 'c3'.repeat(32)
const card = (mode: DeviceCardMode = 'lan'): DeviceCard => ({ version: 1, mode, publicKey: remote, name: 'Synthetic recipient', ...(mode === 'lan' ? { relay: { address: '192.0.2.3:443', certificateSHA256: pin } } : { endpoint: '192.168.50.3:48444' }) })
const encode = (value: unknown) => 'soba-card1.' + btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(value)))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const response = async (value = card()) => ({ ok: true, result: { ...value, verification: 'unverified', freshness: 'unknown', contentDigest: await cardDigest(encode(value)) } })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
function setup({ mode = 'lan', locale = 'en', publicKey = key, disabled = false, canUseRecipient = true }: { mode?: DeviceCardMode; locale?: Locale; publicKey?: string; disabled?: boolean; canUseRecipient?: boolean } = {}) {
  const user = userEvent.setup(), run = vi.fn<Server['run']>(), useRecipient = vi.fn()
  const server = { auth: 'ready', stale: false, run, busy: new Set(), setError: vi.fn() } as unknown as Server
  const props = { server, locale, mode, publicKey, disabled, canUseRecipient, binding: 'synthetic-config', onUseRecipient: useRecipient }
  const view = render(<DeviceCards {...props} />), c = (key: Parameters<typeof deviceCardText>[1]) => deviceCardText(locale, key)
  fireEvent.click(screen.getByRole('button', { name: c('open') }))
  return { ...view, user, run, useRecipient, c, props, update: (patch: Partial<typeof props>) => view.rerender(<DeviceCards {...props} {...patch} />) }
}
function fill(value = card()) { fireEvent.change(screen.getByLabelText(deviceCardText('en', 'input')), { target: { value: encode(value) } }) }
describe('read-only device-card controls', () => {
  it.each(['en', 'ja'] as const)('exports an explicit alias in %s with no identity initialization or hostname default', async locale => {
    const v = setup({ locale }), copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    expect(screen.getByLabelText(v.c('alias'), { exact: false })).toHaveValue(''); expect(v.run).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText(v.c('alias'), { exact: false }), { target: { value: 'Chosen alias' } })
    const own = { version: 1, mode: 'lan', publicKey: key, name: 'Chosen alias' }
    v.run.mockResolvedValueOnce({ ok: true, result: { ...own, card: encode(own), verification: 'unverified', freshness: 'unknown' } })
    await v.user.dblClick(screen.getByRole('button', { name: v.c('export') }))
    expect(v.run.mock.calls).toEqual([['device-card.export', { mode: 'lan', name: 'Chosen alias', includeEndpointHint: false }, 'device-card.export:lan']])
    expect(copy).not.toHaveBeenCalled(); expect(screen.getByLabelText(v.c('exported'))).toHaveValue(encode(own))
    await v.user.click(screen.getByRole('button', { name: v.c('copy') })); expect(copy).toHaveBeenCalledWith(encode(own))
    expect(localStorage.length).toBe(0); expect(sessionStorage.length).toBe(0)
  })
  it.each(['en', 'ja'] as const)('reviews uncertainty then copies only key/name into an inert draft in %s', async locale => {
    const v = setup({ locale }); fireEvent.change(screen.getByLabelText(v.c('input')), { target: { value: encode(card()) } }); v.run.mockResolvedValueOnce(await response())
    await v.user.dblClick(screen.getByRole('button', { name: v.c('inspect') }))
    const region = await screen.findByRole('region', { name: v.c('review') })
    expect(region).toHaveTextContent(v.c('unverified')); expect(region).toHaveTextContent(v.c('unknown')); expect(region).toHaveTextContent(pin); expect(region.querySelector('a')).toBeNull()
    expect(v.useRecipient).not.toHaveBeenCalled(); expect(v.run.mock.calls).toEqual([['device-card.inspect', { card: encode(card()), expectedMode: 'lan' }, 'device-card.inspect:lan']])
    await v.user.dblClick(screen.getByRole('button', { name: v.c('use') }))
    expect(v.useRecipient.mock.calls).toEqual([[{ publicKey: remote, name: 'Synthetic recipient' }]]); expect(v.run).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('region', { name: v.c('review') })).not.toBeInTheDocument(); expect(screen.getByRole('status')).toHaveTextContent(v.c('used'))
  })
  it.each(['soba-lan1.synthetic-private-invitation', 'soba-directlan1.synthetic-private-invitation', 'soba-card1.not-valid', 'private'.repeat(1000)])('rejects malformed/oversized private input locally without echo', value => {
    const v = setup(); fireEvent.change(screen.getByLabelText(v.c('input')), { target: { value } }); fireEvent.submit(screen.getByRole('button', { name: v.c('inspect') }).closest('form')!)
    expect(v.run).not.toHaveBeenCalled(); expect(v.useRecipient).not.toHaveBeenCalled(); expect(screen.queryByDisplayValue(value)).not.toBeInTheDocument(); expect(v.container.textContent).not.toContain(value); expect(screen.getByRole('alert')).toBeInTheDocument()
  })
  it('rejects wrong-mode cards before inspection', () => {
    const v = setup(); fill(card('direct-lan')); fireEvent.submit(screen.getByRole('button', { name: v.c('inspect') }).closest('form')!)
    expect(v.run).not.toHaveBeenCalled(); expect(screen.getByRole('alert')).toHaveTextContent(v.c('wrongMode'))
  })
  it('allows inspection without identity, configuration or listener readiness', async () => {
    const v = setup({ publicKey: '' }); fill(); expect(screen.getByRole('button', { name: v.c('export') })).toBeDisabled(); v.run.mockResolvedValue(await response())
    await v.user.click(screen.getByRole('button', { name: v.c('inspect') })); await screen.findByRole('region', { name: v.c('review') }); expect(v.run.mock.calls.map(call => call[0])).toEqual(['device-card.inspect'])
  })
  it('prevents replacing an active invitation recipient', async () => {
    const v = setup({ canUseRecipient: false }); fill(); v.run.mockResolvedValue(await response()); await v.user.click(screen.getByRole('button', { name: v.c('inspect') }))
    expect(await screen.findByRole('button', { name: v.c('use') })).toBeDisabled(); expect(v.useRecipient).not.toHaveBeenCalled()
  })
  it.each(['edit', 'sameInputEvent', 'close', 'unmount', 'mode', 'identity', 'config', 'stale', 'auth'] as const)('ignores inspection completing after %s', async reason => {
    const v = setup(), pending = deferred<CommandResult>(); fill(); v.run.mockReturnValue(pending.promise); await v.user.dblClick(screen.getByRole('button', { name: v.c('inspect') })); await waitFor(() => expect(v.run).toHaveBeenCalledTimes(1))
    if (reason === 'edit') fill({ ...card(), name: 'New input' })
    if (reason === 'sameInputEvent') { fill({ ...card(), name: 'Temporary' }); fill() }
    if (reason === 'close') await v.user.click(screen.getByRole('button', { name: v.c('close') }))
    if (reason === 'unmount') v.unmount()
    if (reason === 'mode') v.update({ mode: 'direct-lan' })
    if (reason === 'identity') v.update({ publicKey: pin })
    if (reason === 'config') v.update({ binding: 'different-config' })
    if (reason === 'stale') v.update({ server: { ...v.props.server, stale: true } })
    if (reason === 'auth') v.update({ server: { ...v.props.server, auth: 'locked' } })
    await act(async () => { pending.resolve(await response()); await pending.promise })
    expect(screen.queryByRole('region', { name: v.c('review') })).not.toBeInTheDocument(); expect(v.useRecipient).not.toHaveBeenCalled()
  })
  it.each(['edit', 'cancel', 'config', 'identity', 'mode', 'stale'] as const)('invalidates existing review on %s', async reason => {
    const v = setup(); fill(); v.run.mockResolvedValue(await response()); await v.user.click(screen.getByRole('button', { name: v.c('inspect') })); await screen.findByRole('region', { name: v.c('review') })
    if (reason === 'edit') fill({ ...card(), name: 'Edited alias' })
    if (reason === 'cancel') await v.user.click(screen.getByRole('button', { name: v.c('cancel') }))
    if (reason === 'config') v.update({ binding: 'new-prefix-or-pin' })
    if (reason === 'identity') v.update({ publicKey: pin })
    if (reason === 'mode') v.update({ mode: 'direct-lan' })
    if (reason === 'stale') v.update({ server: { ...v.props.server, stale: true } })
    expect(screen.queryByRole('button', { name: v.c('use') })).not.toBeInTheDocument(); expect(v.useRecipient).not.toHaveBeenCalled()
  })
  it.each(['edit', 'close', 'identity'] as const)('drops late export after %s', async reason => {
    const v = setup(), pending = deferred<CommandResult>(); fireEvent.change(screen.getByLabelText(v.c('alias'), { exact: false }), { target: { value: 'Chosen alias' } }); v.run.mockReturnValue(pending.promise); await v.user.click(screen.getByRole('button', { name: v.c('export') }))
    if (reason === 'edit') fireEvent.change(screen.getByLabelText(v.c('alias'), { exact: false }), { target: { value: 'Different alias' } })
    if (reason === 'close') await v.user.click(screen.getByRole('button', { name: v.c('close') }))
    if (reason === 'identity') v.update({ publicKey: pin })
    const own = { version: 1, mode: 'lan', publicKey: key, name: 'Chosen alias' }; await act(async () => pending.resolve({ ok: true, result: { ...own, card: encode(own), verification: 'unverified', freshness: 'unknown' } }))
    expect(screen.queryByLabelText(v.c('exported'))).not.toBeInTheDocument()
  })
  it('offers selectable text when clipboard and file saving are unavailable', async () => {
    const v = setup(), copy = vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('Denied')), own = { version: 1, mode: 'lan', publicKey: key, name: 'Chosen alias' }
    v.run.mockResolvedValue({ ok: true, result: { ...own, card: encode(own), verification: 'unverified', freshness: 'unknown' } }); fireEvent.change(screen.getByLabelText(v.c('alias'), { exact: false }), { target: { value: own.name } })
    await v.user.click(screen.getByRole('button', { name: v.c('export') })); await v.user.click(screen.getByRole('button', { name: v.c('copy') }))
    expect(copy).toHaveBeenCalledTimes(1); expect(screen.getByRole('status')).toHaveTextContent(v.c('copyFailed'))
    const text = screen.getByLabelText(v.c('exported')) as HTMLTextAreaElement; expect(text).toHaveFocus(); expect(text.selectionEnd).toBe(encode(own).length)
    vi.spyOn(URL, 'createObjectURL').mockImplementation(() => { throw new Error('Unavailable') }); await v.user.click(screen.getByRole('button', { name: v.c('download') })); expect(screen.getByRole('status')).toHaveTextContent(v.c('downloadFailed'))
  })
  it('renders only an explicitly requested strict quiet-zone bitmap', async () => {
    const v = setup(), own = { version: 1, mode: 'lan', publicKey: key, name: 'Chosen alias' }, qr = Array.from({ length: 29 }, (_, y) => Array.from({ length: 29 }, (_, x) => x === 4 && y === 4))
    fireEvent.change(screen.getByLabelText(v.c('alias'), { exact: false }), { target: { value: own.name } }); await v.user.click(screen.getByLabelText(v.c('qr')))
    v.run.mockResolvedValue({ ok: true, result: { ...own, card: encode(own), verification: 'unverified', freshness: 'unknown', qr } }); await v.user.click(screen.getByRole('button', { name: v.c('export') }))
    const image = await screen.findByRole('img', { name: v.c('qrAlt') }); expect(image).toHaveAttribute('viewBox', '0 0 29 29'); expect(image.querySelector('rect')).toHaveAttribute('fill', '#fff')
    expect(v.run).toHaveBeenCalledWith('device-card.export', { mode: 'lan', name: own.name, includeEndpointHint: false, qr: true }, 'device-card.export:lan')
  })
  it('rejects malformed responses without echoing unexpected private fields', async () => {
    const v = setup(); fill(); v.run.mockResolvedValue({ ...(await response()), result: { ...(await response()).result, privateKey: 'synthetic-private-value' } }); await v.user.click(screen.getByRole('button', { name: v.c('inspect') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(v.c('invalidResponse')); expect(v.container.textContent).not.toContain('synthetic-private-value'); expect(v.useRecipient).not.toHaveBeenCalled()
  })
  it('loads bounded plain-text files without inspecting or importing automatically', async () => {
    const v = setup(), text = encode(card()), bytes = new TextEncoder().encode(text), file = new File([text], 'synthetic-card.txt', { type: 'text/plain' }); Object.defineProperty(file, 'arrayBuffer', { value: async () => bytes.buffer })
    fireEvent.change(screen.getByLabelText(v.c('file')), { target: { files: [file] } }); await waitFor(() => expect(screen.getByLabelText(v.c('input'))).toHaveValue(text)); expect(v.run).not.toHaveBeenCalled(); expect(v.useRecipient).not.toHaveBeenCalled()
  })
  it('rejects oversized files before reading without displaying filenames', () => {
    const v = setup(), file = new File(['a'.repeat(1041)], 'synthetic-private-filename.txt'), arrayBuffer = vi.fn(); Object.defineProperty(file, 'arrayBuffer', { value: arrayBuffer })
    fireEvent.change(screen.getByLabelText(v.c('file')), { target: { files: [file] } }); expect(screen.getByRole('alert')).toHaveTextContent(v.c('tooLarge')); expect(arrayBuffer).not.toHaveBeenCalled(); expect(v.container.textContent).not.toContain(file.name); expect(v.run).not.toHaveBeenCalled()
  })
  it('drops late file reading after newer input', async () => {
    const v = setup(), pending = deferred<ArrayBuffer>(), file = new File(['x'], 'synthetic-card.txt'); Object.defineProperty(file, 'arrayBuffer', { value: () => pending.promise })
    fireEvent.change(screen.getByLabelText(v.c('file')), { target: { files: [file] } }); fill({ ...card(), name: 'Newer input' }); await act(async () => pending.resolve(new TextEncoder().encode(encode(card())).buffer)); expect(screen.getByLabelText(v.c('input'))).toHaveValue(encode({ ...card(), name: 'Newer input' }))
  })
  it('blocks commands while disabled even on direct form submission', () => {
    const v = setup({ disabled: true }); fill(); fireEvent.submit(screen.getByRole('button', { name: v.c('inspect') }).closest('form')!); expect(v.run).not.toHaveBeenCalled(); expect(v.useRecipient).not.toHaveBeenCalled()
  })
})

it('saves exact public card text under a generic filename only after the explicit file action', async () => {
  const v = setup(), own = { version: 1, mode: 'lan', publicKey: key, name: 'Chosen alias' }
  const create = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:synthetic-public-card'), revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { expect(this.download).toBe('sobalink-lan-device-card.txt'); expect(this.href).toBe('blob:synthetic-public-card') })
  v.run.mockResolvedValue({ ok: true, result: { ...own, card: encode(own), verification: 'unverified', freshness: 'unknown' } })
  fireEvent.change(screen.getByLabelText(v.c('alias'), { exact: false }), { target: { value: own.name } }); await v.user.click(screen.getByRole('button', { name: v.c('export') }))
  expect(create).not.toHaveBeenCalled(); expect(click).not.toHaveBeenCalled()
  await v.user.click(screen.getByRole('button', { name: v.c('download') }))
  expect(create).toHaveBeenCalledTimes(1); expect(await (create.mock.calls[0][0] as Blob).text()).toBe(encode(own)); expect(click).toHaveBeenCalledTimes(1); expect(revoke).toHaveBeenCalledWith('blob:synthetic-public-card')
  expect(document.querySelector('a[download]')).toBeNull()
})

it('keeps keyboard focus on unchanged state refreshes after a review', async () => {
  const v = setup(); fill(); v.run.mockResolvedValue(await response())
  await v.user.click(screen.getByRole('button', { name: v.c('inspect') }))
  const review = await screen.findByRole('region', { name: v.c('review') })
  expect(review).toHaveFocus()
  const alias = screen.getByLabelText(v.c('alias'), { exact: false })
  alias.focus(); expect(alias).toHaveFocus()
  v.update({}); expect(alias).toHaveFocus()
})
