import { useState } from 'react'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { translator } from '../i18n'
import { Button, Modal } from './ui'

describe('shared dialog controls', () => {
  it.each(['en', 'ja'] as const)('closes without submitting and restores the opener focus (%s)', async locale => {
    const submit = vi.fn(); const t = translator(locale)
    function Harness() {
      const [open, setOpen] = useState(false)
      return <><Button onClick={() => setOpen(true)}>{t('settings')}</Button>{open && <Modal title={t('settings')} t={t} onClose={() => setOpen(false)}><form onSubmit={event => { event.preventDefault(); submit() }}><label>{t('deviceName')}<input /></label><div className="modal-actions"><Button type="button" onClick={() => setOpen(false)}>{t('cancel')}</Button><Button type="submit">{t('save')}</Button></div></form></Modal>}</>
    }
    render(<Harness />)
    const opener = screen.getByRole('button', { name: t('settings') })
    await userEvent.click(opener)
    const dialog = screen.getByRole('dialog', { name: t('settings') })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(within(dialog).getByRole('heading', { name: t('settings') })).toBeVisible()
    await userEvent.click(within(dialog).getByRole('button', { name: t('close') }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
    expect(submit).not.toHaveBeenCalled()
    await userEvent.click(opener)
    fireEvent(screen.getByRole('dialog'), new Event('cancel', { cancelable: true }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
    expect(submit).not.toHaveBeenCalled()
  })
})
