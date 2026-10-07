import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
export default {
    name: 'pinned-png-worker-assets',
    buildStart() {
      const pins = {
        'THIRD_PARTY_NOTICES.txt': '3e7975d38af01a4537dedb6bc1541c6af4e3b8d187af5404809a7351ee2ddcf1',
        'share.js': '68b09047a19c17ba74c0bf7e0a4341e77d46b36ae1cae4bdcca31d0c6757f3f2',
        'reader/index.js': '3c364d8477404a513d7c0629b1cf9e63ae11e01f9746146d3574f8b10a122320',
        'reader/zxing_reader.wasm': 'aecc1876de036c62c8419f67a5e1a16b1698a325bcd190aa84810d516e263931',
      }
      for (const [path, hash] of Object.entries(pins)) {
        const bytes = readFileSync(new URL(`../vendor/zxing-wasm-3.1.5/${path}`, import.meta.url))
        if (createHash('sha256').update(bytes).digest('hex') !== hash) this.error('Pinned QR reader asset mismatch')
      }
    },
    generateBundle(_, bundle) {
      this.emitFile({ type: 'asset', fileName: 'assets/png-reader-notices.txt', source: readFileSync(new URL('../vendor/zxing-wasm-3.1.5/THIRD_PARTY_NOTICES.txt', import.meta.url)) })
      const workers = Object.values(bundle).filter(asset => /^assets\/device-card-worker-[A-Za-z0-9_-]+\.js$/.test(asset.fileName))
      if (workers.length !== 1 || workers[0].type !== 'asset') this.error('Expected one owned PNG worker asset')
      const worker = workers[0]
      this.emitFile({ type: 'asset', fileName: 'png-worker-manifest.json', source: JSON.stringify({ version: 1, path: worker.fileName, sha256: createHash('sha256').update(worker.source).digest('hex') }) + '\n' })
    },
  }
