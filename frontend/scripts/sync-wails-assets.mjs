import { cp, mkdir, rm, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const frontendRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const source = path.join(frontendRoot, 'dist')
const target = path.resolve(frontendRoot, '../cmd/yuri/frontend/dist')

await rm(target, { recursive: true, force: true })
await mkdir(target, { recursive: true })
await cp(source, target, { recursive: true })
await writeFile(path.join(target, '.gitkeep'), '')
