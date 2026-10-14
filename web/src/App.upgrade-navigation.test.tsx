import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { State } from './api'
import { translator } from './i18n'
import { nearbyText } from './nearby-i18n'
import { upgradeText } from './upgrade-i18n'

const peerId = 'a'.repeat(64)
const localEndpoint = '127.0.0.1:48444', peerEndpoint = '127.0.0.1:48445'

describe('full App saved-pair upgrade navigation', () => {
  it.each([
    { locale: 'en', listenerReady: false }, { locale: 'ja', listenerReady: false },
    { locale: 'en', listenerReady: true }, { locale: 'ja', listenerReady: true },
  ] as const)('opens the explicit advanced path in $locale with listenerReady=$listenerReady', async ({ locale, listenerReady }) => {
    localStorage.setItem('sobalink.locale', locale)
    const t = translator(locale), u = (key: Parameters<typeof upgradeText>[1]) => upgradeText(locale, key)
    const state: State = {
      csrfToken: 'synthetic-upgrade-only', resourceCatalogProcessId: 'b'.repeat(64),
      self: { name: 'Synthetic local device', status: listenerReady ? 'online' : 'offline' },
      settings: { network: 'direct-lan' }, peers: [], services: [], shares: [], transfers: [], messages: [],
      directLAN: { configured: true, listenerReady, publicKey: 'c'.repeat(64), endpoint: localEndpoint,
        prefixes: ['127.0.0.1/32'], peers: [{ key: peerId, name: 'Synthetic saved peer', endpoint: peerEndpoint }] },
    }
    const commands: { name: string; payload: Record<string, unknown> }[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      const request = JSON.parse(init!.body as string); commands.push({ name: request.name, payload: request.payload })
      const result = request.name === 'direct-lan.upgrade.review'
        ? { ...request.payload, revision: 'synthetic-review', localEndpoint, peerEndpoint,
            scope: { family: 'ipv4', prefixes: ['127.0.0.1/32'] }, restartRequired: true, resumePreparation: false }
        : { state: 'network-started', peerId, deadline: new Date(Date.now() + 60000).toISOString() }
      return new Response(JSON.stringify({ ok: true, result }))
    }))
    const { container } = render(<App />)
    await screen.findByRole('main')
    const sidebar = container.querySelector('.sidebar-title') as HTMLElement
    await userEvent.click(within(sidebar).getByRole('button', { name: t('addDevice') }))
    const dialog = screen.getByRole('dialog', { name: t('chooseNetwork') })
    // The same options screen appears for a stopped saved profile and a fresh
    // successor with its ordinary listener ready. Entering it grants nothing.
    expect(dialog.querySelector('input[type="radio"][value="direct-lan"]')).not.toBeInTheDocument()
    expect(within(dialog).queryByRole('region', { name: u('title') })).not.toBeInTheDocument()
    expect(commands).toHaveLength(0)
    await userEvent.click(within(dialog).getByRole('button', { name: nearbyText(locale, 'advanced') }))
    await userEvent.click(dialog.querySelector('input[type="radio"][value="direct-lan"]') as HTMLInputElement)
    const panel = within(dialog).getByRole('region', { name: u('title') })
    expect(within(panel).getAllByRole('combobox')).toHaveLength(1)
    expect(commands).toHaveLength(0)
    if (listenerReady) {
      await userEvent.click(within(panel).getByRole('button', { name: u('refresh') }))
      await waitFor(() => expect(within(panel).getByRole('status', { name: u('status') })).toHaveTextContent(u('network-started')))
      expect(commands).toEqual([{ name: 'direct-lan.upgrade.status', payload: {} }])
    } else {
      await userEvent.selectOptions(within(panel).getByRole('combobox'), peerId)
      await userEvent.click(within(panel).getByRole('button', { name: u('review') }))
      const review = await within(panel).findByRole('region', { name: u('review') })
      expect(review).toHaveTextContent(peerId)
      expect(review).toHaveTextContent(localEndpoint)
      expect(review).toHaveTextContent(peerEndpoint)
      expect(within(review).getByRole('button', { name: u('apply') })).toBeEnabled()
      expect(commands).toEqual([{ name: 'direct-lan.upgrade.review', payload: { peerId, deadline: expect.any(String) } }])
    }
    fireEvent(dialog, new Event('cancel', { cancelable: true }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await userEvent.click(within(sidebar).getByRole('button', { name: t('addDevice') }))
    const reopened = screen.getByRole('dialog', { name: t('chooseNetwork') })
    expect(reopened.querySelector('input[type="radio"][value="direct-lan"]')).not.toBeInTheDocument()
    await userEvent.click(within(reopened).getByRole('button', { name: nearbyText(locale, 'advanced') }))
    expect(within(reopened).getByRole('region', { name: u('title') })).toBeInTheDocument()
    expect(within(reopened).queryByRole('region', { name: u('review') })).not.toBeInTheDocument()
    expect(commands).toHaveLength(1)
  })
})
