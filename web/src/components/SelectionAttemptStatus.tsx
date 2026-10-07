import type { Locale, Service, ServiceSelection, State } from '../api'
import type { Translate } from '../i18n'
import { selectionAttemptText } from '../selection-attempt-i18n'
import { Button } from './ui'

export interface SelectionAttempt {
  action: 'start' | 'stop'
  reviewed: ServiceSelection
  snapshot: Pick<State, 'services' | 'shares'> | null
  refreshing: boolean
}

// Missing or ambiguous observations are unknown, never an inferred rollback or
// an execution result. Match IDs in the reviewed direction even if a saved
// group has since been edited or removed.
function memberStatus(states: { id: string; status: Service['status'] }[], id: string) {
  const matches = states.filter(state => state && state.id === id)
  const status = matches.length === 1 ? matches[0].status : undefined
  return status && ['active', 'reconnecting', 'stopped', 'failed', 'saved', 'expired'].includes(status) ? status : undefined
}

export function SelectionAttemptStatus({ attempt, locale, t, onRefresh, onDismiss }: { attempt: SelectionAttempt; locale: Locale; t: Translate; onRefresh: () => void; onDismiss: () => void }) {
  const text = (key: Parameters<typeof selectionAttemptText>[1]) => selectionAttemptText(locale, key)
  return <section className="policy-review" aria-label={text(attempt.action)}>
    <h3>{text(attempt.action)}</h3>
    <p>{text('explanation')}</p>
    {attempt.refreshing ? <p role="status">{text('refreshing')}</p> : !attempt.snapshot && <p role="status">{text('unavailable')}</p>}
    {attempt.reviewed.services.map(service => {
      const before = memberStatus(attempt.reviewed.states, service.id)
      const observations = attempt.snapshot?.[service.direction === 'share' ? 'shares' : 'services']
      const current = Array.isArray(observations) ? memberStatus(observations, service.id) : undefined
      return <article className="definition-scope" key={service.id} aria-label={service.name}>
        <strong>{service.name}</strong><p className="small code-value">{service.id}</p>
        <dl><div><dt>{text('atReview')}</dt><dd>{before ? t(before) : text('unknown')}</dd></div>
          <div><dt>{text('afterRefresh')}</dt><dd>{current ? t(current) : text('unknown')}</dd></div></dl>
      </article>
    })}
    <div className="modal-actions"><Button type="button" onClick={onDismiss}>{text('dismiss')}</Button><Button type="button" disabled={attempt.refreshing} onClick={onRefresh}>{text('refresh')}</Button></div>
  </section>
}
