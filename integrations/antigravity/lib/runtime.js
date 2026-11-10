import { daemonBinary, mcpServers } from './compat.js'
import { contextWrapped } from '../../runtime-sdk/lib/compat.js'
// Antigravity (agy) runtime for Persona (docs/integrations/runtimes.md).
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, join } from 'node:path'
import { createRuntime, interactionId, page, snippet, summaryOf } from '../../runtime-sdk/index.js'
import { isAssistantDir } from '../../runtime-sdk/lib/identity.js'
import { AgyAcpClient } from './acp.js'
import {
  agyModeToPersona,
  defaultModelsList,
  LABEL,
  mapToolInput,
  mapToolName,
  personaModeToAgy,
  readTranscriptSteps,
  RUNTIME,
  stepsToItems,
  transcriptStatus,
  toAgyId,
  toPersonaId,
  workspacePathOf,
} from './map.js'

export function createAgyRuntime(options = {}) {
  const home = options.home || homedir()
  const agyDir = options.agyDir || process.env.ANTIGRAVITY_CLI_CONFIG_DIR || join(home, '.gemini', 'antigravity-cli')
  const personadBin = daemonBinary(home, options)
  const agyBin = options.agyBin || join(home, '.local', 'bin', 'agy')
  const log = options.log || ((m) => console.error(m))
  const listDays = options.listDays ?? 14
  const maxSessions = options.maxSessions ?? 80

  const activeTurns = new Map() // personaId -> { streamingText, startedAt }
  const completedTurns = new Set() // owned ACP turns that ended before a native transcript was available
  const sessionTitles = new Map() // agyId -> title
  const sessionCwds = new Map() // agyId -> cwd
  const sessionModels = new Map() // agyId -> modelId
  const sessionModes = new Map() // agyId -> { mode, effort }
  const archived = new Set() // agyId
  const lastPushedSeq = new Map() // personaId -> number
  const assistants = new Set() // personaId
  const permissionAsks = new Map() // agyId -> Set of AbortControllers, one per permission card still on the phone
  const cancelling = new Set() // agyId: interrupted, its turn has not ended yet

  const acp = new AgyAcpClient({
    bin: existsSync(agyBin) ? agyBin : 'agy',
    log,
    onPermissionRequest: async (params) => handlePermissionRequest(params),
  })

  const models = defaultModelsList()
  const launchers = [
    {
      name: 'agy',
      model: 'Gemini 4 Argon (High)',
      agent: RUNTIME,
      models,
    },
  ]

  const rt = createRuntime(
    {
      listSessions,
      items,
      cursor,
      item,
      send,
      interrupt,
      start,
      rename,
      close,
      modes,
    },
    {
      runtime: RUNTIME,
      name: LABEL,
      agent: RUNTIME,
      version: '1.5.1',
      launcher: 'agy',
      launcherModel: 'Gemini 4 Argon (High)',
      launchers,
      assistant: true,
      socketPath: options.socketPath,
      log,
    },
  )

  acp.on('update', (params) => {
    const rawId = params?.sessionId
    const upd = params?.update
    if (!rawId || !upd) return
    const pid = toPersonaId(rawId)
    switch (upd.sessionUpdate) {
      case 'session_info_update':
        if (typeof upd.title === 'string' && upd.title.trim()) {
          sessionTitles.set(rawId, upd.title.trim())
          rt.sessionsChanged()
        }
        break
      case 'current_mode_update':
        if (typeof upd.currentModeId === 'string') {
          const cur = sessionModes.get(rawId) || {}
          const mapped = agyModeToPersona(upd.currentModeId)
          if ((cur.mode === 'bypassPermissions' || cur.mode === 'auto') && mapped === 'acceptEdits') {
            break
          }
          sessionModes.set(rawId, { ...cur, mode: mapped })
          rt.sessionsChanged()
        }
        break
      case 'agent_message_chunk': {
        const chunk = upd.content?.text
        if (typeof chunk === 'string' && chunk) {
          const turn = activeTurns.get(pid)
          if (turn) {
            turn.streamingText += chunk
            rt.stream(pid, turn.streamingText)
          }
        }
        break
      }
      case 'tool_call':
      case 'tool_call_update':
        syncTranscriptAppend(rawId)
        break
      default:
        break
    }
  })

  function readAgySettings() {
    const cfgFile = join(agyDir, 'settings.json')
    if (!existsSync(cfgFile)) return {}
    try {
      return JSON.parse(readFileSync(cfgFile, 'utf8')) || {}
    } catch {
      return {}
    }
  }

  function effectiveModeOf(rawId) {
    const cur = sessionModes.get(rawId)
    if (cur?.mode) return cur.mode
    const cfg = readAgySettings()
    if (cfg.toolPermission === 'always-proceed' || cfg.agentMode === 'bypassPermissions') {
      return 'bypassPermissions'
    }
    if (cfg.agentMode === 'auto') return 'auto'
    if (cfg.agentMode === 'accept-edits') return 'acceptEdits'
    if (cfg.agentMode === 'plan') return 'plan'
    return 'default'
  }

  function shouldBypassPermission(rawId) {
    const mode = effectiveModeOf(rawId)
    return mode === 'bypassPermissions' || mode === 'auto'
  }

  async function handlePermissionRequest(params) {
    const rawId = params?.sessionId
    const pid = toPersonaId(rawId)
    const tc = params?.toolCall || {}
    const rawName = tc.title || tc.name || 'run_command'
    const toolName = mapToolName(rawName)
    const toolInput = mapToolInput(rawName, tc.rawInput || tc.input || {})
    const opts = Array.isArray(params?.options) ? params.options : []
    const allowAlwaysOpt = opts.find((o) => o.kind === 'allow_always') || opts.find((o) => o.kind === 'allow_once') || opts[0]
    const allowOpt = opts.find((o) => o.kind === 'allow_once') || allowAlwaysOpt
    const rejectOpt = opts.find((o) => o.kind === 'reject_once') || opts.find((o) => o.kind === 'reject_always')

    // A request that crossed the interrupt on the wire: ACP wants every pending one answered 'cancelled'.
    if (cancelling.has(rawId)) return { outcome: { outcome: 'cancelled' } }
    if (shouldBypassPermission(rawId) && allowAlwaysOpt) {
      return { outcome: { outcome: 'selected', optionId: allowAlwaysOpt.optionId } }
    }

    // Interrupt and the end of the turn withdraw the card (the SDK takes it off the phone) and agy hears 'cancelled'.
    const ac = new AbortController()
    let asks = permissionAsks.get(rawId)
    if (!asks) permissionAsks.set(rawId, (asks = new Set()))
    asks.add(ac)
    let res
    try {
      res = await rt.ask({
        id: interactionId(pid, tc.toolCallId || String(Date.now())),
        kind: 'permission',
        session_id: pid,
        tool_name: toolName,
        tool_input: toolInput,
        summary: summaryOf(toolName, toolInput),
      }, { signal: ac.signal })
    } catch (err) {
      if (ac.signal.aborted) return { outcome: { outcome: 'cancelled' } }
      throw err
    } finally {
      asks.delete(ac)
      if (asks.size === 0 && permissionAsks.get(rawId) === asks) permissionAsks.delete(rawId)
    }

    if (res?.allow && allowOpt) {
      return { outcome: { outcome: 'selected', optionId: allowOpt.optionId } }
    }
    if (rejectOpt) {
      return { outcome: { outcome: 'selected', optionId: rejectOpt.optionId } }
    }
    return { outcome: { outcome: 'cancelled' } }
  }

  /** Take the session's permission cards off the phone: each one answers agy 'cancelled'. */
  function withdrawPermissionAsks(rawId, why) {
    const asks = permissionAsks.get(rawId)
    if (!asks) return
    permissionAsks.delete(rawId)
    for (const ac of asks) ac.abort(new Error(why))
  }

  function readCacheMetadata() {
    const metaFile = join(agyDir, 'cache', 'conversation_metadata.json')
    const lastFile = join(agyDir, 'cache', 'last_conversations.json')
    let convs = {}
    const cwdByConv = {}
    if (existsSync(metaFile)) {
      try {
        convs = JSON.parse(readFileSync(metaFile, 'utf8'))?.conversations || {}
      } catch {}
    }
    if (existsSync(lastFile)) {
      try {
        const last = JSON.parse(readFileSync(lastFile, 'utf8')) || {}
        for (const [dir, cid] of Object.entries(last)) {
          if (typeof cid === 'string' && !cwdByConv[cid]) cwdByConv[cid] = dir
        }
      } catch {}
    }
    return { convs, cwdByConv }
  }

  function brainDirOf(rawId) {
    return join(agyDir, 'brain', rawId)
  }

  function historyOf(rawId) {
    return `agy:${rawId}`
  }

  function syncTranscriptAppend(rawId) {
    const pid = toPersonaId(rawId)
    const bDir = brainDirOf(rawId)
    const steps = readTranscriptSteps(bDir, false)
    if (steps.length === 0) return
    const { items: allItems, toSeq } = stepsToItems(steps, { brainDir: bDir, full: false })
    const prevSeq = lastPushedSeq.get(pid) ?? 0
    if (toSeq > prevSeq) {
      const deltaItems = allItems.filter((it) => it.seq > prevSeq)
      lastPushedSeq.set(pid, toSeq)
      rt.append(pid, {
        items: deltaItems,
        fromSeq: prevSeq + 1,
        toSeq,
        history: historyOf(rawId),
      })
    }
  }

  async function listSessions() {
    const { convs, cwdByConv } = readCacheMetadata()
    const brainRoot = join(agyDir, 'brain')
    const seenIds = new Set(Object.keys(convs))
    if (existsSync(brainRoot)) {
      try {
        for (const name of readdirSync(brainRoot)) {
          if (/^[0-9a-fA-F-]{36}$/.test(name)) seenIds.add(name)
        }
      } catch {}
    }

    const candidates = []
    for (const rawId of seenIds) {
      if (archived.has(rawId)) continue
      const meta = convs[rawId]
      const summary = meta?.summary
      const pid = toPersonaId(rawId)
      const bDir = brainDirOf(rawId)
      const transcriptFile = join(bDir, '.system_generated', 'logs', 'transcript.jsonl')
      const hasTranscript = existsSync(transcriptFile)
      if (!hasTranscript && !activeTurns.has(pid) && !rt.touched(pid)) continue
      if (meta?.is_internal && !summary && !rt.touched(pid) && !activeTurns.has(pid)) continue

      let mtimeMs = Date.parse(summary?.UpdatedAt || meta?.last_modified_time || '') || 0
      if (hasTranscript) {
        try {
          const st = statSync(transcriptFile)
          if (st.mtimeMs > mtimeMs) mtimeMs = st.mtimeMs
        } catch {}
      }
      if (!mtimeMs) mtimeMs = Date.now()

      const cwd = sessionCwds.get(rawId) || workspacePathOf(summary, cwdByConv[rawId] || home)
      if (cwd) sessionCwds.set(rawId, cwd)
      if (isAssistantDir(cwd)) assistants.add(pid)

      const running = activeTurns.has(pid)
      candidates.push({ rawId, pid, summary, cwd, mtimeMs, running, hasTranscript })
    }

    const chosen = rt.recent(candidates, {
      days: listDays,
      max: maxSessions,
      pinned: (c) => c.running || assistants.has(c.pid),
      id: (c) => c.pid,
      updatedAt: (c) => c.mtimeMs,
    })

    return chosen.map(({ rawId, pid, summary, cwd, mtimeMs, running, hasTranscript }) => {
      const steps = readTranscriptSteps(brainDirOf(rawId), false)
      const parsed = stepsToItems(steps, { brainDir: brainDirOf(rawId), full: false })
      if (!lastPushedSeq.has(pid)) lastPushedSeq.set(pid, parsed.toSeq)
      let status = running ? 'working' : transcriptStatus(steps)
      if (!hasTranscript && completedTurns.has(pid) && !running) status = 'done'
      const title =
        sessionTitles.get(rawId) ||
        summary?.Title ||
        (cwd ? basename(cwd) : 'Antigravity')
      const modeInfo = sessionModes.get(rawId)
      const row = {
        session_id: pid,
        title,
        cwd: cwd || home,
        status,
        updated_at: new Date(mtimeMs).toISOString(),
        rev: Math.max(1, parsed.toSeq),
        last_text: parsed.lastText || snippet(summary?.Preview || ''),
        model: sessionModels.get(rawId) || 'Gemini 4 Argon (High)',
        agent_label: LABEL,
      }
      if (cwd) {
        row.space_id = cwd
        row.space_name = basename(cwd)
      }
      const effMode = effectiveModeOf(rawId)
      if (effMode && effMode !== 'default') row.permission_mode = effMode
      if (modeInfo?.effort) row.effort = modeInfo.effort
      return row
    })
  }

  async function items(params) {
    const rawId = toAgyId(params.session_id)
    const bDir = brainDirOf(rawId)
    const steps = readTranscriptSteps(bDir, false)
    const { items: allItems, toSeq } = stepsToItems(steps, { brainDir: bDir, full: false })
    return page(allItems, params, { history: historyOf(rawId), toSeq })
  }

  async function cursor(sessionId) {
    const rawId = toAgyId(sessionId)
    const steps = readTranscriptSteps(brainDirOf(rawId), false)
    const { toSeq } = stepsToItems(steps, { brainDir: brainDirOf(rawId), full: false })
    return toSeq
  }

  async function item(params) {
    const rawId = toAgyId(params.session_id)
    const bDir = brainDirOf(rawId)
    const steps = readTranscriptSteps(bDir, true)
    const { items: fullItems } = stepsToItems(steps, { brainDir: bDir, full: true })
    const found = fullItems.find((it) => it.id === params.item_id)
    if (!found) throw new Error(`item ${params.item_id} not found`)
    return found
  }

  function mcpServersFor(pid, isAsst) {
    if (!existsSync(personadBin)) return []
    return mcpServers(personadBin, pid, isAsst)
  }

  async function applySessionOptions(rawId, { model, permission_mode, effort }) {
    if (model) {
      sessionModels.set(rawId, model)
      await acp.setModel(rawId, model).catch(() => {})
    }
    if (permission_mode) {
      const cur = sessionModes.get(rawId) || {}
      sessionModes.set(rawId, { ...cur, mode: permission_mode })
      await acp.setMode(rawId, personaModeToAgy(permission_mode)).catch(() => {})
    }
    if (effort && ['low', 'medium', 'high'].includes(effort)) {
      const cur = sessionModes.get(rawId) || {}
      sessionModes.set(rawId, { ...cur, effort })
      await acp.setConfigOption(rawId, 'thought_level', effort).catch(() => {})
    }
  }

  async function start(params) {
    const cwd = params.cwd || home
    const isAsst = Boolean(params.assistant) || isAssistantDir(cwd)
    const res = await acp.newSession({ cwd, mcpServers: [] })
    const rawId = res.sessionId
    const pid = toPersonaId(rawId)
    sessionCwds.set(rawId, cwd)
    if (isAsst) assistants.add(pid)

    await acp.ensureSessionLoaded(rawId, cwd, mcpServersFor(pid, isAsst))
    await applySessionOptions(rawId, {
      model: params.model,
      permission_mode: params.permission_mode,
      effort: params.effort,
    })

    const session = {
      session_id: pid,
      title: basename(cwd) || LABEL,
      cwd,
      status: params.prompt ? 'working' : 'done',
      updated_at: new Date().toISOString(),
      rev: 1,
      model: sessionModels.get(rawId) || 'Gemini 4 Argon (High)',
      agent_label: LABEL,
      space_id: cwd,
      space_name: basename(cwd),
      permission_mode: effectiveModeOf(rawId) !== 'default' ? effectiveModeOf(rawId) : undefined,
    }

    if (params.prompt) {
      await send({
        session_id: pid,
        text: params.prompt,
        client_msg_id: `start-${Date.now()}`,
        assistant: isAsst,
        mode: params.permission_mode,
        model: params.model,
        effort: params.effort,
      })
    }

    rt.sessionsChanged()
    return { session }
  }

  async function send(params) {
    const pid = toPersonaId(params.session_id)
    completedTurns.delete(pid)
    const rawId = toAgyId(pid)
    const cwd = sessionCwds.get(rawId) || home
    const isAsst = Boolean(params.assistant) || assistants.has(pid) || isAssistantDir(cwd)
    if (isAsst) assistants.add(pid)

    await acp.ensureSessionLoaded(rawId, cwd, mcpServersFor(pid, isAsst))
    await applySessionOptions(rawId, {
      model: params.model,
      permission_mode: params.mode,
      effort: params.effort,
    })

    const blocks = []
    let fullText = params.text || ''
    if (typeof params.context === 'string' && params.context.trim()) {
      const ctx = params.context.trim()
      const wrapped =
        contextWrapped(ctx)
          ? ctx
          : `<persona-context>\n${ctx}\n</persona-context>`
      fullText = `${wrapped}\n\n${fullText}`
    }
    if (fullText) {
      blocks.push({ type: 'text', text: fullText })
    }
    if (Array.isArray(params.images)) {
      for (const img of params.images) {
        if (img?.data) {
          blocks.push({
            type: 'image',
            data: img.data,
            mimeType: img.media_type || img.mime || 'image/png',
          })
        }
      }
    }

    const prevTurn = activeTurns.get(pid)
    if (prevTurn?.pollTimer) clearInterval(prevTurn.pollTimer)
    const pollTimer = setInterval(() => syncTranscriptAppend(rawId), 400)
    const turnToken = Symbol('turn')
    const prevPromise = prevTurn?.promise || Promise.resolve()
    const promise = prevPromise
      .catch(() => {})
      .then(() => acp.prompt(rawId, blocks))
      .catch((err) => log(`agy prompt error (${pid}): ${err.message}`))
      .finally(() => {
        // The turn is over: a card still waiting has nothing to answer for any more.
        cancelling.delete(rawId)
        withdrawPermissionAsks(rawId, 'the turn ended')
        if (activeTurns.get(pid)?.token === turnToken) {
          clearInterval(pollTimer)
          activeTurns.delete(pid)
          completedTurns.add(pid)
          rt.streamEnd(pid)
          syncTranscriptAppend(rawId)
          rt.sessionsChanged()
        }
      })
    activeTurns.set(pid, { streamingText: '', startedAt: Date.now(), pollTimer, token: turnToken, promise })
    rt.sessionsChanged()
  }

  async function interrupt(params) {
    const rawId = toAgyId(params.session_id)
    if (activeTurns.has(toPersonaId(rawId))) cancelling.add(rawId)
    withdrawPermissionAsks(rawId, 'interrupted')
    await acp.cancel(rawId)
  }

  async function rename(params) {
    const rawId = toAgyId(params.session_id)
    if (typeof params.title === 'string' && params.title.trim()) {
      sessionTitles.set(rawId, params.title.trim())
      rt.sessionsChanged()
    }
  }

  async function close(params) {
    const rawId = toAgyId(params.session_id)
    archived.add(rawId)
    await acp.closeSession(rawId)
    rt.sessionsChanged()
  }

  async function modes(params) {
    const rawId = toAgyId(params.session_id)
    const cwd = sessionCwds.get(rawId) || home
    await acp.ensureSessionLoaded(rawId, cwd, mcpServersFor(toPersonaId(rawId), assistants.has(toPersonaId(rawId))))
    await applySessionOptions(rawId, {
      permission_mode: params.permission_mode,
      effort: params.effort,
    })
    rt.sessionsChanged()
  }

  return {
    start: () => {
      void acp.ensureStarted().catch((err) => log(`agy warm start error: ${err.message}`))
      return rt.start()
    },
    /** Resolves once agy is gone. */
    stop: () => {
      rt.stop()
      // A turn's transcript poll would otherwise keep running; acp.stop() fails the prompts themselves.
      for (const turn of activeTurns.values()) clearInterval(turn.pollTimer)
      activeTurns.clear()
      return acp.stop()
    },
  }
}
