import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import * as api from '../api'
import { advancedText } from '../advanced-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { ServiceDiagnostics } from './ServiceDiagnostics'
const failure = { code: 'tcp_unreachable', at: '2026-10-01T00:00:00Z', nextSteps: { en: 'Check the approved listener.', ja: '許可済みの待受を確認してください。' } }
const service: api.Service = { id: 'fixture-service', peerId: 'fixture-studio', name: 'Example', network: 'tcp', status: 'active', ports: '443,8443-8445', lastFailure: failure }
function setup(locale: api.Locale = 'en', current = service) {
  const requests: { name: string; payload: Record<string, unknown> }[] = []
  const server = { stale: false, refresh: vi.fn(), handleError: vi.fn() } as unknown as Server
  vi.stubGlobal('fetch', vi.fn(async (_path: string, init: RequestInit) => {
    const request = JSON.parse(init.body as string); requests.push(request)
    return new Response(JSON.stringify({ ok: true, result: { serviceId: current.id, port: request.payload.port, checkedAt: '2026-10-03T04:00:00Z', code: 'tcp_reachable', transport: 'reachable', application: 'unverified', nextSteps: { en: 'Check application authentication separately.', ja: 'アプリの認証を別途確認してください。' } } }))
  }))
  return { requests, props: { service: current, locale, t: translator(locale), server }, p: (key: string) => advancedText(locale, key) }
}
describe('explicit TCP diagnostics', () => {
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: requires one effective port, then shows TCP-only success and retained last failure`, async () => {
      const { requests, props, p } = setup(locale); render(<ServiceDiagnostics {...props} />)
      await userEvent.click(screen.getByText(p('diagnosticTitle')))
      expect(requests).toHaveLength(0)
      const button = screen.getByRole('button', { name: p('check') })
      expect(button).toBeDisabled()
      fireEvent.change(screen.getByLabelText(p('selectPort')), { target: { value: '444' } })
      expect(button).toBeDisabled()
      fireEvent.change(screen.getByLabelText(p('selectPort')), { target: { value: '8444' } })
      await userEvent.click(button)
      expect(await screen.findByRole('status')).toHaveTextContent(p('reachable'))
      expect(screen.getByRole('status')).toHaveTextContent(p('application'))
      expect(screen.getByRole('status')).toHaveTextContent('2026-10-03T04:00:00Z')
      expect(screen.getByText('tcp_unreachable')).toBeVisible()
      expect(screen.getByText(failure.nextSteps[locale])).toBeVisible()
      expect(requests).toEqual([{ requestId: expect.any(String), name: 'diagnostics.run', payload: { serviceId: 'fixture-service', probeTCP: true, port: 8444 } }])
    })
  }
  it('checks a single approved port explicitly and blocks duplicate submissions while pending', async () => {
    const { props, p } = setup('en', { ...service, ports: '443' }); render(<ServiceDiagnostics {...props} />)
    let finish: (response: Response) => void = () => {}
    const fetch = vi.fn(() => new Promise<Response>(resolve => { finish = resolve })); vi.stubGlobal('fetch', fetch)
    await userEvent.click(screen.getByText(p('diagnosticTitle')))
    expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument()
    const button = screen.getByRole('button', { name: p('check') })
    fireEvent.click(button); fireEvent.click(button)
    expect(fetch).toHaveBeenCalledTimes(1)
    await act(async () => finish(new Response(JSON.stringify({ code: 'diagnostic_capacity' }), { status: 409 })))
    expect(await screen.findByRole('alert')).toHaveTextContent(p('diagnostic_capacity'))
    expect(button).toBeEnabled()
  })
  it('never probes UDP or inactive services, while retained failure history remains visible', () => {
    const { props, p } = setup('en', { ...service, network: 'udp' }); const rendered = render(<ServiceDiagnostics {...props} />)
    expect(screen.queryByRole('button', { name: p('check') })).not.toBeInTheDocument()
    rendered.rerender(<ServiceDiagnostics {...props} service={{ ...service, status: 'stopped' }} />)
    expect(screen.queryByRole('button', { name: p('check') })).not.toBeInTheDocument()
    expect(screen.getByText('tcp_unreachable')).toBeInTheDocument()
  })
  it('aborts a pending check after scope change and ignores a late result', async () => {
    const { props, p } = setup('en', { ...service, ports: '443' }); const rendered = render(<ServiceDiagnostics {...props} />)
    let finish: (response: Response) => void = () => {}
    let signal: AbortSignal | undefined
    vi.stubGlobal('fetch', vi.fn((_url: string, init: RequestInit) => { signal = init.signal!; return new Promise<Response>(resolve => { finish = resolve }) }))
    await userEvent.click(screen.getByText(p('diagnosticTitle')))
    await userEvent.click(screen.getByRole('button', { name: p('check') }))
    rendered.rerender(<ServiceDiagnostics {...props} service={{ ...service, status: 'stopped' }} />)
    expect(signal?.aborted).toBe(true)
    await act(async () => finish(new Response(JSON.stringify({ ok: true, result: {} }))))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
  it('shows a newer authoritative diagnostic after an explicit local check', async () => {
    const { props, p } = setup('en', { ...service, ports: '443' }); const rendered = render(<ServiceDiagnostics {...props} />)
    await userEvent.click(screen.getByText(p('diagnosticTitle')))
    await userEvent.click(screen.getByRole('button', { name: p('check') }))
    await screen.findByRole('status')
    const diagnostic: api.ServiceDiagnostic = { serviceId: service.id, port: 443, checkedAt: '2026-10-03T05:00:00Z', code: 'tcp_unreachable', transport: 'unreachable', application: 'unverified', nextSteps: { en: 'Newer check failed.', ja: '新しい確認に失敗しました。' } }
    rendered.rerender(<ServiceDiagnostics {...props} service={{ ...props.service, diagnostic }} />)
    expect(screen.getByRole('status')).toHaveTextContent('Newer check failed.')
    expect(screen.getByRole('status')).toHaveTextContent('2026-10-03T05:00:00Z')
  })
  it('keeps stale states disabled and localizes transport failure codes without raw details', async () => {
    const { props, p } = setup('ja', { ...service, ports: '443' }); const rendered = render(<ServiceDiagnostics {...props} server={{ ...props.server, stale: true }} />)
    await userEvent.click(screen.getByText(p('diagnosticTitle')))
    expect(screen.getByRole('button', { name: p('check') })).toBeDisabled()
    rendered.rerender(<ServiceDiagnostics {...props} />)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ code: 'diagnostic_service_inactive', error: 'private raw details' }), { status: 409 })))
    await userEvent.click(screen.getByRole('button', { name: p('check') }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(p('diagnostic_service_inactive')))
    expect(document.body).not.toHaveTextContent('private raw details')
  })
})
