// Agreement check between the Go `desktop.Bridge` and the renderer's
// hand-written client layer.
//
// The renderer never imports the Wails-generated `wailsjs/go/desktop/Bridge.d.ts`.
// It reaches the backend through `src/lib/client/wails-client.ts`, which looks
// methods up *by name string* at runtime:
//
//     callBridge(['SetActiveAgent', 'SelectAgent'], [agentId])
//
// `findBridgeMethod` returns `undefined` when no name in the list is bound, and
// `callBridge` then resolves to `undefined` rather than throwing. So renaming a
// Go method, or calling a real one with the wrong number of arguments, produces
// a silently dead call path that no compiler, test, or `tsc` run can see. That
// is the gap this script closes.
//
// The Go source is the oracle, deliberately, and not the generated `.d.ts`:
// `frontend/wailsjs/` is gitignored (.gitignore:17) and the copy in a working
// tree goes stale between `wails build` runs. At the time of writing the local
// copy was missing five bound methods and still declared the pre-rename
// `ProbeProvider(ProviderSettingsInput) ProviderTestResult`. A check anchored to
// it would have validated the renderer against a fossil.
//
// TIERED ADOPTION, same as the `.golangci.yml` linter set: the BLOCKING tier
// below is green today, so wiring it into CI is meaningful from the first run
// instead of being permanently red and routed around. The REPORT tier prints
// its findings and exits 0.
//
// Staged for promotion to blocking, with the current count and what must land
// first:
//
//   vocabulary   10 divergences — `PersonaTraitId` / `AffectEmotion` versus the
//                  seeds the Bridge actually emits. Reserved as a PRODUCT
//                  decision for the owner (which trait set is correct is not a
//                  question this script may answer), so it must not gate CI
//                  until that decision is made. See the REPORT tier output.
//   dto-fields    6 fields      — json tags on `*View`/`*DTO` structs that no
//                  renderer file mentions (LoginView.loginId,
//                  OnboardingView.providerConfigured, PluginDTO.runtime_status,
//                  PluginPackageInspection.executable_path,
//                  ProviderTestResult.providerId, Status.platform), plus the
//                  5-field PluginPublisherKeyDTO whose UI does not exist yet.
//                  Not implemented here: the normalizers accept several
//                  spellings per field on purpose (`apiKeyConfigured ??
//                  api_key_configured ?? hasSecret`), so a field-name equality
//                  check would report that intentional leniency as drift.
import { readFile, readdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const frontendDir = path.resolve(scriptDir, '..')
const repoDir = path.resolve(frontendDir, '..')
const desktopDir = path.join(repoDir, 'internal/desktop')
const clientFile = path.join(frontendDir, 'src/lib/client/wails-client.ts')
const personaContract = path.join(frontendDir, 'src/lib/contracts/persona.ts')
const personalityGo = path.join(desktopDir, 'personality.go')

const rel = (p) => path.relative(repoDir, p)

// Bridge methods Wails does not bind as callable IPC: they are the app
// lifecycle hooks Wails invokes itself.
const LIFECYCLE_METHODS = new Set(['Startup', 'Shutdown'])

// Call sites whose candidate list resolves to nothing on today's Bridge.
// Empty: every renderer call site resolves to a bound Bridge method. Add an
// entry only with the reason and the follow-up, and delete it once fixed.
const KNOWN_UNRESOLVED = new Map()

// ---------------------------------------------------------------- Go parsing

function splitTopLevel(text) {
  const out = []
  let depth = 0
  let cur = ''
  for (const ch of text) {
    if (ch === '(' || ch === '[' || ch === '{') depth += 1
    else if (ch === ')' || ch === ']' || ch === '}') depth -= 1
    if (ch === ',' && depth === 0) {
      out.push(cur)
      cur = ''
      continue
    }
    cur += ch
  }
  if (cur.trim()) out.push(cur)
  return out
}

async function goFiles(dir) {
  const entries = await readdir(dir, { withFileTypes: true })
  return entries
    .filter((e) => e.isFile() && e.name.endsWith('.go') && !e.name.endsWith('_test.go'))
    .map((e) => path.join(dir, e.name))
}

async function readBridgeMethods() {
  const methods = new Map()
  for (const file of await goFiles(desktopDir)) {
    const src = await readFile(file, 'utf8')
    const pattern = /^func \(\w+ \*Bridge\) ([A-Z]\w*)\(([^)]*)\)/gm
    for (const m of src.matchAll(pattern)) {
      const [, name, params] = m
      if (LIFECYCLE_METHODS.has(name)) continue
      methods.set(name, {
        // `a, b string` is two parameters, so counting top-level separators is
        // correct for grouped declarations as well as one-type-each ones.
        arity: params.trim() === '' ? 0 : splitTopLevel(params).length,
        where: `${rel(file)}:${src.slice(0, m.index).split('\n').length}`,
      })
    }
  }
  return methods
}

// ---------------------------------------------------------------- TS parsing

function matchBracket(src, start, open, close) {
  let depth = 0
  for (let i = start; i < src.length; i += 1) {
    if (src[i] === open) depth += 1
    else if (src[i] === close) {
      depth -= 1
      if (depth === 0) return i
    }
  }
  return -1
}

function readNameList(text) {
  const trimmed = text.trim()
  if (!trimmed.startsWith('[')) return undefined
  const names = splitTopLevel(trimmed.slice(1, -1)).map((s) => s.trim().replace(/^['"]|['"]$/g, ''))
  if (names.length === 0) return undefined
  // A candidate list is always literal method names. Anything else (a computed
  // key, a spread) is not a bridge lookup this script can reason about.
  if (!names.every((n) => /^[A-Z]\w*$/.test(n))) return undefined
  return names
}

function readCallSites(src) {
  const sites = []
  // `runWithBridge` takes the request and the event sink positionally rather
  // than as an argument array, so its arity is not statically comparable;
  // `findBridgeMethod` hands the caller the raw function and takes no args at
  // all. Both are still checked for name resolution.
  const pattern = /\b(callBridgeSafe|callBridge|findBridgeMethod|runWithBridge)\s*(?:<[\s\S]*?>)?\s*\(/g
  for (const m of src.matchAll(pattern)) {
    const openParen = m.index + m[0].length - 1
    const closeParen = matchBracket(src, openParen, '(', ')')
    if (closeParen === -1) continue
    const args = splitTopLevel(src.slice(openParen + 1, closeParen))
    if (args.length === 0) continue
    const names = readNameList(args[0])
    if (!names) continue

    let arity
    if (m[1] === 'callBridge' || m[1] === 'callBridgeSafe') {
      // `callBridge(names)` defaults the argument array to `[]`.
      if (args.length === 1) arity = 0
      else {
        const second = args[1].trim()
        if (second.startsWith('[')) arity = splitTopLevel(second.slice(1, -1)).length
      }
    }

    sites.push({
      fn: m[1],
      names,
      arity,
      line: src.slice(0, m.index).split('\n').length,
    })
  }
  return sites
}

// -------------------------------------------------------- vocabulary parsing

function goStringKeys(src, funcName) {
  const at = src.indexOf(`func ${funcName}()`)
  if (at === -1) return undefined
  const open = src.indexOf('{', src.indexOf('return', at))
  const close = matchBracket(src, open, '{', '}')
  if (open === -1 || close === -1) return undefined
  return new Set([...src.slice(open, close).matchAll(/"([a-z_]+)":/g)].map((m) => m[1]))
}

function tsUnionMembers(src, alias) {
  const at = src.indexOf(`export type ${alias} =`)
  if (at === -1) return undefined
  const end = src.indexOf('\n\n', at)
  return new Set([...src.slice(at, end === -1 ? undefined : end).matchAll(/'([^']+)'/g)].map((m) => m[1]))
}

// ------------------------------------------------------------------- checks

const methods = await readBridgeMethods()
const clientSrc = await readFile(clientFile, 'utf8')
const sites = readCallSites(clientSrc)

if (methods.size === 0 || sites.length === 0) {
  console.error(
    `check-bridge-contract: parsed ${methods.size} Bridge methods and ${sites.length} call sites; ` +
      'one side came back empty, so the check would pass vacuously.',
  )
  process.exit(1)
}

const failures = []
let checkedArity = 0

for (const site of sites) {
  const where = `${rel(clientFile)}:${site.line}`
  const resolved = site.names.find((n) => methods.has(n))

  if (!resolved) {
    const excuse = KNOWN_UNRESOLVED.get(site.names.join('|'))
    if (!excuse) {
      failures.push(
        `${where}  ${site.fn}([${site.names.join(', ')}])\n` +
          '      no name in this list is an exported method on desktop.Bridge, so the call ' +
          'resolves to undefined at runtime',
      )
    }
    continue
  }

  if (site.arity === undefined) continue
  checkedArity += 1
  const expected = methods.get(resolved).arity
  if (site.arity !== expected) {
    failures.push(
      `${where}  ${resolved} called with ${site.arity} argument(s), Bridge declares ${expected}\n` +
        `      ${methods.get(resolved).where}`,
    )
  }
}

// Go methods the renderer never names at all. Reported, never fatal: a bound
// method with no UI yet is a normal state for this app.
const named = new Set(sites.flatMap((s) => s.names))
const unreferenced = [...methods.keys()].filter((n) => !named.has(n)).sort()

if (failures.length > 0) {
  console.error('check-bridge-contract: renderer and desktop.Bridge disagree\n')
  for (const f of failures) console.error(`  ${f}\n`)
  console.error(
    'The renderer resolves Bridge methods by name at runtime and swallows a miss,\n' +
      'so neither tsc nor the test suite can see these. Fix the name or the argument\n' +
      'list in src/lib/client/wails-client.ts, or rename the Go method back.\n',
  )
  process.exit(1)
}

console.log(
  `check-bridge-contract: ${methods.size} Bridge methods, ${sites.length} call sites, ` +
    `${checkedArity} arities verified — no disagreement`,
)
if (KNOWN_UNRESOLVED.size > 0) {
  console.log(`  ${KNOWN_UNRESOLVED.size} allowlisted unresolved call site(s); see KNOWN_UNRESOLVED`)
}
if (unreferenced.length > 0) {
  console.log(`  ${unreferenced.length} bound method(s) with no renderer caller: ${unreferenced.join(', ')}`)
}

// ------------------------------------------------------- REPORT tier (exit 0)
//
// `PersonaTraitId` and `AffectEmotion` are the only renderer string unions worth
// comparing to Go by set membership. Every other union in src/lib/contracts is a
// UI vocabulary produced by a total normalizer — `normalizeJobRunStatus` folds
// Go's `succeeded` into `completed` and anything unrecognized into `unknown`, and
// `ScheduleStatus` carries an `error` member Go never emits — so equality against
// a Go const block would be a category error, not a drift report.
//
// The comparison is against internal/desktop/personality.go, because that is what
// the Bridge emits. internal/domain/persona.go's `CommonPersonaTraits` is
// documented as "the stable set used by the default seed" with custom names
// explicitly allowed; it is not the wire vocabulary.

const personalitySrc = await readFile(personalityGo, 'utf8')
const personaSrc = await readFile(personaContract, 'utf8')

const vocabularies = [
  ['PersonaTraitId', tsUnionMembers(personaSrc, 'PersonaTraitId'), goStringKeys(personalitySrc, 'defaultPersonaTraits'), 'defaultPersonaTraits()'],
  ['AffectEmotion', tsUnionMembers(personaSrc, 'AffectEmotion'), goStringKeys(personalitySrc, 'defaultAffectDimensions'), 'defaultAffectDimensions()'],
]

let divergences = 0
const lines = []
for (const [alias, ts, go, goName] of vocabularies) {
  if (!ts || !go) {
    console.error(`check-bridge-contract: could not read vocabulary for ${alias}; the parser needs updating`)
    process.exit(1)
  }
  const rendererOnly = [...ts].filter((v) => !go.has(v)).sort()
  const backendOnly = [...go].filter((v) => !ts.has(v)).sort()
  divergences += rendererOnly.length + backendOnly.length
  if (rendererOnly.length === 0 && backendOnly.length === 0) {
    lines.push(`  ${alias}: agrees with ${goName} (${ts.size} members)`)
    continue
  }
  lines.push(`  ${alias} vs ${rel(personalityGo)} ${goName}:`)
  if (rendererOnly.length > 0) lines.push(`      renderer lists, backend never emits: ${rendererOnly.join(', ')}`)
  if (backendOnly.length > 0) lines.push(`      backend emits, renderer omits:       ${backendOnly.join(', ')}`)
}

console.log(`\ncheck-bridge-contract [report tier]: ${divergences} vocabulary divergence(s)`)
for (const l of lines) console.log(l)
if (divergences > 0) {
  console.log(
    '\n  Not fatal, and not for this script to reconcile: which trait set is correct\n' +
      '  is a product decision reserved for the owner. Both unions are open\n' +
      "  (`| (string & {})`) and PersonaTraitView carries its own Label from Go, so\n" +
      '  an unlisted trait still type-checks and still renders with the right label.\n' +
      '  Promote this tier to blocking once the intended vocabulary is settled.',
  )
}
