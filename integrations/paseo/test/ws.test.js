import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { EventEmitter } from 'node:events'
import { mkdtempSync, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { join } from 'node:path'
import { WsConnection } from '../../runtime-sdk/lib/listen.js'
import { dialWebSocket, WsClient } from '../lib/ws.js'

const GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11'

// A WebSocket server for the client to dial: the SDK's server connection behind a plain upgrade.
async function server(t, { on, socketPath, status } = {}) {
  const conns = []
  const http = createServer()
  http.on('upgrade', (req, socket, head) => {
    if (status) {
      socket.end(`HTTP/1.1 ${status}\r\nContent-Length: 0\r\n\r\n`)
      return
    }
    const accept = createHash('sha1').update(req.headers['sec-websocket-key'] + GUID).digest('base64')
    socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`)
    const c = new WsConnection(socket, head)
    c.req = req
    conns.push(c)
    on?.(c)
  })
  await new Promise((r) => (socketPath ? http.listen(socketPath, r) : http.listen(0, '127.0.0.1', r)))
  t.after(() => {
    for (const c of conns) c.socket.destroy() // upgraded sockets are the test's to end, not the server's
    http.close()
  })
  return { port: http.address().port, conns }
}

test('a text message each way, over TCP and over a unix socket, and a closure the other end sees', async (t) => {
  const got = []
  const s = await server(t, { on: (c) => c.on('data', (line) => { got.push(line); c.write(`echo ${line}`) }) })
  const ws = await dialWebSocket({ port: s.port })
  const back = new Promise((r) => ws.once('message', r))
  ws.send('{"type":"hello"}')
  assert.equal(await back, 'echo {"type":"hello"}')
  assert.equal(got[0], '{"type":"hello"}\n')
  assert.equal(s.conns[0].req.url, '/ws')
  assert.equal(s.conns[0].req.headers.host, `127.0.0.1:${s.port}`)
  assert.equal(s.conns[0].req.headers.origin, undefined, 'no Origin: the daemon refuses browsers it does not know')
  const closed = new Promise((r) => s.conns[0].once('close', r))
  ws.close()
  await closed

  const dir = mkdtempSync('/tmp/pws-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const u = await server(t, { socketPath: join(dir, 'd.sock'), on: (c) => c.write('hi over unix') })
  const ws2 = await dialWebSocket({ socketPath: join(dir, 'd.sock') })
  assert.equal(await new Promise((r) => ws2.once('message', r)), 'hi over unix')
  assert.equal(u.conns[0].req.headers.host, 'localhost')
  const gone = new Promise((r) => ws2.once('close', (code) => r(code)))
  u.conns[0].destroy()
  assert.equal(await gone, 1000)
})

test('a large message, a fragmented one and a ping', async (t) => {
  const big = 'x'.repeat(200_000)
  const s = await server(t, {
    on: (c) => {
      c.write(big)
      // Fragmented by hand: a text frame and a continuation, a ping between them.
      c.socket.write(Buffer.from([0x01, 3, ...Buffer.from('abc')]))
      c.socket.write(Buffer.from([0x89, 2, ...Buffer.from('hi')]))
      c.socket.write(Buffer.from([0x80, 3, ...Buffer.from('def')]))
    },
  })
  const ws = await dialWebSocket({ port: s.port })
  const msgs = []
  await new Promise((r) => ws.on('message', (m) => { msgs.push(m); if (msgs.length === 2) r() }))
  assert.equal(msgs[0].length, big.length)
  assert.equal(msgs[1], 'abcdef')
})

test('a refused handshake and a closed port fail the dial', async (t) => {
  const s = await server(t, { status: '401 Unauthorized' })
  await assert.rejects(dialWebSocket({ port: s.port }), /refused: HTTP\/1.1 401/)
  const free = createServer()
  await new Promise((r) => free.listen(0, '127.0.0.1', r))
  const port = free.address().port
  await new Promise((r) => free.close(r))
  await assert.rejects(dialWebSocket({ port, timeoutMs: 2000 }), /ECONNREFUSED|closed/)
})

// A server frame (unmasked), any length.
function frame(op, payload, { fin = true } = {}) {
  const n = payload.length
  const b0 = (fin ? 0x80 : 0) | op
  let head
  if (n < 126) head = Buffer.from([b0, n])
  else if (n < 65536) head = Buffer.from([b0, 126, n >> 8, n & 0xff])
  else {
    head = Buffer.from([b0, 127, 0, 0, 0, 0, 0, 0, 0, 0])
    head.writeBigUInt64BE(BigInt(n), 2)
  }
  return Buffer.concat([head, payload])
}

// A socket stand-in for WsClient: the test emits the chunks and reads what the client writes back.
function fakeSocket() {
  const sock = new EventEmitter()
  sock.written = []
  sock.write = (b) => { sock.written.push(Buffer.from(b)); return true }
  sock.end = () => {}
  sock.destroy = () => { sock.destroyed = true }
  sock.setNoDelay = () => {}
  return sock
}

// The payload of a frame the client wrote (masked, as a client's are).
function unmasked(b) {
  const len = b[1] & 0x7f
  const at = len === 126 ? 4 : len === 127 ? 10 : 2
  const mask = b.subarray(at, at + 4)
  return Buffer.from(b.subarray(at + 4).map((x, i) => x ^ mask[i & 3]))
}

test('a 32 MiB message in 64 KiB chunks is put together with one copy, not a copy of everything so far per chunk', (t) => {
  const payload = Buffer.alloc(32 * 1024 * 1024, '{"text":"0123456789abcdef"}')
  const wire = frame(0x1, payload)
  const sock = fakeSocket()
  const ws = new WsClient(sock)
  const got = []
  ws.on('message', (m) => got.push(m))
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
  assert.ok(got[0] === payload.toString('utf8'), 'the message arrives intact')
  assert.ok(copied <= 3 * payload.length, `copied ${copied} bytes for a ${payload.length}-byte message`)
})

test('a frame split at any byte, or a ping between two fragments, still gives the same message and a pong', async () => {
  // Multi-byte characters, and over 125 bytes so the length takes 16 bits; byte 50 is inside a two-byte character.
  const text = Buffer.from(`{"text":"${'\u00e9\u{1F600}\u00fc'.repeat(30)}"}`)
  const one = frame(0x1, text)
  const fragmented = Buffer.concat([frame(0x1, text.subarray(0, 50), { fin: false }), frame(0x9, Buffer.from('hi')), frame(0x0, text.subarray(50))])
  const big = Buffer.alloc(70_000, 'abc\u00e9')
  const long = frame(0x1, big) // a 64-bit length
  const before = [one, fragmented, long].map((b) => b.toString('hex'))
  const run = async (wire, cuts, { rest = false } = {}) => {
    const sock = fakeSocket()
    const pieces = []
    let at = 0
    for (const cut of [...cuts, wire.length]) {
      pieces.push(wire.subarray(at, cut))
      at = cut
    }
    const ws = new WsClient(sock, rest ? pieces.shift() : undefined)
    const got = []
    ws.on('message', (m) => got.push(m))
    if (rest) await new Promise((r) => setImmediate(r)) // what came with the handshake's answer is read first
    for (const p of pieces) sock.emit('data', p)
    return { got, sock }
  }
  const pong = (sock) => sock.written.some((b) => b[0] === 0x8a && b[1] === (0x80 | 2) && unmasked(b).toString() === 'hi')
  for (const wire of [one, fragmented]) {
    for (let cut = 1; cut < wire.length; cut++) {
      const { got, sock } = await run(wire, [cut], { rest: cut % 2 === 0 })
      assert.deepEqual(got, [text.toString('utf8')], `split at ${cut}`)
      if (wire === fragmented) assert.ok(pong(sock), `a pong, split at ${cut}`)
    }
    const bytes = Array.from({ length: wire.length - 1 }, (_, i) => i + 1)
    assert.deepEqual((await run(wire, bytes)).got, [text.toString('utf8')], 'a byte at a time')
  }
  const header = Array.from({ length: 16 }, (_, i) => i + 1) // the 10-byte header one byte at a time
  assert.deepEqual((await run(long, header)).got, [big.toString('utf8')])
  assert.deepEqual([one, fragmented, long].map((b) => b.toString('hex')), before, 'the chunks fed in are not changed')
})
