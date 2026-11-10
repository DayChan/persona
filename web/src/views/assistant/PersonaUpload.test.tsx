// @vitest-environment jsdom
// The avatar picker and uploaded looks (#2404): the Upload card only on a hub with `look_uploads`, a look made from an
// upload listed and chosen as soon as the hub answers (its pushes change nothing), and deleting one: asked first, the
// hub's choice after it taken, its refusal shown as it is.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { LookCurrent, LookView } from "../../lib/persona";

const hub = vi.hoisted(() => ({
  features: [] as string[],
  looks: [] as unknown[],
  current: { id: null, effective: "persona" } as { id: string | null; effective: string },
  deletes: [] as string[],
  refuseDelete: null as null | { status: number; body: Record<string, unknown> },
  uploads: [] as unknown[],
  listeners: new Map<string, (m: Record<string, unknown>) => void>(),
  /** What happens on the hub between an upload's or a delete's change and its answer (pushes, another device). */
  meanwhile: null as null | (() => Promise<void>),
  /** Image loads held back (a list read waiting for a package's files) until let go. */
  held: null as null | (() => void)[],
}));

vi.mock("../../lib/api", async (orig) => {
  const real = await orig<typeof import("../../lib/api")>();
  return {
    ...real,
    getToken: () => "",
    api: vi.fn(async (path: string, init?: { method?: string; json?: { id: string } }) => {
      if (path === "/looks") return { looks: hub.looks, current: hub.current };
      if (path === "/looks/current" && init?.method === "PUT") {
        hub.current = { id: init.json!.id, effective: init.json!.id };
        return hub.current;
      }
      if (path.startsWith("/looks/uploads/") && init?.method === "DELETE") {
        hub.deletes.push(path);
        if (hub.refuseDelete) throw new real.ApiError(hub.refuseDelete.status, String(hub.refuseDelete.body.error), hub.refuseDelete.body);
        const id = decodeURIComponent(path.split("/").pop()!);
        hub.looks = hub.looks.filter((l) => (l as LookView).id !== id);
        if (hub.current.id === id) hub.current = { id: "persona", effective: "persona" };
        const answer = { current: hub.current };
        await hub.meanwhile?.();
        return answer;
      }
      throw new real.ApiError(404, "not found", {});
    }),
  };
});
vi.mock("../../lib/store", () => ({
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => hub.listeners.set(type, l),
  hubHas: (features: string[], feature: string) => features.includes(feature),
  useStore: <T,>(select: (s: { features: string[] }) => T) => select({ features: hub.features }),
}));
vi.mock("../../lib/look-upload", async (orig) => {
  const real = await orig<typeof import("../../lib/look-upload")>();
  return {
    ...real,
    readImage: async () => ({ source: {}, size: { w: 600, h: 600 }, release: () => {} }),
    exportCrop: () => ({ image: "Q1JPUA==", mime: "image/png", side: 600 }),
    uploadLook: async (body: unknown) => {
      hub.uploads.push(body);
      const look = made();
      // the hub lists its own packages, then the extensions'
      const at = hub.looks.findIndex((l) => (l as LookView).source === "extension");
      hub.looks = at < 0 ? [...hub.looks, look] : [...hub.looks.slice(0, at), look, ...hub.looks.slice(at)];
      hub.current = { id: look.id, effective: look.id };
      const answer = { look, current: hub.current };
      await hub.meanwhile?.();
      return answer;
    },
  };
});
class FakeImage {
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  set src(_: string) {
    const load = () => this.onload?.();
    if (hub.held) hub.held.push(load);
    else queueMicrotask(load);
  }
}

const manifest = (id: string, name: string, subtitle?: string) => ({
  format: 1, id, name, ...(subtitle ? { subtitle } : {}),
  avatar: { sizes: { "64": "avatar/64.png", "128": "avatar/128.png" } },
  dot: { size: 144, states: { idle: { file: "dot/idle.png" } } },
  source: { kind: "upload" },
});
const builtins: LookView[] = [{ id: "persona", source: "builtin", name: "Mochi", app_icon: "persona" }];
const doudou: LookView = { id: "u-demo0001", source: "hub", name: "Doudou", app_icon: "persona", rev: "r1", manifest: manifest("u-demo0001", "Doudou"), uploaded: true };
const frog: LookView = { id: "nwverse", source: "hub", name: "Milk Frog", app_icon: "persona", rev: "r2", manifest: { ...manifest("nwverse", "Milk Frog"), source: { kind: "hub" } } };
const ext: LookView = { id: "aurora", source: "extension", name: "Aurora", app_icon: "persona", rev: "r3", extension: "x", extension_name: "Example Looks", manifest: { ...manifest("aurora", "Aurora"), source: { kind: "extension" } } };
const made = (): LookView => ({ id: "u-new00001", source: "hub", name: "Bean", subtitle: "A round one", app_icon: "persona", rev: "r9", manifest: manifest("u-new00001", "Bean", "A round one"), uploaded: true });

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;

async function load() {
  vi.resetModules();
  const { initI18n } = await import("../../i18n");
  await initI18n("en");
  const persona = await import("./Persona");
  // the upload dialog is its own chunk (views/LazyOverlay.tsx): in before it is drawn, in this fresh module set
  await (await import("../../lib/lazy-page")).loadPages();
  return { ...persona, t: (await import("../../i18n")).t };
}

async function draw() {
  const p = await load();
  await act(async () => root.render(<p.PersonaPicker />));
  await act(async () => {}); // the list read and its preload
  return p;
}

const radios = () => [...host.querySelectorAll<HTMLElement>('[role="radio"]')];
const checked = () => host.querySelector('[role="radio"][aria-checked="true"]')?.getAttribute("aria-label");
const byLabel = (label: string) => document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
const dialogButton = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((b) => b.textContent === text)!;

/** Opens Upload, picks an image, names it Bean and sends it, until the dialog has the answer. */
async function uploadBean(t: (key: string) => string) {
  await act(async () => byLabel(t("assistant-persona.upload_label"))!.click());
  const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
  const input = dialog.querySelector<HTMLInputElement>('[data-testid="look-upload-file"]')!;
  Object.defineProperty(input, "files", { value: [new File(["x"], "bean.png", { type: "image/png" })] });
  await act(async () => input.dispatchEvent(new Event("change", { bubbles: true })));
  const name = document.getElementById([...dialog.querySelectorAll("label")].find((l) => l.textContent === t("assistant-lookupload.name"))!.htmlFor) as HTMLInputElement;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(name, "Bean");
    name.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const send = [...dialog.querySelectorAll("button")].find((b) => b.textContent === t("assistant-lookupload.upload"))!;
  await act(async () => send.click());
  await settle(60);
}

/** Lets timers and the reads they start run out. */
const settle = (ms = 20) => act(async () => new Promise((r) => setTimeout(r, ms)));
const push = (type: string, m: Record<string, unknown> = {}) => hub.listeners.get(type)!({ type, ...m });
/** Another device chooses a look: the hub keeps it and pushes it. */
const elsewhere = (id: string) => {
  hub.current = { id, effective: id };
  push("look", { id, effective: id });
};
/** The list read the next `personas` push starts waits for the chosen package's files: its rev is new. */
const holdNextRead = (id: string) => {
  hub.looks = hub.looks.map((l) => ((l as LookView).id === id ? { ...(l as LookView), rev: "r-new" } : l));
  hub.held = [];
};
const letImagesLoad = async () => {
  const held = hub.held ?? [];
  hub.held = null;
  await act(async () => held.forEach((load) => load()));
};

describe("uploaded looks in the picker", () => {
  beforeAll(() => import("./Persona"), 60_000);
  beforeEach(() => {
    localStorage.clear();
    vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }));
    vi.stubGlobal("requestAnimationFrame", (f: FrameRequestCallback) => setTimeout(() => f(0), 0));
    vi.stubGlobal("Image", FakeImage);
    hub.features = ["look_uploads"];
    hub.looks = [...builtins, frog, doudou, ext];
    hub.current = { id: "u-demo0001", effective: "u-demo0001" } satisfies LookCurrent;
    hub.deletes = [];
    hub.refuseDelete = null;
    hub.uploads = [];
    hub.meanwhile = null;
    hub.held = null;
    hub.listeners.clear();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    localStorage.clear();
  });

  it("offers no upload, and no delete, on a hub without look_uploads", async () => {
    hub.features = [];
    const { t } = await draw();
    expect(byLabel(t("assistant-persona.upload_label"))).toBeNull();
    expect(byLabel(t("assistant-persona.delete_label", { name: "Doudou" }))).toBeNull();
    expect(radios().map((r) => r.getAttribute("aria-label"))).toEqual(["Mochi, the default avatar", "Milk Frog", "Doudou", "Aurora"]);
  });

  it("offers Upload as the last card, after every look", async () => {
    const { t } = await draw();
    const upload = byLabel(t("assistant-persona.upload_label"))!;
    expect(upload).not.toBeNull();
    expect(upload.getAttribute("role")).toBeNull();
    expect(upload.closest('[role="radiogroup"]')!.lastElementChild).toBe(upload);
    expect(upload.textContent).toContain(t("assistant-persona.upload"));
  });

  it("says an uploaded look is the user's own, and offers Delete only on it", async () => {
    const { t } = await draw();
    const card = radios().find((r) => r.getAttribute("aria-label") === "Doudou")!;
    expect(card.textContent).toContain(t("assistant-persona.uploaded"));
    expect(byLabel(t("assistant-persona.delete_label", { name: "Doudou" }))).not.toBeNull();
    expect(byLabel(t("assistant-persona.delete_label", { name: "Milk Frog" }))).toBeNull();
    expect(byLabel(t("assistant-persona.delete_label", { name: "Aurora" }))).toBeNull();
  });

  it("asks before deleting the current one, then deletes it and goes back to the default", async () => {
    const { t } = await draw();
    expect(checked()).toBe("Doudou");
    await act(async () => byLabel(t("assistant-persona.delete_label", { name: "Doudou" }))!.click());
    const dialog = document.querySelector('[role="dialog"]')!;
    expect(dialog.textContent).toContain(t("assistant-persona.delete_title", { name: "Doudou" }));
    expect(dialog.textContent).toContain(t("assistant-persona.delete_body_current"));
    expect(hub.deletes).toEqual([]);
    await act(async () => dialogButton(t("assistant-persona.delete")).click());
    expect(hub.deletes).toEqual(["/looks/uploads/u-demo0001"]);
    expect(radios().map((r) => r.getAttribute("aria-label"))).toEqual(["Mochi, the default avatar", "Milk Frog", "Aurora"]);
    expect(checked()).toBe("Mochi, the default avatar");
    expect(localStorage.getItem("persona.persona")).toBeNull();
    // the hub's pushes for it change nothing
    await act(async () => hub.listeners.get("look")!({ type: "look", id: "persona", effective: "persona" }));
    await act(async () => hub.listeners.get("personas")!({ type: "personas" }));
    expect(checked()).toBe("Mochi, the default avatar");
    expect(radios()).toHaveLength(3);
  });

  it("does not say the current one goes when it deletes another", async () => {
    hub.current = { id: "nwverse", effective: "nwverse" };
    const { t } = await draw();
    await act(async () => byLabel(t("assistant-persona.delete_label", { name: "Doudou" }))!.click());
    const dialog = document.querySelector('[role="dialog"]')!;
    expect(dialog.textContent).toContain(t("assistant-persona.delete_body"));
    expect(dialog.textContent).not.toContain(t("assistant-persona.delete_body_current"));
    await act(async () => dialogButton(t("assistant-persona.delete")).click());
    expect(checked()).toBe("Milk Frog");
  });

  it("shows the hub's refusal to delete as it is, and keeps the look", async () => {
    hub.refuseDelete = { status: 409, body: { error: "Only avatars made from an uploaded image can be deleted here", code: "not_uploaded" } };
    const { t } = await draw();
    await act(async () => byLabel(t("assistant-persona.delete_label", { name: "Doudou" }))!.click());
    await act(async () => dialogButton(t("assistant-persona.delete")).click());
    expect(document.querySelector('[role="dialog"] [role="alert"]')?.textContent).toBe("Only avatars made from an uploaded image can be deleted here");
    expect(radios().some((r) => r.getAttribute("aria-label") === "Doudou")).toBe(true);
    expect(checked()).toBe("Doudou");
  });

  it("lists the new look and chooses it as soon as the upload answers; the pushes after it change nothing", async () => {
    const { t } = await draw();
    await uploadBean(t);
    expect(hub.uploads).toEqual([{ name: "Bean", image: "Q1JPUA==", choose: true }]);
    // in the hub's order: its own packages, then the extensions'
    expect(radios().map((r) => r.getAttribute("aria-label"))).toEqual(["Mochi, the default avatar", "Milk Frog", "Doudou", "Bean", "Aurora"]);
    expect(checked()).toBe("Bean");
    expect(radios().find((r) => r.getAttribute("aria-label") === "Bean")!.textContent).toContain("A round one");
    expect(localStorage.getItem("persona.persona")).toBe("u-new00001");
    await act(async () => hub.listeners.get("look")!({ type: "look", id: "u-new00001", effective: "u-new00001" }));
    await act(async () => hub.listeners.get("personas")!({ type: "personas" }));
    expect(checked()).toBe("Bean");
    expect(radios().filter((r) => r.getAttribute("aria-label") === "Bean")).toHaveLength(1);
  });
  it("keeps the new look when a list read that left before the upload answers after it, even one waiting for files", async () => {
    const { t } = await draw();
    holdNextRead("u-demo0001");
    await act(async () => push("personas")); // a read on its way, older than the upload
    await uploadBean(t);
    expect(hub.held!.length).toBeGreaterThan(0); // that read still waits for Doudou's new files
    expect(checked()).toBe("Bean");
    await act(async () => push("personas")); // the upload's own push joins the read on its way
    await act(async () => push("look", { id: "u-new00001", effective: "u-new00001" }));
    await letImagesLoad();
    await settle();
    expect(radios().map((r) => r.getAttribute("aria-label"))).toEqual(["Mochi, the default avatar", "Milk Frog", "Doudou", "Bean", "Aurora"]);
    expect(checked()).toBe("Bean");
  });

  it("keeps a choice another device made while the upload's answer was on its way", async () => {
    const { t } = await draw();
    hub.meanwhile = async () => {
      // the hub pushes its own choice first, the page reads the list for the look it does not know yet, then another
      // device chooses the frog, all before the upload's answer comes
      push("look", { id: "u-new00001", effective: "u-new00001" });
      elsewhere("nwverse");
      await new Promise((r) => setTimeout(r, 20));
    };
    await uploadBean(t);
    await settle();
    expect(radios().some((r) => r.getAttribute("aria-label") === "Bean")).toBe(true);
    expect(checked()).toBe("Milk Frog");
    expect(localStorage.getItem("persona.persona")).toBe("nwverse");
  });

  it("keeps a choice another device made while the delete's answer was on its way", async () => {
    const { t } = await draw();
    hub.meanwhile = async () => {
      push("look", { id: "persona", effective: "persona" }); // the hub's own, for the deleted current one
      elsewhere("nwverse");
      await new Promise((r) => setTimeout(r, 20));
    };
    await act(async () => byLabel(t("assistant-persona.delete_label", { name: "Doudou" }))!.click());
    await act(async () => dialogButton(t("assistant-persona.delete")).click());
    await settle();
    expect(radios().map((r) => r.getAttribute("aria-label"))).toEqual(["Mochi, the default avatar", "Milk Frog", "Aurora"]);
    expect(checked()).toBe("Milk Frog");
  });

  it("reads the list again after a read already on its way when the hub no longer has the look", async () => {
    const { t } = await draw();
    holdNextRead("u-demo0001");
    await act(async () => push("personas")); // it left before another device deleted Doudou
    hub.looks = hub.looks.filter((l) => (l as LookView).id !== "u-demo0001");
    hub.current = { id: "persona", effective: "persona" };
    hub.refuseDelete = { status: 404, body: { error: "That avatar is gone", code: "not_found" } };
    await act(async () => byLabel(t("assistant-persona.delete_label", { name: "Doudou" }))!.click());
    await act(async () => dialogButton(t("assistant-persona.delete")).click());
    expect(document.querySelector('[role="dialog"] [role="alert"]')?.textContent).toBe("That avatar is gone");
    expect(hub.held!.length).toBeGreaterThan(0); // the old read still waits for Doudou's new files
    await act(async () => push("personas")); // the other device's delete, joining the read on its way
    await letImagesLoad();
    await settle();
    expect(radios().some((r) => r.getAttribute("aria-label") === "Doudou")).toBe(false);
  });
});
