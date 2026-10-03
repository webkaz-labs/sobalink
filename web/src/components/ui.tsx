import { useEffect, useId, useRef, type ButtonHTMLAttributes, type ReactNode } from 'react'
import type { Translate } from '../i18n'

const paths = {
  copy: 'M9 9h12v12H9zM15 9V3H3v12h6',
  arrow: 'M5 12h14m-6-6 6 6-6 6', back: 'M19 12H5m6-6-6 6 6 6', check: 'm5 12 4 4L19 6', close: 'm6 6 12 12M6 18 18 6',
  plus: 'M12 5v14M5 12h14', search: 'm21 21-5-5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0',
  monitor: 'M3 4h18v13H3zM8 21h8M12 17v4', message: 'M21 11a8 8 0 0 1-8 8H7l-5 3V11a9 9 0 0 1 19 0Z',
  folder: 'M3 5h6l2 3h10v12H3z', file: 'M5 3h9l5 5v13H5zM14 3v6h5M8 14h8M8 17h6',
  clip: 'm8 13 6-6a3 3 0 0 1 4 4l-8 8a5 5 0 0 1-7-7l8-8a7 7 0 0 1 10 10l-8 8',
  send: 'm3 3 19 9-19 9 4-9-4-9Zm4 9h15', shield: 'm12 2 8 4v6c0 5-8 10-8 10S4 17 4 12V6l8-4Zm-4 9 3 3 5-5',
  link: 'm10 13 4-4M8 16l-2 2a4 4 0 0 1-6-6l5-5a4 4 0 0 1 6 0m2 2 2-2a4 4 0 0 1 6 6l-5 5a4 4 0 0 1-6 0',
  globe: 'M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0ZM3 12h18M12 3c5 5 5 13 0 18-5-5-5-13 0-18Z',
  wifi: 'M2 8a16 16 0 0 1 20 0M5 12a11 11 0 0 1 14 0M8 16a5 5 0 0 1 8 0M12 20h.01',
  info: 'M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0ZM12 11v6M12 7h.01',
  settings: 'm10 2-1 3-3 1-3-1-2 4 2 2v3l-2 2 2 4 3-1 3 1 1 3h4l1-3 3-1 3 1 2-4-2-2v-3l2-2-2-4-3 1-3-1-1-3h-4ZM16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0',
  refresh: 'M20 7V2m0 5h-5M4 17v5m0-5h5M20 7A9 9 0 0 0 4 5M4 17a9 9 0 0 0 16 2',
  download: 'M12 3v12m-5-5 5 5 5-5M3 16v5h18v-5', upload: 'M12 16V3m-5 5 5-5 5 5M3 16v5h18v-5',
  stop: 'M6 6h12v12H6z', chevron: 'm8 4 8 8-8 8', lock: 'M6 10h12v11H6zM8 10V6a4 4 0 0 1 8 0v4',
  alert: 'm12 3 10 18H2L12 3ZM12 9v5M12 17h.01', moon: 'M20 14A9 9 0 0 1 10 3a9 9 0 1 0 10 11Z',
  sun: 'M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0ZM12 1v2m0 18v2M1 12h2m18 0h2M4 4l2 2m12 12 2 2M4 20l2-2M18 6l2-2',
} as const
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
    return () => { element.removeEventListener('cancel', cancel); element.close(); focusBefore?.focus() }
  }, [])
  return <dialog ref={dialog} aria-labelledby={id} className={`modal ${wide ? 'modal-wide' : ''}`} onClick={event => { if (event.target === dialog.current) { const rect = dialog.current.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) onClose() } }}>
    <div className="modal-heading"><h2 id={id}>{title}</h2><IconButton icon="close" label={t('close')} onClick={onClose} /></div><div className="modal-body">{children}</div>
  </dialog>
}
export function Logo({ small = false }: { small?: boolean }) {
  return <div className={`brand ${small ? 'brand-small' : ''}`}><span className="brand-mark" aria-hidden="true"><i /><i /><i /></span><span>sobalink<span className="brand-dot">.</span></span></div>
}
