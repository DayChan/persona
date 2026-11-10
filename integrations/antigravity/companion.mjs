#!/usr/bin/env node
// Antigravity (agy) companion runtime for Persona.
// Connects to ~/.persona/runtime.sock, exposes agy sessions & ACP runtime for both Agents tab and Persona Assistant.

import { parseArgs } from 'node:util'
import { createAgyRuntime } from './lib/runtime.js'

const { values } = parseArgs({
  options: {
    socket: { type: 'string' },
    'agy-dir': { type: 'string' },
    'agy-bin': { type: 'string' },
  },
  strict: false,
})

const log = (msg) => process.stderr.write(`persona-agy: ${msg}\n`)
const runtime = createAgyRuntime({
  socketPath: values.socket || undefined,
  agyDir: values['agy-dir'] || undefined,
  agyBin: values['agy-bin'] || undefined,
  log,
})

runtime.start()
const alive = setInterval(() => {}, 1 << 30)
log('started Antigravity (agy) runtime for Persona')

for (const sig of ['SIGTERM', 'SIGINT']) {
  process.on(sig, () => {
    clearInterval(alive)
    // Exit once agy is gone (killed after a short grace when it ignores SIGTERM), so it does not outlive the companion.
    runtime.stop().finally(() => process.exit(0))
  })
}
