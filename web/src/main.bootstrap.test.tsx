import { StrictMode } from 'react'
import { expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ bootstrap: vi.fn(), createRoot: vi.fn(), render: vi.fn() }))
vi.mock('./ui-bootstrap', () => ({ bootstrapLocalAccess: mocks.bootstrap }))
vi.mock('react-dom/client', () => ({ default: { createRoot: mocks.createRoot } }))
vi.mock('./App', () => ({ App: () => null }))
it('waits for one bootstrap before mounting even the StrictMode tree', async () => {
  document.body.innerHTML = '<div id="root"></div>'
  let resolve!: () => void
  mocks.bootstrap.mockReturnValue(new Promise<void>(done => { resolve = done }))
  mocks.createRoot.mockReturnValue({ render: mocks.render })
  await import('./main')
  expect(mocks.bootstrap).toHaveBeenCalledTimes(1)
  expect(mocks.createRoot).not.toHaveBeenCalled()
  expect(mocks.render).not.toHaveBeenCalled()
  resolve(); await Promise.resolve()
  expect(mocks.createRoot).toHaveBeenCalledTimes(1)
  expect(mocks.render).toHaveBeenCalledTimes(1)
  expect(mocks.render.mock.calls[0][0].type).toBe(StrictMode)
})
