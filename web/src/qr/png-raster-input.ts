/** Retain only validated pixel-bearing chunks. Strip compressed text/profiles,
 * EXIF orientation, and every other ancillary chunk before the native raster API.
 * The QR reader tries rotations itself, at the original IHDR resolution.
 * Call only after preflightStaticPng has accepted the same immutable bytes.
 */
export function pngRasterInput(bytes: Uint8Array): Uint8Array | undefined {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  let offset = 8, total = 8, paletteEntries = 0, dataSeen = false, transparencySeen = false
  const bitDepth = bytes[24], colorType = bytes[25]
  while (offset < bytes.length) {
    const length = view.getUint32(offset), end = offset + 12 + length
    const type = String.fromCharCode(...bytes.subarray(offset + 4, offset + 8))
    if (type === 'PLTE') {
      if (transparencySeen) return undefined
      paletteEntries = length / 3
    }
    if (type === 'IDAT') dataSeen = true
    if (type === 'tRNS') {
      if (transparencySeen || dataSeen) return undefined
      transparencySeen = true
      if (colorType === 0) {
        if (length !== 2 || view.getUint16(offset + 8) >= 2 ** bitDepth) return undefined
      } else if (colorType === 2) {
        if (length !== 6 || [8, 10, 12].some(at => view.getUint16(offset + at) >= 2 ** bitDepth)) return undefined
      } else if (colorType === 3) {
        if (!paletteEntries || !length || length > paletteEntries) return undefined
      } else return undefined
    }
    if (['IHDR', 'PLTE', 'IDAT', 'IEND', 'tRNS'].includes(type)) total += end - offset
    offset = end
  }
  const result = new Uint8Array(total)
  result.set(bytes.subarray(0, 8)); offset = 8
  let written = 8
  // Two bounded passes avoid retaining one JS object per (possibly empty) IDAT.
  while (offset < bytes.length) {
    const end = offset + 12 + view.getUint32(offset)
    const type = String.fromCharCode(...bytes.subarray(offset + 4, offset + 8))
    if (['IHDR', 'PLTE', 'IDAT', 'IEND', 'tRNS'].includes(type)) { result.set(bytes.subarray(offset, end), written); written += end - offset }
    offset = end
  }
  return result
}
