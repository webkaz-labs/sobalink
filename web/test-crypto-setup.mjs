import { webcrypto } from 'node:crypto'
import { beforeEach, vi } from 'vitest'

// jsdom lacks SubtleCrypto; exercise the browser's real SHA-256 contract.
// Keep this Node-only setup outside the browser application's TypeScript scope.
beforeEach(() => { vi.stubGlobal('crypto', webcrypto) })
