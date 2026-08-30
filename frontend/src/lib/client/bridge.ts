import type { BridgeMethod, UnknownRecord } from './primitives'

function getNested(root: unknown, path: string[]): unknown {
  let value: unknown = root
  for (const key of path) {
    if (!value || typeof value !== 'object') return undefined
    value = (value as UnknownRecord)[key]
  }
  return value
}

function findBridgeMethod(names: string[]): BridgeMethod | undefined {
  if (typeof window === 'undefined') return undefined

  const candidates = [
    ['go', 'main', 'Bridge'],
    ['go', 'desktop', 'Bridge'],
    ['go', 'app', 'Bridge'],
    ['go', 'Bridge'],
  ]

  for (const path of candidates) {
    const bridge = getNested(window as unknown as UnknownRecord, path)
    if (!bridge || typeof bridge !== 'object') continue

    for (const name of names) {
      const method = (bridge as UnknownRecord)[name]
      if (typeof method === 'function') return method as BridgeMethod
    }
  }

  return undefined
}

function findRuntimeMethod(name: string): BridgeMethod | undefined {
  if (typeof window === 'undefined') return undefined
  const runtime = getNested(window as unknown as UnknownRecord, ['runtime'])
  if (!runtime || typeof runtime !== 'object') return undefined
  const method = (runtime as UnknownRecord)[name]
  return typeof method === 'function' ? method as BridgeMethod : undefined
}

/**
 * Registers one listener on the Wails event bus and returns a cleanup that
 * removes only that listener.
 *
 * `EventsOff(name)` drops the whole event registration, so using it to clean up
 * a single subscription silences every other listener on the same name — two
 * concurrent chat runs used to kill each other's stream that way. Wails v2
 * hands back a per-listener unregister function from `EventsOn`; that is the
 * only correct cleanup. The callback is additionally wrapped in an `active`
 * guard so a runtime that returns nothing still ends up with an inert listener
 * instead of a live one delivering into a released subscription.
 */
function subscribeRuntimeEvent(name: string, callback: (value: unknown) => void): (() => void) | undefined {
  const on = findRuntimeMethod('EventsOn')
  if (!on) return undefined
  let active = true
  const unregister = on(name, (value: unknown) => {
    if (active) callback(value)
  })
  return () => {
    if (!active) return
    active = false
    if (typeof unregister === 'function') void (unregister as () => void)()
  }
}

async function callBridge<T>(names: string[], args: unknown[] = []): Promise<T | undefined> {
  const method = findBridgeMethod(names)
  if (!method) return undefined
  return await method(...args) as T
}

async function callBridgeSafe<T>(names: string[], args: unknown[] = []): Promise<T | undefined> {
  try {
    return await callBridge<T>(names, args)
  } catch {
    return undefined
  }
}

export { callBridge, callBridgeSafe, findBridgeMethod, subscribeRuntimeEvent }
