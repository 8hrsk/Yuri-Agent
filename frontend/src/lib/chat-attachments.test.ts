import { afterEach, describe, expect, it, vi } from 'vitest'

import { readChatAttachments } from './chat-attachments'

describe('chat attachments', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('accepts arbitrary UTF-8 code and JSON files rather than an extension allow-list', async () => {
    const result = await readChatAttachments([
      new File(['package main\n\nfunc main() {}\n'], 'main.go'),
      new File(['{"enabled":true}'], 'settings.json', { type: 'application/json' }),
    ])

    expect(result).toHaveLength(2)
    expect(result.map((item) => [item.name, item.kind])).toEqual([
      ['main.go', 'text'],
      ['settings.json', 'text'],
    ])
    expect(result[0].dataBase64).toBeTruthy()
  })

  it('accepts supported images and creates an immediate local preview', async () => {
    const result = await readChatAttachments([
      new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'screen.png', { type: 'image/png' }),
    ])

    expect(result[0]).toMatchObject({ name: 'screen.png', kind: 'image', mediaType: 'image/png', sizeBytes: 4 })
    expect(result[0].previewDataUrl).toMatch(/^data:image\/png;base64,/)
  })

  it('works in the Wails WebView when crypto.randomUUID is unavailable', async () => {
    vi.stubGlobal('crypto', {})

    const result = await readChatAttachments([
      new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'wails.png', { type: 'image/png' }),
    ])

    expect(result[0]).toMatchObject({ name: 'wails.png', kind: 'image' })
    expect(result[0].id).toMatch(/^attachment-/)
    expect(result[0].previewDataUrl).toMatch(/^data:image\/png;base64,/)
  })

  it('rejects explicit binaries and invalid UTF-8', async () => {
    await expect(readChatAttachments([new File(['MZ'], 'helper.dll')])).rejects.toThrow(/бинарный/i)
    await expect(readChatAttachments([new File([new Uint8Array([0xff, 0xfe, 0xfd])], 'unknown.dat')])).rejects.toThrow(/UTF-8/i)
  })

  it('rejects duplicate names across subsequent selections', async () => {
    const existing = await readChatAttachments([new File(['first'], 'notes.md')])
    await expect(readChatAttachments([new File(['second'], 'NOTES.md')], existing)).rejects.toThrow(/уже прикреплён/i)
  })
})
