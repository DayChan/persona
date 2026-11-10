import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createPaseoBridge, TIMELINES_MAX, uuidOf } from '../lib/bridge.js'
import { PaseoClient } from '../lib/paseo.js'
import { agentSnapshot, entry, startFakePaseo } from './fake-paseo.js'

const SID = 'aaaaaaaa-0000-4000-8000-000000000001'
const SID2 = 'aaaaaaaa-0000-4000-8000-000000000002'
const FEATURES = ['prefer_running', 'resume_argv', 'handover', 'transcript', 'launchers', 'start_params', 'modes', 'resume']

const PROVIDERS = { claude: { enabled: false }, 'my-claude': { extends: 'claude', label: 'My Claude' }, 'my-codex': { extends: 'codex' }, grok: { extends: 'acp', label: 'Grok' } }
const ENTRIES = [
  { provider: 'claude', enabled: false, status: 'unavailable', models: [] },
  { provider: 'my-claude', enabled: true, status: 'ready', label: 'My Claude', models: [
    { id: 'fable[1m]', label: 'Fable', isDefault: true, thinkingOptions: [{ id: 'low' }, { id: 'high' }, { id: 'max' }], defaultThinkingOptionId: 'max' },
  ] },
  { provider: 'my-codex', enabled: true, status: 'ready', label: 'My Codex', models: [{ id: 'gpt', label: 'GPT', isDefault: true }] },
  { provider: 'grok', enabled: true, status: 'ready', label: 'Grok', models: [] },
]
const PROJECT = { projectKey: 'prj_1', projectName: 'proj', workspaceName: '主线', checkout: { cwd: '/tmp', isGit: false } }

function agents() {
  const claude = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'A', status: 'idle', lastUserMessageAt: '2026-11-04T00:00:00Z',
    persistence: { provider: 'my-claude', sessionId: SID, metadata: { provider: 'claude' } }, runtimeInfo: { provider: 'claude', sessionId: SID },
    availableModes: [{ id: 'default' }, { id: 'acceptEdits' }, { id: 'plan' }, { id: 'bypassPermissions' }], model: 'fable[1m]' })
  const codex = agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: 'B', status: 'running', persistence: { provider: 'my-codex', sessionId: 'th-1', metadata: { provider: 'codex' } } })
  const grok = agentSnapshot({ provider: 'grok', cwd: '/tmp', title: 'C', status: 'closed' })
  const fresh = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: null })
  const labeled = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'E', labels: { 'persona.session': 'paseo-e1' },
    persistence: { provider: 'my-claude', sessionId: SID2, metadata: { provider: 'claude' } } })
  return { claude, codex, grok, fresh, labeled }
}

async function setup(t, o = {}) {
  const a = agents()
  const fake = await startFakePaseo(t, { providers: PROVIDERS, entries: ENTRIES, agents: Object.values(a).map((agent) => ({ agent, project: PROJECT })), ...o })
  const node = await startFakeNode(t, { features: FEATURES })
  const home = mkdtempSync('/tmp/ppaseo-')
  t.after(() => rmSync(home, { recursive: true, force: true }))
  // Where Paseo's own CLI finds its daemon: the address in the home's paseo.pid.
  writeFileSync(join(home, 'paseo.pid'), JSON.stringify({ pid: process.pid, listen: `127.0.0.1:${fake.port}` }))
  const logs = []
  const paseo = new PaseoClient({ home, retryMin: 30, retryMax: 100, log: (m) => logs.push(m) })
  const bridge = createPaseoBridge({ paseo, log: (m) => logs.push(m) },
    { socketPath: node.path, retryMs: 30, pushDelayMs: 5, refreshMs: 0, awayGrace: o.awayGrace ?? 400, callWait: 1500 })
  bridge.start()
  t.after(() => bridge.stop())
  await node.ready
  await until('the first list', () => node.last('runtime.sessions')?.sessions?.length >= 4)
  return { fake, node, bridge, paseo, logs, a, rows: () => node.last('runtime.sessions').sessions }
}

const call = (node, method, params) => node.peer.call(method, params)

test("Paseo's agents are the engine's sessions: Claude Code's by its session and transcript, the others by agent", async (t) => {
  const { node, a, rows } = await setup(t)
  assert.equal(node.hello.runtime, 'paseo')
  assert.equal(node.hello.name, 'Paseo')
  assert.equal(node.hello.version, '0.14.0', "Paseo's version")
  assert.deepEqual(node.hello.caps, { start: true, send: true, interrupt: true, rename: true, close: true, assistant: false, modes: true, resume: true })
  assert.deepEqual(node.hello.launchers.map((l) => [l.name, l.agent]), [['my-claude', 'claude'], ['my-codex', 'codex'], ['grok', 'paseo']])
  assert.deepEqual(node.hello.launchers[0].models, [{ id: 'fable[1m]', name: 'Fable', default: true, efforts: ['low', 'high', 'max'], default_effort: 'max' }])
  const byId = Object.fromEntries(rows().map((r) => [r.session_id, r]))
  assert.deepEqual(Object.keys(byId).sort(), [SID, 'paseo-e1', `paseo-${a.codex.id}`, `paseo-${a.grok.id}`].sort(), 'a Claude agent without a session yet is not listed')
  assert.deepEqual([byId[SID].agent, byId[SID].transcript, byId[SID].status, byId[SID].launcher], ['claude', SID, 'done', 'my-claude'])
  assert.deepEqual([byId[SID].space_id, byId[SID].space_name, byId[SID].slot_id, byId[SID].slot_name], ['prj_1', 'proj', 'wks_1', '主线'],
    "the project is the space, Paseo's workspace the slot")
  assert.deepEqual([byId['paseo-e1'].transcript, byId['paseo-e1'].title], [SID2, 'E'])
  assert.deepEqual([byId[`paseo-${a.codex.id}`].agent, byId[`paseo-${a.codex.id}`].status, byId[`paseo-${a.codex.id}`].transcript], ['codex', 'working', undefined])
  assert.deepEqual([byId[`paseo-${a.grok.id}`].agent_label, byId[`paseo-${a.grok.id}`].status], ['Grok', 'offline'])
})

test('changes in Paseo move the list: a new agent, a status, one archived', async (t) => {
  const { fake, node, a, rows } = await setup(t)
  const n = agentSnapshot({ provider: 'grok', cwd: '/tmp', title: 'new', status: 'running' })
  fake.upsert(n, PROJECT)
  await until('the new agent listed', () => rows().some((r) => r.session_id === `paseo-${n.id}` && r.status === 'working'))
  fake.update(a.codex.id, { status: 'idle', lastUserMessageAt: '2026-11-04T01:00:00Z' })
  await until('the codex agent done', () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.status === 'done')
  fake.update(a.fresh.id, { runtimeInfo: { provider: 'claude', sessionId: 'bbbbbbbb-0000-4000-8000-000000000003' } })
  await until('the Claude agent listed once it has a session', () => rows().some((r) => r.session_id === 'bbbbbbbb-0000-4000-8000-000000000003' && !r.transcript))
  await call(node, 'session.close', { session_id: `paseo-${n.id}` })
  assert.equal(fake.requestsOf('archive_agent_request').at(-1).agentId, n.id)
  await until('the archived agent gone', () => !rows().some((r) => r.session_id === `paseo-${n.id}`))
})

test("an agent's timeline as items, its live text, and what it writes next while watched", async (t) => {
  const { fake, node, a } = await setup(t)
  const id = `paseo-${a.codex.id}`
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '跑测试', messageId: 'u1' }),
    entry(2, { type: 'tool_call', callId: 'c1', name: 'shell', status: 'running', error: null, detail: { type: 'shell', command: 'go test ./...' } }),
  ])
  const p = await call(node, 'session.items', { session_id: id, turns: 3 })
  assert.deepEqual(p.items.map((i) => [i.id, i.kind]), [['1#0', 'text'], ['2#0', 'tool_use']])
  assert.equal(p.items[1].tool_name, 'Bash')
  assert.equal(p.history, `paseo:${a.codex.id}:ep1`)

  await call(node, 'session.watch', { session_id: id, since_seq: 0 })
  await until('the timeline followed', () => fake.requestsOf('agent.timeline.set_subscription.request').some((r) => r.agentIds[0] === a.codex.id))
  await until('the subscription made', () => [...fake.subs.values()].some((s) => s.kind === 'timeline'))
  fake.stream(a.codex.id, { type: 'timeline', provider: 'codex', turnId: 't1', item: { type: 'assistant_message', text: '测试', messageId: 'm1' } }, 5)
  fake.stream(a.codex.id, { type: 'timeline', provider: 'codex', turnId: 't1', item: { type: 'assistant_message', text: '都过了', messageId: 'm1' } }, 6)
  await until('the live text', () => node.all('session.delta').some((d) => d.session_id === id && d.text === '测试都过了'))

  // The tool finished and the text was written: the next read pushes them, from where the watch began.
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '跑测试', messageId: 'u1' }),
    entry(2, { type: 'tool_call', callId: 'c1', name: 'shell', status: 'completed', error: null, detail: { type: 'shell', command: 'go test ./...', output: 'ok', exitCode: 0 } }, 4),
    entry(5, { type: 'assistant_message', text: '测试都过了' }, 6),
  ])
  fake.update(a.codex.id, { status: 'idle', lastUserMessageAt: '2026-11-04T01:00:00Z' })
  fake.stream(a.codex.id, { type: 'turn_completed', provider: 'codex', turnId: 't1' })
  const pushed = await until('the new items', () => node.all('session.items').filter((x) => x.session_id === id).flatMap((x) => x.items).length >= 2 &&
    node.all('session.items').filter((x) => x.session_id === id))
  const all = pushed.flatMap((x) => x.items)
  assert.deepEqual(all.map((i) => [i.id, i.kind]), [['4#0', 'tool_result'], ['5#0', 'text']])
  assert.equal(pushed[0].from_seq, 2, 'continuous with where the watch began')
  assert.equal(pushed.at(-1).to_seq, 6)
  assert.ok(node.all('session.delta').some((d) => d.session_id === id && d.text === ''), 'the live text ends')
  await call(node, 'session.unwatch', { session_id: id })
  await until('the subscription released', () => fake.requestsOf('subscription.release.request').length > 0)
})

test('whole timelines are kept for the watched agents and the most recently read ones only', async (t) => {
  const many = Array.from({ length: 40 }, (_, i) => agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: `T${i}`, status: 'idle' }))
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { fake, node, bridge } = await setup(t, { agents: [...base, ...many.map((agent) => ({ agent, project: PROJECT }))] })
  for (const [i, x] of many.entries()) {
    fake.setTimeline(x.id, [entry(1, { type: 'user_message', text: `question ${i}`, messageId: `u${i}` }), entry(2, { type: 'assistant_message', text: `answer ${i}` })])
  }
  const sid = (x) => `paseo-${x.id}`
  const reads = (x) => fake.requestsOf('fetch_agent_timeline_request').filter((r) => r.agentId === x.id && r.limit === 0).length
  const kept = bridge.state.timelines
  const [watched, ...others] = many
  const first = await call(node, 'session.items', { session_id: sid(watched), turns: 3 })
  await call(node, 'session.watch', { session_id: sid(watched), since_seq: 0 })
  const pages = []
  for (const x of others) pages.push(await call(node, 'session.items', { session_id: sid(x), turns: 3 }))
  assert.ok(kept.size <= TIMELINES_MAX, `${kept.size} whole timelines kept after reading ${many.length} agents`)
  assert.ok(kept.has(watched.id), "the watched agent's is kept, though read first")
  assert.ok(!kept.has(others[0].id) && kept.has(others.at(-1).id), 'the least recently read go first')

  // A read from the cache counts as recent: the next one to go is the one after it.
  const oldest = others.find((x) => kept.has(x.id))
  const next = others[others.indexOf(oldest) + 1]
  const before = reads(oldest)
  await call(node, 'session.items', { session_id: sid(oldest), turns: 3 })
  assert.equal(reads(oldest), before, 'read from the cache')
  const again = await call(node, 'session.items', { session_id: sid(others[0]), turns: 3 })
  assert.deepEqual(again, pages[0], 'one that went is read from Paseo again, the same')
  assert.ok(kept.has(oldest.id) && !kept.has(next.id), 'the one read from the cache stays; the next least recently read went')
  assert.ok(kept.size <= TIMELINES_MAX)

  // The watched session pages from its kept timeline, as before.
  const watchedReads = reads(watched)
  assert.deepEqual(await call(node, 'session.items', { session_id: sid(watched), turns: 3 }), first)
  assert.equal(reads(watched), watchedReads, 'not read from Paseo again')
  assert.deepEqual(first.items.map((i) => i.text), ['question 0', 'answer 0'])
})

test('a rewind in Paseo goes to the phone as another history, even when the timeline got shorter', async (t) => {
  const { fake, node, a } = await setup(t)
  const id = `paseo-${a.codex.id}`
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '一', messageId: 'u1' }),
    entry(2, { type: 'assistant_message', text: '答一' }),
    entry(3, { type: 'user_message', text: '二', messageId: 'u2' }),
    entry(4, { type: 'assistant_message', text: '答二' }),
  ])
  await call(node, 'session.watch', { session_id: id, since_seq: 0 })
  await until('the subscription made', () => [...fake.subs.values()].some((s) => s.kind === 'timeline'))
  // Rewound to before the second entry: a new epoch, two entries.
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '一', messageId: 'u1' }),
    entry(2, { type: 'assistant_message', text: '答一' }),
  ], 'ep2')
  fake.emit('timeline', { type: 'agent.timeline.replacement', payload: { agentId: a.codex.id, epoch: 'ep2' } }, (s) => s.agentIds.includes(a.codex.id))
  const b = await until('another history', () => node.all('session.items').find((x) => x.session_id === id && x.history === `paseo:${a.codex.id}:ep2`))
  assert.ok(b.to_seq <= 2, 'the shorter timeline, not the old end')
  // What it writes next goes on from there.
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '一', messageId: 'u1' }),
    entry(2, { type: 'assistant_message', text: '答一' }),
    entry(3, { type: 'user_message', text: '二，重来', messageId: 'u3' }),
  ], 'ep2')
  fake.stream(a.codex.id, { type: 'timeline', provider: 'codex', turnId: 't2', item: { type: 'user_message', text: '二，重来', messageId: 'u3' } }, 3)
  const next = await until('the next entry', () => node.all('session.items').find((x) => x.session_id === id && x.items.some((i) => i.text === '二，重来')))
  assert.equal(next.history, `paseo:${a.codex.id}:ep2`)
  assert.equal(next.from_seq, b.to_seq)
})

test("a Claude Code session's watch is its live text only: the node reads the transcript", async (t) => {
  const { fake, node, a } = await setup(t)
  await call(node, 'session.watch', { session_id: SID, since_seq: 0, transcript: true })
  await until('the subscription made', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds[0] === a.claude.id))
  fake.stream(a.claude.id, { type: 'timeline', provider: 'claude', turnId: 't', item: { type: 'assistant_message', text: '好的', messageId: 'msg_1' } }, 10)
  await until('the live text', () => node.all('session.delta').some((d) => d.session_id === SID && d.text === '好的'))
  fake.stream(a.claude.id, { type: 'timeline', provider: 'claude', turnId: 't', item: { type: 'tool_call', callId: 'toolu_1', name: 'Bash', status: 'running', error: null, detail: { type: 'shell', command: 'ls' } } }, 11)
  await until('it ends at the tool call', () => node.all('session.delta').some((d) => d.session_id === SID && d.text === ''))
  await new Promise((r) => setTimeout(r, 300))
  assert.equal(node.all('session.items').filter((x) => x.session_id === SID).length, 0, 'no items: the transcript has them')
  assert.equal(fake.requestsOf('fetch_agent_timeline_request').filter((r) => r.agentId === a.claude.id).length, 0, 'its timeline is not read')
})

test('a watched session another agent takes on: the watch follows that agent, and its content is another history', async (t) => {
  const { fake, node, bridge, a } = await setup(t)
  // A Claude session (its items are the transcript's): the live text of the agent that has it now.
  fake.update(a.claude.id, { status: 'closed' })
  await until('closed', () => bridge.state.agents.get(a.claude.id)?.agent.status === 'closed')
  await call(node, 'session.watch', { session_id: SID, since_seq: 0, transcript: true })
  await until('followed', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds.includes(a.claude.id)))
  const b = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'A again', status: 'running',
    persistence: { provider: 'my-claude', sessionId: SID, metadata: { provider: 'claude' } }, runtimeInfo: { provider: 'claude', sessionId: SID } })
  fake.upsert(b, PROJECT)
  await until('the new agent followed', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds.includes(b.id)))
  assert.equal(bridge.state.watches.get(SID).agentId, b.id)
  assert.ok(fake.requestsOf('subscription.release.request').length > 0, 'the old agent is let go')
  fake.stream(b.id, { type: 'timeline', provider: 'claude', turnId: 't1', item: { type: 'assistant_message', text: '接着说', messageId: 'm1' } }, 5)
  await until('its live text', () => node.all('session.delta').some((d) => d.session_id === SID && d.text === '接着说'))

  // An agent whose items are its timeline: the new one's goes out as another history.
  const id = 'paseo-e1' // the labeled agent's session
  fake.setTimeline(a.labeled.id, [entry(1, { type: 'user_message', text: '一', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: '答一' }),
    entry(3, { type: 'user_message', text: '二', messageId: 'u2' })])
  await call(node, 'session.watch', { session_id: id, since_seq: 0 })
  await until('followed', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds.includes(a.labeled.id)))
  fake.update(a.labeled.id, { status: 'closed' })
  const c = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'E again', status: 'running', labels: { 'persona.session': id },
    persistence: { provider: 'my-claude', sessionId: SID2, metadata: { provider: 'claude' } } })
  fake.setTimeline(c.id, [entry(1, { type: 'user_message', text: '一', messageId: 'u1' })])
  fake.upsert(c, PROJECT)
  const pushed = await until('another history', () => node.all('session.items').find((x) => x.session_id === id && x.history?.startsWith(`paseo:${c.id}:`)))
  assert.equal(pushed.to_seq, 1)
  assert.equal(pushed.from_seq, undefined, 'not a continuation of the old agent')
  // The phone reads the new agent's page, then it writes on: pushed from its own seq, not the old agent's.
  await call(node, 'session.items', { session_id: id, turns: 3 })
  fake.setTimeline(c.id, [entry(1, { type: 'user_message', text: '一', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: '新答' })])
  fake.update(c.id, { status: 'idle' })
  fake.stream(c.id, { type: 'turn_completed', provider: 'claude', turnId: 't2' })
  const next = await until('the new agent writes on', () => node.all('session.items').find((x) => x.session_id === id && x.items.some((i) => i.text === '新答')))
  assert.equal(next.from_seq, 1)
  assert.equal(next.history, pushed.history)
})

test('sending: into the running turn, once per message, images as Paseo takes them', async (t) => {
  const { fake, node, a } = await setup(t)
  await call(node, 'session.send', { session_id: SID, text: '继续', client_msg_id: 'cm-1', images: [{ mime: 'image/png', data: 'iVBO' }] })
  await call(node, 'session.send', { session_id: SID, text: '继续', client_msg_id: 'cm-1' })
  const sent = fake.requestsOf('send_agent_message_request')
  assert.equal(sent.length, 1, 'a retry of the same message is not sent again')
  assert.deepEqual({ ...sent[0], requestId: undefined }, { type: 'send_agent_message_request', requestId: undefined, agentId: a.claude.id, text: '继续',
    messageId: uuidOf(`persona:${a.claude.id}:cm-1`), activeTurnBehavior: 'steer', images: [{ data: 'iVBO', mimeType: 'image/png' }] })
  fake.handlers.send_agent_message_request = () => ({ payload: { agentId: a.claude.id, accepted: false, error: 'Agent is archived' } })
  await assert.rejects(call(node, 'session.send', { session_id: SID, text: 'x', client_msg_id: 'cm-2' }), /Paseo 没有收下这条消息：Agent is archived/)
  await assert.rejects(call(node, 'session.send', { session_id: 'paseo-nope', text: 'x' }), /没有这个会话/)
  // Paseo slow to start the run (a closed agent loading): the send is left with it, not failed.
  fake.handlers.send_agent_message_request = () => null
  const t0 = Date.now()
  await call(node, 'session.send', { session_id: SID, text: '慢', client_msg_id: 'cm-3' })
  assert.ok(Date.now() - t0 >= 1400, 'waited for Paseo first')
})

test('interrupt, rename, permission mode and effort go to the agent', async (t) => {
  const { fake, node, a } = await setup(t)
  await call(node, 'session.interrupt', { session_id: SID })
  assert.equal(fake.requestsOf('cancel_agent_request')[0].agentId, a.claude.id)
  await call(node, 'session.rename', { session_id: SID, title: '新名字' })
  assert.equal(fake.requestsOf('update_agent_request')[0].name, '新名字')
  await call(node, 'session.modes', { session_id: SID, permission_mode: 'plan', effort: 'high' })
  assert.deepEqual([fake.requestsOf('set_agent_mode_request')[0].modeId, fake.requestsOf('set_agent_thinking_request')[0].thinkingOptionId], ['plan', 'high'])
  await assert.rejects(call(node, 'session.modes', { session_id: SID, effort: 'xhigh' }), /没有「xhigh」/)
  await assert.rejects(call(node, 'session.modes', { session_id: SID, permission_mode: 'auto' }), /没有「auto」模式/)
  await assert.rejects(call(node, 'session.modes', { session_id: `paseo-${a.codex.id}`, permission_mode: 'plan' }), /没有这个权限模式/)
  // Codex: Persona's bypassPermissions is Paseo's full-access; the other modes are Paseo's own (left alone).
  await call(node, 'session.modes', { session_id: `paseo-${a.codex.id}`, permission_mode: 'bypassPermissions' })
  assert.equal(fake.requestsOf('set_agent_mode_request').at(-1).modeId, 'full-access')
})

test('a new session: an agent of the launcher\'s provider, with the form\'s choices, under an id kept on it', async (t) => {
  const { fake, node, rows } = await setup(t)
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: '修一下登录', launcher: 'my-claude', model: 'fable[1m]', permission_mode: 'acceptEdits', effort: 'high', workspace_id: 'wks_1' })
  const req = fake.requestsOf('agent.create.request')[0]
  assert.deepEqual(req.config, { provider: 'my-claude', cwd: '/tmp', model: 'fable[1m]', modeId: 'acceptEdits', thinkingOptionId: 'high' })
  assert.equal(req.initialPrompt, '修一下登录')
  assert.equal(req.workspaceId, 'wks_1')
  assert.equal(res.session.session_id, `paseo-${req.agentId}`)
  assert.equal(req.labels['persona.session'], res.session.session_id)
  assert.equal(res.session.agent, 'claude')
  await until('listed', () => rows().some((r) => r.session_id === res.session.session_id))
  // A launcher of the login shell's that Paseo does not have: Paseo's default provider.
  await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'cc_gateway', shell_launcher: true })
  assert.equal(fake.requestsOf('agent.create.request')[1].config.provider, 'my-claude')
  await assert.rejects(call(node, 'session.start', { cwd: '/nonexistent/dir', prompt: 'x' }), /没有这个目录/)
  fake.handlers['agent.create.request'] = () => ({ payload: { agent: null, error: 'Provider unavailable' } })
  await assert.rejects(call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'grok' }), /Paseo 没能新建：Provider unavailable/)
})

test("a new Codex session: bypassPermissions starts it with Paseo's full-access mode; the other modes are left alone", async (t) => {
  const { fake, node } = await setup(t)
  const start = (permission_mode) => call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-codex', permission_mode })
    .then(() => fake.requestsOf('agent.create.request').at(-1).config)
  assert.equal((await start('bypassPermissions')).modeId, 'full-access')
  assert.equal((await start('auto')).modeId, undefined)
  assert.equal((await start('default')).modeId, undefined)
})

test("完成未看 follows Paseo: seen there, seen here; a new turn to look at, unread again", async (t) => {
  const { fake, a, rows } = await setup(t)
  const status = () => rows().find((r) => r.session_id === SID)?.status
  assert.equal(status(), 'done', 'a Paseo without the flag: done after a turn')
  fake.update(a.claude.id, { requiresAttention: false, attentionReason: null })
  await until('seen in Paseo', () => status() === 'idle')
  fake.update(a.claude.id, { requiresAttention: true, attentionReason: 'finished' })
  await until('a turn to look at', () => status() === 'done')
})

test('continuing a Claude session: the agent Paseo has, else Paseo imports it under the same id', async (t) => {
  const { fake, node, a } = await setup(t)
  const mine = await call(node, 'session.start', { cwd: '/tmp', resume: SID, open_elsewhere: true })
  assert.equal(mine.session.session_id, SID, "Paseo's own agent: taken as it is, even with its claude running")
  assert.equal(fake.requestsOf('import_agent_request').length, 0)
  const other = 'cccccccc-0000-4000-8000-000000000009'
  const res = await call(node, 'session.start', { cwd: '/tmp', resume: other })
  const imp = fake.requestsOf('import_agent_request')[0]
  assert.deepEqual([imp.provider, imp.sessionId, imp.cwd, imp.labels['persona.session']], [a.claude.provider, other, '/tmp', other])
  assert.equal(res.session.session_id, other)
  assert.equal(res.session.transcript, other)
  const err = await call(node, 'session.start', { cwd: '/tmp', resume: 'dddddddd-0000-4000-8000-000000000004', open_elsewhere: true }).catch((e) => e)
  assert.equal(err.code, 'open_elsewhere', 'a claude Paseo does not run has it: no second writer')
})

test("a project's worktrees are one space; a new session at a worktree goes into Paseo's workspace there", async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-wt-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const app = join(dir, 'app')
  const wt = join(dir, 'brave-fox')
  mkdirSync(app)
  mkdirSync(wt)
  const key = 'prj_app' // the placement's projectKey: Paseo's project record id
  const checkout = (cwd, o = {}) => ({ cwd, isGit: true, currentBranch: null, remoteUrl: null, worktreeRoot: cwd,
    isPaseoOwnedWorktree: o.paseo ?? false, mainRepoRoot: o.main ?? null })
  const placed = [
    { agent: agentSnapshot({ provider: 'my-codex', cwd: app, workspaceId: 'wks_main', title: 'M', updatedAt: '2026-11-01T00:00:00Z' }),
      project: { projectKey: key, projectName: 'app', workspaceName: '主线', checkout: checkout(app) } },
    { agent: agentSnapshot({ provider: 'my-codex', cwd: wt, workspaceId: 'wks_wt', title: 'W', updatedAt: '2026-11-02T00:00:00Z' }),
      project: { projectKey: key, projectName: 'app', workspaceName: '修登录', checkout: checkout(wt, { paseo: true, main: app }) } },
    // A later record Paseo made when handed the worktree's directory alone (#2802): a project of its own, named after the folder.
    { agent: agentSnapshot({ provider: 'my-codex', cwd: wt, workspaceId: 'wks_stray', title: 'S', updatedAt: '2026-11-03T00:00:00Z' }),
      project: { projectKey: 'prj_stray', projectName: 'brave-fox', workspaceName: null, checkout: checkout(wt, { paseo: true, main: app }) } },
  ]
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { fake, node, rows } = await setup(t, { agents: [...base, ...placed] })
  await until('the worktree agents', () => ['M', 'W', 'S'].every((title) => rows().some((r) => r.title === title)))
  const byTitle = Object.fromEntries(rows().filter((r) => r.space_id === key).map((r) => [r.title, r]))
  assert.deepEqual(['M', 'W', 'S'].map((x) => [byTitle[x].space_name, byTitle[x].slot_id, byTitle[x].slot_name]),
    [['app', 'wks_main', '主线'], ['app', 'wks_wt', '修登录'], ['app', 'wks_stray', 'brave-fox']],
    "one space named after the repository's project, the stray record's too; each workspace a slot")
  assert.deepEqual(['M', 'W', 'S'].map((x) => byTitle[x].repo_root), [app, app, app], "按仓库: the repository's main checkout (#2840)")

  const started = (params) => call(node, 'session.start', { prompt: 'x', launcher: 'my-codex', ...params })
    .then(() => fake.requestsOf('agent.create.request').at(-1))
  assert.equal((await started({ cwd: wt, workspace_id: key })).workspaceId, 'wks_wt', "the project's space and a worktree: its workspace")
  assert.equal((await started({ cwd: wt })).workspaceId, 'wks_wt', 'a worktree alone: its workspace under the repository, not the stray record')
  assert.equal((await started({ cwd: app, workspace_id: key })).workspaceId, undefined, 'the checkout: a new workspace of the project')
  assert.equal((await started({ cwd: app, workspace_id: 'wks_main' })).workspaceId, 'wks_main', 'a workspace named (older apps): that one')
  const other = 'cccccccc-0000-4000-8000-00000000000a'
  await call(node, 'session.start', { cwd: wt, resume: other })
  assert.equal(fake.requestsOf('import_agent_request').at(-1).workspaceId, 'wks_wt', 'continuing a session at a worktree: its workspace')
  // Paseo slow to answer (its provider starting): the row it will have is in its project already.
  fake.handlers['agent.create.request'] = () => null
  const slow = (params) => call(node, 'session.start', { prompt: 'x', launcher: 'my-codex', ...params }).then((r) => r.session)
  const inWt = await slow({ cwd: wt })
  assert.deepEqual([inWt.space_id, inWt.space_name, inWt.slot_id, inWt.slot_name], [key, 'app', 'wks_wt', '修登录'])
  const inApp = await slow({ cwd: app, workspace_id: key })
  assert.deepEqual([inApp.space_id, inApp.space_name, inApp.slot_id], [key, 'app', undefined], 'a new workspace of the project: no slot yet')
})

test("cards: Paseo's requests go to the phone and its answers back; one answered in Paseo leaves the phone", async (t) => {
  const { fake, node, a } = await setup(t)
  let answer
  node.resolve = (ix) => answer(ix)
  answer = () => ({ allow: true })
  fake.permission(a.claude.id, { id: 'permission-1', provider: 'claude', name: 'Bash', kind: 'tool', input: { command: 'npm test' }, metadata: { toolUseId: 'toolu_1' } })
  await until('the response', () => fake.requestsOf('agent_permission_response').length === 1)
  assert.deepEqual(node.asked[0].tool_input, { command: 'npm test' })
  assert.deepEqual([node.asked[0].kind, node.asked[0].session_id, node.asked[0].tool_name, node.asked[0].tool_use_id], ['permission', SID, 'Bash', 'toolu_1'])
  const r = fake.requestsOf('agent_permission_response')[0]
  assert.deepEqual([r.agentId, r.requestId, r.response], [a.claude.id, 'permission-1', { behavior: 'allow' }])

  answer = (ix) => ({ answers: { [ix.questions[0].question]: 'B' } })
  fake.permission(a.claude.id, { id: 'permission-2', provider: 'claude', name: 'AskUserQuestion', kind: 'question',
    input: { questions: [{ question: '选哪个？', header: '选择', multiSelect: false, options: [{ label: 'A' }, { label: 'B' }] }] } })
  await until('the answer', () => fake.requestsOf('agent_permission_response').length === 2)
  assert.deepEqual(fake.requestsOf('agent_permission_response')[1].response, { behavior: 'allow', updatedInput: { answers: { '选哪个？': 'B' } } })

  // Nobody answers on the phone; Paseo's own app does: the card is taken off the phone.
  answer = () => new Promise(() => {})
  fake.permission(a.claude.id, { id: 'permission-3', provider: 'claude', name: 'Edit', kind: 'tool', input: { file_path: '/a' } })
  await until('on the phone', () => node.asked.length === 3)
  fake.resolvePermission(a.claude.id, 'permission-3', { behavior: 'allow' })
  await until('withdrawn', () => node.all('interaction.cancel').some((c) => c.id === node.asked[2].id))
  assert.equal(fake.requestsOf('agent_permission_response').length, 2, 'nothing sent for it')

  // Handed back to the computer: left for Paseo's app.
  answer = () => ({ dismiss: true })
  fake.permission(a.claude.id, { id: 'permission-4', provider: 'claude', name: 'ExitPlanMode', kind: 'plan', input: { plan: 'p' }, metadata: { planText: '## 计划' },
    actions: [{ id: 'reject', behavior: 'deny' }, { id: 'implement', behavior: 'allow' }] })
  await until('asked', () => node.asked.length === 4)
  assert.equal(node.asked[3].plan, '## 计划')
  await new Promise((r) => setTimeout(r, 200))
  assert.equal(fake.requestsOf('agent_permission_response').length, 2)
})

test('an approved plan with a mode: implement, then that mode; the plan read from its call', async (t) => {
  const { fake, node, a } = await setup(t)
  node.resolve = () => ({ approve: true, mode: 'bypassPermissions' })
  // Claude Code gives the permission no input for ExitPlanMode: the plan is in the tool call on the timeline.
  fake.setTimeline(a.claude.id, [entry(7, { type: 'tool_call', callId: 'toolu_p', name: 'ExitPlanMode', status: 'running', error: null, detail: { type: 'plan', text: '## 计划\n\n1. 先写测试' } })])
  fake.permission(a.claude.id, { id: 'permission-9', provider: 'claude', name: 'ExitPlanMode', kind: 'plan', input: {}, metadata: { toolUseId: 'toolu_p' },
    actions: [{ id: 'reject', behavior: 'deny' }, { id: 'implement', behavior: 'allow' }] })
  await until('the mode set', () => fake.requestsOf('set_agent_mode_request').length === 1)
  assert.equal(node.asked[0].plan, '## 计划\n\n1. 先写测试')
  assert.deepEqual(fake.requestsOf('agent_permission_response')[0].response, { behavior: 'allow', selectedActionId: 'implement' })
  assert.equal(fake.requestsOf('set_agent_mode_request')[0].modeId, 'bypassPermissions')
})

test('Paseo restarting: the bridge connects again and keeps the sessions; gone for good, it leaves the node', async (t) => {
  const { fake, node, rows, logs } = await setup(t, { awayGrace: 600 })
  const peer = node.peer
  fake.drop()
  await until('connected again', () => fake.hellos.length === 2)
  await until('subscribed again', () => [...fake.subs.values()].some((s) => s.kind === 'agents'))
  assert.equal(node.peer, peer, 'the node connection stayed')
  assert.ok(rows().length >= 4)
  fake.refuse = 4003
  fake.drop()
  await until('left the node', async () => {
    const gone = await Promise.race([peer.done.then(() => true), new Promise((r) => setTimeout(() => r(false), 50))])
    return gone
  }, 5000)
  assert.ok(logs.some((l) => /away/.test(l)))
})
