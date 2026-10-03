import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, Peer, State } from '../api'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { AutosaveDialog } from './Dialogs'

function setup(locale: Locale, peerDirectory: string, draft?: string) {
  const peer: Peer = { id: 'fixture-peer', name: 'Fixture device', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct', autosave: { enabled: false, paused: false, directory: peerDirectory } }
  const state: State = { csrfToken: 'fixture', self: { name: 'This device', status: 'online', receiveDirectory: '/receiving/default' }, peers: [peer], messages: [], transfers: [], services: [], shares: [], settings: { receiveDirectory: '/receiving/default' } }
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true })
  const server: Server = { state, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn().mockResolvedValue(state), run, updatedAt: null, handleError: vi.fn() }
  const t = translator(locale)
  function Harness() {
    const [directory, setDirectory] = useState<string | undefined>(draft)
    return <AutosaveDialog server={server} peer={peer} locale={locale} t={t} onClose={vi.fn()} directoryDraft={directory} onDirectoryDraft={setDirectory} />
  }
  render(<Harness />)
  return { run, t, input: screen.getByRole('textbox', { name: t('receiveDirectory') }) }
}
describe('automatic receiving directory fallback', () => {
  it.each(['en', 'ja'] as const)('uses a configured directory for an empty peer value (%s)', async locale => {
    const { run, t, input } = setup(locale, '')
    expect(input).toHaveValue('/receiving/default')
    await userEvent.click(screen.getByRole('button', { name: t('enableAutosave') }))
    expect(run).toHaveBeenCalledExactlyOnceWith('peer.autosave', { peerId: 'fixture-peer', enabled: true, directory: '/receiving/default' }, 'autosave:fixture-peer')
  })
  it.each(['en', 'ja'] as const)('preserves an explicit per-peer directory (%s)', async locale => {
    const { run, t, input } = setup(locale, '/receiving/peer-specific')
    expect(input).toHaveValue('/receiving/peer-specific')
    await userEvent.click(screen.getByRole('button', { name: t('enableAutosave') }))
    expect(run).toHaveBeenCalledExactlyOnceWith('peer.autosave', { peerId: 'fixture-peer', enabled: true, directory: '/receiving/peer-specific' }, 'autosave:fixture-peer')
  })
  it.each(['en', 'ja'] as const)('keeps a deliberately cleared draft empty and blocks submission (%s)', async locale => {
    const { run, t, input } = setup(locale, '/receiving/peer-specific', '')
    expect(input).toHaveValue('')
    await userEvent.click(screen.getByRole('button', { name: t('enableAutosave') }))
    expect(run).not.toHaveBeenCalled()
    fireEvent.change(input, { target: { value: '/receiving/reviewed' } })
    fireEvent.change(input, { target: { value: '' } })
    expect(input).toHaveValue('')
    expect(run).not.toHaveBeenCalled()
  })
})
