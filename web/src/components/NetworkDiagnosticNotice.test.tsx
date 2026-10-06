import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { DiagnosticGuidance, Locale } from '../api'
import { translator } from '../i18n'
import { networkGuidanceAction, networkGuidanceEnglish, networkGuidanceJapanese, readNetworkGuidance } from '../network-guidance'
import { NetworkDiagnosticNotice } from './NetworkDiagnosticNotice'

const guidance: DiagnosticGuidance = { code: 'direct_lan_address_unknown', category: 'unknown', action: 'refresh_state', summary: { en: 'The address could not be checked.', ja: 'アドレスを確認できませんでした。' }, nextSteps: { en: 'Refresh the local state.', ja: 'ローカル状態を更新してください。' } }
function props(locale: Locale = 'en') {
  return { self: { name: 'Example', status: 'error', error: 'fixture diagnostic detail', guidance }, locale, t: translator(locale), stale: false, reviewNetwork: vi.fn(), reviewCapacity: vi.fn(), refresh: vi.fn() }
}
describe('shared passive network guidance', () => {
  it('keeps labels aligned and rejects malformed guidance or unknown actions', () => {
    expect(Object.keys(networkGuidanceJapanese)).toEqual(Object.keys(networkGuidanceEnglish))
    expect(readNetworkGuidance(guidance)).toBe(guidance)
    for (const value of [null, {}, { ...guidance, action: 'execute' }, { ...guidance, nextSteps: { en: 'Only one language' } }, { ...guidance, summary: { en: '', ja: '説明' } }]) expect(readNetworkGuidance(value)).toBeUndefined()
  })
  it.each(['en', 'ja'] as const)('%s shows the known problem and only refreshes explicitly', locale => {
    const p = props(locale); render(<NetworkDiagnosticNotice {...p} />)
    expect(screen.getByRole('status')).toHaveTextContent(guidance.summary[locale])
    expect(screen.getByRole('status')).toHaveTextContent(guidance.nextSteps[locale])
    expect(p.refresh).not.toHaveBeenCalled()
    expect(p.reviewNetwork).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: networkGuidanceAction(locale, 'refresh_state') }))
    expect(p.refresh).toHaveBeenCalledTimes(1)
    expect(p.reviewNetwork).not.toHaveBeenCalled()
    expect(p.reviewCapacity).not.toHaveBeenCalled()
  })
  it.each(['review_network', 'review_capacity'] as const)('routes %s to its review surface without another action', action => {
    const p = props(); render(<NetworkDiagnosticNotice {...p} self={{ ...p.self, guidance: { ...guidance, action } }} />)
    fireEvent.click(screen.getByRole('button'))
    expect(action === 'review_network' ? p.reviewNetwork : p.reviewCapacity).toHaveBeenCalledTimes(1)
    expect(action === 'review_network' ? p.reviewCapacity : p.reviewNetwork).not.toHaveBeenCalled()
    expect(p.refresh).not.toHaveBeenCalled()
  })
  it('suppresses stale guidance, clears after recovery and never turns waiting into an automatic retry', () => {
    const p = props(); const view = render(<NetworkDiagnosticNotice {...p} stale />)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    view.rerender(<NetworkDiagnosticNotice {...p} self={{ ...p.self, guidance: { ...guidance, action: 'wait' } }} />)
    expect(screen.getByRole('status')).toBeVisible()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    expect(p.refresh).not.toHaveBeenCalled()
    view.rerender(<NetworkDiagnosticNotice {...p} self={{ name: 'Example', status: 'running' }} />)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
  it('retains a safe fallback for older state and never trusts an unsupported action', () => {
    const p = props(); render(<NetworkDiagnosticNotice {...p} self={{ ...p.self, guidance: { ...guidance, action: 'execute' } as unknown as DiagnosticGuidance }} />)
    expect(screen.getByRole('status')).toHaveTextContent(p.t('networkProblem'))
    fireEvent.click(screen.getByRole('button', { name: networkGuidanceAction('en', 'review_network') }))
    expect(p.reviewNetwork).toHaveBeenCalledTimes(1)
    expect(p.refresh).not.toHaveBeenCalled()
  })
})
