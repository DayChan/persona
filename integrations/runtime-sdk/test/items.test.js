import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clipInput, clipTo, MAX_INPUT, summaryOf } from '../index.js'

const jsonBytes = (v) => Buffer.byteLength(JSON.stringify(v), 'utf8')

test('clipInput clips a million-byte multi-edit string without changing the edits or source', () => {
  const input = { file_path: 'a.go', edits: [
    { old_string: 'a', new_string: 'x'.repeat(1_000_000) },
    { old_string: 'b', new_string: 'c', replace_all: true },
  ] }
  const original = structuredClone(input)
  const out = clipInput(input)
  assert.ok(jsonBytes(out) < MAX_INPUT)
  assert.equal(out.file_path, 'a.go')
  assert.equal(out.edits.length, 2)
  assert.equal(out.edits[0].old_string, 'a')
  assert.equal(out.edits[0].new_string, clipTo(input.edits[0].new_string, MAX_INPUT / 2))
  assert.deepEqual(out.edits[1], input.edits[1])
  assert.deepEqual(input, original)
})

test('clipInput visits nested objects and arrays, preserving keys and non-string values', () => {
  const big = 'x'.repeat(50_000)
  const input = { blocks: [{ nested: [[big, null, true, 42]], empty: [], object: {} }],
    ...JSON.parse('{"__proto__":{"content":"small"}}') }
  const out = clipInput(input)
  assert.deepEqual(out, { ...input, blocks: [{ ...input.blocks[0], nested: [[clipTo(big, MAX_INPUT / 2), null, true, 42]] }] })
  assert.ok(Object.hasOwn(out, '__proto__'))
  assert.equal(Object.getPrototypeOf(out), Object.prototype)
  assert.equal(input.blocks[0].nested[0][0], big)
})

test('clipInput shares the budget across medium strings, accounting for JSON escapes', () => {
  for (const [text, count] of [['Some notes on the plan. '.repeat(125), 40], ['\\"\n\t\u0000'.repeat(600), 20]]) {
    const input = { page_id: 'p1', children: Array.from({ length: count }, () => ({ type: 'paragraph', text })) }
    const out = clipInput(input)
    assert.ok(jsonBytes(input) > 100_000)
    assert.ok(jsonBytes(out) <= MAX_INPUT, `clipped to ${jsonBytes(out)} bytes`)
    assert.equal(out.page_id, input.page_id)
    assert.equal(out.children.length, count)
    const first = out.children[0].text
    assert.notEqual(first, text)
    assert.ok(first.startsWith(text.slice(0, 100)))
    assert.ok(Buffer.byteLength(first) >= 256)
    assert.ok(first.endsWith(` ${Buffer.byteLength(text)} 字节）`))
    for (const block of out.children) assert.deepEqual(block, { type: 'paragraph', text: first })
    assert.ok(input.children.every((block) => block.text === text))
  }
})

test('clipInput keeps short inputs, top-level non-objects, and many short values whole', () => {
  for (const input of [null, undefined, true, 42, 'x'.repeat(50_000), ['x'.repeat(50_000)], { text: 'small' }]) {
    assert.equal(clipInput(input), input)
  }
  const input = { values: Array.from({ length: 4_000 }, () => ['short', 42, true, null]) }
  assert.ok(jsonBytes(input) > MAX_INPUT)
  assert.deepEqual(clipInput(input), input, 'many short values still have no total bound')
})

test('clipInput only replaces a near-share string when its JSON is strictly shorter', () => {
  // With a three-digit original size the note costs 37 JSON bytes (excluding its enclosing quotes).
  for (const [extra, cut] of [[30, false], [36, false], [37, false], [38, true], [48, true], [49, true]]) {
    const text = 'x'.repeat(256 + extra)
    const input = { items: Array.from({ length: 200 }, () => ({ text })) }
    const out = clipInput(input)
    const candidate = clipTo(text, 256)
    if (extra === 37) assert.equal(jsonBytes(candidate), jsonBytes(text))
    assert.ok(jsonBytes(out) <= jsonBytes(input))
    for (const item of out.items) assert.equal(item.text, cut ? candidate : text)
  }
  for (const tail of ['\\'.repeat(30), '"'.repeat(30), '\u0000'.repeat(30), '<>&'.repeat(10), '\u2028\u2029'.repeat(5)]) {
    const text = 'x'.repeat(256) + tail
    const candidate = clipTo(text, 256)
    const input = { items: Array.from({ length: 200 }, () => ({ text })) }
    const out = clipInput(input)
    const shouldCut = jsonBytes(candidate) < jsonBytes(text)
    assert.equal(shouldCut, ['\\', '"', '\u0000'].includes(tail[0]), 'use JSON.stringify escaping, not Go HTML escaping')
    for (const item of out.items) assert.equal(item.text, shouldCut ? candidate : text)
  }
})

test('clipTo and nested clipInput cut UTF-8 characters and surrogate pairs only at boundaries', () => {
  for (const char of ['é', '界', '🙂']) {
    const width = Buffer.byteLength(char)
    for (let offset = 0; offset < width; offset++) {
      const text = 'a'.repeat(offset) + char.repeat(20_000)
      const prefix = 'a'.repeat(offset) + char.repeat(Math.floor((MAX_INPUT / 2 - offset) / width))
      const expected = prefix + `\n…（已截断，共 ${Buffer.byteLength(text)} 字节）`
      assert.equal(clipTo(text, MAX_INPUT / 2), expected)
      const out = clipInput({ nested: { text } })
      assert.equal(out.nested.text, expected)
      assert.ok(!out.nested.text.includes('\ufffd'))
      assert.ok(out.nested.text.isWellFormed())
      const medium = 'a'.repeat(offset) + char.repeat(1_000)
      const shared = clipInput({ rows: Array.from({ length: 40 }, () => [medium]) })
      assert.ok(jsonBytes(shared) <= MAX_INPUT)
      for (const [value] of shared.rows) {
        assert.notEqual(value, medium)
        assert.ok(value.endsWith(` ${Buffer.byteLength(medium)} 字节）`))
        assert.ok(!value.includes('\ufffd'))
        assert.ok(value.isWellFormed())
      }
    }
  }
})

test('summaryOf says what a call does, not just which tool', () => {
  assert.equal(summaryOf('bash', { command: 'rm -rf build', description: 'clean' }), 'rm -rf build', 'what runs comes first')
  assert.equal(summaryOf('Agent', { description: 'Find the flaky test', prompt: 'long…' }), 'Find the flaky test')
  assert.equal(summaryOf('mcp__lark__send', { chat_id: 'oc_1', message: 'hello there' }), 'hello there')
  assert.equal(summaryOf('mcp__x__create', { repo: 'a/b', count: 3 }), 'repo: a/b', 'any other text, with its name')
  assert.equal(summaryOf('mcp__x__ping', { count: 3 }), 'mcp__x__ping', 'no text at all: the tool name')
  assert.equal(summaryOf('mcp__x__ping', { path: '  ', count: 3 }), 'mcp__x__ping', 'blank strings say nothing')
  assert.equal(summaryOf('t', null), 't')
  assert.equal(summaryOf('t', ['a']), 't')
  assert.equal([...summaryOf('t', { text: 'x'.repeat(300) })].length, 201, 'one line, cut')
  assert.equal(summaryOf('x', { api_key: 'sk-live-123', repo: 'a' }), 'repo: a', 'a credential is skipped')
  for (const k of ['token', 'api_key', 'apiKey', 'APIKey', 'x-api-key', 'authorization', 'Authorization', 'password', 'client_secret', 'cookie', 'credentials',
    'session_id', 'sessionId', 'private_key', 'headers', 'bearer', 'Bearer', 'totp', 'pin', 'dsn', 'database_dsn', 'connection_string', 'connStr', 'webhook_url', 'slackWebhook', 'pwd', 'pass', 'jwt', 'access_key', 'accessKeyId', 'key', 'signature', 'auth',
    'x-auth', 'auth_code', 'otp', 'SSHKey', 'GPGKey', 'AWSKey', 'DBPass', 'HTTPAuth', 'MFAPin', 'PGPKeys']) {
    assert.equal(summaryOf('mcp__x__call', { [k]: 'secret-value' }), 'mcp__x__call', `${k} alone: the tool name`)
  }
  assert.equal(summaryOf('x', { password: 'hunter2', user: 'bob' }), 'user: bob')
  for (const k of ['author', 'authorName', 'oauth_scope', 'session_name', 'keyword', 'passage', 'monkey', 'OAuthScope', 'URLPath', 'HTTPMethod', 'IDList']) {
    assert.equal(summaryOf('x', { [k]: 'v' }), `${k}: v`, `${k} is no credential: shown`)
  }
})
