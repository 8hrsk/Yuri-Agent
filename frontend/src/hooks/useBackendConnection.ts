import { useCallback, useEffect, useMemo, useState } from 'react'

import { detectBackendConnection, type BackendConnection } from '../lib/backend'

const PROBE_DELAY_MS = 220

/**
 * A single frozen instance, so re-probing sets state to the *same* reference and
 * React bails out of the extra render instead of thrashing every consumer.
 */
const CONNECTING: BackendConnection = {
  status: 'connecting',
  label: 'Connecting…',
  detail: 'Проверяем Wails runtime',
}

/**
 * The probe used to run inside a `useCallback` whose cleanup was thrown away by
 * `void probe()`, so the pending timeout survived unmount and could call
 * `setConnection` on a dead component. Owning the timer from an effect gives
 * React the cleanup back, and `refresh` only bumps a token so its identity — and
 * the identity of the returned object — stays stable across renders.
 */
export function useBackendConnection(): BackendConnection & { refresh: () => void } {
  const [connection, setConnection] = useState<BackendConnection>(CONNECTING)
  const [probeToken, setProbeToken] = useState(0)

  const refresh = useCallback(() => setProbeToken((current) => current + 1), [])

  useEffect(() => {
    setConnection(CONNECTING)
    const timer = window.setTimeout(() => setConnection(detectBackendConnection()), PROBE_DELAY_MS)
    return () => window.clearTimeout(timer)
  }, [probeToken])

  return useMemo(() => ({ ...connection, refresh }), [connection, refresh])
}
