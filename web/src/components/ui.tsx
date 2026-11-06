import { useEffect, useId, useRef, type ButtonHTMLAttributes, type KeyboardEvent, type ReactNode } from 'react'
import type { Translate } from '../i18n'
import paths from '../../../internal/uibrand/icons.json'

export function useAlive() {
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  return alive
}
export type IconName = keyof typeof paths
export function Icon({ name, size = 18, className = '' }: { name: IconName; size?: number; className?: string }) {
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={className}><path d={paths[name]} /></svg>
}
export function Button({ variant = 'secondary', className = '', busy, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'secondary' | 'ghost' | 'danger'; busy?: boolean }) {
  return <button {...props} disabled={props.disabled || busy} className={`button button-${variant} ${className}`} aria-busy={busy || undefined}>{busy && <span className="spinner" aria-hidden="true" />}{children}</button>
}
export function IconButton({ icon, label, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { icon: IconName; label: string }) {
  return <Button {...props} variant="ghost" className={`icon-button ${props.className || ''}`} aria-label={label} title={label}><Icon name={icon} /></Button>
}
export function Badge({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'green' | 'amber' | 'purple' | 'red' }) {
  return <span className={`badge badge-${tone}`}>{children}</span>
}
export function ErrorBanner({ message, detail, onDismiss, t }: { message: string; detail?: string; onDismiss?: () => void; t: Translate }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => { ref.current?.focus() }, [message])
  return <div className="error-banner" role="alert" tabIndex={-1} ref={ref}><Icon name="alert" /><div className="error-copy"><p>{message}</p>{detail && <details><summary>{t('technicalDetails')}</summary><p>{detail}</p></details>}</div>{onDismiss && <IconButton icon="close" label={t('close')} onClick={onDismiss} />}</div>
}
function keepModalFocus(event: KeyboardEvent<HTMLDialogElement>) {
  if (event.key !== 'Tab' || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.nativeEvent.isComposing) return
  const element = event.currentTarget
  if (!(event.target instanceof Element) || event.target.closest('dialog') !== element) return
  // Native dialogs can send focus to browser chrome at either end. Resolve the
  // current controls on every keypress so validation and expanded details can
  // change the boundaries without changing native navigation inside the form.
  const controls = [...element.querySelectorAll<HTMLElement>('button, input, select, textarea, a[href], summary, [tabindex]')].filter(control => {
    for (let parent = control.parentElement; parent && parent !== element; parent = parent.parentElement) {
      if (parent instanceof HTMLDetailsElement && !parent.open && !parent.querySelector(':scope > summary')?.contains(control)) return false
    }
    const visibility = getComputedStyle(control).visibility
    return control.tabIndex >= 0 && !control.matches(':disabled') && !control.closest('[hidden], [inert]') &&
      control.getClientRects().length > 0 && visibility !== 'hidden' && visibility !== 'collapse'
  }).filter((control, _, candidates) => {
    if (!(control instanceof HTMLInputElement) || control.type !== 'radio' || !control.name) return true
    const group = candidates.filter(candidate => candidate instanceof HTMLInputElement && candidate.type === 'radio' && candidate.name === control.name && candidate.form === control.form) as HTMLInputElement[]
    return control === (group.find(radio => radio === document.activeElement) || group.find(radio => radio.checked) || (event.shiftKey ? group.at(-1) : group[0]))
  }).sort((a, b) => (a.tabIndex || Number.MAX_SAFE_INTEGER) - (b.tabIndex || Number.MAX_SAFE_INTEGER))
  const first = controls[0]
  const last = controls.at(-1)
  if (!first || !last) return
  if (document.activeElement === (event.shiftKey ? first : last) || document.activeElement === element) {
    event.preventDefault()
    const next = event.shiftKey ? last : first
    next.focus()
  }
}
export function Modal({ title, children, onClose, t, wide = false }: { title: string; children: ReactNode; onClose: () => void; t: Translate; wide?: boolean }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const id = useId()
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose
  useEffect(() => {
    const element = dialog.current!
    const focusBefore = document.activeElement as HTMLElement | null
    if (!element.open) element.showModal()
    const cancel = (event: Event) => { event.preventDefault(); onCloseRef.current() }
    element.addEventListener('cancel', cancel)
    return () => { element.removeEventListener('cancel', cancel); element.close(); if (focusBefore?.isConnected) focusBefore.focus() }
  }, [])
  return <dialog ref={dialog} aria-labelledby={id} aria-modal="true" className={`modal ${wide ? 'modal-wide' : ''}`} onKeyDown={keepModalFocus} onClick={event => { if (event.target === dialog.current) { const rect = dialog.current.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) onClose() } }}>
    <div className="modal-heading"><h2 id={id}>{title}</h2><IconButton icon="close" label={t('close')} onClick={onClose} /></div><div className="modal-body">{children}</div>
  </dialog>
}
export function Logo({ small = false }: { small?: boolean }) {
  return <div className={`brand ${small ? 'brand-small' : ''}`}><span className="brand-mark" aria-hidden="true" /><span>sobalink<span className="brand-dot">.</span></span></div>
}
