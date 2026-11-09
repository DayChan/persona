import { describe, expect, it } from "vitest";
import type { Item } from "../types";
import { cutLine, parseFrame, recentLines, resendFrame, runOpen, stampDate, startFrame, textFrame, INTERRUPT_FRAME, validExtensionID, voiceSocketURL } from "./wire";

const item = (seq: number, role: Item["role"], text: string, kind: Item["kind"] = "text"): Item => ({ id: `i${seq}`, seq, role, kind, text });

describe("voice frames from the extension", () => {
  it("reads ready with its rates, features and target", () => {
    const f = parseFrame(JSON.stringify({ type: "ready", protocol: 2, features: ["target", "runs", 3], input_rate: 24000, output_rate: 24000, model: "m1",
      target: { machine_id: "m", session_id: "s", kind: "agent", agent_label: "Claude Code", machine_name: "Workstation" } }));
    expect(f).toEqual({ type: "ready", ready: { protocol: 2, features: ["target", "runs"], inputRate: 24000, outputRate: 24000, model: "m1", voice: undefined,
      target: { machineID: "m", sessionID: "s", kind: "agent", title: undefined, agentLabel: "Claude Code", machineName: "Workstation" } } });
  });

  it("falls back to protocol 1 and the default rates on odd fields", () => {
    const f = parseFrame(JSON.stringify({ type: "ready", protocol: "2", input_rate: -1, output_rate: 1e9 }));
    expect(f).toMatchObject({ type: "ready", ready: { protocol: 1, inputRate: 16000, outputRate: 24000, target: undefined } });
  });

  it("reads transcripts, an empty before as none", () => {
    expect(parseFrame('{"type":"transcript","id":"u3","role":"user","text":"第一个","final":false,"before":"a3"}')).toEqual({ type: "transcript", transcript: { id: "u3", role: "user", text: "第一个", final: false, before: "a3" } });
    expect(parseFrame('{"type":"transcript","id":"a1","role":"assistant","text":"hi","final":true,"before":""}')).toMatchObject({ transcript: { before: undefined, final: true } });
  });

  it("reads a run with its 409 details, started_at kept as it came", () => {
    const f = parseFrame(JSON.stringify({ type: "run", id: "voice-1", task: "fix lint", state: "refused", msg_ids: ["voice-1"], base_seq: 812, started_at: 1793872800123,
      code: "input_draft", message: "…", draft: "[⧉ Selected 3 lines from foo.go] fix it", clearable: "fix it" }));
    expect(f).toEqual({ type: "run", run: { id: "voice-1", task: "fix lint", state: "refused", msgIDs: ["voice-1"], baseSeq: 812, startedAt: 1793872800123,
      code: "input_draft", message: "…", draft: "[⧉ Selected 3 lines from foo.go] fix it", clearable: "fix it" } });
    expect(parseFrame('{"type":"run","state":"done"}')).toEqual({ type: "other", name: "run" });
    expect(parseFrame('{"type":"run","id":"r","started_at":"2026-11-06T10:00:00Z","clearable":""}')).toMatchObject({ run: { startedAt: "2026-11-06T10:00:00Z", clearable: undefined } });
  });

  it("reads ask, link, tool, clear and error frames", () => {
    expect(parseFrame('{"type":"ask","interaction_id":"ix1","state":"asking"}')).toEqual({ type: "ask", ask: { interactionID: "ix1", state: "asking", by: undefined } });
    expect(parseFrame('{"type":"link","state":"paused"}')).toEqual({ type: "link", state: "paused" });
    expect(parseFrame('{"type":"tool","id":"t1","label":"Searching","done":false}')).toEqual({ type: "tool", id: "t1", label: "Searching", done: false });
    expect(parseFrame('{"type":"clear"}')).toEqual({ type: "clear" });
    expect(parseFrame('{"type":"error","code":"upstream_forbidden","message":"401","fatal":true}')).toEqual({ type: "error", failure: { code: "upstream_forbidden", message: "401", fatal: true } });
  });

  it("keeps an unknown frame and refuses what is not a frame", () => {
    expect(parseFrame('{"type":"newer","x":1}')).toEqual({ type: "other", name: "newer" });
    expect(parseFrame("not json")).toBeNull();
    expect(parseFrame("[1]")).toBeNull();
    expect(parseFrame('{"no":"type"}')).toBeNull();
  });
});

describe("frames the page sends", () => {
  it("starts with protocol 2, the target and only what it has", () => {
    expect(JSON.parse(startFrame({ target: { machine_id: "m", session_id: "s" } }))).toEqual({ type: "start", protocol: 2, target: { machine_id: "m", session_id: "s" } });
  });

  it("hands back history and the open runs on a resume, started_at unchanged", () => {
    const frame = JSON.parse(startFrame({
      model: "rt/m1", voice: "soft", target: { machine_id: "m", session_id: "s" }, recent: [{ role: "user", text: "hi" }],
      history: [{ role: "assistant", text: "ok" }], echoCancellation: false,
      resume: [
        { id: "voice-1", task: "fix", state: "working", msgIDs: ["voice-1"], baseSeq: 7, startedAt: 1793872800123 },
        { id: "voice-2", task: "lint", state: "held", msgIDs: [], baseSeq: 9, startedAt: "2026-11-06T10:00:00Z" },
      ],
    }));
    expect(frame).toEqual({
      type: "start", protocol: 2, model: "rt/m1", voice: "soft", target: { machine_id: "m", session_id: "s" }, recent: [{ role: "user", text: "hi" }],
      history: [{ role: "assistant", text: "ok" }], echo_cancellation: false,
      resume: { runs: [
        { id: "voice-1", msg_ids: ["voice-1"], task: "fix", base_seq: 7, started_at: 1793872800123, state: "working" },
        { id: "voice-2", msg_ids: [], task: "lint", base_seq: 9, started_at: "2026-11-06T10:00:00Z", state: "held" },
      ] },
    });
  });

  it("writes interrupt, text and resend", () => {
    expect(JSON.parse(INTERRUPT_FRAME)).toEqual({ type: "interrupt" });
    expect(JSON.parse(textFrame("hello"))).toEqual({ type: "text", text: "hello" });
    expect(JSON.parse(resendFrame("voice-1"))).toEqual({ type: "resend", id: "voice-1" });
    expect(JSON.parse(resendFrame("voice-1", "fix it"))).toEqual({ type: "resend", id: "voice-1", clear_draft: "fix it" });
  });
});

describe("recent turns", () => {
  it("keeps the last three turns' words, without tools, thinking or context blocks", () => {
    const items = [
      item(1, "user", "first"), item(2, "assistant", "a1"),
      item(3, "user", "second"), item(4, "assistant", "x", "tool_use"), item(5, "assistant", "thinking", "thinking"),
      item(6, "user", '<persona-context kind="doc" title="t" url="u">\nbig\n</persona-context> third'), item(7, "assistant", "a3"),
      item(8, "user", "fourth"), item(9, "assistant", "  a4\n\n more "),
    ];
    expect(recentLines(items)).toEqual([
      { role: "user", text: "second" }, { role: "user", text: "third" }, { role: "assistant", text: "a3" },
      { role: "user", text: "fourth" }, { role: "assistant", text: "a4 more" },
    ]);
  });

  it("cuts each line to 300 characters", () => {
    const [line] = recentLines([item(1, "user", "字".repeat(400))]);
    expect(Array.from(line.text)).toHaveLength(300);
    expect(line.text.endsWith("…")).toBe(true);
    expect(cutLine("short", 300)).toBe("short");
  });
});

describe("runs and stamps", () => {
  it("knows which runs are still open", () => {
    expect(["sending", "sent", "working", "needs_you", "blocked", "held", "later"].every(runOpen)).toBe(true);
    expect(["done", "failed", "refused", "unknown", "folded"].some(runOpen)).toBe(false);
  });

  it("reads started_at as seconds, milliseconds or RFC 3339", () => {
    expect(stampDate(1793872800)).toBe(1793872800000);
    expect(stampDate(1793872800123)).toBe(1793872800123);
    expect(stampDate("2026-11-06T10:00:00Z")).toBe(Date.parse("2026-11-06T10:00:00Z"));
    expect(stampDate("nope")).toBeUndefined();
  });
});

describe("the socket's address", () => {
  it("is ws or wss on the page's own origin and sub-path, with the token in the query", () => {
    expect(voiceSocketURL({ protocol: "http:", host: "127.0.0.1:18660" }, "", "/extensions", "voice", "jd_a b")).toBe("ws://127.0.0.1:18660/api/v1/extensions/voice/ws?token=jd_a%20b");
    expect(voiceSocketURL({ protocol: "https:", host: "hub.example.com" }, "/persona", "/plugins", "voice", "t")).toBe("wss://hub.example.com/persona/api/v1/plugins/voice/ws?token=t");
  });

  it("takes only path-safe extension ids", () => {
    expect(validExtensionID("voice_2-x")).toBe(true);
    expect(validExtensionID("../x")).toBe(false);
    expect(validExtensionID("")).toBe(false);
  });
});
