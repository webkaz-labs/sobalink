import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, Locale, State } from '../api'
import { translator } from '../i18n'
import { resumeEnglish, resumeJapanese, resumeText, type NetworkResumeReview } from '../network-resume'
import type { Server } from '../useServer'
import { ResumeConnection } from './ResumeConnection'

function review(): NetworkResumeReview {
  return { revision: 'a'.repeat(64), mode: 'direct-lan', hostname: 'example-node', backends: ['direct-lan'], externalControlTraffic: false, trustedDevices: 1, pairedDevices: 1, directLAN: { listen: '192.0.2.10:48443', prefixes: ['192.0.2.0/24'] }, automaticReceivers: [{ peerId: 'example-peer', name: 'Example', directory: 'example-receive' }], startupSuppressed: true, savedProxiesSuppressed: true, pendingStartup: ['example-service'] }
}
function state(): State {
  return { csrfToken: 'example-session', processId: 123, self: { name: 'example-node', status: 'idle', guidance: { code: 'network_stopped', category: 'availability', action: 'review_network', summary: { en: 'Stopped', ja: '停止中' }, nextSteps: { en: 'Review', ja: '確認' } } }, settings: { network: 'direct-lan' }, peers: [], messages: [], transfers: [], services: [], shares: [] }
}
function setup(locale: Locale = 'en') {
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true, result: review() as unknown as CommandResult['result'] })
  const server = { run, auth: 'ready', stale: false, busy: new Set() } as unknown as Server
  const initial = state()
  const view = render(<ResumeConnection server={server} state={initial} locale={locale} t={translator(locale)} blocked={false} />)
  return { ...view, run, server, initial, user: userEvent.setup(), update: (next = initial, patch: Partial<Server> = {}) => view.rerender(<ResumeConnection server={{ ...server, ...patch }} state={next} locale={locale} t={translator(locale)} blocked={Boolean(patch.stale)} />) }
}
function deferred() { let resolve!: (value: CommandResult) => void; const promise = new Promise<CommandResult>(done => { resolve = done }); return { promise, resolve } }

describe('explicit saved connection resume', () => {
  it('keeps bilingual labels aligned', () => { expect(Object.keys(resumeJapanese)).toEqual(Object.keys(resumeEnglish)) })
  it.each(['en', 'ja'] as const)('reviews exact scope, cancels without starting and applies once in %s', async locale => {
    const view = setup(locale), d = (key: keyof typeof resumeEnglish) => resumeText(locale, key)
    expect(view.run).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: d('review') }))
    expect(view.run.mock.calls).toEqual([['network.resume.preview', {}]])
    const region = screen.getByRole('region')
    for (const value of ['example-node', '192.0.2.10:48443', '192.0.2.0/24', 'example-receive', d('impact'), d('suppressed')]) expect(region).toHaveTextContent(value)
    await view.user.click(screen.getByRole('button', { name: translator(locale)('cancel') }))
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
    await view.user.click(screen.getByRole('button', { name: d('review') }))
    const pending = deferred(); view.run.mockReturnValueOnce(pending.promise)
    const start = screen.getByRole('button', { name: d('start') })
    fireEvent.click(start); fireEvent.click(start)
    expect(view.run.mock.calls.at(-1)).toEqual(['network.resume', { expectedRevision: 'a'.repeat(64) }])
    expect(view.run).toHaveBeenCalledTimes(3)
    await act(async () => { pending.resolve({ ok: true, result: { state: 'starting' } }); await pending.promise })
    expect(screen.getByRole('status')).toHaveTextContent(d('accepted'))
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
  it.each(['session', 'settings', 'stale', 'process'] as const)('invalidates a review after %s changes without reviving it', async change => {
    const view = setup()
    await view.user.click(screen.getByRole('button', { name: resumeText('en', 'review') }))
    const next = structuredClone(view.initial)
    if (change === 'session') next.csrfToken = 'another-session'
    if (change === 'settings') next.settings!.network = 'tailnet'
    if (change === 'process') next.processId = 456
    view.update(next, change === 'stale' ? { stale: true } : {})
    view.update()
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
    expect(view.run).toHaveBeenCalledTimes(1)
  })
  it.each(['close', 'newer-state'] as const)('discards a delayed preview after %s', async change => {
    const view = setup(), pending = deferred(); view.run.mockReturnValueOnce(pending.promise)
    fireEvent.click(screen.getByRole('button', { name: resumeText('en', 'review') }))
    if (change === 'close') view.unmount()
    else view.update({ ...view.initial, csrfToken: 'another-session' })
    await act(async () => { pending.resolve({ ok: true, result: review() as unknown as CommandResult['result'] }); await pending.promise })
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
    expect(view.run).toHaveBeenCalledTimes(1)
  })
  it('never offers resume for running or unconfigured state', () => {
    const view = setup(); view.update({ ...view.initial, self: { name: 'example', status: 'running' } })
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    view.update({ ...view.initial, settings: { network: 'none' } })
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
  })
})
