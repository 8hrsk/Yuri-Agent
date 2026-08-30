/**
 * Shared `Intl` formatters.
 *
 * `new Intl.DateTimeFormat(...)` is one of the most expensive constructors in
 * the standard library: it resolves locale data on every call. Building one per
 * list item per render put it on the streaming hot path — in chat that was
 * (N messages + M conversations) constructions per token. These module-level
 * instances are built once and shared by every view.
 *
 * The locale and option bags are identical to the per-call ones they replaced,
 * so the rendered strings are unchanged.
 */

const dateTimeFormatter = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short' })
const clockFormatter = new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' })

/** Medium date + short time, e.g. `29 авг. 2026 г., 13:05`. */
export function formatDateTime(date: Date): string {
  return dateTimeFormatter.format(date)
}

/** Two-digit hour and minute, e.g. `13:05`. */
export function formatClock(date: Date): string {
  return clockFormatter.format(date)
}
