import { SNAPSHOT_RE, SNAPSHOT_END_RE } from '../../runtime-sdk/lib/compat.js'
// Mapping between Antigravity CLI (agy) transcripts / ACP updates and Persona runtime items.
import { existsSync, readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { appToolName, clipInput, light, snippet } from '../../runtime-sdk/index.js'

export const RUNTIME = 'agy'
export const LABEL = 'Antigravity'
export const PREFIX = 'agy-'

const OWN_TOOLS = {
  run_command: 'Bash',
  view_file: 'Read',
  read_file: 'Read',
  write_to_file: 'Write',
  write_file: 'Write',
  replace_file_content: 'Edit',
  edit_file: 'Edit',
  grep_search: 'Grep',
  find_by_name: 'Glob',
  search_web: 'WebSearch',
  read_url_content: 'WebFetch',
  ask_question: 'AskUserQuestion',
  invoke_subagent: 'Agent',
}

export function toPersonaId(rawId) {
  const s = String(rawId ?? '').trim()
  if (!s) return ''
  return s.startsWith(PREFIX) ? s : `${PREFIX}${s}`
}

export function toAgyId(personaId) {
  const s = String(personaId ?? '').trim()
  return s.startsWith(PREFIX) ? s.slice(PREFIX.length) : s
}

const USER_REQ_RE = /<USER_REQUEST>\s*([\s\S]*?)\s*<\/USER_REQUEST>/

export function cleanUserText(raw) {
  if (typeof raw !== 'string') return ''
  const m = USER_REQ_RE.exec(raw)
  let body = m ? m[1] : raw
  let prev
  do {
    prev = body
    body = body.replace(SNAPSHOT_RE, '').replace(SNAPSHOT_END_RE, '')
  } while (body !== prev)
  return body.trim()
}

export function mapToolName(name) {
  return appToolName(name, OWN_TOOLS)
}

export function mapToolInput(name, args) {
  if (!args || typeof args !== 'object') return {}
  switch (name) {
    case 'run_command':
      return clipInput({
        command: args.CommandLine ?? args.command ?? '',
        description: args.toolSummary ?? args.Cwd ?? '',
      })
    case 'view_file':
    case 'read_file':
      return clipInput({
        file_path: args.AbsolutePath ?? args.Path ?? args.file_path ?? '',
      })
    case 'write_to_file':
    case 'write_file':
      return clipInput({
        file_path: args.TargetFile ?? args.Path ?? args.file_path ?? '',
        content: args.CodeContent ?? args.Content ?? args.content ?? '',
      })
    case 'replace_file_content':
    case 'edit_file':
      return clipInput({
        file_path: args.TargetFile ?? args.Path ?? args.file_path ?? '',
        old_string: args.TargetContent ?? args.Target ?? args.old_string ?? '',
        new_string: args.ReplacementContent ?? args.Replacement ?? args.new_string ?? '',
      })
    case 'grep_search':
      return clipInput({
        pattern: args.Query ?? args.Pattern ?? args.pattern ?? '',
        path: args.SearchPath ?? args.Path ?? args.path ?? '',
      })
    case 'find_by_name':
      return clipInput({
        pattern: args.Pattern ?? args.pattern ?? '',
        path: args.SearchDirectory ?? args.Path ?? args.path ?? '',
      })
    case 'search_web':
      return clipInput({
        query: args.query ?? args.Query ?? '',
      })
    case 'read_url_content':
      return clipInput({
        url: args.Url ?? args.url ?? '',
      })
    case 'invoke_subagent':
      return clipInput({
        description: args.toolSummary ?? 'subagent',
        prompt: typeof args.Subagents === 'string' ? args.Subagents : JSON.stringify(args.Subagents ?? ''),
      })
    default:
      return clipInput(args)
  }
}

export function cleanGenericOutput(content) {
  if (typeof content !== 'string') return ''
  // Strip run_command metadata header if followed by Output:
  const outIdx = content.indexOf('\nOutput:\n')
  if (outIdx >= 0 && content.startsWith('Created At:')) {
    return content.slice(outIdx + '\nOutput:\n'.length)
  }
  return content
}

/**
 * Parse a transcript.jsonl file (or array of parsed step objects) into Persona items.
 * @param {object[]} steps
 * @param {{ brainDir?: string, full?: boolean }} [opts]
 */
export function stepsToItems(steps, opts = {}) {
  const items = []
  const pendingCalls = []
  let maxSeq = 0
  let lastText = ''

  for (let i = 0; i < steps.length; i++) {
    const step = steps[i]
    if (!step || typeof step !== 'object') continue
    const idx = typeof step.step_index === 'number' ? step.step_index : i
    const seq = idx + 1
    if (seq > maxSeq) maxSeq = seq
    const ts = step.created_at || new Date().toISOString()

    switch (step.type) {
      case 'USER_INPUT': {
        if (step.source && step.source !== 'USER_EXPLICIT') break
        const text = cleanUserText(step.content)
        if (!text) break
        lastText = snippet(text)
        const item = { id: `${seq}#0`, seq, role: 'user', kind: 'text', text, ts }
        items.push(opts.full ? item : light(item))
        break
      }
      case 'PLANNER_RESPONSE': {
        let sub = 0
        if (typeof step.thinking === 'string' && step.thinking.trim()) {
          const item = { id: `${seq}#${sub++}`, seq, role: 'assistant', kind: 'thinking', text: step.thinking.trim(), ts }
          items.push(opts.full ? item : light(item))
        }
        if (typeof step.content === 'string' && step.content.trim()) {
          const text = step.content.trim()
          lastText = snippet(text)
          const item = { id: `${seq}#${sub++}`, seq, role: 'assistant', kind: 'text', text, ts }
          items.push(opts.full ? item : light(item))
        }
        const calls = Array.isArray(step.tool_calls) ? step.tool_calls : []
        for (let k = 0; k < calls.length; k++) {
          const tc = calls[k]
          if (!tc || typeof tc !== 'object') continue
          const callId = `call_${seq}_${k}`
          const toolName = mapToolName(tc.name || 'tool')
          const toolInput = mapToolInput(tc.name || 'tool', tc.args)
          pendingCalls.push({ callId, idx })
          const item = {
            id: `${seq}#${sub++}`,
            seq,
            role: 'assistant',
            kind: 'tool_use',
            tool_name: toolName,
            tool_input: toolInput,
            tool_use_id: callId,
            ts,
          }
          items.push(opts.full ? item : light(item))
        }
        break
      }
      case 'GENERIC': {
        const paired = pendingCalls.shift()
        const callId = paired ? paired.callId : `call_${seq}_0`
        let rawOut = step.error || step.content || ''
        if (opts.full && opts.brainDir && Array.isArray(step.truncated_fields) && step.truncated_fields.includes('content')) {
          const outFile = join(opts.brainDir, '.system_generated', 'steps', String(idx), 'output.txt')
          if (existsSync(outFile)) {
            try {
              rawOut = readFileSync(outFile, 'utf8')
            } catch {}
          }
        }
        const text = step.error ? String(step.error) : cleanGenericOutput(rawOut)
        const isError = Boolean(step.error) || step.status === 'ERROR'
        const resItem = {
          id: `${seq}#0`,
          seq,
          role: 'user',
          kind: 'tool_result',
          tool_use_id: callId,
          text,
          is_error: isError,
          ts,
        }
        items.push(opts.full ? resItem : light(resItem))
        if (Array.isArray(step.media)) {
          step.media.forEach((m, mi) => {
            if (m && typeof m.mime_type === 'string' && m.mime_type.startsWith('image/')) {
              items.push({
                id: `${seq}#img${mi}`,
                seq,
                role: 'user',
                kind: 'image',
                mime: m.mime_type,
                lazy: true,
                ts,
              })
            }
          })
        }
        break
      }
      case 'ERROR_MESSAGE': {
        const text = step.error || step.content || 'Error'
        items.push({ id: `${seq}#0`, seq, role: 'system', kind: 'notice', text: String(text), ts })
        break
      }
      default:
        break
    }
  }

  return { items, toSeq: maxSeq, lastText }
}

export function readTranscriptSteps(brainDir, full = false) {
  const file = join(brainDir, '.system_generated', 'logs', full ? 'transcript_full.jsonl' : 'transcript.jsonl')
  const fallback = full ? join(brainDir, '.system_generated', 'logs', 'transcript.jsonl') : null
  const target = existsSync(file) ? file : fallback && existsSync(fallback) ? fallback : null
  if (!target) return []
  try {
    const raw = readFileSync(target, 'utf8')
    const out = []
    for (const line of raw.split('\n')) {
      const trimmed = line.trim()
      if (!trimmed) continue
      try {
        out.push(JSON.parse(trimmed))
      } catch {}
    }
    return out
  } catch {
    return []
  }
}

/** Native tool results are intermediate work. A completed assistant reply closes the turn. */
export function transcriptStatus(steps, now = Date.now()) {
  const last = steps.findLast((step) => ['USER_INPUT', 'PLANNER_RESPONSE', 'GENERIC', 'ERROR_MESSAGE'].includes(step?.type))
  if (!last) return 'unknown'
  if (last.type === 'ERROR_MESSAGE' || ['ERROR', 'CANCELLED', 'CANCELED'].includes(last.status)) return 'idle'
  if (last.type === 'PLANNER_RESPONSE' && last.status === 'DONE' && typeof last.content === 'string' && last.content.trim() && !last.tool_calls?.length) {
    return 'done'
  }
  if (!['RUNNING', 'PENDING', 'CREATED', 'DONE'].includes(last.status)) return 'unknown'
  // Only recent unfinished steps establish external activity. Older history remains uncertain.
  const started = Date.parse(last.created_at)
  if (!Number.isFinite(started) || now - started > 24 * 60 * 60 * 1000) return 'unknown'
  return 'working'
}

export function workspacePathOf(summary, fallbackCwd = '') {
  if (Array.isArray(summary?.WorkspaceURIs) && summary.WorkspaceURIs.length > 0) {
    const u = String(summary.WorkspaceURIs[0])
    if (u.startsWith('file://')) {
      try {
        return decodeURIComponent(new URL(u).pathname)
      } catch {
        return u.slice('file://'.length)
      }
    }
    return u
  }
  return fallbackCwd
}

export function defaultModelsList() {
  const efforts = ['low', 'medium', 'high']
  return [
    { id: 'gemini-4-argon-high', name: 'Gemini 4 Argon (High)', default: true, efforts, default_effort: 'high' },
    { id: 'gemini-4-argon-medium', name: 'Gemini 4 Argon (Medium)', efforts, default_effort: 'medium' },
    { id: 'gemini-4-argon-low', name: 'Gemini 4 Argon (Low)', efforts, default_effort: 'low' },
    { id: 'gemini-3.8-flash-high', name: 'Gemini 3.8 Flash (High)', efforts, default_effort: 'high' },
    { id: 'gemini-3.8-flash-medium', name: 'Gemini 3.8 Flash (Medium)', efforts, default_effort: 'medium' },
    { id: 'gemini-3.8-flash-low', name: 'Gemini 3.8 Flash (Low)', efforts, default_effort: 'low' },
    { id: 'claude-opus-5-5-high', name: 'Claude Opus 5.5 (High)', efforts, default_effort: 'high' },
    { id: 'claude-opus-5-5-medium', name: 'Claude Opus 5.5 (Medium)', efforts, default_effort: 'medium' },
    { id: 'claude-opus-5-5-low', name: 'Claude Opus 5.5 (Low)', efforts, default_effort: 'low' },
    { id: 'claude-sonnet-5-5-high', name: 'Claude Sonnet 5.5 (High)', efforts, default_effort: 'high' },
    { id: 'claude-sonnet-5-5-medium', name: 'Claude Sonnet 5.5 (Medium)', efforts, default_effort: 'medium' },
    { id: 'claude-sonnet-5-5-low', name: 'Claude Sonnet 5.5 (Low)', efforts, default_effort: 'low' },
    { id: 'gpt-oss-120b-medium', name: 'GPT-OSS 120B (Medium)', efforts: ['medium'], default_effort: 'medium' },
  ]
}

export function personaModeToAgy(mode) {
  switch (mode) {
    case 'plan':
      return 'plan'
    case 'acceptEdits':
    case 'auto':
    case 'bypassPermissions':
      return 'accept-edits'
    case 'default':
    default:
      return 'default'
  }
}

export function agyModeToPersona(mode) {
  switch (mode) {
    case 'plan':
      return 'plan'
    case 'accept-edits':
      return 'acceptEdits'
    default:
      return 'default'
  }
}
