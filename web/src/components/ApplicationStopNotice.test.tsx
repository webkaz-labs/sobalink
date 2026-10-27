import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ApplicationStopNotice } from './ApplicationStopNotice'
import { lanTranslator } from '../lan-i18n'

for (const locale of ['en', 'ja'] as const) {
  it.each(['requested', 'disconnected', 'unconfirmed'] as const)(`${locale}: explains %s without claiming completed shutdown`, phase => {
    render(<ApplicationStopNotice phase={phase} locale={locale} />)
    const lt = lanTranslator(locale)
    expect(screen.getByRole('status')).toHaveTextContent(lt(phase === 'requested' ? 'stopping' : phase === 'disconnected' ? 'stopDisconnected' : 'stopUnconfirmed'))
    expect(screen.getByRole('status')).toHaveTextContent(lt('stopRecovery'))
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
}
