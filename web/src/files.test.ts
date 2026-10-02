import { describe, expect, it } from 'vitest'
import { fromDrop, fromFiles, safePath } from './files'

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
})
