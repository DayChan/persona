import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const calls = vi.hoisted(() => ({ list: [] as { path: string; init?: { json?: unknown } }[], reply: (_path: string, _json: unknown): unknown => ({}) }));
vi.mock("./api", () => ({
  api: vi.fn(async (path: string, init?: { json?: unknown }) => {
    calls.list.push({ path, init });
    return calls.reply(path, init?.json);
  }),
  enc: encodeURIComponent,
}));

import {
  detectLanguage, family, getTranslatePrefs, hubTranslation, isForeign, languageName, MAILS_KEPT, parseChats, requestTranslation,
  resetTranslateForTests, setTranslatePrefs, strippedHTML, TEXTS_KEPT, translateCacheSizesForTests, translateMail, translatesChat,
  translatesMail, translationOf,
} from "./translate";

function memoryStorage() {
  const m = new Map<string, string>();
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
    dump: () => Object.fromEntries(m),
  };
}

let storage = memoryStorage();
beforeEach(() => {
  storage = memoryStorage();
  vi.stubGlobal("localStorage", storage);
  resetTranslateForTests();
  calls.list = [];
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("detectLanguage", () => {
  it("tells the scripts apart", () => {
    expect(detectLanguage("周四我临时要出差，分享能不能挪到周二下午？")).toBe("zh");
    expect(detectLanguage("来週の木曜日は出張なので、レビューを火曜日に移せますか？")).toBe("ja");
    expect(detectLanguage("다음 주 목요일에 출장이 있어서 리뷰를 화요일로 옮길 수 있을까요?")).toBe("ko");
    expect(detectLanguage("Я в четверг в командировке, можно перенести обзор на вторник?")).toBe("ru");
  });

  it("keeps a Chinese sentence with English terms Chinese", () => {
    expect(detectLanguage("我先看了一下，tab bar 的 icon 偏细，release 分支还有两个问题")).toBe("zh");
  });

  it("tells Latin-script languages apart by their common words", () => {
    expect(detectLanguage("Hi, I'm travelling on Thursday. Could we move the design review to Tuesday afternoon?")).toBe("en");
    expect(detectLanguage("Bonjour, je suis en déplacement jeudi. Est-ce que nous pouvons déplacer la revue à mardi ?")).toBe("fr");
    expect(detectLanguage("Ich bin am Donnerstag nicht da. Können wir die Besprechung auf den Dienstag legen? Danke")).toBe("de");
    expect(detectLanguage("Hola, el jueves estoy de viaje. ¿Podemos mover la revisión al martes por la tarde? Gracias")).toBe("es");
  });

  it("says nothing about too little text", () => {
    expect(detectLanguage("OK 👍")).toBeNull();
    expect(detectLanguage("2026-10-28 12:30")).toBeNull();
    expect(detectLanguage("")).toBeNull();
  });

  it("calls Latin script it cannot place latin", () => {
    expect(detectLanguage("Lorem ipsum dolor sit amet")).toBe("latin");
  });
});

describe("isForeign", () => {
  it("treats both Chinese scripts as one language", () => {
    expect(family("zh-Hans")).toBe("zh");
    expect(isForeign("今天下午三点开会", "zh-Hant")).toBe(false);
    expect(isForeign("Could we move the design review to Tuesday?", "zh-Hans")).toBe(true);
    expect(isForeign("Could we move the design review to Tuesday?", "en")).toBe(false);
  });

  it("only calls unplaceable Latin text foreign to a target in another script", () => {
    expect(isForeign("Lorem ipsum dolor sit amet", "zh-Hans")).toBe(true);
    expect(isForeign("Lorem ipsum dolor sit amet", "en")).toBe(false);
    expect(isForeign("OK", "en")).toBe(false);
  });
});

describe("prefs", () => {
  it("defaults to off, Simplified Chinese, mail on, no chats", () => {
    expect(getTranslatePrefs()).toEqual({ enabled: false, target: "zh-Hans", mail: true, chats: [] });
  });

  it("keeps them in this browser", () => {
    setTranslatePrefs({ enabled: true, target: "en", chats: [{ id: "f2", name: "设计评审" }] });
    expect(storage.dump()).toMatchObject({ "translate.enabled": "1", "translate.target": "en", "translate.mail": "1" });
    resetTranslateForTests();
    const p = getTranslatePrefs();
    expect(p).toEqual({ enabled: true, target: "en", mail: true, chats: [{ id: "f2", name: "设计评审" }] });
    expect(translatesMail(p)).toBe(true);
    expect(translatesChat(p, "f2")).toBe(true);
    expect(translatesChat(p, "f3")).toBe(false);
    expect(translatesChat({ ...p, enabled: false }, "f2")).toBe(false);
  });

  it("drops an unknown target and broken chat lists", () => {
    storage.setItem("translate.target", "xx");
    storage.setItem("translate.larkChats", "{not json");
    expect(getTranslatePrefs()).toMatchObject({ target: "zh-Hans", chats: [] });
    expect(parseChats('[{"id":"f1"},{"name":"no id"}]')).toEqual([{ id: "f1", name: "f1" }]);
  });

  it("uses the hub's translation of a mail item only when mail is translated", () => {
    const it = { mail: {}, translations: { "zh-Hans": { subject: "主题", preview: "预览" } } };
    const on = { enabled: true, target: "zh-Hans", mail: true, chats: [] };
    expect(hubTranslation(it, on)).toEqual({ subject: "主题", preview: "预览" });
    expect(hubTranslation(it, { ...on, mail: false })).toBeUndefined();
    expect(hubTranslation(it, { ...on, target: "en" })).toBeUndefined();
    expect(hubTranslation({ translations: it.translations }, on)).toBeUndefined();
  });

  it("names the languages", () => {
    expect(languageName("ja")).toBe("日本語");
    expect(languageName("xx")).toBe("xx");
  });
});

describe("translations", () => {
  it("asks for the texts of a moment in one request", async () => {
    vi.useFakeTimers();
    calls.reply = (_p, json) => ({ texts: (json as { texts: string[] }).texts.map((t) => (t === "skip" ? "" : `译：${t}`)) });
    requestTranslation("Hello there, how are you", "zh-Hans");
    requestTranslation("skip", "zh-Hans");
    requestTranslation("Hello there, how are you", "zh-Hans"); // once
    await vi.advanceTimersByTimeAsync(350);
    expect(calls.list).toHaveLength(1);
    expect(calls.list[0]).toMatchObject({ path: "/translate", init: { json: { texts: ["Hello there, how are you", "skip"], target: "zh-Hans" } } });
    expect(translationOf("Hello there, how are you", "zh-Hans")).toBe("译：Hello there, how are you");
    expect(translationOf("skip", "zh-Hans")).toBeUndefined();
    requestTranslation("skip", "zh-Hans"); // failed: not again this visit
    await vi.advanceTimersByTimeAsync(350);
    expect(calls.list).toHaveLength(1);
  });

  it("asks for a mail's translation once", async () => {
    calls.reply = () => ({ subject: "主题", text: "正文" });
    const a = await translateMail("acc1", 504, "INBOX", "zh-Hans");
    const b = await translateMail("acc1", 504, "INBOX", "zh-Hans");
    expect(a).toEqual({ subject: "主题", text: "正文" });
    expect(b).toBe(a);
    expect(calls.list).toEqual([{ path: "/translate/mail", init: { method: "POST", json: { account_id: "acc1", uid: 504, mailbox: "INBOX", target: "zh-Hans" } } }]);
  });

  it("keeps only the translations read last", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    calls.reply = (_p, json) => ({ texts: (json as { texts: string[] }).texts.map((t) => `译：${t}`) });
    const msg = (i: number) => `Could we move review number ${i} to Tuesday?`;
    for (let i = 0; i < TEXTS_KEPT; i++) requestTranslation(msg(i), "zh-Hans");
    await vi.advanceTimersByTimeAsync(350);
    expect(calls.list).toHaveLength(Math.ceil(TEXTS_KEPT / 30));
    expect(translateCacheSizesForTests().texts).toBe(TEXTS_KEPT);
    expect(translationOf(msg(0), "zh-Hans")).toBe(`译：${msg(0)}`); // read again: now the last to go

    for (let i = TEXTS_KEPT; i < TEXTS_KEPT + 100; i++) requestTranslation(msg(i), "zh-Hans");
    await vi.advanceTimersByTimeAsync(350);
    expect(translateCacheSizesForTests().texts).toBe(TEXTS_KEPT);
    expect(translationOf(msg(TEXTS_KEPT + 99), "zh-Hans")).toBe(`译：${msg(TEXTS_KEPT + 99)}`);
    expect(translationOf(msg(0), "zh-Hans")).toBe(`译：${msg(0)}`);
    expect(translationOf(msg(1), "zh-Hans")).toBeUndefined();
    expect(translationOf(msg(100), "zh-Hans")).toBeUndefined();
    expect(translationOf(msg(101), "zh-Hans")).toBe(`译：${msg(101)}`);

    const asked = calls.list.length;
    requestTranslation(msg(1), "zh-Hans"); // dropped: asked again, as on first view
    await vi.advanceTimersByTimeAsync(350);
    expect(calls.list).toHaveLength(asked + 1);
    expect(translationOf(msg(1), "zh-Hans")).toBe(`译：${msg(1)}`);
  });

  it("keeps only the newest failures", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    calls.reply = () => ({ texts: [] });
    for (let i = 0; i < TEXTS_KEPT + 50; i++) requestTranslation(`Untranslatable message ${i}`, "zh-Hans");
    await vi.advanceTimersByTimeAsync(350);
    expect(translateCacheSizesForTests()).toMatchObject({ texts: 0, failed: TEXTS_KEPT });
    const asked = calls.list.length;
    requestTranslation(`Untranslatable message ${TEXTS_KEPT + 49}`, "zh-Hans"); // still failed: not again
    await vi.advanceTimersByTimeAsync(350);
    expect(calls.list).toHaveLength(asked);
  });

  it("keeps the newest mail translations", async () => {
    calls.reply = (_p, json) => ({ subject: `主题 ${(json as { uid: number }).uid}`, text: "正文" });
    for (let uid = 0; uid <= MAILS_KEPT; uid++) await translateMail("acc1", uid, "INBOX", "zh-Hans");
    expect(calls.list).toHaveLength(MAILS_KEPT + 1);
    expect(translateCacheSizesForTests().mails).toBe(MAILS_KEPT);
    await translateMail("acc1", MAILS_KEPT, "INBOX", "zh-Hans"); // the newest: still here
    expect(calls.list).toHaveLength(MAILS_KEPT + 1);
    expect(await translateMail("acc1", 0, "INBOX", "zh-Hans")).toEqual({ subject: "主题 0", text: "正文" }); // dropped: asked again
    expect(calls.list).toHaveLength(MAILS_KEPT + 2);
    expect(translateCacheSizesForTests().mails).toBe(MAILS_KEPT);
  });

  it("strips mail HTML down to its text", () => {
    expect(strippedHTML("<head><title>x</title></head><style>p{}</style><p>Hi&nbsp;there</p>").replace(/\s+/g, " ").trim()).toBe("Hi there");
  });
});
