#!/usr/bin/env node

import { readFile, writeFile } from 'node:fs/promises'

const [configPath, version] = process.argv.slice(2)
if (!configPath || !/^[0-9]+\.[0-9]+\.[0-9]+$/.test(version ?? '')) {
  console.error('usage: set-wails-version.mjs WAILS_JSON X.Y.Z')
  process.exit(2)
}

const config = JSON.parse(await readFile(configPath, 'utf8'))
if (!config.info || typeof config.info !== 'object') {
  throw new Error(`${configPath} does not contain an info object`)
}
config.info.productVersion = version
await writeFile(configPath, `${JSON.stringify(config, null, 2)}\n`, 'utf8')
