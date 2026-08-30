import type { ChatAttachmentInput, ChatAttachmentKind } from './contracts'
import { makeId } from './client/primitives'

export const MAX_CHAT_ATTACHMENTS = 6
export const MAX_CHAT_TEXT_ATTACHMENT_BYTES = 1024 * 1024
export const MAX_CHAT_IMAGE_ATTACHMENT_BYTES = 4 * 1024 * 1024
export const MAX_CHAT_ATTACHMENT_TOTAL_BYTES = 5 * 1024 * 1024

const imageTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])
const imageTypeByExtension = new Map([
  ['.png', 'image/png'], ['.jpg', 'image/jpeg'], ['.jpeg', 'image/jpeg'], ['.gif', 'image/gif'], ['.webp', 'image/webp'],
])
const blockedExtensions = new Set([
  '.a', '.app', '.bin', '.class', '.dmg', '.dll', '.dylib', '.exe', '.iso', '.jar', '.o', '.pkg', '.pyc', '.so', '.tar', '.wasm', '.zip',
])

function extension(name: string): string {
  const index = name.lastIndexOf('.')
  return index < 0 ? '' : name.slice(index).toLowerCase()
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ''
  const chunkSize = 0x8000
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize))
  }
  return btoa(binary)
}

async function readFileBytes(file: File): Promise<Uint8Array> {
  if (typeof file.arrayBuffer === 'function') {
    return new Uint8Array(await file.arrayBuffer())
  }

  // Older WebKit builds expose FileReader but not Blob.arrayBuffer(). Wails
  // inherits that surface from the WebView installed on the host macOS.
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error ?? new Error(`Не удалось прочитать «${file.name}».`))
    reader.onload = () => {
      if (reader.result instanceof ArrayBuffer) resolve(new Uint8Array(reader.result))
      else reject(new Error(`Не удалось прочитать «${file.name}».`))
    }
    reader.readAsArrayBuffer(file)
  })
}

function attachmentKind(file: File, bytes: Uint8Array): ChatAttachmentKind {
  const type = file.type.toLowerCase().split(';')[0]
  if (imageTypes.has(type) || (!type && imageTypeByExtension.has(extension(file.name)))) return 'image'
  if (type.startsWith('image/')) throw new Error(`Формат изображения «${file.name}» не поддерживается. Используйте PNG, JPEG, GIF или WebP.`)
  if (blockedExtensions.has(extension(file.name))) throw new Error(`«${file.name}» — бинарный файл. Yuri принимает UTF-8 текст/код и изображения.`)
  let text: string
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    throw new Error(`«${file.name}» не является UTF-8 текстом или поддерживаемым изображением.`)
  }
  if (text.includes('\0')) throw new Error(`«${file.name}» содержит бинарные данные.`)
  return 'text'
}

export async function readChatAttachments(files: FileList | File[], existing: ChatAttachmentInput[] = []): Promise<ChatAttachmentInput[]> {
  const selected = Array.from(files)
  if (existing.length + selected.length > MAX_CHAT_ATTACHMENTS) {
    throw new Error(`Можно прикрепить не более ${MAX_CHAT_ATTACHMENTS} файлов к одному сообщению.`)
  }
  const names = new Set(existing.map((attachment) => attachment.name.toLocaleLowerCase('ru-RU')))
  let total = existing.reduce((sum, attachment) => sum + attachment.sizeBytes, 0)
  const result: ChatAttachmentInput[] = []
  for (const file of selected) {
    const name = file.name.trim()
    if (!name) throw new Error('У вложения должно быть имя.')
    const normalizedName = name.toLocaleLowerCase('ru-RU')
    if (names.has(normalizedName)) throw new Error(`Файл «${name}» уже прикреплён.`)
    const bytes = await readFileBytes(file)
    const kind = attachmentKind(file, bytes)
    const limit = kind === 'image' ? MAX_CHAT_IMAGE_ATTACHMENT_BYTES : MAX_CHAT_TEXT_ATTACHMENT_BYTES
    if (bytes.byteLength === 0) throw new Error(`Файл «${name}» пуст.`)
    if (bytes.byteLength > limit) throw new Error(`Файл «${name}» больше ${limit / 1024 / 1024} МБ.`)
    total += bytes.byteLength
    if (total > MAX_CHAT_ATTACHMENT_TOTAL_BYTES) throw new Error('Общий размер вложений больше 5 МБ.')
    const dataBase64 = bytesToBase64(bytes)
    const mediaType = file.type || (kind === 'image' ? imageTypeByExtension.get(extension(file.name)) ?? 'image/png' : 'text/plain')
    result.push({
      // `crypto.randomUUID()` is only exposed in secure contexts. Wails uses a
      // custom WebView origin, so it can be absent even though the same code
      // works in a regular development browser.
      id: makeId('attachment'),
      name,
      kind,
      mediaType,
      sizeBytes: bytes.byteLength,
      dataBase64,
      previewDataUrl: kind === 'image' ? `data:${mediaType};base64,${dataBase64}` : undefined,
    })
    names.add(normalizedName)
  }
  return result
}
