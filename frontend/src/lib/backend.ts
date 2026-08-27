export type BackendStatus = 'connecting' | 'connected' | 'offline'

export type BackendConnection = {
  status: BackendStatus
  label: string
  detail: string
}

declare global {
  interface Window {
    /** Wails injects the generated Go bindings into this namespace at runtime. */
    go?: unknown
    /** Wails runtime events are available when the app is hosted by Wails. */
    runtime?: unknown
  }
}

export function detectBackendConnection(): BackendConnection {
  if (typeof window !== 'undefined' && (window.go || window.runtime)) {
    return {
      status: 'connected',
      label: 'Backend connected',
      detail: 'Wails runtime is ready',
    }
  }

  return {
    status: 'offline',
    label: 'Backend offline',
    detail: 'Запущен только UI shell',
  }
}
