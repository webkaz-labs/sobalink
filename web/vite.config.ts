import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { sourcemap: false, target: 'es2022', assetsInlineLimit: 0 },
  server: { host: '127.0.0.1' },
  test: { include: ['src/**/*.test.{ts,tsx}'], environment: 'jsdom', setupFiles: ['./src/test-setup.ts', './test-crypto-setup.mjs'], restoreMocks: true },
})
