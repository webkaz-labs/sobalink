// Test-only oracle from the already locked Node runtime. Keep the browser module
// independent of Node, without introducing a dependency just for this signature.
declare module 'node:zlib' {
  export function crc32(data: Uint8Array): number
}
declare module 'node:fs' {
  export function readFileSync(path: URL | string): Uint8Array
}
