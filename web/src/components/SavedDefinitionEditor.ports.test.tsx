import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, State } from '../api'
import { source, proposal } from '../port-proposals-fixture'
import { translator } from '../i18n'
import { portProposalText } from '../port-proposals-i18n'
import { savedEditorText } from '../saved-editor-i18n'
import type { Server } from '../useServer'
import { SavedDefinitionEditor } from './SavedDefinitionEditor'
const state: State = { csrfToken: 'synthetic', self: { name: 'Example', status: 'online' }, settings: { network: 'tailnet' }, peers: [], services: [], shares: [], transfers: [], messages: [] }
function setup(locale: Locale = 'en') {
  const run = vi.fn<Server['run']>().mockImplementation(async (name, payload) => ({ ok: true, result: name === 'service.config' ? source as any : { ...source, configuration: (payload as any).configuration } }))
  const server = { state, stale: false, busy: new Set(), error: null, setError: vi.fn(), run } as unknown as Server
  return { run, e: (key: string) => savedEditorText(locale, key), props: { server, locale, t: translator(locale), mode: 'connect' as const, services: [source.configuration], source: { id: source.configuration.id, intent: 'edit' as const }, portChoice: { source, localPort: 49152, checkedAt: proposal.checkedAt }, onClose: vi.fn(), onSaved: vi.fn() } }
}
// Source guard only; hosted browser assertions measure the complete modal body.
const fileSystemModule = 'node:fs'
const { readFileSync } = await import(fileSystemModule) as { readFileSync(path: string, encoding: 'utf8'): string }
it('lays out the multi-paragraph port choice vertically with breakable values', () => {
  const styles = readFileSync('src/styles.css', 'utf8')
  const rule = styles.match(/\.port-choice-note \{([^}]+)\}/)?.[1]
  expect(rule).toMatch(/display:\s*block;/)
  expect(rule).toMatch(/min-width:\s*0;/)
  expect(rule).toMatch(/overflow-wrap:\s*anywhere;/)
})
describe('candidate handoff to reviewed stopped definition', () => {
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: rereads exact revision, changes only draft local port and saves only after full review`, async () => {
      const { props, e, run } = setup(locale); const t = translator(locale); render(<SavedDefinitionEditor {...props} />)
      const local = await screen.findByLabelText(t('localStart')); expect(local).toHaveValue(49152)
      const chosen = screen.getByRole('region', { name: portProposalText(locale, 'chosen') })
      expect(chosen).toHaveClass('port-choice-note')
      expect(chosen.querySelectorAll(':scope > p')).toHaveLength(7)
      expect(chosen).toHaveTextContent('127.0.0.1:18080 → 8000; 127.0.0.1:18081 → 8002')
      expect(run.mock.calls.map(([name]) => name)).toEqual(['service.config'])
      expect(source.configuration.localPort).toBe(18080)
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      const review = screen.getByRole('region', { name: e('reviewTitle') })
      for (const value of ['example-forward', 'example-peer', 'tailnet', 'TCP', '8000-8002', '8001', '127.0.0.1:49152-49153', '8000, 8002', '7200', 'generic']) expect(review).toHaveTextContent(value)
      await userEvent.click(screen.getByRole('button', { name: e('back') })); expect(run).toHaveBeenCalledTimes(1)
      await userEvent.click(screen.getByRole('button', { name: e('review') })); await userEvent.click(screen.getByRole('button', { name: e('confirm') }))
      expect(run.mock.calls[1]).toEqual(['service.save', { configuration: { ...source.configuration, peerIds: undefined, serviceId: undefined, serviceRevision: undefined, localPort: 49152 }, expectedRevision: source.revision }])
      expect(props.onSaved).toHaveBeenCalledOnce()
    })
  }
  it('rejects a source changed before draft entry; explicit reload discards the candidate instead of reapplying it', async () => {
    const { props, e, run } = setup(); run.mockResolvedValue({ ok: true, result: { ...source, revision: 'b'.repeat(64), configuration: { ...source.configuration, localPort: 19080 } } })
    render(<SavedDefinitionEditor {...props} />)
    await waitFor(() => expect(props.server.setError).toHaveBeenCalledWith({ code: 'service_revision_conflict' }))
    expect(screen.queryByRole('button', { name: e('review') })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: e('reload') }))
    expect(await screen.findByLabelText('Local starting port')).toHaveValue(19080)
    expect(screen.queryByRole('region', { name: 'Selected checked port' })).not.toBeInTheDocument()
    expect(run.mock.calls.map(([name]) => name)).toEqual(['service.config', 'service.config'])
  })
  it('reloads the original fixed mapping without a new probe or candidate reapplication', async () => {
    const { props, e, run } = setup(); render(<SavedDefinitionEditor {...props} />)
    expect(await screen.findByLabelText('Local starting port')).toHaveValue(49152)
    await userEvent.click(screen.getByRole('button', { name: e('reload') }))
    await waitFor(() => expect(screen.getByLabelText('Local starting port')).toHaveValue(18080))
    expect(run.mock.calls.map(([name]) => name)).toEqual(['service.config', 'service.config'])
  })
  it('a source change after review is rejected without retrying against an automatically refreshed revision', async () => {
    const { props, e, run } = setup(); render(<SavedDefinitionEditor {...props} />)
    await screen.findByLabelText('Local starting port'); await userEvent.click(screen.getByRole('button', { name: e('review') }))
    run.mockResolvedValueOnce(undefined)
    await userEvent.click(screen.getByRole('button', { name: e('confirm') }))
    expect(run.mock.calls[1][1]).toMatchObject({ expectedRevision: source.revision })
    expect(props.onSaved).not.toHaveBeenCalled(); expect(run.mock.calls.map(([name]) => name)).toEqual(['service.config', 'service.save'])
  })
  it('losing contact erases candidate review and requires explicit original-source reload', async () => {
    const { props, e, run } = setup(); const view = render(<SavedDefinitionEditor {...props} />)
    await screen.findByLabelText('Local starting port'); await userEvent.click(screen.getByRole('button', { name: e('review') }))
    view.rerender(<SavedDefinitionEditor {...props} server={{ ...props.server, stale: true }} />)
    view.rerender(<SavedDefinitionEditor {...props} />)
    expect(screen.queryByRole('button', { name: e('confirm') })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: e('review') })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: e('reload') }))
    expect(await screen.findByLabelText('Local starting port')).toHaveValue(18080); expect(run).toHaveBeenCalledTimes(2)
  })
  it('does not seed a late candidate source after editor close', async () => {
    const { props, run } = setup(); let resolve!: (value: any) => void
    run.mockReturnValueOnce(new Promise(done => { resolve = done })); render(<SavedDefinitionEditor {...props} />)
    await waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    await act(async () => resolve({ ok: true, result: source }))
    expect(screen.queryByLabelText('Local starting port')).not.toBeInTheDocument(); expect(props.onSaved).not.toHaveBeenCalled()
  })
})
