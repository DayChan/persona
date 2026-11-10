// The JSON-lines RPC of server/internal/rpc, for the runtime end of runtime.sock: one JSON object per line, either a
// request {"id","method","params"}, a response {"id","result"} / {"id","error":{"message","code"}} or an event
// {"event","params"}. Both ends may call each other.

/** A failed call's error, with the code the other end set (rpc.Error.Code). */
export class RpcError extends Error {
  constructor(message, code, data) {
    super(message)
    this.name = 'RpcError'
    if (code !== undefined) this.code = code
    if (data !== undefined) this.data = data
  }
}

/** One message as its line on the wire; throws what JSON.stringify throws (a BigInt, a cycle). */
function encode(m) {
  return JSON.stringify(m) + '\n'
}

function messageOf(err) {
  return err instanceof Error ? err.message : String(err)
}

export class Peer {
  /**
   * @param {import('node:net').Socket} socket - connected stream.
   * @param {{prefix?: string, onRequest?: (method: string, params: any) => any, onEvent?: (event: string, params: any) => void, maxLine?: number}} [opts]
   */
  constructor(socket, opts = {}) {
    this.socket = socket
    this.prefix = opts.prefix ?? 'd'
    this.onRequest = opts.onRequest
    this.onEvent = opts.onEvent
    this.maxLine = opts.maxLine ?? 64 * 1024 * 1024
    this.next = 0
    this.pending = new Map()
    this.closed = false
    this.partial = [] // the line read so far, as the pieces it arrived in
    this.partialLength = 0
    this.donePromise = new Promise((resolve) => { this.resolveDone = resolve })
    socket.setEncoding('utf8')
    socket.on('data', (chunk) => this.read(chunk))
    socket.on('close', () => this.close(new Error('connection closed')))
    socket.on('error', (err) => this.close(err))
  }

  /** Resolves once the connection is gone. */
  get done() {
    return this.donePromise
  }

  read(chunk) {
    // Only what just arrived is searched for newlines, and a line's pieces are joined once, when its newline comes: a
    // long line (a phone's images, a runtime package, both base64) costs time linear in its length however it is cut.
    const text = String(chunk)
    let start = 0
    let nl
    while (!this.closed && (nl = text.indexOf('\n', start)) >= 0) {
      let line = text.slice(start, nl)
      if (this.partial.length > 0) {
        this.partial.push(line)
        line = this.partial.join('')
        this.partial = []
        this.partialLength = 0
      }
      start = nl + 1
      if (line.trim() !== '') this.dispatch(line)
    }
    if (this.closed || start === text.length) return
    this.partial.push(start === 0 ? text : text.slice(start))
    this.partialLength += text.length - start
    if (this.partialLength > this.maxLine) this.close(new Error('line too long'))
  }

  dispatch(line) {
    let m
    try {
      m = JSON.parse(line)
    } catch {
      return
    }
    if (typeof m.method === 'string' && m.id !== undefined) {
      void this.handle(m)
    } else if (typeof m.event === 'string') {
      try {
        this.onEvent?.(m.event, m.params)
      } catch {
        // a bad event handler must not take the connection down
      }
    } else if (m.id !== undefined) {
      const p = this.pending.get(m.id)
      if (!p) return
      this.pending.delete(m.id)
      if (m.error) p.reject(new RpcError(m.error.message ?? 'error', m.error.code, m.error.data))
      else p.resolve(m.result)
    }
  }

  async handle(m) {
    let out
    try {
      if (!this.onRequest) throw new Error(`no handler for ${m.method}`)
      const result = await this.onRequest(m.method, m.params ?? {})
      out = { id: m.id, result: result === undefined ? null : result }
    } catch (err) {
      const error = { message: messageOf(err) }
      if (err && typeof err.code === 'string') error.code = err.code
      if (err && err.data !== undefined && err.data !== null && typeof err.data === 'object') error.data = err.data
      out = { id: m.id, error }
    }
    let line
    try {
      line = encode(out)
    } catch (err) {
      // An answer JSON cannot carry (a BigInt, a cycle): the caller still gets one, and the throw does not end the
      // host process as an unhandled rejection.
      line = encode({ id: m.id, error: { message: `the answer could not be encoded: ${messageOf(err)}`, code: 'internal' } })
    }
    this.writeLine(line)
  }

  write(m) {
    if (this.closed) return false
    return this.writeLine(encode(m))
  }

  /** Write a message already encoded by encode(). */
  writeLine(line) {
    if (this.closed) return false
    this.socket.write(line)
    return true
  }

  /** Call a method on the other end. */
  call(method, params = {}, { timeoutMs = 30_000, signal } = {}) {
    if (this.closed) return Promise.reject(new RpcError('connection closed', 'closed'))
    const id = `${this.prefix}${++this.next}`
    let line
    try {
      line = encode({ id, method, params })
    } catch (err) {
      // Encoded before anything is registered: nothing is left pending until the timeout.
      return Promise.reject(new RpcError(`${method}: the params could not be encoded: ${messageOf(err)}`, 'internal'))
    }
    return new Promise((resolve, reject) => {
      let timer
      const cleanup = () => {
        clearTimeout(timer)
        signal?.removeEventListener('abort', onAbort)
      }
      const onAbort = () => {
        this.pending.delete(id)
        cleanup()
        reject(signal.reason ?? new Error('aborted'))
      }
      this.pending.set(id, {
        resolve: (v) => { cleanup(); resolve(v) },
        reject: (e) => { cleanup(); reject(e) },
      })
      if (timeoutMs > 0) {
        timer = setTimeout(() => {
          this.pending.delete(id)
          cleanup()
          reject(new RpcError(`${method}: timed out`, 'timeout'))
        }, timeoutMs)
        timer.unref?.()
      }
      if (signal) {
        if (signal.aborted) return onAbort()
        signal.addEventListener('abort', onAbort, { once: true })
      }
      this.writeLine(line)
    })
  }

  /** Send a one-way event. */
  notify(event, params = {}) {
    return this.write({ event, params })
  }

  close(err) {
    if (this.closed) return
    this.closed = true
    for (const p of this.pending.values()) p.reject(new RpcError(err?.message ?? 'connection closed', 'closed'))
    this.pending.clear()
    this.socket.destroy()
    this.resolveDone(err)
  }
}
