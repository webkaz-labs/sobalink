import type { UploadSelection } from './api'

export const DEFAULT_MAX_FILES = 256
export const DEFAULT_MAX_BYTES = 1024 ** 3
export interface PathLimits { pathDepth: number; pathBytes: number }
const DEFAULT_PATH_LIMITS: PathLimits = { pathDepth: 16, pathBytes: 4096 }
export class SelectionError extends Error {
  constructor(public code: 'unsafe_path' | 'too_many_files' | 'too_large' | 'unreadable' | 'duplicate_path' | 'file_too_large') { super(code) }
}
const encoder = new TextEncoder()
export function safePath(path: string, pathLimits: PathLimits = DEFAULT_PATH_LIMITS): string {
  const parts = path.split('/')
  if (!path || encoder.encode(path).length > pathLimits.pathBytes || parts.length > pathLimits.pathDepth || /[\\<>:"|?*\u0000-\u001f\u007f-\u009f]/.test(path)) throw new SelectionError('unsafe_path')
  if (parts.some(part => !part || part === '.' || part === '..' || /[. ]$/.test(part) || encoder.encode(part).length > 255 || /^(CON|PRN|AUX|NUL|CLOCK\$|CONIN\$|CONOUT\$|COM[1-9¹²³]|LPT[1-9¹²³])$/i.test(part.split('.')[0].trimEnd()))) throw new SelectionError('unsafe_path')
  return path
}
// Include implicit parent folders when detecting portable case/Unicode collisions.
const canonicalPath = (path: string) => path.normalize('NFC').toUpperCase().toLowerCase().normalize('NFC')
export function validateSelection(selection: UploadSelection, maxFiles = DEFAULT_MAX_FILES, maxBytes = DEFAULT_MAX_BYTES, maxFileBytes = Infinity, pathLimits?: PathLimits) {
  // Keep one canonical key per entry instead of all full parent prefixes.
  // The separator sorts before any portable name character, so a parent is
  // adjacent to its first descendant even with siblings such as "folder!".
  const paths = selection.entries.map((entry, index) => {
    const path = safePath(entry.path, pathLimits)
    return { path, key: canonicalPath(path).replaceAll('/', '\u0000'), index }
  }).sort((left, right) => left.key < right.key ? -1 : left.key > right.key ? 1 : 0)
  const parents = new Set<number>()
  for (let index = 1; index < paths.length; index++) {
    const previous = paths[index - 1], current = paths[index]
    const left = previous.key.split('\u0000'), right = current.key.split('\u0000')
    const leftNames = previous.path.split('/'), rightNames = current.path.split('/')
    let shared = 0
    while (shared < left.length && shared < right.length && left[shared] === right[shared]) {
      if (leftNames[shared] !== rightNames[shared]) throw new SelectionError('duplicate_path')
      shared++
    }
    if (shared === left.length) {
      if (shared === right.length || selection.entries[previous.index].kind === 'file') throw new SelectionError('duplicate_path')
      parents.add(previous.index)
    }
  }
  // Empty folders become implicit as soon as a child is added to the batch.
  const entries = selection.entries.filter((_, index) => !parents.has(index))
  if (entries.length > maxFiles) throw new SelectionError('too_many_files')
  if (entries.some(entry => entry.kind === 'file' && entry.size > maxFileBytes)) throw new SelectionError('file_too_large')
  if (entries.reduce((total, entry) => total + entry.size, 0) > maxBytes) throw new SelectionError('too_large')
  return entries.length === selection.entries.length ? selection : { ...selection, entries }
}
export function appendSelection(current: UploadSelection | undefined, addition: UploadSelection, maxFiles?: number, maxBytes?: number, pathLimits?: PathLimits): UploadSelection {
  const result = validateSelection({ entries: [...(current?.entries || []), ...addition.entries], files: [...(current?.files || []), ...addition.files] }, maxFiles, maxBytes, Infinity, pathLimits)
  // A folder that already has children adds no manifest entry and needs no new ID.
  if (current && result.entries.length === current.entries.length && result.entries.every((entry, index) => entry === current.entries[index])) return current
  return result
}
export function removeSelection(selection: UploadSelection, path: string): UploadSelection {
  if (!selection.entries.some(entry => entry.path === path)) return selection
  return { entries: selection.entries.filter(entry => entry.path !== path), files: selection.files.filter(entry => entry.path !== path) }
}
export function fromFiles(files: Iterable<File>, maxFiles?: number, maxBytes?: number, pathLimits?: PathLimits): UploadSelection {
  const selected = Array.from(files).map(file => ({ file, path: safePath(file.webkitRelativePath || file.name, pathLimits) }))
  return validateSelection({ files: selected, entries: selected.map(({ path, file }) => ({ path, size: file.size, kind: 'file' })) }, maxFiles, maxBytes, Infinity, pathLimits)
}
export function fromClipboardImages(files: Iterable<File>, current?: UploadSelection, maxFiles?: number, maxBytes?: number, pathLimits?: PathLimits): UploadSelection {
  // Clipboard captures often reuse image.png. Reserve implicit folder names too,
  // and expose each generated name in the same preview and upload manifest.
  const occupied = new Set((current?.entries || []).map(entry => canonicalPath(entry.path.split('/')[0])))
  const selected = Array.from(files).map(file => {
    const original = safePath(file.name, pathLimits)
    if (original.includes('/')) throw new SelectionError('unsafe_path')
    const dot = original.lastIndexOf('.')
    const extension = dot > 0 ? original.slice(dot) : ''
    const stem = dot > 0 ? original.slice(0, dot) : original
    let path = original
    for (let number = 2; occupied.has(canonicalPath(path)); number++) {
      const suffix = ` (${number})${extension}`
      const characters = Array.from(stem)
      while (characters.length && encoder.encode(characters.join('') + suffix).length > Math.min(255, pathLimits?.pathBytes ?? 4096)) characters.pop()
      path = safePath(characters.join('') + suffix, pathLimits)
    }
    occupied.add(canonicalPath(path))
    return { file, path }
  })
  return validateSelection({ files: selected, entries: selected.map(({ path, file }) => ({ path, size: file.size, kind: 'file' })) }, maxFiles, maxBytes, Infinity, pathLimits)
}
interface Entry {
  name: string
  isFile: boolean
  isDirectory: boolean
  file?(success: (file: File) => void, failure: () => void): void
  createReader?(): { readEntries(success: (entries: Entry[]) => void, failure: () => void): void }
}
export async function fromDrop(data: DataTransfer, maxFiles = DEFAULT_MAX_FILES, maxBytes = DEFAULT_MAX_BYTES, pathLimits?: PathLimits): Promise<UploadSelection> {
  // Capture entries during the drop event before DataTransfer becomes protected.
  const roots = Array.from(data.items || []).filter(item => item.kind === 'file').map(item => item.webkitGetAsEntry?.() as Entry | null)
  const fallback = Array.from(data.files)
  if (!roots.length || roots.some(entry => !entry)) return fromFiles(fallback, maxFiles, maxBytes, pathLimits)
  const result: UploadSelection = { files: [], entries: [] }
  let bytes = 0
  const append = (path: string, file?: File) => {
    if (result.entries.length >= maxFiles) throw new SelectionError('too_many_files')
    bytes += file?.size || 0
    if (bytes > maxBytes) throw new SelectionError('too_large')
    result.entries.push({ path: safePath(path, pathLimits), size: file?.size || 0, kind: file ? 'file' : 'directory' })
    if (file) result.files.push({ path, file })
  }
  const visit = async (entry: Entry, parent: string): Promise<void> => {
    const path = safePath(parent ? `${parent}/${entry.name}` : entry.name, pathLimits)
    if (entry.isFile && entry.file) {
      const file = await new Promise<File>((resolve, reject) => entry.file!(resolve, () => reject(new SelectionError('unreadable'))))
      append(path, file)
    } else if (entry.isDirectory && entry.createReader) {
      const reader = entry.createReader()
      let empty = true
      for (;;) {
        const children = await new Promise<Entry[]>((resolve, reject) => reader.readEntries(resolve, () => reject(new SelectionError('unreadable'))))
        if (!children.length) break
        empty = false
        for (const child of children) await visit(child, path)
      }
      // Nonempty parent folders are implicit in child paths. The receiver's
      // portable manifest reserves explicit directory entries for empty folders.
      if (empty) append(path)
    } else throw new SelectionError('unreadable')
  }
  for (const root of roots) await visit(root!, '')
  return validateSelection(result, maxFiles, maxBytes, Infinity, pathLimits)
}
