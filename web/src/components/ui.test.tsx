import { useState } from 'react'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { translator } from '../i18n'
import { Button, Modal } from './ui'

// jsdom has no layout. Mark only the controls rendered by each scenario; the
// browser suite separately verifies native layout, scrolling and hit testing.
function markRendered(...controls: HTMLElement[]) {
  for (const control of controls) vi.spyOn(control, 'getClientRects').mockReturnValue([new DOMRect(0, 0, 44, 44)] as unknown as DOMRectList)
}

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

  it.each(['en', 'ja'] as const)('wraps Tab only at the current modal boundaries (%s)', async locale => {
    const submit = vi.fn(); const close = vi.fn(); const t = translator(locale)
    render(<><Button>Background</Button><Modal title={t('settings')} t={t} onClose={close}><form onSubmit={event => { event.preventDefault(); submit() }}><label>{t('deviceName')}<input /></label><Button type="button">{t('cancel')}</Button><Button type="submit">{t('save')}</Button></form></Modal></>)
    const dialog = screen.getByRole('dialog')
    const first = within(dialog).getByRole('button', { name: t('close') })
    const input = within(dialog).getByRole('textbox')
    const cancel = within(dialog).getByRole('button', { name: t('cancel') })
    const last = within(dialog).getByRole('button', { name: t('save') })
    markRendered(first, input, cancel, last)

    first.focus()
    await userEvent.tab({ shift: true })
    expect(last).toHaveFocus()
    await userEvent.tab()
    expect(first).toHaveFocus()
    await userEvent.tab()
    expect(input).toHaveFocus()
    expect(fireEvent.keyDown(input, { key: 'Tab' })).toBe(true)
    await userEvent.tab()
    expect(cancel).toHaveFocus()
    await userEvent.tab()
    expect(last).toHaveFocus()
    expect(submit).not.toHaveBeenCalled()
    expect(close).not.toHaveBeenCalled()
  })

  it('skips hidden, inert, disabled and nonsequential controls at the boundary', () => {
    const t = translator('en')
    render(<Modal title="Settings" t={t} onClose={vi.fn()}><Button>Last action</Button><Button disabled>Disabled action</Button><fieldset disabled><Button>Disabled group action</Button></fieldset><Button hidden>Hidden action</Button><div hidden><Button>Hidden ancestor action</Button></div><div inert><Button>Inert action</Button></div><Button style={{ visibility: 'hidden' }}>Invisible action</Button><Button style={{ display: 'none' }}>Unrendered action</Button><Button tabIndex={-1}>Programmatic action</Button></Modal>)
    const dialog = screen.getByRole('dialog')
    const controls = [...dialog.querySelectorAll('button')]
    markRendered(...controls.filter(control => control.textContent !== 'Unrendered action'))
    const first = within(dialog).getByRole('button', { name: t('close') })
    const last = within(dialog).getByRole('button', { name: 'Last action' })
    first.focus()
    expect(fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })).toBe(false)
    expect(last).toHaveFocus()
    expect(fireEvent.keyDown(last, { key: 'Tab' })).toBe(false)
    expect(first).toHaveFocus()
  })

  it('resolves new controls, disabled actions and expanded details on every Tab', () => {
    const t = translator('en')
    const close = vi.fn()
    function content(enabled: boolean, expanded: boolean) {
      return <Modal title="Settings" t={t} onClose={close}><Button disabled={!enabled}>Save</Button><details open={expanded}><summary>Advanced</summary>{expanded && <Button>Advanced action</Button>}</details></Modal>
    }
    const { rerender } = render(content(false, false))
    const first = screen.getByRole('button', { name: t('close') })
    const save = screen.getByRole('button', { name: 'Save' })
    const summary = screen.getByText('Advanced')
    markRendered(first, save, summary)
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(summary).toHaveFocus()

    rerender(content(true, true))
    const advanced = screen.getByRole('button', { name: 'Advanced action' })
    markRendered(advanced)
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(advanced).toHaveFocus()
    fireEvent.keyDown(advanced, { key: 'Tab' })
    expect(first).toHaveFocus()

    rerender(<Modal title="Settings" t={t} onClose={close}><Button>Save</Button></Modal>)
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(save).toHaveFocus()
    rerender(<Modal title="Settings" t={t} onClose={close}><Button disabled>Save</Button></Modal>)
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(first).toHaveFocus()
  })

  it('excludes collapsed details content even when it has layout rectangles', () => {
    render(<Modal title="Settings" t={translator('en')} onClose={vi.fn()}><details><summary>Advanced</summary><Button>Advanced action</Button></details></Modal>)
    const dialog = screen.getByRole('dialog')
    const first = screen.getByRole('button', { name: 'Close' })
    const summary = screen.getByText('Advanced')
    const advanced = screen.getByText('Advanced action')
    const details = dialog.querySelector('details')!
    markRendered(first, summary, advanced)
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(summary).toHaveFocus()
    details.open = true
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(advanced).toHaveFocus()
    details.open = false
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(summary).toHaveFocus()
  })

  it('uses sequential tabindex order and the selected radio at a boundary', () => {
    render(<Modal title="Settings" t={translator('en')} onClose={vi.fn()}><input aria-label="First" tabIndex={1} /><input type="radio" name="choice" aria-label="Selected" defaultChecked /><input type="radio" name="choice" aria-label="Unselected" /></Modal>)
    const first = screen.getByRole('textbox', { name: 'First' })
    const last = screen.getByRole('radio', { name: 'Selected' })
    markRendered(...screen.getByRole('dialog').querySelectorAll<HTMLElement>('button, input'))
    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(first).toHaveFocus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(last).toHaveFocus()
  })

  it('wraps an unselected radio group without choosing a value', () => {
    render(<Modal title="Settings" t={translator('en')} onClose={vi.fn()}><input type="radio" name="choice" aria-label="First choice" /><input type="radio" name="choice" aria-label="Last choice" /></Modal>)
    const first = screen.getByRole('button', { name: 'Close' })
    const choices = screen.getAllByRole('radio')
    markRendered(first, ...choices)
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(choices[1]).toHaveFocus()
    fireEvent.keyDown(choices[1], { key: 'Tab' })
    expect(first).toHaveFocus()
    choices[0].focus()
    fireEvent.keyDown(choices[0], { key: 'Tab' })
    expect(first).toHaveFocus()
    for (const choice of choices) expect(choice).not.toBeChecked()
  })

  it.each(['altKey', 'ctrlKey', 'metaKey'])('leaves %s browser shortcuts alone', modifier => {
    render(<Modal title="Settings" t={translator('en')} onClose={vi.fn()}><Button>Last action</Button></Modal>)
    const first = screen.getByRole('button', { name: 'Close' })
    const last = screen.getByRole('button', { name: 'Last action' })
    markRendered(first, last)
    last.focus()
    expect(fireEvent.keyDown(last, { key: 'Tab', [modifier]: true })).toBe(true)
    expect(last).toHaveFocus()
    first.focus()
    expect(fireEvent.keyDown(first, { key: 'Tab', shiftKey: true, [modifier]: true })).toBe(true)
    expect(first).toHaveFocus()
  })

  it('respects a control that already handled Tab', () => {
    render(<Modal title="Settings" t={translator('en')} onClose={vi.fn()}><Button onKeyDown={event => event.preventDefault()}>Last action</Button></Modal>)
    const first = screen.getByRole('button', { name: 'Close' })
    const last = screen.getByRole('button', { name: 'Last action' })
    markRendered(first, last)
    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(last).toHaveFocus()
  })
})
