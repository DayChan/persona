import assert from 'node:assert/strict'
import { once } from 'node:events'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { fileURLToPath } from 'node:url'
import { AgyAcpClient } from '../lib/acp.js'

const FAKE = fileURLToPath(new URL('./fake-acp-process.mjs', import.meta.url))
const TIMEOUT = { timeout: 15_000 }

/** A client running the fake agy in `mode`; every process it spawned is killed when the test ends. */
function fakeClient(t, mode, options = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-acp-'))
  const pidFile = join(dir, 'pids')
  const logs = []
  const client = new AgyAcpClient({
    bin: process.execPath,
    args: [FAKE, pidFile, mode, join(dir, 'marker')],
    log: (m) => logs.push(m),
    ...options,
  })
  const pids = () => (existsSync(pidFile) ? readFileSync(pidFile, 'utf8').split('\n').filter(Boolean).map(Number) : [])
  t.after(() => {
    const left = new Set([...pids(), client.proc?.pid].filter(Boolean))
    client.stop()
    for (const pid of left) {
      try {
        process.kill(pid, 'SIGKILL')
      } catch {}
    }
    rmSync(dir, { recursive: true, force: true })
  })
  return { client, logs, pids }
}

function alive(pid) {
  try {
    process.kill(pid, 0)
    return true
  } catch (err) {
    return err.code !== 'ESRCH'
  }
}

async function waitFor(cond, ms, what) {
  const deadline = Date.now() + ms
  while (!cond()) {
    if (Date.now() > deadline) throw new Error(`timed out after ${ms}ms waiting for ${what}`)
    await new Promise((r) => setTimeout(r, 20))
  }
}

function watchUnhandled(t) {
  const seen = []
  const on = (err) => seen.push(err)
  process.on('unhandledRejection', on)
  process.on('uncaughtException', on)
  t.after(() => {
    process.off('unhandledRejection', on)
    process.off('uncaughtException', on)
  })
  return seen
}

describe('agy acp process lifecycle', () => {
  it('a missing agy fails the start instead of crashing the process', TIMEOUT, async (t) => {
    const crashes = watchUnhandled(t)
    const client = new AgyAcpClient({ bin: '/nonexistent/agy', log: () => {} })
    t.after(() => client.stop())
    await assert.rejects(client.ensureStarted(), /ENOENT/)
    assert.equal(client.proc, null)
    // Forgotten: the next call tries again, and fails the same way.
    await assert.rejects(client.listSessions(), /ENOENT/)
    assert.equal(client.proc, null)
    assert.deepEqual(crashes, [])
  })

  it('a permission answered after agy exited is dropped instead of thrown', TIMEOUT, async (t) => {
    const crashes = watchUnhandled(t)
    let answered
    const done = new Promise((r) => {
      answered = r
    })
    const { client, logs } = fakeClient(t, 'permission-exit', {
      onPermissionRequest: async () => {
        // agy goes away while the phone decides.
        const child = client.proc
        const gone = once(child, 'exit')
        child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'test/exit' })}\n`)
        await gone
        answered()
        return { outcome: { outcome: 'selected', optionId: 'allow' } }
      },
    })
    await assert.rejects(client.prompt('s1', [{ type: 'text', text: 'hi' }]), /exited/)
    await done
    await waitFor(() => logs.some((m) => /dropped/.test(m)), 2000, 'the dropped reply')
    await new Promise((r) => setTimeout(r, 50))
    assert.deepEqual(crashes, [])
    assert.equal(logs.filter((m) => /dropped/.test(m)).length, 1)
  })

  it('a write to an agy that closed its stdin fails the calls instead of crashing', TIMEOUT, async (t) => {
    const crashes = watchUnhandled(t)
    const { client, logs } = fakeClient(t, 'close-stdin')
    await client.ensureStarted()
    const first = client.listSessions()
    await waitFor(() => logs.some((m) => /stdin closed/.test(m)), 3000, 'the fake to close its stdin')
    const second = client.newSession({ cwd: '/tmp' })
    await assert.rejects(first, /EPIPE/)
    await assert.rejects(second, /EPIPE/)
    assert.equal(client.proc, null)
    assert.deepEqual(crashes, [])
  })

  it('stop() fails pending and later calls and never spawns agy again', TIMEOUT, async (t) => {
    const { client, pids } = fakeClient(t, 'ok')
    await client.ensureStarted()
    const pid = client.proc.pid
    const turn = client.prompt('s1', [{ type: 'text', text: 'hi' }]) // never answered
    await waitFor(() => client.pending.size === 1, 2000, 'the prompt to be sent')
    const next = client.listSessions() // still waiting for its start when stop() comes
    const stopped = client.stop()
    await assert.rejects(turn, /stopped/)
    await assert.rejects(next, /stopped/)
    await assert.rejects(client.listSessions(), /stopped/)
    await assert.rejects(client.ensureStarted(), /stopped/)
    assert.equal(client.proc, null)
    await stopped
    assert.equal(alive(pid), false)
    assert.deepEqual(pids(), [pid])
  })

  it('stop() kills an agy that ignores SIGTERM after the grace period', TIMEOUT, async (t) => {
    const grace = 300
    const { client } = fakeClient(t, 'ignore-term', { killGraceMs: grace })
    await client.ensureStarted()
    const pid = client.proc.pid
    client.stop()
    await waitFor(() => !alive(pid), grace + 1000, `agy ${pid} to be gone`)
  })

  it('stop() also waits for an agy that a failed initialize is still ending', TIMEOUT, async (t) => {
    const grace = 300
    const { client } = fakeClient(t, 'silent-once', { initTimeoutMs: 200, killGraceMs: grace })
    const starting = client.ensureStarted()
    const stale = client.proc.pid
    await assert.rejects(starting, /initialize timed out/)
    assert.equal(client.proc, null)
    assert.equal(alive(stale), true) // it ignores SIGTERM and is inside its grace period
    await client.stop()
    assert.equal(alive(stale), false)
  })

  for (const [mode, why] of [
    ['silent-once', /initialize timed out/],
    ['fail-once', /initialize failed/],
  ]) {
    it(`an initialize that fails (${mode}) kills that agy and the next call starts a fresh one`, TIMEOUT, async (t) => {
      const { client, pids } = fakeClient(t, mode, { initTimeoutMs: 300, killGraceMs: 300 })
      const starting = client.ensureStarted()
      const stale = client.proc.pid
      await assert.rejects(starting, why)
      assert.equal(client.proc, null)
      // The next call starts a new agy at once, while the old one may still be dying.
      assert.deepEqual(await client.listSessions(), [])
      const fresh = client.proc.pid
      assert.notEqual(fresh, stale)
      assert.equal(client.agentVersion, '9.9.9')
      await waitFor(() => !alive(stale), 2000, `the failed agy ${stale} to be killed`)
      // The old process's exit leaves the new one alone.
      assert.equal(client.proc?.pid, fresh)
      assert.deepEqual(await client.listSessions(), [])
      assert.deepEqual(pids(), [stale, fresh])
    })
  }
})
