import { describe, expect, it } from 'vitest'
import { appendSelection, DEFAULT_MAX_BYTES, DEFAULT_MAX_FILES, fromClipboardImages, fromDrop, fromFiles, removeSelection, safePath } from './files'
import type { UploadSelection } from './api'

describe('batch collection', () => {
  it.each(['../file', '/absolute', 'one/../file', 'one//file', 'C:\\file', 'one\\file', 'file\u0000.txt'])('rejects unsafe path %s', value => { expect(() => safePath(value)).toThrow('unsafe_path') })
  it('preserves relative folder paths and zero-byte files', () => {
    const file = new File([], 'empty.txt')
    Object.defineProperty(file, 'webkitRelativePath', { value: 'folder/empty.txt' })
    expect(fromFiles([file]).entries).toEqual([{ path: 'folder/empty.txt', size: 0, kind: 'file' }])
  })
  it('bounds item count and bytes before upload', () => {
    expect(() => fromFiles([new File(['x'], 'a'), new File(['x'], 'b')], 1)).toThrow('too_many_files')
    expect(() => fromFiles([new File(['long'], 'a')], 256, 3)).toThrow('too_large')
  })
  it('does not silently overwrite duplicate paths', () => { expect(() => fromFiles([new File([], 'a'), new File([], 'a')])).toThrow('duplicate_path') })
  it('preserves empty directories via supported drop APIs', async () => {
    const directory = { name: 'empty', isDirectory: true, isFile: false, createReader: () => ({ readEntries: (success: (items: unknown[]) => void) => success([]) }) }
    const result = await fromDrop({ items: [{ kind: 'file', webkitGetAsEntry: () => directory }], files: [] } as unknown as DataTransfer)
    expect(result.entries).toEqual([{ path: 'empty', kind: 'directory', size: 0 }])
    expect(result.files).toHaveLength(0)
  })
  it('reads every directory-reader page', async () => {
    let page = 0
    const file = { name: 'a.txt', isFile: true, isDirectory: false, file: (success: (file: File) => void) => success(new File(['abc'], 'a.txt')) }
    const directory = { name: 'folder', isDirectory: true, isFile: false, createReader: () => ({ readEntries: (success: (items: unknown[]) => void) => success(page++ === 0 ? [file] : []) }) }
    const result = await fromDrop({ items: [{ kind: 'file', webkitGetAsEntry: () => directory }], files: [] } as unknown as DataTransfer)
    expect(result.entries).toEqual([{ path: 'folder/a.txt', kind: 'file', size: 3 }])
  })
  it.each(['CON.txt', 'folder/name.', 'folder/name ', 'a?b', 'a<b', 'COM¹.txt', 'a/\u0080b', 'a'.repeat(256), Array(17).fill('a').join('/')])('rejects nonportable path %s', value => { expect(() => safePath(value)).toThrow('unsafe_path') })
  it.each([
    ['Note.txt', 'note.txt'], ['caf\u00e9.txt', 'cafe\u0301.txt'], ['Straße.txt', 'STRASSE.txt'], ['A/one.txt', 'a/two.txt'], ['parent', 'parent/child'],
  ])('rejects duplicate or conflicting paths %s and %s across additions', (first, second) => {
    const current = fromFiles([new File([], first)])
    expect(() => appendSelection(current, fromFiles([new File([], second)]))).toThrow('duplicate_path')
    expect(current.entries.map(entry => entry.path)).toEqual([first])
  })
  it('appends mixed files without mutating either input and removes only the chosen entry', () => {
    const current = fromFiles([new File(['note'], 'note.txt')])
    const addition = fromFiles([new File(['picture'], 'image.png'), new File([], 'folder/empty.txt')])
    const appended = appendSelection(current, addition)
    expect(appended.entries.map(entry => entry.path)).toEqual(['note.txt', 'image.png', 'folder/empty.txt'])
    const removed = removeSelection(appended, 'image.png')
    expect(removed.entries.map(entry => entry.path)).toEqual(['note.txt', 'folder/empty.txt'])
    expect(removed.files.map(entry => entry.path)).toEqual(['note.txt', 'folder/empty.txt'])
    expect(current.entries).toHaveLength(1)
    expect(addition.entries).toHaveLength(2)
    expect(removeSelection(removed, 'missing')).toBe(removed)
  })
  it('normalizes explicit empty folders once they contain children, before counting the batch', () => {
    const empty: UploadSelection = { entries: [{ path: 'folder', kind: 'directory', size: 0 }], files: [] }
    const file = fromFiles([new File(['hello'], 'folder/hello.txt')])
    const result = appendSelection(empty, file, 1)
    expect(result.entries).toEqual(file.entries)
    expect(empty.entries).toHaveLength(1)
    expect(appendSelection(result, empty, 1)).toBe(result)
  })
  it('validates count and bytes against the whole batch and preserves the previous selection on failure', () => {
    const current = fromFiles([new File(['1234'], 'one.txt')])
    const addition = fromFiles([new File(['5678'], 'two.txt')])
    expect(() => appendSelection(current, addition, 1)).toThrow('too_many_files')
    expect(() => appendSelection(current, addition, 2, 7)).toThrow('too_large')
    expect(current.entries).toEqual([{ path: 'one.txt', size: 4, kind: 'file' }])
    expect(current.files).toHaveLength(1)
  })
  it('never allows server limits to raise the 256-entry or 1-GiB browser cap', () => {
    const files = Array.from({ length: DEFAULT_MAX_FILES + 1 }, (_, index) => new File([], `file-${index}`))
    expect(() => fromFiles(files, 1000)).toThrow('too_many_files')
    const file = new File([], 'large.bin')
    Object.defineProperty(file, 'size', { value: DEFAULT_MAX_BYTES + 1 })
    expect(() => fromFiles([file], 1000, DEFAULT_MAX_BYTES * 2)).toThrow('too_large')
  })
  it('numbers clipboard image collisions without renaming selected files or mutating image data', () => {
    const current = fromFiles([new File([], 'IMAGE.png'), new File([], 'image (2).png/child.txt')])
    const images = [new File(['first'], 'image.png', { type: 'image/png' }), new File(['second'], 'image.png', { type: 'image/png' })]
    const addition = fromClipboardImages(images, current)
    expect(addition.entries.map(entry => entry.path)).toEqual(['image (3).png', 'image (4).png'])
    expect(addition.files.map(entry => entry.path)).toEqual(['image (3).png', 'image (4).png'])
    expect(addition.files.map(entry => entry.file)).toEqual(images)
    expect(current.entries.map(entry => entry.path)).toEqual(['IMAGE.png', 'image (2).png/child.txt'])
    expect(() => appendSelection(current, fromFiles([images[0]]))).toThrow('duplicate_path')
  })
  it('keeps numbered clipboard filenames within the portable byte limit', () => {
    const image = new File(['pixels'], `${'画'.repeat(83)}.png`, { type: 'image/png' })
    const addition = fromClipboardImages([image], fromFiles([image]))
    expect(addition.entries[0].path.endsWith(' (2).png')).toBe(true)
    expect(new TextEncoder().encode(addition.entries[0].path).length).toBeLessThanOrEqual(255)
  })
})
