import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EventEmitter, once } from 'node:events'
import { mkdtempSync, rmSync } from 'node:fs'
import { createServer, connect } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Peer, RpcError } from '../lib/wire.js'

// The part of a socket Peer uses: 'data' strings in, what it writes recorded.
function fakeSocket() {
  const s = new EventEmitter()
  s.written = []
  s.destroyed = false
  s.setEncoding = () => {}
  s.write = (line) => { s.written.push(line); return true }
  s.destroy = () => { s.destroyed = true }
  return s
}

// Both ends of a real connection, over a unix socket in a temporary directory.
async function socketPair(t) {
  const dir = mkdtempSync(join(tmpdir(), 'persona-wire-'))
  const server = createServer()
  await new Promise((resolve) => server.listen(join(dir, 'wire.sock'), resolve))
  const accepted = once(server, 'connection')
  const client = connect(join(dir, 'wire.sock'))
  const [[served]] = await Promise.all([accepted, once(client, 'connect')])
  t.after(() => {
    client.destroy()
    served.destroy()
    server.close()
    rmSync(dir, { recursive: true, force: true })
  })
  return [served, client]
}

// How many characters String.prototype.indexOf looks at while searching for '\n' during fn().
function newlineScan(t, fn) {
  const indexOf = String.prototype.indexOf
  let scanned = 0
  String.prototype.indexOf = function (search, from) {
    const i = indexOf.call(this, search, from)
    if (search === '\n') scanned += (i < 0 ? this.length : i + 1) - Math.max(0, Math.trunc(from) || 0)
    return i
  }
  t.after(() => { String.prototype.indexOf = indexOf })
  try {
    fn()
  } finally {
    String.prototype.indexOf = indexOf
  }
  return scanned
}

const eventLine = (params) => JSON.stringify({ event: 'e', params })

test('a 32 MiB line in 64 KiB chunks is dispatched once, looking at each character about once', (t) => {
  const sock = fakeSocket()
  const got = []
  new Peer(sock, { onEvent: (event, params) => got.push(params.data.length) })
  const size = 32 * 1024 * 1024
  const line = eventLine({ data: 'a'.repeat(size) }) + '\n'
  const scanned = newlineScan(t, () => {
    for (let i = 0; i < line.length; i += 64 * 1024) sock.emit('data', line.slice(i, i + 64 * 1024))
  })
  assert.deepEqual(got, [size])
  // Searching the whole partial line again on every chunk looks at about n²/2/65536 characters: 256 times the line.
  assert.ok(scanned <= 2 * line.length, `looked at ${scanned} characters for a line of ${line.length}`)
})

test('lines are cut wherever the chunks end, blank lines are skipped, and reading stops once the peer is closed', () => {
  const text = [1, 2, 3, 4, 5].map((n) => eventLine({ n })).join('\n\n  \n') + '\n'
  for (const size of [1, 3, 64, text.length]) {
    const sock = fakeSocket()
    const seen = []
    const peer = new Peer(sock, {
      onEvent: (event, params) => {
        seen.push(params.n)
        if (params.n === 4) peer.close(new Error('enough'))
      },
    })
    for (let i = 0; i < text.length; i += size) sock.emit('data', text.slice(i, i + size))
    assert.deepEqual(seen, [1, 2, 3, 4], `chunks of ${size}`)
    assert.equal(sock.destroyed, true)
  }
})

test('a line longer than maxLine closes the connection, however it is cut', async () => {
  const text = eventLine({ s: 'short' }) + '\n' + eventLine({ s: 'y'.repeat(100) })
  for (const size of [4, text.length]) {
    const sock = fakeSocket()
    const seen = []
    const peer = new Peer(sock, { maxLine: 64, onEvent: (event, params) => seen.push(params.s) })
    for (let i = 0; i < text.length; i += size) sock.emit('data', text.slice(i, i + size))
    assert.deepEqual(seen, ['short'], `chunks of ${size}`)
    assert.equal(peer.closed, true)
    assert.equal((await peer.done).message, 'line too long')
    assert.equal(sock.destroyed, true)
  }
})

test('a character whose bytes arrive in two socket reads is read whole', async (t) => {
  const [served, client] = await socketPair(t)
  let deliver
  const delivered = new Promise((resolve) => { deliver = resolve })
  new Peer(served, { onEvent: (event, params) => deliver(params.s) })
  const bytes = Buffer.from(eventLine({ s: 'a\u{1F600}b' }) + '\n')
  const cut = bytes.indexOf(0xf0) + 2 // inside the emoji's four bytes
  const firstRead = once(served, 'data')
  client.write(bytes.subarray(0, cut))
  const [first] = await firstRead
  assert.ok(!first.includes('\u{1F600}'), 'the first read ends before the emoji')
  client.write(bytes.subarray(cut))
  assert.equal(await delivered, 'a\u{1F600}b')
})

test('an answer that cannot be encoded reaches the caller as an error, and the connection keeps working', async (t) => {
  const unhandled = []
  const onUnhandled = (err) => unhandled.push(err)
  process.on('unhandledRejection', onUnhandled)
  t.after(() => process.off('unhandledRejection', onUnhandled))
  const [served, client] = await socketPair(t)
  new Peer(served, {
    onRequest: async (method) => {
      if (method === 'size') return { n: 1n }
      if (method === 'fail') throw Object.assign(new Error('refused'), { code: 'nope', data: { n: 1n } })
      return { ok: true }
    },
  })
  const caller = new Peer(client, { prefix: 'n' })
  for (const method of ['size', 'fail']) {
    await assert.rejects(caller.call(method, {}, { timeoutMs: 5000 }), (err) => {
      assert.ok(err instanceof RpcError, `${method}: ${err}`)
      assert.equal(err.code, 'internal')
      assert.match(err.message, /^the answer could not be encoded: .*BigInt/)
      return true
    })
  }
  assert.deepEqual(await caller.call('ok', {}, { timeoutMs: 5000 }), { ok: true })
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(unhandled, [])
})

test('call() with params that cannot be encoded rejects at once and leaves nothing pending', async () => {
  const sock = fakeSocket()
  const peer = new Peer(sock)
  const cycle = {}
  cycle.self = cycle
  for (const params of [{ n: 1n }, cycle]) {
    await assert.rejects(peer.call('session.send', params), (err) => {
      assert.ok(err instanceof RpcError, String(err))
      assert.equal(err.code, 'internal')
      assert.match(err.message, /^session\.send: the params could not be encoded: /)
      return true
    })
  }
  assert.equal(peer.pending.size, 0)
  assert.deepEqual(sock.written, [])
  const answered = peer.call('session.send', { text: 'hi' })
  const { id } = JSON.parse(sock.written[0])
  sock.emit('data', JSON.stringify({ id, result: 'sent' }) + '\n')
  assert.equal(await answered, 'sent')
  assert.equal(peer.pending.size, 0)
})
