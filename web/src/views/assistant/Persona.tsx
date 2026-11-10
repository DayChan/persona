// Persona's look in the page (avatar, docs/apple/persona.md, docs/extensions/extensions.md "Avatar"; the app's PersonaStore / PersonaViews /
// PersonaPicker): the looks the hub offers (GET /looks: the built-in ones, its packages and the extensions'), the one
// chosen — the user's, kept on the hub so every device shows it — and its face (a still avatar) and dot (the state's
// APNG, which the browser animates by itself). The default dumpling (MochiFace) is drawn locally and also shows while
// a chosen package is not there. An older hub without /looks gets the
// packages from /personas and the choice stays this browser's.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { api, ApiError, errorText, getToken } from "../../lib/api";
import {
  avatarFile, BUILTIN_PERSONA, dotFile, isBuiltinPersona, localized, lookPackages, normalizePersonaChoice, PERSONA_STATES, personaFileURL,
  personaFiles, usablePersonas, withChosenReady, type LookCurrent, type LookView, type PersonaInfo, type PersonaState,
} from "../../lib/persona";
import { isResumed } from "../../lib/reconnect";
import { hubHas, onHubEvent, useStore } from "../../lib/store";
import { deleteUploadedLook, LOOK_UPLOADS_FEATURE, refusalText, type UploadAnswer } from "../../lib/look-upload";
import { Button, cx, Dialog, IconButton } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { lazyDialog } from "../LazyOverlay";
import { MochiFace } from "./MochiFace";
import { useReducedMotion } from "./face-motion";
import { personaAccent, setPersonaAccent } from "../../ui/look";
import { currentLanguage, t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { BASE } from "../../lib/base";
import { navigate } from "../../lib/router";
import { settingsHref } from "../settings/nav";
import { setLookIcon } from "../../lib/look-icon";

// the upload dialog (picking, cropping, sending an image) loads on its first open, not with the persona page
const LookUploadDialog = lazyDialog(() => import("./LookUpload").then((m) => m.LookUploadDialog));

const CHOSEN_KEY = "persona.persona";

interface PersonaStoreState {
  list: PersonaInfo[];
  /** GET /looks's list; null on a hub without it. */
  looks: LookView[] | null;
  loaded: boolean;
  error: string;
  chosen: string;
}

let ps: PersonaStoreState = { list: [], looks: null, loaded: false, error: "", chosen: readChosen() };
const listeners = new Set<() => void>();
let loading: Promise<void> | null = null;
/** Whether the hub keeps the choice (it answered /looks): then choosing goes there. */
let synced = false;
/** Counts the choices made on this page and pushed from other devices: a list read that left before one keeps it. */
let choices = 0;
/** A choice made before the hub was known to keep it (no /looks answer yet): it is sent once one comes. */
let unsent = false;
/** Counts the looks uploaded and deleted from this page: a list read that left before one is older than what it shows. */
let edits = 0;

function readChosen() {
  try {
    const saved = localStorage.getItem(CHOSEN_KEY) || BUILTIN_PERSONA;
    const chosen = normalizePersonaChoice(saved);
    if (saved !== chosen) saveChosen(chosen);
    return chosen;
  } catch {
    return BUILTIN_PERSONA;
  }
}

/** Whether this browser chose a look itself before the hub kept the choice (what moves up to the hub). */
function choseHere(): string | null {
  try {
    return localStorage.getItem(CHOSEN_KEY);
  } catch {
    return null;
  }
}

function saveChosen(id: string) {
  try {
    if (id === BUILTIN_PERSONA) localStorage.removeItem(CHOSEN_KEY);
    else localStorage.setItem(CHOSEN_KEY, id);
  } catch {
    // private mode: this page only
  }
}

function setPS(patch: Partial<PersonaStoreState>) {
  ps = { ...ps, ...patch };
  // Theme color "Match avatar" (ui/look.ts) keeps the chosen persona's accent, from the last list the hub gave; while there is no
  // list yet (or the last load failed) and the chosen one is not in it, the saved accent stays.
  const shown = ps.list.find((p) => p.id === ps.chosen);
  if (isBuiltinPersona(ps.chosen)) setPersonaAccent(null);
  else if (shown || (ps.loaded && !ps.error)) setPersonaAccent(personaAccent(shown?.manifest.colors));
  setLookIcon(shown ?? null);
  listeners.forEach((l) => l());
}

/** The choice to keep given the hub's: the hub's once it has one; else this browser's moves up (once, if unset there). */
async function settleChoice(cur: LookCurrent): Promise<string> {
  if (cur.id) return normalizePersonaChoice(cur.id);
  // one chosen on this page before this answer is sent as it is once the answer is in (refreshPersonas)
  if (unsent) return BUILTIN_PERSONA;
  const mine = choseHere();
  if (!mine) return BUILTIN_PERSONA;
  try {
    const r = await api<LookCurrent>("/looks/current", { method: "PUT", json: { id: mine, if_unset: true } });
    return normalizePersonaChoice(r.id ?? BUILTIN_PERSONA);
  } catch {
    // the hub does not know it (the page's old mark, a package gone): the default
    return BUILTIN_PERSONA;
  }
}

/** What the hub offers now (each time the picker opens, once per page load, and when the hub says the list changed). */
export function refreshPersonas(): Promise<void> {
  if (loading) return loading;
  // the hub's `current` is its choice when the request left: one made since (here, or another device's push) stays
  const asked = choices;
  // a look uploaded or deleted here while this read is on its way: what it lists is older, and another read follows
  const edited = edits;
  let again = false;
  loading = api<{ looks: LookView[]; current: LookCurrent }>("/looks")
    .then(async (r) => {
      synced = true;
      return { list: usablePersonas(lookPackages(r.looks)), looks: r.looks, chosen: await settleChoice(r.current) };
    })
    .catch(async (e) => {
      if (!(e instanceof ApiError && e.status === 404)) throw e;
      synced = false; // an older hub: its packages, this browser's choice
      const r = await api<{ personas: PersonaInfo[] }>("/personas");
      return { list: usablePersonas(r.personas), looks: null, chosen: ps.chosen };
    })
    .then(async ({ list, looks, chosen: answered }) => {
      const pick = () => (choices === asked && !unsent ? answered : ps.chosen);
      const chosen = pick();
      const next = list.find((p) => p.id === chosen);
      const shown = ps.list.find((p) => p.id === chosen);
      const ready = !next || !shown || next.rev === shown.rev || (await preload(next));
      const now = pick(); // chosen again while the files loaded: that one was not waited for, it shows as it comes
      if (edits !== edited) {
        again = true;
        return;
      }
      if (now === answered) saveChosen(now);
      setPS({ list: withChosenReady(list, ps.list, now, now === chosen ? ready : true), looks, chosen: now, loaded: true, error: "" });
      // chosen before the hub was known to keep the choice: it goes now
      if (unsent) {
        unsent = false;
        if (synced) void api("/looks/current", { method: "PUT", json: { id: now } }).catch(() => undefined);
      }
    })
    .catch((e) => setPS({ loaded: true, error: e instanceof ApiError && e.status === 404 ? t("assistant-persona.hub_unsupported") : t("assistant-persona.load_failed", { error: errorText(e) }) }))
    .finally(() => {
      loading = null;
      if (again) void refreshPersonas();
    });
  return loading;
}

/** A read that leaves after now: one already on its way left before, so another follows it. */
function readPersonasAgain() {
  const again = () => void refreshPersonas();
  if (loading) loading.then(again, again);
  else again();
}

/** Every file of the package in the browser's cache before it is shown; false when one did not come. */
function preload(p: PersonaInfo): Promise<boolean> {
  const token = getToken();
  return Promise.all(
    personaFiles(p.manifest).map(
      (f) =>
        new Promise<boolean>((done) => {
          const img = new Image();
          img.onload = () => done(true);
          img.onerror = () => done(false);
          img.src = personaFileURL(p, f, token);
        }),
    ),
  ).then((all) => all.every(Boolean));
}

// The list changed (a tiered look moved, an extension with looks came or went): list again, the new files loaded first.
onHubEvent("personas", () => void refreshPersonas());
// A reconnect the hub could not resume: a `personas` or `look` push missed meanwhile is not replayed (#4954). The read
// takes the hub's choice as a `look` push would; a page that has not read the looks yet reads them when it shows one.
// A read already on its way left before the drop: another one follows it.
onHubEvent("hello", (msg) => {
  if (isResumed(msg) || !ps.loaded) return;
  readPersonasAgain();
});
// Another device chose a look (or the hub went back to the default when its extension went).
onHubEvent("look", (m) => {
  const pushed = typeof m.id === "string" ? m.id : "";
  if (!pushed) return;
  const id = normalizePersonaChoice(pushed);
  choices++; // the hub's choice now, even when this page already shows it: a list read that left before is older
  if (id === ps.chosen) return;
  saveChosen(id);
  if (!isBuiltinPersona(id) && !ps.list.some((p) => p.id === id)) void refreshPersonas();
  setPS({ chosen: id });
});

/** Choosing one keeps it when its package leaves the hub (the built-in shows meanwhile), as on the phone; on a hub that
 *  keeps the choice, every device follows. */
export function choosePersona(choice: string) {
  const id = normalizePersonaChoice(choice);
  choices++;
  saveChosen(id);
  setPS({ chosen: id });
  if (synced) void api("/looks/current", { method: "PUT", json: { id } }).catch(() => undefined);
  else unsent = true; // the list being read says whether this hub keeps it
}

/** Where the choices stand: taken as a request that makes the hub choose (an upload, a delete) leaves. */
function choiceMark(): number {
  return choices;
}

/**
 * The hub's choice in the answer to an upload or a delete that left at `since` (choiceMark): the newest, as a `look`
 * push would be, unless another choice came meanwhile (another device's push, or one made here) — the hub pushes its
 * own before it answers, so which of the two it kept last only the hub can say. Then the one on screen stays and a
 * fresh read settles it.
 */
function takeChoice(id: string, since: number) {
  if (choices !== since && id !== ps.chosen) {
    readPersonasAgain();
    return ps.chosen;
  }
  choices++;
  saveChosen(id);
  return id;
}

/**
 * A look the hub made from an uploaded image (#2404), shown at once with the choice the upload made (it left at
 * `since`, choiceMark): in the hub's order (its own packages, then the extensions'). Its `personas` and `look` pushes
 * come too and change nothing.
 */
function lookUploaded({ look, current }: UploadAnswer, since: number) {
  edits++;
  const theirs = (l: LookView) => l.source === "extension";
  let looks = ps.looks;
  if (looks) {
    const rest = looks.filter((l) => l.id !== look.id);
    const at = rest.findIndex(theirs);
    looks = at < 0 ? [...rest, look] : [...rest.slice(0, at), look, ...rest.slice(at)];
  }
  // the packages already listed stay as they are shown (one still loading keeps its old files, withChosenReady)
  const shown = new Map(ps.list.map((p) => [p.id, p]));
  const made = usablePersonas(lookPackages([look]));
  const list = looks ? usablePersonas(lookPackages(looks)).map((p) => (p.id === look.id ? p : (shown.get(p.id) ?? p))) : [...ps.list.filter((p) => p.id !== look.id), ...made];
  setPS(current ? { looks, list, chosen: takeChoice(normalizePersonaChoice(current.id ?? current.effective), since) } : { looks, list });
}

/**
 * An uploaded look deleted (#2404; the request left at `since`, choiceMark): gone from the list, and the hub's choice
 * after it (the default when it was the chosen one).
 */
function uploadedLookDeleted(id: string, current: LookCurrent | undefined, since: number) {
  edits++;
  const chosen = current?.id ? normalizePersonaChoice(current.id) : ps.chosen === id ? BUILTIN_PERSONA : ps.chosen;
  // the look on screen is the one deleted: nothing newer can name it any more, so the answer stands
  setPS({ looks: ps.looks?.filter((l) => l.id !== id) ?? null, list: ps.list.filter((p) => p.id !== id), chosen: takeChoice(chosen, ps.chosen === id ? choices : since) });
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

function usePersonaStore(): PersonaStoreState {
  const s = useSyncExternalStore(subscribe, () => ps);
  useEffect(() => {
    if (!ps.loaded && !loading) void refreshPersonas();
  }, []);
  return s;
}

export interface CurrentPersona {
  /** The package on screen; null: a built-in. */
  info: PersonaInfo | null;
  /** The built-in look on screen (the chosen one, or the default while a chosen package is not there); null with a package. */
  builtin: string | null;
  /** Mochi is on screen. */
  mochi: boolean;
  name: string;
  chosen: string;
}

/** The look on screen: the chosen package once the hub listed it, else the built-in (named Persona). */
export function usePersona(): CurrentPersona {
  const s = usePersonaStore();
  const info = isBuiltinPersona(s.chosen) ? null : (s.list.find((p) => p.id === s.chosen) ?? null);
  const builtin = info ? null : isBuiltinPersona(s.chosen) ? s.chosen : BUILTIN_PERSONA;
  const name = info ? localized(info.manifest.name, info.manifest.names, currentLanguage()) : "Persona";
  return { info, builtin, mochi: builtin === BUILTIN_PERSONA, name, chosen: s.chosen };
}

/** Whether the page is dark now (the theme's class on <html>), for the dark avatar. */
export function useDarkPage(): boolean {
  const read = () => typeof document !== "undefined" && document.documentElement.classList.contains("dark");
  const [dark, setDark] = useState(read);
  useEffect(() => {
    if (typeof MutationObserver === "undefined") return;
    const o = new MutationObserver(() => setDark(read()));
    o.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    return () => o.disconnect();
  }, []);
  return dark;
}

/**
 * A still face in a circle: the package's avatar at the size it needs (its SVG, its dark one on a dark page), else the
 * built-in look on screen. With `persona` given (a card in the picker), that package's, or `fallback` when its file
 * does not load.
 */
export function PersonaAvatar({ size, persona, fallback, className }: { size: number; persona?: PersonaInfo | null; fallback?: React.ReactNode; className?: string }) {
  const current = usePersona();
  const dark = useDarkPage();
  const p = persona === undefined ? current.info : persona;
  const [broken, setBroken] = useState(false);
  const file = p ? avatarFile(p.manifest, size * Math.max(2, Math.ceil(typeof devicePixelRatio === "number" ? devicePixelRatio : 2)), dark) : undefined;
  useEffect(() => setBroken(false), [p?.id, p?.rev]);
  if (!p || !file || broken) return persona === undefined ? <MochiFace size={size} className={className} /> : <>{fallback}</>;
  return (
    <img
      src={personaFileURL(p, file, getToken())}
      alt=""
      aria-hidden
      draggable={false}
      onError={() => setBroken(true)}
      width={size}
      height={size}
      className={cx("shrink-0 rounded-full object-cover", className)}
      style={{ width: size, height: size }}
    />
  );
}

/**
 * The face moving for a state (the top bar's dot): the state's frames, looping. With reduced motion only the first frame,
 * which says the state by itself (a canvas draws an animated image's first frame). The built-in look drawn in that
 * state when the look on screen is one; `fallback` for a given package whose frames do not load.
 */
export function PersonaDot({ size, state, persona, fallback, className }: { size: number; state: PersonaState; persona?: PersonaInfo | null; fallback?: React.ReactNode; className?: string }) {
  const current = usePersona();
  const p = persona === undefined ? current.info : persona;
  const still = useReducedMotion();
  const [broken, setBroken] = useState(false);
  const file = p ? dotFile(p.manifest, state) : undefined;
  const url = p && file ? personaFileURL(p, file, getToken()) : "";
  useEffect(() => setBroken(false), [url]);
  if (!p || !file || broken) return persona === undefined ? <MochiFace size={size} state={state} className={className} /> : <>{fallback}</>;
  if (still) return <StillFrame url={url} size={size} className={className} onError={() => setBroken(true)} />;
  return (
    <img
      src={url}
      alt=""
      aria-hidden
      draggable={false}
      onError={() => setBroken(true)}
      width={size}
      height={size}
      className={cx("shrink-0 object-contain", className)}
      style={{ width: size, height: size }}
    />
  );
}

function StillFrame({ url, size, className, onError }: { url: string; size: number; className?: string; onError: () => void }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const img = new Image();
    let live = true;
    img.onload = () => {
      const c = ref.current;
      if (!live || !c) return;
      const k = Math.max(1, Math.ceil(typeof devicePixelRatio === "number" ? devicePixelRatio : 1));
      c.width = size * k;
      c.height = size * k;
      const g = c.getContext("2d");
      g?.clearRect(0, 0, c.width, c.height);
      g?.drawImage(img, 0, 0, c.width, c.height);
    };
    img.onerror = () => live && onError();
    img.src = url;
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url, size]);
  return <canvas ref={ref} aria-hidden className={cx("shrink-0", className)} style={{ width: size, height: size }} />;
}

/** The palette's colors as small dots (a card in the picker); they are the package's own, data rather than page tokens. */
function Swatches({ p }: { p: PersonaInfo }) {
  const c = p.manifest.colors?.light ?? p.manifest.colors?.dark;
  const keys = ["primary", "secondary", "accent", "bubble"].filter((k) => c?.[k]);
  if (!keys.length) return <span className="text-[11px] text-ink-3">{t("assistant-persona.no_colors")}</span>;
  return (
    <span className="flex items-center gap-1" aria-hidden>
      {keys.map((k) => (
        <span key={k} className="size-3 rounded-full shadow-hairline" style={{ background: c![k] }} />
      ))}
    </span>
  );
}

/**
 * Avatar (Settings › General and the profile page; the app's Settings › Appearance › Avatar): the one setting for Persona's face, name, the
 * page's mark and icon (#3200): the default dumpling, then the hub's packages (including the bundled frog) and the
 * extensions' looks, and the chosen one's dot in each state. On a hub that keeps the choice every device follows it; the
 * page's colors follow it only with theme color "Match avatar" (ui/look.ts).
 */
/**
 * What the picker says when the hub has no looks beyond the built-in ones: where to get one, with Settings › Extensions as a link
 * (#3597). onExtensions opens it in place (Settings keeps its own tab state); without it the link goes to the page.
 */
export function NoOtherLooks({ onExtensions }: { onExtensions?: () => void }) {
  const to = settingsHref("extensions", null, false);
  const open = (e: React.MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    if (onExtensions) onExtensions();
    else navigate(to);
  };
  return (
    <p className="px-1 text-[11.5px] leading-relaxed text-ink-3">
      {tRich("assistant-persona.none", {
        extensions: (
          <a href={BASE + to} onClick={open} className="text-accent hover:underline">
            {t("assistant-persona.none_extensions")}
          </a>
        ),
      })}
    </p>
  );
}

export function PersonaPicker({ onExtensions }: { onExtensions?: () => void } = {}) {
  const s = usePersonaStore();
  const current = usePersona();
  const lang = currentLanguage();
  // making avatars from images (#2404): only a hub that says it can
  const uploads = useStore((st) => hubHas(st.features, LOOK_UPLOADS_FEATURE));
  const [refreshing, setRefreshing] = useState(false);
  // the upload dialog, a new one each time it opens (`key`)
  const [upload, setUpload] = useState({ open: false, key: 0 });
  const [deleting, setDeleting] = useState<{ id: string; name: string; current: boolean } | null>(null);
  // where the choices stood as the upload left (takeChoice)
  const uploadSent = useRef(0);
  const grid = useRef<HTMLDivElement>(null);
  const load = () => {
    setRefreshing(true);
    void refreshPersonas().finally(() => setRefreshing(false));
  };
  useEffect(load, []);
  const sub = (text: string) => <span className="max-w-full truncate text-[11px] whitespace-nowrap text-ink-3">{text}</span>;
  const lookOf = (id: string) => s.looks?.find((l) => l.id === id);
  const cards: { id: string; title: string; label: string; sub: React.ReactNode; face: React.ReactNode; uploaded?: boolean }[] = [
    { id: BUILTIN_PERSONA, title: t("assistant-persona.mochi"), label: t("assistant-persona.mochi_default"), sub: sub(t("assistant-persona.default_avatar")), face: <MochiFace size={56} /> },
    ...s.list.map((p) => {
      const title = localized(p.manifest.name, p.manifest.names, lang);
      const look = lookOf(p.id);
      const from = look?.extension_name ?? p.manifest.source?.extension_name;
      // one made from an image says what the user wrote under it, else that it is theirs (it has no colors to show)
      if (look?.uploaded) {
        const about = p.manifest.subtitle ? localized(p.manifest.subtitle, p.manifest.subtitles, lang) : "";
        return { id: p.id, title, label: title, uploaded: true, sub: sub(about || t("assistant-persona.uploaded")), face: <PersonaAvatar size={56} persona={p} fallback={<MochiFace size={56} />} /> };
      }
      return {
        id: p.id,
        title,
        label: title,
        sub: p.manifest.about ? sub(p.manifest.about) : from ? sub(t("assistant-persona.from_extension", { name: from })) : <Swatches p={p} />,
        face: <PersonaAvatar size={56} persona={p} fallback={<MochiFace size={56} />} />,
      };
    }),
  ];
  const missing = !isBuiltinPersona(s.chosen) && s.loaded && !s.list.some((p) => p.id === s.chosen);
  const shownId = current.info?.id ?? BUILTIN_PERSONA;
  return (
    <div className="flex flex-col gap-3 p-3">
      <div ref={grid} role="radiogroup" aria-label={t("assistant-persona.avatar")} className="grid grid-cols-2 gap-2.5 sm:grid-cols-3">
        {cards.map((c) => {
          const selected = c.id === shownId;
          const card = (
            <button
              key={c.id}
              type="button"
              role="radio"
              aria-checked={selected}
              aria-label={c.label}
              onClick={() => choosePersona(c.id)}
              className={cx(
                "flex min-w-0 flex-col items-center gap-2 rounded-card px-2 pt-3.5 pb-2.5 transition-[background-color,box-shadow] duration-150",
                c.uploaded && uploads && "flex-1",
                selected ? "bg-accent-tint shadow-[inset_0_0_0_1.5px_var(--accent)]" : "bg-inset shadow-hairline hover:bg-hover",
              )}
            >
              <span className="flex h-14 items-center justify-center">{c.face}</span>
              <span className="flex max-w-full items-center gap-1 text-[12.5px] font-medium whitespace-nowrap text-ink">
                {selected && <Icon name="check" size={12} stroke={2.6} className="shrink-0 text-accent-ink" />}
                <span className="truncate">{c.title}</span>
              </span>
              {c.sub}
            </button>
          );
          if (!c.uploaded || !uploads) return card;
          // Delete sits in the card's corner: shown on hover and keyboard focus, always on touch screens.
          return (
            <div key={c.id} className="group/look relative flex min-w-0">
              {card}
              <span className="absolute top-1 right-1 opacity-0 transition-opacity duration-150 group-hover/look:opacity-100 focus-within:opacity-100 [@media(hover:none)]:opacity-100">
                <IconButton icon="trash" label={t("assistant-persona.delete_label", { name: c.title })} onClick={() => setDeleting({ id: c.id, name: c.title, current: selected })} />
              </span>
            </div>
          );
        })}
        {uploads && (
          <button
            type="button"
            aria-label={t("assistant-persona.upload_label")}
            onClick={() => setUpload((u) => ({ open: true, key: u.key + 1 }))}
            className="flex min-w-0 flex-col items-center gap-2 rounded-card bg-inset px-2 pt-3.5 pb-2.5 shadow-hairline transition-[background-color,box-shadow] duration-150 hover:bg-hover"
          >
            <span className="flex h-14 items-center justify-center">
              <span className="flex size-14 items-center justify-center rounded-full border border-dashed border-line-strong text-ink-3">
                <Icon name="plus" size={22} />
              </span>
            </span>
            <span className="max-w-full truncate text-[12.5px] font-medium whitespace-nowrap text-ink">{t("assistant-persona.upload")}</span>
            {sub(t("assistant-persona.upload_sub"))}
          </button>
        )}
      </div>
      <div className="grid grid-cols-5 gap-1 rounded-card bg-inset py-2.5 shadow-hairline" aria-label={t("assistant-persona.states_label", { name: current.name })} role="img">
        {PERSONA_STATES.map((st) => (
          <span key={st.id} className="flex flex-col items-center gap-1">
            <PersonaDot size={40} state={st.id} />
            <span className="text-[11px] whitespace-nowrap text-ink-3">{st.title}</span>
          </span>
        ))}
      </div>
      {missing && <p className="px-1 text-[11.5px] text-ink-3">{t("assistant-persona.missing")}</p>}
      {s.error && <div className="flex items-center gap-3 px-1">
        <p role="alert" className="min-w-0 flex-1 text-[11.5px] break-words text-orange">{s.error}</p>
        <Button icon="retry" onClick={load} busy={refreshing}>{t("assistant-persona.retry")}</Button>
      </div>}
      {s.loaded && !s.error && !s.list.length && <NoOtherLooks onExtensions={onExtensions} />}
      {uploads && (
        <LookUploadDialog
          key={upload.key}
          open={upload.open}
          onClose={() => setUpload((u) => ({ ...u, open: false }))}
          onSend={() => (uploadSent.current = choiceMark())}
          onUploaded={(answer) => lookUploaded(answer, uploadSent.current)}
        />
      )}
      <DeleteLookDialog
        look={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={() => {
          setDeleting(null);
          // the card that opened it is gone: the focus goes to the avatar now chosen (unless it moved on meanwhile)
          requestAnimationFrame(() => {
            if (document.activeElement && document.activeElement !== document.body) return;
            grid.current?.querySelector<HTMLElement>('[role="radio"][aria-checked="true"]')?.focus({ preventScroll: true });
          });
        }}
      />
    </div>
  );
}

/** Asks before deleting an uploaded look: it goes from every device, and the current one gives way to the default. */
function DeleteLookDialog({ look, onClose, onDeleted }: { look: { id: string; name: string; current: boolean } | null; onClose: () => void; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [was, setWas] = useState(look);
  if (look && look !== was) {
    // another look (or the same one again): it starts over
    setWas(look);
    setBusy(false);
    setError("");
  }
  const remove = async () => {
    if (!look || busy) return;
    setBusy(true);
    setError("");
    const since = choiceMark();
    try {
      uploadedLookDeleted(look.id, await deleteUploadedLook(look.id), since);
      setBusy(false);
      onDeleted();
    } catch (e) {
      setBusy(false);
      setError(refusalText(e));
      // gone already (another device deleted it): a list read after this says so (one on its way may be older)
      if (e instanceof ApiError && e.code === "not_found") readPersonasAgain();
    }
  };
  const shown = look ?? was;
  return (
    <Dialog
      open={!!look}
      onClose={onClose}
      title={shown ? t("assistant-persona.delete_title", { name: shown.name }) : undefined}
      width={400}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="danger" icon="trash" busy={busy} onClick={() => void remove()}>{t("assistant-persona.delete")}</Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{t(shown?.current ? "assistant-persona.delete_body_current" : "assistant-persona.delete_body")}</p>
      {error && <p role="alert" className="mt-2 text-[12px] leading-relaxed break-words text-red">{error}</p>}
    </Dialog>
  );
}
