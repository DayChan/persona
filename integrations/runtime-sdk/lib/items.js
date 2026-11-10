// Helpers every runtime needs to turn its own content into Persona items the way the apps expect (docs/integrations/runtimes.md
// "Content"): the page limits and the notes of server/internal/transcript, interaction ids that survive a node
// restart, one-line summaries.

import { createHash } from 'node:crypto'

// Pages and pushes carry at most this much text per item; the whole of a cut item comes from session.item.
export const PAGE_TEXT = 24 * 1024
/** Tool output is cut shorter: the app has a "Show all" button for it. */
export const PAGE_OUTPUT = 4 * 1024
/** Tool inputs are not fetched whole: long strings in them are clipped for good. */
export const MAX_INPUT = 24 * 1024

/** Cut s to at most n bytes on a UTF-8 boundary, noting the original size. */
export function clipTo(s, n, note = '\n…（已截断，共 %d 字节）') {
  const buf = Buffer.from(s, 'utf8')
  if (buf.length <= n) return s
  let end = n
  while (end > 0 && (buf[end] & 0xc0) === 0x80) end--
  return buf.subarray(0, end).toString('utf8') + note.replace('%d', String(buf.length))
}

/**
 * The page / push copy of an item: long text cut with the note the apps read and full_size set (only a tool's output
 * says to tap for the whole). Items already short enough come back unchanged.
 */
export function light(item) {
  if (typeof item?.text !== 'string') return item
  const limit = item.kind === 'tool_result' ? PAGE_OUTPUT : PAGE_TEXT
  const note = item.kind === 'tool_result' ? '\n…（已截断，点开看全文，共 %d 字节）' : '\n…（已截断，共 %d 字节）'
  const buf = Buffer.from(item.text, 'utf8')
  if (buf.length <= limit) return item
  let end = limit
  while (end > 0 && (buf[end] & 0xc0) === 0x80) end--
  let text = buf.subarray(0, end).toString('utf8')
  const nl = text.lastIndexOf('\n')
  if (nl > 0 && Buffer.byteLength(text.slice(nl), 'utf8') < 512) text = text.slice(0, nl) // end on a whole line
  return { ...item, text: text + note.replace('%d', String(buf.length)), full_size: buf.length }
}

const minInputShare = 256 // shorter strings, keys and other values stay whole
const clipNoteRoom = 48 // room for clipTo's note, as JSON

// Count what this runtime sends. Unlike Go's json.Marshal, JSON.stringify does not escape < > & or U+2028/U+2029,
// so the budget and the decision to cut must use its own encoded UTF-8 size.
function jsonLen(value) {
  return Buffer.byteLength(JSON.stringify(value), 'utf8')
}

// Copy a JSON value, visiting strings in objects and arrays without changing keys or the input itself.
function mapStrings(value, f) {
  if (typeof value === 'string') return f(value)
  if (Array.isArray(value)) return value.map((v) => mapStrings(v, f))
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, mapStrings(v, f)]))
  }
  return value
}

function clipIfShorter(s, n) {
  const size = Buffer.byteLength(s, 'utf8')
  if (size <= n) return s
  const clipped = clipTo(s, n)
  return size > n + clipNoteRoom || jsonLen(clipped) < jsonLen(s) ? clipped : s
}

/** Clip long strings anywhere in a tool input object, with the same two passes as transcript.clipJSON. */
export function clipInput(input) {
  if (input === null || typeof input !== 'object' || Array.isArray(input)) return input
  if (jsonLen(input) <= MAX_INPUT) return input
  const out = mapStrings(input, (s) => clipTo(s, MAX_INPUT / 2))
  const size = jsonLen(out)
  if (size <= MAX_INPUT) return out
  let long = 0
  let kept = 0
  const rest = jsonLen(mapStrings(input, (s) => {
    const bytes = Buffer.byteLength(s, 'utf8')
    if (bytes <= minInputShare) return s
    long++
    kept += Math.min(bytes, MAX_INPUT / 2)
    return ''
  }))
  if (long === 0) return out
  // Share the remaining budget, accounting for JSON escapes and the note. The floor means many short values (or
  // enough long ones) can still exceed MAX_INPUT; dropping values needs a separate policy.
  let share = Math.trunc((MAX_INPUT - rest) * kept / (size - rest) / long)
  share = Math.min(Math.max(share - clipNoteRoom, minInputShare), MAX_INPUT / 2)
  return mapStrings(input, (s) => clipIfShorter(s, share))
}

/** One line, at most 200 characters (a list row's last_text, a card's summary). */
export function snippet(s) {
  const one = String(s).split(/\s+/).filter(Boolean).join(' ')
  const chars = [...one]
  return chars.length > 200 ? chars.slice(0, 200).join('') + '…' : one
}

// The arguments that say what a call does, best first: what it runs or touches, then what it is about (a subagent's
// description and prompt, an MCP tool's title, text or message), then what it names.
const SUMMARY_KEYS = ['command', 'file_path', 'path', 'url', 'pattern', 'query', 'description', 'prompt', 'title', 'text', 'message', 'name']

// Argument names whose values are credentials, never shown in a summary. Parts of a name that say so wherever they
// are (apiKey, x-api-key, client_secret, accessKeyId), and whole words that say so only alone: key, pass, auth…
// (so author, oauth_scope, session_name and keyword are shown). A DSN, a connection string or a webhook URL carries
// its credential inside the value, so those names count too.
const SECRET_PARTS = /token|passw|secret|apikey|accesskey|privatekey|credential|cookie|bearer|jwt|signature|sessionid|sessionkey|header|totp|webhook|connectionstring|connstr/
const SECRET_WORDS = new Set(['key', 'keys', 'pass', 'pwd', 'pin', 'auth', 'authorization', 'authentication', 'authn', 'sig', 'otp', 'dsn'])

/** Whether an argument's name says its value is a credential. An acronym before a word (SSHKey, DBPass) is split off
 *  first: two capitals or more, so OAuth stays one word. */
function secretKey(k) {
  const words = String(k).replace(/([A-Z]{2,})([A-Z][a-z])/g, '$1_$2').replace(/([a-z0-9])([A-Z])/g, '$1_$2').toLowerCase().split(/[^a-z0-9]+/).filter(Boolean)
  return SECRET_PARTS.test(words.join('')) || words.some((w) => SECRET_WORDS.has(w))
}

/**
 * One line saying what a tool is about to do, for a permission card: the owner decides from it without opening the
 * card. An input with none of the usual arguments shows its first string one as "key: value", never one whose name
 * looks like a credential; only an input with no such text is the bare tool name.
 */
export function summaryOf(name, input) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return name ?? ''
  for (const k of SUMMARY_KEYS) {
    if (typeof input[k] === 'string' && input[k].trim()) return snippet(input[k])
  }
  for (const [k, v] of Object.entries(input)) {
    if (secretKey(k)) continue // the summary is a push's body: it shows on the lock screen
    if (typeof v === 'string' && v.trim()) return snippet(`${k}: ${v}`)
  }
  return name ?? ''
}

/**
 * A stable interaction id: the same tool call asks with the same id, so after a node restart the hub hangs the
 * repeated ask on the card the phone already shows.
 */
export function interactionId(runtime, sessionId, kind, key) {
  return `${runtime}-` + createHash('sha256').update(`${sessionId}\0${kind}\0${key}`).digest('hex').slice(0, 16)
}

// The tools the apps draw as tools (a command, a diff, a file read, a search, a to-do list), under Claude Code's names;
// their arguments are Claude's too (command, file_path, old_string / new_string, pattern, url, query, todos).
const APP_TOOLS = {
  bash: 'Bash', read: 'Read', write: 'Write', edit: 'Edit', multi_edit: 'MultiEdit', grep: 'Grep', glob: 'Glob', ls: 'LS',
  web_fetch: 'WebFetch', web_search: 'WebSearch', todo_write: 'TodoWrite',
}

/**
 * The name the apps know a runtime's tool by: `own` (the runtime's names for the rest: DSH's ask_user_question, pi's
 * find), then the common ones (bash → Bash, read → Read…), else as it is.
 * @param {string} name
 * @param {Record<string, string>} [own]
 */
export function appToolName(name, own = {}) {
  return own[name] ?? APP_TOOLS[name] ?? name
}

/**
 * One session.items page over items a runtime has in memory (sorted by seq; several may share a seq), with the
 * paging every runtime needs (docs/protocol/protocol.md "session.items"): from the end by `turns` (a turn starts at a user's
 * message) or `limit`, back from `before_seq`, forward from `after_seq` (at most 500 items, `more_after` when cut),
 * or the span `from_seq` … `through_seq`. Texts are cut with light(). toSeq is the session's current to_seq.
 * @param {object[]} all
 * @param {object} params
 * @param {{ history: string, toSeq: number }} at
 */
export function page(all, params = {}, { history, toSeq }) {
  if (typeof params.through_seq === 'number' && params.through_seq > 0) {
    const from = typeof params.from_seq === 'number' ? params.from_seq : 1
    return { items: all.filter((i) => i.seq >= from && i.seq <= params.through_seq).map(light), more: false }
  }
  if (params.after_seq !== undefined && params.after_seq !== null) {
    const after = all.filter((i) => i.seq > params.after_seq)
    let out = after
    let to = Math.max(toSeq, params.after_seq)
    let cut = false
    if (after.length > 500) {
      let n = 500
      while (n < after.length && after[n].seq === after[n - 1].seq) n++ // a record's items stay together
      out = after.slice(0, n)
      cut = n < after.length
      if (cut) to = out.at(-1).seq
    }
    return { items: out.map(light), has_more: false, more_after: cut, history, from_seq: params.after_seq, to_seq: to }
  }
  const before = params.before_seq > 0 ? all.filter((i) => i.seq < params.before_seq) : all
  let start
  if (params.turns > 0) {
    let turns = 0
    start = 0
    for (let i = before.length - 1; i >= 0; i--) {
      const it = before[i]
      const opens = it.role === 'user' && (it.kind === 'text' || it.kind === 'image') && !(before[i - 1]?.role === 'user' && before[i - 1]?.seq === it.seq)
      if (opens && ++turns === Math.min(params.turns, 10)) {
        start = i
        break
      }
    }
  } else {
    const limit = params.limit > 0 ? Math.min(params.limit, 500) : 80
    start = Math.max(0, before.length - limit)
    while (start > 0 && before[start].seq === before[start - 1].seq) start-- // not half a record
  }
  const out = before.slice(start)
  return {
    items: out.map(light),
    has_more: start > 0,
    history,
    to_seq: params.before_seq > 0 ? (out.at(-1)?.seq ?? 0) : toSeq,
  }
}
