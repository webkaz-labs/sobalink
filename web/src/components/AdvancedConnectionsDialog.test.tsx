import { StrictMode } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import * as api from '../api'
import { advancedEnglish, advancedJapanese, advancedText } from '../advanced-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { AdvancedConnectionsDialog } from './AdvancedConnectionsDialog'

const state: api.State = { csrfToken: 'fictional-csrf', self: { name: 'Notebook', status: 'online' }, settings: { network: 'tailnet' }, peers: [{ id: 'fixture-studio', name: 'Studio', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct', address: '100.64.0.2' }], services: [], shares: [], messages: [], transfers: [], reservedPorts: [54543, 54544, 54545] }
const revision = 'a'.repeat(64)
function setup(locale: api.Locale = 'en', failure?: string) {
  const requests: { name: string; payload: Record<string, any>; requestId: string }[] = []
  const server: Server = { state, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn().mockResolvedValue(state), run: vi.fn(), handleError: vi.fn(), updatedAt: null, messageBlock: vi.fn().mockResolvedValue(null), messageGuardRevision: 0 }
  let running = false
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(state))
    const request = JSON.parse(init!.body as string); requests.push(request)
    let result: unknown
    if (request.name === 'proxy.list') result = running ? [{ id: 'proxy-one', name: 'example-proxy', backend: 'tailnet', endpoint: '127.0.0.1:1080', targets: [{ peerId: 'fixture-studio', port: 443 }], expiresAt: null, lifetime: 'until-stopped', ttlSeconds: 0, status: 'active', protocol: 'socks5-tcp-connect', authentication: 'required', application: 'unverified' }] : []
    if (request.name === 'proxy.preview') result = { scope: request.payload.scope, revision, endpoint: `${request.payload.scope.loopbackHost === '::1' ? '[::1]' : '127.0.0.1'}:${request.payload.scope.localPort}`, targets: request.payload.scope.targets.map((target: api.ProxyTarget) => ({ ...target, host: 'studio.example' })), authentication: 'username-password-required', application: 'unverified' }
    if (request.name === 'proxy.start') {
      if (failure === 'network_error') throw new TypeError('fictional private failure details')
      if (failure) return new Response(JSON.stringify({ code: failure, error: 'FICTIONAL_CREDENTIAL_SHOULD_NOT_RENDER' }), { status: 409 })
      running = true; result = { id: 'proxy-one', status: 'active', application: 'unverified' }
    }
    if (request.name === 'proxy.stop') { running = false; result = { id: request.payload.id, status: 'stopped' } }
    return new Response(JSON.stringify({ ok: true, result }))
  }))
  api.setCSRFToken(state.csrfToken)
  const close = vi.fn()
  const props = { server, locale, t: translator(locale), onClose: close }
  return { requests, server, close, props, p: (key: string) => advancedText(locale, key) }
}
async function draft(p: (key: string) => string) {
  await screen.findByText(p('none'))
  await userEvent.type(screen.getByLabelText(p('name')), 'example-proxy')
  const target = screen.getByRole('combobox', { name: `${p('target')} 1` })
  expect(target).toHaveAccessibleName(`${p('target')} 1`)
  await userEvent.selectOptions(target, 'fixture-studio')
  expect(target).toHaveAccessibleName(`${p('target')} 1`)
  expect(screen.getByRole('combobox', { name: p('listener') })).toHaveAccessibleName(p('listener'))
  expect(screen.getByRole('combobox', { name: p('lifetime') })).toHaveAccessibleName(p('lifetime'))
}
async function review(p: (key: string) => string) {
  await userEvent.click(screen.getByRole('button', { name: p('review') }))
  await screen.findByRole('button', { name: p('authenticate') })
}
async function authenticate(p: (key: string) => string) {
  await userEvent.click(screen.getByRole('button', { name: p('authenticate') }))
  const username = screen.getByLabelText(p('username')) as HTMLInputElement
  const password = screen.getByLabelText(p('password')) as HTMLInputElement
  await userEvent.type(username, 'fictional-user')
  await userEvent.type(password, 'fictional-password')
  return { username, password }
}
describe('advanced scoped proxy controls', () => {
  it('keeps the entry in preferences and survives StrictMode with no automatic start', async () => {
    const { requests } = setup(); localStorage.setItem('sobalink.locale', 'en')
    render(<StrictMode><App /></StrictMode>)
    await userEvent.click(await screen.findByRole('button', { name: 'Preferences' }))
    await userEvent.click(screen.getByRole('button', { name: 'Advanced connections' }))
    await screen.findByText('No running proxies')
    expect(requests.filter(request => request.name === 'proxy.list')).toHaveLength(1)
    expect(requests.some(request => request.name === 'proxy.start')).toBe(false)
    expect(screen.queryByLabelText('Runtime password')).not.toBeInTheDocument()
  })
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: moves keyboard focus to each new scope review without starting it`, async () => {
      const { requests, props, p } = setup(locale); render(<AdvancedConnectionsDialog {...props} />)
      await draft(p)
      for (let attempt = 0; attempt < 2; attempt++) {
        await review(p)
        const heading = screen.getByRole('heading', { name: p('reviewTitle') })
        expect(heading).toHaveFocus()
        await userEvent.tab()
        expect(screen.getByRole('button', { name: p('back') })).toHaveFocus()
        if (attempt === 0) await userEvent.keyboard('{Enter}')
        else {
          await userEvent.click(screen.getByRole('button', { name: p('authenticate') }))
          expect(screen.getByLabelText(p('username'))).toHaveFocus()
          await userEvent.tab()
          expect(screen.getByLabelText(p('password'))).toHaveFocus()
          await userEvent.click(screen.getByRole('button', { name: p('back') }))
        }
        expect(screen.getByLabelText(p('name'))).toHaveFocus()
      }
      expect(requests.filter(request => request.name === 'proxy.preview')).toHaveLength(2)
      expect(requests.some(request => request.name === 'proxy.start')).toBe(false)
    })
    it(`${locale}: reviews exact scope before credentials and starts once without retaining private fields`, async () => {
      const { requests, server, props, p } = setup(locale); render(<AdvancedConnectionsDialog {...props} />)
      await draft(p)
      await userEvent.selectOptions(screen.getByLabelText(p('lifetime')), 'finite')
      fireEvent.change(screen.getByLabelText(p('seconds')), { target: { value: '259200' } })
      await userEvent.selectOptions(screen.getByLabelText(p('listener')), '::1')
      await review(p)
      const region = screen.getByRole('region', { name: p('reviewTitle') })
      expect(region).toHaveTextContent('[::1]:1080')
      expect(region).toHaveTextContent('fixture-studio')
      expect(region).toHaveTextContent('studio.example · TCP 443')
      expect(region).toHaveTextContent('259200')
      expect(screen.queryByLabelText(p('password'))).not.toBeInTheDocument()
      const fields = await authenticate(p)
      fireEvent.submit(fields.password.closest('form')!)
      fireEvent.submit(fields.password.closest('form')!)
      expect(fields.username.value).toBe(''); expect(fields.password.value).toBe('')
      await screen.findByText(p('started'))
      const starts = requests.filter(request => request.name === 'proxy.start')
      expect(starts).toHaveLength(1)
      expect(starts[0].payload).toMatchObject({ expectedRevision: revision, username: 'fictional-user', password: 'fictional-password', scope: { backend: 'tailnet', loopbackHost: '::1', ttlSeconds: 259200, targets: [{ peerId: 'fixture-studio', port: 443 }] } })
      expect(server.run).not.toHaveBeenCalled()
      expect(JSON.stringify(localStorage)).not.toContain('fictional-user')
      expect(JSON.stringify(history.state)).not.toContain('fictional-password')
      expect(screen.queryByLabelText(p('password'))).not.toBeInTheDocument()
      await userEvent.click(screen.getByRole('button', { name: p('stop') }))
      await screen.findByText(p('stopped'))
      expect(requests.filter(request => request.name === 'proxy.stop').map(request => request.payload)).toEqual([{ id: 'proxy-one' }])
    })
    it(`${locale}: back, close and unmount erase credentials without starting`, async () => {
      const { requests, props, p, close } = setup(locale); const rendered = render(<AdvancedConnectionsDialog {...props} />)
      await draft(p); await review(p); const first = await authenticate(p)
      await userEvent.click(screen.getByRole('button', { name: p('back') }))
      expect(first.username.value).toBe(''); expect(first.password.value).toBe('')
      await review(p); const second = await authenticate(p)
      await userEvent.click(screen.getByRole('button', { name: translator(locale)('close') }))
      expect(close).toHaveBeenCalledOnce(); expect(second.username.value).toBe(''); expect(second.password.value).toBe('')
      fireEvent.change(second.username, { target: { value: 'fictional-again' } })
      fireEvent.change(second.password, { target: { value: 'fictional-again' } })
      rendered.unmount()
      expect(second.username.value).toBe(''); expect(second.password.value).toBe('')
      expect(requests.some(request => request.name === 'proxy.start')).toBe(false)
    })
  }
  it.each([false, true])('keeps an enabled edit return target during a pending refresh (credentials=%s)', async credentials => {
    const { props, p } = setup(); render(<AdvancedConnectionsDialog {...props} />)
    await draft(p); await review(p)
    if (credentials) await userEvent.click(screen.getByRole('button', { name: p('authenticate') }))
    let finish: (response: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { finish = resolve })))
    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await userEvent.click(screen.getByRole('button', { name: p('back') }))
    const heading = screen.getByRole('heading', { name: p('create') })
    expect(screen.getByLabelText(p('name'))).toBeDisabled()
    expect(heading).toHaveFocus()
    await act(async () => finish(new Response(JSON.stringify({ ok: true, result: [] }))))
    expect(heading).toHaveFocus()
    await userEvent.tab()
    expect(screen.getByLabelText(p('name'))).toHaveFocus()
  })
  it('has aligned Japanese and English controls and only current identity target choices', async () => {
    expect(Object.keys(advancedJapanese)).toEqual(Object.keys(advancedEnglish))
    const { props, p, requests } = setup(); render(<AdvancedConnectionsDialog {...props} />); await draft(p)
    const options = within(screen.getByLabelText(`${p('target')} 1`)).getAllByRole('option')
    expect(options.map(option => (option as HTMLOptionElement).value)).toEqual(['', 'fixture-studio'])
    fireEvent.change(screen.getByLabelText(p('localPort')), { target: { value: '54543' } })
    expect(screen.getByRole('button', { name: p('review') })).toBeDisabled()
    expect(requests.some(request => request.name === 'proxy.preview')).toBe(false)
  })
  it('invalidates a reviewed scope on peer identity or network changes and clears detached private inputs', async () => {
    const { props, p, requests } = setup(); const rendered = render(<AdvancedConnectionsDialog {...props} />); await draft(p); await review(p); const fields = await authenticate(p)
    rendered.rerender(<AdvancedConnectionsDialog {...props} server={{ ...props.server, state: { ...state, peers: [{ ...state.peers[0], address: '100.64.0.3' }] } }} />)
    expect(fields.username.value).toBe(''); expect(fields.password.value).toBe('')
    expect(screen.queryByRole('button', { name: p('start') })).not.toBeInTheDocument()
    expect(screen.getByText(p('changed'))).toBeVisible()
    expect(requests.some(request => request.name === 'proxy.start')).toBe(false)
  })
  it('requires fresh review after revision conflict and never renders echoed private server messages', async () => {
    const { requests, props, p } = setup('en', 'proxy_review_required'); render(<AdvancedConnectionsDialog {...props} />); await draft(p); await review(p); const fields = await authenticate(p)
    await userEvent.click(screen.getByRole('button', { name: p('start') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(p('proxy_review_required'))
    expect(document.body).not.toHaveTextContent('FICTIONAL_CREDENTIAL_SHOULD_NOT_RENDER')
    expect(fields.username.value).toBe(''); expect(fields.password.value).toBe('')
    expect(screen.queryByRole('button', { name: p('start') })).not.toBeInTheDocument()
    await review(p)
    await userEvent.click(screen.getByRole('button', { name: p('authenticate') }))
    expect(screen.getByLabelText(p('password'))).toHaveValue('')
    expect(requests.filter(request => request.name === 'proxy.start')).toHaveLength(1)
  })
  it('requires a list refresh after an uncertain start without retaining or replaying credentials', async () => {
    const { requests, props, p } = setup('en', 'network_error'); render(<AdvancedConnectionsDialog {...props} />); await draft(p); await review(p); await authenticate(p)
    await userEvent.click(screen.getByRole('button', { name: p('start') }))
    await screen.findByText(p('startUncertain'))
    expect(screen.getByRole('button', { name: p('review') })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(screen.getByRole('button', { name: p('review') })).toBeEnabled())
    expect(requests.filter(request => request.name === 'proxy.start')).toHaveLength(1)
    expect(requests.filter(request => request.name === 'proxy.list')).toHaveLength(2)
  })
  it('rejects oversized UTF-8 credentials locally and erases both inputs', async () => {
    const { props, p, requests } = setup(); render(<AdvancedConnectionsDialog {...props} />); await draft(p); await review(p); const fields = await authenticate(p)
    fireEvent.change(fields.password, { target: { value: 'あ'.repeat(86) } })
    await userEvent.click(screen.getByRole('button', { name: p('start') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(p('credentialInvalid'))
    expect(fields.username.value).toBe(''); expect(fields.password.value).toBe('')
    expect(requests.some(request => request.name === 'proxy.start')).toBe(false)
  })
  it('removes expired proxies from authoritative state and rejects stale list results', async () => {
    const { props, p } = setup()
    const proxy: api.ProxyView = { id: 'old-proxy', name: 'Expired example', backend: 'tailnet', endpoint: '127.0.0.1:1080', targets: [{ peerId: 'fixture-studio', port: 443 }], expiresAt: null, lifetime: 'until-stopped', ttlSeconds: 0, status: 'active', protocol: 'socks5-tcp-connect', authentication: 'required', application: 'unverified' }
    let finish: (response: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { finish = resolve })))
    const rendered = render(<AdvancedConnectionsDialog {...props} server={{ ...props.server, state: { ...state, proxies: [proxy] } }} />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh' })).toBeDisabled())
    expect(screen.getByText('Expired example')).toBeVisible()
    rendered.rerender(<AdvancedConnectionsDialog {...props} server={{ ...props.server, state: { ...state, proxies: [] } }} />)
    expect(screen.queryByText('Expired example')).not.toBeInTheDocument()
    await act(async () => finish(new Response(JSON.stringify({ ok: true, result: [proxy] }))))
    expect(screen.queryByText('Expired example')).not.toBeInTheDocument()
    expect(screen.getByText(p('none'))).toBeVisible()
  })
  it('does not announce a late start as active after the current network has stopped', async () => {
    const { props, p } = setup(); const rendered = render(<AdvancedConnectionsDialog {...props} />)
    await draft(p); await review(p); const fields = await authenticate(p)
    let finish: (response: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn((_path: string, init: RequestInit) => JSON.parse(init.body as string).name === 'proxy.start' ? new Promise<Response>(resolve => { finish = resolve }) : Promise.resolve(new Response(JSON.stringify({ ok: true, result: [] })))))
    await userEvent.click(screen.getByRole('button', { name: p('start') }))
    rendered.rerender(<AdvancedConnectionsDialog {...props} server={{ ...props.server, state: { ...state, self: { ...state.self, status: 'offline' }, proxies: [] } }} />)
    await act(async () => finish(new Response(JSON.stringify({ ok: true, result: { id: 'late-proxy', status: 'active', application: 'unverified' } }))))
    expect(screen.queryByText(p('started'))).not.toBeInTheDocument()
    expect(screen.getByText(p('changed'))).toBeVisible()
    expect(fields.username.value).toBe(''); expect(fields.password.value).toBe('')
  })
  it('discards a late preview after a network change', async () => {
    const { props, p } = setup(); const rendered = render(<AdvancedConnectionsDialog {...props} />); await draft(p)
    let finish: (response: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { finish = resolve })))
    await userEvent.click(screen.getByRole('button', { name: p('review') }))
    rendered.rerender(<AdvancedConnectionsDialog {...props} server={{ ...props.server, state: { ...state, settings: { network: 'none' } } }} />)
    await act(async () => finish(new Response(JSON.stringify({ ok: true, result: {} }))))
    expect(screen.queryByRole('button', { name: p('authenticate') })).not.toBeInTheDocument()
    expect(screen.getByText(p('noNetwork'))).toBeVisible()
  })
})
