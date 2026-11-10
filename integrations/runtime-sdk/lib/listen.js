// The daemon end of the runtime protocol (docs/integrations/daemons.md): instead of dialing the node's runtime.sock, a runtime that
// is a service of its own — another system user, a container, another machine — opens a WebSocket endpoint and the
// node dials it. The handshake carries a bearer token and the subprotocol below; after it, every text message is one
// JSON object of the same protocol. This file is the endpoint: the upgrade, the token, and a minimal RFC 6455 server
// connection that looks to wire.js like the socket it reads lines from. No dependencies.

import { LEGACY_SUBPROTOCOL } from './compat.js'
import { createHash, timingSafeEqual } from 'node:crypto'
import { EventEmitter } from 'node:events'

/** The WebSocket subprotocol of the runtime protocol (node: DaemonSubprotocol). */
export const SUBPROTOCOL = 'persona-runtime.v1'

const GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11'
const MAX_MESSAGE = 64 * 1024 * 1024 // as the node's limit on a line
const OP = { cont: 0x0, text: 0x1, binary: 0x2, close: 0x8, ping: 0x9, pong: 0xa }

/** Whether the request's bearer token is token (compared in constant time). */
export function tokenMatches(req, token) {
  const h = req.headers.authorization ?? ''
  const m = /^Bearer\s+(.+)$/i.exec(h)
  if (!m || !token) return false
  const a = createHash('sha256').update(m[1].trim()).digest()
  const b = createHash('sha256').update(String(token)).digest()
  return timingSafeEqual(a, b)
}

function refuse(socket, status, text) {
  try {
    socket.end(`HTTP/1.1 ${status} ${text}\r\nConnection: close\r\nContent-Type: text/plain\r\nContent-Length: ${Buffer.byteLength(text)}\r\n\r\n${text}`)
  } catch {}
  socket.destroy?.()
}

/**
 * Take an HTTP upgrade request as the runtime protocol's endpoint (an http.Server 'upgrade' event's arguments). Answers
 * 401 for a wrong token, 400 / 426 for what is not a WebSocket upgrade with the subprotocol; else switches protocols.
 * @param {import('node:http').IncomingMessage} req
 * @param {import('node:stream').Duplex} socket
 * @param {Buffer} head
 * @param {{ token: string }} opts
 * @returns {WsConnection | null} the connection, or null when refused (the socket is closed then).
 */
export function upgrade(req, socket, head, { token }) {
  if (!tokenMatches(req, token)) {
    refuse(socket, 401, 'Unauthorized')
    return null
  }
  const key = req.headers['sec-websocket-key']
  const upgradeTo = String(req.headers.upgrade ?? '').toLowerCase()
  if (req.method !== 'GET' || upgradeTo !== 'websocket' || typeof key !== 'string' || key === '') {
    refuse(socket, 400, 'Bad Request')
    return null
  }
  if (req.headers['sec-websocket-version'] !== '13') {
    refuse(socket, 426, 'Upgrade Required')
    return null
  }
  const offered = String(req.headers['sec-websocket-protocol'] ?? '').split(',').map((s) => s.trim())
  const selected = offered.find((p) => p === SUBPROTOCOL || p === LEGACY_SUBPROTOCOL)
  if (!selected) {
    refuse(socket, 400, `Subprotocol ${SUBPROTOCOL} required`)
    return null
  }
  const accept = createHash('sha1').update(key + GUID).digest('base64')
  socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n'
    + `Sec-WebSocket-Accept: ${accept}\r\nSec-WebSocket-Protocol: ${selected}\r\n\r\n`)
  socket.setNoDelay?.(true)
  socket.setKeepAlive?.(true, 15_000)
  return new WsConnection(socket, head)
}

/**
 * A server-side WebSocket connection shaped like the socket wire.js's Peer reads: 'data' gives each text message
 * followed by a newline, write(line) sends one message, destroy() closes. Pings are answered; a message over 64 MiB,
 * an unmasked frame or a bad opcode closes it.
 */
export class WsConnection extends EventEmitter {
  constructor(socket, head) {
    super()
    this.socket = socket
    this.rx = new Received()
    this.parts = [] // the fragments of the message being received
    this.size = 0
    this.closed = false
    socket.on('data', (chunk) => {
      this.rx.push(chunk)
      this.parse()
    })
    socket.on('close', () => this.finish())
    socket.on('error', (err) => this.emit('error', err))
    if (head?.length) {
      this.rx.push(Buffer.from(head))
      queueMicrotask(() => this.parse())
    }
  }

  /** wire.js sets the encoding; the messages are strings already. */
  setEncoding() {}

  parse() {
    const rx = this.rx
    while (!this.closed) {
      if (rx.length < 2) return
      const b = rx.peek(2)
      const fin = (b[0] & 0x80) !== 0
      const op = b[0] & 0x0f
      const masked = (b[1] & 0x80) !== 0
      let len = b[1] & 0x7f
      let at = 2
      if (len === 126) {
        if (rx.length < 4) return
        len = rx.peek(4).readUInt16BE(2)
        at = 4
      } else if (len === 127) {
        if (rx.length < 10) return
        const big = rx.peek(10).readBigUInt64BE(2)
        if (big > BigInt(MAX_MESSAGE)) return this.fail(1009, 'message too big')
        len = Number(big)
        at = 10
      }
      if (!masked) return this.fail(1002, 'client frames must be masked')
      if (op < 0x8 && this.size + len > MAX_MESSAGE) return this.fail(1009, 'message too big')
      if (rx.length < at + 4 + len) return
      const mask = rx.take(at + 4).subarray(at)
      const payload = rx.take(len) // a copy of its own: unmasked in place
      for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3]
      if (op >= 0x8) {
        if (!fin || len > 125) return this.fail(1002, 'bad control frame')
        if (op === OP.ping) this.send(OP.pong, payload)
        else if (op === OP.close) {
          this.send(OP.close, payload.subarray(0, 2))
          return this.end()
        } else if (op !== OP.pong) return this.fail(1002, 'bad opcode')
        continue
      }
      if (op === OP.cont ? this.parts.length === 0 : (op !== OP.text && op !== OP.binary) || this.parts.length > 0) {
        return this.fail(1002, 'bad frame order')
      }
      this.size += payload.length
      this.parts.push(payload)
      if (fin) {
        const msg = (this.parts.length === 1 ? this.parts[0] : Buffer.concat(this.parts, this.size)).toString('utf8')
        this.parts = []
        this.size = 0
        this.emit('data', msg.endsWith('\n') ? msg : `${msg}\n`)
      }
    }
  }

  send(op, payload) {
    if (this.closed || this.socket.destroyed) return false
    const n = payload.length
    let head
    if (n < 126) {
      head = Buffer.from([0x80 | op, n])
    } else if (n < 65536) {
      head = Buffer.alloc(4)
      head[1] = 126
      head.writeUInt16BE(n, 2)
    } else {
      head = Buffer.alloc(10)
      head[1] = 127
      head.writeBigUInt64BE(BigInt(n), 2)
    }
    head[0] = 0x80 | op
    this.socket.write(Buffer.concat([head, payload]))
    return true
  }

  /** Send one line (wire.js writes a JSON object and a newline) as one text message. */
  write(line) {
    const s = String(line)
    return this.send(OP.text, Buffer.from(s.endsWith('\n') ? s.slice(0, -1) : s, 'utf8'))
  }

  fail(code, reason) {
    const p = Buffer.alloc(2 + Buffer.byteLength(reason))
    p.writeUInt16BE(code, 0)
    p.write(reason, 2)
    this.send(OP.close, p)
    this.emit('error', new Error(reason))
    this.end()
  }

  end() {
    if (this.closed) return
    this.closed = true
    this.socket.end()
    setTimeout(() => this.socket.destroy(), 1000).unref?.()
    this.finish()
  }

  /** Close the connection (a normal closure). */
  destroy() {
    if (!this.closed) {
      const p = Buffer.alloc(2)
      p.writeUInt16BE(1000, 0)
      this.send(OP.close, p)
    }
    this.end()
  }

  finish() {
    if (this.finished) return
    this.finished = true
    this.closed = true
    this.emit('close')
  }

  get destroyed() {
    return this.closed
  }
}

/**
 * The bytes received and not parsed yet, kept as the chunks they came in: joining every chunk onto what came before
 * would copy a large frame again for each of its chunks. A frame is copied out once, when all of it is here.
 */
class Received {
  constructor() {
    this.chunks = []
    this.length = 0
  }

  push(chunk) {
    if (!chunk.length) return
    this.chunks.push(chunk)
    this.length += chunk.length
  }

  /** The first n bytes (a header's few; n <= length), copied only when they span chunks. Read only. */
  peek(n) {
    const first = this.chunks[0]
    if (first.length >= n) return first
    const out = Buffer.alloc(n)
    let at = 0
    for (const c of this.chunks) {
      at += c.copy(out, at, 0, Math.min(c.length, n - at))
      if (at === n) break
    }
    return out
  }

  /** Remove the first n bytes (n <= length) and return them copied into a buffer of their own. */
  take(n) {
    const pieces = []
    let left = n
    let used = 0
    while (left > 0) {
      const c = this.chunks[used]
      if (c.length <= left) {
        pieces.push(c)
        left -= c.length
        used++
      } else {
        pieces.push(c.subarray(0, left))
        this.chunks[used] = c.subarray(left)
        left = 0
      }
    }
    this.chunks.splice(0, used)
    this.length -= n
    return Buffer.concat(pieces, n)
  }
}
