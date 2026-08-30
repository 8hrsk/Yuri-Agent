// Cross-file duplicate-selector check for the barrel stylesheet.
//
// `stylelint --rule no-duplicate-selectors` runs per file, so it cannot see the
// case that actually bit us: `.toggle` declared once in view-settings.css and
// again in view-persona.css, with the persona copy silently winning on every
// property. This script rebuilds the sheet the way the browser does — the
// @import list in styles/global.css, in order — and lints the concatenation, so
// a selector duplicated across two files is reported like any other duplicate.
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import stylelint from 'stylelint'

const stylesDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../src/styles')
const barrel = await readFile(path.join(stylesDir, 'global.css'), 'utf8')

const imports = [...barrel.matchAll(/@import\s+(?:url\()?["']([^"']+)["']\)?\s*;/g)].map((m) => m[1])
if (imports.length === 0) {
  console.error('lint-css-cascade: no @import found in styles/global.css')
  process.exit(1)
}

// Offset table so a warning on the concatenation can be reported against the
// file and line a human can actually go and edit.
const parts = []
const offsets = []
let line = 1
for (const spec of imports) {
  const file = path.resolve(stylesDir, spec)
  const text = await readFile(file, 'utf8')
  offsets.push({ file: path.relative(path.resolve(stylesDir, '../..'), file), start: line })
  parts.push(text)
  line += text.split('\n').length - 1
}

const locate = (n) => {
  let hit = offsets[0]
  for (const o of offsets) if (o.start <= n) hit = o
  return `${hit.file}:${n - hit.start + 1}`
}

const { results } = await stylelint.lint({
  code: parts.join(''),
  codeFilename: path.join(stylesDir, 'global.css'),
  config: { rules: { 'no-duplicate-selectors': true } },
})

const warnings = results.flatMap((r) => r.warnings)
if (warnings.length === 0) {
  console.log(`lint-css-cascade: ${imports.length} files, no selector declared twice across the cascade`)
  process.exit(0)
}

console.error('lint-css-cascade: selectors declared more than once across the barrel')
for (const w of warnings) {
  console.error(`  ${locate(w.line)}  ${w.text.replace(/first used at line (\d+)/, (_, n) => `first used at ${locate(Number(n))}`)}`)
}
console.error(
  '\nA selector declared in two files renders as whichever copy the barrel imports last.\n' +
    'Delete the losing copy (check first that its property set is a subset of the winner)\n' +
    'or give the two rules distinct selectors.',
)
process.exit(1)
