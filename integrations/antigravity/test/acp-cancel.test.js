import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { AgyAcpClient } from '../lib/acp.js'
import { toPersonaId } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-cancel.mjs', import.meta.url))
const SESSION = '0c7d2c4e-5b1a-4f3e-9a6d-2f8b1c3d4e5f' // the session the fake agent opens
const SLOW = 15_000 // spawning a node process can be slow on a busy machine

/** A temp dir with the fake's record file; afterwards every fake it started is killed and the dir removed. */
function scratch(t) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-cancel-'))
  const record = join(dir, 'record.jsonl')
  writeFileSync(record, '')
  t.after(() => {
    for (const m of received(record)) {
      if (m.started) {
        try {
          process.kill(m.started, 'SIGKILL')
        } catch {}
      }
    }
    rmSync(dir, { recursive: true, force: true })
  })
  return { dir, record }
}

/** What the fake agent received, in order. */
function received(record) {
  return readFileSync(record, 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line))
}

/** The answers to the fake's session/request_permission requests. */
function permissionAnswers(record) {
  return received(record).filter((m) => typeof m.id === 'string' && m.id.startsWith('perm-'))
}

const cancelled = { outcome: { outcome: 'cancelled' } }

test('cancel is an ACP notification: the running prompt ends with stopReason cancelled', async (t) => {
  const { record } = scratch(t)
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'] })
  t.after(() => acp.stop())
  await acp.ensureStarted()
  const prompt = acp.prompt(SESSION, [{ type: 'text', text: 'hold' }])
  await until('the prompt reaches the agent', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await acp.cancel(SESSION)
  assert.deepEqual(await prompt, { stopReason: 'cancelled' })
  const cancels = received(record).filter((m) => m.method === 'session/cancel')
  assert.equal(cancels.length, 1)
  assert.equal('id' in cancels[0], false, 'session/cancel carries no id')
  assert.deepEqual(cancels[0].params, { sessionId: SESSION })
})

test('cancel with no agy running starts none and resolves', async (t) => {
  const { record } = scratch(t)
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'] })
  t.after(() => acp.stop())
  await acp.cancel(SESSION)
  assert.equal(acp.proc, null)
  assert.deepEqual(received(record), [], 'no agy process was started')
})

/** The agy runtime on a fake node, with an executable fake agy; nobody answers a card on the phone. */
async function agyRuntime(t) {
  const { dir, record } = scratch(t)
  const agyBin = join(dir, 'agy')
  writeFileSync(agyBin, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' --record '${record}' "$@"\n`)
  chmodSync(agyBin, 0o755)
  // agy keeps each conversation in brain/<id>: the runtime lists the sessions it finds there.
  const agyDir = join(dir, 'config')
  mkdirSync(join(agyDir, 'brain', SESSION), { recursive: true })
  const n = await startFakeNode(t)
  n.resolve = () => new Promise(() => {})
  const runtime = createAgyRuntime({ agyBin, agyDir, home: dir, socketPath: n.path, log: () => {} })
  t.after(() => runtime.stop())
  runtime.start()
  await n.ready
  const start = async (prompt) => {
    const { session } = await n.peer.call('session.start', { cwd: dir, prompt })
    assert.equal(session.session_id, toPersonaId(SESSION))
    return session.session_id
  }
  const status = (pid) => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)?.status
  return { n, record, start, status }
}

test('interrupt withdraws the permission card, answers agy cancelled and ends the turn', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask')
  await until('the card is on the phone', () => n.asked.length === 1, SLOW)
  assert.equal(n.asked[0].kind, 'permission')
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('the card is withdrawn', () => n.last('interaction.cancel')?.id === n.asked[0].id, SLOW)
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  const cancels = received(record).filter((m) => m.method === 'session/cancel')
  assert.equal(cancels.length, 1)
  assert.equal('id' in cancels[0], false, 'session/cancel carries no id')
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})

test('a permission request that crosses the interrupt is answered cancelled without a card', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask-on-cancel')
  await until('the prompt reaches the agent', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  await until('the turn ends', () => status(pid) === 'done', SLOW)
  assert.equal(n.asked.length, 0, 'no card went to the phone')
})

test('a card still out when the turn ends is withdrawn and agy hears cancelled', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask-then-end')
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  await until('the card is withdrawn', () => n.last('interaction.cancel'), SLOW)
  for (const ix of n.asked) assert.ok(n.all('interaction.cancel').some((c) => c.id === ix.id), 'every card that went out is taken off')
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})

test('an answered card still selects the matching agy option', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  n.resolve = () => ({ allow: true })
  const pid = await start('ask')
  await until('agy hears the answer', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'allow' } })
  assert.equal(n.all('interaction.cancel').length, 0, 'an answered card is not withdrawn')
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})
