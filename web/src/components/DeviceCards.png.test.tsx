import { act, fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Server } from '../useServer'
import type { DecodeResult } from '../qr/decode-job'
import { deviceCardText } from '../device-card-i18n'
import { DeviceCards } from './DeviceCards'
const mocks = vi.hoisted(() => ({ start: vi.fn(), cancel: vi.fn(), dispose: vi.fn() }))
vi.mock('../qr/png-import', () => ({ PngCardImporter: class { start = mocks.start; cancel = mocks.cancel; dispose = mocks.dispose } }))
const card = `soba-card1.${btoa(JSON.stringify({ version: 1, mode: 'lan', publicKey: 'a'.repeat(64), name: 'Synthetic recipient' })).replace(/=/g, '')}`
function setup(locale: 'en' | 'ja' = 'en') {
  const props = { server: { auth: 'ready', stale: false, run: vi.fn() } as unknown as Server, locale, mode: 'lan' as const, publicKey: 'b'.repeat(64), binding: 'synthetic', disabled: false, canUseRecipient: true, onUseRecipient: vi.fn() }
  const view = render(<DeviceCards {...props} />), c = (key: Parameters<typeof deviceCardText>[1]) => deviceCardText(locale, key)
  fireEvent.click(screen.getByRole('button', { name: c('open') }))
  const select = () => fireEvent.change(screen.getByLabelText(c('pngFile')), { target: { files: [new File(['synthetic bytes'], 'card.png')] } })
  return { ...view, props, c, select }
}
beforeEach(() => { vi.clearAllMocks() })
describe('PNG card controls preserve explicit Review and stale boundaries', () => {
  it.each(['en', 'ja'] as const)('fills only card text in %s, without any Core action', async locale => {
    mocks.start.mockResolvedValue({ ok: true, text: card }); const v = setup(locale)
    await act(async () => v.select())
    expect(screen.getByLabelText(v.c('input'))).toHaveValue(card)
    expect(v.props.server.run).not.toHaveBeenCalled(); expect(v.props.onUseRecipient).not.toHaveBeenCalled()
    expect(screen.queryByRole('region', { name: v.c('review') })).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(v.c('pngReady'))
  })
  it.each(['edit', 'cancel', 'close', 'mode', 'auth', 'stale', 'binding', 'policy', 'reselect'] as const)('rejects late results after %s', async boundary => {
    let complete!: (value: DecodeResult) => void
    mocks.start.mockImplementationOnce(() => new Promise(resolve => { complete = resolve })).mockImplementation(() => new Promise(() => {}))
    const v = setup(); await act(async () => v.select())
    if (boundary === 'edit') fireEvent.change(screen.getByLabelText(v.c('input')), { target: { value: 'new draft' } })
    if (boundary === 'cancel') fireEvent.click(screen.getByRole('button', { name: v.c('pngCancel') }))
    if (boundary === 'close') { fireEvent.click(screen.getByRole('button', { name: v.c('close') })); fireEvent.click(screen.getByRole('button', { name: v.c('open') })) }
    if (boundary === 'mode') v.rerender(<DeviceCards {...v.props} mode="direct-lan" />)
    if (boundary === 'auth') v.rerender(<DeviceCards {...v.props} server={{ ...v.props.server, auth: 'locked' } as unknown as Server} />)
    if (boundary === 'stale') v.rerender(<DeviceCards {...v.props} server={{ ...v.props.server, stale: true }} />)
    if (boundary === 'binding') v.rerender(<DeviceCards {...v.props} binding="new synthetic binding" />)
    if (boundary === 'policy') v.rerender(<DeviceCards {...v.props} server={{ ...v.props.server, state: { limits: { effective: { resources: { pngPixels: { mode: 'limited', value: 1 } } } } } } as unknown as Server} />)
    if (boundary === 'reselect') await act(async () => v.select())
    await act(async () => complete({ ok: true, text: card }))
    expect(screen.getByLabelText(v.c('input'))).toHaveValue(boundary === 'edit' ? 'new draft' : '')
    expect(v.props.server.run).not.toHaveBeenCalled(); expect(v.props.onUseRecipient).not.toHaveBeenCalled()
    expect(mocks.cancel.mock.calls.length + mocks.dispose.mock.calls.length).toBeGreaterThan(0)
  })
})
