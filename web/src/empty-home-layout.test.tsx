import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { State } from './api'
import { definitionText } from './definitions-i18n'
import { translator } from './i18n'

const fileSystemModule = 'node:fs'
const { readFileSync } = await import(fileSystemModule) as { readFileSync(path: string, encoding: 'utf8'): string }
const styles = readFileSync('src/styles.css', 'utf8')
function rule(selector: string) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const matches = [...styles.matchAll(new RegExp(`${escaped}\\s*\\{([^}]+)\\}`, 'g'))]
  expect(matches, `One authoritative rule for ${selector}`).toHaveLength(1)
  return Object.fromEntries(matches[0][1].split(';').filter(value => value.includes(':')).map(value => value.split(':').map(part => part.trim())))
}

// These are source/DOM guards, not rendered-layout evidence. The browser test
// measures actual viewport bounds, hit targets and complete keyboard scrolling.
describe('empty-home scroll ownership', () => {
  it('keeps tall empty content in a bounded, top-reachable scroll frame', () => {
    expect(rule('.main-empty')).toMatchObject({ 'min-height': '0', flex: '1', 'justify-content': 'flex-start', 'overflow-y': 'auto', 'overscroll-behavior': 'contain' })
    expect(rule('.main-empty > *')).toMatchObject({ 'flex-shrink': '0' })
  })
  it('preserves spacious centering with auto end margins when content fits', () => {
    expect(rule('.main-empty > :first-child')).toMatchObject({ 'margin-top': 'auto' })
    expect(rule('.main-empty > :last-child')).toMatchObject({ 'margin-bottom': 'auto' })
    expect(rule('.main-empty')).toMatchObject({ padding: '40px', 'align-items': 'center' })
  })
  it('gives the short empty-home sidebar one complete scroll area', () => {
    const short = styles.match(/@media \(max-height: 500px\) \{([\s\S]*?)\n\}/)?.[1]
    expect(short).toContain('.workspace:not(.device-selected) .device-sidebar')
    expect(rule('.workspace:not(.device-selected) .device-sidebar')).toMatchObject({ 'overflow-y': 'auto', 'scroll-padding-block': '8px' })
    expect(rule('.workspace:not(.device-selected) .device-sidebar > *')).toMatchObject({ flex: '0 0 auto' })
    expect(rule('.workspace:not(.device-selected) .device-list')).toMatchObject({ 'overflow-y': 'visible' })
  })
  it.each(['en', 'ja'] as const)('retains the decorative illustration, setup action and saved-services keyboard flow (%s)', async locale => {
    localStorage.setItem('sobalink.locale', locale)
    const state: State = { csrfToken: 'fixture-only', self: { name: 'This device', status: 'online' }, peers: [], services: [], shares: [], messages: [], transfers: [], settings: { network: 'none' } }
    const commands: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      const command = JSON.parse(init?.body as string)
      commands.push(command.name)
      return new Response(JSON.stringify({ ok: true, result: { profile: { version: 1, services: [], groups: [] }, revision: 'a'.repeat(64), disabled: true } }))
    }))
    const { container } = render(<App />)
    const t = translator(locale)
    await screen.findByRole('heading', { name: t('selectDevice') })
    const home = container.querySelector('.main-empty') as HTMLElement
    expect(home.querySelector('.empty-network')).toHaveAttribute('aria-hidden', 'true')
    expect(home.querySelectorAll('.empty-network span')).toHaveLength(2)
    expect(within(home).getByRole('button', { name: t('addDevice') })).toBeEnabled()
    const saved = screen.getByRole('button', { name: definitionText(locale, 'title') })
    saved.focus()
    await userEvent.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog', { name: definitionText(locale, 'title') })
    await within(dialog).findByText(definitionText(locale, 'empty'))
    fireEvent(dialog, new Event('cancel', { bubbles: false, cancelable: true }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(saved).toHaveFocus()
    await userEvent.click(saved)
    await screen.findByRole('dialog', { name: definitionText(locale, 'title') })
    expect(commands.every(name => name === 'profile.export')).toBe(true)
  })
})
