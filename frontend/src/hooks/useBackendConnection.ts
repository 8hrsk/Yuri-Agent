import { useCallback, useEffect, useState } from 'react'

import { detectBackendConnection, type BackendConnection } from '../lib/backend'

const PROBE_DELAY_MS = 220

export function useBackendConnection(): BackendConnection & { refresh: () => void } {
  const [connection, setConnection] = useState<BackendConnection>({
    status: 'connecting',
    label: 'Connecting…',
    detail: 'Проверяем Wails runtime',
  })

  const probe = useCallback(() => {
    setConnection({
      status: 'connecting',
      label: 'Connecting…',
      detail: 'Проверяем Wails runtime',
    })

    const timer = window.setTimeout(() => {
      setConnection(detectBackendConnection())
    }, PROBE_DELAY_MS)

    return () => window.clearTimeout(timer)
  }, [])

  useEffect(() => probe(), [probe])

  return { ...connection, refresh: () => void probe() }
}
