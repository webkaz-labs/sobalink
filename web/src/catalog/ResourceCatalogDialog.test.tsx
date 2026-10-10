import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { translator } from '../i18n'
import { deferred } from '../resource/fixtures.test-support'
import { ResourceCatalogController } from './controller'
import { ResourceCatalogDialog } from './ResourceCatalogDialog'
import { catalogEnglish as en, catalogJapanese as ja } from './i18n'
import { context, fixtureTransport, responseFor, target } from './fixtures.test-support'
function show(locale: 'en' | 'ja' = 'en') { const fixture = fixtureTransport(), controller = new ResourceCatalogController(fixture.transport, vi.fn()), navigate = vi.fn(), close = vi.fn(() => controller.close()); controller.updateContext(context); render(<ResourceCatalogDialog controller={controller} locale={locale} t={translator(locale)} onClose={close} onNavigate={navigate} />); return { ...fixture, controller, navigate, close } }
describe('explicit unified resource catalog dialog', () => {
  it.each(['en', 'ja'] as const)('keeps the %s sources unresolved until explicit selection and refresh', async locale => {
    const text = locale === 'ja' ? ja : en, h = show(locale); expect(h.calls).toHaveLength(0); expect(screen.getByText(text.unresolved)).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: text.refresh })); expect(h.calls).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: text.loadLocal })); await screen.findByRole('option', { name: target.resourceId }); await userEvent.selectOptions(screen.getByRole('combobox', { name: text.chooseTarget }), target.resourceId)
    await userEvent.click(screen.getByRole('button', { name: text.refresh })); await screen.findByText(text.complete)
    expect(h.calls.map(call => call.name)).toEqual(['resource.list', 'resource.catalog.snapshot']); expect(screen.getByRole('region', { name: text.local_service })).toHaveTextContent('Synthetic service'); expect(screen.getByRole('region', { name: text.transfer_activity })).toHaveTextContent('batch-synthetic')
    await userEvent.click(screen.getByRole('button', { name: text.inspect_settings })); expect(h.navigate).toHaveBeenCalledTimes(1); expect(h.calls).toHaveLength(2)
  })
  it('requires explicit deselection for a narrower service and transfer snapshot', async () => { const h = show(); await userEvent.click(screen.getByRole('checkbox', { name: en.local_settings })); await userEvent.click(screen.getByRole('button', { name: en.refresh })); await screen.findByText(en.complete); expect(h.calls[0].request?.sources.map(source => source.kind)).toEqual(['local_service', 'transfer_activity']) })
  it('clears action controls immediately on selector edits', async () => { const h = show(); await userEvent.click(screen.getByRole('checkbox', { name: en.local_settings })); await userEvent.click(screen.getByRole('button', { name: en.refresh })); await screen.findByRole('button', { name: en.open_transfer }); await userEvent.click(screen.getByRole('checkbox', { name: en.transfer_activity })); expect(screen.queryByRole('button', { name: en.open_transfer })).not.toBeInTheDocument(); expect(h.controller.getSnapshot().candidate).toBeNull() })
  it('Stop waiting suppresses late rows and does not rerun or claim cancellation', async () => { const h = show(), pending = deferred(); h.transport.snapshot = async () => pending.promise; await userEvent.click(screen.getByRole('checkbox', { name: en.local_settings })); await userEvent.click(screen.getByRole('button', { name: en.refresh })); await userEvent.click(screen.getByRole('button', { name: en.stop })); await act(async () => { pending.resolve(responseFor({ schemaVersion: 1, sources: [{ kind: 'local_service' }, { kind: 'transfer_activity' }] })) }); expect(screen.queryByText('Synthetic service')).not.toBeInTheDocument(); expect(screen.getByText(en.stoppedHint)).toBeVisible() })
  it('shows a generic unavailable response without claiming an empty catalog', async () => { const h = show(); h.transport.snapshot = async () => { throw new Error('Private synthetic provider message') }; await userEvent.click(screen.getByRole('checkbox', { name: en.local_settings })); await userEvent.click(screen.getByRole('button', { name: en.refresh })); await screen.findByRole('alert'); expect(screen.getByRole('alert')).toHaveTextContent(en.unavailable); expect(screen.queryByText(en.empty)).not.toBeInTheDocument(); expect(screen.queryByText('Private synthetic provider message')).not.toBeInTheDocument() })
  it('preserves keyboard close with no automatic read', async () => { const h = show(); const modal = screen.getByRole('dialog', { name: en.title }); within(modal).getAllByRole('button', { name: 'Close' })[0].focus(); await userEvent.keyboard('{Enter}'); await waitFor(() => expect(h.close).toHaveBeenCalledOnce()); expect(h.calls).toHaveLength(0) })
})
