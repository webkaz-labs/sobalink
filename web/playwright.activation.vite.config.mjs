import { defineConfig } from 'vite'
import { resolve } from 'node:path'
export default defineConfig({
  root: resolve('browser/activation-app'),
  build: { outDir: resolve('.activation-browser-assets'), emptyOutDir: true, sourcemap: false, target: 'es2022', assetsInlineLimit: 0 },
})
