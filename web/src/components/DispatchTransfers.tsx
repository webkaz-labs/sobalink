import * as api from '../api'
import type { DispatchCommitted } from '../dispatch'
import type { Server } from '../useServer'
import { transferTranslator } from '../transfer-i18n'
import { PeerTransfers } from './PeerTransfers'

export function DispatchTransfers({ receipt, state, server, locale, resetToken }: { receipt: DispatchCommitted; state: api.State; server: Server; locale: api.Locale; resetToken: string }) {
  const text = transferTranslator(locale)
  const current = receipt.processId === state.resourceCatalogProcessId
  return <section className="dispatch-summary" aria-label={text('deliverySummary')}>
    <h3>{text('deliverySummary')}</h3>
    <p className="small muted">{text(current ? 'deliveryPartialHint' : 'dispatchExpired')}</p>
    {receipt.children.map(child => {
      const peer = state.peers.find(value => value.id === child.peerId)
      const transfer = current ? state.transfers.find(value => value.id === child.transferId && value.peerId === child.peerId && value.direction === 'outgoing') : undefined
      return <div key={child.transferId} className="dispatch-recipient-result">
        {peer && transfer ? <PeerTransfers peer={peer} state={{ ...state, transfers: [transfer] }} server={server} locale={locale} resetToken={resetToken} /> : <p className="small"><strong>{peer?.name || child.peerId}</strong>: {text('deliveryMissing')}</p>}
      </div>
    })}
  </section>
}
