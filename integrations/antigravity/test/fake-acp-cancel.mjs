// A fake `agy acp` for acp-cancel.test.js that cancels as the ACP spec says (protocol/prompt-turn, "Cancellation"):
// session/cancel is a notification, and a session/cancel request gets -32601 as an agent built on the ACP SDK answers
// it. The notification ends the held session/prompt with stopReason 'cancelled' once every permission request the
// turn made is answered. The prompt text picks what the turn does:
//   'ask'            request permission mid-prompt and wait for the answer
//   'ask-then-end'   request permission, then end the turn without waiting for the answer
//   'ask-on-cancel'  request permission when the cancel arrives (a request that crossed it on the wire)
//   anything else    hold the prompt until it is cancelled
// Every message it receives goes to the --record file as one JSON line, after a first {"started": <pid>} line.
import { appendFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

const SESSION = '0c7d2c4e-5b1a-4f3e-9a6d-2f8b1c3d4e5f'
const recordFile = process.argv[process.argv.indexOf('--record') + 1]
const record = (o) => appendFileSync(recordFile, `${JSON.stringify(o)}\n`)
const send = (o) => process.stdout.write(`${JSON.stringify(o)}\n`)

let nextId = 1
const turns = new Map() // sessionId -> { id, mode, cancelled, open: Set of permission request ids }

function askPermission(sessionId, toolCallId) {
  const id = `perm-${nextId++}`
  send({
    jsonrpc: '2.0',
    id,
    method: 'session/request_permission',
    params: {
      sessionId,
      toolCall: { toolCallId, title: 'run_command', rawInput: { command: 'ls' } },
      options: [
        { optionId: 'allow', name: 'Allow', kind: 'allow_once' },
        { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
      ],
    },
  })
  return id
}

function endIfDone(sessionId) {
  const turn = turns.get(sessionId)
  if (!turn || !turn.cancelled || turn.open.size > 0) return
  turns.delete(sessionId)
  send({ jsonrpc: '2.0', id: turn.id, result: { stopReason: 'cancelled' } })
}

function onRequest(msg) {
  const { id, method, params } = msg
  const answer = (result) => send({ jsonrpc: '2.0', id, result })
  switch (method) {
    case 'initialize':
      return answer({ protocolVersion: 1, agentInfo: { name: 'fake-agy', version: '0.0.0' }, agentCapabilities: {} })
    case 'session/new':
      return answer({ sessionId: SESSION })
    case 'session/resume':
    case 'session/set_model':
    case 'session/set_mode':
    case 'session/set_config_option':
    case 'session/close':
      return answer({})
    case 'session/prompt': {
      const sessionId = params.sessionId
      const mode = params.prompt?.find((b) => b.type === 'text')?.text?.trim() || ''
      const turn = { id, mode, cancelled: false, open: new Set() }
      turns.set(sessionId, turn)
      if (mode === 'ask') turn.open.add(askPermission(sessionId, 'call-1'))
      if (mode === 'ask-then-end') {
        askPermission(sessionId, 'call-1')
        turns.delete(sessionId)
        answer({ stopReason: 'end_turn' })
      }
      return
    }
    default:
      // session/cancel included: it is a notification only.
      return send({ jsonrpc: '2.0', id, error: { code: -32601, message: `Method not found: ${method}` } })
  }
}

function onNotification(msg) {
  if (msg.method !== 'session/cancel') return
  const sessionId = msg.params?.sessionId
  const turn = turns.get(sessionId)
  if (!turn) return
  turn.cancelled = true
  if (turn.mode === 'ask-on-cancel') turn.open.add(askPermission(sessionId, 'call-2'))
  endIfDone(sessionId)
}

function onAnswer(msg) {
  for (const [sessionId, turn] of turns) {
    if (turn.open.delete(msg.id)) endIfDone(sessionId)
  }
}

record({ started: process.pid })
const rl = createInterface({ input: process.stdin, crlfDelay: Infinity })
rl.on('line', (line) => {
  if (!line.trim()) return
  const msg = JSON.parse(line)
  record(msg)
  if (msg.method && msg.id !== undefined) onRequest(msg)
  else if (msg.method) onNotification(msg)
  else onAnswer(msg)
})
rl.on('close', () => process.exit(0))
