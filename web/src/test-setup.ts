import '@testing-library/jest-dom/vitest'
import { afterEach, vi } from 'vitest'
import { cleanup } from '@testing-library/react'

Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value: function () { this.setAttribute('open', '') } })
Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: function () { this.removeAttribute('open') } })
afterEach(() => { cleanup(); localStorage.clear(); history.replaceState(null, ''); vi.unstubAllGlobals() })
