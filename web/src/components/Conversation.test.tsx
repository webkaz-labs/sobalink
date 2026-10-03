import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import * as api from '../api'
import { transferEnglish, transferJapanese } from '../transfer-i18n'

const initialState: api.State = {
  csrfToken: 'test-csrf', self: { name: 'This device', status: 'online', receiveDirectory: '/receiving', networks: ['tailnet'] },
  peers: [
    { id: 'peer-a', name: 'Studio', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' },
    { id: 'peer-b', name: 'Notebook', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' },
  ], messages: [], transfers: [], services: [], shares: [], settings: { network: 'tailnet', receiveDirectory: '/receiving' },
}
function setup(state = initialState) {
  let current = state
  const requests: { name: string; payload: Record<string, unknown> }[] = []
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.body) requests.push(JSON.parse(init.body as string))
    return new Response(JSON.stringify(path === '/api/state' ? current : { ok: true }), { status: 200 })
  })
  vi.stubGlobal('fetch', fetch)
  return { requests, fetch, setState: (next: api.State) => { current = next } }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
async function openPeer(name = 'Studio') { await userEvent.click(await screen.findByRole('button', { name: new RegExp(name) })); await userEvent.click(screen.getByRole('button', { name: /^(Files & messages|ファイルとメッセージ)$/ })) }
function fileInput(container: HTMLElement, folder = false) {
  return container.querySelector(`input[type="file"]${folder ? '[webkitdirectory]' : ':not([webkitdirectory])'}`) as HTMLInputElement
}
function folderFile(path: string, contents = 'x') {
  const file = new File([contents], path.split('/').at(-1)!)
  Object.defineProperty(file, 'webkitRelativePath', { value: path })
  return file
}
function delayedFolder() {
  let deliver!: (file: File) => void
  const file = { name: 'later.txt', isFile: true, isDirectory: false, file: (success: (value: File) => void) => { deliver = success } }
  let page = 0
  const folder = { name: 'folder', isFile: false, isDirectory: true, createReader: () => ({ readEntries: (success: (entries: unknown[]) => void) => success(page++ === 0 ? [file] : []) }) }
  return { data: { items: [{ kind: 'file', webkitGetAsEntry: () => folder }], files: [], types: ['Files'] }, finish: () => deliver(new File(['later'], 'later.txt')) }
}
beforeEach(() => { localStorage.setItem('sobalink.locale', 'en') })

describe('editable transfer batches', () => {
  it('reviews two separately pasted image.png captures with distinct filenames and uploads that exact manifest', async () => {
    setup()
    const upload = vi.spyOn(api, 'upload').mockResolvedValue({ ok: true })
    render(<App />); await openPeer()
    const first = new File(['first'], 'image.png', { type: 'image/png' })
    const second = new File(['second'], 'image.png', { type: 'image/png' })
    const input = screen.getByRole('textbox', { name: 'Write a message…' })
    fireEvent.paste(input, { clipboardData: { files: [first] } })
    await screen.findByRole('button', { name: 'Remove from batch: image.png' })
    fireEvent.paste(input, { clipboardData: { files: [second] } })
    await screen.findByRole('button', { name: 'Remove from batch: image (2).png' })
    expect(screen.getByText('image.png')).toBeInTheDocument()
    expect(screen.getByText('image (2).png')).toBeInTheDocument()
    expect(upload).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    expect(upload).toHaveBeenCalledTimes(1)
    expect(upload.mock.calls[0][1]).toEqual({
      entries: [{ path: 'image.png', size: 5, kind: 'file' }, { path: 'image (2).png', size: 6, kind: 'file' }],
      files: [{ path: 'image.png', file: first }, { path: 'image (2).png', file: second }],
    })
  })

  it('appends files, a folder and a pasted image; removes one entry; preserves drafts through navigation and stages only one upload', async () => {
    setup()
    const pending = deferred<api.CommandResult>()
    const upload = vi.spyOn(api, 'upload').mockImplementationOnce(() => pending.promise).mockResolvedValue({ ok: true })
    const { container } = render(<App />); await openPeer()
    fireEvent.change(screen.getByRole('textbox', { name: 'Write a message…' }), { target: { value: 'Unsent text' } })
    await userEvent.upload(fileInput(container), [new File(['keep'], 'keep.txt'), new File(['remove'], 'remove.txt')])
    await userEvent.upload(fileInput(container, true), folderFile('folder/nested.txt'))
    fireEvent.paste(screen.getByRole('textbox', { name: 'Write a message…' }), { clipboardData: { files: [new File(['image'], 'picture.png', { type: 'image/png' })] } })
    await screen.findByText('picture.png')
    await userEvent.click(screen.getByRole('button', { name: 'Remove from batch: remove.txt' }))
    expect(screen.queryByText('remove.txt')).not.toBeInTheDocument()
    expect(upload).not.toHaveBeenCalled()
    await openPeer('Notebook')
    await userEvent.upload(fileInput(container), new File(['other'], 'other.txt'))
    await openPeer()
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toHaveValue('Unsent text')
    expect(screen.getByText('folder/nested.txt')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    expect(upload).toHaveBeenCalledTimes(1)
    expect(upload.mock.calls[0][0]).toBe('peer-a')
    expect(upload.mock.calls[0][1].entries.map(entry => entry.path)).toEqual(['keep.txt', 'folder/nested.txt', 'picture.png'])
    expect(upload.mock.calls[0][1].files.map(entry => entry.path)).toEqual(['keep.txt', 'folder/nested.txt', 'picture.png'])
    await openPeer('Notebook')
    expect(screen.getByRole('button', { name: 'Send batch' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Remove from batch: other.txt' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Attach files' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    expect(upload).toHaveBeenCalledTimes(1)
    await act(async () => { pending.resolve({ ok: true }) })
    expect(screen.getByText('other.txt')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send batch' })).toBeEnabled()
    await openPeer()
    expect(screen.queryByRole('button', { name: 'Send batch' })).not.toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toHaveValue('Unsent text')
  })

  it('preserves the previous batch and ID when a duplicate or cumulative limit rejects an addition', async () => {
    setup({ ...initialState, settings: { ...initialState.settings, maxFiles: 2, maxBatchBytes: 6 } })
    const id = vi.spyOn(api, 'requestID')
    const upload = vi.spyOn(api, 'upload').mockResolvedValue({ ok: true })
    const { container } = render(<App />); await openPeer()
    await userEvent.upload(fileInput(container), new File(['123'], 'base.txt'))
    const originalId = id.mock.results[0].value
    await userEvent.upload(fileInput(container), new File(['456'], 'base.txt'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Two items have the same path')
    await userEvent.upload(fileInput(container), [new File([], 'second.txt'), new File([], 'third.txt')])
    expect(await screen.findByRole('alert')).toHaveTextContent('batch entry limit')
    await userEvent.upload(fileInput(container), new File(['4567'], 'large.txt'))
    expect(await screen.findByRole('alert')).toHaveTextContent('batch size limit')
    expect(screen.getByText('base.txt')).toBeInTheDocument()
    expect(screen.queryByText('second.txt')).not.toBeInTheDocument()
    expect(screen.queryByText('large.txt')).not.toBeInTheDocument()
    expect(id).toHaveBeenCalledTimes(1)
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    expect(upload.mock.calls[0][2]).toBe(originalId)
    expect(upload.mock.calls[0][1].entries.map(entry => entry.path)).toEqual(['base.txt'])
  })

  it('reuses an unchanged failed request ID and changes it only after append or removal', async () => {
    setup()
    const upload = vi.spyOn(api, 'upload').mockRejectedValue(new api.ApiError('network_error', ''))
    const { container } = render(<App />); await openPeer()
    await userEvent.upload(fileInput(container), new File(['one'], 'one.txt'))
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Send batch' })).toBeEnabled())
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Send batch' })).toBeEnabled())
    expect(upload.mock.calls[1][2]).toBe(upload.mock.calls[0][2])
    await userEvent.upload(fileInput(container), new File(['two'], 'two.txt'))
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Send batch' })).toBeEnabled())
    expect(upload.mock.calls[2][2]).not.toBe(upload.mock.calls[1][2])
    await userEvent.click(screen.getByRole('button', { name: 'Remove from batch: one.txt' }))
    upload.mockResolvedValueOnce({ ok: true })
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    expect(upload.mock.calls[3][2]).not.toBe(upload.mock.calls[2][2])
    expect(upload.mock.calls[3][1].entries.map(entry => entry.path)).toEqual(['two.txt'])
  })

  it('keeps async folder additions addressed to the original peer and disables send until collection finishes', async () => {
    setup()
    const upload = vi.spyOn(api, 'upload').mockResolvedValue({ ok: true })
    const { container } = render(<App />); await openPeer()
    await userEvent.upload(fileInput(container), new File([], 'first.txt'))
    const folder = delayedFolder()
    fireEvent.drop(container.querySelector('.conversation')!, { dataTransfer: folder.data })
    expect(screen.getByRole('button', { name: 'Send batch' })).toBeDisabled()
    await openPeer('Notebook')
    await act(async () => { folder.finish() })
    expect(screen.queryByText('folder/later.txt')).not.toBeInTheDocument()
    await openPeer()
    expect(screen.getByText('folder/later.txt')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    expect(upload.mock.calls[0][0]).toBe('peer-a')
    expect(upload.mock.calls[0][1].entries.map(entry => entry.path)).toEqual(['first.txt', 'folder/later.txt'])
  })

  it('normalizes an added empty parent folder without changing a failed request ID', async () => {
    setup()
    const upload = vi.spyOn(api, 'upload').mockRejectedValue(new api.ApiError('network_error', ''))
    const { container } = render(<App />); await openPeer()
    await userEvent.upload(fileInput(container, true), folderFile('folder/child.txt'))
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Send batch' })).toBeEnabled())
    const empty = { name: 'folder', isDirectory: true, isFile: false, createReader: () => ({ readEntries: (success: (entries: unknown[]) => void) => success([]) }) }
    fireEvent.drop(container.querySelector('.conversation')!, { dataTransfer: { items: [{ kind: 'file', webkitGetAsEntry: () => empty }], files: [] } })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Send batch' })).toBeEnabled())
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    expect(upload.mock.calls[1][2]).toBe(upload.mock.calls[0][2])
    expect(upload.mock.calls[1][1].entries.map(entry => entry.path)).toEqual(['folder/child.txt'])
  })

  it('discards a pending folder addition when the original peer is paused before collection finishes', async () => {
    const { setState } = setup()
    const { container } = render(<App />); await openPeer()
    const folder = delayedFolder()
    fireEvent.drop(container.querySelector('.conversation')!, { dataTransfer: folder.data })
    setState({ ...initialState, peers: [{ ...initialState.peers[0], autosave: { enabled: false, paused: true } }, initialState.peers[1]] })
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    await screen.findByText(/Messages and file transfers with this device are paused/)
    await act(async () => { folder.finish() })
    setState(initialState)
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(screen.queryByText('folder/later.txt')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Send batch' })).not.toBeInTheDocument()
  })

  it('revalidates against a lowered current limit before staging without discarding the draft', async () => {
    const { setState } = setup()
    const upload = vi.spyOn(api, 'upload').mockResolvedValue({ ok: true })
    const { container } = render(<App />); await openPeer()
    await userEvent.upload(fileInput(container), new File(['1234'], 'saved.txt'))
    setState({ ...initialState, settings: { ...initialState.settings, maxBatchBytes: 3 } })
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('batch size limit')
    expect(screen.getByText('saved.txt')).toBeInTheDocument()
    expect(upload).not.toHaveBeenCalled()
  })

  it('clears pending folder collection on authentication loss without restoring it after sign-in', async () => {
    const { fetch } = setup()
    const { container } = render(<App />); await openPeer()
    await userEvent.upload(fileInput(container), new File([], 'private.txt'))
    const folder = delayedFolder()
    fireEvent.drop(container.querySelector('.conversation')!, { dataTransfer: folder.data })
    fetch.mockImplementation(async () => new Response('{"code":"unauthenticated"}', { status: 401 }))
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(await screen.findByLabelText('Local access code')).toBeInTheDocument()
    await act(async () => { folder.finish() })
    fetch.mockImplementation(async (path: string) => new Response(JSON.stringify(path === '/api/state' ? initialState : { ok: true }), { status: 200 }))
    fireEvent.change(screen.getByLabelText('Local access code'), { target: { value: 'test-access-code' } })
    await userEvent.click(screen.getByRole('button', { name: 'Open sobalink' }))
    await openPeer()
    expect(screen.queryByText('private.txt')).not.toBeInTheDocument()
    expect(screen.queryByText('folder/later.txt')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Send batch' })).not.toBeInTheDocument()
  })

  it('aborts the single active upload on authentication loss and clears the attachment draft', async () => {
    const { fetch } = setup()
    const pending = deferred<api.CommandResult>()
    const upload = vi.spyOn(api, 'upload').mockImplementation(() => pending.promise)
    const { container } = render(<App />); await openPeer()
    await userEvent.upload(fileInput(container), new File([], 'private.txt'))
    await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
    const signal = upload.mock.calls[0][4]
    fetch.mockImplementation(async () => new Response('{"code":"unauthenticated"}', { status: 401 }))
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(await screen.findByLabelText('Local access code')).toBeInTheDocument()
    expect(signal.aborted).toBe(true)
    await act(async () => { pending.resolve({ ok: true }) })
    expect(screen.queryByText('private.txt')).not.toBeInTheDocument()
  })
})

function withOffer(): api.State {
  return { ...initialState, peers: [{ ...initialState.peers[0], autosave: { enabled: false, paused: false, directory: '/automatic' } }, initialState.peers[1]], transfers: [{ id: 'batch-one', peerId: 'peer-a', direction: 'incoming', name: 'Notes', entries: [{ path: 'note.txt', kind: 'file', size: 5 }], totalBytes: 5, completedBytes: 0, status: 'offered', createdAt: '2026-10-02T10:00:00Z' }] }
}
describe('per-batch receive destination', () => {
  it('retains each incoming batch override through peer navigation and accepts only its reviewed destination', async () => {
    const state = withOffer()
    state.transfers.push({ ...state.transfers[0], id: 'batch-two', peerId: 'peer-b', name: 'Notebook notes' })
    const { requests } = setup(state)
    render(<App />); await openPeer()
    await userEvent.click(screen.getByRole('button', { name: 'Change folder' }))
    fireEvent.change(screen.getByRole('textbox', { name: /Receive directory/ }), { target: { value: '/studio-batch' } })
    await openPeer('Notebook')
    await userEvent.click(screen.getByRole('button', { name: 'Change folder' }))
    expect(screen.getByRole('textbox', { name: /Receive directory/ })).toHaveValue('/receiving')
    fireEvent.change(screen.getByRole('textbox', { name: /Receive directory/ }), { target: { value: '/notebook-batch' } })
    await openPeer()
    expect(screen.getByRole('textbox', { name: /Receive directory/ })).toHaveValue('/studio-batch')
    expect(requests).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Accept batch' }))
    await waitFor(() => expect(requests[0]).toMatchObject({ name: 'transfer.accept', payload: { transferId: 'batch-one', destination: '/studio-batch' } }))
    await openPeer('Notebook')
    expect(screen.getByRole('textbox', { name: /Receive directory/ })).toHaveValue('/notebook-batch')
    expect(requests).toHaveLength(1)
    expect(state.settings?.receiveDirectory).toBe('/receiving')
  })

  it('clears a remembered incoming destination on authentication loss', async () => {
    const state = withOffer()
    const { fetch } = setup(state)
    render(<App />); await openPeer()
    await userEvent.click(screen.getByRole('button', { name: 'Change folder' }))
    fireEvent.change(screen.getByRole('textbox', { name: /Receive directory/ }), { target: { value: '/private-destination' } })
    await openPeer('Notebook')
    fetch.mockImplementation(async () => new Response('{"code":"unauthenticated"}', { status: 401 }))
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(await screen.findByLabelText('Local access code')).toBeInTheDocument()
    fetch.mockImplementation(async (path: string) => new Response(JSON.stringify(path === '/api/state' ? state : { ok: true }), { status: 200 }))
    fireEvent.change(screen.getByLabelText('Local access code'), { target: { value: 'test-access-code' } })
    await userEvent.click(screen.getByRole('button', { name: 'Open sobalink' }))
    await openPeer()
    expect(screen.queryByDisplayValue('/private-destination')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Change folder' }))
    expect(screen.getByRole('textbox', { name: /Receive directory/ })).toHaveValue('/receiving')
  })

  it('prefills the default and accepts with an explicit override without changing saved settings', async () => {
    const state = withOffer()
    const { requests } = setup(state)
    render(<App />); await openPeer()
    const card = screen.getByRole('article', { name: /Notes/ })
    await userEvent.click(within(card).getByRole('button', { name: 'Change folder' }))
    const input = within(card).getByRole('textbox', { name: /Receive directory/ })
    expect(input).toHaveValue('/receiving')
    expect(input).toHaveFocus()
    fireEvent.change(input, { target: { value: '' } })
    expect(within(card).getByRole('button', { name: 'Accept batch' })).toBeDisabled()
    fireEvent.change(input, { target: { value: ' /batch-only ' } })
    await userEvent.click(within(card).getByRole('button', { name: 'Accept batch' }))
    await waitFor(() => expect(requests).toEqual([{ requestId: expect.any(String), name: 'transfer.accept', payload: { transferId: 'batch-one', destination: '/batch-only' } }]))
    expect(state.settings?.receiveDirectory).toBe('/receiving')
    expect(state.peers[0].autosave).toEqual({ enabled: false, paused: false, directory: '/automatic' })
  })

  it('lets an edit be discarded before acceptance and sends the explicit default', async () => {
    const { requests } = setup(withOffer())
    render(<App />); await openPeer()
    await userEvent.click(screen.getByRole('button', { name: 'Change folder' }))
    fireEvent.change(screen.getByRole('textbox', { name: /Receive directory/ }), { target: { value: '/changed' } })
    await userEvent.click(screen.getByRole('button', { name: 'Use default folder' }))
    expect(screen.queryByRole('textbox', { name: /Receive directory/ })).not.toBeInTheDocument()
    expect(requests).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Accept batch' }))
    await waitFor(() => expect(requests[0]).toMatchObject({ name: 'transfer.accept', payload: { destination: '/receiving' } }))
  })

  it('keeps new Japanese and English copy aligned, including accessible removal paths', async () => {
    expect(Object.keys(transferJapanese)).toEqual(Object.keys(transferEnglish))
    localStorage.setItem('sobalink.locale', 'ja')
    setup(withOffer())
    const { container } = render(<App />); await openPeer()
    await userEvent.click(screen.getByRole('button', { name: '保存先を変更' }))
    expect(screen.getByText('このバッチだけに使う保存先です。既定の保存先や自動受信の設定は変わりません。')).toBeInTheDocument()
    await userEvent.upload(fileInput(container), new File([], '資料.txt'))
    expect(screen.getByRole('button', { name: 'バッチから削除: 資料.txt' })).toBeInTheDocument()
  })
})

describe.each(['en', 'ja'] as const)('reviewed outgoing discard (%s)', locale => {
  const copy = locale === 'ja' ? transferJapanese : transferEnglish
  const cancel = locale === 'ja' ? 'キャンセル' : 'Cancel'
  const close = locale === 'ja' ? '閉じる' : 'Close'
  const retry = locale === 'ja' ? '転送を再試行' : 'Retry transfer'

  it('reviews and cancels failed copies before committing the exact batch', async () => {
    const status = 'failed' as const
    localStorage.setItem('sobalink.locale', locale)
    const state = withOffer()
    state.transfers[0] = { ...state.transfers[0], direction: 'outgoing', status }
    const { requests } = setup(state)
    render(<App />); await openPeer()
    const card = screen.getByRole('article', { name: /Notes/ })
    expect(within(card).getByText(copy.discardHint)).toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: /^(Remove from history|履歴から削除)$/ })).not.toBeInTheDocument()
    const review = () => userEvent.click(within(card).getByRole('button', { name: copy.discardBatch }))
    await review()
    let dialog = screen.getByRole('dialog', { name: copy.discardTitle })
    expect(dialog).toHaveTextContent('Notes')
    expect(dialog).toHaveTextContent('Studio')
    expect(dialog).toHaveTextContent(copy.discardImpact)
    expect(dialog).toHaveTextContent(copy.discardPreservedFiles)
    expect(requests).toHaveLength(0)
    await userEvent.click(within(dialog).getByRole('button', { name: cancel }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(requests).toHaveLength(0)
    if (status === 'failed') expect(within(card).getByRole('button', { name: retry })).toBeEnabled()
    await review()
    dialog = screen.getByRole('dialog', { name: copy.discardTitle })
    await userEvent.click(within(dialog).getByRole('button', { name: close }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(requests).toHaveLength(0)
    await review()
    fireEvent(screen.getByRole('dialog'), new Event('cancel', { bubbles: true, cancelable: true }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(requests).toHaveLength(0)
    await review()
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: copy.discardConfirm }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(requests).toEqual([{ requestId: expect.any(String), name: 'transfer.forget', payload: { transferId: 'batch-one' } }])
  })
})

describe.each(['en', 'ja'] as const)('declined outgoing history (%s)', locale => {
  it('offers no retry and clears history without a stale sending-copy review', async () => {
    localStorage.setItem('sobalink.locale', locale)
    const state = withOffer()
    state.transfers[0] = { ...state.transfers[0], direction: 'outgoing', status: 'declined' }
    const { requests } = setup(state)
    render(<App />); await openPeer()
    const card = screen.getByRole('article', { name: /Notes/ })
    expect(within(card).getByText(locale === 'ja' ? '辞退済み' : 'Declined')).toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: /^(Retry transfer|転送を再試行)$/ })).not.toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: (locale === 'ja' ? transferJapanese : transferEnglish).discardBatch })).not.toBeInTheDocument()
    await userEvent.click(within(card).getByRole('button', { name: locale === 'ja' ? '履歴から削除' : 'Remove from history' }))
    await waitFor(() => expect(requests).toEqual([{ requestId: expect.any(String), name: 'transfer.forget', payload: { transferId: 'batch-one' } }]))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

describe('discard review lifetime', () => {
  it('keeps a failed discard visible and lets the user cancel without repeating the command', async () => {
    const state = withOffer()
    state.transfers[0] = { ...state.transfers[0], direction: 'outgoing', status: 'failed' }
    const { requests, fetch } = setup(state)
    render(<App />); await openPeer()
    await userEvent.click(screen.getByRole('button', { name: 'Discard batch…' }))
    fetch.mockImplementationOnce(async (_path: string, init?: RequestInit) => {
      if (init?.body) requests.push(JSON.parse(init.body as string))
      return new Response(JSON.stringify({ code: 'invalid_state' }), { status: 409 })
    })
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Discard copies and history' }))
    expect(await within(screen.getByRole('dialog')).findByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toHaveTextContent(transferEnglish.discardPreservedFiles)
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(requests).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Retry transfer' })).toBeEnabled()
  })

  it('dismisses an outdated discard review when the transfer state changes', async () => {
    const state = withOffer()
    state.transfers[0] = { ...state.transfers[0], direction: 'outgoing', status: 'failed' }
    const { requests, setState } = setup(state)
    render(<App />); await openPeer()
    await userEvent.click(screen.getByRole('button', { name: 'Discard batch…' }))
    setState({ ...state, transfers: [{ ...state.transfers[0], status: 'transferring' }] })
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    setState(state)
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(await screen.findByRole('button', { name: 'Discard batch…' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(requests).toHaveLength(0)
  })

  it.each(['incoming', 'outgoing'] as const)('still removes completed %s history without a sending-copy review', async direction => {
    const state = withOffer()
    state.transfers[0] = { ...state.transfers[0], direction, status: 'completed' }
    const { requests } = setup(state)
    render(<App />); await openPeer()
    expect(screen.getByText('Saved files stay in place.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Remove from history' }))
    await waitFor(() => expect(requests).toEqual([{ requestId: expect.any(String), name: 'transfer.forget', payload: { transferId: 'batch-one' } }]))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

it('honors extended path choices and rechecks a lowered limit without discarding the draft', async () => {
  const limits: api.ServiceLimits = { effective: { logical: { pathDepth: { mode: 'limited', value: 40 }, pathBytes: { mode: 'limited', value: 8192 } }, resources: { transferManifestBytes: { mode: 'limited', value: 256 * 1024 } } }, usage: { materializedListeners: 0 } }
  const { setState } = setup({ ...initialState, limits })
  const upload = vi.spyOn(api, 'upload')
  const { container } = render(<App />); await openPeer()
  const path = `${Array.from({ length: 20 }, (_, index) => `folder-${index}`).join('/')}/notes.txt`
  await userEvent.upload(fileInput(container), folderFile(path))
  expect(await screen.findByRole('heading', { name: 'To: Studio' })).toBeInTheDocument()
  expect(container.querySelector('.batch-preview')).toHaveTextContent(path)
  setState({ ...initialState, limits: { ...limits, effective: { ...limits.effective, logical: { ...limits.effective.logical, pathDepth: { mode: 'limited', value: 2 } } } } })
  await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
  await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('One selected path cannot be safely transferred')
  expect(container.querySelector('.batch-preview')).toHaveTextContent(path)
  expect(upload).not.toHaveBeenCalled()
})
