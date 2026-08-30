function safeMarkdownURL(value: string): string {
  if (value.startsWith('/')) return value
  try {
    const parsed = new URL(value)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.toString() : ''
  } catch {
    return ''
  }
}

function localPathFromHref(href: string): string | undefined {
  if (!href.startsWith('/')) return undefined
  try {
    const decoded = decodeURIComponent(href)
    return decoded.startsWith('/') && !decoded.includes('\0') ? decoded : undefined
  } catch {
    return undefined
  }
}

export { localPathFromHref, safeMarkdownURL }
