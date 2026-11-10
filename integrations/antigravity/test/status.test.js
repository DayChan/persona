import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { transcriptStatus } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const ID = '11111111-2222-3333-4444-555555555555'
const SESSION = `agy-${ID}`
const NOW = Date.parse('2026-01-02T12:00:00Z')
const at = '2026-01-02T11:59:00Z'
const step = (type, fields = {}) => ({ type, status: 'DONE', created_at: at, ...fields })

test('native transcript stages distinguish external work from completed replies', () => {
  const cases = [
    ['user message', [step('USER_INPUT')], 'working'],
    ['streaming response', [step('PLANNER_RESPONSE', { status: 'RUNNING', content: 'Reading' })], 'working'],
    ['tool request', [step('PLANNER_RESPONSE', { content: 'Running tests', tool_calls: [{ name: 'run_command' }] })], 'working'],
    ['completed tool, next model step pending', [step('GENERIC', { content: 'Tests passed' })], 'working'],
    ['final reply', [step('PLANNER_RESPONSE', { content: 'Finished' })], 'done'],
    ['metadata after final reply', [step('PLANNER_RESPONSE', { content: 'Finished' }), step('CHECKPOINT')], 'done'],
    ['error', [step('ERROR_MESSAGE')], 'idle'],
    ['interrupted step', [step('GENERIC', { status: 'CANCELLED' })], 'idle'],
    ['empty history', [], 'unknown'],
    ['unfinished history without a time', [step('GENERIC', { created_at: undefined })], 'unknown'],
    ['stale unfinished history', [step('USER_INPUT', { created_at: '2025-12-01T00:00:00Z' })], 'unknown'],
    ['old completed reply', [step('PLANNER_RESPONSE', { content: 'Finished', created_at: '2025-12-01T00:00:00Z' })], 'done'],
  ]
  for (const [name, steps, expected] of cases) {
    assert.equal(transcriptStatus(steps, NOW), expected, name)
  }
})

async function fixture(t, steps) {
  const home = mkdtempSync(join(tmpdir(), 'persona-agy-status-'))
  const agyDir = join(home, 'agy')
  const logs = join(agyDir, 'brain', ID, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const transcript = join(logs, 'transcript.jsonl')
  const write = (values) => writeFileSync(transcript, values.map((value, index) => JSON.stringify({ step_index: index, ...value })).join('\n') + '\n')
  write(steps)
  const binary = join(home, 'fake-agy.mjs')
  writeFileSync(binary, `#!/usr/bin/env node
import { createInterface } from 'node:readline'
createInterface({ input: process.stdin }).on('line', (line) => {
  const request = JSON.parse(line)
  if (request.id === undefined || request.method === 'session/prompt') return
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result: {} }) + '\\n')
})
`, { mode: 0o700 })
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, socketPath: node.path, log: () => {} })
  t.after(async () => {
    await runtime.stop()
    rmSync(home, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready
  await until('native conversation listed', () => node.last('runtime.sessions')?.sessions.length)
  return { node, write }
}

test('runtime inventory follows an external tool turn through its final reply', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node, write } = await fixture(t, [step('GENERIC', { created_at, content: 'Tool output' })])
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'working')
  write([step('PLANNER_RESPONSE', { created_at, content: 'Finished' })])
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
  await until('external turn completed', () => node.last('runtime.sessions')?.sessions[0]?.status === 'done')
})

test('owned ACP turn stays working while the native transcript has the previous reply', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { created_at, content: 'Previous reply' })])
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'done')
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue', client_msg_id: 'owned-turn' })
  await until('owned ACP turn running', () => node.last('runtime.sessions')?.sessions[0]?.status === 'working')
})
