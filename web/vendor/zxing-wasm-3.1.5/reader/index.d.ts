/** Local adapter declaration; runtime JS remains byte-identical to upstream. */
export function prepareZXingModule(options: { overrides: { wasmBinary: ArrayBuffer; locateFile: (name: string) => string; print: () => void; printErr: () => void }; fireImmediately: true }): Promise<unknown>
export function readBarcodes(input: ImageData, options: { formats: ['QRCode']; tryHarder: true; tryRotate: true; tryInvert: true; tryDownscale: false; tryDenoise: false; isPure: false; maxNumberOfSymbols: 2; textMode: 'Plain'; returnErrors: false }): Promise<unknown>
