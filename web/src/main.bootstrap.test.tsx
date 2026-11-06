import { StrictMode } from 'react'
import { expect, it, vi } from 'vitest'
import markURL from '../../internal/uibrand/mark.svg'

const mocks = vi.hoisted(() => ({ bootstrap: vi.fn(), createRoot: vi.fn(), render: vi.fn() }))
vi.mock('./ui-bootstrap', () => ({ bootstrapLocalAccess: mocks.bootstrap }))
vi.mock('react-dom/client', () => ({ default: { createRoot: mocks.createRoot } }))
vi.mock('./App', () => ({ App: () => null }))
it.each([false, true])('waits for one bootstrap before mounting even the StrictMode tree (favicon: %s)', async (hasFavicon) => {
  vi.resetModules()
  vi.clearAllMocks()
  document.head.innerHTML = hasFavicon ? '<link rel="icon" type="image/svg+xml">' : ''
  document.body.innerHTML = '<div id="root"></div>'
  let resolve!: () => void
  mocks.bootstrap.mockReturnValue(new Promise<void>(done => { resolve = done }))
  mocks.createRoot.mockReturnValue({ render: mocks.render })
  await import('./main')
  if (hasFavicon) expect(document.querySelector('link[rel="icon"]')?.getAttribute('href')).toBe(markURL)
  expect(mocks.bootstrap).toHaveBeenCalledTimes(1)
  expect(mocks.createRoot).not.toHaveBeenCalled()
  expect(mocks.render).not.toHaveBeenCalled()
  resolve(); await Promise.resolve()
  expect(mocks.createRoot).toHaveBeenCalledTimes(1)
  expect(mocks.render).toHaveBeenCalledTimes(1)
  expect(mocks.render.mock.calls[0][0].type).toBe(StrictMode)
})
