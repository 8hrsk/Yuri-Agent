// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { localPathFromHref, safeMarkdownURL } from '../lib/markdown'
import { MarkdownMessage } from './MarkdownMessage'

function renderMarkdown(content: string) {
  const onOpenExternalURL = vi.fn()
  const onOpenLocalPath = vi.fn()
  const view = render(
    <MarkdownMessage
      content={content}
      onOpenExternalURL={onOpenExternalURL}
      onOpenLocalPath={onOpenLocalPath}
    />,
  )
  return { ...view, onOpenExternalURL, onOpenLocalPath }
}

describe('MarkdownMessage', () => {
  it('renders GFM headings, emphasis, code blocks and tables', () => {
    const { container } = renderMarkdown([
      '## Результат',
      '',
      '**Готово** и `inline()`.',
      '',
      '```go',
      'fmt.Println("Yuri")',
      '```',
      '',
      '| Файл | Статус |',
      '| --- | --- |',
      '| main.go | ✅ |',
    ].join('\n'))

    expect(screen.getByRole('heading', { name: 'Результат' })).toBeInTheDocument()
    expect(screen.getByText('Готово')).toHaveProperty('tagName', 'STRONG')
    expect(screen.getByText('inline()')).toHaveProperty('tagName', 'CODE')
    expect(container.querySelector('.markdown-code-block__header')).toHaveTextContent('go')
    expect(container.querySelector('pre code')).toHaveTextContent('fmt.Println("Yuri")')
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: 'main.go' })).toBeInTheDocument()
  })

  it('routes HTTP and absolute local links without navigating the WebView', async () => {
    const user = userEvent.setup()
    const { onOpenExternalURL, onOpenLocalPath } = renderMarkdown(
      '[Документация](https://example.test/docs) и [файл](/Users/owner/My%20Project/main.go)',
    )

    await user.click(screen.getByRole('link', { name: 'Документация' }))
    expect(onOpenExternalURL).toHaveBeenCalledWith('https://example.test/docs')
    await user.click(screen.getByRole('link', { name: 'файл' }))
    expect(onOpenLocalPath).toHaveBeenCalledWith('/Users/owner/My Project/main.go')
  })

  it('does not execute raw HTML, unsafe schemes, or remote image loads', () => {
    const { container } = renderMarkdown([
      '<script>window.pwned = true</script>',
      '[опасная ссылка](javascript:alert(1))',
      '![tracker](https://tracker.example/pixel.png)',
    ].join('\n\n'))

    expect(container.querySelector('script')).toBeNull()
    expect(screen.queryByRole('link', { name: 'опасная ссылка' })).not.toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
    expect(screen.getByText('Изображение не загружено: tracker')).toBeInTheDocument()
  })

  it('rejects malformed and non-absolute link targets in helpers', () => {
    expect(safeMarkdownURL('javascript:alert(1)')).toBe('')
    expect(safeMarkdownURL('https://example.test/a')).toBe('https://example.test/a')
    expect(localPathFromHref('relative/file.go')).toBeUndefined()
    expect(localPathFromHref('/Users/owner/a%20b.go')).toBe('/Users/owner/a b.go')
    expect(localPathFromHref('/Users/owner/%E0%A4%A')).toBeUndefined()
  })
})
