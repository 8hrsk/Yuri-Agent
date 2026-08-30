import { Children, isValidElement, memo, useMemo, type MouseEvent, type ReactNode } from 'react'
import Markdown, { type Components, type UrlTransform } from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { localPathFromHref, safeMarkdownURL } from '../lib/markdown'

type MarkdownMessageProps = {
  content: string
  onOpenExternalURL: (url: string) => void
  onOpenLocalPath: (path: string) => void
}

type CodeChildProps = {
  children?: ReactNode
  className?: string
}

const urlTransform: UrlTransform = (value) => safeMarkdownURL(value)

function CodeBlock({ children }: { children?: ReactNode }) {
  const child = Children.count(children) === 1 ? Children.only(children) : undefined
  const props = isValidElement<CodeChildProps>(child) ? child.props : undefined
  const language = props?.className?.match(/(?:^|\s)language-([^\s]+)/)?.[1]
  return (
    <div className="markdown-code-block">
      <div className="markdown-code-block__header">
        <span>{language || 'code'}</span>
      </div>
      <pre>{children}</pre>
    </div>
  )
}

export const MarkdownMessage = memo(function MarkdownMessage({
  content,
  onOpenExternalURL,
  onOpenLocalPath,
}: MarkdownMessageProps) {
  const components = useMemo<Components>(() => ({
    a({ children, href }) {
      if (!href) return <span>{children}</span>
      const localPath = localPathFromHref(href)
      const externalURL = localPath ? undefined : safeMarkdownURL(href)
      if (!localPath && !externalURL) return <span>{children}</span>
      const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
        event.preventDefault()
        if (localPath) onOpenLocalPath(localPath)
        else if (externalURL) onOpenExternalURL(externalURL)
      }
      return (
        <a
          className={localPath ? 'markdown-link markdown-link--local' : 'markdown-link markdown-link--external'}
          href={href}
          onClick={handleClick}
          rel={externalURL ? 'noreferrer noopener' : undefined}
          title={localPath ? `Открыть локальный путь ${localPath}` : `Открыть ${externalURL}`}
        >
          {children}
        </a>
      )
    },
    img({ alt }) {
      return <span className="markdown-image-placeholder">Изображение не загружено{alt ? `: ${alt}` : ''}</span>
    },
    pre({ children }) {
      return <CodeBlock>{children}</CodeBlock>
    },
    table({ children }) {
      return <div className="markdown-table-wrap"><table>{children}</table></div>
    },
  }), [onOpenExternalURL, onOpenLocalPath])

  return (
    <div className="markdown-message">
      <Markdown
        components={components}
        remarkPlugins={[remarkGfm]}
        skipHtml
        urlTransform={urlTransform}
      >
        {content}
      </Markdown>
    </div>
  )
})
