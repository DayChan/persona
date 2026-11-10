// A fake `agy acp` for the process lifecycle tests: newline-delimited JSON-RPC on stdio, with the misbehaviours the
// client has to survive. Usage: node fake-acp-process.mjs <pid file> <mode> [marker file]
//   ok                 answers initialize, session/list and session/new; session/prompt never ends
//   permission-exit    like ok, but session/prompt asks for a permission; the test/exit notification ends the process
//   ignore-term        like ok, but ignores SIGTERM and the end of stdin
//   close-stdin        like ok, but session/list closes stdin (writes to it fail with EPIPE) and says so on stderr
//   silent-once        while the marker file is missing (it creates it): never answers, ignores SIGTERM; ok afterwards
//   fail-once          while the marker file is missing (it creates it): answers initialize with an error; ok afterwards
import { appendFileSync, closeSync, existsSync, writeFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

const [pidFile, mode = 'ok', marker] = process.argv.slice(2)
if (pidFile) appendFileSync(pidFile, `${process.pid}\n`)

let first = false
if ((mode === 'silent-once' || mode === 'fail-once') && marker && !existsSync(marker)) {
  writeFileSync(marker, String(process.pid))
  first = true
}
const silent = mode === 'silent-once' && first
const stubborn = mode === 'ignore-term' || silent
if (stubborn) {
  process.on('SIGTERM', () => {})
  setInterval(() => {}, 1 << 30)
}

const send = (msg) => process.stdout.write(`${JSON.stringify(msg)}\n`)

const rl = createInterface({ input: process.stdin, crlfDelay: Infinity })
rl.on('line', (line) => {
  let msg
  try {
    msg = JSON.parse(line)
  } catch {
    return
  }
  if (mode === 'permission-exit' && msg.method === 'test/exit') process.exit(0)
  if (silent || msg.id === undefined || !msg.method) return
  switch (msg.method) {
    case 'initialize':
      if (mode === 'fail-once' && first) {
        send({ jsonrpc: '2.0', id: msg.id, error: { code: -32603, message: 'initialize failed' } })
        return
      }
      send({ jsonrpc: '2.0', id: msg.id, result: { protocolVersion: 1, agentInfo: { name: 'fake-agy', version: '9.9.9' } } })
      return
    case 'session/list':
      if (mode === 'close-stdin') {
        setInterval(() => {}, 1 << 30)
        rl.removeAllListeners('close')
        process.stdin.once('close', () => {
          closeSync(0) // libuv leaves stdio descriptors open on destroy()
          process.stderr.write('stdin closed\n')
        })
        process.stdin.destroy()
        return
      }
      send({ jsonrpc: '2.0', id: msg.id, result: { sessions: [] } })
      return
    case 'session/new':
      send({ jsonrpc: '2.0', id: msg.id, result: { sessionId: `fake-${process.pid}` } })
      return
    case 'session/prompt':
      if (mode === 'permission-exit') {
        const ask = {
          jsonrpc: '2.0',
          id: 'perm-1',
          method: 'session/request_permission',
          params: {
            sessionId: msg.params?.sessionId,
            toolCall: { toolCallId: 'call-1', title: 'run_command', rawInput: { CommandLine: 'true' } },
            options: [{ optionId: 'allow', kind: 'allow_once', name: 'Allow' }],
          },
        }
        send(ask)
      }
      return // a turn that never ends
    default:
      send({ jsonrpc: '2.0', id: msg.id, error: { code: -32601, message: `Method not found: ${msg.method}` } })
  }
})
rl.on('close', () => {
  if (!stubborn) process.exit(0)
})
