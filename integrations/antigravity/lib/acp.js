import { assistantSearchPaths } from './compat.js'
// Persistent stdio JSON-RPC client for `agy acp`.
import { spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { createInterface } from 'node:readline'

export class AgyAcpClient extends EventEmitter {
  constructor(options = {}) {
    super()
    this.bin = options.bin || 'agy'
    this.args = options.args || ['acp']
    this.log = options.log || (() => {})
    this.onPermissionRequest = options.onPermissionRequest || null
    this.proc = null
    this.readyPromise = null
    this.nextId = 1
    this.pending = new Map()
    this.loadedSessions = new Map() // sessionId -> boolean (hasMcp)
    this.availableModels = null
    this.agentVersion = ''
    this.closed = false
    this.initTimeoutMs = options.initTimeoutMs ?? 60_000
    // How long a stopped agy gets to exit on SIGTERM before it is killed.
    this.killGraceMs = options.killGraceMs ?? 3_000
    // Kills in flight: each resolves once its agy is gone. stop() waits for all of them, so an agy dropped earlier (a
    // failed initialize, a stdin error) that ignores SIGTERM cannot outlive the companion.
    this.dying = new Set()
  }

  async ensureStarted() {
    if (this.closed) throw stoppedError()
    if (this.proc && this.readyPromise) return this.readyPromise
    this.readyPromise = this._start()
    return this.readyPromise
  }

  async _start() {
    const home = process.env.HOME || '/root'
    const extraPath = assistantSearchPaths(home).join(':')
    const child = spawn(this.bin, this.args, {
      stdio: ['pipe', 'pipe', 'pipe'],
      env: { ...process.env, PATH: `${extraPath}:${process.env.PATH || ''}` },
    })
    this.proc = child
    this.loadedSessions.clear()
    // A missing binary ('exit' never comes then) or a failed kill: without a listener the companion would crash.
    child.on('error', (err) => {
      this.log(`agy acp process error: ${err.message}`)
      this._forget(child, err)
    })

    const rl = createInterface({ input: child.stdout, crlfDelay: Infinity })
    rl.on('line', (line) => {
      const trimmed = line.trim()
      if (!trimmed) return
      let msg
      try {
        msg = JSON.parse(trimmed)
      } catch {
        return
      }
      this._onMessage(msg, child)
    })

    child.stderr.on('data', (chunk) => {
      const s = String(chunk).trim()
      if (s) this.log(`agy acp stderr: ${s}`)
    })

    // EPIPE: agy closed its stdin or exited before 'exit' arrived. It cannot take requests any more.
    child.stdin.on('error', (err) => {
      this.log(`agy acp stdin error: ${err.message}`)
      if (this._forget(child, err)) void this._kill(child)
    })
    child.on('exit', (code, sig) => {
      this.log(`agy acp exited (code=${code}, sig=${sig})`)
      this._forget(child, new Error(`agy acp exited (${code ?? sig})`))
    })

    let init
    try {
      init = await this._rawCall('initialize', {
        protocolVersion: 1,
        clientCapabilities: {},
        clientInfo: { name: 'persona', version: '0.1.0' },
      }, this.initTimeoutMs)
    } catch (err) {
      // An agy that did not initialize is of no use: end it, and let the next call start a fresh one. (Already
      // forgotten: it exited, failed to spawn, or stop() or a stdin error ended it.)
      if (this._forget(child, err)) void this._kill(child)
      throw err
    }
    this.agentVersion = init?.agentInfo?.version || ''
    return init
  }

  /**
   * Drop `child` when it is still the current process: later calls start a new one and every pending call fails with
   * `err`. A process that is not the current one any more (replaced, stopped) changes nothing. True when it was dropped.
   */
  _forget(child, err) {
    if (this.proc !== child) return false
    this.proc = null
    this.readyPromise = null
    this.loadedSessions.clear()
    const pending = [...this.pending.values()]
    this.pending.clear()
    for (const p of pending) p.reject(err)
    return true
  }

  /** End `child`: close its stdin, SIGTERM, and SIGKILL when it is still there after the grace period. */
  _kill(child) {
    try {
      child.stdin?.end()
    } catch {}
    // No pid: the spawn failed, there is nothing to signal.
    if (!child.pid || child.exitCode !== null || child.signalCode !== null) return Promise.resolve()
    const gone = new Promise((resolve) => {
      const timer = setTimeout(() => {
        if (child.exitCode !== null || child.signalCode !== null) return
        this.log(`agy acp did not exit ${this.killGraceMs}ms after SIGTERM, killing it`)
        try {
          child.kill('SIGKILL')
        } catch {}
      }, this.killGraceMs)
      timer.unref?.()
      child.once('exit', () => {
        clearTimeout(timer)
        resolve()
      })
      try {
        child.kill('SIGTERM')
      } catch {}
    })
    this.dying.add(gone)
    void gone.then(() => this.dying.delete(gone))
    return gone
  }

  _send(obj) {
    if (this.closed) throw stoppedError()
    if (!this.proc || !this.proc.stdin.writable) {
      throw new Error('agy acp process is not writable')
    }
    this.proc.stdin.write(`${JSON.stringify(obj)}\n`)
  }

  /**
   * Answer a request `child` made. Never throws: when that process is gone (it exited while the phone decided), the
   * answer has nobody to go to and is dropped.
   */
  _reply(child, obj) {
    try {
      if (child && child === this.proc && child.stdin.writable) {
        child.stdin.write(`${JSON.stringify(obj)}\n`)
        return
      }
      this.log(`agy acp: dropped the answer to request ${obj.id}, the agy process that asked is gone`)
    } catch (err) {
      this.log(`agy acp: could not answer request ${obj.id}: ${err?.message || err}`)
    }
  }

  _rawCall(method, params, timeoutMs = 60_000) {
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      const timer = timeoutMs > 0 ? setTimeout(() => {
        this.pending.delete(id)
        reject(new Error(`agy acp ${method} timed out after ${timeoutMs}ms`))
      }, timeoutMs) : null
      this.pending.set(id, {
        resolve: (res) => { clearTimeout(timer); resolve(res) },
        reject: (err) => { clearTimeout(timer); reject(err) },
      })
      try {
        this._send({ jsonrpc: '2.0', id, method, params })
      } catch (err) {
        clearTimeout(timer)
        this.pending.delete(id)
        reject(err)
      }
    })
  }

  /** Send a JSON-RPC notification (no id, no answer). False when no agy process is running to take it. */
  notify(method, params) {
    if (!this.proc || !this.proc.stdin.writable) return false
    try {
      this._send({ jsonrpc: '2.0', method, params })
      return true
    } catch (err) {
      this.log(`agy acp: could not send ${method}: ${err.message}`)
      return false
    }
  }

  async call(method, params, timeoutMs = 60_000) {
    await this.ensureStarted()
    return this._rawCall(method, params, timeoutMs)
  }

  _onMessage(msg, child) {
    if (msg.id !== undefined && (msg.result !== undefined || msg.error !== undefined)) {
      const entry = this.pending.get(msg.id)
      if (!entry) return
      this.pending.delete(msg.id)
      if (msg.error) {
        const err = new Error(msg.error.message || `ACP error ${msg.error.code}`)
        err.code = msg.error.code
        entry.reject(err)
      } else {
        entry.resolve(msg.result)
      }
      return
    }

    if (msg.id !== undefined && msg.method) {
      void this._onServerRequest(msg.id, msg.method, msg.params, child)
      return
    }

    if (msg.method === 'session/update' && msg.params) {
      this.emit('update', msg.params)
    }
  }

  /** Answer a request from the agy process `child`. Never throws: the reply may find that process gone. */
  async _onServerRequest(id, method, params, child) {
    if (child !== this.proc) {
      // It came from a process already dropped (exited, stopped): nobody is left to answer, so don't ask the phone.
      this._reply(child, { jsonrpc: '2.0', id, error: { code: -32000, message: 'agy acp process is gone' } })
      return
    }
    try {
      if (method === 'session/request_permission' && this.onPermissionRequest) {
        const result = await this.onPermissionRequest(params)
        this._reply(child, { jsonrpc: '2.0', id, result })
        return
      }
      // Default fallback for unhandled permission requests: select allow_once if available
      if (method === 'session/request_permission') {
        const opts = Array.isArray(params?.options) ? params.options : []
        const allow = opts.find((o) => o.kind === 'allow_once' || o.kind === 'allow_always') || opts[0]
        this._reply(child, {
          jsonrpc: '2.0',
          id,
          result: allow ? { outcome: { outcome: 'selected', optionId: allow.optionId } } : { outcome: { outcome: 'cancelled' } },
        })
        return
      }
      this._reply(child, { jsonrpc: '2.0', id, error: { code: -32601, message: `Method not found: ${method}` } })
    } catch (err) {
      this._reply(child, { jsonrpc: '2.0', id, error: { code: -32000, message: err?.message || 'Internal error' } })
    }
  }

  async listSessions() {
    const res = await this.call('session/list', {}, 20_000)
    return Array.isArray(res?.sessions) ? res.sessions : []
  }

  async newSession({ cwd, mcpServers = [] }) {
    const res = await this.call('session/new', { cwd, mcpServers }, 30_000)
    if (res?.sessionId) {
      this.loadedSessions.set(res.sessionId, mcpServers.length > 0)
      if (res.models?.availableModels) this.availableModels = res.models.availableModels
    }
    return res
  }

  async ensureSessionLoaded(sessionId, cwd, mcpServers = []) {
    await this.ensureStarted()
    if (this.loadedSessions.has(sessionId) && (mcpServers.length === 0 || this.loadedSessions.get(sessionId))) {
      return
    }
    const res = await this.call('session/resume', { sessionId, cwd: cwd || process.env.HOME || '/tmp', mcpServers }, 30_000)
    this.loadedSessions.set(sessionId, mcpServers.length > 0)
    if (res?.models?.availableModels) this.availableModels = res.models.availableModels
    return res
  }

  async setModel(sessionId, modelId) {
    if (!modelId) return
    await this.call('session/set_model', { sessionId, modelId }, 15_000)
  }

  async setMode(sessionId, modeId) {
    if (!modeId) return
    await this.call('session/set_mode', { sessionId, modeId }, 15_000)
  }

  async setConfigOption(sessionId, configId, value) {
    if (!configId || value === undefined || value === '') return
    await this.call('session/set_config_option', { sessionId, configId, value }, 15_000)
  }

  async prompt(sessionId, promptBlocks) {
    // Turn execution can take minutes
    return this.call('session/prompt', { sessionId, prompt: promptBlocks }, 0)
  }

  // ACP's session/cancel is a notification: the agent ends the running session/prompt with stopReason 'cancelled' and
  // answers nothing else (sent as a request, an agent that follows the spec rejects it and keeps running). With no agy
  // process there is no turn to cancel.
  async cancel(sessionId) {
    this.notify('session/cancel', { sessionId })
  }

  async closeSession(sessionId) {
    this.loadedSessions.delete(sessionId)
    try {
      return await this.call('session/close', { sessionId }, 10_000)
    } catch {
      return {}
    }
  }

  /**
   * Stop for good: pending and later calls fail at once and agy is never started again. agy gets its stdin closed and
   * SIGTERM, and SIGKILL when it is still running after the grace period. Resolves once it is gone, and so is every agy
   * dropped earlier that was still inside its grace period.
   */
  stop() {
    this.closed = true
    const child = this.proc
    if (child) {
      this._forget(child, stoppedError())
      void this._kill(child)
    }
    return Promise.all([...this.dying]).then(() => {})
  }
}

function stoppedError() {
  return new Error('agy acp client stopped')
}
