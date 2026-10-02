import type { UploadSelection } from './api'

export const DEFAULT_MAX_FILES = 256
export const DEFAULT_MAX_BYTES = 1024 ** 3
export class SelectionError extends Error {
  constructor(public code: 'unsafe_path' | 'too_many_files' | 'too_large' | 'unreadable' | 'duplicate_path') { super(code) }
}
export function safePath(path: string): string {
  if (!path || path.startsWith('/') || path.includes('\\') || path.includes(':') || /[\u0000-\u001f\u007f]/.test(path)) throw new SelectionError('unsafe_path')
  if (path.split('/').some(part => !part || part === '.' || part === '..')) throw new SelectionError('unsafe_path')
  return path
}
export function validateSelection(selection: UploadSelection, maxFiles = DEFAULT_MAX_FILES, maxBytes = DEFAULT_MAX_BYTES) {
  if (selection.entries.length > maxFiles) throw new SelectionError('too_many_files')
  if (selection.entries.reduce((total, entry) => total + entry.size, 0) > maxBytes) throw new SelectionError('too_large')
  const paths = new Set<string>()
  for (const entry of selection.entries) {
    const path = safePath(entry.path)
    if (paths.has(path)) throw new SelectionError('duplicate_path')
    paths.add(path)
  }
  return selection
}
export function fromFiles(files: Iterable<File>, maxFiles?: number, maxBytes?: number): UploadSelection {
  const selected = Array.from(files).map(file => ({ file, path: safePath(file.webkitRelativePath || file.name) }))
  return validateSelection({ files: selected, entries: selected.map(({ path, file }) => ({ path, size: file.size, kind: 'file' })) }, maxFiles, maxBytes)
}
interface Entry {
  name: string
  isFile: boolean
  isDirectory: boolean
  file?(success: (file: File) => void, failure: () => void): void
  createReader?(): { readEntries(success: (entries: Entry[]) => void, failure: () => void): void }
}
export async function fromDrop(data: DataTransfer, maxFiles = DEFAULT_MAX_FILES, maxBytes = DEFAULT_MAX_BYTES): Promise<UploadSelection> {
  // Capture entries during the drop event before DataTransfer becomes protected.
  const roots = Array.from(data.items || []).filter(item => item.kind === 'file').map(item => item.webkitGetAsEntry?.() as Entry | null)
  const fallback = Array.from(data.files)
  if (!roots.length || roots.some(entry => !entry)) return fromFiles(fallback, maxFiles, maxBytes)
  const result: UploadSelection = { files: [], entries: [] }
  let bytes = 0
  const append = (path: string, file?: File) => {
    if (result.entries.length >= maxFiles) throw new SelectionError('too_many_files')
    bytes += file?.size || 0
    if (bytes > maxBytes) throw new SelectionError('too_large')
    result.entries.push({ path: safePath(path), size: file?.size || 0, kind: file ? 'file' : 'directory' })
    if (file) result.files.push({ path, file })
  }
  const visit = async (entry: Entry, parent: string, depth = 0): Promise<void> => {
    if (depth > 64) throw new SelectionError('unsafe_path')
    const path = safePath(parent ? `${parent}/${entry.name}` : entry.name)
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
        for (const child of children) await visit(child, path, depth + 1)
      }
      // Nonempty parent folders are implicit in child paths. The receiver's
      // portable manifest reserves explicit directory entries for empty folders.
      if (empty) append(path)
    } else throw new SelectionError('unreadable')
  }
  for (const root of roots) await visit(root!, '')
  return validateSelection(result, maxFiles, maxBytes)
}
