import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash, randomBytes } from 'node:crypto'
import { createServer, request } from 'node:http'
import { connect } from 'node:net'
import { spawn } from 'node:child_process'
import { EventEmitter, once } from 'node:events'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRuntime, Peer, SUBPROTOCOL } from '../index.js'
import { WsConnection } from '../lib/listen.js'
import { until } from '../testing/fake-node.js'

// The node's end of a daemon connection: a WebSocket client wrapped as the socket wire.js's Peer reads, so the test
// answers hello and calls the runtime like personad does.
function dialNode(url, { token, protocols = [SUBPROTOCOL] } = {}) {
  const ws = new WebSocket(url, { protocols, headers: token ? { authorization: `Bearer ${token}` } : {} })
  const handlers = { data: [], close: [], error: [] }
  const socket = {
    setEncoding() {},
    on(ev, fn) { (handlers[ev] ??= []).push(fn) },
    write(s) { ws.send(s.endsWith('\n') ? s.slice(0, -1) : s) },
    destroy() { ws.close() },
  }
  const n = { ws, hello: undefined, events: [], opened: false, closed: false }
  ws.onmessage = (e) => handlers.data.forEach((fn) => fn(String(e.data) + '\n'))
  ws.onclose = () => { n.closed = true; handlers.close.forEach((fn) => fn()) }
  ws.onerror = () => {}
  ws.onopen = () => { n.opened = true }
  n.peer = new Peer(socket, {
    prefix: 'n',
    onRequest: async (method, params) => {
      if (method === 'runtime.hello') {
        n.hello = params
        return { protocol: 1, node_version: 'test', hub: true, features: ['prefer_running'] }
      }
      throw new Error(`unexpected ${method}`)
    },
    onEvent: (event, params) => n.events.push({ event, params }),
  })
  n.last = (event) => n.events.filter((e) => e.event === event).at(-1)?.params
  return n
}

// Read the HTTP refusal itself: Node 22's WebSocket reports a failed handshake as 'error', not 'close'.
function refusedStatus(url, { token, protocols = [SUBPROTOCOL] } = {}) {
  return new Promise((resolve, reject) => {
    const endpoint = new URL(url)
    endpoint.protocol = 'http:'
    const headers = {
      connection: 'Upgrade', upgrade: 'websocket',
      'sec-websocket-version': '13', 'sec-websocket-key': randomBytes(16).toString('base64'),
    }
    if (token) headers.authorization = `Bearer ${token}`
    if (protocols.length) headers['sec-websocket-protocol'] = protocols.join(', ')
    const req = request(endpoint, { headers })
    const deadline = setTimeout(() => req.destroy(new Error('timed out waiting for the HTTP refusal')), 3000)
    const done = (error, status) => {
      clearTimeout(deadline)
      if (error) reject(error)
      else resolve(status)
    }
    req.on('response', (res) => { res.resume(); done(null, res.statusCode) })
    req.on('upgrade', (_res, socket) => {
      socket.destroy()
      done(new Error('a refused handshake unexpectedly upgraded'))
    })
    req.on('error', (error) => done(error))
    req.end()
  })
}

function toy(extra = {}) {
  const sent = []
  const driver = {
    listSessions: async () => [{ session_id: 's1', status: 'idle' }],
    items: async () => ({ items: [], to_seq: 0 }),
    send: async (p) => { sent.push(p) },
    ...extra,
  }
  return { driver, sent }
}

test('the echo daemon CLI honors only the Persona token environment, including an explicitly empty value', async (t) => {
  for (const [name, values, accepted] of [
    ['current', { PERSONA_DAEMON_TOKEN: 'current-fixture-token', PREVIOUS_DAEMON_TOKEN: 'ignored-fixture-token' }, 'current-fixture-token'],
    ['empty', { PERSONA_DAEMON_TOKEN: '', PREVIOUS_DAEMON_TOKEN: 'ignored-fixture-token' }, undefined],
    ['missing', { PREVIOUS_DAEMON_TOKEN: 'ignored-fixture-token' }, undefined],
  ]) {
    await t.test(name, async (t) => {
      const home = mkdtempSync(join(tmpdir(), 'persona-echo-cli-'))
      const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('PERSONA_') && !key.startsWith('PERSONA_')))
      const child = spawn(process.execPath, [fileURLToPath(new URL('../examples/echo-runtime.mjs', import.meta.url)), '--listen', '0'],
        { env: { ...env, HOME: home, ...values }, stdio: ['ignore', 'ignore', 'pipe'] })
      const exited = once(child, 'exit')
      t.after(async () => {
        if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
        await exited
        rmSync(home, { recursive: true, force: true })
      })
      let log = ''
      child.stderr.setEncoding('utf8')
      child.stderr.on('data', (part) => { log += part })
      if (!accepted) {
        const [code] = await Promise.race([exited, new Promise((_, reject) => {
          const timer = setTimeout(() => reject(new Error('echo CLI exit timeout')), 5000)
          exited.finally(() => clearTimeout(timer))
        })])
        assert.equal(code, 2)
        assert.match(log, /PERSONA_DAEMON_TOKEN/)
        assert.ok(!log.includes(values.PREVIOUS_DAEMON_TOKEN))
        return
      }
      await until('echo CLI ready or exited', () => /echo daemon: ws:/.test(log) || child.exitCode !== null, 5000)
      const url = log.match(/echo daemon: (ws:\/\/\S+);/)?.[1]
      assert.ok(url, 'the daemon must listen rather than exit')
      assert.ok(!log.includes(accepted), 'the token must not appear in logs')
      if (values.PERSONA_DAEMON_TOKEN !== accepted) {
        assert.equal(await refusedStatus(url, { token: values.PERSONA_DAEMON_TOKEN }), 401)
      }
      const node = dialNode(url, { token: accepted })
      t.after(() => node.ws.close())
      await until('echo CLI hello', () => node.hello, 3000)
      assert.equal(node.hello.runtime, 'echo')
    })
  }
})

test('a listening runtime serves its endpoint: the node dials in with the token and the protocol runs as on the socket', async (t) => {
  const { driver, sent } = toy()
  const rt = createRuntime(driver, { runtime: 'toy', name: 'Toy', listen: { port: 0, token: 'tok-1' }, refreshMs: 0 })
  t.after(() => rt.stop())
  const addr = await rt.start()
  assert.equal(addr.address, '127.0.0.1', 'loopback by default')
  const n = dialNode(`ws://127.0.0.1:${addr.port}/persona/runtime`, { token: 'tok-1' })
  t.after(() => n.ws.close())
  await until('hello', () => n.hello, 3000)
  assert.equal(n.hello.runtime, 'toy')
  assert.deepEqual(n.hello.caps, { start: false, send: true, interrupt: false, rename: false, close: false, assistant: false })
  await until('the list', () => n.last('runtime.sessions'), 3000)
  await n.peer.call('session.send', { session_id: 's1', text: 'over the websocket', client_msg_id: 'm-1' })
  assert.equal(sent[0].text, 'over the websocket')
  // A big message both ways (beyond a 16-bit frame length).
  const big = 'x'.repeat(200_000)
  await n.peer.call('session.send', { session_id: 's1', text: big, client_msg_id: 'm-2' })
  assert.equal(sent[1].text.length, big.length)
  assert.ok(rt.connected)
})

test('a wrong token, a missing subprotocol or another path is refused before the protocol starts', async (t) => {
  const { driver } = toy()
  const logs = []
  const rt = createRuntime(driver, { runtime: 'toy', listen: { port: 0, token: 'tok-R4ND0M' }, refreshMs: 0, log: (m) => logs.push(m) })
  t.after(() => rt.stop())
  const { port } = await rt.start()
  for (const [url, opts, status] of [
    [`ws://127.0.0.1:${port}/persona/runtime`, { token: 'wrong-fixture-token' }, 401],
    [`ws://127.0.0.1:${port}/persona/runtime`, {}, 401],
    [`ws://127.0.0.1:${port}/persona/runtime`, { token: 'tok-R4ND0M', protocols: [] }, 400],
    [`ws://127.0.0.1:${port}/persona/runtime`, { token: 'tok-R4ND0M', protocols: ['something-else'] }, 400],
    [`ws://127.0.0.1:${port}/elsewhere`, { token: 'tok-R4ND0M' }, 404],
  ]) {
    assert.equal(await refusedStatus(url, opts), status)
    assert.equal(rt.connected, false)
  }
  assert.ok(logs.some((l) => /refused a connection/.test(l)))
  assert.ok(!logs.some((l) => l.includes('tok-')), 'no token in the log')
  assert.equal(rt.connected, false)
})

test('a newer connection from the node replaces the last one', async (t) => {
  const { driver } = toy()
  const rt = createRuntime(driver, { runtime: 'toy', listen: { port: 0, token: 'tok' }, refreshMs: 0 })
  t.after(() => rt.stop())
  const { port } = await rt.start()
  const a = dialNode(`ws://127.0.0.1:${port}/persona/runtime`, { token: 'tok' })
  await until('first hello', () => a.hello, 3000)
  const b = dialNode(`ws://127.0.0.1:${port}/persona/runtime`, { token: 'tok' })
  t.after(() => b.ws.close())
  await until('second hello', () => b.hello, 3000)
  await until('the first closed', () => a.closed, 3000)
  await b.peer.call('session.send', { session_id: 's1', text: 'x' })
  assert.ok(rt.connected)
})

test('mounted on the runtime\'s own HTTP server through accept(), with a token read per connection', async (t) => {
  const { driver } = toy()
  let current = 'one'
  const rt = createRuntime(driver, { runtime: 'toy', listen: true, token: () => current, refreshMs: 0 })
  t.after(() => rt.stop())
  assert.equal(await rt.start(), undefined)
  const server = createServer((req, res) => res.end('the daemon\'s own API'))
  server.on('upgrade', (req, socket, head) => {
    if (req.url === '/api/persona') rt.accept(req, socket, head)
    else socket.destroy()
  })
  await new Promise((r) => server.listen(0, '127.0.0.1', r))
  t.after(() => server.close())
  const url = `ws://127.0.0.1:${server.address().port}/api/persona`
  current = 'two' // rotated
  assert.equal(await refusedStatus(url, { token: 'one' }), 401)
  assert.equal(rt.connected, false)
  const n = dialNode(url, { token: 'two' })
  t.after(() => n.ws.close())
  await until('hello', () => n.hello, 3000)
  assert.throws(() => createRuntime(driver, { runtime: 'toy', listen: { port: 0 } }), /token/)
  assert.throws(() => createRuntime(driver, { runtime: 'toy' }).accept({}, {}, Buffer.alloc(0)), /dials runtime.sock/)
})

// Raw frames: what a WebSocket client may send that Node's own client does not (fragments, pings between them,
// unmasked frames).
function frame(op, payload, { fin = true, mask = true } = {}) {
  const n = payload.length
  const b0 = (fin ? 0x80 : 0) | op
  const m = mask ? 0x80 : 0
  let head
  if (n < 126) head = Buffer.from([b0, m | n])
  else if (n < 65536) head = Buffer.from([b0, m | 126, n >> 8, n & 0xff])
  else {
    head = Buffer.from([b0, m | 127, 0, 0, 0, 0, 0, 0, 0, 0])
    head.writeBigUInt64BE(BigInt(n), 2)
  }
  if (!mask) return Buffer.concat([head, payload])
  const key = randomBytes(4)
  const body = Buffer.from(payload)
  for (let i = 0; i < body.length; i++) body[i] ^= key[i & 3]
  return Buffer.concat([head, key, body])
}

async function rawConnect(port, token) {
  const key = randomBytes(16).toString('base64')
  const sock = connect(port, '127.0.0.1')
  await new Promise((r) => sock.once('connect', r))
  sock.write(`GET /persona/runtime HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ${key}\r\n`
    + `Sec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: ${SUBPROTOCOL}\r\nAuthorization: Bearer ${token}\r\n\r\n`)
  let buf = Buffer.alloc(0)
  sock.on('data', (d) => { buf = Buffer.concat([buf, d]) })
  await until('101', () => buf.includes('\r\n\r\n'), 3000)
  const end = buf.indexOf('\r\n\r\n') + 4
  const headers = buf.subarray(0, end).toString()
  const accept = createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64')
  assert.match(headers, /^HTTP\/1\.1 101/)
  assert.ok(headers.includes(`Sec-WebSocket-Accept: ${accept}`))
  assert.ok(headers.includes(`Sec-WebSocket-Protocol: ${SUBPROTOCOL}`))
  buf = buf.subarray(end)
  return { sock, take: () => { const b = buf; buf = Buffer.alloc(0); return b }, peek: () => buf }
}

test('fragmented messages, pings between them, and an unmasked frame', async (t) => {
  const { driver, sent } = toy()
  const rt = createRuntime(driver, { runtime: 'toy', listen: { port: 0, token: 'tok' }, refreshMs: 0 })
  t.after(() => rt.stop())
  const { port } = await rt.start()
  const c = await rawConnect(port, 'tok')
  t.after(() => c.sock.destroy())
  await until('the hello request', () => c.peek().includes('runtime.hello'), 3000)
  const f = c.take() // one unmasked text frame from the server
  const len = f[1] & 0x7f
  const hello = JSON.parse(f.subarray(len === 126 ? 4 : 2).toString())
  const answer = Buffer.from(JSON.stringify({ id: hello.id, result: { protocol: 1, hub: true, features: [] } }))
  c.sock.write(frame(0x1, answer.subarray(0, 10), { fin: false }))
  c.sock.write(frame(0x9, Buffer.from('hi'))) // a ping in the middle of a message
  c.sock.write(frame(0x0, answer.subarray(10)))
  await until('the pong', () => c.peek().includes(Buffer.from([0x8a, 0x02])), 3000)
  await until('connected', () => rt.connected, 3000)
  const call = Buffer.from(JSON.stringify({ id: 'n1', method: 'session.send', params: { session_id: 's1', text: 'pieces' } }))
  c.sock.write(Buffer.concat([frame(0x1, call.subarray(0, 5), { fin: false }), frame(0x0, call.subarray(5, 9), { fin: false }), frame(0x0, call.subarray(9))]))
  await until('the send', () => sent.length === 1, 3000)
  assert.equal(sent[0].text, 'pieces')
  c.take()
  c.sock.write(frame(0x1, Buffer.from('{}'), { mask: false }))
  await until('closed with 1002', () => c.peek().length >= 4 && c.peek()[0] === 0x88, 3000)
  assert.equal(c.peek().readUInt16BE(2), 1002)
  await until('disconnected', () => !rt.connected, 3000)
})

// A socket stand-in for WsConnection: the test emits the chunks and reads what the connection writes back.
function fakeSocket() {
  const sock = new EventEmitter()
  sock.written = []
  sock.write = (b) => { sock.written.push(Buffer.from(b)); return true }
  sock.end = () => {}
  sock.destroy = () => { sock.destroyed = true }
  sock.setNoDelay = () => {}
  return sock
}

test('a 32 MiB message in 64 KiB chunks is put together with one copy, not a copy of everything so far per chunk', (t) => {
  const payload = Buffer.alloc(32 * 1024 * 1024, '{"text":"0123456789abcdef"}')
  const wire = frame(0x1, payload)
  const sock = fakeSocket()
  const c = new WsConnection(sock)
  const got = []
  c.on('data', (m) => got.push(m))
  const concat = Buffer.concat
  t.after(() => { Buffer.concat = concat })
  let copied = 0
  Buffer.concat = (list, n) => {
    const b = concat(list, n)
    copied += b.length
    return b
  }
  for (let i = 0; i < wire.length; i += 64 * 1024) sock.emit('data', wire.subarray(i, i + 64 * 1024))
  Buffer.concat = concat
  assert.equal(got.length, 1)
  assert.ok(got[0] === `${payload.toString('utf8')}\n`, 'the message arrives intact')
  assert.ok(copied <= 3 * payload.length, `copied ${copied} bytes for a ${payload.length}-byte message`)
})

test('a frame split at any byte, or a ping between two fragments, still gives the same message and a pong', async () => {
  // Multi-byte characters, and over 125 bytes so the length takes 16 bits; byte 50 is inside a two-byte character.
  const text = Buffer.from(`{"text":"${'\u00e9\u{1F600}\u00fc'.repeat(30)}"}`)
  const one = frame(0x1, text)
  const fragmented = Buffer.concat([frame(0x1, text.subarray(0, 50), { fin: false }), frame(0x9, Buffer.from('hi')), frame(0x0, text.subarray(50))])
  const big = Buffer.alloc(70_000, 'abc\u00e9')
  const long = frame(0x1, big) // a 64-bit length
  const pong = Buffer.from([0x8a, 2, ...Buffer.from('hi')]).toString('hex')
  const before = [one, fragmented, long].map((b) => b.toString('hex'))
  const run = async (wire, cuts, { head = false } = {}) => {
    const sock = fakeSocket()
    const pieces = []
    let at = 0
    for (const cut of [...cuts, wire.length]) {
      pieces.push(wire.subarray(at, cut))
      at = cut
    }
    const c = new WsConnection(sock, head ? pieces.shift() : undefined)
    const got = []
    c.on('data', (m) => got.push(m))
    if (head) await new Promise((r) => setImmediate(r)) // what came with the upgrade is read first
    for (const p of pieces) sock.emit('data', p)
    return { got, sock }
  }
  for (const wire of [one, fragmented]) {
    for (let cut = 1; cut < wire.length; cut++) {
      const { got, sock } = await run(wire, [cut], { head: cut % 2 === 0 })
      assert.deepEqual(got, [`${text.toString('utf8')}\n`], `split at ${cut}`)
      if (wire === fragmented) assert.ok(sock.written.some((b) => b.toString('hex') === pong), `a pong, split at ${cut}`)
    }
    const bytes = Array.from({ length: wire.length - 1 }, (_, i) => i + 1)
    assert.deepEqual((await run(wire, bytes)).got, [`${text.toString('utf8')}\n`], 'a byte at a time')
  }
  const header = Array.from({ length: 20 }, (_, i) => i + 1) // the 10-byte header and the mask one byte at a time
  assert.deepEqual((await run(long, header)).got, [`${big.toString('utf8')}\n`])
  assert.deepEqual([one, fragmented, long].map((b) => b.toString('hex')), before, 'the chunks fed in are not changed')
})
