// End-to-end acceptance of a daemon (docs/integrations/daemons.md "Tests") against a real `personad node`: the echo example as a
// daemon (the SDK's listen mode), dialed by the node from daemons.json; the script plays the hub. A daemon's own e2e
// copies this and starts its service instead of the echo daemon.
//
//   PERSONAD=<personad binary> node integrations/runtime-sdk/e2e/daemon.mjs

import { randomUUID } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createEchoRuntime } from '../examples/echo-runtime.mjs'
import { Peer } from '../lib/wire.js'
import { createConnection } from 'node:net'
import { checks, startNode, startResident, until } from '../testing/node-harness.mjs'

const personad = process.env.PERSONAD
if (!personad) {
  console.error('set PERSONAD (a personad binary)')
  process.exit(2)
}
const { check, summary } = checks()
const TOKEN = `t-${randomUUID()}`
const root = mkdtempSync(join('/tmp', 'jdm-e2e-'))

/** The echo example as a daemon on port (0: any); counts the connections the node opens. */
async function startDaemon(port = 0) {
  const d = { dials: 0 }
  const { runtime } = createEchoRuntime({ answerFirst: 'phone', refreshMs: 0,
    listen: { port, token: () => { d.dials++; return TOKEN } } })
  d.runtime = runtime
  d.addr = await runtime.start()
  d.url = `ws://127.0.0.1:${d.addr.port}/persona/runtime`
  return d
}

function writeConfig(home, daemons) {
  mkdirSync(join(home, '.persona'), { recursive: true })
  writeFileSync(join(home, '.persona', 'daemons.json'), JSON.stringify({ daemons }, null, 2), { mode: 0o600 })
}

const sessions = async (node) => (await node.call('sessions.list')).sessions ?? []
const launchers = async (node) => (await node.call('launchers.list')).launchers ?? []

/** Say hello on the node's runtime socket as a runtime called id; the error code, or 'ok'. */
async function helloOnSocket(path, id) {
  const sock = createConnection(path)
  await new Promise((res, rej) => { sock.once('connect', res); sock.once('error', rej) })
  const p = new Peer(sock, { prefix: 'x', onRequest: async () => ({ ok: true }) })
  try {
    await p.call('runtime.hello', { runtime: id, caps: {} }, { timeoutMs: 5000 })
    return 'ok'
  } catch (err) {
    return err.code ?? err.message
  } finally {
    p.close()
  }
}

let daemon, node, local
const cleanups = []
try {
  // ---- a node serving its hub connection itself ----
  const home = join(root, 'home')
  writeFileSync(join(root, 'token'), `${TOKEN}\n`, { mode: 0o600 })
  daemon = await startDaemon()
  writeConfig(home, [
    { id: 'acme', name: 'Acme Echo', url: daemon.url, token_file: join(root, 'token') },
    { id: 'wrong', url: daemon.url, token: 'not-the-token-QX' },
  ])
  node = await startNode({ personad, home })
  cleanups.push(() => node.stop())

  const ls = await until('the daemon\'s launcher', async () => {
    const l = await launchers(node)
    return l.find((x) => x.name === 'acme') ? l : undefined
  }, 20_000)
  check('the node dials the daemon; its launcher is named by the config, not by the daemon', ls.some((l) => l.source === 'acme') && !ls.some((l) => l.name === 'echo'), JSON.stringify(ls.map((l) => l.name)))
  const { session } = await node.call('session.start', { launcher: 'acme', cwd: '/tmp' })
  const sid = session.session_id
  check('a session started from the phone gets the daemon\'s namespace', /^acme\.echo-/.test(sid), sid)
  // The daemon's own list (with its space) follows the row its start answered with.
  const row = await until('listed', async () => (await sessions(node)).find((s) => s.session_id === sid && s.space_id))
  check('its row: the config\'s name, the daemon as source, its own space', row.agent === 'acme' && row.agent_label === 'Acme Echo' && row.space_kind === 'acme'
    && row.source_kind === 'runtime' && row.source_instance_id === 'acme' && row.space_id === 'acme:/tmp' && !row.pane_id, JSON.stringify(row))
  check('the declared capabilities, nothing of a terminal', row.capabilities?.send === true && row.capabilities?.interrupt === true && !row.capabilities?.terminal && !row.capabilities?.attach)

  await node.call('session.watch', { session_id: sid, since_seq: 0 })
  node.resolve = (ix) => (ix.kind === 'permission' ? { allow: true } : { dismiss: true })
  const id = randomUUID()
  await node.call('session.send', { session_id: sid, text: 'ask then echo', client_msg_id: id })
  await node.call('session.send', { session_id: sid, text: 'ask then echo', client_msg_id: id }) // an app retry
  await until('the echo', () => node.items(sid).some((i) => i.text === 'echo: ask then echo'), 15_000)
  check('a message from the phone reaches the daemon and its reply comes back', true)
  check('a retried send is delivered once', node.items(sid).filter((i) => i.role === 'user').length === 1)
  const card = node.asked.find((i) => i.kind === 'permission' && i.session_id === sid)
  check('the permission card reaches the phone under the config\'s name and the hub\'s ids', card?.agent === 'acme' && card?.agent_label === 'Acme Echo' && /^dx_/.test(card?.id ?? ''), JSON.stringify({ id: card?.id, agent: card?.agent, label: card?.agent_label }))
  check('live text arrives as deltas', node.deltas(sid).some((t) => t.length > 0 && 'echo: ask then echo'.startsWith(t)))
  const page = await node.call('session.items', { session_id: sid, turns: 1 })
  check('history pages through the node with the daemon\'s own ids inside', page.items.length === 2 && page.history === `echo:${sid.slice('acme.'.length)}`, String(page.history))
  await until('done', async () => (await sessions(node)).find((s) => s.session_id === sid)?.status === 'done', 5000).catch(() => {})
  check('the turn ends as done', (await sessions(node)).find((s) => s.session_id === sid)?.status === 'done')
  check('pushed batches are continuous', node.gaps(sid).length === 0, `${node.batches(sid).length} batches`)
  await node.call('session.interrupt', { session_id: sid })
  check('an interrupt reaches the daemon', true)
  const keys = await node.call('session.keys', { session_id: sid, keys: ['enter'] }).then(() => 'ok', (e) => e.message)
  check('terminal calls are refused as unsupported', /^unsupported/.test(keys), keys)

  // ---- names do not collide ----
  check('a runtime on the socket cannot take a daemon\'s id', (await helloOnSocket(node.runtimeSocket, 'acme')) === 'runtime_conflict')
  local = createEchoRuntime({ socketPath: node.runtimeSocket, retryMs: 100, answerFirst: 'phone' }).runtime
  local.start()
  const both = await until('both launchers', async () => {
    const l = await launchers(node)
    return l.some((x) => x.name === 'echo') && l.some((x) => x.name === 'acme') ? l : undefined
  }, 20_000)
  const { session: localSession } = await node.call('session.start', { launcher: 'echo', cwd: '/tmp' })
  check('the same runtime on the socket and as a daemon live side by side', both.length >= 2 && !localSession.session_id.startsWith('acme.') && localSession.session_id !== sid)

  // ---- a wrong token ----
  await until('the refusal in the log', () => node.log.join('').includes('daemon wrong') && node.log.join('').includes('refused the token'), 20_000)
  check('a wrong token is refused, and logged without the token', !node.log.join('').includes('not-the-token-QX'))

  // ---- the config changes while the node runs ----
  writeConfig(home, [{ id: 'acme', name: 'Acme Renamed', url: daemon.url, token_file: join(root, 'token') }])
  await until('renamed', async () => (await sessions(node)).find((s) => s.session_id === sid)?.agent_label === 'Acme Renamed', 30_000)
  check('a changed config takes effect without a restart', true)

  // ---- the daemon restarts ----
  const port = daemon.addr.port
  daemon.runtime.stop()
  await until('its sessions leave', async () => !(await sessions(node)).some((s) => s.session_id.startsWith('acme.')), 15_000)
  check('when the daemon goes away its sessions leave', true)
  daemon = await startDaemon(port)
  await until('dialed again', async () => (await launchers(node)).some((l) => l.name === 'acme'), 30_000)
  check('a restarted daemon is dialed again', daemon.dials >= 1)

  // ---- removed from the config ----
  writeConfig(home, [])
  await until('the launcher gone', async () => !(await launchers(node)).some((l) => l.name === 'acme'), 30_000)
  check('a daemon removed from the config is disconnected', true)
  check('its id is free again', (await helloOnSocket(node.runtimeSocket, 'acme')) === 'ok')
  node.stop()
  cleanups.length = 0

  // ---- a resident node: the daemon stays while the hub reconnects ----
  const rhome = join(root, 'resident')
  writeConfig(rhome, [{ id: 'acme', name: 'Acme Echo', url: daemon.url, token_file: join(root, 'token') }])
  const dialsBefore = daemon.dials
  const resident = await startResident({ personad, home: rhome })
  cleanups.push(() => resident.stop())
  const a = await startNode({ personad, home: rhome })
  await until('listed through the resident node', async () => (await launchers(a)).some((l) => l.name === 'acme'), 20_000)
  const { session: rs } = await a.call('session.start', { launcher: 'acme', cwd: '/tmp' })
  a.stop()
  const b = await startNode({ personad, home: rhome })
  cleanups.push(() => b.stop())
  const again = await until('listed on the next hub connection', async () => (await sessions(b)).find((s) => s.session_id === rs.session_id), 20_000)
  check('a resident node keeps the daemon while the hub reconnects', again && daemon.dials - dialsBefore === 1, `dials ${daemon.dials - dialsBefore}`)
} catch (err) {
  check('run', false, err.message)
  if (node) console.log('--- node log ---\n' + node.log.join('').slice(-3000))
} finally {
  for (const c of cleanups.reverse()) await c()
  local?.stop()
  daemon?.runtime.stop()
  rmSync(root, { recursive: true, force: true })
}
process.exit(summary() ? 1 : 0)
