import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TailnetLogin, readLoginView } from './TailnetLogin'
import { translator } from '../i18n'
import { loginEnglish, loginJapanese } from '../login-i18n'
import type { Server } from '../useServer'

const url = 'https://login.tailscale.com/a/synthetic-ui-fixture'
const matrix = Array.from({ length: 21 }, (_, y) => Array.from({ length: 21 }, (_, x) => x === y))
function setup(results: unknown[] = []) {
  const requests: { name: string; payload: Record<string, unknown>; signal?: AbortSignal }[] = []
  const fetch = vi.fn(async (_path: string, init?: RequestInit) => {
    requests.push({ ...JSON.parse(init!.body as string), signal: init?.signal })
    return new Response(JSON.stringify({ ok: true, result: results.shift() }))
  })
  vi.stubGlobal('fetch', fetch)
  const server = { refresh: vi.fn().mockResolvedValue(null), handleError: vi.fn() } as unknown as Server
  const rendered = render(<TailnetLogin server={server} locale="en" t={translator('en')} disabled={false} />)
  return { requests, fetch, server, ...rendered }
}
afterEach(() => { vi.useRealTimers() })
describe('explicit private sign-in', () => {
  it('has matching bilingual labels and never requests sign-in on mount or a connected status check', async () => {
    expect(Object.keys(loginJapanese)).toEqual(Object.keys(loginEnglish))
    const { requests } = setup([{ state: 'connected', authUrl: url, qr: matrix }])
    expect(requests).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Check sign-in status' }))
    expect(await screen.findByText('This device is connected')).toBeVisible()
    expect(requests.map(item => item.name)).toEqual(['network.login.status'])
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Sign in to Tailscale' })).not.toBeInTheDocument()
  })
  it('shows approval status without restarting login', async () => {
    const { requests } = setup([{ state: 'approval-required' }])
    await userEvent.click(screen.getByRole('button', { name: 'Check sign-in status' }))
    expect(await screen.findByText('Device approval is required')).toBeVisible()
    expect(screen.getByText(/Another sign-in request is not needed/)).toBeVisible()
    expect(requests.map(item => item.name)).toEqual(['network.login.status'])
  })
  it('requests QR locally and clears it immediately when the next response omits it', async () => {
    const { requests } = setup([{ state: 'waiting', authUrl: url }, { state: 'waiting', authUrl: url, qr: matrix }, { state: 'waiting' }])
    await userEvent.click(screen.getByRole('button', { name: 'Sign in to Tailscale' }))
    expect(await screen.findByRole('link', { name: /Continue/ })).toHaveAttribute('href', url)
    await userEvent.click(screen.getByRole('button', { name: 'Show sign-in QR code' }))
    expect(await screen.findByRole('img', { name: 'Private Tailscale sign-in QR code' })).toBeVisible()
    expect(requests[1]).toMatchObject({ name: 'network.login.status', payload: { qr: true } })
    await userEvent.click(screen.getByRole('button', { name: 'Check sign-in status' }))
    await waitFor(() => expect(screen.queryByRole('link')).not.toBeInTheDocument())
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(screen.getByText(/sign-in link is not ready yet/)).toBeVisible()
  })
  it('uses refresh only when the new-link action is explicitly selected', async () => {
    const { requests } = setup([{ state: 'waiting', authUrl: url }, { state: 'waiting', authUrl: url }])
    await userEvent.click(screen.getByRole('button', { name: 'Sign in to Tailscale' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Request a new sign-in link' }))
    expect(requests.map(item => item.payload)).toEqual([{}, { refresh: true }])
  })
  it('polls waiting status without restarting sign-in and stops after dismissal', async () => {
    vi.useFakeTimers()
    const { requests } = setup([{ state: 'waiting', authUrl: url }, { state: 'waiting' }])
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Sign in to Tailscale' })) })
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(requests.map(item => item.name)).toEqual(['network.login', 'network.login.status'])
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Hide sign-in details' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
    expect(requests).toHaveLength(2)
  })
  it('aborts a pending response when closed and never renders late auth data', async () => {
    const { fetch, unmount } = setup()
    let signal: AbortSignal | undefined
    let finish!: (value: Response) => void
    fetch.mockImplementation(async (_path, init) => { signal = init?.signal as AbortSignal; return await new Promise(resolve => { finish = resolve }) })
    await userEvent.click(screen.getByRole('button', { name: 'Sign in to Tailscale' }))
    unmount()
    expect(signal?.aborted).toBe(true)
    await act(async () => { finish(new Response(JSON.stringify({ ok: true, result: { state: 'waiting', authUrl: url, qr: matrix } }))) })
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })
  it('rejects unsafe links and malformed matrices before rendering', () => {
    expect(() => readLoginView({ state: 'waiting', authUrl: 'https://example.com/a/synthetic', qr: matrix })).toThrow()
    expect(() => readLoginView({ state: 'waiting', authUrl: url, qr: [[true]] })).toThrow()
    expect(() => readLoginView({ state: 'waiting', qr: matrix })).toThrow()
    expect(() => readLoginView({ state: 'waiting', authUrl: url, qr: matrix.map(row => row.slice(1)) })).toThrow()
    expect(readLoginView({ state: 'connected', authUrl: url, qr: matrix })).toEqual({ state: 'connected' })
  })
})
