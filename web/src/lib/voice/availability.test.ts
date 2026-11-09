import { describe, expect, it } from "vitest";
import type { HubExtension } from "../extensions";
import type { Session } from "../types";
import { browserVoice, entryMode, evaluate, httpsAddress, pickVoice, readInfo, type VoiceInfoState } from "./availability";
import { unavailableNote, endNote } from "./notes";

const ext = (id: string, status: string, extra: Partial<HubExtension> = {}): HubExtension => ({ id, name: id, kind: "voice", status, ...extra });
const session = (extra: Partial<Session> = {}) => ({ machine_id: "m", session_id: "s", ...extra }) as Session;
const info = (protocol: number, setup: string[] = []): VoiceInfoState => ({ kind: "info", info: { protocol, features: [], setup } });
const ok = { secure: true, audio: true };

describe("whether a conversation offers voice", () => {
  it("offers it with a running protocol 2 extension, on a sendable chat, in a secure browser", () => {
    const a = evaluate({ session: session(), extensions: [ext("voice", "running")], info: info(2), browser: ok });
    expect(a).toEqual({ kind: "available", extensionID: "voice" });
    expect(entryMode(a)).toBe("start");
  });

  it("hides it where nothing can be done here", () => {
    expect(evaluate({ session: undefined, extensions: [], info: undefined, browser: ok })).toEqual({ kind: "noConversation" });
    const readOnly = session({ source_kind: "codex_history", capabilities: { send: false } as Session["capabilities"] });
    expect(evaluate({ session: readOnly, extensions: [ext("voice", "running")], info: info(2), browser: ok })).toEqual({ kind: "readOnly" });
    expect(evaluate({ session: session(), extensions: [], info: undefined, browser: ok })).toEqual({ kind: "noExtension" });
    expect(evaluate({ session: session(), extensions: [ext("voice", "disabled")], info: undefined, browser: ok })).toEqual({ kind: "noExtension" });
    expect(evaluate({ session: session(), extensions: [ext("voice", "running")], info: { kind: "forbidden" }, browser: ok })).toEqual({ kind: "limitedDevice" });
    expect(evaluate({ session: session(), extensions: undefined, info: undefined, browser: ok })).toEqual({ kind: "unknown" });
    expect(evaluate({ session: session(), extensions: [ext("voice", "running")], info: { kind: "failed" }, browser: ok })).toEqual({ kind: "unknown" });
    for (const k of ["noConversation", "readOnly", "noExtension", "limitedDevice", "unknown"] as const) expect(entryMode({ kind: k } as never)).toBeNull();
  });

  it("explains a hub that needs setting up or is too old", () => {
    expect(evaluate({ session: session(), extensions: [ext("voice", "needs_setup", { needs_setup: ["No key"] })], info: undefined, browser: ok })).toEqual({ kind: "needsSetup", extensionID: "voice", reasons: ["No key"] });
    expect(evaluate({ session: session(), extensions: [ext("voice", "running")], info: info(2, ["No address"]), browser: ok })).toEqual({ kind: "needsSetup", extensionID: "voice", reasons: ["No address"] });
    expect(evaluate({ session: session(), extensions: [ext("voice", "running")], info: { kind: "missing" }, browser: ok })).toEqual({ kind: "outdated", extensionID: "voice" });
    expect(evaluate({ session: session(), extensions: [ext("voice", "running")], info: info(1), browser: ok })).toEqual({ kind: "outdated", extensionID: "voice" });
    expect(entryMode({ kind: "outdated", extensionID: "voice" })).toBe("explain");
  });

  it("explains a page without the microphone: plain HTTP, or a browser without the APIs", () => {
    const insecure = evaluate({ session: session(), extensions: [ext("voice", "running")], info: info(2), browser: { secure: false, audio: false } });
    expect(insecure).toEqual({ kind: "insecure", extensionID: "voice" });
    expect(entryMode(insecure)).toBe("explain");
    expect(evaluate({ session: session(), extensions: [ext("voice", "running")], info: info(2), browser: { secure: true, audio: false } })).toEqual({ kind: "noAudio", extensionID: "voice" });
    // nothing about HTTPS on a hub without voice
    expect(evaluate({ session: session(), extensions: [], info: undefined, browser: { secure: false, audio: false } })).toEqual({ kind: "noExtension" });
  });

  it("picks the first running voice extension with a safe id", () => {
    expect(pickVoice([ext("Bad/Id", "running"), ext("v2", "stopped"), ext("voice", "running"), ext("later", "running")])?.id).toBe("voice");
    expect(pickVoice([{ ...ext("x", "running"), kind: "channel" }])).toBeUndefined();
  });

  it("reads /info leniently", () => {
    expect(readInfo({ protocol: 2, features: ["target", 1], setup: "No key" })).toEqual({ protocol: 2, features: ["target"], setup: ["No key"] });
    expect(readInfo({ setup: ["", "a"] })).toEqual({ protocol: 1, features: [], setup: ["a"] });
    expect(readInfo(null)).toEqual({ protocol: 1, features: [], setup: [] });
  });

  it("reads what the browser can do", () => {
    const fn = () => {};
    expect(browserVoice({ isSecureContext: true, navigator: { mediaDevices: { getUserMedia: fn } }, AudioWorkletNode: fn })).toEqual({ secure: true, audio: true });
    expect(browserVoice({ isSecureContext: false, navigator: {} })).toEqual({ secure: false, audio: false });
  });
});

describe("the hub's HTTPS address", () => {
  const here = { origin: "http://192.0.2.10:18660", pathname: "/s/m/s", search: "?x=1", hash: "" };

  it("prefers an https public_url, keeping its sub-path and this page's route", () => {
    expect(httpsAddress({ public_url: "https://hub.example.com/persona", endpoints: [{ url: "https://[2001:db8::1]:18662", kind: "ipv6" }] }, here, "")).toBe("https://hub.example.com/persona/s/m/s?x=1");
  });

  it("then an https address by name, then any https address", () => {
    const endpoints = [{ url: "http://192.0.2.10:18660", kind: "lan" }, { url: "https://[2001:db8::1]:18662", kind: "ipv6" }, { url: "https://hub.example.com:18662", kind: "name" }];
    expect(httpsAddress({ public_url: "http://192.0.2.10:18660", endpoints }, here, "")).toBe("https://hub.example.com:18662/s/m/s?x=1");
    expect(httpsAddress({ endpoints: endpoints.slice(0, 2) }, here, "")).toBe("https://[2001:db8::1]:18662/s/m/s?x=1");
  });

  it("drops this page's own sub-path for an endpoint, and has none without https", () => {
    expect(httpsAddress({ endpoints: [{ url: "https://hub.example.com", kind: "name" }] }, { ...here, pathname: "/persona/s/m/s" }, "/persona")).toBe("https://hub.example.com/s/m/s?x=1");
    expect(httpsAddress({ public_url: "http://x", endpoints: "nope" }, here, "")).toBeNull();
    expect(httpsAddress({ public_url: "https://hub.example.com" }, { ...here, origin: "https://hub.example.com" }, "")).toBeNull();
  });
});

describe("the words around a call", () => {
  it("explains HTTP with the hub's HTTPS address when it has one", () => {
    expect(unavailableNote({ kind: "insecure", extensionID: "voice" }, "https://hub.example.com/s/m/s")).toMatchObject({ title: "麦克风要在 HTTPS 下才能用", https: "https://hub.example.com/s/m/s" });
    expect(unavailableNote({ kind: "insecure", extensionID: "voice" }, null)?.https).toBeUndefined();
    expect(unavailableNote({ kind: "needsSetup", extensionID: "voice", reasons: ["No key"] }, null)).toEqual({ title: "还没配实时语音服务", message: "No key", settings: "/settings?tab=extensions&extension=voice" });
    expect(unavailableNote({ kind: "available", extensionID: "voice" }, null)).toBeNull();
  });

  it("leaves nothing after the user's own endings, and Continue or Retry after the others", () => {
    expect(endNote({ kind: "user" }, "voice")).toBeNull();
    expect(endNote({ kind: "left" }, "voice")).toBeNull();
    expect(endNote({ kind: "lost" }, "voice")).toMatchObject({ again: "resume" });
    expect(endNote({ kind: "interrupted" }, "voice")).toMatchObject({ again: "resume" });
    expect(endNote({ kind: "permissionDenied" }, "voice")).toMatchObject({ again: "retry" });
    expect(endNote({ kind: "rejected", status: 401 }, "voice")).toMatchObject({ again: null });
    expect(endNote({ kind: "failed", code: "upstream_forbidden", message: "401 from upstream" }, "voice")).toEqual({
      text: "连不上实时语音服务（Key 不对，或所在地区不支持）", again: "retry", settings: "/settings?tab=extensions&extension=voice", details: "401 from upstream",
    });
    expect(endNote({ kind: "modelUnavailable", why: "读不到插件模型列表" }, "voice")).toMatchObject({ settings: "/settings?tab=model-config&purpose=plugin.voice" });
    expect(endNote({ kind: "failed", message: "" }, "voice")).toEqual({ text: "语音出了问题", again: "retry", details: undefined });
  });
});
